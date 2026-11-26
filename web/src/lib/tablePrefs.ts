import { useCallback, useState } from 'react'
import { prefsStore } from './prefsStore'

// What a person sets on a table (which columns, in which order, how wide, how tall the rows, how many to a page) is
// kept by the prefs store (see prefsStore.ts): this browser today, never a database. A store that cannot be read or
// written leaves the table working the same without it.

export type Density = 'compact' | 'comfortable'
export const PAGE_SIZES = [25, 50, 100, 200] as const
export const DEFAULT_PAGE_SIZE = 50

export interface TablePrefs {
  // Columns the administrator hid, and the order of the ones they moved (a column not listed follows in its own place).
  hidden: string[]
  // Columns the person switched on in the table settings: a narrow box scrolls to keep them, and only the columns that
  // are shown by default are taken away when they do not fit.
  shown: string[]
  order: string[]
  // The width of a column as a part of the table's width (0 to 1), never pixels: the same share fits any screen.
  widths: Record<string, number>
  density: Density
  pageSize: number
  // The person changed the columns, their order or widths: a default from elsewhere (a saved view) no longer applies.
  custom: boolean
}

export const defaultPrefs = (): TablePrefs => ({
  hidden: [],
  shown: [],
  order: [],
  widths: {},
  density: 'comfortable',
  pageSize: DEFAULT_PAGE_SIZE,
  custom: false,
})

const keyOf = (id: string) => `pb.table.${id}`

const isStrings = (v: unknown): v is string[] => Array.isArray(v) && v.every((x) => typeof x === 'string')

// A saved value from another version, or garbage, is ignored field by field.
export function sanitize(raw: unknown): TablePrefs {
  const out = defaultPrefs()
  if (!raw || typeof raw !== 'object') return out
  const r = raw as Record<string, unknown>
  if (isStrings(r.hidden)) out.hidden = r.hidden
  if (isStrings(r.shown)) out.shown = r.shown
  if (isStrings(r.order)) out.order = r.order
  if (r.widths && typeof r.widths === 'object') {
    for (const [k, v] of Object.entries(r.widths)) {
      if (typeof v === 'number' && Number.isFinite(v) && v > 0 && v < 1) out.widths[k] = v
    }
  }
  if (r.custom === true) out.custom = true
  if (r.density === 'compact' || r.density === 'comfortable') out.density = r.density
  if (typeof r.pageSize === 'number' && (PAGE_SIZES as readonly number[]).includes(r.pageSize))
    out.pageSize = r.pageSize
  return out
}

export function readPrefs(id: string): TablePrefs {
  const raw = prefsStore().read(keyOf(id))
  return raw ? sanitize(raw) : defaultPrefs()
}

export function writePrefs(id: string, prefs: TablePrefs): void {
  prefsStore().write(keyOf(id), prefs)
}

export function clearPrefs(id: string): void {
  prefsStore().remove(keyOf(id))
}

// The preferences of one table, read once and written at each change.
export function useTablePrefs(id: string) {
  const [prefs, setPrefs] = useState(() => readPrefs(id))
  const update = useCallback(
    (patch: Partial<TablePrefs>) =>
      setPrefs((prev) => {
        const touches = 'hidden' in patch || 'shown' in patch || 'order' in patch || 'widths' in patch
        // `custom` may be given outright: putting the columns back to how they were is not a choice of the person's.
        const next = { ...prev, ...patch, ...(touches && !('custom' in patch) ? { custom: true } : {}) }
        writePrefs(id, next)
        return next
      }),
    [id],
  )
  const reset = useCallback(() => {
    clearPrefs(id)
    setPrefs(defaultPrefs())
  }, [id])
  return { prefs, update, reset }
}

// The columns in the order the administrator chose: the ones they placed first, the rest after, in their own order.
export function orderIds(all: readonly string[], saved: readonly string[]): string[] {
  const known = saved.filter((id) => all.includes(id))
  return [...known, ...all.filter((id) => !known.includes(id))]
}

// The widths of the shown columns as parts of the whole (they add up to 1): the saved share where there is one, and
// for a column with none (it was hidden, or is new) the share its weight would give it, so a saved share and a weight
// are never added as if they were the same unit.
export function shares(
  shown: readonly { id: string; weight: number }[],
  saved: Record<string, number>,
): Record<string, number> {
  const weights = shown.reduce((n, c) => n + c.weight, 0) || 1
  const raw = shown.map((c) => saved[c.id] ?? c.weight / weights)
  const total = raw.reduce((n, v) => n + v, 0) || 1
  const out: Record<string, number> = {}
  shown.forEach((c, i) => (out[c.id] = raw[i] / total))
  return out
}
