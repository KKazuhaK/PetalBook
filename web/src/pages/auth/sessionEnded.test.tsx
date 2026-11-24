// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { useState } from 'react'
import { Navigate, Route, Routes, useLocation } from 'react-router'
import { api } from '@/api/client'
import { keys, useSession } from '@/api/hooks'
import type { EndedReason } from '@/api/types'
import { MOCK_ADMIN, type MockOptions } from '@/api/mock/server'
import { loginUrl } from '@/lib/auth'
import { forgetLastAccount, lastAccount, rememberAccount, type LastMethod } from '@/lib/lastAccount'
import { ts } from '@/i18n/signin'
import { signinEn } from '@/i18n/signin/en'
import { t } from '@/i18n'
import {
  adminRequests,
  click,
  installDomShims,
  locationText,
  makePerson,
  mount,
  settle,
  type,
  type Mounted,
} from '@/testing/harness'
import { RememberAccount } from '@/components/auth/RememberAccount'
import LoginPage from './LoginPage'

beforeAll(installDomShims)
beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
})

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
  vi.unstubAllGlobals()
})

// The app's own gate, in short: nobody signed in goes to the sign-in page with the path to come back to.
function Gate() {
  const s = useSession()
  const loc = useLocation()
  if (s.isPending) return null
  if (!s.data?.user) return <Navigate to={loginUrl(loc.pathname + loc.search)} replace />
  return <div data-testid="app">{loc.pathname}</div>
}
const routes = (
  <Routes>
    <Route path="/login" element={<LoginPage />} />
    <Route path="*" element={<Gate />} />
  </Routes>
)

const text = () => document.body.textContent ?? ''
const passwordInput = () => document.querySelector<HTMLInputElement>('input[type="password"]')
const visibleUsername = () => document.querySelector<HTMLInputElement>('input[autocomplete^="username"]:not(.sr-only)')
const buttonWith = (label: string) =>
  [...document.querySelectorAll<HTMLElement>('button')].find((b) => b.textContent?.includes(label))
const linkWith = (label: string) =>
  [...document.querySelectorAll<HTMLAnchorElement>('a')].find((a) => a.textContent?.includes(label))

const admin = { name: MOCK_ADMIN.name, username: MOCK_ADMIN.username, email: 'admin@example.test' }
const company = {
  slug: 'company',
  kind: 'oidc' as const,
  name: 'Company SSO',
  icon: 'preset:microsoft',
  oidc: { issuer: 'https://login.example/v2.0', clientId: 'id' },
}

async function endSession(mm: Mounted, reason: EndedReason) {
  await fetch('/api/_mock/end-session', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ reason }),
  })
  await mm.qc.invalidateQueries({ queryKey: keys.session })
}

// The person is in the app at `path`; then their session ends for `reason` (by itself, as far as they know), and the
// app reads the session again, which lands on the sign-in page.
async function endedAt(path: string, reason: EndedReason, last: LastMethod | null, server: MockOptions = {}) {
  if (last) rememberAccount(admin, last)
  m = await mount(routes, { entries: [path], signIn: true, server: { mode: 'accounts', ...server } })
  await settle(() => document.querySelector('[data-testid="app"]') !== null, 'the app')
  await endSession(m, reason)
  await settle(() => locationText().startsWith('/login'), 'the sign-in page')
  await settle(() => document.querySelector('main') !== null && !text().includes('…'), 'the page')
}

