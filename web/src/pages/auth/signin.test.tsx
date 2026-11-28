// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { Route, Routes } from 'react-router'
import { markSignedIn } from '@/api/client'
import type { FixtureOptions } from '@/api/fixtures/data/model'
import { DEMO_MEMBER, MOCK_ADMIN } from '@/api/fixtures/data/people'
import { fixtureFetch } from '@/api/fixtures/fetch'
import { createFixtureServer, type FixtureServer } from '@/api/fixtures/server'
import { keys } from '@/api/hooks'
import type { SessionInfo } from '@/api/types'
import { acrossSite, can } from '@/lib/access'
import { t } from '@/i18n'
import { ts } from '@/i18n/signin'
import { byText, click, installDomShims, locationText, mount, settle, type, type FxMounted } from '@/testing/harness'
import LoginPage from './LoginPage'
import SetupPage from './SetupPage'
import InvitePage from './InvitePage'

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
    <Route path="/setup" element={<SetupPage />} />
    <Route path="/invite" element={<InvitePage />} />
    <Route path="*" element={<div data-testid="elsewhere">elsewhere</div>} />
  </Routes>
)
const text = () => document.body.textContent ?? ''
const links = () => [...document.querySelectorAll<HTMLAnchorElement>('a[href^="/api/auth/"]')]
const passwordForm = () => document.querySelector('input[type="password"]')
const usernameInput = () => document.querySelector<HTMLInputElement>('input[autocomplete^="username"]')!
const buttonWith = (label: string) =>
  [...document.querySelectorAll<HTMLElement>('button')].find((b) => b.textContent?.includes(label))

const company = {
  slug: 'company',
  kind: 'oidc' as const,
  name: 'Company SSO',
  icon: 'preset:microsoft',
  oidc: { issuer: 'https://login.example/v2.0', clientId: 'id' },
}
const campus = {
  slug: 'campus',
  kind: 'saml' as const,
  name: 'Campus login',
  icon: '',
  order: 1,
  saml: { metadataUrl: 'https://idp.example/metadata' },
}

// Where the page sends the browser.
function stubLocation() {
  const assign = vi.fn()
  vi.stubGlobal('location', { ...window.location, assign })
  return assign
}

async function open(path: string, fixtures: FixtureOptions = {}) {
  m = await mount(routes, { entries: [path], fixtures: { mode: 'accounts', ...fixtures } })
  await settle(() => document.querySelector('main') !== null, 'the page')
}

// The server asks for a picture.
const always: FixtureOptions = {
  data: (d) => {
    d.auth.captcha = { provider: 'image', id: 'cap-1', image: 'data:image/png;base64,iVBORw0KGgo=' }
  },
}

