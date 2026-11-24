// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { byLabel, click, installDomShims, mount, press, settle, shareSpace, type Mounted } from '@/testing/harness'
import { listShell, putViews, seedWork, sender, TASK, type Send } from '@/testing/listKit'
import { t } from '@/i18n'
import { tb } from '@/i18n/boards'

// The board of a folder or a tag against the mock server: its columns, their paging, and moving cards.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
  vi.restoreAllMocks()
})

const BOARD = { name: '看板', layout: 'board', group: 'status', cardFields: ['points', 'due'] }
const col = (id: string) => document.querySelector<HTMLElement>(`[data-column="${id}"]`)!
const cardsIn = (id: string) =>
  [...(col(id) ?? document.createElement('div')).querySelectorAll<HTMLElement>('[data-card]')].map((c) =>
    c.getAttribute('aria-label'),
  )
const card = (title: string) =>
  [...document.querySelectorAll<HTMLElement>('[data-card]')].find((c) => c.getAttribute('aria-label') === title)!
// A card once it is drawn: the columns fill in one by one, so a card in another column may not be there yet.
const shown = async (title: string) => {
  await settle(
    () => [...document.querySelectorAll('[data-card]')].some((c) => c.getAttribute('aria-label') === title),
    `the card ${title}`,
  )
  return card(title)
}
const count = (id: string) => col(id).querySelector('[data-slot="column-count"]')!.textContent
const columnIds = () => [...document.querySelectorAll('[data-column]')].map((c) => c.getAttribute('data-column'))
const listRequests = () =>
  m!.requests
    .filter((r) => r.method === 'GET' && /\/pages\?/.test(r.url))
    .map((r) => new URL(r.url, 'http://x').searchParams)
const filterOf = (q: URLSearchParams) =>
  JSON.parse(q.get('filter') ?? '[]') as { key: string; op: string; values?: unknown[] }[]
const patches = () => m!.requests.filter((r) => r.method === 'PATCH')
const textButton = (label: string, within: ParentNode = document) =>
  [...within.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === label) ?? null

async function openBoard(
  scene?: (send: Send, ids: ReturnType<typeof seedWork>) => void,
  path = '/s/default/f/Work',
  options: Parameters<typeof mount>[1] = {},
) {
  let made: ReturnType<typeof seedWork> | undefined
  m = await mount(listShell, {
    ...options,
    entries: (server) => {
      const send = sender(server)
      made = seedWork(server)
      putViews(send, path.includes('/t/') ? { tag: decodeURIComponent(path.split('/t/')[1]) } : { folder: 'Work' }, [
        BOARD,
      ])
      scene?.(send, made)
      return [path]
    },
  })
  await settle(
    () =>
      document.querySelector('[data-slot="board-view"]') !== null &&
      document.querySelectorAll('[data-column]').length > 0,
    'the board',
  )
  return made!
}

// One drag of a card onto a column, as the browser fires it.
async function drag(from: Element, to: Element, { drop = true } = {}) {
  const dt = { setData() {}, effectAllowed: '', dropEffect: '', types: [] as string[] }
  const fire = (el: Element, type: string) =>
    act(async () => {
      const e = new Event(type, { bubbles: true, cancelable: true })
      Object.assign(e, { dataTransfer: dt })
      el.dispatchEvent(e)
    })
  await fire(from, 'dragstart')
  await fire(to, 'dragover')
  if (drop) await fire(to, 'drop')
  await fire(from, 'dragend')
}

