import { describe, expect, it } from 'vitest'
import { createMockServer, MOCK_ADMIN } from './server.ts'

// The mock's mail, registration, email verification and password reset: the contract of docs/api.md, and the one
// promise the mock makes about mail: it never sends.

function world() {
  let clock = Date.parse('2026-11-23T08:00:00Z')
  const server = createMockServer({ empty: true, mode: 'accounts', now: () => clock, oidc: null })
  const req = (method: string, path: string, body?: unknown, cookie = '') => {
    const r = server.handle(
      method,
      `/api${path}`,
      body === undefined ? undefined : JSON.stringify(body),
      'application/json',
      cookie,
    )
    const set = r.headers?.['Set-Cookie']
    return { status: r.status, body: (r.body ?? {}) as any, cookie: set ? set.split(';')[0] : '' }
  }
  const login = (username: string, password: string) => req('POST', '/login', { username, password }).cookie
  const root = login(MOCK_ADMIN.username, MOCK_ADMIN.password)
  const enable = () =>
    expect(
      req('PUT', '/admin/mail', { enabled: true, host: 'smtp.example.test', from: 'notes@example.test' }, root).status,
    ).toBe(200)
  const open = () => {
    enable()
    expect(req('PUT', '/admin/settings/features.registration', { value: true }, root).status).toBe(200)
  }
  return { server, req, login, root, enable, open, advance: (ms: number) => (clock += ms) }
}
const tokenOf = (link?: string) => link!.split('#')[1]