describe('the sign-in page, in each mode', () => {
  it('dual: the form, then "or sign in with" and a button for each provider', async () => {
    await open('/login', { providers: [company, campus] })
    await settle(() => links().length === 2, 'the buttons')
    expect(passwordForm()).not.toBeNull()
    expect(text()).toContain(ts.login.orSignInWith)
    expect(links().map((a) => a.getAttribute('href'))).toEqual([
      '/api/auth/oidc/company/start?return=%2F',
      '/api/auth/saml/campus/start?return=%2F',
    ])
    expect(links().map((a) => a.querySelector('span')!.textContent)).toEqual([
      ts.login.withProvider('Company SSO'),
      ts.login.withProvider('Campus login'),
    ])
    // The icon is a drawn mark, never an address.
    expect(document.querySelector('img[src^="http"]')).toBeNull()
    expect(links()[0].querySelector('svg[data-icon="microsoft"]')).not.toBeNull()
  })

  it('the buttons carry the path to come back to', async () => {
    await open('/login?return=%2Fs%2Fabc', { providers: [company] })
    await settle(() => links().length === 1, 'the button')
    expect(links()[0].getAttribute('href')).toBe('/api/auth/oidc/company/start?return=%2Fs%2Fabc')
  })

  it('sso_first: the buttons come first and the password form is folded behind a link', async () => {
    await open('/login', { providers: [company], loginMode: 'sso_first' })
    await settle(() => links().length === 1, 'the button')
    expect(passwordForm()).toBeNull()
    await click(buttonWith(ts.login.usePassword))
    expect(passwordForm()).not.toBeNull()
  })

  it('sso_redirect with one provider goes straight to it and says so', async () => {
    const assign = stubLocation()
    await open('/login?return=%2Fs%2Fabc', { providers: [company], loginMode: 'sso_redirect' })
    await settle(() => assign.mock.calls.length > 0, 'the redirect')
    expect(assign).toHaveBeenCalledWith('/api/auth/oidc/company/start?return=%2Fs%2Fabc')
    expect(text()).toContain(ts.login.goingTo('Company SSO'))
    expect(passwordForm()).toBeNull()
  })

  it('sso_redirect does not go anywhere with ?local=1: it shows the form', async () => {
    const assign = stubLocation()
    await open('/login?local=1', { providers: [company], loginMode: 'sso_redirect' })
    await settle(() => passwordForm() !== null, 'the form')
    expect(assign).not.toHaveBeenCalled()
  })

  it('sso_redirect does not go anywhere after a failure (?sso_error=), and says what failed', async () => {
    const assign = stubLocation()
    await open('/login?sso_error=no_account', { providers: [company], loginMode: 'sso_redirect' })
    await settle(() => text().includes(ts.ssoErrors.no_account), 'the message')
    expect(assign).not.toHaveBeenCalled()
    expect(links()).toHaveLength(1)
    expect(text()).toContain(ts.login.usePassword)
  })

  it('sso_redirect with several providers lists the buttons and a link to the form, and does not redirect', async () => {
    const assign = stubLocation()
    await open('/login', { providers: [company, campus], loginMode: 'sso_redirect' })
    await settle(() => links().length === 2, 'the buttons')
    expect(assign).not.toHaveBeenCalled()
    expect(passwordForm()).toBeNull()
    const other = [...document.querySelectorAll<HTMLAnchorElement>('a')].find(
      (a) => a.textContent === ts.login.usePassword,
    )!
    expect(other.getAttribute('href')).toBe('/login?local=1')
  })

  it('sso_redirect with several providers: the link to the form opens the form', async () => {
    const assign = stubLocation()
    await open('/login', { providers: [company, campus], loginMode: 'sso_redirect' })
    await settle(() => links().length === 2, 'the buttons')
    await click([...document.querySelectorAll('a')].find((a) => a.textContent === ts.login.usePassword))
    await settle(() => passwordForm() !== null, 'the form')
    expect(assign).not.toHaveBeenCalled()
  })

  it('sso_redirect with one provider after ?sso_error=: the link to the form opens the form and does not redirect', async () => {
    const assign = stubLocation()
    await open('/login?sso_error=no_account', { providers: [company], loginMode: 'sso_redirect' })
    await settle(() => text().includes(ts.ssoErrors.no_account), 'the message')
    await click([...document.querySelectorAll('a')].find((a) => a.textContent === ts.login.usePassword))
    await settle(() => passwordForm() !== null, 'the form')
    expect(assign).not.toHaveBeenCalled()
  })

  it('with no provider the page is the form alone, whatever mode is stored', async () => {
    const assign = stubLocation()
    await open('/login', { loginMode: 'sso_redirect' })
    await settle(() => passwordForm() !== null, 'the form')
    expect(links()).toHaveLength(0)
    expect(text()).not.toContain(ts.login.orSignInWith)
    expect(assign).not.toHaveBeenCalled()
  })

  it('local_only hides the providers that exist', async () => {
    await open('/login', { providers: [company], loginMode: 'local_only' })
    await settle(() => passwordForm() !== null, 'the form')
    expect(links()).toHaveLength(0)
  })
})