describe('the columns', () => {
  it('are the options in the order of the type, then "empty"; the heads say how many pages are in each, from the index', async () => {
    await openBoard()
    await settle(() => cardsIn('o:todo').length === 1 && cardsIn('o:done').length === 1, 'cards')
    expect(columnIds()).toEqual(['o:todo', 'o:doing', 'o:done', 'empty'])
    expect(['o:todo', 'o:doing', 'o:done', 'empty'].map(count)).toEqual(['1', '1', '1', '1'])
    expect(cardsIn('o:todo')).toEqual(['B'])
    expect(cardsIn('o:doing')).toEqual(['C'])
    expect(cardsIn('o:done')).toEqual(['A'])
    expect(cardsIn('empty')).toEqual(['D'])
    const groups = m!.requests.filter((r) => /\/pages\/groups\?/.test(r.url))
    expect(groups).toHaveLength(1)
    expect(decodeURIComponent(groups[0].url)).toContain('key=status')
    expect(decodeURIComponent(groups[0].url)).toContain('folder=Work')
  })

  it('have an "other values" column only when some page has a value that is not an option, and a card cannot be dropped on it', async () => {
    await openBoard((send) =>
      send('POST', '/pages', { folder: 'Work', meta: { title: 'E', status: 'dropped' }, body: '' }),
    )
    await settle(() => cardsIn('other').length === 1, 'the other column')
    expect(columnIds()).toEqual(['o:todo', 'o:doing', 'o:done', 'other', 'empty'])
    expect(count('other')).toBe('1')
    expect(col('other').textContent).toContain(tb.board.other)
    const q = listRequests().find((s) => filterOf(s).some((f) => f.op === 'notIn'))!
    expect(filterOf(q)).toEqual([{ key: 'status', op: 'notIn', values: ['todo', 'doing', 'done'] }])
    await drag(await shown('B'), col('other'))
    expect(patches()).toHaveLength(0)
  })

  it('ask for each by one filter, twenty cards at a time, with the props; a column the counts say is empty is not asked for', async () => {
    await openBoard((send) => {
      // An option nobody has.
      const rev = (send('GET', '/types').body as { types: { rev: string }[] }).types[0].rev
      send('PUT', '/types/task', {
        ...TASK,
        baseRev: rev,
        fields: [{ ...TASK.fields[0], options: ['todo', 'doing', 'done', 'never'] }, ...TASK.fields.slice(1)],
      })
    })
    await settle(() => columnIds().includes('o:never'), 'the new option')
    await settle(() => cardsIn('o:done').length === 1, 'cards')
    expect(count('o:never')).toBe('0')
    expect(col('o:never').textContent).toContain(tb.board.noCards)
    const qs = listRequests().filter((q) => q.get('props') === '1' && filterOf(q).length)
    for (const q of qs) expect(q.get('limit')).toBe('20')
    const ops = qs.map((q) =>
      filterOf(q)
        .map((f) => `${f.op}:${(f.values ?? []).join('|')}`)
        .join(),
    )
    expect(ops).toEqual(expect.arrayContaining(['in:todo', 'in:doing', 'in:done', 'empty:']))
    expect(ops.some((o) => o.includes('never'))).toBe(false)
  })

  it('page by their own cursor: a column of forty-five pages is read in three batches, and the others are not read again', async () => {
    await openBoard((send) => {
      for (let i = 0; i < 44; i++)
        send('POST', '/pages', {
          folder: 'Work',
          meta: { title: `T${String(i).padStart(2, '0')}`, status: 'todo' },
          body: '',
        })
    })
    await settle(() => cardsIn('o:todo').length === 20, 'the first twenty')
    expect(count('o:todo')).toBe('45')
    const todoQ = () =>
      listRequests().filter(
        (q) => q.get('props') === '1' && filterOf(q).some((f) => f.op === 'in' && f.values?.[0] === 'todo'),
      )
    expect(todoQ()).toHaveLength(1)
    const doneBefore = listRequests().filter((q) => filterOf(q).some((f) => f.values?.[0] === 'done')).length
    await click(textButton(tb.board.loadMore, col('o:todo')))
    await settle(() => cardsIn('o:todo').length === 40, 'forty')
    expect(todoQ()[1].get('cursor')).toBeTruthy()
    // A scroll to the bottom of the column asks too.
    const scroller = col('o:todo').querySelector<HTMLElement>('.overflow-y-auto')!
    Object.defineProperty(scroller, 'scrollHeight', { value: 1000, configurable: true })
    Object.defineProperty(scroller, 'clientHeight', { value: 400, configurable: true })
    await act(async () => {
      scroller.scrollTop = 560
      scroller.dispatchEvent(new Event('scroll', { bubbles: true }))
    })
    await settle(() => cardsIn('o:todo').length === 45, 'all forty-five')
    expect(textButton(tb.board.loadMore, col('o:todo'))).toBeNull()
    expect(todoQ()).toHaveLength(3)
    expect(listRequests().filter((q) => filterOf(q).some((f) => f.values?.[0] === 'done')).length).toBe(doneBefore)
  })
})

