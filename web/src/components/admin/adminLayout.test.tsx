// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tc } from '@/i18n/console'
import {
  adminRequests,
  click,
  installDomShims,
  locationText,
  makePerson,
  mount,
  settle,
  type Mounted,
} from '@/testing/harness'
import type { MockServer } from '@/api/mock/server'
import { AdminGate } from './AdminGate'

// The frame of the administration: a sidebar of sections with the pages of each along its top, a drawer on a phone, a
// rail that folds on a tablet.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
  vi.restoreAllMocks()
})

const routes = (
  <SidebarProvider>
    <Routes>
      <Route path="/admin/*" element={<AdminGate />} />
      <Route path="/s/:space/*" element={<div>a space</div>} />
      <Route path="*" element={<div>elsewhere</div>} />
    </Routes>
  </SidebarProvider>
)
const open = (path: string, scene?: (s: MockServer) => void) =>
  mount(routes, { signIn: true, server: { mode: 'accounts' }, entries: (s) => (scene?.(s), [path]) })
const sidebar = () => document.querySelector<HTMLElement>('aside[data-collapsed], aside')!
const links = (root: ParentNode = document) =>
  [...root.querySelectorAll(`nav[aria-label="${tc.nav}"] a`)].map((a) => a.textContent?.trim())
const tabs = () => [...document.querySelectorAll(`nav[aria-label="${tc.tabsLabel}"] a`)].map((a) => a.textContent)

