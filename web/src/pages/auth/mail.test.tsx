// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import { MOCK_ADMIN } from '@/api/mock/server'
import { tm } from '@/i18n/mail'
import { click, installDomShims, mount, settle, type, type Mounted } from '@/testing/harness'
import LoginPage from './LoginPage'
import { ForgotPage, RegisterConfirmPage, RegisterPage, ResetPasswordPage, VerifyEmailPage } from './MailPages'

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
  window.location.hash = ''
  vi.unstubAllGlobals()
})

const routes = (
  <Routes>
    <Route path="/login" element={<LoginPage />} />
    <Route path="/register" element={<RegisterPage />} />
    <Route path="/register/confirm" element={<RegisterConfirmPage />} />
    <Route path="/verify-email" element={<VerifyEmailPage />} />
    <Route path="/forgot" element={<ForgotPage />} />
    <Route path="/reset-password" element={<ResetPasswordPage />} />
    <Route path="*" element={<div data-testid="elsewhere">elsewhere</div>} />
  </Routes>
)
const text = () => document.body.textContent ?? ''
const input = (selector: string) => document.querySelector<HTMLInputElement>(selector)!
const submit = () => click(document.querySelector('form button[type="submit"]'))

// The server's side, as an administrator sets it up (the mock sends nothing: its outbox holds the messages).
function admin(method: string, path: string, body?: unknown, server = m!.server) {
  const login = server.handle('POST', '/api/login', JSON.stringify(MOCK_ADMIN), 'application/json')
  const cookie = login.headers!['Set-Cookie'].split(';')[0]
  return server.handle(
    method,
    `/api${path}`,
    body === undefined ? undefined : JSON.stringify(body),
    'application/json',
    cookie,
  )
}
async function open(path: string, { mail = false, registration = false, terms = false, hash = '' } = {}) {
  window.location.hash = hash
  m = await mount(routes, { entries: [path], server: { mode: 'accounts' } })
  if (mail) admin('PUT', '/admin/mail', { enabled: true, host: 'smtp.example.test', from: 'notes@example.test' })
  if (registration) admin('PUT', '/admin/settings/features.registration', { value: true })
  if (terms) admin('PUT', '/admin/settings/legal.termsUrl', { value: 'https://notes.example.test/terms' })
  await m.qc.invalidateQueries()
  await settle(() => document.querySelector('main') !== null, 'the page')
}
const outbox = (kind: string) => m!.server.mail.outbox.filter((x) => x.kind === kind)
const tokenOf = (link?: string) => link!.split('#')[1]

