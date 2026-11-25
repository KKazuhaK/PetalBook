// What the folder's table is made of, without any screen: the rows (subfolders first, or mixed in), what the headers
// sort by, and the filters the person put above it.

import type { ListParams, PageSummary, PropFilter, PageType, SortKey } from '@/api/types'
import type { FolderEntry } from '@/lib/folders'

export type TableRow = { kind: 'folder'; folder: FolderEntry } | { kind: 'page'; page: PageSummary }

export const rowKey = (r: TableRow): string => (r.kind === 'folder' ? `f:${r.folder.path}` : `p:${r.page.id}`)

const compare = (a: string, b: string) => a.localeCompare(b, undefined, { sensitivity: 'base', numeric: true })

// The rows to show. By default the subfolders come first, then the pages in the order the server gave. The folders are
// ordered by the sort where it means something for a folder: by name under a title sort, by their updated (the newest
// page in them) under an updated sort, empty ones last; by name under any other. With `mixed` the folders go in among
// the pages under a title or updated sort, by the same value; under any other order they follow the pages.
export function arrangeRows(folders: FolderEntry[], pages: PageSummary[], mixed: boolean, sort: string): TableRow[] {
  const byDate = sort === '' || sort === 'updated' || sort === '-updated'
  const dir = sort === '-title' || sort === '' || sort === '-updated' ? -1 : 1
  const ms = (iso?: string) => (iso ? Date.parse(iso) : NaN)
  const dated = (f: FolderEntry) => !Number.isNaN(ms(f.updated))
  const sorted = [...folders].sort((a, b) => {
    if (byDate && (dated(a) || dated(b))) {
      if (dated(a) !== dated(b)) return dated(a) ? -1 : 1 // empty ones last, either way round
      return (ms(a.updated) - ms(b.updated)) * dir || compare(a.name, b.name)
    }
    return compare(a.name, b.name) * (sort === '-title' ? -1 : 1)
  })
  const f: TableRow[] = sorted.map((folder) => ({ kind: 'folder', folder }))
  const p: TableRow[] = pages.map((page) => ({ kind: 'page', page }))
  if (!mixed) return [...f, ...p]
  if (sort !== 'title' && sort !== '-title' && !byDate) return [...p, ...f]
  // The folders, already in order, step in front of the first page they come before.
  const before = (folder: FolderEntry, row: Extract<TableRow, { kind: 'page' }>) =>
    byDate
      ? dated(folder) && (ms(folder.updated) - (Date.parse(row.page.updated) || 0)) * dir < 0
      : compare(folder.name, row.page.label) * dir <= 0
  const out: TableRow[] = []
  let i = 0
  for (const row of p) {
    while (i < sorted.length && before(sorted[i], row as Extract<TableRow, { kind: 'page' }>)) out.push(f[i++])
    out.push(row)
  }
  return [...out, ...f.slice(i)]
}

// A header's sort and the sort of the listing: `title` and `-title`, `prop:source`, `-prop:status`.
export interface HeaderSort {
  key: string
  order: 'asc' | 'desc'
}
export const toHeaderSort = (sort: string): HeaderSort | undefined => {
  const s = sort === '' ? '-updated' : sort
  return s.startsWith('-') ? { key: s.slice(1), order: 'desc' } : { key: s, order: 'asc' }
}
export const fromHeaderSort = (s: HeaderSort): string => (s.order === 'desc' ? `-${s.key}` : s.key)

// The filters above the table. Each is shown as a chip that can be taken away.
export interface TableFilters {
  tags: string[]
  sources: string[]
  // Choices of the select fields of the folder's type, by field key.
  fields: Record<string, string[]>
  created: { from: string; to: string }
  updated: { from: string; to: string }
}

export const noFilters = (): TableFilters => ({
  tags: [],
  sources: [],
  fields: {},
  created: { from: '', to: '' },
  updated: { from: '', to: '' },
})

export const filterCount = (f: TableFilters): number =>
  f.tags.length +
  f.sources.length +
  Object.values(f.fields).reduce((n, v) => n + v.length, 0) +
  (f.created.from || f.created.to ? 1 : 0) +
  (f.updated.from || f.updated.to ? 1 : 0)

// What the listing is asked besides the folder: tags, the sources and the fields as property filters, and the date ranges.
export function filterParams(
  f: TableFilters,
): Pick<ListParams, 'tag' | 'filter' | 'createdFrom' | 'createdTo' | 'updatedFrom' | 'updatedTo'> {
  const filter: PropFilter[] = []
  if (f.sources.length) filter.push({ key: 'source', op: 'in', values: f.sources })
  for (const [key, values] of Object.entries(f.fields)) if (values.length) filter.push({ key, op: 'in', values })
  return {
    ...(f.tags.length ? { tag: f.tags } : {}),
    ...(filter.length ? { filter } : {}),
    ...(f.created.from ? { createdFrom: f.created.from } : {}),
    ...(f.created.to ? { createdTo: f.created.to } : {}),
    ...(f.updated.from ? { updatedFrom: f.updated.from } : {}),
    ...(f.updated.to ? { updatedTo: f.updated.to } : {}),
  }
}

// The fields of a type that a person can filter by: the ones with a list of options.
export const filterableFields = (type: PageType | undefined) =>
  (type?.fields ?? []).filter((f) => (f.kind === 'select' || f.kind === 'multiselect') && (f.options?.length ?? 0) > 0)

// The columns of the table that can be sorted by (the server's own sorts and a property).
export const SORTABLE: readonly SortKey[] = ['title', 'updated', 'created']
