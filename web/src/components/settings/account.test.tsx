// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import type { FixtureOptions } from '@/api/fixtures/data/model'
import type { Link } from '@/api/types'
import { t } from '@/i18n'
import { apiErrorText } from '@/i18n/apiErrors'
import { ts } from '@/i18n/signin'
import { byLabel, click, installDomShims, locationText, mount, settle, type, type FxMounted } from '@/testing/harness'
import SettingsRoute from './SettingsRoute'

// The account's security page: passkeys and the linked sign-ins.

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
    <Route path="/settings/:section?" element={<SettingsRoute />} />
    <Route path="/login" element={<div>sign in</div>} />
  </Routes>
)
const dialog = () => document.querySelector<HTMLElement>('[data-slot="settings-dialog"]')
const panelText = () => dialog()?.textContent ?? ''
const buttonWith = (label: string, within: ParentNode = document) =>
  [...within.querySelectorAll<HTMLElement>('button')].find((b) => b.textContent?.trim() === label)
// A button whose label contains `text` (a provider's button also holds its drawn icon).
const buttonHolding = (text: string) =>
  [...document.querySelectorAll<HTMLElement>('button')].find((b) => b.textContent?.includes(text))
const rows = () => [...document.querySelectorAll<HTMLElement>('[data-passkey]')]
const calls = (method: string, url: string) => m!.requests.filter((r) => r.method === method && r.url.startsWith(url))

const company = {
  slug: 'company',
  kind: 'oidc' as const,
  name: 'Company SSO',
  icon: 'preset:microsoft',
  oidc: { issuer: 'https://login.example/v2.0', clientId: 'id' },
}
const github = {
  slug: 'gh',
  kind: 'github' as const,
  name: 'GitHub',
  icon: 'preset:github',
  github: { clientId: 'id' },
}

async function open(path = '/settings/security', server: FixtureOptions = {}) {
  m = await mount(routes, { entries: [path], signIn: true, fixtures: { mode: 'accounts', ...server } })
  await settle(() => panelText().includes(ts.passkeys.title), 'the security page')
}

// The browser's side of creating a passkey.
function stubCreate(json: Record<string, unknown> = { id: 'cred-1', type: 'public-key', response: {} }) {
  const create = vi.fn(async () => ({ toJSON: () => json }))
  vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
  vi.stubGlobal('navigator', { ...navigator, credentials: { create }, languages: ['zh-CN'], language: 'zh-CN' })
  return create
}

describe('the passkeys of the account', () => {
  it("starts with none, and adds one with a name through the browser's prompt", async () => {
    await open()
    expect(panelText()).toContain(ts.passkeys.empty)
    const create = stubCreate({ id: 'cred-1', type: 'public-key', response: {}, synced: true })
    await type(dialog()!.querySelector<HTMLInputElement>('input[maxlength="64"]')!, 'MacBook')
    await click(buttonWith(ts.passkeys.add))
    await settle(() => rows().length === 1, 'the passkey')
    expect(JSON.parse(calls('POST', '/api/account/passkeys/begin')[0].body!)).toEqual({ name: 'MacBook' })
    // The options were decoded for the browser: the user handle and the challenge are bytes.
    const opts = (create.mock.calls[0] as unknown as [{ publicKey: { challenge: unknown; user: { id: unknown } } }])[0]
      .publicKey
    expect(opts.challenge).toBeInstanceOf(Uint8Array)
    expect(opts.user.id).toBeInstanceOf(Uint8Array)
    expect(calls('POST', '/api/account/passkeys/finish?ceremony=')).toHaveLength(1)
    expect(rows()[0].textContent).toContain('MacBook')
    // Whether the authenticator says it is synced is shown.
    expect(rows()[0].textContent).toContain(ts.passkeys.synced)
    expect(rows()[0].textContent).toContain(ts.passkeys.neverUsed)
  })

  it('renames one', async () => {
    await open()
    stubCreate()
    await click(buttonWith(ts.passkeys.add))
    await settle(() => rows().length === 1, 'the passkey')
    await click(byLabel(t.fmt.labelled(ts.passkeys.rename, 'Passkey')))
    await settle(() => document.querySelector('[data-slot="dialog-content"] input') !== null, 'the dialog')
    await type(document.querySelector<HTMLInputElement>('[data-slot="dialog-content"] input')!, 'Phone')
    await click(buttonWith(ts.passkeys.save, document.querySelector('[data-slot="dialog-content"]')!))
    await settle(() => rows()[0]?.textContent?.includes('Phone') === true, 'renamed')
    expect(JSON.parse(calls('PATCH', '/api/account/passkeys/')[0].body!)).toEqual({ name: 'Phone' })
  })

  it('deletes one after asking', async () => {
    await open()
    stubCreate()
    await click(buttonWith(ts.passkeys.add))
    await settle(() => rows().length === 1, 'the passkey')
    await click(byLabel(t.fmt.labelled(ts.passkeys.remove, 'Passkey')))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await click(buttonWith(ts.passkeys.removeConfirm, document.querySelector('[role="alertdialog"]')!))
    await settle(() => rows().length === 0, 'deleted')
    expect(calls('DELETE', '/api/account/passkeys/')).toHaveLength(1)
  })

  it('a sign-in older than ten minutes is asked to be done again, coming back to this page', async () => {
    await open('/settings/security')
    // The server wants a sign-in from the last ten minutes; the page is told so.
    m!.fx.fail('POST', '/api/account/passkeys/begin', 403, { code: 'recent_login_required' })
    stubCreate()
    await click(buttonWith(ts.passkeys.add))
    await settle(() => panelText().includes(ts.passkeys.recentLogin), 'the explanation')
    expect(rows()).toHaveLength(0)
    await click(buttonWith(ts.passkeys.recentLoginAction))
    await settle(() => locationText() === '/login?return=%2Fsettings%2Fsecurity', 'the sign-in page')
    expect(calls('POST', '/api/logout')).toHaveLength(1)
  })

  it('says so when the server refuses one more (20 is the most there)', async () => {
    await open()
    m!.fx.fail('POST', '/api/account/passkeys/begin', 409, { code: 'too_many_passkeys' })
    stubCreate({ id: 'cred-21', type: 'public-key', response: {} })
    await click(buttonWith(ts.passkeys.add))
    await settle(() => panelText().includes(ts.passkeys.tooMany), 'the limit')
    expect(rows()).toHaveLength(0)
  })

  it('a prompt the person closes says so, and nothing is added', async () => {
    await open()
    vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
    vi.stubGlobal('navigator', {
      ...navigator,
      credentials: {
        create: vi.fn(async () => Promise.reject(Object.assign(new Error('x'), { name: 'NotAllowedError' }))),
      },
      languages: ['zh-CN'],
      language: 'zh-CN',
    })
    await click(buttonWith(ts.passkeys.add))
    await settle(() => panelText().includes(ts.passkeys.cancelled), 'the message')
    expect(rows()).toHaveLength(0)
  })

  it('says they cannot be used when the server has no address for them', async () => {
    await open('/settings/security', { passkeys: false })
    await settle(() => panelText().includes(ts.passkeys.unavailable), 'the note')
    expect(buttonWith(ts.passkeys.add)).toBeUndefined()
  })
})

