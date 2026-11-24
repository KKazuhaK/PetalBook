// What the search dialog asks and how it lays out the answer. All pure: the
// dates, the parameters of the request and the grouping of results are checked
// without rendering anything.

import type { ListParams, PageSummary } from '../../api/types.ts'

export type DatePreset = 'today' | 'week' | 'month'
export type DateFilter = { kind: 'preset'; preset: DatePreset } | { kind: 'custom'; from?: string; to?: string }

export interface SearchFilters {
  titleOnly: boolean
  // One folder; its subfolders are included.
  folder: string | null
  tags: string[]
  updated: DateFilter | null
  created: DateFilter | null
}

export const emptyFilters: SearchFilters = { titleOnly: false, folder: null, tags: [], updated: null, created: null }

export const hasFilters = (f: SearchFilters) =>
  f.titleOnly || f.folder !== null || f.tags.length > 0 || f.updated !== null || f.created !== null

const DAY = 86_400_000

// The server counts days in UTC (docs/api.md), so the presets do too: "today"
// is the UTC day, which can be yesterday's date on a clock far from Greenwich
// for a few hours. The alternative, local days sent as UTC ones, would miss
// pages written in those hours.
export const utcDay = (d: Date) => d.toISOString().slice(0, 10)

// `from` and/or `to`, as days. "Today" has no end (nothing is written in the
// future); the week and the month reach back that many days from today.
export function presetRange(preset: DatePreset, now: Date): { from: string } {
  if (preset === 'today') return { from: utcDay(now) }
  return { from: utcDay(new Date(now.getTime() - (preset === 'week' ? 7 : 30) * DAY)) }
}

export function dateRange(filter: DateFilter | null, now: Date): { from?: string; to?: string } {
  if (!filter) return {}
  if (filter.kind === 'preset') return presetRange(filter.preset, now)
  return { ...(filter.from ? { from: filter.from } : {}), ...(filter.to ? { to: filter.to } : {}) }
}

export type SearchSort = 'relevance' | 'updated'

// Relevance only means something with text to match; without it the list is by
// time, newest first.
export const effectiveSort = (text: string, sort: SearchSort): SearchSort => (text.trim() === '' ? 'updated' : sort)

// The request for what is typed and chosen. Without text and without a filter
// it is just the recent pages. `sort` is left out for relevance, which is what
// the server does with text and no sort.
export function buildSearchParams(
  text: string,
  filters: SearchFilters,
  sort: SearchSort,
  now: Date,
): Omit<ListParams, 'cursor' | 'limit'> {
  const q = text.trim()
  const params: Omit<ListParams, 'cursor' | 'limit'> = {}
  if (q) {
    params.q = q
    if (filters.titleOnly) params.in = 'title'
  }
  if (filters.folder !== null) {
    params.folder = filters.folder
    params.recursive = true
  }
  if (filters.tags.length) params.tag = [...filters.tags]
  const updated = dateRange(filters.updated, now)
  if (updated.from) params.updatedFrom = updated.from
  if (updated.to) params.updatedTo = updated.to
  const created = dateRange(filters.created, now)
  if (created.from) params.createdFrom = created.from
  if (created.to) params.createdTo = created.to
  if (effectiveSort(q, sort) === 'updated') params.sort = '-updated'
  return params
}

// --- the address of the full results page ---------------------------------------
// The page lists more than the dialog does and keeps the same filters, which
// travel in its query.

export function filtersToQuery(text: string, filters: SearchFilters, now: Date): URLSearchParams {
  const p = buildSearchParams(text, filters, 'relevance', now)
  const out = new URLSearchParams()
  if (p.q) out.set('q', p.q)
  if (p.in) out.set('in', p.in)
  if (p.folder !== undefined) out.set('folder', p.folder)
  for (const tag of p.tag ?? []) out.append('tag', tag)
  for (const k of ['updatedFrom', 'updatedTo', 'createdFrom', 'createdTo'] as const) if (p[k]) out.set(k, p[k])
  return out
}

