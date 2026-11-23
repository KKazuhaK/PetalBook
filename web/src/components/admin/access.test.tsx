// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { adminRequests, byLabel, click, installDomShims, makePerson, mount, settle, type Mounted } from '@/testing/harness'
import { MOCK_ADMIN, type MockServer } from '@/api/mock/server'
import { oa } from '@/i18n/oauth'
import { ta } from '@/i18n/admin'
import { tk } from '@/i18n/tokens'
import { AdminGate } from './AdminGate'

// The administration's "machine access" page, against the mock server.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const routes = (
  <SidebarProvider>
    <Routes>
      <Route path="/admin/:section?" element={<AdminGate />} />
      <Route path="*" element={<div>elsewhere</div>} />
    </Routes>
  </SidebarProvider>
)
const text = () => document.body.textContent ?? ''
const items = () => [...document.querySelectorAll<HTMLElement>('[data-slot="token-item"]')]
const open = () => mount(routes, { entries: ['/admin/access'], signIn: true, server: { mode: 'accounts' } })

describe('machine access', () => {
  it('has a page in the administration, with the switch on', async () => {
    m = await open()
    await settle(() => document.querySelector('[role="switch"]') !== null, 'the switch')
    expect(document.querySelector('nav [aria-current="page"]')?.textContent).toBe(ta.sections.access)
    expect(document.querySelector('[role="switch"]')!.getAttribute('aria-checked')).toBe('true')
    expect(text()).toContain(tk.admin.empty)
  })

  it('switches API tokens off and on, and the server is told', async () => {
    m = await open()
    await settle(() => document.querySelector('[role="switch"]') !== null, 'the switch')
    await click(byLabel(tk.admin.switchLabel))
    await settle(() => document.querySelector('[role="switch"]')!.getAttribute('aria-checked') === 'false', 'off')
    const puts = m.requests.filter((r) => r.method === 'PUT' && r.url === '/api/admin/access')
    expect(puts.map((r) => JSON.parse(r.body!))).toEqual([{ tokens: false }])
    expect(adminRequests(m.server)('GET', '/api/admin/access').body).toEqual({ tokens: false, mcp: true, oauth: false })
    await click(byLabel(tk.admin.switchLabel))
    await settle(() => document.querySelector('[role="switch"]')!.getAttribute('aria-checked') === 'true', 'on again')
  })

  it("lists everyone's tokens with their owners and revokes one", async () => {
    m = await mount(routes, {
      entries: (server) => {
        const person = makePerson(server, 'carol')
        const space = (person.request('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces[0].id
        person.request('POST', '/api/account/tokens', { name: 'carol script', spaces: [space] })
        const mine = adminRequests(server)
        const own = (mine('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces[0].id
        mine('POST', '/api/account/tokens', { name: 'admin script', spaces: [own], access: 'write' })
        return ['/admin/access']
      },
      signIn: true,
      server: { mode: 'accounts' },
    })
    await settle(() => items().length === 2, 'both tokens')
    const carol = items().find((i) => i.textContent?.includes('carol script'))!
    expect(carol.textContent).toContain('carol')
    await click(byLabel(tk.revokeLabel('carol script')))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await click([...document.querySelectorAll<HTMLButtonElement>('[role="alertdialog"] button')].find((b) => b.textContent?.trim() === tk.revokeConfirm))
    await settle(() => items().find((i) => i.textContent?.includes('carol script'))?.dataset.state === 'revoked', 'revoked')
    expect(m.requests.filter((r) => r.method === 'DELETE' && r.url.startsWith('/api/admin/tokens/'))).toHaveLength(1)
  })

  it('reads the list a page at a time', async () => {
    m = await mount(routes, {
      entries: (server) => {
        const mine = adminRequests(server)
        const own = (mine('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces[0].id
        // 50 is the most a person may have in use; the 51st is another person's.
        for (let i = 0; i < 50; i++) mine('POST', '/api/account/tokens', { name: `t${i}`, spaces: [own] })
        const person = makePerson(server, 'dave')
        const space = (person.request('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces[0].id
        person.request('POST', '/api/account/tokens', { name: 'dave', spaces: [space] })
        return ['/admin/access']
      },
      signIn: true,
      server: { mode: 'accounts' },
    })
    await settle(() => items().length === 50, 'the first page')
    const more = [...document.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.includes(tk.admin.loadMore))!
    await click(more)
    await settle(() => items().length === 51, 'the second page')
    expect([...document.querySelectorAll('button')].some((b) => b.textContent?.includes(tk.admin.loadMore))).toBe(false)
  })
})

const grantItems = () => [...document.querySelectorAll<HTMLElement>('[data-slot="grant-item"]')]
const approveFor = (server: MockServer, who: string, name: string) => {
  const person = makePerson(server, who)
  const space = (person.request('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces[0].id
  const req = server.startAuthorization(who, { clientName: name })
  person.request('POST', '/api/oauth/consent', { req, approve: true, spaces: [space], access: 'write' })
}

describe('MCP and OAuth in the machine access', () => {
  it('has its own switch, and says whether the authorization server is running', async () => {
    m = await mount(routes, { entries: ['/admin/access'], signIn: true, server: { mode: 'accounts', oauth: true } })
    await settle(() => document.querySelector(`[aria-label="${oa.admin.mcpLabel}"]`) !== null, 'the switch')
    expect(document.querySelector(`[aria-label="${oa.admin.mcpLabel}"]`)!.getAttribute('aria-checked')).toBe('true')
    expect(document.querySelector('[data-slot="oauth-status"]')!.textContent).toBe(oa.admin.oauthOn)
    await click(byLabel(oa.admin.mcpLabel))
    await settle(() => document.querySelector('[data-slot="oauth-status"]')!.textContent === oa.admin.oauthOff, 'off')
    expect(m.requests.filter((r) => r.method === 'PUT' && r.url === '/api/admin/access').map((r) => JSON.parse(r.body!))).toEqual([{ mcp: false }])
    // The tokens' switch is its own.
    expect(adminRequests(m.server)('GET', '/api/admin/access').body).toEqual({ tokens: true, mcp: false, oauth: false })
  })

  it('says it is not running when the server has no public address, and lists no grants', async () => {
    m = await mount(routes, { entries: ['/admin/access'], signIn: true, server: { mode: 'accounts' } })
    await settle(() => document.querySelector('[data-slot="oauth-status"]') !== null, 'the status')
    expect(document.querySelector('[data-slot="oauth-status"]')!.textContent).toBe(oa.admin.oauthOff)
    await new Promise((r) => setTimeout(r, 50))
    expect(text()).not.toContain(oa.admin.all)
  })

  it("lists everyone's connected apps with their owners and disconnects one", async () => {
    m = await mount(routes, {
      entries: (server) => {
        approveFor(server, 'carol', 'carol app')
        approveFor(server, 'dave', 'dave app')
        return ['/admin/access']
      },
      signIn: true,
      server: { mode: 'accounts', oauth: true },
    })
    await settle(() => grantItems().length === 2, 'both apps')
    const carol = grantItems().find((i) => i.textContent?.includes('carol app'))!
    expect(carol.textContent).toContain(`${oa.admin.owner}: `)
    expect(carol.textContent).toContain('carol')
    await click(byLabel(oa.yours.revokeLabel('carol app')))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await click([...document.querySelectorAll<HTMLButtonElement>('[role="alertdialog"] button')].find((b) => b.textContent?.trim() === oa.yours.revokeConfirm))
    await settle(() => grantItems().length === 1, 'one left')
    expect(grantItems()[0].textContent).toContain('dave app')
    expect(m.requests.filter((r) => r.method === 'DELETE' && r.url.startsWith('/api/admin/grants/'))).toHaveLength(1)
  })

  it('keeps listing them while MCP is off: they are kept, and may be disconnected', async () => {
    m = await mount(routes, {
      entries: (server) => {
        approveFor(server, 'erin', 'erin app')
        return ['/admin/access']
      },
      signIn: true,
      server: { mode: 'accounts', oauth: true },
    })
    await settle(() => grantItems().length === 1, 'the app')
    await click(byLabel(oa.admin.mcpLabel))
    await settle(() => document.querySelector('[data-slot="oauth-status"]')!.textContent === oa.admin.oauthOff, 'off')
    expect(grantItems()).toHaveLength(1)
    expect(MOCK_ADMIN.username).toBeTruthy()
  })
})
