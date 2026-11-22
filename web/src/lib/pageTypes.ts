// Rules about page types that more than one place needs (docs/api.md, "Page
// types"). Pure, so they are tested without a page.

import type { FieldKind, FilterOp, FolderBinding, PageMeta, TypeField } from '../api/types.ts'

// The header keys the app has fields of its own for, and the two the server
// keeps back: neither is a property of a type, and a change of a property never
// touches them.
export const BUILTIN_KEYS: ReadonlySet<string> = new Set(['id', 'title', 'subtitle', 'source', 'tags', 'created', 'type'])
export const RESERVED_KEYS: ReadonlySet<string> = new Set(['publish', 'slug'])
export const isPropertyKey = (key: string) => !BUILTIN_KEYS.has(key) && !RESERVED_KEYS.has(key)

export const FIELD_KINDS: readonly FieldKind[] = ['text', 'longtext', 'select', 'multiselect', 'date', 'number', 'checkbox', 'url']

// The folder a path is in, one level up; "" for a top-level folder, null for the root.
const parentOf = (folder: string): string | null => (folder === '' ? null : folder.includes('/') ? folder.slice(0, folder.lastIndexOf('/')) : '')

// The type a folder has: its own binding, else that of the nearest folder above
// it, the root included. Folder names compare as the server holds them.
export function folderTypeName(bindings: readonly FolderBinding[], folder: string): string {
  const by = new Map(bindings.map((b) => [b.folder, b.type]))
  for (let f: string | null = folder; f !== null; f = parentOf(f)) {
    const type = by.get(f)
    if (type) return type
  }
  return ''
}

// The folder a binding comes from, "" for the root, or null when the folder has none.
export function bindingSource(bindings: readonly FolderBinding[], folder: string): string | null {
  const by = new Map(bindings.map((b) => [b.folder, b.type]))
  for (let f: string | null = folder; f !== null; f = parentOf(f)) if (by.get(f)) return f
  return null
}

const DAY = /^\d{4}-\d{2}-\d{2}$/
export function isDay(v: unknown): v is string {
  if (typeof v !== 'string' || !DAY.test(v)) return false
  const d = new Date(`${v}T00:00:00Z`)
  return !Number.isNaN(d.getTime()) && d.toISOString().slice(0, 10) === v
}

// Whether a value fits what its field says. A value that does not is kept and
// shown as wrong; nothing here changes it.
export function fits(field: Pick<TypeField, 'kind' | 'options'>, v: unknown): boolean {
  switch (field.kind) {
    case 'text':
    case 'longtext':
      return typeof v === 'string'
    case 'url':
      return typeof v === 'string' && /^[a-z][a-z0-9+.-]*:/i.test(v)
    case 'select':
      return typeof v === 'string' && (field.options ?? []).includes(v)
    case 'multiselect':
      return Array.isArray(v) && v.every((x) => typeof x === 'string' && (field.options ?? []).includes(x))
    case 'date':
      return isDay(v)
    case 'number':
      return typeof v === 'number' && Number.isFinite(v)
    case 'checkbox':
      return typeof v === 'boolean'
    case 'other':
      return true
  }
}

// Whether a header has a value for a field: `false` and `0` are values.
export const hasValue = (v: unknown): boolean => v !== undefined && v !== null && v !== '' && !(Array.isArray(v) && v.length === 0)

// The ops a filter on a field of this kind offers.
export function filterOps(kind: FieldKind): FilterOp[] {
  switch (kind) {
    case 'text':
    case 'longtext':
    case 'url':
      return ['contains', 'empty', 'notEmpty']
    case 'select':
    case 'multiselect':
      return ['in', 'empty', 'notEmpty']
    case 'date':
    case 'number':
      return ['range', 'empty', 'notEmpty']
    case 'checkbox':
      return ['in', 'empty', 'notEmpty']
    case 'other':
      return ['empty', 'notEmpty']
  }
}

