// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { Route, Routes } from 'react-router'
import type { MockServer } from '@/api/mock/server'
import { FolderList } from '@/pages/Lists'
import { FileOpsProvider } from '@/components/files/FileOps'
import { QuickCaptureProvider } from '@/components/layout/QuickCapture'
import { SidebarProvider } from '@/components/ui/sidebar'
import { viewKey } from '@/lib/listView'
import { t } from '@/i18n'
import { tp } from '@/i18n/props'
import {
  byLabel,
  click,
  installDomShims,
  locationText,
  mount,
  press,
  settle,
  shareSpace,
  type,
  type Mounted,
} from '@/testing/harness'
import { columnsOf, nextSort, tableSortOf } from './TableView'

// The table view of a folder that has a type, against the mock server.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
  vi.restoreAllMocks()
})

const shell = (
  <SidebarProvider>
    <QuickCaptureProvider>
      <Routes>
        <Route
          path="/s/:space/f/*"
          element={
            <FileOpsProvider>
              <FolderList />
            </FileOpsProvider>
          }
        />
      </Routes>
    </QuickCaptureProvider>
  </SidebarProvider>
)

const json = 'application/json'
type Send = (method: string, path: string, body?: unknown) => { status: number; body: any }
const sender =
  (server: MockServer): Send =>
  (method, path, body) =>
    server.handle(
      method,
      `/api/spaces/default${path}`,
      body === undefined ? undefined : JSON.stringify(body),
      json,
    ) as { status: number; body: any }

const TASK = {
  title: '任务',
  fields: [
    { key: 'status', title: '状态', kind: 'select', options: ['todo', 'doing', 'done'] },
    { key: 'points', title: '点数', kind: 'number' },
    { key: 'due', title: '截止', kind: 'date' },
    { key: 'labels', title: '标签组', kind: 'multiselect', options: ['a', 'b', 'c'] },
    { key: 'done', title: '完成', kind: 'checkbox' },
    { key: 'note', title: '备注', kind: 'text' },
  ],
}

// A typed folder `Work` with four pages, and an untyped one, as the single-user server has them.
function seed(server: MockServer) {
  const send = sender(server)
  send('PUT', '/types/task', TASK)
  send('POST', '/folders', { path: 'Work' })
  send('PUT', '/folders/type', { folder: 'Work', type: 'task' })
  const make = (title: string, meta: Record<string, unknown>) =>
    send('POST', '/pages', { folder: 'Work', meta: { title, ...meta }, body: `${title} 正文\n` }).body as {
      id: string
      rev: string
    }
  return {
    A: make('A', { status: 'done', points: 5, due: '2026-03-01', labels: ['a'], done: true, tags: ['x'], note: '甲' }),
    B: make('B', { status: 'todo', points: 12, due: '2026-01-01', labels: ['b', 'a'] }),
    C: make('C', { status: 'doing', points: 7.5 }),
    D: make('D', {}),
  }
}

async function openWork(options: { server?: Parameters<typeof mount>[1] } = {}) {
  let ids: ReturnType<typeof seed> | undefined
  m = await mount(shell, { entries: (server) => ((ids = seed(server)), ['/s/default/f/Work']), ...options.server })
  await settle(() => byLabel(t.view.list) !== null, 'the listing')
  return ids!
}
const showTable = async () => {
  await settle(() => byLabel(t.view.table) !== null, 'the table option')
  await click(byLabel(t.view.table))
  await settle(
    () =>
      document.querySelector('[role="columnheader"]') !== null && document.querySelectorAll('[data-row]').length > 0,
    'the table',
  )
}

const headers = () => [...document.querySelectorAll('[role="columnheader"]')].map((h) => h.textContent?.trim())
const rowEls = () => [...document.querySelectorAll<HTMLElement>('[data-row]')]
const titlesInOrder = () => rowEls().map((r) => r.querySelector('[data-col="title"]')!.textContent)
const rowOf = (title: string) => rowEls().find((r) => r.querySelector('[data-col="title"]')!.textContent === title)!
const cell = (title: string, col: string) => rowOf(title).querySelector<HTMLElement>(`[data-col="${col}"]`)!
const listRequests = () => m!.requests.filter((r) => r.method === 'GET' && /\/pages\?/.test(r.url))
const decoded = (url: string) => decodeURIComponent(url)
const patches = () => m!.requests.filter((r) => r.method === 'PATCH')

