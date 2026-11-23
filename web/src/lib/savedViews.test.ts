import { afterEach, describe, expect, it, vi } from 'vitest'
import type { PageType, SavedView } from '../api/types.ts'
import { boardColumns, cardFieldsOf, cardMovable, columnDroppable, columnFilter, MAX_COLUMNS, DEFAULT_STATE, freshName, groupFieldsOf, loadViewId, moveView, pickView, sameState, followSnapshot, snapshotOf, viewAtMark, saveViewId, stateOfView, viewIdKey, withState } from './savedViews.ts'

const view = (id: string, extra: Partial<SavedView> = {}): SavedView => ({ id, name: id, layout: 'list', sort: '', group: '', filter: [], hidden: [], ...extra })

describe('the state of a view', () => {
  it('is what the view says apart from its name, and writes back over a view without losing what the app does not know', () => {
    const v = view('a', { layout: 'board', group: 'status', sort: '-prop:written', filter: [{ key: 'series', op: 'in', values: ['冬季'] }], hidden: ['note'], color: 'teal', cardFields: ['due'] })
    const s = stateOfView(v)
    expect(s).toEqual({ layout: 'board', group: 'status', sort: '-prop:written', filter: [{ key: 'series', op: 'in', values: ['冬季'] }], hidden: ['note'], cardFields: ['due'] })
    const written = withState(v, { ...s, layout: 'table', cardFields: undefined })
    expect(written).toMatchObject({ id: 'a', name: 'a', layout: 'table', color: 'teal' })
    // The server cannot forget a key it does not know; the default is said as null.
    expect(written.cardFields).toBeNull()
    expect(stateOfView({ ...v, ...written } as SavedView).cardFields).toBeUndefined()
  })
  it('reads a layout the app does not know as the list', () => {
    expect(stateOfView(view('a', { layout: 'gantt' as never })).layout).toBe('list')
  })
  it('differs when anything the tab shows differs; newest first is the same as no sort', () => {
    const a = stateOfView(view('a'))
    expect(sameState(a, { ...a })).toBe(true)
    expect(sameState(a, { ...a, sort: '-updated' })).toBe(true)
    expect(sameState(a, { ...a, sort: 'title' })).toBe(false)
    expect(sameState(a, { ...a, layout: 'cards' })).toBe(false)
    expect(sameState(a, { ...a, filter: [{ key: 'k', op: 'empty' }] })).toBe(false)
    expect(sameState(a, { ...a, hidden: ['x'] })).toBe(false)
    expect(sameState(a, { ...a, group: 'g' })).toBe(false)
    expect(sameState(a, { ...a, cardFields: [] })).toBe(false)
    expect(sameState(DEFAULT_STATE, a)).toBe(true)
  })
})

describe('the tabs', () => {
  it('names a new view with a number when the name is taken', () => {
    expect(freshName([], '视图')).toBe('视图')
    expect(freshName(['视图'], '视图')).toBe('视图 2')
    expect(freshName(['视图', '视图 2'], '视图')).toBe('视图 3')
  })
  it('moves a view to a place, and keeps the list when nothing moves or the id is not there', () => {
    const list = ['a', 'b', 'c', 'd'].map((id) => ({ id }))
    expect(moveView(list, 'a', 2).map((v) => v.id)).toEqual(['b', 'c', 'a', 'd'])
    expect(moveView(list, 'd', 0).map((v) => v.id)).toEqual(['d', 'a', 'b', 'c'])
    expect(moveView(list, 'b', -5).map((v) => v.id)).toEqual(['b', 'a', 'c', 'd'])
    expect(moveView(list, 'b', 99).map((v) => v.id)).toEqual(['a', 'c', 'd', 'b'])
    expect(moveView(list, 'b', 1).map((v) => v.id)).toEqual(['a', 'b', 'c', 'd'])
    expect(moveView(list, 'zz', 1).map((v) => v.id)).toEqual(['a', 'b', 'c', 'd'])
  })
  it('shows the remembered view if it is still there, else the first', () => {
    const views = [view('a'), view('b')]
    expect(pickView(views, 'b')?.id).toBe('b')
    expect(pickView(views, 'gone')?.id).toBe('a')
    expect(pickView(views, '')?.id).toBe('a')
    expect(pickView([], 'a')).toBeNull()
  })
  it('finds a remembered "@n" by its place once the views have real ids, and no other id', () => {
    const views = [view('a'), view('b'), view('c')]
    expect(pickView(views, '@1')?.id).toBe('b')
    expect(pickView(views, '@7')?.id).toBe('a')
    expect(pickView(views, 'x@1')?.id).toBe('a')
    expect(viewAtMark(views, '@2')?.id).toBe('c')
    expect(viewAtMark(views, 'b')).toBeNull()
    expect(viewAtMark(views, '@3')).toBeNull()
  })
})