describe('a card', () => {
  it('shows the title, two lines of the text, the chosen fields and the tags; an untitled page is its text', async () => {
    await openBoard((send) => {
      send('POST', '/pages', {
        folder: 'Work',
        meta: { status: 'doing', tags: ['栀子'] },
        body: '没有标题的一页，从这里开始写。\n',
      })
    })
    await settle(() => cardsIn('o:done').length === 1, 'cards')
    const a = await shown('A')
    expect(a.textContent).toContain('A 正文')
    expect(a.querySelector('[data-field="points"]')!.textContent).toBe('点数5')
    expect(a.querySelector('[data-field="due"]')!.textContent).toBe('截止2026-03-01')
    // Only the chosen fields.
    expect(a.querySelector('[data-field="labels"]')).toBeNull()
    expect(a.textContent).toContain('栀子')
    expect(a.querySelector('.line-clamp-2')).not.toBeNull()
    // A field the page has no value for is not shown.
    expect((await shown('D')).querySelector('[data-field]')).toBeNull()
    await settle(
      () => [...col('o:doing').querySelectorAll('[data-card]')].some((c) => c.textContent!.includes('没有标题的一页')),
      'the untitled card',
    )
    const untitled = [...col('o:doing').querySelectorAll<HTMLElement>('[data-card]')].find((c) =>
      c.textContent!.includes('没有标题的一页'),
    )!
    expect(untitled.querySelector('a')!.textContent).toContain('没有标题的一页')
    expect(untitled.textContent).not.toContain('《')
    // The title opens the page.
    expect(a.querySelector('a')!.getAttribute('href')).toMatch(/\/p\//)
  })

  it('takes the first three fields of the type when the view chose none', async () => {
    // A view that chose no fields.
    await openBoard((send) => putViews(send, { folder: 'Work' }, [{ name: '看板', layout: 'board', group: 'status' }]))
    await settle(() => cardsIn('o:done').length === 1, 'cards')
    const fields = [...(await shown('A')).querySelectorAll('[data-field]')].map((f) => f.getAttribute('data-field'))
    expect(fields).toEqual(['points', 'due', 'labels'])
  })
})

describe('moving a card', () => {
  it('to another column sets the property with the rev of the card, and the card and the counts follow', async () => {
    const ids = await openBoard()
    await settle(() => cardsIn('o:todo').length === 1, 'cards')
    expect((await shown('B')).getAttribute('draggable')).toBe('true')
    await drag(await shown('B'), col('o:doing'))
    await settle(() => patches().length === 1, 'the request')
    expect(patches()[0].url).toContain(`/pages/${ids.B.id}/props`)
    expect(JSON.parse(patches()[0].body!)).toEqual({ baseRev: ids.B.rev, set: { status: 'doing' } })
    expect(m!.server.peek(ids.B.id)!.meta.status).toBe('doing')
    await settle(() => cardsIn('o:doing').length === 2, 'the card in its new column')
    expect(cardsIn('o:todo')).toEqual([])
    expect(['o:todo', 'o:doing'].map(count)).toEqual(['0', '2'])
  })

  it('to "empty" takes the property away', async () => {
    const ids = await openBoard()
    await settle(() => cardsIn('o:todo').length === 1, 'cards')
    await drag(await shown('A'), col('empty'))
    await settle(() => patches().length === 1, 'the request')
    expect(JSON.parse(patches()[0].body!)).toEqual({ baseRev: ids.A.rev, unset: ['status'] })
    expect(m!.server.peek(ids.A.id)!.meta.status).toBeUndefined()
    await settle(() => cardsIn('empty').length === 2, 'the card in the empty column')
  })

  it('from "empty" to an option sets it, and onto its own column does nothing', async () => {
    const ids = await openBoard()
    await settle(() => cardsIn('empty').length === 1, 'cards')
    await drag(await shown('C'), col('o:doing'))
    expect(patches()).toHaveLength(0)
    await drag(await shown('D'), col('o:done'))
    await settle(() => patches().length === 1, 'the request')
    expect(JSON.parse(patches()[0].body!)).toEqual({ baseRev: ids.D.rev, set: { status: 'done' } })
  })

  it('does nothing when the drag is not dropped on a column', async () => {
    await openBoard()
    await settle(() => cardsIn('o:todo').length === 1, 'cards')
    await drag(await shown('B'), col('o:doing'), { drop: false })
    expect(patches()).toHaveLength(0)
  })

  it('when the page was changed elsewhere says so, refreshes the card, and does not move it', async () => {
    const ids = await openBoard()
    await settle(() => cardsIn('o:todo').length === 1, 'cards')
    // Someone else changed B after the board was read.
    sender(m!.server)('PATCH', `/pages/${ids.B.id}/props`, { baseRev: ids.B.rev, set: { points: 99 } })
    await drag(await shown('B'), col('o:done'))
    await settle(() => document.body.textContent!.includes(tb.board.conflict('B')), 'the message')
    expect(m!.server.peek(ids.B.id)!.meta.status).toBe('todo')
    // The card shows the page as it is now (the new points), in the column it was in.
    await settle(() => card('B').querySelector('[data-field="points"]')?.textContent === '点数99', 'the refreshed card')
    expect(cardsIn('o:todo')).toEqual(['B'])
    // With the new rev the same move goes through.
    await drag(await shown('B'), col('o:done'))
    await settle(() => m!.server.peek(ids.B.id)!.meta.status === 'done', 'the second try')
    await settle(() => cardsIn('o:done').includes('B'), 'moved')
  })

  it('of two cards in a row frees both when the answers come, and tells of the one that failed', async () => {
    const ids = await openBoard(undefined, '/s/default/f/Work', { delay: (method) => (method === 'PATCH' ? 300 : 0) })
    await settle(() => cardsIn('o:todo').length === 1 && cardsIn('o:doing').length === 1, 'cards')
    // B is changed elsewhere, so its move is refused; C is dragged before B has been answered.
    sender(m!.server)('PATCH', `/pages/${ids.B.id}/props`, { baseRev: ids.B.rev, set: { points: 99 } })
    await drag(await shown('B'), col('o:done'))
    await drag(await shown('C'), col('o:done'))
    expect((await shown('B')).getAttribute('aria-busy')).toBe('true')
    await settle(() => patches().length === 2, 'both requests')
    await settle(() => document.body.textContent!.includes(tb.board.conflict('B')), 'the message for B')
    await settle(() => m!.server.peek(ids.C.id)!.meta.status === 'done', 'C moved')
    await settle(
      () => [...document.querySelectorAll('[data-card]')].every((c) => c.getAttribute('aria-busy') !== 'true'),
      'no card left busy',
    )
    expect((await shown('B')).getAttribute('draggable')).toBe('true')
    expect(m!.server.peek(ids.B.id)!.meta.status).toBe('todo')
  })

  it('is not offered for a card whose value the listing holds only in part', async () => {
    await openBoard((send) =>
      send('POST', '/pages', { folder: 'Work', meta: { title: 'LONG', status: 'x'.repeat(400) }, body: '' }),
    )
    await settle(() => cardsIn('other').length === 1, 'the other column')
    const long = await shown('LONG')
    expect(long.getAttribute('draggable')).toBe('false')
    expect(long.getAttribute('data-partial')).toBe('true')
    expect(long.getAttribute('title')).toBe(tb.board.partial)
    await drag(long, col('o:todo'))
    expect(patches()).toHaveLength(0)
  })

  it('is not offered for a card whose value is a list', async () => {
    await openBoard((send) =>
      send('POST', '/pages', { folder: 'Work', meta: { title: 'MANY', status: ['todo', 'doing'] }, body: '' }),
    )
    await settle(() => document.querySelector('[data-card]') !== null, 'cards')
    await settle(() => (card('MANY')?.getAttribute('draggable') ?? '') !== '', 'the card')
    expect((await shown('MANY')).getAttribute('draggable')).toBe('false')
  })

  it('cannot be done by someone who may only read: nothing can be dragged and nothing is sent', async () => {
    m = await mount(listShell, {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        const { space, owner } = shareSpace(server, 'viewer')
        const send = (method: string, path: string, body?: unknown) =>
          owner.request(method, `/api/spaces/${space}${path}`, body)
        send('PUT', '/types/task', TASK)
        send('POST', '/folders', { path: 'Work' })
        send('PUT', '/folders/type', { folder: 'Work', type: 'task' })
        send('POST', '/pages', { folder: 'Work', meta: { title: 'A', status: 'done' }, body: '' })
        const cur = send('GET', '/views?folder=Work').body as { rev: string }
        send('PUT', '/views', { folder: 'Work', baseRev: cur.rev, views: [BOARD] })
        return [`/s/${space}/f/Work`]
      },
    })
    await settle(() => cardsIn('o:done').length === 1, 'the card')
    expect((await shown('A')).getAttribute('draggable')).toBe('false')
    await drag(await shown('A'), col('o:todo'))
    expect(m.requests.filter((r) => r.method !== 'GET' && r.url !== '/api/login')).toEqual([])
    expect(cardsIn('o:done')).toEqual(['A'])
  })
})

