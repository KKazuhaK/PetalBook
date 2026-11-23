// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { adminRequests, byLabel, click, installDomShims, makePerson, mount, settle, type Mounted } from '@/testing/harness'
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
