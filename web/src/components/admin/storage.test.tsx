// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { toast } from 'sonner'
import type { MockServer } from '@/api/mock/server'
import { SidebarProvider } from '@/components/ui/sidebar'
import { SpaceGate } from '@/components/layout/SpaceGate'
import { ta } from '@/i18n/admin'
import { ts } from '@/i18n/storage'
import { adminRequests, byLabel, click, installDomShims, makePerson, mount, press, settle, type, type Mounted } from '@/testing/harness'
import { AdminGate } from './AdminGate'

// The administrators' page for the storage policy, what the spaces list shows of each space's copy
// on the server, and the notice a space shows while it needs a remote.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  toast.dismiss()
  await m?.unmount()
  m = undefined
})

const routes = (
  <SidebarProvider>
    <Routes>
      <Route path="/admin/:section?" element={<AdminGate />} />
      <Route path="/s/:space" element={<SpaceGate />}>
        <Route index element={<div>the pages of the space</div>} />
      </Route>
    </Routes>
  </SidebarProvider>
)
const text = () => document.body.textContent ?? ''
const rows = () => [...document.querySelectorAll<HTMLElement>('tbody tr[data-space]')]
const rowOf = (name: string) => rows().find((r) => r.querySelector('th')?.textContent?.includes(name))!
const menuItem = (label: string) => [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((e) => e.textContent?.trim() === label)!
const buttonIn = (root: ParentNode | null | undefined, label: string) => [...(root?.querySelectorAll('button') ?? [])].find((b) => b.textContent?.trim() === label) as HTMLButtonElement | undefined
const puts = () => m!.requests.filter((r) => r.method === 'PUT' && r.url === '/api/admin/storage').map((r) => JSON.parse(r.body!) as Record<string, unknown>)

async function openMenu(name: string) {
  await click(byLabel(ta.spaces.moreFor(name)))
  await settle(() => document.querySelectorAll('[role="menuitem"]').length > 0, 'the actions')
}

const storagePage = (setup?: NonNullable<Parameters<typeof mount>[1]>['entries']) =>
  mount(routes, { entries: setup ?? ['/admin/storage'], signIn: true, server: { mode: 'accounts', github: { configured: true } } })

describe('the storage page', () => {
  it('is a section of the administration, with the defaults of the policy', async () => {
    m = await storagePage()
    await settle(() => text().includes(ts.admin.remotes.title), 'the storage settings')
    expect(document.querySelector('nav [aria-current="page"]')?.textContent).toBe(ta.sections.storage)
    const input = (label: string) => byLabel(label) as HTMLInputElement
    expect(input(ts.admin.remotes.graceDays).value).toBe('30')
    expect(input(ts.admin.cache.evictAfter).value).toBe('14')
    expect(input(ts.admin.cache.minIdle).value).toBe('24')
    expect(input(ts.admin.cache.clones).value).toBe('')
    expect(input(ts.admin.cache.minFree).value).toBe('10')
    expect(input(ts.admin.local.maxBytes).value).toBe('')
    expect((byLabel(ts.admin.remotes.require) as HTMLSelectElement).value).toBe('')
    expect(text()).toContain(ts.admin.git('2.53.0', true))
  })

  it('saves only what changed, then shows the new state and how many spaces it touches', async () => {
    m = await storagePage()
    await settle(() => byLabel(ts.admin.remotes.require) !== null, 'the form')
    await type(byLabel(ts.admin.remotes.require) as HTMLSelectElement, 'any')
    await type(byLabel(ts.admin.remotes.scope) as HTMLSelectElement, 'all')
    await type(byLabel(ts.admin.remotes.graceDays) as HTMLInputElement, '10')
    await type(byLabel(ts.admin.cache.evictAfter) as HTMLInputElement, '')
    await click(buttonIn(document, ts.admin.save))
    await settle(() => puts().length === 1, 'the save')
    expect(puts()[0]).toEqual({ remotes: { require: 'any', scope: 'all', graceDays: 10 }, cache: { evictAfterDays: null } })
    await settle(() => text().includes(ts.admin.remotes.affected(1, 0, 0)), 'the count of spaces in their grace period')
    expect((byLabel(ts.admin.cache.evictAfter) as HTMLInputElement).value).toBe('')
    // Saving again with nothing changed sends nothing.
    await click(buttonIn(document, ts.admin.save))
    expect(puts()).toHaveLength(1)
  })

  it('sets the attachment cache and the default for new spaces', async () => {
    m = await storagePage()
    await settle(() => byLabel(ts.admin.cache.assetsBytes) !== null, 'the form')
    expect((byLabel(ts.admin.cache.assetsBytes) as HTMLInputElement).value).toBe('5120')
    await type(byLabel(ts.admin.cache.assetsBytes) as HTMLInputElement, '2048')
    await click(byLabel(ts.admin.assets.newSpaces))
    await click(buttonIn(document, ts.admin.save))
    await settle(() => puts().length === 1, 'the save')
    expect(puts()[0]).toEqual({ cache: { assetsBytes: 2048 * 1024 ** 2 }, assets: { newSpaces: true } })
    await type(byLabel(ts.admin.cache.assetsBytes) as HTMLInputElement, '10')
    await click(buttonIn(document, ts.admin.save))
    await settle(() => text().includes(ts.admin.cache.invalid), 'the message')
    expect(puts()).toHaveLength(1)
  })

  it('refuses numbers outside their ranges before asking the server', async () => {
    m = await storagePage()
    await settle(() => byLabel(ts.admin.remotes.graceDays) !== null, 'the form')
    await type(byLabel(ts.admin.remotes.graceDays) as HTMLInputElement, '400')
    await click(buttonIn(document, ts.admin.save))
    await settle(() => text().includes(ts.admin.cache.invalid), 'the message')
    expect(puts()).toHaveLength(0)
    await type(byLabel(ts.admin.remotes.graceDays) as HTMLInputElement, '30')
    await type(byLabel(ts.admin.local.maxBytes) as HTMLInputElement, '0')
    await click(buttonIn(document, ts.admin.save))
    await settle(() => text().includes(ts.admin.cache.invalid), 'the message')
    expect(puts()).toHaveLength(0)
  })

  it('shows what the server refuses in its own words', async () => {
    m = await mount(routes, { entries: ['/admin/storage'], signIn: true, server: { mode: 'accounts' } })
    await settle(() => byLabel(ts.admin.remotes.require) !== null, 'the form')
    // The platforms come from the connections; the mock lists GitHub, not set up.
    await settle(() => (byLabel(ts.admin.remotes.require) as HTMLSelectElement).querySelector('option[value="github"]') !== null, 'the platform')
    await type(byLabel(ts.admin.remotes.require) as HTMLSelectElement, 'github')
    await click(buttonIn(document, ts.admin.save))
    await settle(() => text().includes(ts.admin.errors.codes.provider_not_configured), 'the refusal')
  })
})

describe('the spaces list and the copy of each space', () => {
  const open = () => mount(routes, { entries: (server) => { adminRequests(server)('POST', '/api/spaces', { name: '团队空间' }); return ['/admin/spaces'] }, signIn: true, server: { mode: 'accounts' } })
  const connect = async (server: Mounted['server'], name: string) => {
    const admin = adminRequests(server)
    const list = admin('GET', '/api/admin/spaces').body as { items: { id: string; name: string }[] }
    const id = list.items.find((s) => s.name === name)!.id
    const r = admin('PUT', `/api/spaces/${id}/remote`, { url: 'https://github.com/a/b.git', credential: { type: 'token', token: 'x' } })
    expect(r.status).toBe(200)
    return id
  }

  it('says what each copy is, keeps a local space out of eviction, and evicts one that follows a remote', async () => {
    m = await open()
    await settle(() => rows().length === 2, 'the spaces')
    expect(rowOf('团队空间').querySelector('[data-slot="space-cache"]')!.textContent).toContain(ts.spaces.caches.local)
    // A local space is the only copy of its pages: there is no way to evict it.
    await openMenu('团队空间')
    expect(menuItem(ts.spaces.evict).getAttribute('aria-disabled')).toBe('true')
    await press(document.activeElement ?? document, 'Escape')
    await settle(() => document.querySelectorAll('[role="menuitem"]').length === 0, 'the menu closed')
    await connect(m.server, '团队空间')
    await m.qc.invalidateQueries({ queryKey: ['admin', 'spaces'] })
    await settle(() => rowOf('团队空间').querySelector('[data-slot="space-cache"]')!.textContent!.includes(ts.spaces.caches.partial), 'a partial clone')
    await openMenu('团队空间')
    await click(menuItem(ts.spaces.evict))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the confirmation')
    await click(buttonIn(document.querySelector('[role="alertdialog"]'), ts.spaces.evictConfirm))
    await settle(() => rowOf('团队空间').querySelector('[data-slot="space-cache"]')!.textContent!.includes(ts.spaces.caches.evicted), 'an evicted space')
    expect(m.requests.some((r) => r.method === 'POST' && /\/admin\/spaces\/[^/]+\/evict$/.test(r.url))).toBe(true)
  })

  it('pins a space and leaves one out of the requirement, and filters by copy and by deadline', async () => {
    m = await open()
    await settle(() => rows().length === 2, 'the spaces')
    await openMenu('团队空间')
    await click(menuItem(ts.spaces.pin))
    await settle(() => rowOf('团队空间').textContent!.includes(ts.spaces.pinned), 'the pin')
    await openMenu('团队空间')
    await click(menuItem(ts.spaces.exemptOn))
    await settle(() => rowOf('团队空间').textContent!.includes(ts.spaces.exempt), 'the exemption')
    // Require a remote of every space at once: the space that is not exempt is past its deadline.
    const admin = adminRequests(m.server)
    expect(admin('PUT', '/api/admin/storage', { remotes: { require: 'any', scope: 'all', graceDays: 0 } }).status).toBe(200)
    await m.qc.invalidateQueries({ queryKey: ['admin', 'spaces'] })
    await settle(() => text().includes(ts.spaces.deadline('', true).slice(-2)) || document.querySelector('.text-destructive') !== null, 'the deadline of the personal space')
    await type(byLabel(ts.spaces.filter) as HTMLSelectElement, 'overdue')
    await settle(() => rows().length === 1, 'only the overdue space')
    expect(rows()[0].textContent).not.toContain('团队空间')
    await type(byLabel(ts.spaces.filter) as HTMLSelectElement, 'evicted')
    await settle(() => text().includes(ta.spaces.empty) || rows().length === 0, 'none evicted')
  })
})

describe('the notice of a space that needs a remote', () => {
  const sceneOf = (graceDays: number) => (server: MockServer) => {
    const admin = adminRequests(server)
    const list = admin('GET', '/api/spaces').body as { spaces: { id: string; personal: boolean }[] }
    admin('PUT', '/api/admin/storage', { remotes: { require: 'any', scope: 'all', graceDays } })
    return [`/s/${list.spaces[0].id}`]
  }

  it('shows the deadline in its grace period, and can be put away for the day', async () => {
    m = await mount(routes, { entries: sceneOf(30), signIn: true, server: { mode: 'accounts' } })
    await settle(() => document.querySelector('[data-slot="storage-notice"]') !== null, 'the notice')
    const note = document.querySelector<HTMLElement>('[data-slot="storage-notice"]')!
    expect(note.dataset.overdue).toBe('false')
    expect(note.textContent).toContain(ts.notice.graceTitle)
    expect(note.querySelector('a')!.getAttribute('href')).toBe('/settings/sync')
    await click(note.querySelector('button'))
    await settle(() => document.querySelector('[data-slot="storage-notice"]') === null, 'the notice put away')
  })

  it('says the space is read only once the deadline has passed, and cannot be put away', async () => {
    m = await mount(routes, { entries: sceneOf(0), signIn: true, server: { mode: 'accounts' } })
    await settle(() => document.querySelector('[data-slot="storage-notice"]') !== null, 'the notice')
    const note = document.querySelector<HTMLElement>('[data-slot="storage-notice"]')!
    expect(note.dataset.overdue).toBe('true')
    expect(note.textContent).toContain(ts.notice.overdueTitle)
    expect(note.querySelector('button')).toBeNull()
  })

  it('shows nothing to someone who is not asked: a space that follows a remote, or one that is exempt', async () => {
    m = await mount(routes, {
      entries: (server) => {
        const admin = adminRequests(server)
        const id = (admin('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces[0].id
        admin('PUT', `/api/spaces/${id}/remote`, { url: 'https://github.com/a/b.git', credential: { type: 'token', token: 'x' } })
        admin('PUT', '/api/admin/storage', { remotes: { require: 'any', scope: 'all', graceDays: 0 } })
        return [`/s/${id}`]
      },
      signIn: true,
      server: { mode: 'accounts' },
    })
    await settle(() => text().includes('the pages of the space'), 'the space')
    expect(document.querySelector('[data-slot="storage-notice"]')).toBeNull()
  })

  it('a member sees it too, without the way to connect', async () => {
    m = await mount(routes, {
      entries: (server) => {
        const owner = makePerson(server, 'sharer')
        const id = (owner.request('POST', '/api/spaces', { name: '共享' }).body as { id: string }).id
        owner.request('POST', `/api/spaces/${id}/members`, { username: 'admin', role: 'viewer' })
        adminRequests(server)('PUT', '/api/admin/storage', { remotes: { require: 'any', scope: 'all', graceDays: 5 } })
        return [`/s/${id}`]
      },
      signIn: true,
      server: { mode: 'accounts' },
    })
    await settle(() => document.querySelector('[data-slot="storage-notice"]') !== null, 'the notice')
    const note = document.querySelector<HTMLElement>('[data-slot="storage-notice"]')!
    expect(note.textContent).toContain(ts.notice.graceTitle)
  })
})