describe('the sign-in page after a session ended', () => {
  it.each([
    ['expired', ts.ended.expired, '登录已过期，请重新登录'],
    ['signed_out_elsewhere', ts.ended.signed_out_elsewhere, '你已在其他设备上退出登录'],
    ['password_changed', ts.ended.password_changed, '密码已修改，请用新密码登录'],
  ] as const)('%s: says why at the top and offers the same person', async (reason, line, zh) => {
    expect(line).toBe(zh)
    await endedAt('/s/abc/p/1', reason, { type: 'password' })
    await settle(() => text().includes(line), 'the line')
    const main = document.querySelector('main')!
    // Why first, then who.
    expect(main.textContent!.indexOf(line)).toBeLessThan(main.textContent!.indexOf(admin.name))
    expect(document.querySelector('[role="status"]')!.textContent).toBe(line)
    const card = document.querySelector('[role="group"]')!
    expect(card.textContent).toContain(admin.name)
    expect(card.textContent).toContain(admin.email)
    // The first letter stands in for an avatar.
    expect(card.textContent).toContain('管')
  })

  it('has the words in English too', () => {
    expect(signinEn.ended.expired).toBe('Your sign-in has expired. Please sign in again.')
    expect(signinEn.ended.signed_out_elsewhere).toBe('You were signed out on another device.')
    expect(signinEn.ended.password_changed).toBe('Your password was changed. Sign in with the new password.')
    expect(signinEn.ended.disabled).toBe('Your account has been disabled. Please contact an administrator.')
    expect(signinEn.ended.continueWith('Google')).toBe('Continue with Google')
  })

  it('disabled: the message and the way back, no account and no form, and the account is not shown even if remembered', async () => {
    await endedAt('/s/abc', 'disabled', { type: 'password' })
    await settle(() => text().includes(ts.ended.disabled), 'the message')
    expect(ts.ended.disabled).toBe('你的账号已被停用，请联系管理员')
    expect(document.querySelector('[role="group"]')).toBeNull()
    expect(text()).not.toContain(admin.email)
    expect(text()).not.toContain(admin.username)
    expect(passwordInput()).toBeNull()
    expect(document.querySelector('form')).toBeNull()
    expect(document.querySelector('button[type="submit"]')).toBeNull()
    const back = linkWith(ts.ended.backToSignIn)!
    expect(back.getAttribute('href')).toBe('/login?return=%2Fs%2Fabc&other=1')
    await click(back)
    // The ordinary page: empty, and nothing said about the last person.
    await settle(() => visibleUsername() !== null, 'the ordinary form')
    expect(visibleUsername()!.value).toBe('')
    expect(text()).not.toContain(ts.ended.disabled)
  })

  it('nothing remembered: the line and the ordinary form', async () => {
    await endedAt('/', 'expired', null)
    await settle(() => visibleUsername() !== null, 'the form')
    expect(text()).toContain(ts.ended.expired)
    expect(document.querySelector('[role="group"]')).toBeNull()
  })

  it('a plain visit says nothing about a remembered account', async () => {
    rememberAccount(admin, { type: 'password' })
    m = await mount(routes, { entries: ['/login'], server: { mode: 'accounts' } })
    await settle(() => visibleUsername() !== null, 'the form')
    expect(document.querySelector('[role="group"]')).toBeNull()
    expect(text()).not.toContain(ts.ended.expired)
    expect(visibleUsername()!.value).toBe('')
  })
})

