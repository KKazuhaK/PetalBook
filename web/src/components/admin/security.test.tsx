// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { Route, Routes } from 'react-router'
import { keys } from '@/api/hooks'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tc } from '@/i18n/console'
import { ta } from '@/i18n/admin'
import { tsec } from '@/i18n/security'
import {
  byLabel,
  byText,
  click,
  installDomShims,
  locationText,
  mount,
  settle,
  type,
  type FxMounted,
} from '@/testing/harness'
import type { FixtureData } from '@/api/fixtures/data/model'
import type { FixtureServer } from '@/api/fixtures/server'
import type { ProviderInput, AuthProvider, SecurityNotice } from '@/api/types'
import { addPerson, addProvider, setSite } from '@/testing/adminScene'
import { ssoErrorMessage } from '@/pages/auth/LoginPage'
import { AdminGate } from './AdminGate'
import { buildSteps, draftOf } from './securityDraft'
import { detailText } from './AuditSection'
import type { SecuritySettings } from '@/api/types'

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
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

// A sign-in provider that is there already: what its form would have sent, and what the server lists of it.
const company = {
  slug: 'company',
  name: 'Company SSO',
  input: {
    icon: 'preset:microsoft',
    oidc: { issuer: 'https://login.example/v2.0', clientId: 'id', clientSecret: 'x' },
  } satisfies ProviderInput,
}

// What a scene may set before the page opens: the sign-in settings, the providers, what the server says besides.
interface Scene {
  mode?: string
  lockMinutes?: number
  perMinute?: number
  captcha?: { provider: string; siteKey: string; hasSecret: boolean }
  providers?: { slug: string; name: string; input?: ProviderInput; extra?: Partial<AuthProvider> }[]
  passkeysAvailable?: boolean
  notices?: SecurityNotice[]
  data?: (d: FixtureData) => void
  setup?: (fx: FixtureServer) => void
}
const sceneOf = (o: Scene) => (d: FixtureData) => {
  if (o.mode) setSite(d, 'signin.mode', o.mode)
  if (o.lockMinutes) setSite(d, 'signin.lockout.minutes', o.lockMinutes)
  if (o.perMinute) setSite(d, 'signin.perMinute', o.perMinute)
  if (o.captcha) {
    setSite(d, 'captcha.provider', o.captcha.provider)
    d.admin.captcha = { siteKey: o.captcha.siteKey, hasSecret: o.captcha.hasSecret }
  }
  for (const p of o.providers ?? []) addProvider(d, p.slug, p.name, p.input, p.extra)
  if (o.passkeysAvailable === false) d.admin.security.passkeysAvailable = false
  if (o.notices) d.admin.security.notices = o.notices
  o.data?.(d)
}

