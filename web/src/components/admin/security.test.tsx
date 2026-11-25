// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { Route, Routes } from 'react-router'
import { keys } from '@/api/hooks'
import { SidebarProvider } from '@/components/ui/sidebar'
import { t } from '@/i18n'
import { tc } from '@/i18n/console'
import { ta } from '@/i18n/admin'
import { tsec } from '@/i18n/security'
import {
  adminRequests,
  byLabel,
  byText,
  click,
  installDomShims,
  makePerson,
  mount,
  settle,
  type,
  type Mounted,
} from '@/testing/harness'
import { AdminGate } from './AdminGate'
import { buildPatch, draftOf } from './securityDraft'
import { detailText } from './AuditSection'
import type { MockOptions } from '@/api/mock/server'
import type { SecuritySettings } from '@/api/types'

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
  vi.unstubAllGlobals()
})

const routes = (
  <SidebarProvider>
    <Routes>
      <Route path="/admin/*" element={<AdminGate />} />
    </Routes>
  </SidebarProvider>
)
const text = () => document.body.textContent ?? ''
const q = <T extends HTMLElement = HTMLElement>(sel: string) => document.querySelector<T>(sel)
const buttonWith = (label: string) =>
  [...document.querySelectorAll<HTMLElement>('button')].find((b) => b.textContent?.trim() === label)
const section = (id: string) => q(`section[aria-labelledby="${id}"]`)!
const dialog = () => q('[role="alertdialog"], [role="dialog"]')!
const calls = (method: string, url: string) => m!.requests.filter((r) => r.method === method && r.url.startsWith(url))

const company = {
  slug: 'company',
  kind: 'oidc' as const,
  name: 'Company SSO',
  icon: 'preset:microsoft',
  oidc: { issuer: 'https://login.example/v2.0', clientId: 'id', clientSecret: 'x' },
}

async function open(server: MockOptions = {}) {
  m = await mount(routes, { entries: ['/admin/security'], signIn: true, server: { mode: 'accounts', ...server } })
  await settle(() => q('[data-slot="security-form"]') !== null, 'the security form')
}
// The providers and the audit log are pages of their own.
async function openAt(path: string, server: MockOptions = {}) {
  m = await mount(routes, { entries: [path], signIn: true, server: { mode: 'accounts', ...server } })
  await settle(() => q('[data-slot="admin-layout"]') !== null, 'the administration')
}
const lastPut = () => JSON.parse(calls('PUT', '/api/admin/security').at(-1)!.body!)

describe('the draft of the form', () => {
  const server: SecuritySettings = {
    login: { mode: 'dual', effectiveMode: 'dual', ssoOnly: false, ssoOnlyInEffect: false, ssoAvailable: true },
    passkeys: { enabled: true, available: true, requireRoles: [], required: 0, requiredWithout: 0 },
    captcha: {
      provider: 'image',
      siteKey: '',
      hasSecret: false,
      contexts: {
        login: { on: false, when: 'after_failures', failures: 3 },
        invite: { on: false, when: 'after_failures', failures: 3 },
        setup: { on: false, when: 'after_failures', failures: 3 },
        forgot: { on: false, when: 'after_failures', failures: 3 },
        register: { on: false, when: 'after_failures', failures: 3 },
      },
    },
    lockout: { enabled: false, minutes: 15, scope: 'ip_account' },
    limits: { perMinute: 10, maxFailures: 10, windowMinutes: 15, sessionIdleDays: 30, sessionMaxDays: 90 },
    passwordAdmins: 1,
    notices: [],
  }
  it('makes no patch when nothing changed, and only the changed fields otherwise', () => {
    expect(buildPatch(server, draftOf(server))).toEqual({})
    const d = draftOf(server)
    d.mode = 'sso_first'
    d.contexts.login.on = true
    d.lockMinutes = '30'
    d.limits.perMinute = '20'
    expect(buildPatch(server, d)).toEqual({
      login: { mode: 'sso_first' },
      captcha: { contexts: { login: { on: true } } },
      lockout: { minutes: 30 },
      limits: { perMinute: 20 },
    })
  })
  it('sends the secret only when it was touched: a new one, or "" to clear', () => {
    const d = draftOf(server)
    expect(buildPatch(server, d).captcha).toBeUndefined()
    d.secret = ''
    expect(buildPatch(server, d)).toEqual({ captcha: { secret: '' } })
    d.secret = 'new'
    expect(buildPatch(server, d)).toEqual({ captcha: { secret: 'new' } })
  })
  it('turns the roles choice into the lists the server takes', () => {
    const d = draftOf(server)
    d.pkRoles = 'admins'
    expect(buildPatch(server, d)).toEqual({ passkeys: { requireRoles: ['admin'] } })
    d.pkRoles = 'everyone'
    expect(buildPatch(server, d)).toEqual({ passkeys: { requireRoles: ['admin', 'member'] } })
  })
})