describe('the mock mail', () => {
  it('is off by default: nothing is sent, nothing opens, the sign-in page links to nothing', () => {
    const w = world()
    expect(w.req('GET', '/admin/mail', undefined, w.root).body).toMatchObject({
      enabled: false,
      security: 'starttls',
      hasPassword: false,
      status: { ok: false, checked: null },
    })
    expect(w.req('GET', '/auth/providers').body).toMatchObject({ register: false, forgot: false })
    expect(w.req('GET', '/register').body).toEqual({ open: false })
    expect(w.req('POST', '/password/forgot', { login: 'admin' }).body.error).toBe('password_reset_off')
    expect(w.req('PUT', '/admin/settings/features.registration', { value: true }, w.root).body.error).toBe(
      'mail_not_configured',
    )
    expect(w.server.mail.outbox).toHaveLength(0)
  })

  it('checks the settings before storing, seals the password, and sends nothing even for the test message', () => {
    const w = world()
    const put = (b: Record<string, unknown>) => w.req('PUT', '/admin/mail', b, w.root)
    expect(put({ security: 'none', host: 'mail.example.test' }).body.error).toBe('mail_insecure')
    expect(put({ enabled: true, host: '127.0.0.1' }).body.error).toBe('mail_from_required')
    expect(put({ port: 0 }).body.error).toBe('invalid')
    expect(put({ host: 'localhost', security: 'none', password: 'secret-pass', from: 'a@example.test' }).status).toBe(
      200,
    )
    const view = w.req('GET', '/admin/mail', undefined, w.root)
    expect(view.body.hasPassword).toBe(true)
    expect(JSON.stringify(view.body)).not.toContain('secret-pass')
    expect(w.req('POST', '/admin/mail/test', { to: 'admin@example.test' }, w.root).body).toEqual({ ok: true })
    expect(w.server.mail.outbox.map((m) => [m.kind, m.to])).toEqual([['test', 'admin@example.test']])
    expect(w.req('GET', '/admin/mail', undefined, w.root).body.status).toMatchObject({ ok: true })
    const member = w.req('POST', '/invites', { role: 'member' }, w.root)
    expect(member.status).toBe(201)
  })

  it('registration: the link makes the account, once; a pending one holds its username; closing cancels it', () => {
    const w = world()
    w.open()
    expect(w.req('GET', '/register').body).toMatchObject({
      open: true,
      tenant: { slug: 'default' },
      password: { minLength: 8 },
    })
    expect(w.req('GET', '/auth/providers').body).toMatchObject({ register: true, forgot: true })
    const body = { username: 'tang', name: 'Tang', email: 'tang@example.test', password: 'a long password' }
    expect(w.req('POST', '/register', body).status).toBe(202)
    expect(w.req('POST', '/register', { ...body, email: 'other@example.test' }).body.error).toBe('exists')
    const sent = w.server.mail.outbox.filter((m) => m.kind === 'register')
    expect(sent).toHaveLength(1)
    const made = w.req('POST', '/register/confirm', { token: tokenOf(sent[0].link) })
    expect(made.status).toBe(201)
    expect(made.body.user).toMatchObject({
      username: 'tang',
      emailVerified: true,
      role: 'member',
      email: 'tang@example.test',
    })
    expect(w.req('GET', '/account', undefined, made.cookie).status).toBe(200)
    expect(w.req('POST', '/register/confirm', { token: tokenOf(sent[0].link) }).status).toBe(404)
    // A second one, then the switch goes off.
    expect(w.req('POST', '/register', { ...body, username: 'second', email: 'second@example.test' }).status).toBe(202)
    const second = w.server.mail.outbox.filter((m) => m.kind === 'register')[1]
    expect(w.req('PUT', '/admin/settings/features.registration', { value: false }, w.root).status).toBe(200)
    expect(w.req('POST', '/register/confirm', { token: tokenOf(second.link) }).status).toBe(404)
    expect(w.req('POST', '/register', body).body.error).toBe('registration_closed')
  })

  it('registration refusals: fields, domain, terms, the same answer for an address that has an account, and the hourly cap', () => {
    const w = world()
    w.open()
    const body = { username: 'tang', name: 'Tang', email: 'tang@example.test', password: 'a long password' }
    expect(w.req('POST', '/register', { ...body, username: '!' }).body.error).toBe('invalid')
    expect(w.req('POST', '/register', { ...body, email: 'nope' }).body.error).toBe('invalid')
    w.req('PUT', '/admin/settings/registration.domains', { value: ['example.test'] }, w.root)
    expect(w.req('POST', '/register', { ...body, email: 'a@other.test' }).body.error).toBe('email_domain_not_allowed')
    w.req('PUT', '/admin/settings/legal.termsUrl', { value: 'https://notes.example.test/terms' }, w.root)
    expect(w.req('POST', '/register', body).body.error).toBe('terms_required')
    expect(w.req('POST', '/register', { ...body, acceptTerms: true }).status).toBe(202)
    // An address with a verified account: the same 202, and no registration.
    const admin = w.req('GET', '/session', undefined, w.root).body.user
    expect(
      w.req('PATCH', `/users/${admin.id}`, { email: 'admin@example.test', emailVerified: true }, w.root).status,
    ).toBe(200)
    const before = w.server.mail.pendingCount()
    expect(
      w.req('POST', '/register', { ...body, username: 'other', email: 'admin@example.test', acceptTerms: true }).status,
    ).toBe(202)
    expect(w.server.mail.pendingCount()).toBe(before)
    w.req('PUT', '/admin/settings/registration.perHour', { value: 2 }, w.root)
    w.req('POST', '/register', { ...body, username: 'third', email: 'third@example.test', acceptTerms: true })
    expect(
      w.req('POST', '/register', { ...body, username: 'fourth', email: 'fourth@example.test', acceptTerms: true })
        .status,
    ).toBe(429)
  })

  it('ssoOnly and open registration exclude each other, in both directions', () => {
    const w = world()
    w.enable()
    expect(w.req('PUT', '/admin/security', { login: { ssoOnly: true } }, w.root).status).toBe(200)
    expect(w.req('PUT', '/admin/settings/features.registration', { value: true }, w.root).body.error).toBe(
      'sso_only_with_registration',
    )
    w.req('PUT', '/admin/security', { login: { ssoOnly: false } }, w.root)
    expect(w.req('PUT', '/admin/settings/features.registration', { value: true }, w.root).status).toBe(200)
    expect(w.req('PUT', '/admin/security', { login: { ssoOnly: true } }, w.root).body.error).toBe(
      'sso_only_with_registration',
    )
  })

  it('email verification: a link on another device, once, only for the address it was sent to', () => {
    const w = world()
    w.enable()
    const me = w.req('POST', '/invites', { role: 'member' }, w.root).body
    const joined = w.req('POST', '/invites/accept', { token: me.token, username: 'tang', password: 'a long password' })
    expect(w.req('POST', '/account/email/verify', {}, joined.cookie).body.error).toBe('invalid')
    w.req('PATCH', '/account', { email: 'tang@example.test' }, joined.cookie)
    expect(w.req('POST', '/account/email/verify', {}, joined.cookie).status).toBe(202)
    const link = w.server.mail.outbox.find((m) => m.kind === 'verify')!.link
    expect(w.req('POST', '/email/confirm', { token: 'x' }).status).toBe(404)
    expect(w.req('POST', '/email/confirm', { token: tokenOf(link) }).status).toBe(204)
    expect(w.req('GET', '/account', undefined, joined.cookie).body.user.emailVerified).toBe(true)
    expect(w.req('POST', '/email/confirm', { token: tokenOf(link) }).status).toBe(404)
    expect(w.req('POST', '/account/email/verify', {}, joined.cookie).body.error).toBe('email_verified')
    // Changing a verified address tells the old one, with no link.
    w.req('PATCH', '/account', { email: 'new@example.test' }, joined.cookie)
    const note = w.server.mail.outbox.find((m) => m.kind === 'email_changed')!
    expect(note.to).toBe('tang@example.test')
    expect(note.link).toBeUndefined()
  })

  it('password reset: a link that works once, ends every session, never reveals an account, and is capped', () => {
    const w = world()
    w.enable()
    const inv = w.req('POST', '/invites', { role: 'member' }, w.root).body
    const joined = w.req('POST', '/invites/accept', { token: inv.token, username: 'tang', password: 'a long password' })
    w.req('PATCH', '/account', { email: 'tang@example.test' }, joined.cookie)
    w.req('PATCH', '/users/' + joined.body.user.id, { emailVerified: true }, w.root)
    const same = w.req('POST', '/password/forgot', { login: 'nobody' })
    expect(same.status).toBe(202)
    expect(w.server.mail.outbox).toHaveLength(0)
    expect(w.req('POST', '/password/forgot', { login: 'tang' }).body).toEqual(same.body)
    const link = w.server.mail.outbox.find((m) => m.kind === 'reset')!.link
    expect(w.req('POST', '/password/reset/check', { token: tokenOf(link) }).body.username).toBe('tang')
    expect(w.req('POST', '/password/reset', { token: tokenOf(link), password: 'short' }).body.error).toBe('invalid')
    expect(w.req('POST', '/password/reset', { token: tokenOf(link), password: 'a brand new password' }).status).toBe(
      204,
    )
    expect(w.req('POST', '/password/reset', { token: tokenOf(link), password: 'a brand new password' }).status).toBe(
      404,
    )
    expect(w.req('GET', '/account', undefined, joined.cookie).status).toBe(401)
    expect(w.req('POST', '/login', { username: 'tang', password: 'a brand new password' }).status).toBe(200)
    // Three an hour to one address.
    for (let i = 0; i < 4; i++) w.req('POST', '/password/forgot', { login: 'tang@example.test' })
    expect(w.server.mail.outbox.filter((m) => m.kind === 'reset' && m.to === 'tang@example.test')).toHaveLength(3)
  })

  it('an administrator sends a reset link to a verified address only', () => {
    const w = world()
    w.enable()
    const inv = w.req('POST', '/invites', { role: 'member' }, w.root).body
    const joined = w.req('POST', '/invites/accept', { token: inv.token, username: 'tang', password: 'a long password' })
    const id = joined.body.user.id
    expect(w.req('POST', `/users/${id}/password-reset`, {}, w.root).body.error).toBe('email_unverified')
    w.req('PATCH', `/users/${id}`, { email: 'tang@example.test', emailVerified: true }, w.root)
    expect(w.req('POST', `/users/${id}/password-reset`, {}, w.root).status).toBe(202)
    expect(w.req('POST', `/users/${id}/password-reset`, {}, joined.cookie).status).toBe(403)
    w.req('PUT', '/admin/mail', { enabled: false }, w.root)
    expect(w.req('POST', `/users/${id}/password-reset`, {}, w.root).body.error).toBe('mail_not_configured')
  })
})
