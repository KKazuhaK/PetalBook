import { describe, expect, it } from 'vitest'
import {
  actingOn,
  baseName,
  buildFolderIndex,
  buildFolderTree,
  checkFolderName,
  childFolders,
  collapseCrumbs,
  crumbsOf,
  isForbiddenDestination,
  isSameOrUnder,
  joinPath,
  pagesInside,
  parentOf,
  planDrop,
  type FileItem,
} from './folders'

const list = [
  { path: '', pages: 2 },
  { path: 'a', pages: 1 },
  { path: 'a/b', pages: 3 },
  { path: 'a/b/c', pages: 0 },
  { path: 'z', pages: 4 },
  { path: 'a/empty', pages: 0 },
]

describe('paths', () => {
  it('splits and joins', () => {
    expect(parentOf('a/b/c')).toBe('a/b')
    expect(parentOf('a')).toBe('')
    expect(baseName('a/b/c')).toBe('c')
    expect(baseName('a')).toBe('a')
    expect(joinPath('', 'x')).toBe('x')
    expect(joinPath('a/b', 'x')).toBe('a/b/x')
  })

  it('same-or-under respects whole segments', () => {
    expect(isSameOrUnder('a/b', 'a')).toBe(true)
    expect(isSameOrUnder('a', 'a')).toBe(true)
    expect(isSameOrUnder('ab', 'a')).toBe(false)
    expect(isSameOrUnder('a', 'a/b')).toBe(false)
    expect(isSameOrUnder('anything', '')).toBe(true)
  })
})

describe('children and recursive counts', () => {
  const index = buildFolderIndex(list)

  it('lists the direct subfolders by name', () => {
    expect(childFolders(index, '').map((f) => f.path)).toEqual(['a', 'z'])
    expect(childFolders(index, 'a').map((f) => f.path)).toEqual(['a/b', 'a/empty'])
    expect(childFolders(index, 'a/b/c')).toEqual([])
    expect(childFolders(index, 'nowhere')).toEqual([])
  })

  it('counts the pages in a folder and below it', () => {
    const total = (p: string) => index.byPath.get(p)?.total
    expect(total('')).toBe(10)
    expect(total('a')).toBe(4)
    expect(total('a/b')).toBe(3)
    expect(total('a/b/c')).toBe(0)
    expect(total('z')).toBe(4)
    expect(index.byPath.get('a')?.direct).toBe(1)
  })

  it('tolerates missing parents', () => {
    const partial = buildFolderIndex([{ path: 'x/y/z', pages: 2 }])
    expect(childFolders(partial, '').map((f) => f.path)).toEqual(['x'])
    expect(partial.byPath.get('x')?.total).toBe(2)
    expect(partial.byPath.get('x/y')?.direct).toBe(0)
  })

  it('sorts Chinese names and keeps the tree and the index in agreement', () => {
    const zh = [
      { path: '随笔', pages: 1 },
      { path: '归档', pages: 2 },
      { path: '随笔/2026', pages: 5 },
    ]
    const tree = buildFolderTree(zh)
    const idx = buildFolderIndex(zh)
    expect(tree.nodes.map((n) => n.path)).toEqual(childFolders(idx, '').map((f) => f.path))
    expect(tree.nodes.find((n) => n.path === '随笔')?.total).toBe(6)
  })
})

describe('checkFolderName', () => {
  it('takes one name and builds the path', () => {
    expect(checkFolderName('', ' 随笔 ')).toEqual({ name: '随笔', path: '随笔' })
    expect(checkFolderName('a/b', 'c')).toEqual({ name: 'c', path: 'a/b/c' })
  })
  it('refuses slashes, empty names and the server rules', () => {
    expect('error' in checkFolderName('', 'a/b')).toBe(true)
    expect('error' in checkFolderName('', 'a\\b')).toBe(true)
    expect('error' in checkFolderName('', '  ')).toBe(true)
    expect('error' in checkFolderName('', '.hidden')).toBe(true)
    expect('error' in checkFolderName('', 'notes.md')).toBe(true)
    expect('error' in checkFolderName('', 'assets')).toBe(true)
    // Only reserved at the top.
    expect('error' in checkFolderName('a', 'assets')).toBe(false)
  })
})

