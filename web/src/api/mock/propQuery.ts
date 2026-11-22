// Sorting and filtering the listing by properties of the header (docs/api.md,
// "Sorting by a property" and "Filtering by properties").

import type { FilterOp, PageMeta, PropFilter as TablePropFilter } from '../types.ts'
import { HttpError } from './store.ts'

// The table filters by the ops the app knows; the server also has notIn (the
// "other values" column of a board), which the board will use.
export type PropFilter = Omit<TablePropFilter, 'op'> & { op: FilterOp | 'notIn' }

const invalid = (message: string) => new HttpError(400, 'invalid', message)
const OPS: readonly string[] = ['in', 'notIn', 'range', 'empty', 'notEmpty', 'contains']
const cut = (s: string) => Array.from(s).slice(0, 300).join('')

// What a header holds under a key, as the values the index would have: one per
// scalar, one per element of a list; blank text, null and nested things are none.
export function valuesOf(meta: PageMeta, key: string): (string | number | boolean)[] {
  const v = meta[key]
  const one = (x: unknown): (string | number | boolean)[] => (typeof x === 'string' ? (x.trim() === '' ? [] : [cut(x)]) : typeof x === 'number' || typeof x === 'boolean' ? [x] : [])
  if (Array.isArray(v)) return v.flatMap(one)
  return one(v)
}

export function parseFilter(raw: string): PropFilter[] {
  if (raw.length > 16 * 1024) throw invalid('filter 太长')
  let json: unknown
  try {
    json = JSON.parse(raw)
  } catch {
    throw invalid('filter 不是 JSON')
  }
  if (!Array.isArray(json)) throw invalid('filter 要是数组')
  if (json.length > 10) throw invalid('filter 最多 10 项')
  return json.map((item): PropFilter => {
    if (typeof item !== 'object' || item === null || Array.isArray(item)) throw invalid('filter 的每一项要是对象')
    const o = item as Record<string, unknown>
    for (const k of Object.keys(o)) if (k !== 'key' && k !== 'op' && k !== 'values') throw invalid(`filter 里没有 ${k} 这个成员`)
    if (typeof o.key !== 'string' || o.key === '') throw invalid('filter 的 key 不能空')
    if (typeof o.op !== 'string' || !OPS.includes(o.op)) throw invalid('filter 的 op 不认识')
    const op = o.op as PropFilter['op']
    const values = o.values === undefined || o.values === null ? [] : o.values
    if (!Array.isArray(values)) throw invalid('values 要是数组')
    const scalar = (x: unknown) => typeof x === 'string' || typeof x === 'number' || typeof x === 'boolean'
    if (op === 'empty' || op === 'notEmpty') {
      if (values.length) throw invalid(`${op} 不带 values`)
    } else if (op === 'in' || op === 'notIn') {
      if (values.length < 1 || values.length > 100 || !values.every(scalar)) throw invalid(`${op} 要 1 到 100 个值`)
    } else if (op === 'contains') {
      if (values.length !== 1 || typeof values[0] !== 'string') throw invalid('contains 要一段文字')
    } else {
      const ends = values.map((x) => (x === '' ? null : x))
      if (ends.length !== 2 || ends.every((x) => x === null) || !ends.every((x) => x === null || typeof x === 'number' || typeof x === 'string')) throw invalid('range 要 [从, 到]')
      const kinds = new Set(ends.filter((x) => x !== null).map((x) => typeof x))
      if (kinds.size > 1) throw invalid('range 的两端要是同一种')
    }
    return { key: o.key, op, ...(values.length ? { values: values as PropFilter['values'] } : {}) }
  })
}

const eq = (a: unknown, b: unknown) => a === b
export function matches(meta: PageMeta, f: PropFilter): boolean {
  const vals = valuesOf(meta, f.key)
  switch (f.op) {
    case 'empty':
      return vals.length === 0
    case 'notEmpty':
      return vals.length > 0
    case 'in':
      return vals.some((v) => (f.values ?? []).some((w) => eq(v, w)))
    case 'notIn':
      return vals.length > 0 && !vals.some((v) => (f.values ?? []).some((w) => eq(v, w)))
    case 'contains': {
      const needle = String(f.values?.[0] ?? '')
      const lower = (s: string) => s.replace(/[A-Z]/g, (c) => c.toLowerCase())
      return vals.some((v) => typeof v === 'string' && lower(v).includes(lower(needle)))
    }
    case 'range': {
      const [from, to] = (f.values ?? []).map((x) => (x === '' ? null : x))
      return vals.some((v) => (from === null || from === undefined || (typeof v === typeof from && v >= from)) && (to === null || to === undefined || (typeof v === typeof to && v <= to)))
    }
  }
}

// The value a page is ordered by for a key, or undefined when it has none.
export function sortValue(meta: PageMeta, key: string): string | number | undefined {
  const first = valuesOf(meta, key)[0]
  if (first === undefined) return undefined
  if (typeof first === 'boolean') return first ? 1 : 0
  return typeof first === 'string' ? first.toLowerCase() : first
}
