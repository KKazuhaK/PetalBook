// What changed in the header fields of a page (everything above the body).

import { diffText, type TextPart } from './inline'

export type Meta = Record<string, unknown>

export type HeaderChange =
  | { kind: 'text'; field: 'title' | 'subtitle' | 'source'; from: string; to: string; parts: TextPart[] }
  | { kind: 'tags'; added: string[]; removed: string[] }
  | { kind: 'other'; key: string; change: 'added' | 'removed' | 'changed'; from?: unknown; to?: unknown }

const TEXT_FIELDS = ['title', 'subtitle', 'source'] as const
// The id never changes, and tags and the text fields are shown on their own.
const OWN = new Set<string>(['id', 'tags', ...TEXT_FIELDS])

const text = (v: unknown): string => (v === undefined || v === null ? '' : typeof v === 'string' ? v : JSON.stringify(v))

function sortKeys(v: unknown): unknown {
  if (Array.isArray(v)) return v.map(sortKeys)
  if (v && typeof v === 'object') {
    const o = v as Record<string, unknown>
    return Object.fromEntries(Object.keys(o).sort().map((k) => [k, sortKeys(o[k])]))
  }
  return v
}
const same = (a: unknown, b: unknown) => JSON.stringify(sortKeys(a)) === JSON.stringify(sortKeys(b))

const tagsOf = (m: Meta): string[] => (Array.isArray(m.tags) ? m.tags.filter((t): t is string => typeof t === 'string') : [])

export function diffHeader(a: Meta, b: Meta): HeaderChange[] {
  const out: HeaderChange[] = []
  for (const field of TEXT_FIELDS) {
    const from = text(a[field])
    const to = text(b[field])
    if (from !== to) out.push({ kind: 'text', field, from, to, parts: diffText(from, to) })
  }
  const ta = tagsOf(a)
  const tb = tagsOf(b)
  const added = tb.filter((t) => !ta.includes(t))
  const removed = ta.filter((t) => !tb.includes(t))
  if (added.length || removed.length) out.push({ kind: 'tags', added, removed })
  // Other keys, in the order they first appear (new side first).
  const keys = [...Object.keys(b), ...Object.keys(a).filter((k) => !(k in b))].filter((k) => !OWN.has(k))
  for (const key of keys) {
    const inA = key in a
    const inB = key in b
    if (inA && inB) {
      if (!same(a[key], b[key])) out.push({ kind: 'other', key, change: 'changed', from: a[key], to: b[key] })
    } else if (inB) out.push({ kind: 'other', key, change: 'added', to: b[key] })
    else out.push({ kind: 'other', key, change: 'removed', from: a[key] })
  }
  return out
}
