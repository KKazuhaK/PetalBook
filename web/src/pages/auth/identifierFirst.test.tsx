// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import type { FixtureOptions } from '@/api/fixtures/data/model'
import { MOCK_ADMIN } from '@/api/fixtures/data/people'
import type { DiscoveredSSO } from '@/api/types'
import { t } from '@/i18n'
import { ts } from '@/i18n/signin'
import { click, installDomShims, locationText, mount, settle, type, type FxMounted } from '@/testing/harness'
import LoginPage from './LoginPage'
import SsoEntryPage from './SsoEntryPage'
import { RegisterPage } from './MailPages'

// Identifier-first sign-in and the SSO entry (3.6 d4): the address (or username) first; a domain the server routes to
// an organisation's single sign-on goes there, with the address as the hint; anything else goes on to the password.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
  vi.unstubAllGlobals()
})

const routes = (
  <Routes>
    <Route path="/login" element={<LoginPage />} />
    <Route path="/login/sso" element={<SsoEntryPage />} />
    <Route path="/register" element={<RegisterPage />} />
    <Route path="*" element={<div data-testid="elsewhere">elsewhere</div>} />
  </Routes>
)
const text = () => document.body.textContent ?? ''
const passwordInput = () => document.querySelector<HTMLInputElement>('input[type="password"]')
const identifierInput = () => document.querySelector<HTMLInputElement>('input[autocomplete="username webauthn"]')
const buttonWith = (label: string) =>
  [...document.querySelectorAll<HTMLElement>('button')].find((b) => b.textContent?.includes(label))
const linkWith = (label: string) =>
  [...document.querySelectorAll<HTMLAnchorElement>('a')].find((a) => a.textContent?.includes(label))

const acme: DiscoveredSSO = {
  slug: 'acme-entra',
  kind: 'oidc',
  name: 'Acme',
  icon: 'preset:microsoft',
  start: '/api/auth/oidc/acme-entra/start',
  enforced: true,
}
const beta: DiscoveredSSO = { ...acme, slug: 'beta-sso', name: 'Beta', icon: '', enforced: false }
const routed: FixtureOptions = { ssoDomains: { 'acme.example': acme, 'beta.example': beta } }

async function open(path: string, fixtures: FixtureOptions = routed) {
  m = await mount(routes, { entries: [path], fixtures: { mode: 'accounts', ...fixtures } })
  await settle(() => document.querySelector('main') !== null, 'the page')
}

async function identify(value: string) {
  await settle(() => identifierInput() !== null, 'the first step')
  await type(identifierInput()!, value)
  await click(buttonWith(ts.login.next))
}