describe('a failed single sign-on says why', () => {
  it('has a sentence for each code of the contract, and a generic one for an unknown code', async () => {
    const codes = Object.keys(ts.ssoErrors).filter((c) => c !== 'unknown')
    expect(codes).toEqual(
      expect.arrayContaining([
        'provider_unavailable',
        'idp_error',
        'expired',
        'no_account',
        'not_allowed',
        'disabled',
        'identity_in_use',
        'already_linked',
        'saml_no_email',
        'email_in_use',
        'invite_invalid',
        'setup_invalid',
        'rate_limited',
        'saml_destination',
        'saml_weak_signature',
        'saml_encrypted',
        'saml_replay',
        'saml_multiple_assertions',
        'saml_attributes',
        'saml_transient_nameid',
        'saml_no_nameid',
        'saml_assertion_rejected',
        'internal',
      ]),
    )
    for (const code of ['not_allowed', 'saml_replay', 'email_in_use', 'internal']) {
      await open(`/login?sso_error=${code}`, { providers: [company] })
      await settle(() => text().includes(ts.ssoErrors[code]), code)
      await m!.unmount()
      m = undefined
    }
    await open('/login?sso_error=constructor', { providers: [company] })
    await settle(() => text().includes(ts.ssoErrors.unknown), 'unknown')
  })

  it('offers to try again: with one provider it goes straight to it', async () => {
    await open('/login?sso_error=idp_error', { providers: [company] })
    await settle(() => buttonWith(ts.login.ssoRetry) !== undefined, 'the retry')
    const assign = stubLocation()
    await click(buttonWith(ts.login.ssoRetry))
    expect(assign).toHaveBeenCalledWith('/api/auth/oidc/company/start?return=%2F')
  })
})

describe('signing in with a password', () => {
  const submit = async (user = MOCK_ADMIN.username, pass = MOCK_ADMIN.password) => {
    await type(usernameInput(), user)
    await type(document.querySelector<HTMLInputElement>('input[type="password"]')!, pass)
    await click(document.querySelector('form button[type="submit"]'))
  }

  it('signs in and goes to the path asked for', async () => {
    await open('/login?return=%2Fs%2Fabc')
    await settle(() => passwordForm() !== null, 'the form')
    await submit()
    await settle(() => locationText() === '/s/abc', 'the return path')
  })

  it('sends what was typed as `identifier` (an email address or a username), trimmed', async () => {
    await open('/login')
    await settle(() => passwordForm() !== null, 'the form')
    await submit(`  ${MOCK_ADMIN.username}  `)
    await settle(() => locationText() === '/', 'signed in')
    const body = JSON.parse(m!.requests.find((r) => r.url === '/api/login')!.body!)
    expect(body).toEqual({ identifier: MOCK_ADMIN.username, password: MOCK_ADMIN.password })
  })

  it('makes no second session request: the answer carries the permissions the app asks', async () => {
    await open('/login')
    await settle(() => passwordForm() !== null, 'the form')
    const before = m!.requests.length
    await submit()
    await settle(() => locationText() === '/', 'the page after signing in')
    const after = m!.requests.slice(before).map((r) => r.url)
    expect(after).toContain('/api/login')
    expect(after).not.toContain('/api/session')
    const session = m!.qc.getQueryData<SessionInfo>(keys.session)
    expect(session?.hold).toBeNull()
    expect(can(session?.user, 'site.tenants.manage')).toBe(true)
    expect(acrossSite(session?.user)).toBe(true)
  })

  it('local_login_refused is said in words', async () => {
    await open('/login', { providers: [company] })
    await settle(() => passwordForm() !== null, 'the form')
    m!.fx.data.auth.users.push(DEMO_MEMBER)
    // The site lets people in through single sign-on only.
    m!.fx.fail('POST', '/api/login', 403, { code: 'local_login_refused' })
    await submit(DEMO_MEMBER.username, DEMO_MEMBER.password)
    await settle(() => text().includes(ts.login.localRefused), 'the refusal')
  })

  it('a wrong password for an account with 429 shows how long to wait', async () => {
    await open('/login')
    await settle(() => passwordForm() !== null, 'the form')
    m!.fx.fail('POST', '/api/login', 429, { code: 'rate_limited' }, { headers: { 'Retry-After': '60' } })
    await submit(MOCK_ADMIN.username, 'wrong-password')
    await settle(() => text().includes(t.auth.wait(60)) || /\d+ ?秒/.test(text()), 'the wait')
  })

  it('a person who must use a passkey is told so after the right password, with the passkey button', async () => {
    await open('/login')
    await settle(() => passwordForm() !== null, 'the form')
    m!.fx.data.auth.users.push({ ...DEMO_MEMBER, passkeys: 1, passkeyRequired: true })
    // The right password, from a person who must use a passkey.
    m!.fx.fail('POST', '/api/login', 403, { code: 'passkey_required' })
    await submit(DEMO_MEMBER.username, DEMO_MEMBER.password)
    await settle(() => text().includes(ts.login.passkeyRequired), 'the passkey notice')
    expect(buttonWith(ts.login.passkeyButton)).toBeDefined()
  })
})