describe('breadcrumbs', () => {
  it('starts at the space', () => {
    expect(crumbsOf('', 'Notes')).toEqual([{ path: '', name: 'Notes' }])
    expect(crumbsOf('a/b', 'Notes').map((c) => c.path)).toEqual(['', 'a', 'a/b'])
  })

  const items = (n: number) => Array.from({ length: n }, (_, i) => i)

  it('leaves a short trail whole', () => {
    expect(collapseCrumbs(items(4), 1, 2)).toEqual({ start: [0, 1, 2, 3], hidden: [], end: [] })
  })
  it('folds the middle of a long one', () => {
    expect(collapseCrumbs(items(6), 1, 2)).toEqual({ start: [0], hidden: [1, 2, 3], end: [4, 5] })
    // Folding a single entry would save nothing.
    expect(collapseCrumbs(items(4), 1, 2).hidden).toEqual([])
    expect(collapseCrumbs(items(5), 1, 2).hidden).toEqual([1, 2])
  })
  it('on a phone keeps the last two, and folds even one', () => {
    expect(collapseCrumbs(items(2), 0, 2, 1)).toEqual({ start: [0, 1], hidden: [], end: [] })
    expect(collapseCrumbs(items(3), 0, 2, 1)).toEqual({ start: [], hidden: [0], end: [1, 2] })
    expect(collapseCrumbs(items(5), 0, 2, 1)).toEqual({ start: [], hidden: [0, 1, 2], end: [3, 4] })
  })
})

describe('where things can be dropped', () => {
  const index = buildFolderIndex(list)
  const page = (id: string, folder: string): FileItem => ({ kind: 'page', id, folder, label: id })
  const folder = (path: string): FileItem => ({ kind: 'folder', path })

  it('refuses a folder onto itself and onto anything inside it', () => {
    expect(planDrop([folder('a')], 'a')).toEqual({ ok: false, reason: 'self' })
    expect(planDrop([folder('a')], 'a/b')).toEqual({ ok: false, reason: 'self' })
    expect(planDrop([folder('a'), page('p', 'z')], 'a/b/c')).toEqual({ ok: false, reason: 'self' })
    // A name that merely starts the same is another folder.
    expect(planDrop([folder('a')], 'ab').ok).toBe(true)
  })

  it('accepts a folder elsewhere, and into the root', () => {
    expect(planDrop([folder('a/b')], '').ok).toBe(true)
    expect(planDrop([folder('a/b')], 'z').ok).toBe(true)
  })

  it('does nothing for what is already there', () => {
    expect(planDrop([page('p', 'a')], 'a')).toEqual({ ok: false, reason: 'nothing' })
    expect(planDrop([folder('a/b')], 'a')).toEqual({ ok: false, reason: 'nothing' })
    expect(planDrop([], 'a')).toEqual({ ok: false, reason: 'nothing' })
  })

  it('moves only what is not there yet', () => {
    const plan = planDrop([page('p', 'a'), page('q', 'z'), folder('a/b')], 'a', index)
    expect(plan).toEqual({ ok: true, moving: [page('q', 'z')], conflicts: [] })
  })

  it('holds back a folder that would meet one of its name, when the index is known', () => {
    const withTwin = buildFolderIndex([...list, { path: 'z/b', pages: 0 }])
    expect(planDrop([folder('a/b')], 'z', withTwin)).toEqual({ ok: false, reason: 'exists' })
    const mixed = planDrop([folder('a/b'), page('p', 'a')], 'z', withTwin)
    expect(mixed).toEqual({ ok: true, moving: [page('p', 'a')], conflicts: [folder('a/b')] })
    // Without the index only the structural rules apply: that is what lights a target up.
    expect(planDrop([folder('a/b')], 'z').ok).toBe(true)
  })

  it('knows which destinations a set of items can never go to', () => {
    expect(isForbiddenDestination([folder('a')], 'a/b')).toBe(true)
    expect(isForbiddenDestination([folder('a')], 'z')).toBe(false)
    expect(isForbiddenDestination([page('p', 'a')], 'a')).toBe(false)
  })
})

describe('acting on a selection', () => {
  const p = (id: string): FileItem => ({ kind: 'page', id, folder: '', label: id })
  it('takes the whole selection when the item is in it, else just the item', () => {
    const selected = [p('a'), p('b')]
    expect(actingOn(p('a'), selected)).toBe(selected)
    expect(actingOn(p('c'), selected)).toEqual([p('c')])
  })

  it('counts the pages under selected folders once', () => {
    const index = buildFolderIndex(list)
    expect(pagesInside([{ kind: 'folder', path: 'a' }], index)).toBe(4)
    expect(pagesInside([{ kind: 'folder', path: 'a' }, { kind: 'folder', path: 'a/b' }], index)).toBe(4)
    expect(pagesInside([{ kind: 'folder', path: 'a/b' }, { kind: 'folder', path: 'z' }, p('x')], index)).toBe(7)
    expect(pagesInside([p('x')], index)).toBe(0)
  })
})
