// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { act } from 'react'
import { Route, Routes } from 'react-router'
import { toast } from 'sonner'
import { pollMs } from '@/api/manage'
import type { MockServer } from '@/api/mock/server'
import { SpaceGate } from '@/components/layout/SpaceGate'
import { SidebarProvider } from '@/components/ui/sidebar'
import { ta } from '@/i18n/admin'
import { tg } from '@/i18n/manage'
import {
  adminRequests,
  byLabel,
  click,
  installDomShims,
  locationText,
  makePerson,
  mount,
  settle,
  type,
  type Mounted,
} from '@/testing/harness'
import { AdminGate } from './AdminGate'

// The recycle bin (days left, restoring, purging early), the exports, and the audited view of a space.

beforeAll(() => {
  installDomShims()
  pollMs.value = 20
})
beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
})

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
        <Route index element={<div data-testid="space-pages">the pages of the space</div>} />
      </Route>
    </Routes>
  </SidebarProvider>
)
const text = () => document.body.textContent ?? ''
const dlg = () => document.querySelector<HTMLElement>('[role="dialog"]')!
const buttonIn = (root: ParentNode | null | undefined, label: string) =>
  [...(root?.querySelectorAll('button') ?? [])].find((b) => b.textContent?.trim() === label) as
    HTMLButtonElement | undefined
const items = () => [...document.querySelectorAll<HTMLElement>('[data-slot="recycle-list"] > li')]
const itemOf = (name: string) => items().find((i) => i.textContent?.includes(name))!
const exportsList = () => [...document.querySelectorAll<HTMLElement>('[data-slot="export-list"] > li')]
const requests = (method: string, prefix: string) =>
  m!.requests.filter((r) => r.method === method && r.url.startsWith(prefix))

const asTenantAdmin = (server: MockServer) => {
  const admin = adminRequests(server)
  makePerson(server, 'second', 'admin')
  const me = (admin('GET', '/api/users').body as { users: { id: string; username: string }[] }).users.find(
    (u) => u.username === 'admin',
  )!
  admin('PATCH', `/api/users/${me.id}`, { siteAdmin: false })
}

async function open(path: string, scene?: (server: MockServer) => void) {
  m = await mount(routes, {
    signIn: true,
    server: { mode: 'accounts', maxSpaces: 0 },
    entries: (server) => {
      scene?.(server)
      return [path]
    },
  })
}
const deleteSpace = (server: MockServer, id: string, reason = '') =>
  adminRequests(server)('DELETE', `/api/admin/spaces/${id}`, reason ? { reason } : {})