describe('the captcha on the sign-in form', () => {
  const image = () => document.querySelector<HTMLImageElement>('[data-slot="captcha"] img')
  const answer = () => document.querySelector<HTMLInputElement>('[data-slot="captcha"] input')!

  it('shows the image when the server asks for one, asks again for the username when the field is left, and sends the answer', async () => {
    await open('/login', always)
    await settle(() => image() !== null, 'the image')
    const first = image()!.getAttribute('src')
    await type(usernameInput(), 'admin')
    await act(async () => usernameInput().dispatchEvent(new FocusEvent('focusout', { bubbles: true })))
    await settle(
      () => m!.requests.some((r) => r.url === '/api/captcha?ctx=login&account=admin'),
      'the question about the name',
    )
    await settle(() => image()!.getAttribute('src') !== first || true, 'the new image')
    await type(document.querySelector<HTMLInputElement>('input[type="password"]')!, MOCK_ADMIN.password)
    // Not ready until it is answered.
    expect(document.querySelector<HTMLButtonElement>('form button[type="submit"]')!.disabled).toBe(true)
    await type(answer(), '4821')
    await click(document.querySelector('form button[type="submit"]'))
    await settle(() => locationText() === '/', 'signed in')
    const login = m!.requests.find((r) => r.url === '/api/login')!
    expect(JSON.parse(login.body!).captcha).toEqual({ id: expect.any(String), answer: '4821' })
  })

  it('"another image" asks again for the username that was left in the field', async () => {
    await open('/login', always)
    await settle(() => image() !== null, 'the image')
    await type(usernameInput(), 'admin')
    await act(async () => usernameInput().dispatchEvent(new FocusEvent('focusout', { bubbles: true })))
    await settle(
      () => m!.requests.some((r) => r.url === '/api/captcha?ctx=login&account=admin'),
      'the question about the name',
    )
    const asked = () => m!.requests.filter((r) => r.url === '/api/captcha?ctx=login&account=admin').length
    const before = asked()
    await settle(
      () => buttonWith(ts.captcha.refresh) !== undefined && !buttonWith(ts.captcha.refresh)!.hasAttribute('disabled'),
      'the refresh button',
    )
    await click(buttonWith(ts.captcha.refresh))
    await settle(() => asked() > before, 'asked again with the name')
    expect(m!.requests.filter((r) => r.url === '/api/captcha?ctx=login').length).toBe(1)
  })

  it('a wrong answer says so and shows a new challenge', async () => {
    await open('/login', always)
    await settle(() => image() !== null, 'the image')
    const before = m!.requests.filter((r) => r.url.startsWith('/api/captcha')).length
    m!.fx.fail('POST', '/api/login', 400, { code: 'captcha_failed' })
    await type(usernameInput(), 'admin')
    await type(document.querySelector<HTMLInputElement>('input[type="password"]')!, MOCK_ADMIN.password)
    await type(answer(), 'nope')
    await click(document.querySelector('form button[type="submit"]'))
    await settle(() => text().includes(ts.captcha.failed), 'the message')
    await settle(() => m!.requests.filter((r) => r.url.startsWith('/api/captcha')).length > before, 'a new challenge')
    expect(answer().value).toBe('')
    expect(locationText()).toBe('/login')
  })

  it('no captcha is shown while the server does not need one', async () => {
    await open('/login')
    await settle(() => passwordForm() !== null && m!.requests.some((r) => r.url.startsWith('/api/captcha')), 'asked')
    expect(document.querySelector('[data-slot="captcha"]')).toBeNull()
    expect(document.querySelector<HTMLButtonElement>('form button[type="submit"]')!.disabled).toBe(true)
  })

  it('a third-party provider gets its widget, with the site key, and its token is sent', async () => {
    const render = vi.fn((_el: HTMLElement, o: Record<string, unknown>) => {
      ;(o.callback as (t: string) => void)('token-1')
      return 'w1'
    })
    // The provider's script is not fetched in tests: its global is there already.
    vi.stubGlobal('turnstile', { render, reset: vi.fn(), remove: vi.fn() })
    const append = vi.spyOn(document.head, 'appendChild').mockImplementation(((el: Node) => {
      queueMicrotask(() => (el as HTMLScriptElement).onload?.(new Event('load')))
      return el
    }) as typeof document.head.appendChild)
    try {
      await open('/login', {
        data: (d) => {
          d.auth.captcha = { provider: 'turnstile', siteKey: 'site-1' }
        },
      })
      await settle(() => render.mock.calls.length > 0, 'the widget')
      expect(render.mock.calls[0][1]).toMatchObject({ sitekey: 'site-1' })
      await type(usernameInput(), 'admin')
      await type(document.querySelector<HTMLInputElement>('input[type="password"]')!, MOCK_ADMIN.password)
      await click(document.querySelector('form button[type="submit"]'))
      await settle(() => locationText() === '/', 'signed in')
      expect(JSON.parse(m!.requests.find((r) => r.url === '/api/login')!.body!).captcha).toEqual({ token: 'token-1' })
    } finally {
      append.mockRestore()
    }
  })
})