describe('the last way of signing in is offered again', () => {
  it('password: only a password field (the username is not typed), and signing in returns to the page they were on', async () => {
    await endedAt('/s/abc/p/1?x=2', 'expired', { type: 'password' })
    await settle(() => passwordInput() !== null, 'the password field')
    expect(visibleUsername()).toBeNull()
    expect(document.querySelector<HTMLInputElement>('input.sr-only')!.value).toBe(admin.username)
    // Only the way they came in: no passkey button, no provider links.
    expect(buttonWith(ts.login.passkeyButton)).toBeUndefined()
    expect(document.querySelector('a[href^="/api/auth/"]')).toBeNull()
    await type(passwordInput()!, MOCK_ADMIN.password)
    await click(document.querySelector('form button[type="submit"]'))
    await settle(() => locationText() === '/s/abc/p/1?x=2', 'back on the page')
    expect(JSON.parse(m!.requests.filter((r) => r.url === '/api/login').at(-1)!.body!)).toEqual({
      username: admin.username,
      password: MOCK_ADMIN.password,
    })
    // A wrong password stays on the page, with the account.
  })

  it('password: a wrong password says so and keeps the page', async () => {
    await endedAt('/', 'expired', { type: 'password' })
    await settle(() => passwordInput() !== null, 'the password field')
    await type(passwordInput()!, 'not it at all')
    await click(document.querySelector('form button[type="submit"]'))
    await settle(() => document.querySelector('[role="alert"]') !== null, 'the refusal')
    expect(document.querySelector('[role="group"]')).not.toBeNull()
    expect(passwordInput()!.value).toBe('')
  })

  it('passkey: only the passkey button, which names the person, and signing in returns to the page they were on', async () => {
    rememberAccount({ name: '名 carol', username: 'carol', email: '' }, { type: 'passkey' })
    m = await mount(routes, { entries: ['/s/abc'], signIn: true, server: { mode: 'accounts' } })
    const carol = makePerson(m.server, 'carol')
    const ceremony = (
      carol.request('POST', '/api/account/passkeys/begin', { name: 'Key' }).body as { ceremony: string }
    ).ceremony
    carol.request('POST', `/api/account/passkeys/finish?ceremony=${ceremony}`, { id: 'cred-carol' })
    await settle(() => document.querySelector('[data-testid="app"]') !== null, 'the app')
    await endSession(m, 'signed_out_elsewhere')
    await settle(() => buttonWith(ts.login.passkeyButton) !== undefined, 'the passkey button')
    expect(passwordInput()).toBeNull()
    expect(document.querySelector('a[href^="/api/auth/"]')).toBeNull()
    expect(text()).toContain('名 carol')
    vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
    const get = vi.fn(async () => ({ toJSON: () => ({ id: 'cred-carol', type: 'public-key', response: {} }) }))
    vi.stubGlobal('navigator', { ...navigator, credentials: { get }, languages: ['zh-CN'], language: 'zh-CN' })
    await click(buttonWith(ts.login.passkeyButton))
    await settle(() => locationText() === '/s/abc', 'back on the page')
    expect(JSON.parse(m.requests.find((r) => r.url === '/api/auth/passkey/begin')!.body!)).toEqual({
      username: 'carol',
    })
    expect(lastAccount()?.method).toEqual({ type: 'passkey' })
  })

  it('single sign-on: one button, "Continue with <name>", that passes the email as login_hint, and nothing else', async () => {
    await endedAt(
      '/s/abc',
      'expired',
      { type: 'sso', kind: 'oidc', slug: 'company', name: 'Company SSO' },
      { providers: [company] },
    )
    await settle(() => linkWith('Company SSO') !== undefined, 'the button')
    const a = linkWith('Company SSO')!
    expect(a.querySelector('span:last-child')!.textContent).toBe(ts.ended.continueWith('Company SSO'))
    expect(ts.ended.continueWith('Google')).toBe('通过 Google 继续')
    expect(a.getAttribute('href')).toBe(
      '/api/auth/oidc/company/start?return=%2Fs%2Fabc&login_hint=admin%40example.test',
    )
    expect(passwordInput()).toBeNull()
    expect(buttonWith(ts.login.passkeyButton)).toBeUndefined()
    expect(document.querySelectorAll('a[href^="/api/auth/"]')).toHaveLength(1)
    // Leaving for the provider notes how this sign-in is going to happen, for when the person is back.
    a.addEventListener('click', (e) => e.preventDefault())
    await click(a)
    expect(JSON.parse(sessionStorage.getItem('petalbook.signinMethod')!).method).toEqual({
      type: 'sso',
      kind: 'oidc',
      slug: 'company',
      name: 'Company SSO',
    })
  })

  it('a way that no longer exists (the provider was removed) gives the ordinary form, with the line', async () => {
    await endedAt(
      '/',
      'password_changed',
      { type: 'sso', kind: 'oidc', slug: 'gone', name: 'Gone' },
      { providers: [company] },
    )
    await settle(() => visibleUsername() !== null, 'the form')
    expect(text()).toContain(ts.ended.password_changed)
    expect(document.querySelector('[role="group"]')).toBeNull()
  })

  it('"使用其他账号" gives the ordinary empty page, without the line and the account', async () => {
    await endedAt('/s/abc', 'expired', { type: 'password' })
    await settle(() => buttonWith(ts.ended.otherAccount) !== undefined, 'the link')
    expect(ts.ended.otherAccount).toBe('使用其他账号')
    await click(buttonWith(ts.ended.otherAccount))
    await settle(() => visibleUsername() !== null, 'the ordinary form')
    expect(visibleUsername()!.value).toBe('')
    expect(text()).not.toContain(ts.ended.expired)
    expect(document.querySelector('[role="group"]')).toBeNull()
    expect(locationText()).toBe('/login?return=%2Fs%2Fabc&other=1')
    // Another person can sign in there, and goes to the page too.
    await type(visibleUsername()!, MOCK_ADMIN.username)
    await type(passwordInput()!, MOCK_ADMIN.password)
    await click(document.querySelector('form button[type="submit"]'))
    await settle(() => locationText() === '/s/abc', 'back on the page')
  })
})

