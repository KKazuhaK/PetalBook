import { useCallback, useState } from 'react'

// What an administrator sets on a table (which columns, in which order, how wide, how tall the rows, how many to a
// page) lives in this browser only: nothing of it goes to the server. Every read and write of localStorage can throw
// (a private window, blocked site data, a full disk), and a table works the same without it.

export type Density = 'compact' | 'comfortable'
export const PAGE_SIZES = [25, 50, 100, 200] as const
export const DEFAULT_PAGE_SIZE = 50

export interface TablePrefs {
  // Columns the administrator hid, and the order of the ones they moved (a column not listed follows in its own place).
  hidden: string[]
  order: string[]
  // The width of a column as a part of the table's width (0 to 1), never pixels: the same share fits any screen.
  widths: Record<string, number>
  density: Density
  pageSize: number
}

export const defaultPrefs = (): TablePrefs => ({
  hidden: [],
  order: [],
  widths: {},
  density: 'comfortable',
  pageSize: DEFAULT_PAGE_SIZE,
})

const keyOf = (id: string) => `pb.table.${id}`

const isStrings = (v: unknown): v is string[] => Array.isArray(v) && v.every((x) => typeof x === 'string')

// A saved value from another version, or garbage, is ignored field by field.
export function sanitize(raw: unknown): TablePrefs {
  const out = defaultPrefs()
  if (!raw || typeof raw !== 'object') return out
  const r = raw as Record<string, unknown>
  if (isStrings(r.hidden)) out.hidden = r.hidden
  if (isStrings(r.order)) out.order = r.order
  if (r.widths && typeof r.widths === 'object') {
    for (const [k, v] of Object.entries(r.widths)) {
      if (typeof v === 'number' && Number.isFinite(v) && v > 0 && v < 1) out.widths[k] = v
    }
  }
  if (r.density === 'compact' || r.density === 'comfortable') out.density = r.density
  if (typeof r.pageSize === 'number' && (PAGE_SIZES as readonly number[]).includes(r.pageSize))
    out.pageSize = r.pageSize
  return out
}

export function readPrefs(id: string): TablePrefs {
  try {
    const raw = localStorage.getItem(keyOf(id))
    return raw ? sanitize(JSON.parse(raw)) : defaultPrefs()
  } catch {
    return defaultPrefs()
  }
}

export function writePrefs(id: string, prefs: TablePrefs): void {
  try {
    localStorage.setItem(keyOf(id), JSON.stringify(prefs))
  } catch {
    // The choice holds until the page is closed.
  }
}

export function clearPrefs(id: string): void {
  try {
    localStorage.removeItem(keyOf(id))
  } catch {
    // Nothing to forget.
  }
}

// The preferences of one table, read once and written at each change.
export function useTablePrefs(id: string) {
  const [prefs, setPrefs] = useState(() => readPrefs(id))
  const update = useCallback(
    (patch: Partial<TablePrefs>) =>
      setPrefs((prev) => {
        const next = { ...prev, ...patch }
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

// The widths of the shown columns as parts of the whole (they add up to 1): the saved share where there is one, else an
// even share of what is left, each at least its own least share.
export function shares(
  shown: readonly { id: string; weight: number }[],
  saved: Record<string, number>,
): Record<string, number> {
  const total = shown.reduce((n, c) => n + (saved[c.id] ?? c.weight), 0)
  const out: Record<string, number> = {}
  for (const c of shown) out[c.id] = (saved[c.id] ?? c.weight) / (total || 1)
  return out
}