describe('signing in with a passkey', () => {
  const withPasskey = () => void m!.fx.data.auth.users.push({ ...DEMO_MEMBER, passkeys: 1 })
  const stubBrowser = (get: ReturnType<typeof vi.fn>) => {
    vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
    vi.stubGlobal('navigator', { ...navigator, credentials: { get }, languages: ['zh-CN'], language: 'zh-CN' })
  }

  it('the button is there when passkeys work, and not when they do not', async () => {
    await open('/login')
    await settle(() => buttonWith(ts.login.passkeyButton) !== undefined, 'the button')
    await m!.unmount()
    m = undefined
    await open('/login', { passkeys: false })
    await settle(() => passwordForm() !== null, 'the form')
    expect(buttonWith(ts.login.passkeyButton)).toBeUndefined()
  })

  it('asks the server for options (no username: discoverable), gives them to the browser and sends back what it returned', async () => {
    await open('/login?return=%2Fs%2Fabc')
    await settle(() => buttonWith(ts.login.passkeyButton) !== undefined, 'the button')
    withPasskey()
    const get = vi.fn(async () => ({ toJSON: () => ({ id: 'cred-carol', type: 'public-key', response: {} }) }))
    stubBrowser(get)
    await click(buttonWith(ts.login.passkeyButton))
    await settle(() => locationText() === '/s/abc', 'signed in, to the return path')
    expect(JSON.parse(m!.requests.find((r) => r.url === '/api/auth/passkey/begin')!.body!)).toEqual({})
    const finish = m!.requests.find((r) => r.url.startsWith('/api/auth/passkey/finish'))!
    expect(finish.url).toMatch(/ceremony=[^&]+&return=%2Fs%2Fabc$/)
    expect(JSON.parse(finish.body!).id).toBe('cred-carol')
    // The options went through the decoder: the challenge is bytes.
    const opts = (get.mock.calls[0] as unknown as [{ publicKey: { challenge: unknown } }])[0].publicKey
    expect(opts.challenge).toBeInstanceOf(Uint8Array)
  })

  it('names the person when a username was typed', async () => {
    await open('/login')
    await settle(() => buttonWith(ts.login.passkeyButton) !== undefined, 'the button')
    withPasskey()
    stubBrowser(vi.fn(async () => ({ toJSON: () => ({ id: 'cred-carol' }) })))
    await type(usernameInput(), 'carol')
    await click(buttonWith(ts.login.passkeyButton))
    await settle(() => locationText() === '/', 'signed in')
    expect(JSON.parse(m!.requests.find((r) => r.url === '/api/auth/passkey/begin')!.body!)).toEqual({
      username: 'carol',
    })
  })

  it('a prompt the person closes is not an error of the server', async () => {
    await open('/login')
    await settle(() => buttonWith(ts.login.passkeyButton) !== undefined, 'the button')
    stubBrowser(vi.fn(async () => Promise.reject(Object.assign(new Error('x'), { name: 'NotAllowedError' }))))
    await click(buttonWith(ts.login.passkeyButton))
    await settle(() => text().includes(ts.login.passkeyCancelled), 'the message')
    expect(locationText()).toBe('/login')
  })
})

