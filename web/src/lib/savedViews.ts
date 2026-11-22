// Saved views as the app works with them (docs/api.md, "Saved views"): what a view says apart from its name,
// when the working copy differs from the saved one, the order of the tabs, and which view a folder shows. Pure,
// so it is tested without a page.

import type { PageType, PropFilter, SavedView, TypeField, ViewLayout } from '../api/types.ts'

export const MAX_VIEWS = 20
export const VIEW_LAYOUTS: readonly ViewLayout[] = ['list', 'cards', 'table', 'board', 'read']
export const isLayout = (v: unknown): v is ViewLayout => typeof v === 'string' && (VIEW_LAYOUTS as readonly string[]).includes(v)

// The sorts a view may carry: the same words as a listing's `sort`.
export const SORT_WORDS = /^-?(updated|created|title|prop:.+)$/

// What a view is, apart from who it is: the part that is changed in a tab and saved with "Save".
export interface ViewState {
  layout: ViewLayout
  sort: string
  group: string
  filter: PropFilter[]
  hidden: string[]
  // The fields a card of a board shows (at most three). Absent: the first fields of the type.
  cardFields?: string[]
}

export const MAX_CARD_FIELDS = 3

export function stateOfView(v: SavedView): ViewState {
  const card = Array.isArray(v.cardFields) && v.cardFields.every((k) => typeof k === 'string') ? (v.cardFields as string[]) : undefined
  return { layout: isLayout(v.layout) ? v.layout : 'list', sort: v.sort ?? '', group: v.group ?? '', filter: v.filter ?? [], hidden: v.hidden ?? [], ...(card ? { cardFields: card } : {}) }
}

// The view with the state written into it; whatever else the view has (keys the app does not know) stays.
export function withState(v: Omit<SavedView, 'id'> & { id?: string }, s: ViewState): Omit<SavedView, 'id'> & { id?: string } {
  const out: Omit<SavedView, 'id'> & { id?: string } = { ...v, layout: s.layout, sort: s.sort, group: s.group, filter: s.filter, hidden: s.hidden }
  // A key the server does not know: the app keeps the fields of a card here.
  // The server cannot be told to forget a key it does not know, only to hold another value: null is "the default".
  if (s.cardFields) out.cardFields = s.cardFields
  else if ('cardFields' in v) out.cardFields = null
  return out
}

const norm = (s: ViewState) => JSON.stringify([s.layout, s.sort || '-updated', s.group, s.filter.map((f) => [f.key, f.op, f.values ?? []]), s.hidden, s.cardFields ?? null])
export const sameState = (a: ViewState, b: ViewState): boolean => norm(a) === norm(b)

export const DEFAULT_STATE: ViewState = { layout: 'list', sort: '', group: '', filter: [], hidden: [] }

// The server limits a view's name to 200 bytes of UTF-8 (a CJK character is 3), not to a number of characters.
export const MAX_NAME_BYTES = 200
export const nameBytes = (s: string): number => new TextEncoder().encode(s).length

// `s` cut, by whole characters, to at most `max` bytes.
export function cutBytes(s: string, max: number): string {
  let out = ''
  let used = 0
  for (const ch of s) {
    const n = nameBytes(ch)
    if (used + n > max) break
    out += ch
    used += n
  }
  return out
}

// "View 2", "View 3", … : a name no tab has yet.
export function freshName(names: readonly string[], base: string): string {
  const taken = new Set(names)
  if (!taken.has(base)) return base
  for (let n = 2; ; n++) if (!taken.has(`${base} ${n}`)) return `${base} ${n}`
}

// The list with the view of `id` moved to `to` (a position in the list); the same list when nothing moves.
export function moveView<T extends { id: string }>(list: readonly T[], id: string, to: number): T[] {
  const from = list.findIndex((v) => v.id === id)
  const dest = Math.max(0, Math.min(list.length - 1, to))
  if (from < 0 || from === dest) return [...list]
  const next = [...list]
  const [item] = next.splice(from, 1)
  next.splice(dest, 0, item)
  return next
}

// --- which view a folder shows ------------------------------------------------------------

export const viewIdKey = (space: string, kind: string, name = ''): string => `pb.viewid.${space}.${kind}${name === '' ? '' : `.${name}`}`

// The view last used in a folder or tag; storage may be blocked or hold something else.
export function loadViewId(key: string): string {
  try {
    return localStorage.getItem(key) ?? ''
  } catch {
    return ''
  }
}
export function saveViewId(key: string, id: string): void {
  try {
    if (id === '') localStorage.removeItem(key)
    else localStorage.setItem(key, id)
  } catch {
    // Storage blocked: the choice lasts until the page is left.
  }
}