describe('finding a hand-written view again by what it was', () => {
  const v = (id: string, name: string, layout = 'list') => ({ ...view(id), name, layout }) as ReturnType<typeof view>
  it('is not gone while its id still has its name and settings', () => {
    const snap = snapshotOf(v('@1', '乙'))
    expect(followSnapshot([v('@0', '甲'), v('@1', '乙')], snap)).toEqual({ gone: false, to: null })
  })
  it('finds the one view that has its name and settings under another id, wherever it is', () => {
    const snap = snapshotOf(v('@1', '乙'))
    expect(followSnapshot([v('x', '乙'), v('y', '丙')], snap).to?.id).toBe('x')
    expect(followSnapshot([v('y', '丙'), v('z', '甲'), v('x', '乙')], snap).to?.id).toBe('x')
  })
  it('is lost when the name or the settings differ, or when two views have them', () => {
    const snap = snapshotOf(v('@1', '乙'))
    expect(followSnapshot([v('x', '乙乙'), v('y', '丙')], snap)).toEqual({ gone: true, to: null })
    expect(followSnapshot([v('x', '乙', 'cards')], snap)).toEqual({ gone: true, to: null })
    expect(followSnapshot([v('x', '乙'), v('y', '乙')], snap)).toEqual({ gone: true, to: null })
    expect(followSnapshot([v('@1', '丙'), v('y', '甲')], snap)).toEqual({ gone: true, to: null })
  })
})

describe('which view a folder remembers', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    localStorage.clear()
  })
  it('is kept per space, kind and name, and forgotten when empty', () => {
    expect(viewIdKey('s1', 'folder', '文案')).toBe('pb.viewid.s1.folder.文案')
    expect(viewIdKey('s1', 'folder', '')).toBe('pb.viewid.s1.folder')
    const key = viewIdKey('s1', 'tag', '栀子')
    expect(loadViewId(key)).toBe('')
    saveViewId(key, 'k3f9a1')
    expect(loadViewId(key)).toBe('k3f9a1')
    expect(loadViewId(viewIdKey('s1', 'tag', '玉兰'))).toBe('')
    saveViewId(key, '')
    expect(loadViewId(key)).toBe('')
  })
  it('survives storage that throws', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    expect(loadViewId('k')).toBe('')
    expect(() => saveViewId('k', 'a')).not.toThrow()
    expect(() => saveViewId('k', '')).not.toThrow()
  })
})

const TYPE: PageType = {
  name: 'task',
  title: '任务',
  rev: 'r',
  fields: [
    { key: 'status', title: '状态', kind: 'select', options: ['todo', 'doing', 'done'] },
    { key: 'points', title: '点数', kind: 'number' },
    { key: 'labels', title: '标签组', kind: 'multiselect', options: ['a'] },
    { key: 'due', title: '截止', kind: 'date' },
    { key: 'note', title: '备注', kind: 'text' },
  ],
}