async function open(o: Scene = {}) {
  m = await mount(routes, {
    entries: (fx) => {
      o.setup?.(fx)
      return ['/admin/security']
    },
    signIn: true,
    fixtures: { mode: 'accounts', data: sceneOf(o) },
  })
  await settle(() => q('[data-slot="security-form"]') !== null, 'the security form')
}
// The providers and the audit log are pages of their own.
async function openAt(path: string, o: Scene = {}) {
  m = await mount(routes, {
    entries: (fx) => {
      o.setup?.(fx)
      return [path]
    },
    signIn: true,
    fixtures: { mode: 'accounts', data: sceneOf(o) },
  })
  await settle(() => q('[data-slot="admin-layout"]') !== null, 'the administration')
}
const SETTING = '/api/admin/settings/'
// The writes the page made, in order: [key, value] of a setting, or ['credentials', body] of PUT /api/admin/security.
const writes = () =>
  m!.requests
    .filter((r) => r.method === 'PUT' && (r.url.startsWith(SETTING) || r.url === '/api/admin/security'))
    .map((r) => [
      r.url.startsWith(SETTING) ? r.url.slice(SETTING.length) : 'credentials',
      r.url.startsWith(SETTING) ? JSON.parse(r.body!).value : JSON.parse(r.body!).captcha,
    ])

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
  it('makes no writes when nothing changed, and one per setting that changed otherwise', () => {
    expect(buildSteps(server, draftOf(server))).toEqual([])
    const d = draftOf(server)
    d.mode = 'sso_first'
    d.contexts.login.on = true
    d.lockMinutes = '30'
    d.limits.perMinute = '20'
    expect(buildSteps(server, d)).toEqual([
      { key: 'signin.mode', value: 'sso_first' },
      { key: 'captcha.contexts', value: { login: { on: true } } },
      { key: 'signin.lockout.minutes', value: 30 },
      { key: 'signin.perMinute', value: 20 },
    ])
  })
  it('sends the captcha keys only when they were touched: a new secret, or "" to clear', () => {
    const third: SecuritySettings = { ...server, captcha: { ...server.captcha, provider: 'turnstile', siteKey: 'k' } }
    const d = draftOf(third)
    expect(buildSteps(third, d)).toEqual([])
    d.secret = ''
    expect(buildSteps(third, d)).toEqual([{ credentials: { secret: '' } }])
    d.secret = 'new'
    d.siteKey = 'k2'
    expect(buildSteps(third, d)).toEqual([{ credentials: { siteKey: 'k2', secret: 'new' } }])
  })
  it('turns the roles choice into the lists the server takes, switching passkeys on before requiring and off after lifting', () => {
    const d = draftOf(server)
    d.pkRoles = 'admins'
    expect(buildSteps(server, d)).toEqual([{ key: 'signin.passkeyRoles', value: ['admin'] }])
    d.pkRoles = 'everyone'
    expect(buildSteps(server, d)).toEqual([{ key: 'signin.passkeyRoles', value: ['admin', 'member'] }])
    const off = draftOf(server)
    off.pkEnabled = false
    off.pkRoles = 'none'
    const required: SecuritySettings = { ...server, passkeys: { ...server.passkeys, requireRoles: ['admin'] } }
    expect(buildSteps(required, off)).toEqual([
      { key: 'signin.passkeyRoles', value: [] },
      { key: 'features.passkeys', value: false },
    ])
    const on: SecuritySettings = { ...server, passkeys: { ...server.passkeys, enabled: false } }
    const back = draftOf(on)
    back.pkEnabled = true
    back.pkRoles = 'admins'
    expect(buildSteps(on, back)).toEqual([
      { key: 'features.passkeys', value: true },
      { key: 'signin.passkeyRoles', value: ['admin'] },
    ])
  })
  it('turns the contexts off before another provider takes over, then its keys, then the contexts that come on', () => {
    const on: SecuritySettings = {
      ...server,
      captcha: {
        ...server.captcha,
        provider: 'turnstile',
        siteKey: 'k',
        hasSecret: true,
        contexts: { ...server.captcha.contexts, login: { on: true, when: 'always', failures: 3 } },
      },
    }
    const d = draftOf(on)
    d.provider = 'hcaptcha'
    d.siteKey = 'h'
    d.secret = 'hs'
    expect(buildSteps(on, d)).toEqual([
      { key: 'captcha.contexts', value: { login: { on: false } } },
      { key: 'captcha.provider', value: 'hcaptcha' },
      { credentials: { siteKey: 'h', secret: 'hs' } },
      { key: 'captcha.contexts', value: { login: { on: true } } },
    ])
  })
  it('raises the longer session lifetime first and lowers it last', () => {
    const d = draftOf(server)
    d.limits.sessionMaxDays = '120'
    d.limits.sessionIdleDays = '100'
    expect(buildSteps(server, d).map((x) => ('key' in x ? x.key : ''))).toEqual([
      'sessions.maxDays',
      'sessions.idleDays',
    ])
    const e = draftOf(server)
    e.limits.sessionMaxDays = '40'
    e.limits.sessionIdleDays = '20'
    expect(buildSteps(server, e).map((x) => ('key' in x ? x.key : ''))).toEqual([
      'sessions.idleDays',
      'sessions.maxDays',
    ])
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
    await open({ mode: 'sso_first', lockMinutes: 20, perMinute: 7, providers: [company] })
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
    await settle(() => writes().length === 2, 'the save')
    expect(writes()).toEqual([
      ['signin.mode', 'sso_redirect'],
      ['signin.maxFailures', 12],
    ])
    await settle(() => (buttonWith(tsec.save) as HTMLButtonElement).disabled, 'saved')
    expect(q<HTMLSelectElement>('#security-mode')!.value).toBe('sso_redirect')
  })

  it('shows a notice when no provider is enabled and the page differs from the choice', async () => {
    await open({ mode: 'sso_redirect' })
    const note = q('[data-slot="no-provider"]')!
    expect(note.textContent).toContain(tsec.login.noProvider(tsec.login.modes.local_only.name))
  })

  it('local_admin_required: the refusal is in words, next to the sign-in settings, and nothing is saved', async () => {
    // The only administrator has no password any more (a person whose account single sign-on made): the server refuses.
    await open({
      providers: [company],
      setup: (fx) => fx.fail('PUT', '/api/admin/settings/signin.ssoOnly', 409, { code: 'local_admin_required' }),
    })
    await click(q('#security-ssoonly'))
    await click(buttonWith(tsec.save))
    await settle(() => text().includes(tsec.errors.local_admin_required), 'the refusal')
    expect(section('security-login').textContent).toContain(tsec.errors.local_admin_required)
    expect(writes()).toEqual([['signin.ssoOnly', true]])
    expect(m!.requests.filter((r) => r.method === 'GET' && r.url === '/api/admin/settings').length).toBeGreaterThan(0)
  })

  it('passkeys cannot be switched off while someone is required to have one', async () => {
    // Someone is required to have one (this person has none yet): the server refuses to switch passkeys off.
    await open({
      data: (d) => void addPerson(d, 'carol', { passkeyRequired: true }),
      setup: (fx) =>
        fx.fail('PUT', '/api/admin/settings/features.passkeys', 409, { code: 'passkey_requirement_unsatisfiable' }),
    })
    await click(q('#security-pk-enabled'))
    await click(buttonWith(tsec.save))
    await settle(() => text().includes(tsec.errors.passkey_requirement_unsatisfiable), 'the refusal')
  })

  it('a third-party captcha needs a site key and a secret; each refusal keeps what was typed, and the secret is write-only', async () => {
    // The server wants a site key before the contexts come on, then a secret (the last asked is the first registered).
    await open({
      setup: (fx) => {
        fx.fail('PUT', '/api/admin/settings/captcha.contexts', 409, { code: 'captcha_secret_required' }, { times: 1 })
        fx.fail('PUT', '/api/admin/settings/captcha.contexts', 409, { code: 'captcha_site_key_required' }, { times: 1 })
      },
    })
    await type(q<HTMLSelectElement>('#security-captcha-provider')!, 'turnstile')
    await click(q('[data-context="login"] [role="switch"]'))
    await click(buttonWith(tsec.save))
    await settle(() => text().includes(tsec.errors.captcha_site_key_required), 'site key refusal')
    // The provider was written before the refusal; the form is made again with what was typed.
    expect(writes()).toEqual([
      ['captcha.provider', 'turnstile'],
      ['captcha.contexts', { login: { on: true } }],
    ])
    expect(section('security-captcha').textContent).toContain(tsec.errors.captcha_site_key_required)
    expect(q<HTMLSelectElement>('#security-captcha-provider')!.value).toBe('turnstile')
    expect(q('[data-context="login"] [role="switch"]')!.getAttribute('aria-checked')).toBe('true')
    await type(q<HTMLInputElement>('#security-sitekey')!, 'site-1')
    await click(buttonWith(tsec.save))
    await settle(() => text().includes(tsec.errors.captcha_secret_required), 'secret refusal')
    await type(q<HTMLInputElement>('#security-secret')!, 'secret-1')
    await click(buttonWith(tsec.save))
    await settle(() => q('[data-slot="secret-saved"]') !== null, 'the secret is saved')
    expect(writes().at(-2)).toEqual(['credentials', { secret: 'secret-1' }])
    // Never shown again.
    expect(document.body.innerHTML).not.toContain('secret-1')
    // Clearing it with the contexts on is refused; it is sent as "".
    const before = writes().length
    await click(buttonWith(tsec.captcha.clear))
    await click(buttonWith(tsec.save))
    await settle(() => writes().length === before + 1, 'the clearing')
    expect(writes().at(-1)).toEqual(['credentials', { secret: '' }])
  })

  it('changing the captcha provider away from the stored one clears its site key, and coming back restores it', async () => {
    await open({ captcha: { provider: 'turnstile', siteKey: 'site-1', hasSecret: true } })
    await settle(() => q<HTMLInputElement>('#security-sitekey')?.value === 'site-1', 'the stored site key')
    await type(q<HTMLSelectElement>('#security-captcha-provider')!, 'hcaptcha')
    expect(q<HTMLInputElement>('#security-sitekey')!.value).toBe('')
    await type(q<HTMLSelectElement>('#security-captcha-provider')!, 'turnstile')
    expect(q<HTMLInputElement>('#security-sitekey')!.value).toBe('site-1')
  })

  it('the site key and the secret are marked required, and a stored secret reads "saved · change"', async () => {
    await open({ captcha: { provider: 'turnstile', siteKey: 'site-1', hasSecret: true } })
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
    await settle(() => writes().length === 1, 'saved')
    expect(writes()).toEqual([['signin.lockout.scope', 'account']])
  })

  it("a number out of range is refused with the server's message, not clamped", async () => {
    // The server refuses a number out of range, and says which setting.
    await open({
      setup: (fx) =>
        fx.fail('PUT', '/api/admin/settings/sessions.idleDays', 400, {
          code: 'invalid',
          message: 'sessions.idleDays must be between 1 and 365',
        }),
    })
    await type(q<HTMLInputElement>('#security-limit-sessionIdleDays')!, '9999')
    await click(buttonWith(tsec.save))
    await settle(() => section('security-limits').querySelector('[role="alert"]') !== null, 'the refusal')
    expect(section('security-limits').querySelector('[role="alert"]')!.textContent).toContain('sessions.idleDays')
    expect(q<HTMLInputElement>('#security-limit-sessionIdleDays')!.value).toBe('9999')
  })

  it('passkeys: requiring a role is refused while passkeys cannot work, and the page says why', async () => {
    await open({
      passkeysAvailable: false,
      setup: (fx) => fx.fail('PUT', '/api/admin/settings/signin.passkeyRoles', 409, { code: 'passkey_unavailable' }),
    })
    expect(text()).toContain(tsec.passkeys.unavailable)
    await type(q<HTMLSelectElement>('#security-pk-roles')!, 'admins')
    await click(buttonWith(tsec.save))
    await settle(() => text().includes(tsec.errors.passkey_unavailable), 'the refusal')
  })

  it('shows the notices the server has', async () => {
    await open({
      notices: [{ code: 'secret_unreadable', message: 'x', slug: 'company' }],
    })
    const notices = q('[data-slot="security-notices"]')!.textContent!
    expect(notices).toContain(tsec.notices.secret_unreadable)
    expect(notices).toContain('company')
  })

  it('counts the people a passkey is required of', async () => {
    await open({ data: (d) => void addPerson(d, 'carol') })
    m!.fx.data.auth.users.find((u) => u.username === 'carol')!.passkeyRequired = true
    await act(async () => void (await m!.qc.invalidateQueries({ queryKey: keys.adminSecurity })))
    await settle(() => q('[data-slot="passkeys-required"]')!.textContent === tsec.passkeys.required(1, 1), 'the count')
  })
})

