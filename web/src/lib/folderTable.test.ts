import { describe, expect, it } from 'vitest'
import type { PageSummary } from '@/api/types'
import { arrangeRows, filterCount, filterParams, fromHeaderSort, noFilters, rowKey, toHeaderSort } from './folderTable'

const page = (id: string, label: string, updated = ''): PageSummary => ({
  id,
  path: `${label}.md`,
  folder: '',
  label,
  untitled: false,
  updated,
  source: [],
})
const folder = (name: string, updated?: string) => ({
  path: name,
  name,
  direct: 1,
  total: 1,
  ...(updated ? { updated } : {}),
})
const keys = (rows: ReturnType<typeof arrangeRows>) => rows.map(rowKey)

describe('the rows of the folder table', () => {
  const folders = [folder('Zoo'), folder('Apple')]
  const pages = [page('1', 'Banana'), page('2', 'Cherry')]
  it('puts the subfolders first, by name, and the pages in the order the server gave', () => {
    expect(keys(arrangeRows(folders, pages, false, '-updated'))).toEqual(['f:Apple', 'f:Zoo', 'p:1', 'p:2'])
    expect(keys(arrangeRows(folders, pages, false, '-title'))).toEqual(['f:Zoo', 'f:Apple', 'p:1', 'p:2'])
  })
  it('mixes them in by name under a title sort, and after the pages under a date sort', () => {
    expect(keys(arrangeRows(folders, pages, true, 'title'))).toEqual(['f:Apple', 'p:1', 'p:2', 'f:Zoo'])
    const desc = [page('2', 'Cherry'), page('1', 'Banana')]
    expect(keys(arrangeRows(folders, desc, true, '-title'))).toEqual(['f:Zoo', 'p:2', 'p:1', 'f:Apple'])
    expect(keys(arrangeRows(folders, pages, true, '-updated'))).toEqual(['p:1', 'p:2', 'f:Apple', 'f:Zoo'])
  })
})

describe('folders under a sort by date', () => {
  const day = (d: number) => `2026-11-${String(d).padStart(2, '0')}T08:00:00Z`
  const folders = [folder('Old', day(1)), folder('New', day(20)), folder('Empty'), folder('Mid', day(10))]
  it('come first ordered by their updated, the newest first under the default order, empty ones last', () => {
    const pages = [page('1', 'P', day(15))]
    expect(keys(arrangeRows(folders, pages, false, ''))).toEqual(['f:New', 'f:Mid', 'f:Old', 'f:Empty', 'p:1'])
    expect(keys(arrangeRows(folders, pages, false, '-updated'))).toEqual(['f:New', 'f:Mid', 'f:Old', 'f:Empty', 'p:1'])
    expect(keys(arrangeRows(folders, pages, false, 'updated'))).toEqual(['f:Old', 'f:Mid', 'f:New', 'f:Empty', 'p:1'])
  })
  it('are mixed in among the pages by the same value when folders are not first', () => {
    const desc = [page('1', 'A', day(25)), page('2', 'B', day(15)), page('3', 'C', day(5))]
    expect(keys(arrangeRows(folders, desc, true, '-updated'))).toEqual([
      'p:1',
      'f:New',
      'p:2',
      'f:Mid',
      'p:3',
      'f:Old',
      'f:Empty',
    ])
    const asc = [...desc].reverse()
    expect(keys(arrangeRows(folders, asc, true, 'updated'))).toEqual([
      'f:Old',
      'p:3',
      'f:Mid',
      'p:2',
      'f:New',
      'p:1',
      'f:Empty',
    ])
  })
  it('keep the name order under other sorts, and title sorts by name', () => {
    expect(keys(arrangeRows(folders, [], false, 'created'))).toEqual(['f:Empty', 'f:Mid', 'f:New', 'f:Old'])
    expect(keys(arrangeRows(folders, [], false, 'title'))).toEqual(['f:Empty', 'f:Mid', 'f:New', 'f:Old'])
  })
})

describe('the sort of a header', () => {
  it('goes both ways between a header and the sort of the listing', () => {
    expect(toHeaderSort('')).toEqual({ key: 'updated', order: 'desc' })
    expect(toHeaderSort('title')).toEqual({ key: 'title', order: 'asc' })
    expect(toHeaderSort('-prop:source')).toEqual({ key: 'prop:source', order: 'desc' })
    expect(fromHeaderSort({ key: 'prop:source', order: 'desc' })).toBe('-prop:source')
    expect(fromHeaderSort({ key: 'created', order: 'asc' })).toBe('created')
  })
})

describe('the filters above the table', () => {
  it('ask the server for tags, sources, fields and date ranges, and count their chips', () => {
    expect(filterParams(noFilters())).toEqual({})
    const f = {
      tags: ['玉兰'],
      sources: ['书', '电影'],
      fields: { status: ['done'], other: [] as string[] },
      created: { from: '2026-01-01', to: '' },
      updated: { from: '', to: '2026-12-31' },
    }
    expect(filterParams(f)).toEqual({
      tag: ['玉兰'],
      filter: [
        { key: 'source', op: 'in', values: ['书', '电影'] },
        { key: 'status', op: 'in', values: ['done'] },
      ],
      createdFrom: '2026-01-01',
      updatedTo: '2026-12-31',
    })
    expect(filterCount(f)).toBe(1 + 2 + 1 + 1 + 1)
  })
})
