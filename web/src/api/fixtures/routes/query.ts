// Filtering and sorting a listing by the properties of a header: what a list route does with `filter=` and `sort=prop:`.

import type { PageMeta, PropFilter } from '../../types.ts'

// What a header holds under a key: one value per scalar, one per element of a list; blank text and nesting are none.
export function valuesOf(meta: PageMeta, key: string): (string | number | boolean)[] {
  const one = (x: unknown): (string | number | boolean)[] =>
    typeof x === 'string' ? (x.trim() === '' ? [] : [x]) : typeof x === 'number' || typeof x === 'boolean' ? [x] : []
  const v = meta[key]
  return Array.isArray(v) ? v.flatMap(one) : one(v)
}

export function matches(meta: PageMeta, f: PropFilter): boolean {
  const vals = valuesOf(meta, f.key)
  const given = f.values ?? []
  switch (f.op) {
    case 'empty':
      return vals.length === 0
    case 'notEmpty':
      return vals.length > 0
    case 'in':
      return vals.some((v) => given.includes(v))
    case 'notIn':
      return vals.length > 0 && !vals.some((v) => given.includes(v))
    case 'contains':
      return vals.some((v) => typeof v === 'string' && v.toLowerCase().includes(String(given[0] ?? '').toLowerCase()))
    case 'range': {
      const [from, to] = given.map((x) => (x === '' ? null : x))
      return vals.some(
        (v) =>
          (from == null || (typeof v === typeof from && v >= from)) &&
          (to == null || (typeof v === typeof to && v <= to)),
      )
    }
  }
}

// The value a page is ordered by for a key, or undefined when it has none.
export function sortValue(meta: PageMeta, key: string): string | number | undefined {
  const first = valuesOf(meta, key)[0]
  if (first === undefined) return undefined
  return typeof first === 'boolean' ? Number(first) : typeof first === 'string' ? first.toLowerCase() : first
}
