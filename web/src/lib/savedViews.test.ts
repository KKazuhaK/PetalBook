import { afterEach, describe, expect, it, vi } from 'vitest'
import type { PageType, SavedView } from '../api/types.ts'
import { boardColumns, cardFieldsOf, cardMovable, columnFilter, DEFAULT_STATE, freshName, groupFieldsOf, loadViewId, moveView, pickView, sameState, saveViewId, stateOfView, viewIdKey, withState } from './savedViews.ts'

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
