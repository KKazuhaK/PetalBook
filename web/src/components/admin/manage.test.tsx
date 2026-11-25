// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { toast } from 'sonner'
import { pollMs } from '@/api/manage'
import type { MockServer } from '@/api/mock/server'
import { SidebarProvider } from '@/components/ui/sidebar'
import { ta } from '@/i18n/admin'
import { t } from '@/i18n'
import { tc } from '@/i18n/console'
import { tg } from '@/i18n/manage'
import {
  adminRequests,
  byLabel,
  click,
  installDomShims,
  makePerson,
  mount,
  settle,
  type,
  type Mounted,
} from '@/testing/harness'
import { AdminGate } from './AdminGate'
import { fitScreen, tapTarget } from './manage/shared'

// Storage management in the administration: the summary, the lists of people and spaces, what counts toward quotas,
// deleting a space or a person with a preview, bulk actions with a second confirmation and a job, and the differences
// between a site administrator and a tenant administrator.

beforeAll(() => {
  installDomShims()
  pollMs.value = 20
})
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
      <Route path="/admin/*" element={<AdminGate />} />
      <Route path="*" element={<div>elsewhere</div>} />
    </Routes>
  </SidebarProvider>
)
const text = () => document.body.textContent ?? ''
const dlg = () => document.querySelector<HTMLElement>('[role="dialog"]')!
const buttonIn = (root: ParentNode | null | undefined, label: string) =>
  [...(root?.querySelectorAll('button') ?? [])].find((b) => b.textContent?.trim() === label) as
    HTMLButtonElement | undefined