describe('choosing another account', () => {
  // The sign-in page, with a way to mount it afresh (as coming back to it does), keeping the address.
  function Remount() {
    const [n, setN] = useState(0)
    return (
      <>
        <button type="button" data-testid="remount" onClick={() => setN(n + 1)} />
        <LoginPage key={n} />
      </>
    )
  }
  const remountable = (
    <Routes>
      <Route path="/login" element={<Remount />} />
      <Route path="*" element={<Gate />} />
    </Routes>
  )

  it('sticks for this visit: the session read again, or the page mounted afresh, does not bring the account back', async () => {
    rememberAccount(admin, { type: 'password' })
    m = await mount(remountable, { entries: ['/s/abc'], signIn: true, server: { mode: 'accounts' } })
    await settle(() => document.querySelector('[data-testid="app"]') !== null, 'the app')
    await endSession(m, 'expired')
    await settle(() => buttonWith(ts.ended.otherAccount) !== undefined, 'the link')
    const requests = m.requests.length
    await click(buttonWith(ts.ended.otherAccount))
    await settle(() => visibleUsername() !== null, 'the ordinary form')
    // Without asking the server.
    expect(m.requests.slice(requests).filter((r) => r.url === '/api/session')).toHaveLength(0)
    // The server still says why, for the same cookie.
    await m.qc.invalidateQueries({ queryKey: keys.session })
    await click(document.querySelector('[data-testid="remount"]'))
    await settle(() => visibleUsername() !== null, 'the ordinary form again')
    expect(document.querySelector('[role="group"]')).toBeNull()
    expect(text()).not.toContain(ts.ended.expired)
  })
})

describe('a session that ended for a reason the server does not give', () => {
  it('is the plain sign-in page: no line and no remembered account', async () => {
    await endedAt('/s/abc', '' as EndedReason, { type: 'password' })
    await settle(() => visibleUsername() !== null, 'the ordinary form')
    expect(document.querySelector('[role="group"]')).toBeNull()
    expect(document.querySelector('[role="status"]')).toBeNull()
  })
})

// Phones and tablets: every control of the sign-in page, the header's too, is at least 40px high (h-10, size-10, or
// min-h-10 for a text link).
describe('touch targets', () => {
  const TALL = /(^|\s)((min-)?h|size)-(1[0-9]|[2-9][0-9])(\s|$)/
  const tooSmall = () =>
    [...document.querySelectorAll<HTMLElement>('button, a, input, select')]
      .filter((e) => !e.classList.contains('sr-only') && e.getAttribute('type') !== 'hidden')
      .filter((e) => !TALL.test(e.className))
      .map((e) => e.outerHTML.slice(0, 120))

  it('the ordinary page, with every way of signing in and the links', async () => {
    m = await mount(routes, { entries: ['/login'], server: { mode: 'accounts', providers: [company] } })
    adminRequests(m.server)('PUT', '/api/admin/mail', {
      enabled: true,
      host: 'smtp.example.test',
      from: 'notes@example.test',
    })
    adminRequests(m.server)('PUT', '/api/admin/settings/registration.open', { value: true })
    await m.qc.invalidateQueries()
    await settle(() => linkWith(ts.login.withProvider('Company SSO')) !== undefined, 'the page')
    await settle(() => buttonWith(ts.login.passkeyButton) !== undefined, 'the passkey button')
    expect(document.querySelectorAll('main a').length).toBeGreaterThanOrEqual(3)
    expect(tooSmall()).toEqual([])
  })

  it('single sign-on first, with the form folded', async () => {
    m = await mount(routes, {
      entries: ['/login'],
      server: { mode: 'accounts', providers: [company], security: { login: { mode: 'sso_first' } } },
    })
    await settle(() => buttonWith(ts.login.usePassword) !== undefined, 'the page')
    expect(tooSmall()).toEqual([])
  })

  it.each([
    ['password', { type: 'password' }],
    ['passkey', { type: 'passkey' }],
    ['single sign-on', { type: 'sso', kind: 'oidc', slug: 'company', name: 'Company SSO' }],
  ] as const)('the page for someone coming back: %s', async (_, last) => {
    await endedAt('/', 'expired', last, { providers: [company] })
    await settle(() => buttonWith(ts.ended.otherAccount) !== undefined, 'the page')
    expect(tooSmall()).toEqual([])
  })

  it('the page of a disabled account', async () => {
    await endedAt('/', 'disabled', null)
    await settle(() => linkWith(ts.ended.backToSignIn) !== undefined, 'the page')
    expect(tooSmall()).toEqual([])
  })
})

