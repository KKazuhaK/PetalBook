// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { act } from 'react'
import { Route, Routes } from 'react-router'
import { toast } from 'sonner'
import { pollMs } from '@/api/manage'
import type { FixtureData } from '@/api/fixtures/data/model'
import { SpaceGate } from '@/components/layout/SpaceGate'
import { SidebarProvider } from '@/components/ui/sidebar'
import { ta } from '@/i18n/admin'
import { tg } from '@/i18n/manage'
import { byLabel, click, installDomShims, locationText, mount, settle, type, type FxMounted } from '@/testing/harness'
import { addPerson, addRecycled, addSpace, asTenantAdmin } from '@/testing/adminScene'
import { AdminGate } from './AdminGate'

// The recycle bin (days left, restoring, purging early), the exports, and the audited view of a space, against the
// fixture server. What a restore leaves out or hands back, and what the server refuses, are its answers: a test says
// them with `fx.reply` and `fx.fail`.

beforeAll(() => {
  installDomShims()
  pollMs.value = 20
})
beforeEach(() => {
  localStorage.clear()
})

let m: FxMounted | undefined
afterEach(async () => {
  toast.dismiss()
  await m?.unmount()
  m = undefined
})

const routes = (
  <SidebarProvider>
    <Routes>
      <Route path="/admin/*" element={<AdminGate />} />
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

// A page of the bin opens on one kind; `all` shows both.
async function openAll(path: string, scene?: (d: FixtureData) => void, setup?: (fx: FxMounted['fx']) => void) {
  await open(path, scene, setup)
  await settle(() => document.querySelector('[data-slot="native-select"]') !== null, 'the filter')
  await type(document.querySelector<HTMLSelectElement>('[data-slot="native-select"]')!, '')
}

async function open(path: string, scene?: (d: FixtureData) => void, setup?: (fx: FxMounted['fx']) => void) {
  m = await mount(routes, {
    signIn: true,
    fixtures: { mode: 'accounts', data: (d) => void scene?.(d) },
    entries: (fx) => {
      setup?.(fx)
      return [path]
    },
  })
}

describe('the recycle bin', () => {
  it('lists what was deleted with the days left, who deleted it and why', async () => {
    await open('/admin/spaces/deleted', (d) => void addRecycled(d, 'space', '删掉的', { reason: '搬家了' }))
    await settle(() => items().length === 1, 'the item')
    const row = itemOf('删掉的')
    expect(row.querySelector('[data-slot="days-left"]')!.textContent).toBe(tg.recycle.left(30))
    expect(row.textContent).toContain(tg.recycle.deletedBy('admin', '').split(',')[0].slice(0, 5))
    expect(row.textContent).toContain(tg.recycle.reason('搬家了'))
    expect(text()).toContain(tg.recycle.title)
  })

  it('restores a space, and the list is empty again', async () => {
    await open('/admin/spaces/deleted', (d) => void addRecycled(d, 'space', '要回来的'))
    await settle(() => items().length === 1, 'the item')
    await click(byLabel(tg.recycle.restoreFor('要回来的')))
    await click(buttonIn(dlg(), tg.recycle.restoreConfirm))
    await settle(() => text().includes(tg.recycle.restoreDone('要回来的')), 'restored')
    await click(buttonIn(dlg(), tg.bulk.close))
    await settle(() => items().length === 0 && text().includes(tg.recycle.empty), 'the bin is empty')
  })

  it('asks for a new owner when every owner is gone, and lists the people who were left out', async () => {
    let newOwner = ''
    await openAll(
      '/admin/spaces/deleted',
      (d) => {
        newOwner = addPerson(d, 'newowner').id
        addRecycled(d, 'space', '没主人了')
        addRecycled(d, 'user', 'gone')
      },
      (fx) => {
        // The second restore (with an owner named) leaves the gone person out.
        fx.reply(
          'POST',
          '/api/admin/recycle/:id/restore',
          {
            restored: { item: 'x', space: { id: 'gone-没主人了', name: '没主人了' } },
            skipped: [{ user: 'gone-gone', name: 'gone', reason: 'deleted' }],
          },
          { times: 1 },
        )
        // The first refuses: nobody owns the space any more.
        fx.fail('POST', '/api/admin/recycle/:id/restore', 409, { code: 'owner_required' }, { times: 1 })
      },
    )
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
    // The person is named, not shown as an id.
    expect(dlg().querySelector('[data-slot="restore-skipped"]')?.textContent ?? '').toContain(
      tg.recycle.skippedUser('gone'),
    )
    expect(JSON.parse(requests('POST', '/api/admin/recycle/').at(-1)!.body!)).toEqual({ owner: newOwner })
  })

  it('restores a person and lists the handovers it did not undo', async () => {
    await open(
      '/admin/users/deleted',
      (d) => void addRecycled(d, 'user', 'leaver'),
      (fx) =>
        // The server says which spaces the person had handed over, and to whom.
        fx.reply('POST', '/api/admin/recycle/:id/restore', {
          restored: { item: 'x', user: { id: 'gone-leaver', username: 'leaver' } },
          skipped: [],
          transfers: [{ space: 's1', name: '交出去的', to: 'h1', toUsername: 'heir' }],
        }),
    )
    await settle(() => items().length === 1, 'the person')
    expect(items()[0].textContent).toContain(tg.recycle.kinds.user)
    await click(byLabel(tg.recycle.restoreFor('leaver')))
    await click(buttonIn(dlg(), tg.recycle.restoreConfirm))
    await settle(() => document.querySelector('[data-slot="restore-transfers"]') !== null, 'the handovers')
    expect(dlg().querySelector('[data-slot="restore-transfers"]')!.textContent).toContain('交出去的')
    expect(dlg().querySelector('[data-slot="restore-transfers"]')!.textContent).toContain('heir')
  })

  it('purges early for a site administrator: a fresh confirmation, the name typed, then progress until it is gone', async () => {
    await open('/admin/spaces/deleted', (d) => void addRecycled(d, 'space', '清掉我'))
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
    await openAll('/admin/spaces/deleted', (d) => {
      addRecycled(d, 'space', '团队的')
      addRecycled(d, 'user', 'tang')
      asTenantAdmin(d)
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
    await open('/admin/spaces/deleted', (d) => {
      for (const n of ['一', '二']) addRecycled(d, 'space', n)
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
    await open('/admin/spaces', (d) => void addSpace(d, '要导出'))
    await settle(() => document.querySelectorAll('tr[data-space]').length === 2, 'spaces')
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
    await settle(() => locationText() === '/admin/spaces/deleted' && exportsList().length === 1, 'the list')
    await settle(() => exportsList()[0].getAttribute('data-state') === 'ready', 'ready')
    const row = exportsList()[0]
    expect(row.textContent).toContain('要导出')
    // The space still exists and the caller may open it: its name links to it.
    expect(row.querySelector<HTMLAnchorElement>('[data-slot="export-space-link"]')?.getAttribute('href')).toMatch(
      /^\/s\/[^/]+\/$/,
    )
    expect(row.querySelector('[data-slot="export-expires"]')!.textContent).toBe(tg.exports.expires(7))
    const link = row.querySelector<HTMLAnchorElement>('a[download]')!
    expect(link.getAttribute('href')).toMatch(/^\/api\/admin\/exports\/[^/]+\/download$/)
    await click(byLabel(tg.exports.deleteFor(row.dataset.export!)))
    await settle(() => exportsList().length === 0 && text().includes(tg.exports.empty), 'deleted')
  })

  it('says what could not be included, and what failed, and drops an export when it expires', async () => {
    await open('/admin/spaces/deleted', (d) => {
      d.admin.exports.push({
        id: 'EXWARN',
        tenant: '0000000000',
        madeBy: { id: d.auth.users[0].id, username: 'admin' },
        spaces: [{ space: 'x', name: '有缺的', spaceId: 'x', open: false }],
        history: false,
        state: 'ready',
        warnings: ['图.png 没取到'],
        bytes: 0,
        created: new Date().toISOString(),
        expires: new Date(Date.now() + 7 * 86_400_000).toISOString(),
      })
    })
    await settle(
      () => exportsList().length === 1 && exportsList()[0].querySelector('[data-slot="export-warnings"]') !== null,
      'the warning',
    )
    expect(exportsList()[0].textContent).toContain('图.png 没取到')
    // The export runs out: the server no longer lists it.
    m!.fx.data.admin.exports.length = 0
    await act(async () => void (await m!.qc.invalidateQueries({ queryKey: ['admin', 'exports'] })))
    await settle(() => exportsList().length === 0, 'gone')
  })

  it('refuses when the room for exports is full, in plain words', async () => {
    // The room for exports is full: the server refuses a new one.
    await open(
      '/admin/spaces',
      (d) => void addSpace(d, '大的'),
      (fx) => fx.fail('POST', '/api/admin/exports', 409, { code: 'exports_full' }),
    )
    await settle(() => document.querySelectorAll('tr[data-space]').length === 2, 'spaces')
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

  it('does not offer a tenant administrator the history', async () => {
    await open('/admin/spaces', (d) => {
      addSpace(d, '团队的')
      asTenantAdmin(d)
    })
    await settle(() => document.querySelectorAll('tr[data-space]').length >= 1, 'the spaces')
    await click(byLabel(ta.spaces.moreFor('团队的')))
    await settle(() => document.querySelectorAll('[role="menuitem"]').length > 0, 'the actions')
    await click(
      [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
        (e) => e.textContent?.trim() === tg.spaces.export,
      ),
    )
    await settle(() => document.querySelector('[data-slot="export-dialog"]') !== null, 'the dialog')
    expect(dlg().textContent).not.toContain(tg.exports.history2)
  })
})

describe('the audited view of a space', () => {
  // A space of somebody else's, which the administrator manages without being a member.
  const frank = (d: FixtureData) => {
    addSpace(d, '别人的空间', { owner: addPerson(d, 'frank'), outside: true })
  }

  it('asks why from the list, records it, and opens the space read only under a banner that says so', async () => {
    await open('/admin/spaces', frank)
    await settle(() => document.querySelectorAll('tr[data-space]').length === 2, 'spaces')
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
    // The server knows the view is open: the page keeps nothing of it in the browser.
    expect(sessionStorage.length).toBe(0)
    expect(requests('GET', `/api/admin/spaces/${space}`).length).toBeGreaterThan(0)
    await settle(() => document.querySelector('[data-testid="space-pages"]') !== null, 'the space opens')
    // Ending the view closes the space again.
    await click(buttonIn(document.querySelector('[data-slot="admin-view-banner"]'), tg.view.end))
    await settle(() => document.querySelector('[data-slot="admin-view-gate"]') !== null, 'the gate again')
    expect(requests('DELETE', `/api/admin/spaces/${space}/view`)).toHaveLength(1)
  })
})