describe('the recycle bin', () => {
  it('lists what was deleted with the days left, who deleted it and why', async () => {
    await open('/admin/recycle', (server) => {
      const tang = makePerson(server, 'tang')
      const id = (tang.request('POST', '/api/spaces', { name: '删掉的' }).body as { id: string }).id
      deleteSpace(server, id, '搬家了')
    })
    await settle(() => items().length === 1, 'the item')
    const row = itemOf('删掉的')
    expect(row.querySelector('[data-slot="days-left"]')!.textContent).toBe(tg.recycle.left(30))
    expect(row.textContent).toContain(tg.recycle.deletedBy('admin', '').split(',')[0].slice(0, 5))
    expect(row.textContent).toContain(tg.recycle.reason('搬家了'))
    expect(text()).toContain(tg.recycle.title)
  })

  it('restores a space, and the list is empty again', async () => {
    let space = ''
    await open('/admin/recycle', (server) => {
      const tang = makePerson(server, 'tang')
      space = (tang.request('POST', '/api/spaces', { name: '要回来的' }).body as { id: string }).id
      deleteSpace(server, space)
    })
    await settle(() => items().length === 1, 'the item')
    await click(byLabel(tg.recycle.restoreFor('要回来的')))
    await click(buttonIn(dlg(), tg.recycle.restoreConfirm))
    await settle(() => text().includes(tg.recycle.restoreDone('要回来的')), 'restored')
    expect(m!.server.spaceIds()).toContain(space)
    await click(buttonIn(dlg(), tg.bulk.close))
    await settle(() => items().length === 0 && text().includes(tg.recycle.empty), 'the bin is empty')
  })

  it('asks for a new owner when every owner is gone, and lists the people who were left out', async () => {
    await open('/admin/recycle', (server) => {
      const gone = makePerson(server, 'gone')
      makePerson(server, 'newowner')
      const id = (gone.request('POST', '/api/spaces', { name: '没主人了' }).body as { id: string }).id
      deleteSpace(server, id)
      adminRequests(server)('DELETE', `/api/users/${gone.id}`, {})
    })
    await settle(() => items().length === 2, 'the space and the person')
    await click(byLabel(tg.recycle.restoreFor('没主人了')))
    await click(buttonIn(dlg(), tg.recycle.restoreConfirm))
    await settle(() => document.querySelector('[data-slot="restore-owner"]') !== null, 'the owner is asked for')
    expect(dlg().textContent).toContain(tg.errors.owner_required)
    const pick = dlg().querySelector<HTMLSelectElement>(`select[aria-label="${tg.recycle.restoreOwner}"]`)!
    await settle(() => [...pick.options].some((o) => o.textContent?.includes('newowner')), 'people to choose')
    await type(pick, [...pick.options].find((o) => o.textContent?.includes('newowner'))!.value)
    await click(buttonIn(dlg(), tg.recycle.restoreConfirm))
    await settle(() => text().includes(tg.recycle.restoreDone('没主人了')), 'restored')
    expect(dlg().querySelector('[data-slot="restore-skipped"]')?.textContent ?? '').toContain(
      tg.recycle.skippedUser(m!.server.userId('gone') ?? '').slice(0, 6),
    )
    expect(JSON.parse(requests('POST', '/api/admin/recycle/').at(-1)!.body!)).toEqual({
      owner: m!.server.userId('newowner'),
    })
  })

  it('restores a person and lists the handovers it did not undo', async () => {
    await open('/admin/recycle', (server) => {
      const leaver = makePerson(server, 'leaver')
      const heir = makePerson(server, 'heir')
      makePerson(server, 'viewer')
      const id = (leaver.request('POST', '/api/spaces', { name: '交出去的' }).body as { id: string }).id
      leaver.request('POST', `/api/spaces/${id}/members`, { username: 'viewer', role: 'viewer' })
      adminRequests(server)('DELETE', `/api/users/${leaver.id}`, { heir: heir.id })
    })
    await settle(() => items().length === 1, 'the person')
    expect(items()[0].textContent).toContain(tg.recycle.kinds.user)
    await click(byLabel(tg.recycle.restoreFor('leaver')))
    await click(buttonIn(dlg(), tg.recycle.restoreConfirm))
    await settle(() => document.querySelector('[data-slot="restore-transfers"]') !== null, 'the handovers')
    expect(dlg().querySelector('[data-slot="restore-transfers"]')!.textContent).toContain('交出去的')
    expect(dlg().querySelector('[data-slot="restore-transfers"]')!.textContent).toContain('heir')
    expect(m!.server.userId('leaver')).toBeDefined()
  })

  it('purges early for a site administrator: a fresh confirmation, the name typed, then progress until it is gone', async () => {
    await open('/admin/recycle', (server) => {
      const tang = makePerson(server, 'tang')
      deleteSpace(server, (tang.request('POST', '/api/spaces', { name: '清掉我' }).body as { id: string }).id)
    })
    await settle(() => items().length === 1, 'the item')
    await click(byLabel(tg.recycle.purgeFor('清掉我')))
    await settle(() => document.querySelector('#purge-name') !== null, 'the dialog')
    await settle(() => requests('POST', '/api/admin/confirm').length === 1, 'the confirmation is asked for')
    const go = () => buttonIn(dlg(), tg.recycle.purgeConfirm)!
    expect(go().disabled).toBe(true)
    await type(document.querySelector<HTMLInputElement>('#purge-name')!, '清掉我')
    await settle(() => !go().disabled, 'the button is on')
    await click(go())
    await settle(() => text().includes(tg.recycle.purgeStarted), '202 is answered')
    await click(buttonIn(dlg(), tg.bulk.close))
    await settle(() => items().length === 0, 'it is gone')
    const purge = requests('DELETE', '/api/admin/recycle/')[0]
    expect(purge.url).toMatch(/\?confirm=c_/)
    // Each opening of the dialog made its own confirmation.
    expect(requests('POST', '/api/admin/confirm')).toHaveLength(1)
  })

  it('offers a tenant administrator no early purge, and no personal space', async () => {
    await open('/admin/recycle', (server) => {
      const tang = makePerson(server, 'tang')
      deleteSpace(server, (tang.request('POST', '/api/spaces', { name: '团队的' }).body as { id: string }).id)
      adminRequests(server)('DELETE', `/api/users/${tang.id}`, {})
      asTenantAdmin(server)
    })
    await settle(() => items().length === 2, 'the items')
    expect(document.querySelector(`button[aria-label="${tg.recycle.purgeFor('团队的')}"]`)).toBeNull()
    expect(text()).not.toContain(tg.recycle.purge)
    // The person's own item does not say it has their personal space.
    expect(itemOf('tang').textContent).not.toContain(tg.recycle.with(1))
    await click(byLabel(tg.recycle.selectFor('团队的')))
    const labels = [...document.querySelectorAll('[data-slot="bulk-bar"] button')].map((b) => b.textContent)
    expect(labels).toContain(tg.bulk.actions.restore)
    expect(labels).not.toContain(tg.bulk.actions.purge)
  })

  it('restores several at once through a confirmation and a job', async () => {
    await open('/admin/recycle', (server) => {
      const tang = makePerson(server, 'tang')
      for (const n of ['一', '二'])
        deleteSpace(server, (tang.request('POST', '/api/spaces', { name: n }).body as { id: string }).id)
    })
    await settle(() => items().length === 2, 'the items')
    await click(byLabel(tg.recycle.selectFor('一')))
    await click(byLabel(tg.recycle.selectFor('二')))
    await click(buttonIn(document.querySelector('[data-slot="bulk-bar"]'), tg.bulk.actions.restore))
    await settle(() => document.querySelector('#bulk-count') !== null, 'the plan')
    await type(document.querySelector<HTMLInputElement>('#bulk-count')!, '2')
    await click(buttonIn(dlg(), tg.bulk.confirm))
    await settle(() => document.querySelector('[data-slot="job"]')?.getAttribute('data-state') === 'done', 'done')
    await click(buttonIn(dlg(), tg.bulk.close))
    await settle(() => items().length === 0, 'the bin is empty')
  })
})