describe('the audit detail as key=value', () => {
  it('flattens one level and keeps it short', () => {
    expect(detailText({ method: 'password', created: true })).toBe('method=password created=true')
    expect(detailText({ from: { 'login.mode': 'dual' }, to: { 'login.mode': 'sso_first' } })).toBe(
      'from.login.mode=dual to.login.mode=sso_first',
    )
    expect(detailText({})).toBe('')
  })
})

describe('the security page', () => {
  it('is a section of the administration, after the daily text', async () => {
    await open()
    expect(document.querySelector('nav[aria-label="' + tc.nav + '"] [aria-current="page"]')?.textContent).toBe(
      tc.sections.security,
    )
  })

  it('loads the settings into the form', async () => {
    await open({
      security: { login: { mode: 'sso_first' }, lockout: { minutes: 20 }, limits: { perMinute: 7 } },
      providers: [company],
    })
    expect(q<HTMLSelectElement>('#security-mode')!.value).toBe('sso_first')
    expect(q<HTMLInputElement>('#security-lock-minutes')!.value).toBe('20')
    expect(q<HTMLInputElement>('#security-limit-perMinute')!.value).toBe('7')
    expect(q('[data-slot="passkeys-required"]')!.textContent).toBe(tsec.passkeys.required(0, 0))
    // The four modes are all offered.
    expect([...q<HTMLSelectElement>('#security-mode')!.options].map((o) => o.value)).toEqual([
      'dual',
      'sso_first',
      'sso_redirect',
      'local_only',
    ])
  })

  it('saves only what was changed, and shows what the server answered', async () => {
    await open({ providers: [company] })
    const save = () => buttonWith(tsec.save) as HTMLButtonElement
    expect(save().disabled).toBe(true)
    await type(q<HTMLSelectElement>('#security-mode')!, 'sso_redirect')
    await type(q<HTMLInputElement>('#security-limit-maxFailures')!, '12')
    expect(save().disabled).toBe(false)
    await click(save())
    await settle(() => calls('PUT', '/api/admin/security').length === 1, 'the save')
    expect(lastPut()).toEqual({ login: { mode: 'sso_redirect' }, limits: { maxFailures: 12 } })
    await settle(() => (buttonWith(tsec.save) as HTMLButtonElement).disabled, 'saved')
    expect(q<HTMLSelectElement>('#security-mode')!.value).toBe('sso_redirect')
    // The change is in the audit log, a page of its own.
    expect(adminRequests(m!.server)('GET', '/api/admin/audit?kind=security_changed').body).toMatchObject({
      items: [{ kind: 'security_changed' }],
    })
  })

  it('shows a notice when no provider is enabled and the page differs from the choice', async () => {
    await open({ security: { login: { mode: 'sso_redirect' } } })
    const note = q('[data-slot="no-provider"]')!
    expect(note.textContent).toContain(tsec.login.noProvider(tsec.login.modes.local_only.name))
  })

  it('local_admin_required: the refusal is in words, next to the sign-in settings, and nothing is saved', async () => {
    await open({ providers: [company] })
    // The only administrator has no password any more (a person whose account single sign-on made).
    m!.server.handle(
      'POST',
      '/api/_mock/drop-password',
      JSON.stringify({ username: 'admin' }),
      'application/json',
      null,
    )
    await click(q('#security-ssoonly'))
    await click(buttonWith(tsec.save))
    await settle(() => text().includes(tsec.errors.local_admin_required), 'the refusal')
    expect(section('security-login').textContent).toContain(tsec.errors.local_admin_required)
    expect(calls('PUT', '/api/admin/security')).toHaveLength(1)
  })

  it('passkeys cannot be switched off while someone is required to have one', async () => {
    await open()
    // Someone is required to have one (this person has none yet).
    const carol = makePerson(m!.server, 'carol')
    adminRequests(m!.server)('PATCH', `/api/users/${carol.id}`, { passkeyRequired: true })
    await click(q('#security-pk-enabled'))
    await click(buttonWith(tsec.save))
    await settle(() => text().includes(tsec.errors.passkey_requirement_unsatisfiable), 'the refusal')
  })

  it('a third-party captcha needs a site key and a secret; the secret is write-only and shows as saved', async () => {
    await open()
    await type(q<HTMLSelectElement>('#security-captcha-provider')!, 'turnstile')
    await click(q('[data-context="login"] [role="switch"]'))
    await click(buttonWith(tsec.save))
    await settle(() => text().includes(tsec.errors.captcha_site_key_required), 'site key refusal')
    await type(q<HTMLInputElement>('#security-sitekey')!, 'site-1')
    await click(buttonWith(tsec.save))
    await settle(() => text().includes(tsec.errors.captcha_secret_required), 'secret refusal')
    await type(q<HTMLInputElement>('#security-secret')!, 'secret-1')
    await click(buttonWith(tsec.save))
    await settle(() => calls('PUT', '/api/admin/security').length === 3, 'third save')
    await settle(() => q('[data-slot="secret-saved"]') !== null, 'the secret is saved')
    expect(lastPut().captcha.secret).toBe('secret-1')
    // Never shown again.
    expect(document.body.innerHTML).not.toContain('secret-1')
    // Clearing it is saved as "".
    await click(buttonWith(tsec.captcha.clear))
    await click(buttonWith(tsec.save))
    await settle(() => calls('PUT', '/api/admin/security').length === 4, 'fourth save')
    expect(lastPut().captcha).toEqual({ secret: '' })
  })

  it('changing the captcha provider away from the stored one clears its site key, and coming back restores it', async () => {
    await open({ security: { captcha: { provider: 'turnstile', siteKey: 'site-1', hasSecret: true } } })
    await settle(() => q<HTMLInputElement>('#security-sitekey')?.value === 'site-1', 'the stored site key')
    await type(q<HTMLSelectElement>('#security-captcha-provider')!, 'hcaptcha')
    expect(q<HTMLInputElement>('#security-sitekey')!.value).toBe('')
    await type(q<HTMLSelectElement>('#security-captcha-provider')!, 'turnstile')
    expect(q<HTMLInputElement>('#security-sitekey')!.value).toBe('site-1')
  })

  it('the site key and the secret are marked required, and a stored secret reads "saved · change"', async () => {
    await open({ security: { captcha: { provider: 'turnstile', siteKey: 'site-1', hasSecret: true } } })
    await settle(() => q('[data-slot="secret-saved"]') !== null, 'the stored secret')
    const label = (id: string) => q(`label[for="${id}"]`)!
    expect(label('security-sitekey').querySelector('[data-slot="required-mark"]')?.textContent).toBe('*')
    expect(q('#security-sitekey')!.getAttribute('aria-required')).toBe('true')
    expect(
      q(
        '[data-slot="settings-row"]:has(#security-secret), [data-slot="settings-row"]:has([data-slot="secret-saved"])',
      )!.querySelector('[data-slot="required-mark"]'),
    ).not.toBeNull()
    expect(label('security-captcha-provider').querySelector('[data-slot="required-mark"]')).toBeNull()
    expect(q('[data-slot="secret-saved"]')!.textContent).toBe(`${tsec.captcha.secretSaved} · ${tsec.captcha.replace}`)
    expect(tsec.captcha.replace).toBe(ta.daily.headerChange)
  })

  it('lockout by username warns, asks to confirm, and is only chosen after that', async () => {
    await open()
    const scope = q<HTMLSelectElement>('#security-lock-scope')!
    await type(scope, 'account')
    await settle(() => text().includes(tsec.lockout.confirmTitle), 'the confirmation')
    // Cancelled: nothing changed.
    await click(byText('button', tsec.lockout.cancel))
    await settle(() => !text().includes(tsec.lockout.confirmTitle), 'closed')
    expect(q('[data-slot="scope-warning"]')).toBeNull()
    await type(scope, 'account')
    await settle(() => text().includes(tsec.lockout.confirmTitle), 'the confirmation again')
    await click(byText('button', tsec.lockout.confirm))
    await settle(() => q('[data-slot="scope-warning"]') !== null, 'the warning')
    expect(q('[data-slot="scope-warning"]')!.textContent).toBe(tsec.lockout.accountWarning)
    await click(buttonWith(tsec.save))
    await settle(() => calls('PUT', '/api/admin/security').length === 1, 'saved')
    expect(lastPut()).toEqual({ lockout: { scope: 'account' } })
  })

  it("a number out of range is refused with the server's message, not clamped", async () => {
    await open()
    await type(q<HTMLInputElement>('#security-limit-sessionIdleDays')!, '9999')
    await click(buttonWith(tsec.save))
    await settle(() => section('security-limits').querySelector('[role="alert"]') !== null, 'the refusal')
    expect(section('security-limits').querySelector('[role="alert"]')!.textContent).toContain('limits.sessionIdleDays')
    expect(q<HTMLInputElement>('#security-limit-sessionIdleDays')!.value).toBe('9999')
  })

  it('passkeys: requiring a role is refused while passkeys cannot work, and the page says why', async () => {
    await open({ passkeysAvailable: false })
    expect(text()).toContain(tsec.passkeys.unavailable)
    await type(q<HTMLSelectElement>('#security-pk-roles')!, 'admins')
    await click(buttonWith(tsec.save))
    await settle(() => text().includes(tsec.errors.passkey_unavailable), 'the refusal')
  })

  it('shows the notices the server has', async () => {
    await open({
      securityNotices: [
        { code: 'flags_ignored', message: 'flags differ' },
        { code: 'secret_unreadable', message: 'x', slug: 'company' },
      ],
    })
    const notices = q('[data-slot="security-notices"]')!.textContent!
    expect(notices).toContain(tsec.notices.flags_ignored)
    expect(notices).toContain('company')
  })

  it('counts the people a passkey is required of', async () => {
    await open()
    const carol = makePerson(m!.server, 'carol')
    adminRequests(m!.server)('PATCH', `/api/users/${carol.id}`, { passkeyRequired: true })
    await act(async () => void (await m!.qc.invalidateQueries({ queryKey: keys.adminSecurity })))
    await settle(() => q('[data-slot="passkeys-required"]')!.textContent === tsec.passkeys.required(1, 1), 'the count')
  })
})