const rows = () => [...document.querySelectorAll<HTMLElement>('tbody tr[data-space]')]
const rowOf = (name: string) => rows().find((r) => r.querySelector('th')?.textContent?.includes(name))!
const personRows = () => [...document.querySelectorAll<HTMLElement>('tbody tr[data-person]')]
const select = (label: string) => document.querySelector<HTMLSelectElement>(`select[aria-label="${label}"]`)!
const menuItem = (label: string) =>
  [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((e) => e.textContent?.trim() === label)!
const queries = (prefix: string) =>
  m!.requests.filter((r) => r.url.startsWith(prefix)).map((r) => new URL(r.url, 'http://x').searchParams)
const sent = (method: string, prefix: string) =>
  m!.requests.filter((r) => r.method === method && r.url.startsWith(prefix))

// The signed-in administrator becomes one of a tenant, as another site administrator exists.
const asTenantAdmin = (server: MockServer) => {
  const admin = adminRequests(server)
  makePerson(server, 'second', 'admin')
  const me = (admin('GET', '/api/users').body as { users: { id: string; username: string }[] }).users.find(
    (u) => u.username === 'admin',
  )!
  admin('PATCH', `/api/users/${me.id}`, { siteAdmin: false })
}

async function open(section: string, scene?: (server: MockServer) => void) {
  m = await mount(routes, {
    signIn: true,
    server: { mode: 'accounts', maxSpaces: 0 },
    entries: (server) => {
      scene?.(server)
      return [`/admin/${section}`]
    },
  })
}
async function openMenu(name: string) {
  await click(byLabel(ta.spaces.moreFor(name)))
  await settle(() => document.querySelectorAll('[role="menuitem"]').length > 0, 'the actions')
}
// Ticks spaces by name and starts a bulk action.
async function bulk(names: string[], action: string) {
  for (const n of names) await click(byLabel(tg.spaces.selectFor(n)))
  await settle(() => document.querySelector('[data-slot="bulk-bar"]') !== null, 'the bulk bar')
  await click(buttonIn(document.querySelector('[data-slot="bulk-bar"]'), action))
}
const typeCount = async (n: number) => {
  await settle(() => document.querySelector('#bulk-count') !== null, 'the count box')
  await type(document.querySelector<HTMLInputElement>('#bulk-count')!, String(n))
}
const confirmButton = () => buttonIn(dlg(), tg.bulk.confirm)!

describe('the summary and the list of people', () => {
  it('shows what the server holds at the top, with the free disk for a site administrator', async () => {
    await open('manage', (server) => {
      const tang = makePerson(server, 'tang')
      tang.request('POST', '/api/spaces', { name: '团队' })
    })
    await settle(() => document.querySelector('[data-slot="storage-summary"]') !== null, 'the summary')
    const card = (k: string) => document.querySelector(`[data-card="${k}"]`)?.textContent ?? ''
    expect(card('spaces')).toContain(tg.summary.spacesValue(3))
    expect(card('people')).toContain(tg.summary.peopleValue(2, 2).slice(0, 4))
    expect(card('disk')).toContain('50 GB')
    expect(text()).toContain(tg.where.explain)
    // The summary and the settings are one request, and nothing walks anything.
    expect(queries('/api/admin/storage').length).toBeGreaterThan(0)
  })

  it('does not tell a tenant administrator the free disk', async () => {
    await open('manage', asTenantAdmin)
    await settle(() => document.querySelector('[data-slot="storage-summary"]') !== null, 'the summary')
    expect(document.querySelector('[data-card="disk"]')).toBeNull()
    expect(document.querySelector('[data-card="held"]')).not.toBeNull()
  })

  it('lists people with what counts and what is held, sorted and filtered by the server', async () => {
    await open('manage', (server) => {
      const tang = makePerson(server, 'tang')
      tang.request('POST', '/api/spaces', { name: '她的空间' })
      makePerson(server, 'wu')
    })
    await settle(() => personRows().length === 3, 'three people')
    // A click on a header sorts by it on the server; a second click turns the order round.
    const header = () =>
      document.querySelector<HTMLElement>(`button[aria-label="${tc.table.sortBy(tc.users.cols.created)}"]`)!
    await click(header())
    await settle(
      () => queries('/api/admin/storage/people').some((q) => q.get('sort') === 'created' && q.get('order') === 'asc'),
      'sorted by the server',
    )
    await click(header())
    await settle(
      () => queries('/api/admin/storage/people').some((q) => q.get('sort') === 'created' && q.get('order') === 'desc'),
      'the order',
    )
    await type(select(tg.people.filter), 'dormant')
    await settle(() => queries('/api/admin/storage/people').some((q) => q.get('dormant') === '1'), 'the filter')
  })

  it('opens a person to see their spaces, each with where it is stored and whether it counts', async () => {
    let space = ''
    await open('manage', (server) => {
      const tang = makePerson(server, 'tang')
      space = (tang.request('POST', '/api/spaces', { name: '在自己远端' }).body as { id: string }).id
      tang.request('POST', '/api/spaces', { name: '只在服务器' })
      server.manage.setLocation(space, 'own')
    })
    await settle(() => personRows().length === 2, 'people')
    await click(byLabel(tg.people.openFor('名 tang')))
    await settle(() => document.querySelectorAll('[data-slot="person-spaces"] li').length === 3, 'their spaces')
    const page = document.querySelector<HTMLElement>('[data-slot="person-usage"]')!
    const own = page.querySelector(`[data-space="${space}"] [data-slot="space-location"]`)!
    expect(own.getAttribute('data-counted')).toBe('false')
    expect(own.textContent).toContain(tg.where.names.own)
    expect(own.textContent).toContain(tg.where.notCounted)
    const here = [...page.querySelectorAll('[data-slot="space-location"]')].filter(
      (l) => l.getAttribute('data-counted') === 'true',
    )
    expect(here).toHaveLength(2)
    expect(here[0].textContent).toContain(tg.where.names.server)
  })

  it("shows a tenant administrator no personal space in a person's spaces", async () => {
    await open('manage', (server) => {
      makePerson(server, 'tang').request('POST', '/api/spaces', { name: '团队' })
      asTenantAdmin(server)
    })
    await settle(() => personRows().length >= 2, 'people')
    await click(byLabel(tg.people.openFor('名 tang')))
    await settle(
      () => document.querySelectorAll('[data-slot="person-spaces"] li').length === 1,
      'only the shared space',
    )
    const page = document.querySelector<HTMLElement>('[data-slot="person-usage"]')!
    expect(page.textContent).toContain('团队')
    expect(page.textContent).toContain(tg.people.tenantNote)
  })
})

describe('the list of spaces', () => {
  it('says where each space is stored and whether it counts, and sorts and filters on the server', async () => {
    await open('spaces', (server) => {
      const tang = makePerson(server, 'tang')
      const id = (tang.request('POST', '/api/spaces', { name: '外面的' }).body as { id: string }).id
      server.manage.setLocation(id, 'tenant')
    })
    await settle(() => rows().length === 3, 'spaces')
    const loc = (name: string) => rowOf(name).querySelector('[data-slot="space-location"]')!
    expect(loc('外面的').getAttribute('data-location')).toBe('tenant')
    expect(loc('外面的').getAttribute('data-counted')).toBe('false')
    expect(loc('外面的').textContent).toContain(tg.where.notCounted)
    expect(rows().filter((r) => r.querySelector('[data-counted="true"]')).length).toBe(2)
    await click(document.querySelector(`button[aria-label="${tc.table.sortBy(ta.spaces.name)}"]`))
    await settle(
      () => queries('/api/admin/spaces').some((q) => q.get('sort') === 'name' && q.get('order') === 'asc'),
      'sorted by name',
    )
    await type(select(tg.spaces.remote), 'none')
    await settle(() => queries('/api/admin/spaces').some((q) => q.get('remote') === 'none'), 'filtered by remote')
    await type(select(tg.spaces.idle), '30')
    await settle(() => queries('/api/admin/spaces').some((q) => q.get('idleDays') === '30'), 'filtered by idle days')
  })

  it("offers a tenant administrator no personal space and nothing that is the site's", async () => {
    await open('spaces', (server) => {
      makePerson(server, 'tang').request('POST', '/api/spaces', { name: '团队' })
      asTenantAdmin(server)
    })
    await settle(() => rows().length === 1, 'only the shared space')
    expect(rows()[0].textContent).toContain('团队')
    expect(text()).toContain(tg.spaces.personalNote)
    await openMenu('团队')
    expect(menuItem('常驻服务器')).toBeUndefined()
    await click(byLabel(tg.spaces.selectFor('团队')))
    const labels = [...document.querySelectorAll('[data-slot="bulk-bar"] button')].map((b) => b.textContent)
    expect(labels).toContain(tg.bulk.actions.export)
    expect(labels).not.toContain(tg.bulk.actions.pin)
    expect(labels).not.toContain(tg.bulk.actions.evict)
  })
})

describe('deleting a space', () => {
  it('previews what happens, says how long it stays in the bin, and the result points to the bin', async () => {
    await open('spaces', (server) => {
      const tang = makePerson(server, 'tang')
      const id = (tang.request('POST', '/api/spaces', { name: '要删的' }).body as { id: string }).id
      tang.request('POST', `/api/spaces/${id}/pages`, { meta: { title: '一页' }, body: 'x\n' })
    })
    await settle(() => rows().length === 3, 'spaces')
    await openMenu('要删的')
    await click(menuItem(tg.spaces.delete))
    await settle(() => document.querySelector('[data-slot="deletion-preview"]') !== null, 'the preview')
    expect(dlg().textContent).toContain(tg.del.daysLine(30))
    expect(dlg().textContent).toContain(tg.del.space.people(1, 0, 0))
    await type(document.querySelector<HTMLInputElement>('#delete-confirm')!, '要删的')
    await click(buttonIn(dlg(), tg.del.space.confirm))
    await settle(() => text().includes(tg.del.space.deleted('要删的')), 'in the bin')
    expect(sent('DELETE', '/api/admin/spaces/')).toHaveLength(1)
    await click(buttonIn(dlg(), tg.bulk.close))
    await settle(() => rows().length === 2, 'out of the list')
  })
})

describe('deleting a person', () => {
  const people = (server: MockServer) => {
    const leaver = makePerson(server, 'leaver')
    const viewer = makePerson(server, 'viewer')
    makePerson(server, 'heir')
    const shared = (leaver.request('POST', '/api/spaces', { name: '共有的' }).body as { id: string }).id
    leaver.request('POST', `/api/spaces/${shared}/members`, { username: 'viewer', role: 'viewer' })
    void viewer
  }
  const openPerson = async () => {
    await settle(() => personRows().length >= 4, 'people')
    await click(byLabel(t.fmt.labelled(tc.table.more, '名 leaver')))
    await settle(() => document.querySelectorAll('[role="menuitem"]').length > 0, 'the actions')
    await click(menuItem(tg.people.deletePerson))
    await settle(() => document.querySelector('[data-slot="user-plan"]') !== null, 'the plan')
  }

  it('asks for an heir when a shared space has nobody to take it, then deletes and puts the person in the bin', async () => {
    await open('manage', people)
    await openPerson()
    const box = () => document.querySelector<HTMLElement>('[data-slot="delete-person"]')!
    expect(box().textContent).toContain(tg.del.user.needsHeir)
    expect(box().textContent).toContain('共有的')
    const confirm = () => buttonIn(box(), tg.del.user.confirm)!
    await type(document.querySelector<HTMLInputElement>('#delete-person-confirm')!, 'leaver')
    expect(confirm().disabled).toBe(true)
    // An heir is chosen from a search, and the plan is made again with them.
    const pick = box().querySelector<HTMLSelectElement>('select[aria-label="' + tg.del.user.heir + '"]')!
    await settle(() => pick.options.length > 1, 'people to choose')
    await type(pick, [...pick.options].find((o) => o.textContent?.includes('heir'))!.value)
    await settle(() => box().querySelector('[data-slot="plan-transfer"]') !== null, 'the handover')
    expect(box().querySelector('[data-slot="plan-transfer"]')!.textContent).toContain('heir')
    expect(confirm().disabled).toBe(false)
    await click(confirm())
    await settle(() => text().includes(tg.del.user.deleted('leaver')), 'in the bin')
    const body = JSON.parse(sent('DELETE', '/api/users/')[0].body!) as { heir: string }
    expect(body.heir).toBe(m!.server.userId('heir'))
    expect(m!.server.manage.recycled().map((i) => i.name)).toContain('leaver')
  })

  it('refuses to delete yourself: the plan says so and the button stays off', async () => {
    await open('users')
    await settle(() => text().includes('管理员'), 'users')
    // There is no delete button on your own row.
    expect(document.querySelector(`button[aria-label="${tg.bulk.actions.delete_users} 管理员"]`)).toBeNull()
  })
})

describe('bulk actions', () => {
  const three = (server: MockServer) => {
    const tang = makePerson(server, 'tang')
    for (const n of ['甲', '乙', '丙']) tang.request('POST', '/api/spaces', { name: n })
  }

  it('shows the plan, wants the number typed, runs a job with its progress and then says it is done', async () => {
    await open('spaces', three)
    await settle(() => rows().length === 5, 'spaces')
    await bulk(['甲', '乙'], tg.bulk.actions.delete_spaces)
    await settle(() => document.querySelectorAll('[data-slot="plan-line"]').length === 2, 'the plan')
    expect(dlg().textContent).toContain(tg.bulk.deleteNote(30))
    expect(confirmButton().disabled).toBe(true)
    await typeCount(1)
    expect(confirmButton().disabled).toBe(true)
    await typeCount(2)
    expect(confirmButton().disabled).toBe(false)
    await click(confirmButton())
    await settle(() => document.querySelector('[data-slot="job"]') !== null, 'the job')
    await settle(
      () => document.querySelector('[data-slot="job"]')?.getAttribute('data-state') === 'done',
      'the job is done',
    )
    expect(dlg().textContent).toContain(tg.bulk.doneAll(2))
    expect(sent('POST', '/api/admin/confirm')).toHaveLength(1)
    expect(sent('POST', '/api/admin/bulk')).toHaveLength(1)
    expect(sent('GET', '/api/admin/jobs/').length).toBeGreaterThan(1)
    await click(buttonIn(dlg(), tg.bulk.close))
    await settle(() => rows().length === 3, 'two are gone')
  })

  it('lists what did not work and goes on with the rest', async () => {
    await open('spaces', three)
    await settle(() => rows().length === 5, 'spaces')
    // One named space is busy (by name: spaces made in the same millisecond list in either order).
    m!.server.manage.busy(rowOf('乙').dataset.space!)
    await bulk(['乙', '甲'], tg.bulk.actions.delete_spaces)
    await typeCount(2)
    await click(confirmButton())
    await settle(() => document.querySelector('[data-slot="job"]')?.getAttribute('data-state') === 'done', 'done')
    expect(document.querySelector('[data-slot="job-failed"]')).not.toBeNull()
    expect(dlg().textContent).toContain(tg.bulk.doneSome(1, 1))
    expect(dlg().querySelector('[data-slot="job-failed"]')!.textContent).toContain(tg.errors.busy)
  })

  it('says when the names changed under the plan, and looks again before anything is done', async () => {
    await open('spaces', three)
    await settle(() => rows().length === 5, 'spaces')
    await bulk(['甲', '乙'], tg.bulk.actions.disable_spaces)
    await typeCount(2)
    // Someone adds a member to one of them meanwhile.
    const id = rowOf('甲').dataset.space!
    const other = makePerson(m!.server, 'late')
    adminRequests(m!.server)('POST', `/api/spaces/${id}/members`, { username: other.username, role: 'viewer' })
    await click(confirmButton())
    await settle(() => document.querySelector('[data-slot="stale"]') !== null, 'the stale notice')
    expect(sent('POST', '/api/admin/bulk')).toHaveLength(1)
    expect(document.querySelector('[data-slot="job"]')).toBeNull()
    await click(buttonIn(dlg(), tg.bulk.lookAgain))
    await settle(
      () => document.querySelector('[data-slot="stale"]') === null && sent('POST', '/api/admin/confirm').length === 2,
      'a new plan',
    )
    await typeCount(2)
    await click(confirmButton())
    await settle(() => document.querySelector('[data-slot="job"]')?.getAttribute('data-state') === 'done', 'done')
  })

  it('shows a job that waits its turn, and one the server restart interrupted', async () => {
    await open('spaces', three)
    await settle(() => rows().length === 5, 'spaces')
    m!.server.manage.holdJobs(true)
    await bulk(['甲', '乙'], tg.bulk.actions.disable_spaces)
    await typeCount(2)
    await click(confirmButton())
    await settle(() => document.querySelector('[data-slot="job"]')?.getAttribute('data-state') === 'queued', 'queued')
    expect(dlg().textContent).toContain(tg.bulk.states.queued)
    m!.server.manage.interruptJobs()
    await settle(
      () => document.querySelector('[data-slot="job"]')?.getAttribute('data-state') === 'interrupted',
      'interrupted',
    )
    expect(dlg().textContent).toContain(tg.bulk.states.interrupted)
    expect(dlg().textContent).toContain(tg.bulk.notDone(2))
  })

  it('shows a job that broke', async () => {
    await open('spaces', three)
    await settle(() => rows().length === 5, 'spaces')
    m!.server.manage.breakJobs(true)
    await bulk(['甲', '乙'], tg.bulk.actions.disable_spaces)
    await typeCount(2)
    await click(confirmButton())
    await settle(() => document.querySelector('[data-slot="job"]')?.getAttribute('data-state') === 'failed', 'failed')
    expect(dlg().textContent).toContain(tg.bulk.states.failed)
  })
  it('drops the confirmation of an heir that was cleared and asks for the plan again', async () => {
    await open('manage', (server) => {
      const leaver = makePerson(server, 'leaver')
      makePerson(server, 'viewer')
      makePerson(server, 'heir')
      const shared = (leaver.request('POST', '/api/spaces', { name: '共有的' }).body as { id: string }).id
      leaver.request('POST', `/api/spaces/${shared}/members`, { username: 'viewer', role: 'viewer' })
    })
    await settle(() => personRows().length >= 4, 'people')
    await click(byLabel(tg.people.selectFor('名 leaver')))
    await click(buttonIn(document.querySelector('[data-slot="bulk-bar"]'), tg.bulk.actions.delete_users))
    await settle(() => document.querySelector('[data-slot="needs-heir"]') !== null, 'the heir is asked for')
    const pick = () => document.querySelector<HTMLSelectElement>('select[aria-label="' + tg.bulk.heir + '"]')!
    await settle(() => pick().options.length > 1, 'people to choose')
    await type(pick(), [...pick().options].find((o) => o.textContent?.includes('heir'))!.value)
    await settle(() => sent('POST', '/api/admin/confirm').length === 2, 'the plan with the heir')
    await settle(() => !document.querySelector<HTMLInputElement>('#bulk-count')?.disabled, 'the count box')
    await typeCount(1)
    await settle(() => !confirmButton().disabled, 'ready')
    // Clearing the heir drops the confirmation made with them and asks again, without.
    await type(pick(), '')
    await settle(() => sent('POST', '/api/admin/confirm').length === 3, 'the plan again')
    const last = JSON.parse(sent('POST', '/api/admin/confirm')[2].body!) as { heir?: string }
    expect(last.heir).toBeUndefined()
    await settle(() => document.querySelector('[data-slot="needs-heir"]') !== null, 'the heir is asked for again')
    expect(confirmButton().disabled).toBe(true)
    expect(sent('POST', '/api/admin/bulk')).toHaveLength(0)
  })

  it('asks again with the chosen heir when the plan for them failed, and confirms only a plan made with them', async () => {
    await open('manage', (server) => {
      const leaver = makePerson(server, 'leaver')
      makePerson(server, 'viewer')
      makePerson(server, 'heir')
      const shared = (leaver.request('POST', '/api/spaces', { name: '共有的' }).body as { id: string }).id
      leaver.request('POST', `/api/spaces/${shared}/members`, { username: 'viewer', role: 'viewer' })
    })
    await settle(() => personRows().length >= 4, 'people')
    await click(byLabel(tg.people.selectFor('名 leaver')))
    await click(buttonIn(document.querySelector('[data-slot="bulk-bar"]'), tg.bulk.actions.delete_users))
    await settle(() => document.querySelector('[data-slot="needs-heir"]') !== null, 'the heir is asked for')
    // The plan with the heir fails once.
    let failing = true
    const through = globalThis.fetch
    globalThis.fetch = async (input, init) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url
      if (failing && init?.method === 'POST' && url.startsWith('/api/admin/confirm')) {
        failing = false
        await through(input, init) // noted, and its answer dropped
        return new Response(JSON.stringify({ error: 'down', code: 'internal' }), {
          status: 500,
          headers: { 'Content-Type': 'application/json' },
        })
      }
      return through(input, init)
    }
    const pick = () => document.querySelector<HTMLSelectElement>('select[aria-label="' + tg.bulk.heir + '"]')!
    await settle(() => pick().options.length > 1, 'people to choose')
    const heirId = [...pick().options].find((o) => o.textContent?.includes('heir'))!.value
    await type(pick(), heirId)
    await settle(() => buttonIn(dlg(), tg.retry) !== undefined, 'the retry')
    await click(buttonIn(dlg(), tg.retry)!)
    await settle(() => sent('POST', '/api/admin/confirm').length === 3, 'the plan asked again')
    const last = JSON.parse(sent('POST', '/api/admin/confirm')[2].body!) as { heir?: string }
    expect(last.heir).toBe(heirId)
    await typeCount(1)
    await settle(() => !confirmButton().disabled, 'ready')
  })
})