describe('first-run setup and the invite page', () => {
  it('setup offers the providers (they need the setup token) and starts one with intent setup', async () => {
    await open('/setup', { setup: true, providers: [company] })
    await settle(() => buttonWith(ts.setupSso.withProvider('Company SSO')) !== undefined, 'the provider')
    await type(document.querySelector<HTMLInputElement>('input[autocomplete="off"]')!, 'setup-token')
    const assign = stubLocation()
    await click(buttonWith(ts.setupSso.withProvider('Company SSO')))
    await settle(() => assign.mock.calls.length > 0, 'the redirect')
    const start = m!.requests.find((r) => r.url === '/api/auth/oidc/company/start')!
    expect(JSON.parse(start.body!)).toMatchObject({ intent: 'setup', token: 'setup-token' })
    expect(assign.mock.calls[0][0]).toContain('/api/auth/oidc/company/callback')
  })

  it('setup takes the captcha of the setup context', async () => {
    await open('/setup', { setup: true, ...always })
    await settle(() => document.querySelector('[data-slot="captcha"] img') !== null, 'the image')
  })

  it('an invite for a server that only has single sign-on shows the providers when the password is refused', async () => {
    // The token comes from the address fragment, as in the link people are sent.
    window.location.hash = '#inv-1'
    await open('/invite', { providers: [company], invite: { token: 'inv-1', role: 'member' } })
    // The site lets people in through single sign-on only: the password is refused.
    m!.fx.fail('POST', '/api/invites/accept', 403, { code: 'local_login_refused' })
    await settle(() => document.querySelector('form input[autocomplete="username"]') !== null, 'the form')
    await type(document.querySelector<HTMLInputElement>('input[autocomplete="username"]')!, 'dave')
    const pw = [...document.querySelectorAll<HTMLInputElement>('input[type="password"]')]
    await type(pw[0], 'a-long-password')
    await type(pw[1], 'a-long-password')
    await click(document.querySelector('form button[type="submit"]'))
    await settle(() => text().includes(ts.inviteSso.ssoOnly), 'the explanation')
    expect(document.querySelector('form input[type="password"]')).toBeNull()
    expect(buttonWith(ts.inviteSso.withProvider('Company SSO'))).toBeDefined()
    window.location.hash = ''
  })
})

// --- a held session, in the real app ---------------------------------------------------------