describe('moving a card without a mouse', () => {
  const moveButton = (title: string) =>
    card(title).querySelector<HTMLButtonElement>(`button[aria-label="${tb.board.moveTo(title)}"]`)
  const menuItems = () => [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')]
  const openMenu = async (title: string) => {
    await click(moveButton(title))
    await settle(() => menuItems().length > 0, 'the menu')
    return menuItems()
  }

  it('reaches a card with the keyboard and moves it with "Move to…", which lists only the other columns that take a drop', async () => {
    const ids = await openBoard((send) =>
      send('POST', '/pages', { folder: 'Work', meta: { title: 'W', status: 'weird' }, body: '' }),
    )
    await settle(() => cardsIn('o:todo').length === 1 && cardsIn('other').length === 1, 'cards')
    expect((await shown('B')).tabIndex).toBe(0)
    expect(moveButton('B')).not.toBeNull()
    // Not its own column, and not "other values", which is no value.
    expect((await openMenu('B')).map((e) => e.textContent)).toEqual(['doing', 'done', tb.board.empty])
    await click(menuItems().find((e) => e.textContent === 'doing'))
    await settle(() => patches().length === 1, 'the request')
    expect(JSON.parse(patches()[0].body!)).toEqual({ baseRev: ids.B.rev, set: { status: 'doing' } })
    await settle(() => cardsIn('o:doing').includes('B'), 'the card in its new column')
    await settle(() => document.body.textContent!.includes(tb.board.moved('B', 'doing')), 'the move is told')
  })

  it('takes the value away with "empty", and moves a card out of "other values" to an option', async () => {
    const ids = await openBoard((send) =>
      send('POST', '/pages', { folder: 'Work', meta: { title: 'W', status: 'weird' }, body: '' }),
    )
    await settle(() => cardsIn('other').length === 1, 'cards')
    expect((await openMenu('W')).map((e) => e.textContent)).toEqual(['todo', 'doing', 'done', tb.board.empty])
    await click(menuItems().find((e) => e.textContent === 'todo'))
    await settle(() => cardsIn('o:todo').includes('W'), 'W moved')
    await openMenu('A')
    await click(menuItems().find((e) => e.textContent === tb.board.empty))
    await settle(() => m!.server.peek(ids.A.id)!.meta.status === undefined, 'A emptied')
  })

  it('on a board of values (a tag) offers only "empty"', async () => {
    await openBoard(
      (send) => send('POST', '/pages', { folder: 'Work', meta: { title: 'F', tags: ['栀子'] }, body: '' }),
      '/s/default/t/栀子',
    )
    await settle(() => cardsIn('o:done').length === 1 && cardsIn('empty').length === 1, 'cards')
    expect((await openMenu('A')).map((e) => e.textContent)).toEqual([tb.board.empty])
    // Nothing to go to from "empty": no button.
    expect(moveButton('F')).toBeNull()
  })

  it('is not offered for a card that cannot be moved, nor to someone who may only read', async () => {
    await openBoard((send) =>
      send('POST', '/pages', { folder: 'Work', meta: { title: 'MANY', status: ['todo', 'doing'] }, body: '' }),
    )
    await settle(() => document.querySelector('[data-card]') !== null && !!card('MANY'), 'cards')
    expect(moveButton('MANY')).toBeNull()
    await m!.unmount()
    m = undefined
    m = await mount(listShell, {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        const { space, owner } = shareSpace(server, 'viewer')
        const send = (method: string, path: string, body?: unknown) =>
          owner.request(method, `/api/spaces/${space}${path}`, body)
        send('PUT', '/types/task', TASK)
        send('POST', '/folders', { path: 'Work' })
        send('PUT', '/folders/type', { folder: 'Work', type: 'task' })
        send('POST', '/pages', { folder: 'Work', meta: { title: 'A', status: 'done' }, body: '' })
        const cur = send('GET', '/views?folder=Work').body as { rev: string }
        send('PUT', '/views', { folder: 'Work', baseRev: cur.rev, views: [BOARD] })
        return [`/s/${space}/f/Work`]
      },
    })
    await settle(() => cardsIn('o:done').length === 1, 'the card')
    expect((await shown('A')).tabIndex).toBe(0)
    expect(moveButton('A')).toBeNull()
  })

  it('moves the focus between cards with the arrow keys', async () => {
    await openBoard((send) =>
      send('POST', '/pages', { folder: 'Work', meta: { title: 'B2', status: 'todo' }, body: '' }),
    )
    await settle(() => cardsIn('o:todo').length === 2 && cardsIn('o:doing').length === 1, 'cards')
    const [first, second] = cardsIn('o:todo') as string[]
    card(first).focus()
    await press(card(first), 'ArrowDown')
    expect(document.activeElement).toBe(card(second))
    await press(card(second), 'ArrowRight')
    expect(document.activeElement).toBe(await shown('C'))
    await press(await shown('C'), 'ArrowLeft')
    expect(document.activeElement).toBe(card(first))
    await press(card(first), 'ArrowUp')
    expect(document.activeElement).toBe(card(first))
  })
})

