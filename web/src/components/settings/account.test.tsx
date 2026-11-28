// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import type { FixtureOptions } from '@/api/fixtures/data/model'
import type { Link } from '@/api/types'
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'
import { apiErrorText } from '@/i18n/apiErrors'
import { ts } from '@/i18n/signin'
import { keys } from '@/api/hooks'
import type { AccountInfo, AccountMethod } from '@/api/types'
import { byLabel, click, installDomShims, locationText, mount, settle, type, type FxMounted } from '@/testing/harness'
import SettingsRoute from './SettingsRoute'

// The account's security page: one list of sign-in methods (the password, the linked sign-ins, the passkeys).

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
  await settle(() => panelText().includes(ts.methods.title), 'the security page')
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
    expect(rows()).toHaveLength(0)
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
    expect(rows()[0].textContent).toContain(ts.methods.neverUsed)
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
    await click(byLabel(t.fmt.labelled(t.settings.remove, 'mock@example.com')))
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
    await click(byLabel(t.fmt.labelled(t.settings.remove, 'mock@example.com')))
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

// A server answer for the account, for what the fixture does not make: the list as the server says it.
async function answer(methods: AccountMethod[], canUnlink: boolean) {
  const account = await m!.qc.fetchQuery<AccountInfo>({ queryKey: keys.account, staleTime: 0 })
  m!.fx.reply('GET', '/api/account', {
    ...account,
    hasPassword: methods.some((x) => x.type === 'password'),
    methods,
    canUnlink,
  })
  await m!.qc.invalidateQueries({ queryKey: keys.account })
}

const rowOf = (key: string) => document.querySelector<HTMLElement>(`tbody [data-method="${key}"]`)
const cardOf = (key: string) =>
  document.querySelector<HTMLElement>(`[data-slot="methods-cards"] [data-method="${key}"]`)

describe('the list of sign-in methods', () => {
  it('is one table of the password, the linked sign-ins and the passkeys, each with when it was added and last used', async () => {
    await open('/settings/security', {
      providers: [company],
      data: (d) => {
        d.area.account.links.push(linkOf())
        d.area.account.passkeys.push({ id: 'pk1', name: 'MacBook', created: '2026-11-02T00:00:00Z', synced: true })
      },
    })
    await settle(() => rowOf('passkey:pk1') !== null, 'the list')
    // The order is the server's: password, identities, passkeys.
    const order = [...document.querySelectorAll<HTMLElement>('tbody [data-method]')].map((r) => r.dataset.method)
    expect(order).toEqual(['password', 'identity:id1', 'passkey:pk1'])
    const table = document.querySelector('table')!
    expect([...table.querySelectorAll('thead th')].map((h) => h.textContent)).toEqual([
      ts.methods.columns.method,
      ts.methods.columns.added,
      ts.methods.columns.lastUsed,
      ts.methods.columns.actions,
    ])
    // A linked sign-in says its provider and identity, and when it was linked; it was never used.
    const link = rowOf('identity:id1')!
    expect(link.textContent).toContain('Company SSO')
    expect(link.textContent).toContain('mock@example.com')
    expect(link.textContent).toContain(absoluteTime('2026-11-01T00:00:00Z'))
    expect(link.textContent).toContain(ts.methods.neverUsed)
    expect(rowOf('passkey:pk1')!.textContent).toContain(ts.passkeys.synced)
    // The password row has its time of use and a change action, but no remove.
    expect(rowOf('password')!.textContent).not.toContain(ts.methods.neverUsed)
    expect(byLabel(t.settings.changePassword)).not.toBeNull()
    // The old separate lists are gone.
    expect(document.querySelector('#passkeys-heading')).toBeNull()
  })

  it('shows when a method was last used, from the server', async () => {
    await open('/settings/security')
    await settle(() => rowOf('password') !== null, 'the list')
    await answer(
      [
        { type: 'password', added: '2026-11-01T00:00:00Z', lastUsed: '2026-11-20T10:00:00Z', removable: false },
        {
          type: 'identity',
          id: 'l1',
          provider: 'Company SSO',
          providerSlug: 'company',
          label: 'a@example.com',
          added: '2026-11-02T00:00:00Z',
          lastUsed: '2026-11-21T11:30:00Z',
          removable: true,
        },
      ],
      true,
    )
    await settle(() => rowOf('identity:l1') !== null, 'the new list')
    expect(rowOf('identity:l1')!.textContent).toContain(absoluteTime('2026-11-21T11:30:00Z'))
    expect(rowOf('password')!.textContent).toContain(absoluteTime('2026-11-20T10:00:00Z'))
  })

  it('the only way to sign in cannot be removed, and the row says why', async () => {
    await open('/settings/security')
    await settle(() => rowOf('password') !== null, 'the list')
    await answer(
      [
        {
          type: 'identity',
          id: 'l1',
          provider: 'Company SSO',
          providerSlug: 'company',
          label: 'a@example.com',
          added: '2026-11-02T00:00:00Z',
          lastUsed: null,
          removable: false,
        },
      ],
      false,
    )
    await settle(() => rowOf('identity:l1') !== null, 'the new list')
    expect(rowOf('identity:l1')!.textContent).toContain(ts.methods.onlyWay)
    expect(byLabel(t.fmt.labelled(t.settings.remove, 'a@example.com'))!.hasAttribute('disabled')).toBe(true)
    // No password: one is offered to be set.
    expect(buttonHolding(t.settings.setPassword) === undefined).toBe(false)
  })

  it('on a phone each method is a card with its actions in a menu; the table is for wider boxes', async () => {
    await open('/settings/security', {
      data: (d) => {
        d.area.account.links.push(linkOf())
        d.area.account.passkeys.push({ id: 'pk1', name: 'MacBook', created: '2026-11-02T00:00:00Z', synced: false })
      },
    })
    await settle(() => cardOf('passkey:pk1') !== null, 'the cards')
    const cards = document.querySelector('[data-slot="methods-cards"]')!
    expect(cards.className).toContain('@min-[560px]/methods:hidden')
    expect(document.querySelector('[data-slot="methods-table"]')!.className).toContain('@min-[560px]/methods:block')
    expect(cards.querySelectorAll('[data-method]')).toHaveLength(3)
    // Each card says when it was added and when it was last used.
    expect(cardOf('identity:id1')!.textContent).toContain(ts.methods.added(absoluteTime('2026-11-01T00:00:00Z')))
    expect(cardOf('identity:id1')!.textContent).toContain(ts.methods.neverUsed)
    // The actions are in the menu of the card.
    expect(cardOf('passkey:pk1')!.querySelectorAll('button')).toHaveLength(1)
    await click(cardOf('passkey:pk1')!.querySelector('button'))
    await settle(() => document.querySelector('[role="menuitem"]') !== null, 'the menu')
    const items = [...document.querySelectorAll('[role="menuitem"]')].map((i) => i.textContent)
    expect(items).toEqual([ts.passkeys.rename, ts.passkeys.remove])
  })
})