// A short phone screen: a dialog never grows past it and scrolls inside, and its main buttons are big enough for a
// finger (jsdom has no layout, so what is checked is that the classes that do it are there).
describe('on a phone', () => {
  const fits = (slot: string) => {
    const box = document.querySelector<HTMLElement>(`[data-slot="${slot}"]`)!
    for (const c of fitScreen.split(' ')) expect(box.classList, `${slot} ${c}`).toContain(c)
    const footer = box.querySelector('[data-slot="dialog-footer"]')!
    const buttons = [...footer.querySelectorAll<HTMLElement>('button, a')]
    expect(buttons.length).toBeGreaterThan(0)
    for (const b of buttons)
      for (const c of tapTarget.split(' ')) expect(b.classList, `${slot} ${b.textContent}`).toContain(c)
  }

  it('fits the deletion of a space and the bulk dialog to the screen, with big enough buttons', async () => {
    await open('spaces', (server) => {
      const tang = makePerson(server, 'tang')
      for (const n of ['甲', '乙']) tang.request('POST', '/api/spaces', { name: n })
    })
    await settle(() => rows().length === 4, 'spaces')
    await openMenu('甲')
    await click(menuItem(tg.spaces.delete))
    await settle(() => document.querySelector('[data-slot="deletion-preview"]') !== null, 'the preview')
    fits('delete-space')
    await click(buttonIn(dlg(), tg.cancel)!)
    await settle(() => document.querySelector('[data-slot="delete-space"]') === null, 'closed')
    await bulk(['甲', '乙'], tg.bulk.actions.disable_spaces)
    await settle(() => document.querySelectorAll('[data-slot="plan-line"]').length === 2, 'the plan')
    fits('bulk-dialog')
    for (const b of document.querySelectorAll<HTMLElement>('[data-slot="bulk-bar"] button'))
      for (const c of tapTarget.split(' ')) expect(b.classList).toContain(c)
  })
})