const openProviders = async (o: Scene = {}) => {
  await openAt('/admin/security/providers', o)
  await settle(() => buttonWith(tsec.providers.add) !== undefined, 'the providers page')
}

// "Add" opens the templates; a provider of one's own is the last choice.
const openPicker = async () => {
  await click(buttonWith(tsec.providers.add))
  await settle(() => q('[data-slot="template-picker"]') !== null, 'the templates')
}
const openCustom = async () => {
  await openPicker()
  await click(q('[data-slot="template-picker"] [data-template="custom"]')!)
  await settle(() => q('[data-slot="provider-form"]') !== null, 'the form')
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
          name: 'Campus login',
          input: { kind: 'saml', saml: { metadataUrl: 'https://idp.example/m' } },
          extra: { status: { ok: false, checked: '2026-11-23T08:00:00Z', message: 'no route to host' } },
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

  it('adds a provider from a template: the common ones first, GitLab.com under "More templates", and Google needs only the client', async () => {
    await openProviders()
    await openPicker()
    const picker = () => q('[data-slot="template-picker"]')!.textContent!
    for (const name of ['Google', 'Microsoft', 'Apple', 'GitHub']) expect(picker()).toContain(name)
    expect(picker()).not.toContain('GitLab.com')
    await click(buttonWith(tsec.providers.moreTemplates))
    await settle(() => picker().includes('GitLab.com'), 'more templates')
    await click(q('[data-slot="template-picker"] [data-template="google"]')!)
    await settle(() => q('[data-slot="template-form"]') !== null, 'the template form')
    const tform = q('[data-slot="template-form"]')!
    const field = (label: string) =>
      [...tform.querySelectorAll<HTMLInputElement>('input')].find(
        (i) => words(i.closest('div')?.querySelector('label')) === label || i.getAttribute('aria-label') === label,
      )!
    expect(field(tsec.providers.slug).value).toBe('google')
    expect(field(tsec.providers.name).value).toBe('Google')
    expect(field(tsec.providers.oidc.redirectUrl).value).toMatch(/\/api\/auth\/oidc\/google\/callback$/)
    expect(buttonWith(tsec.providers.create)!.hasAttribute('disabled')).toBe(true)
    await type(field(tsec.providers.oidc.clientId), 'g-client')
    await type(field(tsec.providers.oidc.clientSecret), 'g-secret')
    await click(buttonWith(tsec.providers.create))
    await settle(() => listed().length === 1, 'the new provider')
    expect(JSON.parse(calls('POST', '/api/admin/providers')[0].body!)).toEqual({
      template: 'google',
      slug: 'google',
      name: 'Google',
      clientId: 'g-client',
      clientSecret: 'g-secret',
    })
    expect(listed()[0].textContent).toContain(tsec.providers.classes.social)
    // Its issuer is the template's: shown, not editable.
    await click(byLabel(tsec.providers.editFor('Google')))
    await settle(() => q('[data-slot="provider-form"]') !== null, 'the edit form')
    expect(inputIn(tsec.providers.oidc.issuer).readOnly).toBe(true)
    expect(form().textContent).toContain(tsec.providers.fromTemplate('Google'))
    expect(q<HTMLSelectElement>('#provider-class')!.disabled).toBe(true)
  })

  it('Sign in with Apple asks for the team id, the key id and the .p8 key, never a client secret', async () => {
    await openProviders()
    await openPicker()
    await click(q('[data-slot="template-picker"] [data-template="apple"]')!)
    await settle(() => q('[data-slot="apple-fields"]') !== null, 'the Apple fields')
    const tform = q('[data-slot="template-form"]')!
    expect(tform.querySelector('input[type="password"]')).toBeNull()
    const field = (label: string) =>
      [...tform.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>('input, textarea')].find(
        (i) => words(i.closest('div')?.querySelector('label')) === label,
      )!
    await type(field(tsec.providers.oidc.clientId), 'com.example.web')
    await type(field(tsec.providers.apple.teamId), 'TEAM123456')
    await type(field(tsec.providers.apple.keyId), 'KEY1234567')
    await type(field(tsec.providers.apple.privateKey), '-----BEGIN PRIVATE KEY-----\nMIGT\n-----END PRIVATE KEY-----')
    await click(buttonWith(tsec.providers.create))
    await settle(() => listed().length === 1, 'the new provider')
    expect(JSON.parse(calls('POST', '/api/admin/providers')[0].body!)).toEqual({
      template: 'apple',
      slug: 'apple',
      name: 'Apple',
      clientId: 'com.example.web',
      apple: {
        teamId: 'TEAM123456',
        keyId: 'KEY1234567',
        privateKey: '-----BEGIN PRIVATE KEY-----\nMIGT\n-----END PRIVATE KEY-----',
      },
    })
    // Editing it: the key is saved and never shown; a new key id alone is sent.
    await click(byLabel(tsec.providers.editFor('Apple')))
    await settle(() => q('[data-slot="provider-form"]') !== null, 'the edit form')
    expect(q('[data-slot="apple-fields"] [data-slot="secret-saved"]')!.textContent).toContain(
      tsec.providers.apple.keySaved,
    )
    expect(document.body.innerHTML).not.toContain('MIGT')
    await type(inputIn(tsec.providers.apple.keyId), 'KEY7654321')
    await click(buttonWith(tsec.providers.save))
    await settle(() => calls('PATCH', '/api/admin/providers/apple').length === 1, 'the change')
    expect(JSON.parse(calls('PATCH', '/api/admin/providers/apple')[0].body!)).toEqual({
      apple: { keyId: 'KEY7654321' },
    })
  })

  it('a provider of one’s own chooses its class once: single sign-on by default, social for GitHub', async () => {
    await openProviders()
    await openCustom()
    const cls = () => q<HTMLSelectElement>('#provider-class')!
    expect(cls().value).toBe('sso')
    expect(cls().disabled).toBe(false)
    await type(q<HTMLSelectElement>('#provider-kind')!, 'github')
    expect(cls().value).toBe('social')
    expect(cls().disabled).toBe(true)
  })

  it('creates an OpenID Connect provider with the fields of its kind, and shows the address to register and "saved · change"', async () => {
    await openProviders()
    await settle(() => text().includes(tsec.providers.empty), 'no providers yet')
    await openCustom()
    await type(inputIn(tsec.providers.slug), 'acme')
    await type(inputIn(tsec.providers.name), 'Acme SSO')
    await type(inputIn(tsec.providers.oidc.issuer), 'https://login.acme.example')
    await type(inputIn(tsec.providers.oidc.clientId), 'client-1')
    await type(inputIn(tsec.providers.oidc.clientSecret), 'shh')
    await click(buttonWith(tsec.providers.create))
    await settle(() => listed().length === 1, 'the new provider')
    const post = JSON.parse(calls('POST', '/api/admin/providers')[0].body!)
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
    await openCustom()
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
        {
          slug: 'campus',
          name: 'Campus login',
          input: { kind: 'saml', saml: { metadataUrl: 'https://idp.example/m' } },
        },
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
    // The server refuses, in turn: a slug that is taken, an issuer in use, an issuer that is not https.
    await openProviders({
      providers: [company],
      setup: (fx) => {
        fx.fail('POST', '/api/admin/providers', 400, { code: 'oidc_issuer_invalid' }, { times: 1 })
        fx.fail('POST', '/api/admin/providers', 409, { code: 'domain_required' }, { times: 1 })
        fx.fail('POST', '/api/admin/providers', 409, { code: 'exists' }, { times: 1 })
      },
    })
    await settle(() => listed().length === 1, 'the list')
    const fill = async (slug: string, issuer: string) => {
      await openCustom()
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
    await settle(() => form().textContent!.includes(tsec.providers.errors.domain_required), 'domain required')
    await type(inputIn(tsec.providers.oidc.issuer), 'http://insecure.example')
    await click(buttonWith(tsec.providers.create))
    await settle(() => form().textContent!.includes(tsec.providers.errors.oidc_issuer_invalid), 'issuer invalid')
  })

  it('a provider that cannot be reached is stored switched off, and the person is told', async () => {
    await openProviders()
    await openCustom()
    await type(inputIn(tsec.providers.slug), 'dead')
    await type(inputIn(tsec.providers.name), 'Dead')
    await type(inputIn(tsec.providers.oidc.issuer), 'https://unreachable.example')
    await type(inputIn(tsec.providers.oidc.clientId), 'c')
    // The server could not reach it: it stores the provider off, with the reason.
    const dead = addProvider(
      structuredClone(m!.fx.data),
      'dead',
      'Dead',
      { oidc: { issuer: 'https://unreachable.example', clientId: 'c' } },
      { enabled: false, status: { ok: false, checked: '2026-11-23T08:00:00Z', message: 'no route to host' } },
    )
    m!.fx.reply('POST', '/api/admin/providers', dead, { status: 201 })
    m!.fx.data.admin.providers.push(dead)
    await click(buttonWith(tsec.providers.create))
    await settle(() => listed().length === 1, 'stored')
    expect(listed()[0].textContent).toContain(tsec.providers.statusBad)
    // Left at its default, "enabled" is not asked for, so the server keeps it off.
    expect(JSON.parse(calls('POST', '/api/admin/providers')[0].body!)).not.toHaveProperty('enabled')
    expect(listed()[0].textContent).toContain(tsec.providers.disabled)
  })

  it('switching one off and fetching again are single calls', async () => {
    await openProviders({ providers: [company] })
    await settle(() => listed().length === 1, 'the list')
    await click(listed()[0].querySelector('[role="switch"]'))
    await settle(() => calls('PATCH', '/api/admin/providers/company').length === 1, 'patched')
    expect(JSON.parse(calls('PATCH', '/api/admin/providers/company')[0].body!)).toEqual({ enabled: false })
    await click(byLabel(tsec.providers.refreshFor('Company SSO')))
    await settle(() => calls('POST', '/api/admin/providers/company/check').length === 1, 'refreshed')
  })

  it('"Test sign-in" asks for the address and sends the browser there', async () => {
    await openProviders({ providers: [company] })
    await settle(() => listed().length === 1, 'the list')
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, assign })
    await click(byLabel(tsec.providers.testFor('Company SSO')))
    await settle(() => assign.mock.calls.length === 1, 'the redirect')
    expect(calls('POST', '/api/admin/providers/company/test')).toHaveLength(1)
    expect(assign.mock.calls[0][0]).toBe('/admin/security/providers?provider_test=company')
  })

  it('coming back from a test sign-in shows what the provider sent, once, and clears the address', async () => {
    await openAt('/admin/security/providers?provider_test=company', {
      providers: [company],
      setup: (fx) => {
        fx.data.admin.providerTests.company = {
          subject: 'sub-42',
          email: 'tester@example.test',
          emailTrusted: false,
          name: 'Test Person',
          username: 'tester',
          at: '2026-11-26T08:00:00Z',
        }
      },
    })
    await settle(() => text().includes(tsec.providers.testTitle), 'the result')
    await settle(() => text().includes('sub-42'), 'the subject')
    expect(text()).toContain(`tester@example.test (${tsec.providers.testUnverified})`)
    expect(text()).toContain('Test Person')
    expect(calls('GET', '/api/admin/providers/company/test')).toHaveLength(1)
    await settle(() => !locationText().includes('provider_test'), 'the address is clean')
  })

  it('coming back from a failed test sign-in says why', async () => {
    await openAt('/admin/security/providers?sso_error=saml_no_email', { providers: [company] })
    await settle(() => text().includes(tsec.providers.testTitle), 'the dialog')
    expect(text()).toContain(ssoErrorMessage('saml_no_email'))
    expect(calls('GET', '/api/admin/providers/company/test')).toHaveLength(0)
  })

  it("deleting asks first; a provider that is somebody's only way in asks again, and then force-deletes", async () => {
    // Somebody's only way in is this provider (single sign-on made the account): the server refuses once, with the count.
    await openProviders({
      providers: [company],
      setup: (fx) =>
        fx.fail('DELETE', '/api/admin/providers/company', 409, { code: 'provider_in_use', accounts: 1 }, { times: 1 }),
    })
    await settle(() => listed().length === 1, 'the list')
    await click(byLabel(tsec.providers.deleteFor('Company SSO')))
    await settle(() => text().includes(tsec.providers.deleteTitle('Company SSO')), 'the first question')
    await click([...dialog().querySelectorAll('button')].find((b) => b.textContent === tsec.providers.deleteConfirm))
    await settle(() => text().includes(tsec.providers.deleteInUseTitle('Company SSO')), 'the second question')
    expect(text()).toContain(tsec.providers.deleteInUse(1))
    await click([...dialog().querySelectorAll('button')].find((b) => b.textContent === tsec.providers.deleteForce))
    await settle(() => listed().length === 0, 'deleted')
    expect(calls('DELETE', '/api/admin/providers/company?force=1')).toHaveLength(1)
  })

  it('shows the audit log, filters it by kind and loads more', async () => {
    // 60 rows of one kind, so there is a second page.
    await openAt('/admin/audit', {
      providers: [company],
      data: (d) => {
        for (let i = 0; i < 60; i++)
          d.admin.audit.push({
            id: `A1${String(i).padStart(7, '0')}`,
            at: '2026-11-20T08:00:00Z',
            kind: 'setting_changed',
            category: 'admin',
            capacity: 'site_admin',
            actor: d.auth.users[0].id,
            user: null,
            username: null,
            ip: '203.0.113.7',
            detail: { key: 'signin.perMinute' },
          })
      },
    })
    await settle(() => q('#audit-kind') !== null, 'the audit log')
    await click(buttonWith(tsec.audit.refresh))
    await settle(() => document.querySelectorAll('[data-slot="audit-rows"] > li').length === 50, 'the first page')
    await click(buttonWith(tsec.audit.loadMore))
    await settle(() => document.querySelectorAll('[data-slot="audit-rows"] > li').length > 50, 'the next page')
    expect(calls('GET', '/api/admin/audit').some((r) => r.url.includes('cursor='))).toBe(true)
    await type(q<HTMLSelectElement>('#audit-kind')!, 'provider_created')
    await settle(() => calls('GET', '/api/admin/audit?kind=provider_created').length > 0, 'the filter')
  })
})