describe('what is kept in the browser', () => {
  it('is the name, the username, the email, the avatar address and the method, and nothing that is a secret', async () => {
    await endedAt('/', 'expired', { type: 'password' })
    await settle(() => passwordInput() !== null, 'the password field')
    await type(passwordInput()!, MOCK_ADMIN.password)
    await click(document.querySelector('form button[type="submit"]'))
    await settle(() => locationText() === '/', 'signed in')
    const keysKept = Object.keys(JSON.parse(localStorage.getItem('petalbook.lastAccount')!)).sort()
    expect(keysKept).toEqual(['email', 'method', 'name', 'username'])
    expect(JSON.stringify({ ...localStorage })).not.toContain(MOCK_ADMIN.password)
    expect(JSON.stringify({ ...sessionStorage })).not.toContain(MOCK_ADMIN.password)
  })

  it('a passive end keeps it; signing out on purpose forgets it', async () => {
    await endedAt('/', 'expired', { type: 'password' })
    expect(lastAccount()?.username).toBe(admin.username)
    // Back in, then out on purpose.
    await settle(() => passwordInput() !== null, 'the password field')
    await type(passwordInput()!, MOCK_ADMIN.password)
    await click(document.querySelector('form button[type="submit"]'))
    await settle(() => locationText() === '/', 'signed in')
    expect(lastAccount()?.username).toBe(admin.username)
    await api.logout()
    expect(lastAccount()).toBeNull()
    expect(localStorage.getItem('petalbook.lastAccount')).toBeNull()
  })

  it('is read with suspicion: junk, a wrong shape or a strange avatar address is not used', () => {
    for (const bad of [
      '',
      'nope',
      '[]',
      '{"name":1}',
      JSON.stringify({ name: 'a', username: 'a', email: '', method: { type: 'carrier-pigeon' } }),
    ])
      expect(() => {
        localStorage.setItem('petalbook.lastAccount', bad)
        expect(lastAccount()).toBeNull()
      }).not.toThrow()
    localStorage.setItem(
      'petalbook.lastAccount',
      JSON.stringify({
        name: 'a',
        username: 'a',
        email: '',
        avatarUrl: 'javascript:alert(1)',
        method: { type: 'passkey' },
      }),
    )
    expect(lastAccount()).toEqual({ name: 'a', username: 'a', email: '', method: { type: 'passkey' } })
    forgetLastAccount()
    expect(lastAccount()).toBeNull()
  })

  it('shows the avatar when there is an address for one', async () => {
    rememberAccount({ ...admin, avatarUrl: 'https://img.example/a.png' }, { type: 'password' })
    m = await mount(routes, { entries: ['/'], signIn: true, server: { mode: 'accounts' } })
    await settle(() => document.querySelector('[data-testid="app"]') !== null, 'the app')
    await endSession(m, 'expired')
    await settle(() => document.querySelector('[role="group"] img') !== null, 'the avatar')
    expect(document.querySelector('[role="group"] img')!.getAttribute('src')).toBe('https://img.example/a.png')
  })

  it('while someone is signed in, the app keeps who they are', async () => {
    m = await mount(
      <>
        <RememberAccount />
        {routes}
      </>,
      { entries: ['/'], signIn: true, server: { mode: 'accounts' } },
    )
    await settle(() => lastAccount() !== null, 'the account kept')
    expect(lastAccount()).toMatchObject({ username: MOCK_ADMIN.username, method: { type: 'password' } })
  })

  it('the heading of the page is still the sign-in one', async () => {
    await endedAt('/', 'expired', { type: 'password' })
    expect(document.querySelector('h1')!.textContent).toBe(t.login.title)
  })
})