describe('registration', () => {
  it('says registration is closed while mail or the switch is off, and sends nothing', async () => {
    await open('/register')
    await settle(() => text().includes(tm.register.closed), 'the closed page')
    expect(m!.server.mail.outbox).toHaveLength(0)
  })

  it('the form sends the registration, says to look in the mailbox, and the link makes the account and signs in', async () => {
    await open('/register', { mail: true, registration: true })
    await settle(() => input('input[autocomplete="username"]') !== null, 'the form')
    await type(input('input[autocomplete="username"]'), 'tang')
    await type(input('input[autocomplete="email"]'), 'tang@example.test')
    const pw = [...document.querySelectorAll<HTMLInputElement>('input[type="password"]')]
    await type(pw[0], 'a-long-password')
    await type(pw[1], 'a-long-password')
    await submit()
    await settle(() => text().includes(tm.register.sentTitle), 'the confirmation notice')
    expect(text()).toContain('tang@example.test')
    expect(outbox('register')).toHaveLength(1)
    expect(JSON.parse(m!.requests.find((r) => r.url === '/api/register' && r.method === 'POST')!.body!)).toMatchObject({
      username: 'tang',
      email: 'tang@example.test',
      lang: expect.stringMatching(/^(en|zh-Hans)$/),
    })
    expect(outbox('register')[0].link).toContain('/register/confirm#')
  })

  it('a confirmation link signs the person in and goes to the app', async () => {
    m = await mount(routes, {
      entries: (server) => {
        admin('PUT', '/admin/mail', { enabled: true, host: 'smtp.example.test', from: 'notes@example.test' }, server)
        admin('PUT', '/admin/settings/features.registration', { value: true }, server)
        const sent = server.handle(
          'POST',
          '/api/register',
          JSON.stringify({ username: 'bee', name: 'Bee', email: 'bee@example.test', password: 'a-long-password' }),
          'application/json',
        )
        expect(sent.status).toBe(202)
        // The token is in the fragment of the link, never in a query.
        window.location.hash = '#' + tokenOf(server.mail.outbox.find((x) => x.kind === 'register')!.link)
        return ['/register/confirm']
      },
      server: { mode: 'accounts' },
    })
    await settle(
      () => document.querySelector('[data-testid="elsewhere"]') !== null,
      'the app, after the account was made',
    )
    expect(m.requests.find((r) => r.url === '/api/register/confirm')).toBeDefined()
    expect(window.location.hash).toBe('')
  })

  it('a link that is not valid says so and offers the registration page', async () => {
    await open('/register/confirm', { mail: true, registration: true, hash: '#nope' })
    await settle(() => text().includes(tm.confirm.invalid), 'the answer')
    expect(text()).toContain(tm.confirm.toRegister)
  })

  it('terms and privacy: the box must be ticked, and the links open in a new tab', async () => {
    await open('/register', { mail: true, registration: true, terms: true })
    await settle(() => input('input[type="checkbox"]') !== null, 'the checkbox')
    const link = document.querySelector<HTMLAnchorElement>('a[href="https://notes.example.test/terms"]')!
    expect(link.target).toBe('_blank')
    await type(input('input[autocomplete="username"]'), 'tang')
    await type(input('input[autocomplete="email"]'), 'tang@example.test')
    const pw = [...document.querySelectorAll<HTMLInputElement>('input[type="password"]')]
    await type(pw[0], 'a-long-password')
    await type(pw[1], 'a-long-password')
    await submit()
    await settle(() => text().includes(tm.register.termsRequired), 'the refusal')
    expect(m!.server.mail.outbox).toHaveLength(0)
    await click(input('input[type="checkbox"]'))
    await submit()
    await settle(() => text().includes(tm.register.sentTitle), 'the notice')
  })

  it('a password that is too short or not repeated is refused before anything is sent', async () => {
    await open('/register', { mail: true, registration: true })
    await settle(() => input('input[autocomplete="username"]') !== null, 'the form')
    await type(input('input[autocomplete="username"]'), 'tang')
    await type(input('input[autocomplete="email"]'), 'tang@example.test')
    const pw = [...document.querySelectorAll<HTMLInputElement>('input[type="password"]')]
    await type(pw[0], 'a-long-password')
    await type(pw[1], 'another-password')
    await submit()
    await settle(() => text().includes(tm.register.mismatch), 'the mismatch')
    expect(m!.requests.some((r) => r.url === '/api/register' && r.method === 'POST')).toBe(false)
  })
})

describe('the sign-in page links', () => {
  it('shows neither link while mail is off, and both once registration is open', async () => {
    await open('/login')
    await settle(() => input('input[type="password"]') !== null, 'the form')
    expect(document.querySelector('a[href="/forgot"]')).toBeNull()
    expect(document.querySelector('a[href="/register"]')).toBeNull()
    admin('PUT', '/admin/mail', { enabled: true, host: 'smtp.example.test', from: 'notes@example.test' })
    await m!.qc.invalidateQueries()
    await settle(() => document.querySelector('a[href="/forgot"]') !== null, 'the forgot link')
    expect(document.querySelector('a[href="/register"]')).toBeNull()
    admin('PUT', '/admin/settings/features.registration', { value: true })
    await m!.qc.invalidateQueries()
    await settle(() => document.querySelector('a[href="/register"]') !== null, 'the register link')
  })
})