describe('the table option', () => {
  it('is offered for a folder that has a type, and not for one that has none', async () => {
    m = await mount(shell, { entries: (server) => (seed(server), ['/s/default/f/Work']) })
    await settle(() => byLabel(t.view.list) !== null, 'the listing')
    await settle(() => byLabel(t.view.table) !== null, 'the table option')
    await m.unmount()
    m = undefined
    m = await mount(shell, { entries: (server) => (seed(server), ['/s/default/f/随笔']) })
    await settle(
      () => byLabel(t.view.list) !== null && document.querySelector('[data-file-key^="p:"]') !== null,
      'the listing',
    )
    await new Promise((r) => setTimeout(r, 50))
    expect(byLabel(t.view.table)).toBeNull()
  })

  it('is offered below a bound folder, which inherits the type', async () => {
    m = await mount(shell, {
      entries: (server) => {
        seed(server)
        sender(server)('POST', '/folders', { path: 'Work/Sub' })
        return ['/s/default/f/Work/Sub']
      },
    })
    await settle(() => byLabel(t.view.table) !== null, 'the table option')
  })

  it('is remembered for the folder, in the browser, and the list and cards are not asked for props', async () => {
    await openWork()
    await settle(() => rowOfList() !== null, 'the list')
    expect(listRequests().every((r) => !r.url.includes('props=1'))).toBe(true)
    await showTable()
    expect(localStorage.getItem(viewKey('default', 'folder', 'Work'))).toBe('table')
    expect(listRequests().some((r) => r.url.includes('props=1'))).toBe(true)
    // The next visit starts in the table, and the plain list is not read at all.
    await m!.unmount()
    m = undefined
    m = await mount(shell, { entries: (server) => (seed(server), ['/s/default/f/Work']) })
    await settle(() => document.querySelectorAll('[data-row]').length === 4, 'the table at once')
    expect(listRequests().every((r) => r.url.includes('props=1'))).toBe(true)
    // Another folder keeps its own choice.
    expect(localStorage.getItem(viewKey('default', 'folder', 'Work/Sub'))).toBeNull()
  })

  it('is not used for a folder that lost its type: the list shows', async () => {
    localStorage.setItem(viewKey('default', 'folder', '随笔'), 'table')
    m = await mount(shell, { entries: (server) => (seed(server), ['/s/default/f/随笔']) })
    await settle(() => document.querySelector('[data-file-key^="p:"]') !== null, 'the list')
    expect(document.querySelector('[data-slot="table-view"]')).toBeNull()
  })
})

const rowOfList = () => document.querySelector('[data-file-key^="p:"]')