const linkOf = (extra: Partial<Link> = {}): Link => ({
  id: 'id1',
  providerId: 'company',
  provider: 'Company SSO',
  slug: 'company',
  kind: 'oidc',
  label: 'mock@example.com',
  signin: true,
  created: '2026-11-01T00:00:00Z',
  ...extra,
})

describe('the linked sign-ins', () => {
  it('offers to link each enabled provider, and starts the link on the account route', async () => {
    await open('/settings/security', { providers: [company, github] })
    await settle(() => panelText().includes(ts.identities.link('Company SSO')), 'the link buttons')
    expect(panelText()).toContain(ts.identities.link('GitHub'))
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, assign })
    await click(buttonHolding(ts.identities.link('Company SSO')))
    await settle(() => assign.mock.calls.length > 0, 'the redirect')
    expect(JSON.parse(calls('POST', '/api/account/links/company/start')[0].body!)).toEqual({
      purpose: 'signin',
      return: '/settings',
    })
    expect(assign.mock.calls[0][0]).toContain('/callback')
  })

  it('lists a linked account with its provider, and no link button for that provider; it can be removed', async () => {
    await open('/settings/security', {
      providers: [company, github],
      data: (d) => d.area.account.links.push(linkOf()),
    })
    await settle(
      () => panelText().includes('mock@example.com') && panelText().includes(ts.identities.link('GitHub')),
      'the link',
    )
    expect(panelText()).toContain('Company SSO')
    expect(panelText()).not.toContain(ts.identities.link('Company SSO'))
    expect(panelText()).toContain(ts.identities.link('GitHub'))
    await click([...dialog()!.querySelectorAll<HTMLElement>('button')].find((b) => b.textContent === t.settings.remove))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await click(buttonWith(t.settings.removeConfirm, document.querySelector('[role="alertdialog"]')!))
    await settle(() => !panelText().includes('mock@example.com'), 'removed')
    expect(calls('DELETE', '/api/account/links/')).toHaveLength(1)
    expect(panelText()).toContain(ts.identities.link('Company SSO'))
  })

  it('the server refusing a second link at a provider says so', async () => {
    await open('/settings/security', { providers: [company] })
    await settle(() => panelText().includes(ts.identities.link('Company SSO')), 'the link button')
    // Linked elsewhere since the page was loaded: the server answers 409.
    m!.fx.fail('POST', '/api/account/links/company/start', 409, { code: 'already_linked' })
    await click(buttonHolding(ts.identities.link('Company SSO')))
    await settle(() => panelText().includes(apiErrorText('already_linked', {})!), 'the refusal')
  })

  it('unlinking the last way to sign in says it is the only way, and keeps the link', async () => {
    await open('/settings/security', { providers: [company], data: (d) => d.area.account.links.push(linkOf()) })
    await settle(() => panelText().includes('mock@example.com'), 'the link')
    m!.fx.fail('DELETE', '/api/account/links/id1', 409, { code: 'last_method' })
    await click([...dialog()!.querySelectorAll<HTMLElement>('button')].find((b) => b.textContent === t.settings.remove))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await click(buttonWith(t.settings.removeConfirm, document.querySelector('[role="alertdialog"]')!))
    await settle(() => panelText().includes(t.settings.onlyWay), 'the refusal')
    expect(panelText()).toContain('mock@example.com')
  })
})

describe('a link that failed', () => {
  it('?sso_error= is said once, and taken out of the address', async () => {
    await open('/settings?sso_error=identity_in_use', { providers: [company] })
    await settle(() => panelText().includes(ts.ssoErrors.identity_in_use), 'the message')
    expect(panelText()).toContain(ts.identities.linkFailed)
    await settle(() => !locationText().includes('sso_error'), 'the address is clean')
  })
})