describe('the board of a tag', () => {
  it('has a column for each value the pages have, most pages first, and "empty"; a card can be taken out of a value column but not dropped on one', async () => {
    const ids = await openBoard((send) => {
      send('POST', '/pages', { folder: 'Work', meta: { title: 'E', status: 'done', tags: ['栀子'] }, body: '' })
      send('POST', '/pages', { folder: 'Work', meta: { title: 'F', tags: ['栀子'] }, body: '' })
    }, '/s/default/t/栀子')
    await settle(() => document.querySelectorAll('[data-card]').length === 3, 'three cards')
    // A has "done" and so does E; F has nothing.
    expect(columnIds()).toEqual(['o:done', 'empty'])
    expect(['o:done', 'empty'].map(count)).toEqual(['2', '1'])
    expect(cardsIn('empty')).toEqual(['F'])
    // The values are text from the index, not what the page holds (a number, a cut value): nothing is written into them.
    await drag(await shown('F'), col('o:done'))
    expect(patches()).toHaveLength(0)
    expect(document.body.textContent).toContain(tb.board.valuesReadOnly)
    // Taking the value away is typed whatever the property is.
    await drag(await shown('E'), col('empty'))
    await settle(() => patches().length === 1, 'the request')
    expect(JSON.parse(patches()[0].body!)).toMatchObject({ unset: ['status'] })
    expect(m!.server.peek(ids.A.id)).toBeTruthy()
    await settle(() => cardsIn('empty').length === 2, 'moved')
    const groups = m!.requests.filter((r) => /\/pages\/groups\?/.test(r.url))
    expect(decodeURIComponent(groups[0].url)).toContain('tag=栀子')
  })
})