describe('the table', () => {
  it("has the title, then the type's fields in order, then tags and updated, and a row per page", async () => {
    await openWork()
    await showTable()
    expect(headers()).toEqual([
      tp.table.title,
      '状态',
      '点数',
      '截止',
      '标签组',
      '完成',
      '备注',
      tp.table.tags,
      tp.table.updated,
    ])
    expect(rowEls()).toHaveLength(4)
    // A title is a link to the page.
    expect(rowOf('A').querySelector('a')!.getAttribute('href')).toMatch(/\/p\//)
    // The values are in the cells, in the controls of their kind.
    expect((cell('A', 'f:status').querySelector('select') as HTMLSelectElement).value).toBe('done')
    expect((cell('A', 'f:points').querySelector('input') as HTMLInputElement).value).toBe('5')
    expect((cell('A', 'f:due').querySelector('input') as HTMLInputElement).value).toBe('2026-03-01')
    expect(cell('B', 'f:labels').textContent).toBe('b, a')
    expect((cell('A', 'f:done').querySelector('input') as HTMLInputElement).checked).toBe(true)
    expect(cell('A', 'f:note').textContent).toBe('甲')
    expect(cell('A', 'tags').textContent).toContain('x')
    // A page with nothing has empty cells.
    expect((cell('D', 'f:status').querySelector('select') as HTMLSelectElement).value).toBe('')
  })

  it('marks a value that does not fit its field, and keeps it', async () => {
    m = await mount(shell, {
      entries: (server) => {
        seed(server)
        sender(server)('POST', '/pages', { folder: 'Work', meta: { title: 'E', status: 'dropped', points: 'lots' } })
        return ['/s/default/f/Work']
      },
    })
    await settle(() => byLabel(t.view.table) !== null, 'the option')
    await showTable()
    const status = cell('E', 'f:status').querySelector('select') as HTMLSelectElement
    expect([status.value, status.getAttribute('aria-invalid')]).toEqual(['dropped', 'true'])
    expect(cell('E', 'f:points').querySelector('input')!.getAttribute('aria-invalid')).toBe('true')
    expect((cell('E', 'f:points').querySelector('input') as HTMLInputElement).value).toBe('lots')
  })

  it('sorts by a column when its header is clicked, and the other way round when it is clicked again', async () => {
    await openWork()
    await showTable()
    const header = (label: string) =>
      [...document.querySelectorAll('[role="columnheader"]')].find((h) => h.textContent?.trim() === label)!
    await click(header('点数').querySelector('button'))
    await settle(() => titlesInOrder().join('') === 'ACBD', 'ascending by points')
    expect(decoded(listRequests().at(-1)!.url)).toContain('sort=prop:points')
    expect(header('点数').getAttribute('aria-sort')).toBe('ascending')
    expect(locationText()).toContain('sort=prop%3Apoints')
    await click(header('点数').querySelector('button'))
    await settle(() => titlesInOrder().join('') === 'BCAD', 'descending by points')
    expect(decoded(listRequests().at(-1)!.url)).toContain('sort=-prop:points')
    expect(header('点数').getAttribute('aria-sort')).toBe('descending')
    // The title and the update time use the sorts the list has.
    await click(header(tp.table.title).querySelector('button'))
    await settle(() => decoded(listRequests().at(-1)!.url).includes('sort=title'), 'by title')
    await settle(() => titlesInOrder().join('') === 'ABCD', 'ascending by title')
    await click(header(tp.table.updated).querySelector('button'))
    await settle(() => /sort=updated(&|$)/.test(decoded(listRequests().at(-1)!.url)), 'by updated')
    // Tags cannot be sorted by.
    expect(header(tp.table.tags).querySelector('button')).toBeNull()
  })

  it('filters with the ops that suit each kind, and the chips take a filter away again', async () => {
    await openWork()
    await showTable()
    const addButton = () => [...document.querySelectorAll('button')].find((b) => b.textContent === tp.filter.add)!
    const form = () => document.querySelector<HTMLFormElement>(`form[aria-label="${tp.filter.add}"]`)!
    const choose = async (field: string) =>
      type(form().querySelector(`select[aria-label="${tp.filter.field}"]`)!, field)
    const ops = () =>
      [...form().querySelectorAll<HTMLOptionElement>(`select[aria-label="${tp.filter.op}"] option`)].map((o) => o.value)
    const apply = () => click([...form().querySelectorAll('button')].find((b) => b.textContent === tp.filter.apply))

    await click(addButton())
    await choose('status')
    expect(ops()).toEqual(['in', 'empty', 'notEmpty'])
    await choose('points')
    expect(ops()).toEqual(['range', 'empty', 'notEmpty'])
    await choose('due')
    expect(ops()).toEqual(['range', 'empty', 'notEmpty'])
    await choose('note')
    expect(ops()).toEqual(['contains', 'empty', 'notEmpty'])
    await choose('done')
    expect(ops()).toEqual(['in', 'empty', 'notEmpty'])
    await choose('status')
    // Nothing to add until a value is chosen.
    expect([...form().querySelectorAll('button')].find((b) => b.textContent === tp.filter.apply)!.disabled).toBe(true)
    await click([...form().querySelectorAll('button')].find((b) => b.textContent === 'todo'))
    await click([...form().querySelectorAll('button')].find((b) => b.textContent === 'doing'))
    await apply()
    await settle(() => rowEls().length === 2, 'filtered')
    expect(titlesInOrder().sort()).toEqual(['B', 'C'])
    const sent = JSON.parse(new URL(listRequests().at(-1)!.url, 'http://x').searchParams.get('filter')!)
    expect(sent).toEqual([{ key: 'status', op: 'in', values: ['todo', 'doing'] }])
    expect(document.querySelector('[data-slot="table-filters"]')!.textContent).toContain(
      tp.filter.describeIn('状态', 'todo, doing'),
    )

    // A second condition: the points from 10 on. Both must hold.
    await click(addButton())
    await choose('points')
    await type(form().querySelector(`input[aria-label="${tp.filter.from}"]`)!, '10')
    await apply()
    await settle(() => rowEls().length === 1, 'both filters')
    expect(titlesInOrder()).toEqual(['B'])
    const both = JSON.parse(new URL(listRequests().at(-1)!.url, 'http://x').searchParams.get('filter')!)
    expect(both[1]).toEqual({ key: 'points', op: 'range', values: [10, null] })

    await click(byLabel(tp.filter.remove(tp.filter.describeRange('点数', '10', ''))))
    await settle(() => rowEls().length === 2, 'one filter left')
    expect(titlesInOrder().sort()).toEqual(['B', 'C'])
    await click(byLabel(tp.filter.remove(tp.filter.describeIn('状态', 'todo, doing'))))
    await settle(() => rowEls().length === 4, 'no filter left')
  })

  it('says so when a filter leaves nothing', async () => {
    await openWork()
    await showTable()
    await click([...document.querySelectorAll('button')].find((b) => b.textContent === tp.filter.add))
    const form = document.querySelector<HTMLFormElement>(`form[aria-label="${tp.filter.add}"]`)!
    await type(form.querySelector(`select[aria-label="${tp.filter.field}"]`)!, 'note')
    await type(form.querySelector(`select[aria-label="${tp.filter.op}"]`)!, 'contains')
    await type(form.querySelector(`input[aria-label="${tp.filter.text}"]`)!, '不会有这个')
    await click([...form.querySelectorAll('button')].find((b) => b.textContent === tp.filter.apply))
    await settle(() => document.body.textContent!.includes(t.list.emptyFiltered), 'the message')
  })
})

describe('editing a cell', () => {
  it('changes the page with the rev of the row, and the row has the new value without the table being read again', async () => {
    const ids = await openWork()
    await showTable()
    const tableReads = () => listRequests().filter((r) => r.url.includes('props=1')).length
    const before = tableReads()
    await type(cell('B', 'f:status').querySelector('select')!, 'done')
    await settle(() => patches().length === 1, 'the request')
    const sent = JSON.parse(patches()[0].body!)
    expect(patches()[0].url).toContain(`/pages/${ids.B.id}/props`)
    expect(sent).toEqual({ baseRev: ids.B.rev, set: { status: 'done' } })
    await settle(() => m!.server.peek(ids.B.id)!.meta.status === 'done', 'the file')
    expect((cell('B', 'f:status').querySelector('select') as HTMLSelectElement).value).toBe('done')
    const afterFirst = m!.server.peek(ids.B.id)!.rev
    expect(afterFirst).not.toBe(ids.B.rev)
    // The next change uses the rev the answer brought: no 409.
    await settle(() => !cell('B', 'f:points').querySelector('input')!.hasAttribute('readonly'), 'the row free again')
    const points = () => cell('B', 'f:points').querySelector('input')!
    await act(async () => points().focus())
    await type(points(), '20')
    await press(points(), 'Enter')
    await settle(() => patches().length === 2, 'the second request')
    expect(JSON.parse(patches()[1].body!)).toEqual({ baseRev: afterFirst, set: { points: 20 } })
    await settle(() => m!.server.peek(ids.B.id)!.meta.points === 20, 'the number')
    expect(patches().every((p) => p.method === 'PATCH')).toBe(true)
    // The table itself was not read again.
    await new Promise((r) => setTimeout(r, 50))
    expect(tableReads()).toBe(before)
    // The page's other keys, its title and its text, were left alone.
    expect(m!.server.peek(ids.B.id)).toMatchObject({
      body: 'B 正文\n',
      meta: { title: 'B', status: 'done', due: '2026-01-01', labels: ['b', 'a'] },
    })
  })

  it('sends a number once, when the person leaves the cell or presses Enter, and not at all for Escape or for what is not a number', async () => {
    const ids = await openWork()
    await showTable()
    const points = () => cell('C', 'f:points').querySelector('input')!
    const focus = () => act(async () => points().focus())
    const blur = () => act(async () => points().blur())
    // Typed in pieces: nothing goes to the server while the person is still typing.
    await focus()
    for (const text of ['1', '12', '12.', '12.5', '12.55']) await type(points(), text)
    await new Promise((r) => setTimeout(r, 30))
    expect(patches()).toHaveLength(0)
    expect(points().value).toBe('12.55')
    await blur()
    await settle(() => patches().length === 1, 'the request when the cell is left')
    expect(JSON.parse(patches()[0].body!)).toEqual({ baseRev: ids.C.rev, set: { points: 12.55 } })
    await settle(() => m!.server.peek(ids.C.id)!.meta.points === 12.55, 'the number')
    await settle(() => !points().hasAttribute('readonly'), 'the row free again')
    // Enter commits too.
    await focus()
    await type(points(), '3')
    await press(points(), 'Enter')
    await settle(() => patches().length === 2, 'the request for Enter')
    expect(JSON.parse(patches()[1].body!).set).toEqual({ points: 3 })
    await settle(
      () => m!.server.peek(ids.C.id)!.meta.points === 3 && !points().hasAttribute('readonly'),
      'the second number',
    )
    // Escape drops what was typed and keeps the value.
    await focus()
    await type(points(), '99')
    await press(points(), 'Escape')
    await new Promise((r) => setTimeout(r, 30))
    expect(patches()).toHaveLength(2)
    expect(points().value).toBe('3')
    // Text that is no number, and the value it already has, send nothing.
    await focus()
    await type(points(), 'abc')
    await blur()
    await focus()
    await type(points(), '3')
    await blur()
    await new Promise((r) => setTimeout(r, 30))
    expect(patches()).toHaveLength(2)
    expect(points().value).toBe('3')
    // Emptied, it takes the key out.
    await focus()
    await type(points(), '')
    await blur()
    await settle(() => patches().length === 3, 'the request for an emptied cell')
    expect(JSON.parse(patches()[2].body!)).toMatchObject({ unset: ['points'] })
  })

  it('shows a value the listing holds only in part as it is, with a hint, and changes nothing there', async () => {
    // Made behind the table's back: an object where a number goes, cut text, a list that lost an element.
    const long = '字'.repeat(301)
    m = await mount(shell, {
      entries: (server) => {
        seed(server)
        const made = sender(server)('POST', '/pages', {
          folder: 'Work',
          meta: { title: 'E', points: { a: 1 }, note: long, labels: ['a', { x: 1 }], status: 'done' },
          body: '',
        })
        expect(made.status).toBe(201)
        return ['/s/default/f/Work']
      },
    })
    await settle(() => byLabel(t.view.list) !== null, 'the listing')
    await showTable()
    await settle(() => rowEls().some((r) => r.querySelector('[data-col="title"]')!.textContent === 'E'), 'the row')
    for (const col of ['f:points', 'f:note', 'f:labels']) {
      const c = cell('E', col)
      expect(c.querySelector('[data-partial]'), col).not.toBeNull()
      expect(c.querySelector('[data-partial]')!.getAttribute('title')).toBe(tp.table.partial)
      expect(c.querySelector('input, button, select'), col).toBeNull()
    }
    // What is whole in the same row is still editable, and so are the other rows.
    expect(cell('E', 'f:status').querySelector('select')).not.toBeNull()
    expect(cell('B', 'f:points').querySelector('input')).not.toBeNull()
    expect(rowOf('B').querySelector('[data-partial]')).toBeNull()
    expect(patches()).toHaveLength(0)
    // Changing the whole key does not touch the others: the object stays as it is.
    await type(cell('E', 'f:status').querySelector('select')!, 'todo')
    await settle(() => patches().length === 1, 'the request')
    expect(JSON.parse(patches()[0].body!).set).toEqual({ status: 'todo' })
  })

  it('edits a date, a checkbox and a list of choices, and emptying a cell removes the key', async () => {
    const ids = await openWork()
    await showTable()
    await type(cell('C', 'f:due').querySelector('input')!, '2026-05-05')
    await settle(() => m!.server.peek(ids.C.id)!.meta.due === '2026-05-05', 'the date')
    await click(cell('C', 'f:done').querySelector('input'))
    await settle(() => m!.server.peek(ids.C.id)!.meta.done === true, 'the checkbox')
    await click(cell('C', 'f:labels').querySelector('button'))
    await settle(() => document.querySelector('[data-slot="popover-content"]') !== null, 'the choices')
    await click(
      [...document.querySelectorAll('[data-slot="popover-content"] input')].find(
        (i) => i.closest('label')!.textContent === 'c',
      ),
    )
    await settle(() => JSON.stringify(m!.server.peek(ids.C.id)!.meta.labels) === '["c"]', 'the list')
    await type(cell('C', 'f:status').querySelector('select')!, '')
    await settle(() => !('status' in m!.server.peek(ids.C.id)!.meta), 'the key removed')
    expect(JSON.parse(patches().at(-1)!.body!)).toMatchObject({ unset: ['status'] })
  })

  it('when the page was changed elsewhere, says so, shows the page as it is now, and does not change it', async () => {
    const ids = await openWork()
    await showTable()
    // Someone else changed B after the table was read.
    const other = sender(m!.server)('PATCH', `/pages/${ids.B.id}/props`, {
      baseRev: ids.B.rev,
      set: { status: 'doing' },
    })
    expect(other.status).toBe(200)
    await type(cell('B', 'f:status').querySelector('select')!, 'done')
    await settle(() => document.body.textContent!.includes(tp.table.conflict('B')), 'the message')
    // Nothing of the edit was applied, and the row shows what the server has.
    expect(m!.server.peek(ids.B.id)!.meta.status).toBe('doing')
    await settle(
      () => (cell('B', 'f:status').querySelector('select') as HTMLSelectElement).value === 'doing',
      'the row refreshed',
    )
    // The row has the new rev: the same edit made again goes through.
    await type(cell('B', 'f:status').querySelector('select')!, 'done')
    await settle(() => m!.server.peek(ids.B.id)!.meta.status === 'done', 'the second try')
  })

  it('of two rows edited in a row frees both when the answers come, and tells of the one that failed', async () => {
    let ids: ReturnType<typeof seed> | undefined
    m = await mount(shell, {
      entries: (server) => ((ids = seed(server)), ['/s/default/f/Work']),
      delay: (method) => (method === 'PATCH' ? 300 : 0),
    })
    await settle(() => byLabel(t.view.list) !== null, 'the listing')
    await showTable()
    // B was changed elsewhere, so its edit is refused; C is edited before B has been answered.
    sender(m!.server)('PATCH', `/pages/${ids!.B.id}/props`, { baseRev: ids!.B.rev, set: { status: 'doing' } })
    await type(cell('B', 'f:status').querySelector('select')!, 'done')
    await type(cell('C', 'f:status').querySelector('select')!, 'todo')
    await settle(() => patches().length === 2, 'both requests')
    await settle(() => document.body.textContent!.includes(tp.table.conflict('B')), 'the message for B')
    await settle(() => m!.server.peek(ids!.C.id)!.meta.status === 'todo', 'C changed')
    await settle(
      () =>
        [...document.querySelectorAll('[data-row] [aria-busy="true"], [data-row][aria-busy="true"]')].length === 0 &&
        !(cell('B', 'f:status').querySelector('select') as HTMLSelectElement).disabled,
      'no row left busy',
    )
  })

  it('cannot be done by someone who may only read: the values are text, and nothing is sent', async () => {
    m = await mount(shell, {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        const { space, owner } = shareSpace(server, 'viewer')
        owner.request('PUT', `/api/spaces/${space}/types/task`, TASK)
        owner.request('POST', `/api/spaces/${space}/folders`, { path: 'Work' })
        owner.request('PUT', `/api/spaces/${space}/folders/type`, { folder: 'Work', type: 'task' })
        owner.request('POST', `/api/spaces/${space}/pages`, {
          folder: 'Work',
          meta: { title: 'A', status: 'done', points: 5, done: true, labels: ['a'] },
          body: '',
        })
        return [`/s/${space}/f/Work`]
      },
    })
    await settle(() => byLabel(t.view.table) !== null, 'the option')
    await showTable()
    expect(headers()).toContain('状态')
    expect(cell('A', 'f:status').textContent).toBe('done')
    expect(cell('A', 'f:points').textContent).toBe('5')
    expect(cell('A', 'f:labels').textContent).toBe('a')
    expect(document.querySelector('[role="cell"] select, [role="cell"] input, [role="cell"] button')).toBeNull()
    expect(m.requests.filter((r) => r.method !== 'GET' && r.url !== '/api/login')).toEqual([])
  })
})

