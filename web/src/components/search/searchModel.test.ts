import { describe, expect, it } from 'vitest'
import {
  buildSearchParams,
  dateRange,
  effectiveSort,
  emptyFilters,
  filtersToQuery,
  groupByTime,
  hasFilters,
  paramsFromQuery,
  presetRange,
  snippetOf,
  timeGroupOf,
  utcDay,
} from './searchModel'

// 2026-11-21 10:00 UTC.
const NOW = new Date(Date.UTC(2026, 10, 21, 10, 0, 0))

describe('date presets', () => {
  it('today is the UTC day, with no end', () => {
    expect(presetRange('today', NOW)).toEqual({ from: '2026-11-21' })
  })
  it('the past week reaches 7 days back, the past 30 days 30 days back, across month and year ends', () => {
    expect(presetRange('week', NOW)).toEqual({ from: '2026-11-14' })
    expect(presetRange('month', NOW)).toEqual({ from: '2026-10-22' })
    expect(presetRange('week', new Date(Date.UTC(2027, 0, 3, 1)))).toEqual({ from: '2026-12-27' })
    expect(presetRange('month', new Date(Date.UTC(2028, 2, 1, 23, 59)))).toEqual({ from: '2028-01-31' })
  })
  it('uses the UTC day even when it differs from the local one', () => {
    // 23:30 UTC on the 21st is already the 22nd in Shanghai; the server counts UTC.
    expect(utcDay(new Date(Date.UTC(2026, 10, 21, 23, 30)))).toBe('2026-11-21')
  })
  it('a custom range keeps what was given, either end optional', () => {
    expect(dateRange({ kind: 'custom', from: '2026-11-01', to: '2026-11-09' }, NOW)).toEqual({
      from: '2026-11-01',
      to: '2026-11-09',
    })
    expect(dateRange({ kind: 'custom', to: '2026-11-09' }, NOW)).toEqual({ to: '2026-11-09' })
    expect(dateRange({ kind: 'custom' }, NOW)).toEqual({})
    expect(dateRange(null, NOW)).toEqual({})
  })
})

describe('the request', () => {
  it('with no text and no filter it is the recent pages', () => {
    expect(buildSearchParams('', emptyFilters, 'relevance', NOW)).toEqual({ sort: '-updated' })
    expect(buildSearchParams('   ', emptyFilters, 'relevance', NOW)).toEqual({ sort: '-updated' })
    expect(hasFilters(emptyFilters)).toBe(false)
  })
  it('with text it is by relevance (no sort) unless time was asked for', () => {
    expect(buildSearchParams(' 慢 ', emptyFilters, 'relevance', NOW)).toEqual({ q: '慢' })
    expect(buildSearchParams('慢', emptyFilters, 'updated', NOW)).toEqual({ q: '慢', sort: '-updated' })
    expect(effectiveSort('', 'relevance')).toBe('updated')
    expect(effectiveSort('x', 'relevance')).toBe('relevance')
  })
  it('title only is `in=title`, and only with text', () => {
    expect(buildSearchParams('慢', { ...emptyFilters, titleOnly: true }, 'relevance', NOW)).toEqual({
      q: '慢',
      in: 'title',
    })
    expect(buildSearchParams('', { ...emptyFilters, titleOnly: true }, 'relevance', NOW).in).toBeUndefined()
  })
  it('a folder includes its subfolders, including the top level', () => {
    expect(buildSearchParams('', { ...emptyFilters, folder: '随笔' }, 'relevance', NOW)).toMatchObject({
      folder: '随笔',
      recursive: true,
    })
    expect(buildSearchParams('', { ...emptyFilters, folder: '' }, 'relevance', NOW)).toMatchObject({
      folder: '',
      recursive: true,
    })
  })
  it('tags are all required, ranges become the four date parameters', () => {
    const p = buildSearchParams(
      '',
      {
        ...emptyFilters,
        tags: ['a', 'b'],
        updated: { kind: 'preset', preset: 'week' },
        created: { kind: 'custom', from: '2026-01-01', to: '2026-01-31' },
      },
      'relevance',
      NOW,
    )
    expect(p).toEqual({
      tag: ['a', 'b'],
      updatedFrom: '2026-11-14',
      createdFrom: '2026-01-01',
      createdTo: '2026-01-31',
      sort: '-updated',
    })
  })
})