describe('a board with many columns', () => {
  it('of values shows the first fifty and one "other values", which asks for what the fifty are not', async () => {
    await openBoard((send) => {
      for (let i = 0; i < 60; i++)
        send('POST', '/pages', {
          folder: 'Work',
          meta: { title: `V${i}`, status: `s${String(i).padStart(2, '0')}`, tags: ['many'] },
          body: '',
        })
    }, '/s/default/t/many')
    await settle(() => document.querySelectorAll('[data-column]').length > 0, 'columns')
    expect(columnIds()).toHaveLength(52)
    expect(columnIds().at(-2)).toBe('other')
    expect(count('other')).toBe('10')
  })

  it('are asked for only once they come near the visible part: a column far to the side costs no request', async () => {
    const watchers: { cb: IntersectionObserverCallback; el: Element }[] = []
    class Watch {
      cb: IntersectionObserverCallback
      constructor(cb: IntersectionObserverCallback) {
        this.cb = cb
      }
      observe(el: Element) {
        watchers.push({ cb: this.cb, el })
      }
      disconnect() {}
      unobserve() {}
      takeRecords() {
        return []
      }
    }
    vi.stubGlobal('IntersectionObserver', Watch)
    try {
      await openBoard()
      const near = (id: string) =>
        act(async () => {
          const w = watchers.find((x) => x.el.getAttribute('data-column') === id)!
          w.cb([{ isIntersecting: true, target: w.el } as IntersectionObserverEntry], {} as IntersectionObserver)
        })
      // The counts of every column are known, and no cards are asked for.
      await settle(() => count('o:todo') === '1', 'the counts')
      expect(listRequests().filter((q) => q.get('props') === '1')).toHaveLength(0)
      expect(cardsIn('o:todo')).toEqual([])
      await near('o:doing')
      await settle(() => cardsIn('o:doing').length === 1, 'the near column')
      expect(listRequests().filter((q) => q.get('props') === '1')).toHaveLength(1)
      expect(cardsIn('o:todo')).toEqual([])
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('refreshes only the columns a moved card leaves and joins', async () => {
    await openBoard()
    await settle(
      () => cardsIn('o:todo').length === 1 && cardsIn('o:done').length === 1 && cardsIn('o:doing').length === 1,
      'cards',
    )
    const before = listRequests().filter((q) => q.get('props') === '1').length
    await drag(await shown('B'), col('o:done'))
    await settle(() => cardsIn('o:done').length === 2, 'moved')
    // todo (left) and done (joined) are asked for again; doing and empty are not.
    const again = listRequests()
      .filter((q) => q.get('props') === '1')
      .slice(before)
    const ops = again
      .map((q) =>
        filterOf(q)
          .map((f) => `${f.op}:${(f.values ?? []).join('|')}`)
          .join(),
      )
      .sort()
    expect(ops).toEqual(['in:done', 'in:todo'])
  })
})

describe('a view that has no field to group by', () => {
  it('says so instead of showing a board', async () => {
    m = await mount(listShell, {
      entries: (server) => {
        const send = sender(server)
        send('POST', '/folders', { path: 'Plain' })
        send('POST', '/pages', { folder: 'Plain', meta: { title: 'p' }, body: '' })
        putViews(send, { folder: 'Plain' }, [{ name: '看板', layout: 'board' }])
        return ['/s/default/f/Plain']
      },
    })
    await settle(() => byLabel(t.view.list) !== null, 'the listing')
    await settle(() => document.querySelector('[data-slot="view-bar"]') !== null, 'tabs')
    // Not a board without a type: the layout falls back to the list.
    await settle(() => byLabel(t.view.list)!.getAttribute('aria-pressed') === 'true', 'list')
    expect(document.querySelector('[data-slot="board-view"]')).toBeNull()
  })
})