describe('a long folder', () => {
  const MANY = 250

  it('is read a hundred rows at a time as the table is scrolled, and only the rows in view are drawn', async () => {
    m = await mount(shell, {
      entries: (server) => {
        const send = sender(server)
        send('PUT', '/types/task', TASK)
        send('POST', '/folders', { path: 'Big' })
        send('PUT', '/folders/type', { folder: 'Big', type: 'task' })
        for (let i = 0; i < MANY; i++)
          send('POST', '/pages', {
            folder: 'Big',
            meta: { title: `行${String(i).padStart(3, '0')}`, points: i },
            body: '',
          })
        return ['/s/default/f/Big']
      },
    })
    await settle(() => byLabel(t.view.table) !== null, 'the option')
    await showTable()
    const limits = () =>
      listRequests()
        .filter((r) => r.url.includes('props=1'))
        .map((r) => new URL(r.url, 'http://x').searchParams.get('limit'))
    expect(limits()).toEqual(['100'])
    // Far fewer rows than are loaded are in the page, and the footer says how many are.
    expect(rowEls().length).toBeLessThan(40)
    expect(document.body.textContent).toContain(t.list.loaded(100))
    const scroller = document.querySelector<HTMLElement>('[data-slot="table-scroller"]')!
    // Near the end of what is loaded: the next hundred are asked for, with the cursor.
    await act(async () => {
      scroller.scrollTop = 44 * 95
      scroller.dispatchEvent(new Event('scroll'))
    })
    await settle(() => limits().length === 2, 'the second batch')
    expect(
      new URL(
        listRequests()
          .filter((r) => r.url.includes('props=1'))
          .at(-1)!.url,
        'http://x',
      ).searchParams.get('cursor'),
    ).toBeTruthy()
    await settle(() => document.body.textContent!.includes(t.list.loaded(200)), 'two hundred loaded')
    expect(rowEls().length).toBeLessThan(40)
    await act(async () => {
      scroller.scrollTop = 44 * 195
      scroller.dispatchEvent(new Event('scroll'))
    })
    await settle(() => document.body.textContent!.includes(t.list.allLoaded(MANY)), 'all loaded')
    expect(limits()).toEqual(['100', '100', '100'])
    expect(rowEls().length).toBeLessThan(40)
  })
})