describe('the administration frame', () => {
  it('is a sidebar of sections with its heading and a way back to the space, the open one marked', async () => {
    m = await open('/admin/groups')
    await settle(() => document.querySelector('[data-slot="admin-layout"]') !== null, 'the frame')
    expect(document.querySelector('h1')!.textContent).toBe(tc.title)
    expect(sidebar().className).toContain('w-[232px]')
    const current = document.querySelector('nav [aria-current="page"]')!
    expect(current.textContent).toBe(tc.sections.groups)
    // The marked item is soft pink: the brand colour, with its icon.
    expect(current.className).toContain('text-brand')
    expect(current.querySelector('svg')!.getAttribute('class')).toContain('text-brand')
    // Every item has an icon of its own.
    expect(document.querySelectorAll(`nav[aria-label="${tc.nav}"] a svg`)).toHaveLength(links().length)
    const back = [...document.querySelectorAll('aside a')].find((a) =>
      a.getAttribute('aria-label')?.startsWith('返回'),
    )!
    expect(back.getAttribute('href')).toMatch(/^\/s\//)
    expect(back.textContent).toMatch(/^返回 /)
    await click(back)
    await settle(() => locationText().startsWith('/s/'), 'back in the space')
  })

  it("puts a section's pages along its top, and none when it has one", async () => {
    m = await open('/admin/users')
    await settle(() => locationText() === '/admin/users', 'the section')
    await settle(() => tabs().length === 3, 'the tabs')
    expect(tabs()).toEqual([tc.tabs.users.all, tc.tabs.users.invites, tc.tabs.users.deleted])
    expect(document.querySelector('nav a[aria-current="page"][href="/admin/users/all"]')).not.toBeNull()
    await click([...document.querySelectorAll('nav a')].find((a) => a.textContent === tc.tabs.users.deleted))
    await settle(() => locationText() === '/admin/users/deleted', 'the page')
    expect(document.title).toContain(tc.tabs.users.deleted)
    await m.unmount()
    m = await open('/admin/groups')
    await settle(() => document.querySelector('[data-slot="admin-layout"]') !== null, 'groups')
    expect(tabs()).toEqual([])
  })

  it('takes an unknown address to the users', async () => {
    for (const [from, to] of [
      ['/admin/nothing', '/admin/users'],
      ['/admin/users/nothing', '/admin/users'],
      ['/admin', '/admin/users'],
    ] as const) {
      m = await open(from)
      await settle(() => locationText().startsWith(to), `${from} -> ${to}`)
      await m.unmount()
      m = undefined
    }
  })

  it('shows a tenant administrator the sections and pages of their tenant only', async () => {
    m = await open('/admin/storage', (server) => {
      const admin = adminRequests(server)
      makePerson(server, 'second', 'admin')
      const me = (admin('GET', '/api/users').body as { users: { id: string; username: string }[] }).users.find(
        (u) => u.username === 'admin',
      )!
      admin('PATCH', `/api/users/${me.id}`, { siteAdmin: false })
    })
    await settle(() => tabs().length > 0, 'the tabs')
    expect(tabs()).toEqual([tc.tabs.storage.manage, tc.tabs.storage.usage])
    expect(links()).not.toContain(tc.sections.security)
    // The pages of the server's own are not theirs: the address goes to the section.
    await m.unmount()
    m = await open('/admin/storage/rules', (server) => {
      const admin = adminRequests(server)
      makePerson(server, 'second', 'admin')
      const me = (admin('GET', '/api/users').body as { users: { id: string; username: string }[] }).users.find(
        (u) => u.username === 'admin',
      )!
      admin('PATCH', `/api/users/${me.id}`, { siteAdmin: false })
    })
    await settle(() => locationText() === '/admin/storage', 'sent back to the section')
  })

  it('folds on a tablet, and the choice is kept in this browser', async () => {
    m = await open('/admin/groups')
    await settle(() => document.querySelector('[data-slot="admin-layout"]') !== null, 'the frame')
    const fold = () => document.querySelector<HTMLButtonElement>('aside button[aria-expanded]')!
    expect(fold().getAttribute('aria-expanded')).toBe('true')
    await click(fold())
    expect(sidebar().getAttribute('data-collapsed')).toBe('true')
    expect(sidebar().className).toContain('w-16')
    expect(localStorage.getItem('pb.admin.collapsed')).toBe('1')
    // A rail still names each item to a screen reader and a pointer.
    expect(document.querySelector(`nav a[aria-label="${tc.sections.groups}"]`)!.getAttribute('title')).toBe(
      tc.sections.groups,
    )
    await m.unmount()
    m = await open('/admin/groups')
    await settle(() => document.querySelector('[data-slot="admin-layout"]') !== null, 'again')
    expect(sidebar().getAttribute('data-collapsed')).toBe('true')
  })

  it('starts folded on a tablet that has no saved choice, and open on a desktop', async () => {
    vi.stubGlobal('innerWidth', 768)
    m = await open('/admin/groups')
    await settle(() => document.querySelector('[data-slot="admin-layout"]') !== null, 'the frame')
    expect(sidebar().getAttribute('data-collapsed')).toBe('true')
    await m.unmount()
    vi.stubGlobal('innerWidth', 1280)
    m = await open('/admin/groups')
    await settle(() => document.querySelector('[data-slot="admin-layout"]') !== null, 'the frame')
    expect(sidebar().getAttribute('data-collapsed')).toBeNull()
    vi.unstubAllGlobals()
  })

  it('is a drawer behind a menu button on a phone, which closes when a section is chosen', async () => {
    m = await open('/admin/groups')
    await settle(() => document.querySelector('[data-slot="admin-layout"]') !== null, 'the frame')
    const menu = document.querySelector<HTMLButtonElement>(`button[aria-label="${tc.menu}"]`)!
    // The button is the phone's: a finger-sized target, hidden from a desktop.
    expect(menu.className).toContain('size-10')
    expect(menu.closest('header')!.className).toContain('md:hidden')
    expect(document.querySelector('[data-slot="sheet-content"]')).toBeNull()
    await click(menu)
    await settle(() => document.querySelector('[data-slot="sheet-content"]') !== null, 'the drawer')
    const drawer = document.querySelector<HTMLElement>('[data-slot="sheet-content"]')!
    expect(links(drawer)).toContain(tc.sections.users)
    await click([...drawer.querySelectorAll('a')].find((a) => a.textContent === tc.sections.spaces))
    await settle(() => locationText().startsWith('/admin/spaces'), 'the section')
    await settle(() => document.querySelector('[data-slot="sheet-content"]') === null, 'the drawer is closed')
  })

  it('does not carry the app sidebar, and nothing in its frame is wider than the screen', async () => {
    m = await open('/admin/users')
    await settle(() => document.querySelector('[data-slot="admin-layout"]') !== null, 'the frame')
    expect(document.querySelector('[data-slot="sidebar"]')).toBeNull()
    const main = document.querySelector('main')!
    expect(main.className).toContain('min-w-0')
    // The tables scroll in a box of their own.
    await settle(() => document.querySelector('[data-slot="data-table"] .overflow-x-auto') !== null, 'the table')
  })
})
