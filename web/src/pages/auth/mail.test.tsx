// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import type { FixtureOptions } from '@/api/fixtures/data/model'
import { DEMO_MEMBER } from '@/api/fixtures/data/people'
import type { FixtureServer } from '@/api/fixtures/server'
import { tm } from '@/i18n/mail'
import { click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'
import LoginPage from './LoginPage'
import { ForgotPage, RegisterConfirmPage, RegisterPage, ResetPasswordPage, VerifyEmailPage } from './MailPages'

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
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

// What the site has switched on: mail (a password can be reset), registration, a terms page.
interface Site {
  mail?: boolean
  registration?: boolean
  terms?: boolean
}
const site = ({ mail, registration, terms }: Site): FixtureOptions => ({
  mode: 'accounts',
  data: (d) => {
    d.auth.forgot = mail ?? false
    d.auth.register = registration ?? false
    d.auth.termsUrl = terms ? 'https://notes.example.test/terms' : undefined
  },
})
// The page a link from a mail opens: its token is in the fragment of the address, never in a query.
async function open(
  path: string,
  on: Site & { token?: string; scene?: (fx: FixtureServer) => void; leaves?: boolean } = {},
) {
  window.location.hash = on.token ? `#${on.token}` : ''
  m = await mount(routes, { entries: (fx) => (on.scene?.(fx), [path]), fixtures: site(on) })
  // A page that leaves for the app at once has no `main` to wait for.
  if (!on.leaves) await settle(() => document.querySelector('main') !== null, 'the page')
}
const sentTo = (kind: string) => m!.fx.sent.mail.filter((x) => x.kind === kind)
// The answer a link gives that signs the person in.
const signedIn = () => {
  const { password: _password, ...user } = DEMO_MEMBER
  void _password
  return { user, hold: null }
}
const fillRegistration = async () => {
  await type(input('input[autocomplete="username"]'), 'tang')
  await type(input('input[autocomplete="email"]'), 'tang@example.test')
  const pw = [...document.querySelectorAll<HTMLInputElement>('input[type="password"]')]
  await type(pw[0], 'a-long-password')
  await type(pw[1], 'a-long-password')
}

describe('registration', () => {
  it('says registration is closed while the site does not take registrations, and sends nothing', async () => {
    await open('/register')
    await settle(() => text().includes(tm.register.closed), 'the closed page')
    expect(sentTo('register')).toHaveLength(0)
  })

  it('the form sends the registration and says to look in the mailbox', async () => {
    await open('/register', { mail: true, registration: true })
    await settle(() => input('input[autocomplete="username"]') !== null, 'the form')
    await fillRegistration()
    await submit()
    await settle(() => text().includes(tm.register.sentTitle), 'the confirmation notice')
    expect(text()).toContain('tang@example.test')
    expect(JSON.parse(m!.requests.find((r) => r.url === '/api/register' && r.method === 'POST')!.body!)).toMatchObject({
      username: 'tang',
      email: 'tang@example.test',
      lang: expect.stringMatching(/^(en|zh-Hans)$/),
    })
    expect(sentTo('register')).toHaveLength(1)
  })

  it('a confirmation link signs the person in and goes to the app', async () => {
    await open('/register/confirm', { mail: true, registration: true, token: 'tok', leaves: true })
    await settle(
      () => document.querySelector('[data-testid="elsewhere"]') !== null,
      'the app, after the account was made',
    )
    const confirm = m!.requests.find((r) => r.url === '/api/register/confirm')!
    expect(JSON.parse(confirm.body!)).toEqual({ token: 'tok' })
    expect(window.location.hash).toBe('')
  })

  it('organisation creation: the choice appears only when the site allows it, the slug follows the name, and the registration asks for the organisation', async () => {
    await open('/register', { mail: true, registration: true })
    await settle(() => input('input[autocomplete="username"]') !== null, 'the form')
    expect(document.querySelector('[role="radiogroup"]')).toBeNull()
    expect(text()).not.toContain(tm.register.createOrg)
    // The site now lets people make organisations.
    m!.fx.data.features['features.tenancy'] = true
    m!.fx.data.auth.orgCreation = true
    await m!.qc.invalidateQueries()
    await settle(() => document.querySelector('[role="radiogroup"]') !== null, 'the choice')
    await click(document.querySelectorAll('[role="radio"]')[1])
    await settle(() => input('input[autocomplete="organization"]') !== null, 'the organisation fields')
    await type(input('input[autocomplete="organization"]'), 'Acme Notes Ltd')
    const slug = document.querySelectorAll<HTMLInputElement>('input[autocapitalize="none"]')[0]
    expect(slug.value).toBe('acme-notes-ltd')
    await fillRegistration()
    await submit()
    await settle(() => text().includes(tm.register.sentTitle), 'the confirmation notice')
    expect(JSON.parse(m!.requests.find((r) => r.url === '/api/register' && r.method === 'POST')!.body!)).toMatchObject({
      org: { name: 'Acme Notes Ltd', slug: 'acme-notes-ltd' },
    })
    expect(sentTo('register_org')).toHaveLength(1)
  })

  it('a registration that makes an organisation says so when its link is followed', async () => {
    await open('/register/confirm', {
      mail: true,
      token: 'tok',
      scene: (fx) =>
        fx.reply('POST', '/api/register/confirm', { ...signedIn(), tenant: { name: 'Acme', slug: 'acme' } }),
    })
    await settle(() => text().includes(tm.confirm.orgReady('Acme')), 'the welcome')
    expect(text()).toContain(tm.confirm.orgBody('Acme', 'acme'))
  })

  it('a link that is not valid says so and offers the registration page', async () => {
    await open('/register/confirm', {
      mail: true,
      registration: true,
      token: 'nope',
      scene: (fx) => fx.fail('POST', '/api/register/confirm', 404, { code: 'not_found' }),
    })
    await settle(() => text().includes(tm.confirm.invalid), 'the answer')
    expect(text()).toContain(tm.confirm.toRegister)
  })

  it('terms and privacy: the box must be ticked, and the links open in a new tab', async () => {
    await open('/register', { mail: true, registration: true, terms: true })
    await settle(() => input('input[type="checkbox"]') !== null, 'the checkbox')
    const link = document.querySelector<HTMLAnchorElement>('a[href="https://notes.example.test/terms"]')!
    expect(link.target).toBe('_blank')
    await fillRegistration()
    await submit()
    await settle(() => text().includes(tm.register.termsRequired), 'the refusal')
    expect(m!.fx.sent.mail).toHaveLength(0)
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
    m!.fx.data.auth.forgot = true
    await m!.qc.invalidateQueries()
    await settle(() => document.querySelector('a[href="/forgot"]') !== null, 'the forgot link')
    expect(document.querySelector('a[href="/register"]')).toBeNull()
    m!.fx.data.auth.register = true
    await m!.qc.invalidateQueries()
    await settle(() => document.querySelector('a[href="/register"]') !== null, 'the register link')
  })
})

describe('password reset', () => {
  it('the forgot page asks for the account and says the same whoever it is', async () => {
    await open('/forgot', { mail: true })
    await type(input('input[autocomplete="username"]'), 'nobody')
    await submit()
    await settle(() => text().includes(tm.forgot.sentTitle), 'the notice')
    expect(JSON.parse(m!.requests.find((r) => r.url === '/api/password/forgot')!.body!)).toMatchObject({
      login: 'nobody',
    })
  })

  it('off: the page says there is no password reset', async () => {
    await open('/forgot', {
      scene: (fx) => fx.fail('POST', '/api/password/forgot', 404, { code: 'password_reset_off' }),
    })
    await type(input('input[autocomplete="username"]'), 'admin')
    await submit()
    await settle(() => text().includes(tm.forgot.off), 'the refusal')
  })

  it('the reset page checks the link and takes a new password once', async () => {
    await open('/reset-password', { mail: true, token: 'tok' })
    await settle(() => text().includes(tm.reset.intro('admin')), 'the form')
    const pw = [...document.querySelectorAll<HTMLInputElement>('input[type="password"]')]
    await type(pw[0], 'a brand new password')
    await type(pw[1], 'something else entirely')
    await submit()
    await settle(() => text().includes(tm.reset.mismatch), 'the mismatch')
    await type(pw[1], 'a brand new password')
    await submit()
    await settle(() => text().includes(tm.reset.done), 'the confirmation')
    const sent = m!.requests.filter((r) => r.url === '/api/password/reset')
    expect(sent).toHaveLength(1)
    expect(JSON.parse(sent[0].body!)).toEqual({ token: 'tok', password: 'a brand new password' })
  })

  it('a reset link that is not valid offers to ask again', async () => {
    await open('/reset-password', {
      mail: true,
      token: 'nope',
      scene: (fx) => fx.fail('POST', '/api/password/reset/check', 404, { code: 'not_found' }),
    })
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
    await open('/verify-email', {
      mail: true,
      token: 'nope',
      scene: (fx) => fx.fail('POST', '/api/email/confirm', 404, { code: 'not_found' }),
    })
    await settle(() => text().includes(tm.verify.invalid), 'the invalid link')
  })

  it('the right link confirms the address', async () => {
    await open('/verify-email', { mail: true, token: 'tok' })
    await settle(() => text().includes(tm.verify.done), 'the confirmation')
    expect(JSON.parse(m!.requests.find((r) => r.url === '/api/email/confirm')!.body!)).toEqual({ token: 'tok' })
  })
})
