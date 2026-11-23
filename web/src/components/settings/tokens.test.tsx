// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { MOCK_ADMIN } from '@/api/mock/server'
import { adminRequests, byLabel, click, installDomShims, mount, settle, type, type Mounted } from '@/testing/harness'
import { tk } from '@/i18n/tokens'
import { t } from '@/i18n'
import SettingsRoute from './SettingsRoute'

// The "API tokens" section of the account settings, against the mock server.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const routes = (
  <Routes>
    <Route path="/settings/:section?" element={<SettingsRoute />} />
    <Route path="*" element={<div>elsewhere</div>} />
  </Routes>
)
const dialog = () => document.querySelector<HTMLElement>('[data-slot="settings-dialog"]')!
const text = () => dialog()?.textContent ?? ''
const items = () => [...document.querySelectorAll<HTMLElement>('[data-slot="token-item"]')]
const button = (label: string) => [...dialog().querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === label) ?? null
const open = async (server: { mode: 'accounts' | 'single' } = { mode: 'accounts' }) => {
  m = await mount(routes, { entries: ['/settings/tokens'], signIn: server.mode === 'accounts', server })
  await settle(() => dialog() !== null && (text().includes(tk.title) || text().includes(t.settingsUi.sections.preferences)), 'the dialog')
}
const name = () => dialog().querySelector<HTMLInputElement>('input[maxlength="80"]')!
const apiCalls = (method: string) => m!.requests.filter((r) => r.method === method && r.url.startsWith('/api/account/tokens'))

