// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { MOCK_ADMIN, type MockServer } from '@/api/mock/server'
import { adminRequests, byLabel, click, installDomShims, mount, settle, type Mounted } from '@/testing/harness'
import { oa } from '@/i18n/oauth'
import { tk } from '@/i18n/tokens'
import SettingsRoute from './SettingsRoute'

// "Connected apps" in the account settings, against the mock server.

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
const items = () => [...document.querySelectorAll<HTMLElement>('[data-slot="grant-item"]')]

// A person approves a client on the consent page: the mock makes the grant then.
const approve = (server: MockServer, name: string, access: 'read' | 'write' = 'write') => {
  const request = adminRequests(server)
  const req = server.startAuthorization(MOCK_ADMIN.username, { clientName: name })
  const space = (request('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces[0].id
  request('POST', '/api/oauth/consent', { req, approve: true, spaces: [space], access })
}

const open = async (oauth: boolean, seed?: (server: MockServer) => void) => {
  m = await mount(routes, {
    entries: (server) => {
      seed?.(server)
      return ['/settings/tokens']
    },
    signIn: true,
    server: { mode: 'accounts', oauth },
  })
  await settle(() => dialog() !== null && text().includes(tk.create), 'the tokens page')
}

describe('connected apps in the account settings', () => {
  it('has a section under the tokens, with nothing in it yet', async () => {
    await open(true)
    await settle(() => text().includes(oa.yours.none), 'the empty list')
    expect(text()).toContain(oa.yours.title)
    expect(text()).toContain(oa.yours.help)
  })

  it('is not there when the server has no authorization server', async () => {
    await open(false)
    await settle(() => m!.requests.some((r) => r.url === '/api/account/grants'), 'the question')
    await new Promise((r) => setTimeout(r, 50))
    expect(text()).not.toContain(oa.yours.title)
    expect(text()).toContain(tk.yours) // the tokens are
  })

  it('lists an approved app with its access, its spaces and its identifier', async () => {
    await open(true, (s) => approve(s, 'Notes helper'))
    await settle(() => items().length === 1, 'the app')
    const li = items()[0]
    expect(li.textContent).toContain('Notes helper')
    expect(li.textContent).toContain(tk.accessNames.write)
    expect(li.textContent).toContain('pbc_mock')
    expect(li.textContent).toContain(oa.yours.createdOn('').trim())
  })

  it('disconnects one after asking, and the list is read again', async () => {
    await open(true, (s) => {
      approve(s, 'Keep')
      approve(s, 'Drop')
    })
    await settle(() => items().length === 2, 'both apps')
    await click(byLabel(oa.yours.revokeLabel('Drop')))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    expect(document.querySelector('[role="alertdialog"]')!.textContent).toContain(oa.yours.revokeHelp('Drop'))
    // Asking does nothing yet.
    expect(m!.requests.filter((r) => r.method === 'DELETE')).toEqual([])
    await click(
      [...document.querySelectorAll<HTMLButtonElement>('[role="alertdialog"] button')].find(
        (b) => b.textContent?.trim() === oa.yours.revokeConfirm,
      ),
    )
    await settle(() => items().length === 1, 'the list without it')
    expect(items()[0].textContent).toContain('Keep')
    expect(m!.requests.filter((r) => r.method === 'DELETE' && r.url.startsWith('/api/account/grants/'))).toHaveLength(1)
  })

  it('a long name and a long identifier cannot widen the page', async () => {
    await open(true, (s) => approve(s, 'x'.repeat(100)))
    await settle(() => items().length === 1, 'the app')
    const li = items()[0]
    expect(li.className).toMatch(/grid-cols-1/)
    expect(li.className).toMatch(/min-w-0/)
    expect(li.className).toMatch(/w-full/)
    expect(li.querySelector('code')!.className).toMatch(/break-all/)
    expect(li.querySelector('.font-medium')!.className).toMatch(/break-words/)
    expect(li.querySelector('button')!.className).toMatch(/max-sm:w-full/)
    expect(li.parentElement!.className).toMatch(/min-w-0/)
  })
})
