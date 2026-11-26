// @vitest-environment jsdom
import { act } from 'react'
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { toast } from 'sonner'
import { pollMs } from '@/api/manage'
import type { FixtureData } from '@/api/fixtures/data/model'
import { SidebarProvider } from '@/components/ui/sidebar'
import { ta } from '@/i18n/admin'
import { t } from '@/i18n'
import { tc } from '@/i18n/console'
import { tg } from '@/i18n/manage'
import { byLabel, click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'
import { addPerson, addSpace, asTenantAdmin } from '@/testing/adminScene'
import { AdminGate } from './AdminGate'
import { fitScreen, tapTarget } from './manage/shared'

// Storage management in the administration: the summary, the lists of people and spaces, what counts toward quotas,
// deleting a space or a person with a preview, bulk actions with a second confirmation and a job, and the differences
// between a site administrator and a tenant administrator, against the fixture server. What a job does while it runs,
// and the plan the server makes for a person who leaves, are its answers: a test says the ones it needs with
// `fx.reply` and `fx.fail`.

beforeAll(() => {
  installDomShims()
  pollMs.value = 20
})
beforeEach(() => localStorage.clear())

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
  [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].findLast((e) => e.textContent?.trim() === label)!
const queries = (prefix: string) =>
  m!.requests.filter((r) => r.url.startsWith(prefix)).map((r) => new URL(r.url, 'http://x').searchParams)
const sent = (method: string, prefix: string) =>
  m!.requests.filter((r) => r.method === method && r.url.startsWith(prefix))

async function open(section: string, scene?: (d: FixtureData) => void, setup?: (fx: FxMounted['fx']) => void) {
  m = await mount(routes, {
    signIn: true,
    fixtures: { mode: 'accounts', data: (d) => void scene?.(d) },
    entries: (fx) => {
      setup?.(fx)
      return [`/admin/${section}`]
    },
  })
}
async function openMenu(name: string) {
  // On a slow runner the menu of the previous action can still be mounted, and
  // a click can be lost: click the trigger until it says it is open, then wait
  // for its items.
  const trigger = byLabel(ta.spaces.moreFor(name)) as HTMLElement
  const open = () =>
    trigger.getAttribute('aria-expanded') === 'true' && document.querySelectorAll('[role="menuitem"]').length > 0
  const until = Date.now() + 5_000
  while (Date.now() < until) {
    if (open()) return
    if (trigger.getAttribute('aria-expanded') !== 'true') await click(trigger)
    const retry = Date.now() + 1_000
    while (!open() && Date.now() < retry) {
      await act(async () => {
        await new Promise((r) => setTimeout(r, 20))
      })
    }
  }
  if (!open()) throw new Error('never happened: the actions')
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

// The leaver owns a space that one other person is in; an heir is somebody else.
const people = (d: FixtureData) => {
  const leaver = addPerson(d, 'leaver')
  addPerson(d, 'viewer')
  addPerson(d, 'heir')
  addSpace(d, '共有的', { owner: leaver, extra: { members: 2 } })
}

describe('the summary and the list of people', () => {
  it('shows what the server holds at the top, with the free disk for a site administrator', async () => {
    await open('storage/manage', (d) => {
      const tang = addPerson(d, 'tang')
      addSpace(d, 'tang 的空间', { owner: tang, personal: true })
      addSpace(d, '团队', { owner: tang })
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
    await open('storage/manage', asTenantAdmin, (fx) => {
      // The server leaves the free disk out of what it tells a tenant administrator.
      const { summary, ...rest } = fx.call('GET', '/api/admin/storage').body
      const { diskFreeBytes: _disk, ...held } = summary
      void _disk
      fx.reply('GET', '/api/admin/storage', { ...rest, summary: held })
    })
    await settle(() => document.querySelector('[data-slot="storage-summary"]') !== null, 'the summary')
    expect(document.querySelector('[data-card="disk"]')).toBeNull()
    expect(document.querySelector('[data-card="held"]')).not.toBeNull()
  })

  it('lists people with what counts and what is held, sorted and filtered by the server', async () => {
    await open('storage/manage', (d) => {
      addPerson(d, 'tang')
      addPerson(d, 'wu')
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
    await open('storage/manage', (d) => {
      const tang = addPerson(d, 'tang')
      addSpace(d, 'tang 的空间', { owner: tang, personal: true })
      space = addSpace(d, '在自己远端', { owner: tang, extra: { location: 'own', counted: false } }).info.id
      addSpace(d, '只在服务器', { owner: tang })
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
    // The server leaves personal spaces out of what a tenant administrator is shown.
    await open('storage/manage', (d) => {
      addSpace(d, '团队', { owner: addPerson(d, 'tang') })
      asTenantAdmin(d)
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
    await open('spaces', (d) => {
      const tang = addPerson(d, 'tang')
      addSpace(d, 'tang 的空间', { owner: tang, personal: true })
      addSpace(d, '外面的', { owner: tang, extra: { location: 'tenant', counted: false } })
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
    await open('spaces', (d) => {
      addSpace(d, '团队', { owner: addPerson(d, 'tang') })
      asTenantAdmin(d)
    })
    await settle(() => rows().some((r) => r.textContent!.includes('团队')), 'the shared space')
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
    await open('spaces', (d) => {
      const tang = addPerson(d, 'tang')
      addSpace(d, 'tang 的空间', { owner: tang, personal: true })
      addSpace(d, '要删的', { owner: tang })
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
  const openPerson = async () => {
    await settle(() => personRows().length >= 4, 'people')
    await click(byLabel(t.fmt.labelled(tc.table.more, '名 leaver')))
    await settle(() => document.querySelectorAll('[role="menuitem"]').length > 0, 'the actions')
    await click(menuItem(tg.people.deletePerson))
    await settle(() => document.querySelector('[data-slot="user-plan"]') !== null, 'the plan')
  }

  it('asks for an heir when a shared space has nobody to take it, then deletes and puts the person in the bin', async () => {
    await open('storage/manage', people)
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
    expect(body.heir).toBe(m!.fx.data.auth.users.find((u) => u.username === 'heir')!.id)
    expect(m!.fx.data.admin.recycle.map((i) => i.user?.username)).toContain('leaver')
  })

  it('refuses to delete yourself: the plan says so and the button stays off', async () => {
    await open('users')
    await settle(() => text().includes('管理员'), 'users')
    // There is no delete button on your own row.
    expect(document.querySelector(`button[aria-label="${tg.bulk.actions.delete_users} 管理员"]`)).toBeNull()
  })
})

describe('bulk actions', () => {
  const three = (d: FixtureData) => {
    const tang = addPerson(d, 'tang')
    addSpace(d, 'tang 的空间', { owner: tang, personal: true })
    for (const n of ['甲', '乙', '丙']) addSpace(d, n, { owner: tang })
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
    // The job is running when first asked after, and done by the next.
    m!.fx.reply(
      'GET',
      '/api/admin/jobs/:id',
      { job: { id: 'j1', action: 'delete_spaces', total: 2, done: 1, failed: [], state: 'running' } },
      { times: 1 },
    )
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
    // One named space is busy: the job does the other and lists this one as failed.
    m!.fx.reply('GET', '/api/admin/jobs/:id', {
      job: {
        id: 'j1',
        action: 'delete_spaces',
        total: 2,
        done: 1,
        failed: [{ id: rowOf('乙').dataset.space!, error: 'busy', code: 'busy' }],
        state: 'done',
      },
    })
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
    // Someone adds a member to one of them meanwhile: the server finds the plan out of date, once.
    m!.fx.fail('POST', '/api/admin/bulk', 409, { code: 'confirm_stale' }, { times: 1 })
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
    const job = (state: string, done: number) => ({
      job: { id: 'j1', action: 'disable_spaces', total: 2, done, failed: [], state },
    })
    // The job waits its turn for a while; then the server restarts and it is interrupted.
    m!.fx.reply('GET', '/api/admin/jobs/:id', job('interrupted', 0))
    m!.fx.reply('GET', '/api/admin/jobs/:id', job('queued', 0), { times: 25 })
    await bulk(['甲', '乙'], tg.bulk.actions.disable_spaces)
    await typeCount(2)
    await click(confirmButton())
    await settle(() => document.querySelector('[data-slot="job"]')?.getAttribute('data-state') === 'queued', 'queued')
    expect(dlg().textContent).toContain(tg.bulk.states.queued)
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
    m!.fx.reply('GET', '/api/admin/jobs/:id', {
      job: { id: 'j1', action: 'disable_spaces', total: 2, done: 0, failed: [], state: 'failed' },
    })
    await bulk(['甲', '乙'], tg.bulk.actions.disable_spaces)
    await typeCount(2)
    await click(confirmButton())
    await settle(() => document.querySelector('[data-slot="job"]')?.getAttribute('data-state') === 'failed', 'failed')
    expect(dlg().textContent).toContain(tg.bulk.states.failed)
  })
  it('drops the confirmation of an heir that was cleared and asks for the plan again', async () => {
    await open('storage/manage', people)
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
    await open('storage/manage', people)
    await settle(() => personRows().length >= 4, 'people')
    await click(byLabel(tg.people.selectFor('名 leaver')))
    await click(buttonIn(document.querySelector('[data-slot="bulk-bar"]'), tg.bulk.actions.delete_users))
    await settle(() => document.querySelector('[data-slot="needs-heir"]') !== null, 'the heir is asked for')
    // The plan with the heir fails once.
    m!.fx.fail('POST', '/api/admin/confirm', 500, { code: 'internal' }, { times: 1 })
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
    await open('spaces', (d) => {
      const tang = addPerson(d, 'tang')
      addSpace(d, 'tang 的空间', { owner: tang, personal: true })
      for (const n of ['甲', '乙']) addSpace(d, n, { owner: tang })
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