// The view to show: the remembered one if it is still there, else the first (the default one), else none.
export function pickView(views: readonly SavedView[], remembered: string): SavedView | null {
  return views.find((v) => v.id === remembered) ?? views[0] ?? null
}

// --- board ------------------------------------------------------------------------------

// The fields a board can be grouped by: single choices.
export const groupFieldsOf = (type: PageType | undefined): TypeField[] => (type?.fields ?? []).filter((f) => f.kind === 'select')

export type ColumnKind = 'option' | 'other' | 'empty'
export interface BoardColumn {
  id: string
  kind: ColumnKind
  // The value of an `option` column.
  value?: string
  // The values an `other` column leaves out: the options, or the values the columns shown stand for.
  exclude?: readonly string[]
  // Pages in it; undefined while the counts are not known.
  count: number | undefined
}

// The most columns a board made of values (not of the options of a select) shows; the rest are one "other values".
export const MAX_COLUMNS = 50

// The columns of a board: the options in order, "other values" when some page has a value that is not an option, "empty".
// Without options (a tag's board, whose pages need not share a type) the columns are the values the pages have, most
// first, the first MAX_COLUMNS of them and "other values" for the rest. A select with more than MAX_COLUMNS options shows
// those that have pages.
export function boardColumns(options: readonly string[] | null, groups: { value: string; count: number }[] | undefined, empty: number | undefined, truncated = false): BoardColumn[] {
  const counts = groups ? new Map(groups.map((g) => [g.value, g.count])) : null
  const out: BoardColumn[] = []
  if (options) {
    const shown = options.length <= MAX_COLUMNS ? options : counts ? options.filter((o) => (counts.get(o) ?? 0) > 0) : options.slice(0, MAX_COLUMNS)
    for (const o of shown) out.push({ id: `o:${o}`, kind: 'option', value: o, count: counts ? (counts.get(o) ?? 0) : undefined })
    const others = groups ? groups.filter((g) => !options.includes(g.value)).reduce((n, g) => n + g.count, 0) : undefined
    if (others === undefined || others > 0) out.push({ id: 'other', kind: 'other', exclude: options, count: others })
  } else {
    const all = groups ?? []
    const head = all.slice(0, MAX_COLUMNS)
    for (const g of head) out.push({ id: `o:${g.value}`, kind: 'option', value: g.value, count: g.count })
    if (all.length > MAX_COLUMNS || truncated) {
      out.push({ id: 'other', kind: 'other', exclude: head.map((g) => g.value), count: all.slice(MAX_COLUMNS).reduce((n, g) => n + g.count, 0) })
    }
  }
  out.push({ id: 'empty', kind: 'empty', count: empty })
  return out
}

// The condition that picks the pages of a column. "Other values" is the pages that have a value and none of the
// excluded ones; with nothing to exclude, every page that has a value.
export function columnFilter(col: BoardColumn, key: string, options: readonly string[] | null): PropFilter {
  if (col.kind === 'empty') return { key, op: 'empty' }
  if (col.kind === 'other') {
    const values = [...(col.exclude ?? options ?? [])]
    return values.length > 0 ? { key, op: 'notIn', values } : { key, op: 'notEmpty' }
  }
  return { key, op: 'in', values: [col.value as string] }
}

// Whether a card can be dropped on a column: the value of an option of a select is the value to write, as typed; a
// column made of the values the index holds is not (they are text, cut when long, whatever the page has), and "other
// values" is no value. Taking the value away (the "empty" column) is always possible.
export const columnDroppable = (col: BoardColumn, typed: boolean): boolean => col.kind === 'empty' || (col.kind === 'option' && typed)

// The fields a card shows: the chosen ones that the type has, else the first three that are not the grouping one.
export function cardFieldsOf(type: PageType | undefined, chosen: readonly string[] | undefined, group: string): TypeField[] {
  const fields = type?.fields ?? []
  if (chosen) return chosen.map((k) => fields.find((f) => f.key === k)).filter((f): f is TypeField => !!f).slice(0, MAX_CARD_FIELDS)
  return fields.filter((f) => f.key !== group).slice(0, MAX_CARD_FIELDS)
}

// Whether a card may be moved to another column by changing its value of `key`: the value in the listing is the
// whole of it, and is one plain value (a list of values has no single place to go).
export function cardMovable(props: Record<string, unknown> | undefined, partial: readonly string[] | undefined, key: string): boolean {
  if (partial?.includes(key)) return false
  const v = props?.[key]
  return v === undefined || typeof v === 'string' || typeof v === 'number' || typeof v === 'boolean'
}