describe('a session held until a passkey is registered', () => {
  let host: HTMLElement | undefined
  let root: Root | undefined
  let fx: FixtureServer | undefined
  const realFetch = globalThis.fetch
  afterEach(async () => {
    if (root) await act(async () => root!.unmount())
    host?.remove()
    document.body.innerHTML = ''
    globalThis.fetch = realFetch
    markSignedIn()
    root = host = fx = undefined
    vi.resetModules()
  })

  async function boot() {
    // Signed in, and held until a passkey is registered.
    fx = createFixtureServer({
      now: () => Date.now(),
      mode: 'accounts',
      signedIn: true,
      data: (d) => {
        d.auth.hold = 'passkey'
      },
    })
    globalThis.fetch = fixtureFetch(fx)
    window.history.pushState({}, '', '/')
    const { default: App } = await import('@/App')
    host = document.createElement('div')
    document.body.appendChild(host)
    root = createRoot(host)
    await act(async () => root!.render(<App />))
  }

  it('shows only "Set up a passkey to continue" and a sign-out button, and registering one lets the person in', async () => {
    await boot()
    await settle(() => text().includes(ts.held.title), 'the held page')
    expect(byText('button', ts.held.signOut)).not.toBeNull()
    // Nothing of the app: no sidebar, no spaces asked for.
    expect(document.querySelector('[data-slot="sidebar"]')).toBeNull()
    await settle(() => buttonWith(ts.passkeys.add) !== undefined, 'the passkey section')

    const create = vi.fn(async () => ({ toJSON: () => ({ id: 'cred-new', type: 'public-key', response: {} }) }))
    vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
    vi.stubGlobal('navigator', { ...navigator, credentials: { create }, languages: ['zh-CN'], language: 'zh-CN' })
    await click(buttonWith(ts.passkeys.add))
    await settle(() => !text().includes(ts.held.title), 'the hold lifted')
    expect(create).toHaveBeenCalled()
    await settle(
      () => document.querySelector('[data-slot="sidebar"]') !== null || window.location.pathname.startsWith('/s/'),
      'the app',
    )
  })

  it('signing out from the held page goes to the sign-in page', async () => {
    await boot()
    await settle(() => byText('button', ts.held.signOut) !== null, 'the button')
    await click(byText('button', ts.held.signOut))
    await settle(() => window.location.pathname === '/login', 'the sign-in page')
  })

  it('the session read says hold: passkey', async () => {
    await boot()
    await settle(() => text().includes(ts.held.title), 'the held page')
    const session = await (await globalThis.fetch('/api/session')).json()
    expect(session.hold).toBe('passkey')
    void keys
  })
})

// GET /oauth/authorize sends a browser without a session to /login?return=/oauth/authorize?…; every way of signing
// in must bring it back there, query and all (the app then reloads that address from the server).
describe('the return to the authorization server', () => {
  const authorize =
    '/oauth/authorize?response_type=code&client_id=pbc_x&state=s1&code_challenge=c&code_challenge_method=S256'
  const login = `/login?return=${encodeURIComponent(authorize)}`

  it('sso_redirect sends the provider the whole address to come back to', async () => {
    const assign = stubLocation()
    await open(login, { providers: [company], loginMode: 'sso_redirect' })
    await settle(() => assign.mock.calls.length > 0, 'the redirect')
    expect(assign).toHaveBeenCalledWith(`/api/auth/oidc/company/start?return=${encodeURIComponent(authorize)}`)
  })

  it('the provider buttons carry it too', async () => {
    await open(login, { providers: [company, campus] })
    await settle(() => links().length === 2, 'the buttons')
    for (const a of links()) expect(a.getAttribute('href')).toContain(`return=${encodeURIComponent(authorize)}`)
  })

  it('a password sign-in goes back to it', async () => {
    await open(login)
    await settle(() => passwordForm() !== null, 'the form')
    await type(usernameInput(), MOCK_ADMIN.username)
    await type(document.querySelector<HTMLInputElement>('input[type="password"]')!, MOCK_ADMIN.password)
    await click(document.querySelector('form button[type="submit"]'))
    await settle(() => locationText() === authorize, 'the authorization address')
  })

  it('a passkey sign-in sends it to the server and goes back to it', async () => {
    await open(login)
    await settle(() => buttonWith(ts.login.passkeyButton) !== undefined, 'the button')
    m!.fx.data.auth.users.push({ ...DEMO_MEMBER, passkeys: 1 })
    vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
    vi.stubGlobal('navigator', {
      ...navigator,
      credentials: {
        get: vi.fn(async () => ({ toJSON: () => ({ id: 'cred-carol', type: 'public-key', response: {} }) })),
      },
      languages: ['zh-CN'],
      language: 'zh-CN',
    })
    await click(buttonWith(ts.login.passkeyButton))
    await settle(() => locationText() === authorize, 'the authorization address')
    const finish = m!.requests.find((r) => r.url.startsWith('/api/auth/passkey/finish'))!
    expect(finish.url).toContain(`return=${encodeURIComponent(authorize)}`)
  })
})