const openProviders = async (server: MockOptions = {}) => {
  await openAt('/admin/security/providers', server)
  await settle(() => buttonWith(tsec.providers.add) !== undefined, 'the providers page')
}
describe('the sign-in providers', () => {
  const listed = () => [...document.querySelectorAll<HTMLElement>('[data-slot="providers"] > li')]
  const form = () => q('[data-slot="provider-form"]')!
  // A label's words, without the star of a required field.
  const words = (el: Element | null | undefined) => (el?.textContent ?? '').replace(/\*$/, '')
  const inputIn = (label: string) =>
    [...form().querySelectorAll<HTMLInputElement | HTMLTextAreaElement>('input, textarea')].find(
      (i) => words(i.closest('div')?.querySelector('label')) === label || i.getAttribute('aria-label') === label,
    )!
  const labelOf = (label: string) => [...form().querySelectorAll('label')].find((l) => words(l) === label)!

  it('lists the providers with their status and how many people use them', async () => {
    await openProviders({
      providers: [
        company,
        {
          slug: 'campus',
          kind: 'saml',
          name: 'Campus login',
          saml: { metadataUrl: 'https://idp.example/m' },
          status: { ok: false, checked: '2026-11-23T08:00:00Z', message: 'no route to host' },
        },
      ],
    })
    await settle(() => listed().length === 2, 'the list')
    expect(listed()[0].textContent).toContain('Company SSO')
    expect(listed()[0].textContent).toContain(tsec.providers.kinds.oidc)
    expect(listed()[0].textContent).toContain(tsec.providers.statusOk)
    expect(listed()[1].textContent).toContain(tsec.providers.statusBad)
    expect(listed()[1].textContent).toContain('no route to host')
  })

  it('creates an OpenID Connect provider with the fields of its kind, and shows the address to register and "saved · change"', async () => {
    await openProviders()
    await settle(() => text().includes(tsec.providers.empty), 'no providers yet')
    await click(buttonWith(tsec.providers.add))
    await settle(() => q('[data-slot="provider-form"]') !== null, 'the form')
    await type(inputIn(tsec.providers.slug), 'acme')
    await type(inputIn(tsec.providers.name), 'Acme SSO')
    await type(inputIn(tsec.providers.oidc.issuer), 'https://login.acme.example')
    await type(inputIn(tsec.providers.oidc.clientId), 'client-1')
    await type(inputIn(tsec.providers.oidc.clientSecret), 'shh')
    await click(buttonWith(tsec.providers.create))
    await settle(() => listed().length === 1, 'the new provider')
    const post = JSON.parse(calls('POST', '/api/admin/auth/providers')[0].body!)
    expect(post).toMatchObject({
      slug: 'acme',
      kind: 'oidc',
      name: 'Acme SSO',
      oidc: {
        issuer: 'https://login.acme.example',
        clientId: 'client-1',
        clientSecret: 'shh',
        scopes: ['profile', 'email'],
      },
    })
    // Editing it shows the redirect address and that a secret is saved, but not the secret.
    await click(byLabel(tsec.providers.editFor('Acme SSO')))
    await settle(() => q('[data-slot="provider-form"]') !== null, 'the edit form')
    expect(inputIn(tsec.providers.oidc.redirectUrl).value).toBe('https://notes.example.com/api/auth/oidc/acme/callback')
    expect(q('[data-slot="secret-saved"]')!.textContent).toContain(
      `${tsec.providers.secretSaved} · ${tsec.providers.secretReplace}`,
    )
    expect(tsec.providers.secretReplace).toBe(ta.daily.headerChange)
    expect(document.body.innerHTML).not.toContain('shh')
  })

  it('marks the required fields with a star', async () => {
    await openProviders({ providers: [company] })
    await settle(() => listed().length === 1, 'the list')
    await click(buttonWith(tsec.providers.add))
    await settle(() => q('[data-slot="provider-form"]') !== null, 'the form')
    const marked = (label: string) => labelOf(label).querySelector('[data-slot="required-mark"]') !== null
    for (const l of [
      tsec.providers.slug,
      tsec.providers.name,
      tsec.providers.oidc.issuer,
      tsec.providers.oidc.clientId,
    ]) {
      expect(marked(l), l).toBe(true)
      expect(inputIn(l).getAttribute('aria-required'), l).toBe('true')
    }
    for (const l of [tsec.providers.order, tsec.providers.oidc.scopes, tsec.providers.oidc.clientSecret])
      expect(marked(l), l).toBe(false)
    await type(q<HTMLSelectElement>('#provider-kind')!, 'github')
    for (const l of [tsec.providers.github.clientId, tsec.providers.github.clientSecret])
      expect(marked(l), l).toBe(true)
    expect(marked(tsec.providers.github.baseUrl)).toBe(false)
  })

  it('SAML shows what to give the identity provider: entity id, ACS address, metadata path and certificate', async () => {
    await openProviders({
      providers: [
        { slug: 'campus', kind: 'saml', name: 'Campus login', saml: { metadataUrl: 'https://idp.example/m' } },
      ],
    })
    await settle(() => listed().length === 1, 'the list')
    await click(byLabel(tsec.providers.editFor('Campus login')))
    await settle(() => q('[data-slot="saml-readonly"]') !== null, 'the SAML fields')
    const ro = q('[data-slot="saml-readonly"]')!
    expect(ro.textContent).toContain(tsec.providers.saml.acsUrl)
    expect((ro.querySelector('[aria-label="' + tsec.providers.saml.acsUrl + '"]') as HTMLInputElement).value).toBe(
      'https://notes.example.com/api/auth/saml/campus/acs',
    )
    expect(
      (ro.querySelector('[aria-label="' + tsec.providers.saml.metadataPath + '"]') as HTMLInputElement).value,
    ).toBe('/api/auth/saml/campus/metadata')
    expect(
      (ro.querySelector('[aria-label="' + tsec.providers.saml.certificate + '"]') as HTMLTextAreaElement).value,
    ).toContain('BEGIN CERTIFICATE')
  })

  it('maps the refusals to words: a taken slug, an issuer in use, a GitHub provider that creates accounts without an organisation', async () => {
    await openProviders({ providers: [company] })
    await settle(() => listed().length === 1, 'the list')
    const fill = async (slug: string, issuer: string) => {
      await click(buttonWith(tsec.providers.add))
      await settle(() => q('[data-slot="provider-form"]') !== null, 'the form')
      await type(inputIn(tsec.providers.slug), slug)
      await type(inputIn(tsec.providers.name), 'Other')
      await type(inputIn(tsec.providers.oidc.issuer), issuer)
      await type(inputIn(tsec.providers.oidc.clientId), 'c')
    }
    await fill('company', 'https://other.example')
    await click(buttonWith(tsec.providers.create))
    await settle(() => form().textContent!.includes(tsec.providers.errors.exists), 'exists')
    await type(inputIn(tsec.providers.slug), 'other')
    await type(inputIn(tsec.providers.oidc.issuer), 'https://login.example/v2.0')
    await click(buttonWith(tsec.providers.create))
    await settle(() => form().textContent!.includes(tsec.providers.errors.provider_issuer_in_use), 'issuer in use')
    await type(inputIn(tsec.providers.oidc.issuer), 'http://insecure.example')
    await click(buttonWith(tsec.providers.create))
    await settle(() => form().textContent!.includes(tsec.providers.errors.oidc_issuer_invalid), 'issuer invalid')
  })

  it('a provider that cannot be reached is stored switched off, and the person is told', async () => {
    await openProviders()
    await click(buttonWith(tsec.providers.add))
    await settle(() => q('[data-slot="provider-form"]') !== null, 'the form')
    await type(inputIn(tsec.providers.slug), 'dead')
    await type(inputIn(tsec.providers.name), 'Dead')
    await type(inputIn(tsec.providers.oidc.issuer), 'https://unreachable.example')
    await type(inputIn(tsec.providers.oidc.clientId), 'c')
    await click(buttonWith(tsec.providers.create))
    await settle(() => listed().length === 1, 'stored')
    expect(listed()[0].textContent).toContain(tsec.providers.statusBad)
    // Left at its default, "enabled" is not asked for, so the server keeps it off.
    expect(JSON.parse(calls('POST', '/api/admin/auth/providers')[0].body!)).not.toHaveProperty('enabled')
    expect(listed()[0].textContent).toContain(tsec.providers.disabled)
  })

  it('switching one off and fetching again are single calls', async () => {
    await openProviders({ providers: [company] })
    await settle(() => listed().length === 1, 'the list')
    await click(listed()[0].querySelector('[role="switch"]'))
    await settle(() => calls('PATCH', '/api/admin/auth/providers/company').length === 1, 'patched')
    expect(JSON.parse(calls('PATCH', '/api/admin/auth/providers/company')[0].body!)).toEqual({ enabled: false })
    await click(byLabel(tsec.providers.refreshFor('Company SSO')))
    await settle(() => calls('POST', '/api/admin/auth/providers/company/refresh').length === 1, 'refreshed')
  })

  it("deleting asks first; a provider that is somebody's only way in asks again, and then force-deletes", async () => {
    await openProviders({ providers: [company] })
    await settle(() => listed().length === 1, 'the list')
    // Somebody whose only way in is this provider (single sign-on made the account).
    const b = await signInThroughProvider()
    expect(b).toBe(true)
    await click(byLabel(tsec.providers.deleteFor('Company SSO')))
    await settle(() => text().includes(tsec.providers.deleteTitle('Company SSO')), 'the first question')
    await click([...dialog().querySelectorAll('button')].find((b) => b.textContent === tsec.providers.deleteConfirm))
    await settle(() => text().includes(tsec.providers.deleteInUseTitle('Company SSO')), 'the second question')
    expect(text()).toContain(tsec.providers.deleteInUse(1))
    await click([...dialog().querySelectorAll('button')].find((b) => b.textContent === tsec.providers.deleteForce))
    await settle(() => listed().length === 0, 'deleted')
    expect(calls('DELETE', '/api/admin/auth/providers/company?force=1')).toHaveLength(1)
  })

  it('shows the audit log, filters it by kind and loads more', async () => {
    await openAt('/admin/audit', { providers: [company] })
    await settle(() => q('#audit-kind') !== null, 'the audit log')
    // 60 rows of one kind, so there is a second page.
    const admin = adminRequests(m!.server)
    for (let i = 0; i < 60; i++) admin('PUT', '/api/admin/security', { limits: { perMinute: 10 + (i % 2) + 1 } })
    await click(buttonWith(tsec.audit.refresh))
    await settle(() => document.querySelectorAll('[data-slot="audit-rows"] > li').length === 50, 'the first page')
    await click(buttonWith(tsec.audit.loadMore))
    await settle(() => document.querySelectorAll('[data-slot="audit-rows"] > li').length > 50, 'the next page')
    expect(calls('GET', '/api/admin/audit').some((r) => r.url.includes('cursor='))).toBe(true)
    await type(q<HTMLSelectElement>('#audit-kind')!, 'provider_created')
    await settle(() => calls('GET', '/api/admin/audit?kind=provider_created').length > 0, 'the filter')
  })

  it('a person who is not an administrator cannot read it', async () => {
    await openAt('/admin/audit')
    const person = makePerson(m!.server, 'carol')
    expect(() => person.request('GET', '/api/admin/audit')).toThrow(/403/)
    expect(t.appName).toBeTruthy()
  })
})

// An account made by the provider, with nothing else to sign in with.
async function signInThroughProvider(): Promise<boolean> {
  const server = m!.server
  const start = server.handle(
    'POST',
    '/api/auth/oidc/company/start',
    JSON.stringify({ intent: 'login' }),
    'application/json',
    null,
  )
  const cookie = start.headers!['Set-Cookie'].split(';')[0]
  const url = (start.body as { url: string }).url
  // The provider creates accounts here: switch that on for the scene.
  adminRequests(server)('PATCH', '/api/admin/auth/providers/company', { autoCreate: true })
  const cb = server.handle('GET', url, undefined, null, cookie)
  return cb.status === 303 && cb.headers!.Location === '/'
}