// The properties of a header as a table cell can hold them: what the server puts
// in a summary's `props` (docs/api.md, "Page summary"), so a row changed by hand
// reads the same as one the listing brought.
export function propsOfMeta(meta: PageMeta): Record<string, unknown> {
  const plain = (v: unknown): v is string | number | boolean => (typeof v === 'string' && v.trim() !== '') || typeof v === 'number' || typeof v === 'boolean'
  const cut = (v: string | number | boolean) => (typeof v === 'string' ? Array.from(v).slice(0, 300).join('') : v)
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(meta)) {
    if (BUILTIN_KEYS.has(k)) continue
    if (plain(v)) out[k] = cut(v)
    else if (Array.isArray(v)) {
      const list = v.filter(plain).slice(0, 100).map(cut)
      if (list.length) out[k] = list
    }
  }
  return out
}

// The keys of a header whose copy in `propsOfMeta` is not the whole of the value
// (docs/api.md, "Page summary", `partialProps`): text that was cut, a list that was
// cut or lost an element, an object, a key past the 200th. The server says the same
// in a listing; this is for a row a change has just brought.
export function partialPropsOf(meta: PageMeta): string[] {
  const plain = (v: unknown): v is string | number | boolean => (typeof v === 'string' && v.trim() !== '') || typeof v === 'number' || typeof v === 'boolean'
  // An integer past 2^53 is not what the file says once a browser has had it.
  const whole = (v: string | number | boolean) => (typeof v === 'string' ? Array.from(v).length <= 300 : typeof v === 'number' ? !Number.isInteger(v) || Math.abs(v) <= 2 ** 53 : true)
  const out: string[] = []
  let held = 0
  for (const [k, v] of Object.entries(meta)) {
    if (BUILTIN_KEYS.has(k)) continue
    if (held >= 200) {
      out.push(k)
      continue
    }
    if (Array.isArray(v)) {
      let lossy = false
      let kept = 0
      for (const e of v) {
        if (kept === 100) {
          lossy = true
          break
        }
        if (plain(e)) {
          kept++
          if (!whole(e)) lossy = true
        } else lossy = true
      }
      if (kept === 0) {
        if (v.length > 0) out.push(k)
        continue
      }
      held++
      if (lossy) out.push(k)
    } else if (plain(v)) {
      held++
      if (!whole(v)) out.push(k)
    } else if (v !== null && v !== undefined && typeof v !== 'string') {
      out.push(k) // an object
    }
  }
  return out
}

// A value as one line of text, for a cell and for a key that is not in the type.
export function showValue(v: unknown): string {
  if (v === undefined || v === null) return ''
  if (typeof v === 'string') return v
  if (typeof v === 'number' || typeof v === 'boolean') return String(v)
  if (Array.isArray(v)) return v.map(showValue).join(', ')
  try {
    return JSON.stringify(v)
  } catch {
    return String(v)
  }
}

const bytes = (s: string) => new TextEncoder().encode(s).length

// What is wrong with a name for a new type, or null (docs/api.md, "Page types").
// `taken` are the names there already; two that differ only in case are one name.
export function typeNameProblem(name: string, taken: readonly string[]): 'empty' | 'long' | 'chars' | 'exists' | null {
  if (name.trim() === '') return 'empty'
  if (bytes(name) > 100) return 'long'
  if (/[<>:"/\\|?*\p{Cc}]/u.test(name) || name.startsWith('.') || name.endsWith('.') || name.endsWith(' ')) return 'chars'
  if (taken.some((n) => n.toLowerCase() === name.toLowerCase())) return 'exists'
  return null
}

// What is wrong with a field's key, or null: the rules of the server, so a form can say so first.
export function fieldKeyProblem(key: string): 'empty' | 'long' | 'chars' | 'builtin' | null {
  if (key === '') return 'empty'
  if (bytes(key) > 64) return 'long'
  if (/[\p{Cc}:]/u.test(key)) return 'chars'
  if (BUILTIN_KEYS.has(key) || RESERVED_KEYS.has(key)) return 'builtin'
  return null
}