describe('exports', () => {
  it('starts one from a space, shows it being made, ready to download with its expiry, and deletes it', async () => {
    await open(
      '/admin/spaces',
      (server) => void makePerson(server, 'tang').request('POST', '/api/spaces', { name: '要导出' }),
    )
    await settle(
      () =>
        document.querySelector('tr[data-space]') !== null && document.querySelectorAll('tr[data-space]').length === 3,
      'spaces',
    )
    await click(byLabel(ta.spaces.moreFor('要导出')))
    await settle(() => document.querySelectorAll('[role="menuitem"]').length > 0, 'the actions')
    await click(
      [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
        (e) => e.textContent?.trim() === tg.spaces.export,
      ),
    )
    await settle(() => document.querySelector('[data-slot="export-dialog"]') !== null, 'the dialog')
    // The history is the site's: a site administrator is offered it.
    expect(dlg().textContent).toContain(tg.exports.history2)
    await click(buttonIn(dlg(), tg.exports.start))
    await settle(() => text().includes(tg.exports.started(7)), 'started')
    expect(JSON.parse(requests('POST', '/api/admin/exports')[0].body!)).toEqual({
      spaces: [expect.any(String)],
      history: false,
    })
    await click(dlg().querySelector('a'))
    await settle(() => locationText() === '/admin/recycle' && exportsList().length === 1, 'the list')
    await settle(() => exportsList()[0].getAttribute('data-state') === 'ready', 'ready')
    const row = exportsList()[0]
    expect(row.textContent).toContain('要导出')
    expect(row.querySelector('[data-slot="export-expires"]')!.textContent).toBe(tg.exports.expires(7))
    const link = row.querySelector<HTMLAnchorElement>('a[download]')!
    expect(link.getAttribute('href')).toMatch(/^\/api\/admin\/exports\/[^/]+\/download$/)
    await click(byLabel(tg.exports.deleteFor(row.dataset.export!)))
    await settle(() => exportsList().length === 0 && text().includes(tg.exports.empty), 'deleted')
  })

  it('says what could not be included, and what failed, and drops an export when it expires', async () => {
    let first = ''
    await open('/admin/recycle', (server) => {
      const tang = makePerson(server, 'tang')
      const id = (tang.request('POST', '/api/spaces', { name: '有缺的' }).body as { id: string }).id
      const made = adminRequests(server)('POST', '/api/admin/exports', { spaces: [id] }).body as { id: string }
      first = made.id
      adminRequests(server)('GET', `/api/admin/exports/${first}`)
      server.manage.exportWarning(first, '图.png 没取到')
    })
    await settle(
      () => exportsList().length === 1 && exportsList()[0].querySelector('[data-slot="export-warnings"]') !== null,
      'the warning',
    )
    expect(exportsList()[0].textContent).toContain('图.png 没取到')
    m!.server.manage.expireExport(first)
    await act(async () => void (await m!.qc.invalidateQueries({ queryKey: ['admin', 'exports'] })))
    await settle(() => exportsList().length === 0, 'gone')
  })

  it('refuses when the room for exports is full, in plain words', async () => {
    await open('/admin/spaces', (server) => {
      makePerson(server, 'tang').request('POST', '/api/spaces', { name: '大的' })
      server.manage.exportsLimit(1)
    })
    await settle(() => document.querySelectorAll('tr[data-space]').length === 3, 'spaces')
    await click(byLabel(ta.spaces.moreFor('大的')))
    await settle(() => document.querySelectorAll('[role="menuitem"]').length > 0, 'the actions')
    await click(
      [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
        (e) => e.textContent?.trim() === tg.spaces.export,
      ),
    )
    await settle(() => document.querySelector('[data-slot="export-dialog"]') !== null, 'the dialog')
    await click(buttonIn(dlg(), tg.exports.start))
    await settle(() => text().includes(tg.errors.exports_full), 'the refusal')
  })

  it('does not offer a tenant administrator the history, and never a personal space', async () => {
    let personal = ''
    await open('/admin/spaces', (server) => {
      const tang = makePerson(server, 'tang')
      tang.request('POST', '/api/spaces', { name: '团队的' })
      personal = [...server.spaceIds()].find((id) => id !== 'default')!
      asTenantAdmin(server)
    })
    await settle(() => document.querySelectorAll('tr[data-space]').length === 1, 'the shared space only')
    await click(byLabel(ta.spaces.moreFor('团队的')))
    await settle(() => document.querySelectorAll('[role="menuitem"]').length > 0, 'the actions')
    await click(
      [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
        (e) => e.textContent?.trim() === tg.spaces.export,
      ),
    )
    await settle(() => document.querySelector('[data-slot="export-dialog"]') !== null, 'the dialog')
    expect(dlg().textContent).not.toContain(tg.exports.history2)
    // Asked for directly, a personal space is one that does not exist, and so is the history.
    const admin = adminRequests(m!.server)
    expect(() => admin('POST', '/api/admin/exports', { spaces: [personal] })).toThrow(/404/)
    expect(() =>
      admin('POST', '/api/admin/exports', { spaces: [m!.server.spaceIds().at(-1)!], history: true }),
    ).toThrow(/403/)
  })
})