describe('what the table is made of', () => {
  it('the columns follow the type, and a type that is missing leaves the title, tags and update time', () => {
    expect(columnsOf(undefined).map((c) => c.id)).toEqual(['title', 'tags', 'updated'])
    const cols = columnsOf({
      name: 'x',
      title: '',
      rev: '',
      fields: [
        { key: 'a', title: '', kind: 'text' },
        { key: 'b', title: 'B', kind: 'number' },
      ],
    })
    expect(cols.map((c) => [c.id, c.label, c.sort])).toEqual([
      ['title', tp.table.title, 'title'],
      ['f:a', 'a', 'prop:a'],
      ['f:b', 'B', 'prop:b'],
      ['tags', tp.table.tags, undefined],
      ['updated', tp.table.updated, 'updated'],
    ])
  })
  it('a click sorts ascending, then descending, then ascending; from the default, updated goes oldest first', () => {
    expect(nextSort(undefined, 'prop:a')).toBe('prop:a')
    expect(nextSort('prop:a', 'prop:a')).toBe('-prop:a')
    expect(nextSort('-prop:a', 'prop:a')).toBe('prop:a')
    expect(nextSort('prop:b', 'prop:a')).toBe('prop:a')
    expect(nextSort(undefined, 'updated')).toBe('updated')
    expect(nextSort('updated', 'updated')).toBe('-updated')
  })
  it('only sorts the table understands are taken from the address', () => {
    expect(tableSortOf('prop:status')).toBe('prop:status')
    expect(tableSortOf('-title')).toBe('-title')
    expect(tableSortOf('-created')).toBeUndefined()
    expect(tableSortOf('')).toBeUndefined()
    expect(tableSortOf(null)).toBeUndefined()
  })
})