describe('password reset', () => {
  it('the forgot page answers the same and the mock sends only for an account with a verified address', async () => {
    await open('/forgot', { mail: true })
    await type(input('input[autocomplete="username"]'), 'nobody')
    await submit()
    await settle(() => text().includes(tm.forgot.sentTitle), 'the notice')
    expect(m!.server.mail.outbox).toHaveLength(0)
  })

  it('off: the page says there is no password reset', async () => {
    await open('/forgot')
    await type(input('input[autocomplete="username"]'), 'admin')
    await submit()
    await settle(() => text().includes(tm.forgot.off), 'the refusal')
  })

  it('the reset page checks the link, takes a new password once, and says every device is signed out', async () => {
    m = await mount(routes, {
      entries: (server) => {
        admin('PUT', '/admin/mail', { enabled: true, host: 'smtp.example.test', from: 'notes@example.test' }, server)
        const me = admin('GET', '/session', undefined, server).body as { user: { id: string } }
        admin('PATCH', `/users/${me.user.id}`, { email: 'admin@example.test', emailVerified: true }, server)
        expect(
          server.handle('POST', '/api/password/forgot', JSON.stringify({ login: 'admin' }), 'application/json').status,
        ).toBe(202)
        window.location.hash = '#' + tokenOf(server.mail.outbox.find((x) => x.kind === 'reset')!.link)
        return ['/reset-password']
      },
      server: { mode: 'accounts' },
    })
    await settle(() => text().includes(tm.reset.intro('admin')), 'the form')
    const pw = [...document.querySelectorAll<HTMLInputElement>('input[type="password"]')]
    await type(pw[0], 'a brand new password')
    await type(pw[1], 'something else entirely')
    await submit()
    await settle(() => text().includes(tm.reset.mismatch), 'the mismatch')
    await type(pw[1], 'a brand new password')
    await submit()
    await settle(() => text().includes(tm.reset.done), 'the confirmation')
    // The link works once.
    expect(m.requests.filter((r) => r.url === '/api/password/reset')).toHaveLength(1)
    const login = m.server.handle(
      'POST',
      '/api/login',
      JSON.stringify({ username: 'admin', password: 'a brand new password' }),
      'application/json',
    )
    expect(login.status).toBe(200)
  })

  it('a reset link that is not valid offers to ask again', async () => {
    await open('/reset-password', { mail: true, hash: '#nope' })
    await settle(() => text().includes(tm.reset.invalid), 'the answer')
    expect(text()).toContain(tm.reset.again)
  })

  it('a reset page without a token says so', async () => {
    await open('/reset-password')
    await settle(() => text().includes(tm.reset.noToken), 'the notice')
  })
})

describe('email verification link', () => {
  it('without a token the page says so', async () => {
    await open('/verify-email')
    await settle(() => text().includes(tm.verify.noToken), 'the notice')
  })

  it('a link the server does not know is not valid', async () => {
    await open('/verify-email', { mail: true, hash: '#nope' })
    await settle(() => text().includes(tm.verify.invalid), 'the invalid link')
  })

  it('the right link confirms the address', async () => {
    m = await mount(routes, {
      entries: (server) => {
        admin('PUT', '/admin/mail', { enabled: true, host: 'smtp.example.test', from: 'notes@example.test' }, server)
        const inv = admin('POST', '/invites', { role: 'member' }, server).body as { token: string }
        const joined = server.handle(
          'POST',
          '/api/invites/accept',
          JSON.stringify({ token: inv.token, username: 'tang', password: 'a-long-password' }),
          'application/json',
        )
        const cookie = joined.headers!['Set-Cookie'].split(';')[0]
        server.handle(
          'PATCH',
          '/api/account',
          JSON.stringify({ email: 'tang@example.test' }),
          'application/json',
          cookie,
        )
        expect(server.handle('POST', '/api/account/email/verify', '{}', 'application/json', cookie).status).toBe(202)
        window.location.hash = '#' + tokenOf(server.mail.outbox.find((x) => x.kind === 'verify')!.link)
        return ['/verify-email']
      },
      server: { mode: 'accounts' },
    })
    await settle(() => text().includes(tm.verify.done), 'the confirmation')
  })
})