const DAY_RE = /^\d{4}-\d{2}-\d{2}$/

// What the results page lists, from its address; anything not understood is left out.
export function paramsFromQuery(q: URLSearchParams): {
  text: string
  extra: Omit<ListParams, 'cursor' | 'limit' | 'q' | 'sort'>
} {
  const extra: Omit<ListParams, 'cursor' | 'limit' | 'q' | 'sort'> = {}
  if (q.get('in') === 'title') extra.in = 'title'
  const folder = q.get('folder')
  if (folder !== null) {
    extra.folder = folder
    extra.recursive = true
  }
  const tags = q.getAll('tag').filter(Boolean)
  if (tags.length) extra.tag = tags
  for (const k of ['updatedFrom', 'updatedTo', 'createdFrom', 'createdTo'] as const) {
    const v = q.get(k)
    if (v && DAY_RE.test(v)) extra[k] = v
  }
  return { text: q.get('q') ?? '', extra }
}

// --- grouping by time -------------------------------------------------------------

export type TimeGroup = 'today' | 'week' | 'month' | 'older'
export const TIME_GROUPS: readonly TimeGroup[] = ['today', 'week', 'month', 'older']

const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()

// Which group a time falls in, by the reader's own calendar days: today, the
// 7 days before today, the 30 before that window's end, anything older.
export function timeGroupOf(iso: string, now: Date): TimeGroup {
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return 'older'
  const today = startOfDay(now)
  if (t >= today) return 'today'
  if (t >= today - 7 * DAY) return 'week'
  if (t >= today - 30 * DAY) return 'month'
  return 'older'
}

// Keeps the order it is given (newest first from the server) and drops empty groups.
export function groupByTime<T extends { updated: string }>(
  items: readonly T[],
  now: Date,
): { group: TimeGroup; items: T[] }[] {
  const buckets = new Map<TimeGroup, T[]>()
  for (const item of items) {
    const g = timeGroupOf(item.updated, now)
    const list = buckets.get(g)
    if (list) list.push(item)
    else buckets.set(g, [item])
  }
  return TIME_GROUPS.filter((g) => buckets.has(g)).map((g) => ({ group: g, items: buckets.get(g)! }))
}

// --- snippets -------------------------------------------------------------------------

export interface Segment {
  text: string
  hit: boolean
}

// A short run of the excerpt around the first match, with every match marked.
// The excerpt is the first 400 characters of the page, so a match further down
// is not in it; the snippet then just opens the page's text. Line breaks become
// spaces.
export function snippetOf(excerpt: string | undefined, query: string, max = 140, lead = 36): Segment[] {
  const flat = (excerpt ?? '').replace(/\s*\n\s*/g, ' ').trim()
  if (!flat) return []
  const words = [...new Set(query.trim().toLowerCase().split(/\s+/).filter(Boolean))]
  const lower = flat.toLowerCase()
  let first = -1
  for (const w of words) {
    const i = lower.indexOf(w)
    if (i !== -1 && (first === -1 || i < first)) first = i
  }
  const chars = Array.from(flat)
  // Positions are by UTF-16 unit above; windows are cut by code point, which differs only past the BMP.
  const start = first > lead ? Array.from(flat.slice(0, first)).length - lead : 0
  const body = chars.slice(start, start + max).join('')
  const text = (start > 0 ? '…' : '') + body + (start + max < chars.length ? '…' : '')
  if (!words.length) return [{ text, hit: false }]
  const re = new RegExp(`(${words.map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|')})`, 'gi')
  return text
    .split(re)
    .filter((s) => s !== '')
    .map((s) => ({ text: s, hit: words.includes(s.toLowerCase()) }))
}

// --- misc ---------------------------------------------------------------------------------

export const pageLabel = (p: PageSummary, blank: string) => p.label || blank