describe('identifier-first sign-in', () => {
  it('asks for the address or username first, with no password field', async () => {
    await open('/login')
    await settle(() => identifierInput() !== null, 'the first step')
    expect(passwordInput()).toBeNull()
    expect(text()).toContain(ts.login.identifier)
    expect(linkWith(ts.login.ssoEntry)?.getAttribute('href')).toBe('/login/sso')
  })

  it('without a routed domain the page is the form it was', async () => {
    await open('/login', {})
    await settle(() => passwordInput() !== null, 'the form')
    expect(linkWith(ts.login.ssoEntry)).toBeUndefined()
  })

  it('a username goes to the password step without asking where it signs in, and signs in', async () => {
    await open('/login')
    await identify(MOCK_ADMIN.username)
    await settle(() => passwordInput() !== null, 'the password step')
    expect(m!.requests.some((r) => r.url === '/api/auth/discover')).toBe(false)
    expect(document.activeElement).toBe(passwordInput())
    expect(text()).toContain(MOCK_ADMIN.username)
    await type(passwordInput()!, MOCK_ADMIN.password)
    await click(buttonWith(t.login.submit))
    await settle(() => locationText() === '/', 'signed in')
  })

  it('an address whose organisation requires its SSO gets "Continue with" and the hint, and a password way for administrators', async () => {
    await open('/login?return=%2Fs%2Fabc')
    await identify('tang@acme.example')
    await settle(() => linkWith(ts.ended.continueWith('Acme')) !== undefined, 'the SSO step')
    expect(passwordInput()).toBeNull()
    expect(text()).toContain(ts.login.ssoRequired('Acme'))
    expect(linkWith(ts.ended.continueWith('Acme'))!.getAttribute('href')).toBe(
      '/api/auth/oidc/acme-entra/start?return=%2Fs%2Fabc&login_hint=tang%40acme.example',
    )
    expect(document.activeElement).toBe(linkWith(ts.ended.continueWith('Acme')))
    await click(buttonWith(ts.login.usePassword))
    await settle(() => passwordInput() !== null, 'the password step')
  })

  it('an address whose organisation offers SSO without requiring it gets the button above the password', async () => {
    await open('/login')
    await identify('ann@beta.example')
    await settle(() => passwordInput() !== null, 'the password step')
    expect(linkWith(ts.ended.continueWith('Beta'))!.getAttribute('href')).toBe(
      '/api/auth/oidc/beta-sso/start?return=%2F&login_hint=ann%40beta.example',
    )
  })

  it('an address of another domain goes to the password step; "Change" goes back to the first', async () => {
    await open('/login')
    await identify('someone@else.example')
    await settle(() => passwordInput() !== null, 'the password step')
    expect(linkWith(ts.ended.continueWith('Acme'))).toBeUndefined()
    await click(buttonWith(ts.login.change))
    await settle(() => identifierInput() !== null && passwordInput() === null, 'the first step again')
  })

  it('a refusal to wait is said, with the time', async () => {
    await open('/login')
    m!.fx.fail('POST', '/api/auth/discover', 429, { code: 'rate_limited' }, { headers: { 'Retry-After': '30' } })
    await identify('tang@acme.example')
    await settle(() => text().includes(t.auth.wait(30)) || /\d+ ?秒/.test(text()), 'the wait')
    expect(passwordInput()).toBeNull()
  })
})

describe('the SSO entry (/login/sso)', () => {
  it('sends an address with SSO to its identity provider with the hint', async () => {
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, assign })
    await open('/login/sso?return=%2Fs%2Fabc')
    await settle(() => document.querySelector('input[type="email"]') !== null, 'the field')
    await type(document.querySelector<HTMLInputElement>('input[type="email"]')!, 'tang@acme.example')
    await click(buttonWith(ts.login.next))
    await settle(() => assign.mock.calls.length > 0, 'the redirect')
    expect(assign).toHaveBeenCalledWith(
      '/api/auth/oidc/acme-entra/start?return=%2Fs%2Fabc&login_hint=tang%40acme.example',
    )
    expect(text()).toContain(ts.login.goingTo('Acme'))
  })

  it('says so for an address without SSO, and for something that is not an address', async () => {
    await open('/login/sso')
    await settle(() => document.querySelector('input[type="email"]') !== null, 'the field')
    const field = document.querySelector<HTMLInputElement>('input[type="email"]')!
    await type(field, 'tang@nowhere.example')
    await click(buttonWith(ts.login.next))
    await settle(() => text().includes(ts.ssoEntry.notFound), 'not found')
    await type(field, 'tang')
    await click(buttonWith(ts.login.next))
    await settle(() => text().includes(ts.ssoEntry.needAddress), 'not an address')
    expect(linkWith(ts.ended.backToSignIn)?.getAttribute('href')).toBe('/login')
  })
})

describe('registration on a domain that requires SSO', () => {
  it('answers with the way to sign in through it', async () => {
    await open('/register', {
      ...routed,
      data: (d) => {
        d.auth.register = true
      },
    })
    await settle(() => document.querySelector('input[type="email"]') !== null, 'the form')
    const inputs = [...document.querySelectorAll<HTMLInputElement>('input')]
    const byAuto = (a: string) => inputs.find((i) => i.autocomplete === a)!
    await type(byAuto('username'), 'tang')
    await type(document.querySelector<HTMLInputElement>('input[type="email"]')!, 'tang@acme.example')
    for (const p of document.querySelectorAll<HTMLInputElement>('input[type="password"]'))
      await type(p, 'a-long-password')
    await click(document.querySelector('form button[type="submit"]'))
    await settle(() => linkWith(ts.ended.continueWith('Acme')) !== undefined, 'the SSO link')
    expect(linkWith(ts.ended.continueWith('Acme'))!.getAttribute('href')).toBe(
      '/api/auth/oidc/acme-entra/start?return=%2F&login_hint=tang%40acme.example',
    )
  })
})