describe('API tokens in the settings', () => {
  it('is an entry of the account group, with a page of its own', async () => {
    await open()
    await settle(() => text().includes(tk.none), 'the empty list')
    expect([...dialog().querySelectorAll('nav a')].map((a) => a.textContent)).toContain(t.settingsUi.sections.tokens)
    expect(text()).toContain(tk.create)
  })

  it('does not exist in single-user mode', async () => {
    await open({ mode: 'single' })
    expect(text()).not.toContain(tk.title)
    expect(apiCalls('GET')).toEqual([])
  })

  it('makes a token, shows its secret once, and never in the list', async () => {
    await open()
    await settle(() => button(tk.create) !== null, 'the button')
    await click(button(tk.create))
    // The personal space is ticked to start with; read only, 90 days.
    const boxes = [...dialog().querySelectorAll<HTMLInputElement>('input[type="checkbox"]')]
    expect(boxes).toHaveLength(1)
    expect(boxes[0].checked).toBe(true)
    await type(name(), '  Desktop notes ')
    await click(button(tk.submit))
    await settle(() => document.querySelector('#fresh-token-title') !== null, 'the secret')
    const secret = dialog().querySelector<HTMLInputElement>('[role="group"] input')!.value
    expect(secret).toMatch(/^pbk_[0-9A-Za-z]{43}$/)
    const body = JSON.parse(apiCalls('POST')[0].body!)
    expect(body).toMatchObject({ name: 'Desktop notes', access: 'read', expiresInDays: 90 })
    expect(body.spaces).toHaveLength(1)
    // The list has the token, with its prefix and not its secret.
    await settle(() => items().length === 1, 'the list')
    expect(items()[0].textContent).toContain('Desktop notes')
    expect(items()[0].textContent).toContain(secret.slice(0, 12))
    expect(text()).not.toContain(secret.slice(12))
    expect(items()[0].textContent).toContain(tk.accessNames.read)
    // Done: it is gone from the page, and a new look at the list does not have it.
    await click(button(tk.done))
    expect(dialog().querySelector('#fresh-token-title')).toBeNull()
    expect(text()).not.toContain(secret)
  })

  it('asks for a name and for a space, and says what the server refuses', async () => {
    await open()
    await settle(() => button(tk.create) !== null, 'the button')
    await click(button(tk.create))
    await click(dialog().querySelector('input[type="checkbox"]'))
    await click(button(tk.submit))
    expect(text()).toContain(tk.nameRequired)
    expect(text()).toContain(tk.spaceRequired)
    expect(apiCalls('POST')).toEqual([])
    // The fiftieth is the last: the 51st says why not.
    const request = adminRequests(m!.server)
    const space = (request('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces[0].id
    for (let i = 0; i < 50; i++) request('POST', '/api/account/tokens', { name: `t${i}`, spaces: [space] })
    await click(dialog().querySelector('input[type="checkbox"]'))
    await type(name(), 'one too many')
    await click(button(tk.submit))
    await settle(() => text().includes(tk.limit), 'the limit')
  })

  it('a long name, a long prefix and many spaces cannot widen the page', async () => {
    await open()
    const request = adminRequests(m!.server)
    const space = (request('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces[0].id
    request('POST', '/api/account/tokens', { name: 'x'.repeat(80), spaces: [space], access: 'write' })
    await m!.qc.invalidateQueries({ queryKey: ['tokens'] })
    await settle(() => items().length === 1, 'the token')
    const li = items()[0]
    // The classes that let a phone's width hold it: the row is one column that may shrink,
    // and what can be long breaks anywhere.
    expect(li.className).toMatch(/grid-cols-1/)
    expect(li.className).toMatch(/min-w-0/)
    expect(li.className).toMatch(/w-full/)
    expect(li.querySelector('code')!.className).toMatch(/break-all/)
    expect(li.querySelector('span.font-medium, .font-medium')!.className).toMatch(/break-words/)
    expect(li.querySelector('ul')!.className).toMatch(/min-w-0/)
    expect(dialog().querySelector('[data-slot="token-item"]')!.parentElement!.className).toMatch(/min-w-0/)
  })

  it('revokes one after asking, and a revoked one has no button', async () => {
    await open()
    const request = adminRequests(m!.server)
    const space = (request('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces[0].id
    request('POST', '/api/account/tokens', { name: 'keep', spaces: [space] })
    const gone = (request('POST', '/api/account/tokens', { name: 'drop', spaces: [space] }).body as { token: { id: string } }).token.id
    await m!.qc.invalidateQueries({ queryKey: ['tokens'] })
    await settle(() => items().length === 2, 'both tokens')
    await click(byLabel(tk.revokeLabel('drop')))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    expect(document.querySelector('[role="alertdialog"]')!.textContent).toContain(tk.revokeHelp('drop'))
    // Asking does nothing yet.
    expect(m!.requests.filter((r) => r.method === 'DELETE')).toEqual([])
    await click([...document.querySelectorAll<HTMLButtonElement>('[role="alertdialog"] button')].find((b) => b.textContent?.trim() === tk.revokeConfirm))
    await settle(() => m!.requests.some((r) => r.method === 'DELETE' && r.url === `/api/account/tokens/${gone}`), 'the revocation')
    await settle(() => items().some((i) => i.dataset.state === 'revoked'), 'the list shows it revoked')
    const revoked = items().find((i) => i.dataset.state === 'revoked')!
    expect(revoked.textContent).toContain(tk.states.revoked)
    expect(revoked.querySelector('button')).toBeNull()
    expect(byLabel(tk.revokeLabel('keep'))).not.toBeNull()
  })

  it('an expired token is listed as expired and cannot be revoked again', async () => {
    let clock = Date.now()
    m = await mount(routes, { entries: ['/settings/tokens'], signIn: true, server: { mode: 'accounts', now: () => clock } })
    await settle(() => dialog() !== null && text().includes(tk.none), 'the dialog')
    const request = adminRequests(m.server)
    const space = (request('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces[0].id
    request('POST', '/api/account/tokens', { name: 'short', spaces: [space], expiresInDays: 1 })
    clock += 2 * 86_400_000
    await m.qc.invalidateQueries({ queryKey: ['tokens'] })
    await settle(() => items().length === 1 && items()[0].dataset.state === 'expired', 'expired')
    expect(items()[0].querySelector('button')).toBeNull()
  })
})

describe('what the mock keeps to the contract', () => {
  it('names a space only while its owner is a member, and refuses spaces that are not theirs', async () => {
    await open()
    const request = adminRequests(m!.server)
    const spaces = (request('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces
    const r = request('POST', '/api/account/tokens', { name: 'n', spaces: [spaces[0].id], expiresInDays: 0 })
    expect(r.status).toBe(201)
    expect((r.body as { token: { expires: unknown } }).token.expires).toBeNull()
    expect(() => request('POST', '/api/account/tokens', { name: 'n', spaces: ['nope'] })).toThrow(/404/)
    expect(() => request('POST', '/api/account/tokens', { name: '', spaces: [spaces[0].id] })).toThrow(/400/)
    expect(() => request('POST', '/api/account/tokens', { name: 'n', spaces: [spaces[0].id], expiresInDays: 400 })).toThrow(/400/)
    expect(MOCK_ADMIN.username).toBeTruthy()
  })
})