describe('the audited view of a space', () => {
  const frank = (server: MockServer) => {
    const owner = makePerson(server, 'frank')
    const id = (owner.request('POST', '/api/spaces', { name: '别人的空间' }).body as { id: string }).id
    owner.request('POST', `/api/spaces/${id}/pages`, { meta: { title: '别人写的页面' }, body: 'x\n' })
  }

  it('asks why from the list, records it, and opens the space read only under a banner that says so', async () => {
    await open('/admin/spaces', frank)
    await settle(() => document.querySelectorAll('tr[data-space]').length === 3, 'spaces')
    await click(byLabel(ta.spaces.moreFor('别人的空间')))
    await settle(() => document.querySelectorAll('[role="menuitem"]').length > 0, 'the actions')
    await click(
      [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
        (e) => e.textContent?.trim() === tg.spaces.view,
      ),
    )
    await settle(() => document.querySelector('[data-slot="view-dialog"]') !== null, 'the dialog')
    const start = () => buttonIn(dlg(), tg.view.start)!
    expect(start().disabled).toBe(true)
    await type(dlg().querySelector<HTMLTextAreaElement>('textarea')!, '检查一个被举报的页面')
    expect(start().disabled).toBe(false)
    await click(start())
    await settle(() => document.querySelector('[data-slot="admin-view-banner"]') !== null, 'the banner')
    const space = locationText().match(/^\/s\/([^/]+)/)![1]
    expect(JSON.parse(requests('POST', `/api/admin/spaces/${space}/view`)[0].body!)).toEqual({
      reason: '检查一个被举报的页面',
    })
    expect(document.querySelector('[data-slot="admin-view-banner"]')!.textContent).toContain(tg.view.banner)
    await settle(() => document.querySelector('[data-testid="space-pages"]') !== null, 'the space opens')
    // Ending the view closes the space again.
    await click(buttonIn(document.querySelector('[data-slot="admin-view-banner"]'), tg.view.end))
    await settle(() => document.querySelector('[data-slot="admin-view-gate"]') !== null, 'the gate again')
    expect(requests('DELETE', `/api/admin/spaces/${space}/view`)).toHaveLength(1)
  })

  it('shows the gate instead of the space, and a tenant administrator is never let into a personal one', async () => {
    let personal = ''
    await open('/admin/spaces', (server) => {
      frank(server)
      asTenantAdmin(server)
      personal = [...server.spaceIds()].find((id) => id !== 'default')!
    })
    await settle(() => document.querySelectorAll('tr[data-space]').length === 1, 'the shared space')
    const admin = adminRequests(m!.server)
    // Frank's personal space (the first one made after the administrator's) answers as one that does not exist.
    const frankPersonal = m!.server.spaceIds().filter((id) => id !== 'default')[0] ?? personal
    expect(() => admin('POST', `/api/admin/spaces/${frankPersonal}/view`, { reason: 'x' })).toThrow(/404/)
  })
})