describe('the results page address', () => {
  it('carries the text and the filters, and reads them back', () => {
    const filters = {
      ...emptyFilters,
      titleOnly: true,
      folder: '随笔',
      tags: ['a', 'b'],
      created: { kind: 'custom' as const, from: '2026-01-01' },
    }
    const query = filtersToQuery('慢', filters, NOW)
    expect(query.toString()).toBe('q=%E6%85%A2&in=title&folder=%E9%9A%8F%E7%AC%94&tag=a&tag=b&createdFrom=2026-01-01')
    expect(paramsFromQuery(query)).toEqual({
      text: '慢',
      extra: { in: 'title', folder: '随笔', recursive: true, tag: ['a', 'b'], createdFrom: '2026-01-01' },
    })
  })
  it('ignores what it does not understand', () => {
    expect(paramsFromQuery(new URLSearchParams('q=x&in=body&updatedFrom=yesterday&tag='))).toEqual({
      text: 'x',
      extra: {},
    })
    expect(paramsFromQuery(new URLSearchParams(''))).toEqual({ text: '', extra: {} })
  })
})

describe('grouping by time', () => {
  // Local calendar days: built from local parts so the test holds in any time zone.
  const now = new Date(2026, 10, 21, 15, 0, 0)
  const at = (daysAgo: number, hour = 9) => new Date(2026, 10, 21 - daysAgo, hour, 0, 0).toISOString()
  it('puts a time in today, the past week, the past 30 days or older', () => {
    expect(timeGroupOf(at(0, 0), now)).toBe('today')
    expect(timeGroupOf(at(0, 14), now)).toBe('today')
    expect(timeGroupOf(at(1, 23), now)).toBe('week')
    expect(timeGroupOf(at(7, 12), now)).toBe('week')
    expect(timeGroupOf(at(8, 12), now)).toBe('month')
    expect(timeGroupOf(at(30, 12), now)).toBe('month')
    expect(timeGroupOf(at(31, 12), now)).toBe('older')
    expect(timeGroupOf('not a time', now)).toBe('older')
  })
  it('keeps the order, drops empty groups and always lists them from the newest', () => {
    const items = [
      { id: 'a', updated: at(0) },
      { id: 'b', updated: at(2) },
      { id: 'c', updated: at(3) },
      { id: 'd', updated: at(90) },
    ]
    const groups = groupByTime(items, now)
    expect(groups.map((g) => g.group)).toEqual(['today', 'week', 'older'])
    expect(groups[1].items.map((i) => i.id)).toEqual(['b', 'c'])
    expect(groupByTime([], now)).toEqual([])
  })
})

describe('snippets', () => {
  it('marks every match of every word, ignoring case', () => {
    const s = snippetOf('Roses and ROSE water', 'rose')
    expect(s.filter((x) => x.hit).map((x) => x.text)).toEqual(['Rose', 'ROSE'])
    expect(s.map((x) => x.text).join('')).toBe('Roses and ROSE water')
  })
  it('opens with the start when nothing matches or there is no query, and joins lines with spaces', () => {
    expect(snippetOf('第一行\n第二行', '')).toEqual([{ text: '第一行 第二行', hit: false }])
    expect(snippetOf('第一行\n第二行', '没有')).toEqual([{ text: '第一行 第二行', hit: false }])
    expect(snippetOf(undefined, 'x')).toEqual([])
    expect(snippetOf('', 'x')).toEqual([])
  })
  it('cuts around a late match with an ellipsis at either end', () => {
    const text = `${'前'.repeat(100)}目标${'后'.repeat(100)}`
    const s = snippetOf(text, '目标', 40, 10)
    const joined = s.map((x) => x.text).join('')
    expect(joined.startsWith('…')).toBe(true)
    expect(joined.endsWith('…')).toBe(true)
    expect(s.find((x) => x.hit)?.text).toBe('目标')
    expect(Array.from(joined).length).toBeLessThanOrEqual(42)
  })
  it('treats regular-expression characters in the query as text', () => {
    expect(snippetOf('a (b) c', '(b)').find((x) => x.hit)?.text).toBe('(b)')
    expect(snippetOf('a.b', '.').filter((x) => x.hit)).toHaveLength(1)
  })
})