describe('the columns of a board', () => {
  it('are the options in order, "other values" only when some page has one, and "empty"', () => {
    const options = ['todo', 'doing', 'done']
    const cols = boardColumns(options, [{ value: 'done', count: 3 }, { value: 'todo', count: 1 }], 2)
    expect(cols.map((c) => [c.id, c.count])).toEqual([['o:todo', 1], ['o:doing', 0], ['o:done', 3], ['empty', 2]])
    const withOther = boardColumns(options, [{ value: 'dropped', count: 2 }, { value: 'old', count: 1 }, { value: 'todo', count: 1 }], 0)
    expect(withOther.map((c) => [c.kind, c.count])).toEqual([['option', 1], ['option', 0], ['option', 0], ['other', 3], ['empty', 0]])
  })
  it('have no counts until the groups are known, and then "other values" is shown rather than hidden', () => {
    const cols = boardColumns(['a'], undefined, undefined)
    expect(cols.map((c) => [c.kind, c.count])).toEqual([['option', undefined], ['other', undefined], ['empty', undefined]])
  })
  it('are the values the pages have, most first, when there are no options (a tag board)', () => {
    const cols = boardColumns(null, [{ value: '草稿', count: 5 }, { value: '定稿', count: 2 }], 1)
    expect(cols.map((c) => [c.id, c.count])).toEqual([['o:草稿', 5], ['o:定稿', 2], ['empty', 1]])
    expect(boardColumns(null, [], 0).map((c) => c.kind)).toEqual(['empty'])
  })
  it('made of values are the first MAX_COLUMNS of them and one "other values" for the rest, which leaves out the ones shown', () => {
    const groups = Array.from({ length: 80 }, (_, i) => ({ value: `v${String(i).padStart(2, '0')}`, count: 80 - i }))
    const cols = boardColumns(null, groups, 3)
    expect(cols).toHaveLength(MAX_COLUMNS + 2)
    const other = cols[MAX_COLUMNS]
    expect(other.kind).toBe('other')
    expect(other.count).toBe(groups.slice(MAX_COLUMNS).reduce((n, g) => n + g.count, 0))
    const f = columnFilter(other, 'k', null)
    expect(f.op).toBe('notIn')
    expect(f.values).toEqual(groups.slice(0, MAX_COLUMNS).map((g) => g.value))
    // The index cut the list of values short: there are more than are known.
    expect(boardColumns(null, groups.slice(0, 3), 0, true).map((c) => c.kind)).toEqual(['option', 'option', 'option', 'other', 'empty'])
    expect(boardColumns(null, groups.slice(0, 3), 0).map((c) => c.kind)).toEqual(['option', 'option', 'option', 'empty'])
  })
  it('of a select with more than MAX_COLUMNS options are those that have pages', () => {
    const options = Array.from({ length: 120 }, (_, i) => `o${i}`)
    expect(boardColumns(options, [{ value: 'o7', count: 2 }, { value: 'o99', count: 1 }], 0).filter((c) => c.kind === 'option').map((c) => c.value)).toEqual(['o7', 'o99'])
    // The "other values" column leaves out every option, up to what the server takes (500).
    const other = boardColumns(options, [{ value: 'x', count: 1 }], 0).find((c) => c.kind === 'other')!
    expect(columnFilter(other, 'k', options).values).toHaveLength(120)
  })
  it('"other values" of a select with no options is every page that has a value', () => {
    const other = boardColumns([], [{ value: 'x', count: 1 }], 0).find((c) => c.kind === 'other')!
    expect(columnFilter(other, 'k', [])).toEqual({ key: 'k', op: 'notEmpty' })
  })
  it('take a dropped card only when the value is an option of a select, or when it is taken away', () => {
    const cols = boardColumns(['a'], [{ value: 'z', count: 1 }], 0)
    expect(cols.map((c) => columnDroppable(c, true))).toEqual([true, false, true])
    const byValue = boardColumns(null, [{ value: '2', count: 1 }], 0)
    expect(byValue.map((c) => columnDroppable(c, false))).toEqual([false, true])
  })
  it('each pick their pages by one filter: in, none of the options, no value', () => {
    const options = ['todo', 'doing']
    const [todo, , other, empty] = boardColumns(options, [{ value: 'x', count: 1 }], 0)
    expect(columnFilter(todo, 'status', options)).toEqual({ key: 'status', op: 'in', values: ['todo'] })
    expect(columnFilter(other, 'status', options)).toEqual({ key: 'status', op: 'notIn', values: ['todo', 'doing'] })
    expect(columnFilter(empty, 'status', options)).toEqual({ key: 'status', op: 'empty' })
  })
})

describe('what a board needs of a type', () => {
  it('groups by single choices only', () => {
    expect(groupFieldsOf(TYPE).map((f) => f.key)).toEqual(['status'])
    expect(groupFieldsOf(undefined)).toEqual([])
  })
  it('shows the chosen fields of a card (at most three, those the type has), else the first three that are not the grouping one', () => {
    expect(cardFieldsOf(TYPE, undefined, 'status').map((f) => f.key)).toEqual(['points', 'labels', 'due'])
    expect(cardFieldsOf(TYPE, ['note', 'gone', 'due'], 'status').map((f) => f.key)).toEqual(['note', 'due'])
    expect(cardFieldsOf(TYPE, ['points', 'labels', 'due', 'note'], 'status')).toHaveLength(3)
    expect(cardFieldsOf(TYPE, [], 'status')).toEqual([])
    expect(cardFieldsOf(undefined, undefined, 'status')).toEqual([])
  })
  it('lets a card move when its value is whole and plain: not when the key is partial, or a list', () => {
    expect(cardMovable({ status: 'todo' }, undefined, 'status')).toBe(true)
    expect(cardMovable({}, undefined, 'status')).toBe(true)
    expect(cardMovable(undefined, undefined, 'status')).toBe(true)
    expect(cardMovable({ status: 'todo' }, ['status'], 'status')).toBe(false)
    expect(cardMovable({ status: 'todo' }, ['other'], 'status')).toBe(true)
    expect(cardMovable({ status: ['a', 'b'] }, undefined, 'status')).toBe(false)
  })
})
