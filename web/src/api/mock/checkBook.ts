// The check page of one space of the in-memory server (docs/api.md, "Checks").
// The real server records the problems when it indexes a file; the mock works
// them out from the pages it holds whenever it is asked, which comes to the
// same answers: the same eight kinds, in the same order, with the same details.

import type { FieldKind, PageMeta, Problem, ProblemKind, TypeField } from '../types.ts'
import { BUILTIN_KEYS, RESERVED_KEYS } from '../../lib/pageTypes.ts'
import { HttpError } from './store.ts'

// In the order the check page lists them.
export const PROBLEM_KINDS: readonly ProblemKind[] = [
  'no_id',
  'duplicate_id',
  'bad_header',
  'type_mismatch',
  'not_in_type',
  'broken_link',
  'missing_asset',
  'duplicate_title',
]
export const CHECK_PAGE = 50
const MAX_PATHS = 20

// A file that is not a page. The mock keeps no files of its own, so a test (or
// the dev server) puts one there.
export interface Orphan {
  path: string
  kind: 'no_id' | 'bad_header' | 'duplicate_id'
  meta: PageMeta
  body: string
  // The rev of the file (the blob id of what it says).
  rev: string
  // The id a duplicate shares.
  id?: string
  // Where a bad header is wrong.
  line?: number
}

export interface CheckPageView {
  id: string
  path: string
  folder: string
  meta: PageMeta
  body: string
  rev: string
  // The links to other pages, in the order of the text, with the line of the file each is on.
  links: { id: string; path: string; line: number }[]
}

export interface CheckInput {
  pages: CheckPageView[]
  orphans: Orphan[]
  // The fields of the type a page of `folder` with header type `headerType` has; null for none.
  fieldsOf: (folder: string, headerType: string) => TypeField[] | null
  // Whether a file of the space exists, by its path from the root, with no leading slash.
  fileExists: (path: string) => boolean
  // Whether a link leads to a page: by its id when it has one, else by its path.
  linkResolves: (link: { id: string; path: string }) => boolean
}

const compare = (a: string, b: string): number => {
  const x = Array.from(a)
  const y = Array.from(b)
  for (let i = 0; i < Math.min(x.length, y.length); i++) {
    const d = x[i].codePointAt(0)! - y[i].codePointAt(0)!
    if (d !== 0) return d
  }
  return x.length - y.length
}

const isNone = (v: unknown) =>
  v === null || v === undefined || (typeof v === 'string' && v.trim() === '') || (Array.isArray(v) && v.length === 0)

// The header fields a type does not have: those with a value, except the ones the application keeps.
export function strayKeys(meta: PageMeta, fields: TypeField[]): string[] {
  const known = new Set(fields.map((f) => f.key))
  return Object.keys(meta).filter(
    (k) => !BUILTIN_KEYS.has(k) && !RESERVED_KEYS.has(k) && !isNone(meta[k]) && !known.has(k),
  )
}

function fits(f: { kind: FieldKind; options?: string[] }, v: unknown): boolean {
  const inOptions = (s: string) => !f.options || f.options.length === 0 || f.options.includes(s)
  switch (f.kind) {
    case 'text':
    case 'longtext':
      return typeof v === 'string'
    case 'select':
      return typeof v === 'string' && inOptions(v)
    case 'multiselect':
      return Array.isArray(v) && v.every((e) => typeof e === 'string' && inOptions(e))
    case 'date':
      return (
        typeof v === 'string' &&
        /^\d{4}-\d{2}-\d{2}$/.test(v) &&
        !Number.isNaN(Date.parse(v + 'T00:00:00Z')) &&
        new Date(v + 'T00:00:00Z').toISOString().slice(0, 10) === v
      )
    case 'number':
      return typeof v === 'number'
    case 'checkbox':
      return typeof v === 'boolean'
    case 'url':
      return typeof v === 'string' && /^[a-z][a-z0-9+.-]*:/i.test(v)
    default:
      return true
  }
}

const SCHEME = /^[a-z][a-z0-9+.-]*:/i

// The path of the file under assets/ that an address of a page of `folder` points at, or null.
export function assetOf(address: string, folder: string): string | null {
  if (address === '' || SCHEME.test(address) || address.startsWith('//') || address.startsWith('#')) return null
  const cut = address.search(/[?#]/)
  const pathPart = cut < 0 ? address : address.slice(0, cut)
  const segments = pathPart.startsWith('/') ? [] : folder.split('/').filter(Boolean)
  for (const raw of pathPart.split('/')) {
    let dec = raw
    try {
      dec = decodeURIComponent(raw)
    } catch {
      // taken as written
    }
    for (const seg of dec.split(/[/\\]/)) {
      if (seg === '' || seg === '.') continue
      if (seg === '..') segments.pop()
      else segments.push(seg)
    }
  }
  return segments.length >= 2 && segments[0] === 'assets' ? segments.join('/') : null
}

// The addresses of the images and links of a body (code blocks left out).
function addressesOf(body: string): string[] {
  if (!body.includes('](')) return []
  const out: string[] = []
  let fence = ''
  for (const line of body.split('\n')) {
    const t = line.trim()
    const f = /^(`{3,}|~{3,})/.exec(t)
    if (f) {
      if (!fence) fence = f[1][0]
      else if (f[1][0] === fence) fence = ''
      continue
    }
    if (fence) continue
    for (const m of line.matchAll(/\]\(\s*(?:<([^>]*)>|((?:[^\s()\\]|\\.|\([^\s()]*\))*))/g))
      out.push(m[1] ?? m[2] ?? '')
  }
  return out
}

export function problemsOf(input: CheckInput): Problem[] {
  const out: Problem[] = []
  const add = (p: Omit<Problem, 'fix'> & { fix?: Problem['fix'] }) => out.push({ fix: null, ...p })
  // Every problem names the rev of its file, which a fix is based on.
  const pageAt = new Map(input.pages.map((p) => [p.id, p]))

  for (const o of input.orphans) {
    if (o.kind === 'no_id')
      add({ kind: 'no_id', path: o.path, rev: o.rev, message: 'The header has no id', fix: 'add_id' })
    else if (o.kind === 'bad_header')
      add({
        kind: 'bad_header',
        path: o.path,
        rev: o.rev,
        message: 'The header is not valid YAML',
        ...(o.line ? { detail: { line: o.line } } : {}),
      })
    else {
      const served = o.id ? pageAt.get(o.id) : undefined
      const same = input.orphans.filter((x) => x.kind === 'duplicate_id' && x.id === o.id).map((x) => x.path)
      add({
        kind: 'duplicate_id',
        path: o.path,
        ...(o.id ? { id: o.id } : {}),
        rev: o.rev,
        message: `id ${o.id} is already used by ${served?.path ?? 'another file'}`,
        detail: { paths: [...(served ? [served.path] : []), ...same.sort(compare)].slice(0, MAX_PATHS) },
      })
    }
  }

  const titled = new Map<string, CheckPageView[]>()
  for (const p of input.pages) {
    const title = typeof p.meta.title === 'string' ? p.meta.title.trim() : ''
    const fields = input.fieldsOf(p.folder, typeof p.meta.type === 'string' ? p.meta.type.trim() : '')
    if (fields) {
      const bad = Object.keys(p.meta).filter((k) => {
        const f = fields.find((x) => x.key === k)
        return f !== undefined && !isNone(p.meta[k]) && !fits(f, p.meta[k])
      })
      if (bad.length)
        add({
          kind: 'type_mismatch',
          path: p.path,
          id: p.id,
          rev: p.rev,
          message: 'These fields do not fit their type: ' + bad.join(', '),
          detail: { fields: bad },
        })
      const stray = strayKeys(p.meta, fields)
      if (stray.length)
        add({
          kind: 'not_in_type',
          path: p.path,
          id: p.id,
          rev: p.rev,
          message: "These fields are not in the page's type: " + stray.join(', '),
          detail: { fields: stray },
          fix: 'remove_fields',
        })
    }
    const broken = p.links.filter((l) => !input.linkResolves(l)).slice(0, MAX_PATHS)
    for (const l of broken) {
      const message = l.id ? `No page has the id ${l.id}` : `No page is at ${l.path}`
      add({
        kind: 'broken_link',
        path: p.path,
        id: p.id,
        rev: p.rev,
        message,
        detail: { target: l.path, line: l.line },
      })
    }
    const seen = new Set<string>()
    for (const address of addressesOf(p.body)) {
      const rel = assetOf(address, p.folder)
      if (rel === null || seen.has(rel) || seen.size >= MAX_PATHS) continue
      seen.add(rel)
      if (!input.fileExists(rel))
        add({
          kind: 'missing_asset',
          path: p.path,
          id: p.id,
          rev: p.rev,
          message: `The file ${address} does not exist`,
          detail: { target: address },
        })
    }
    if (title) {
      const key = p.folder + '\u0000' + title.toLowerCase()
      titled.set(key, [...(titled.get(key) ?? []), p])
    }
  }
  for (const group of titled.values()) {
    if (group.length < 2) continue
    const paths = group
      .map((p) => p.path)
      .sort(compare)
      .slice(0, MAX_PATHS)
    for (const p of group)
      add({
        kind: 'duplicate_title',
        path: p.path,
        id: p.id,
        rev: p.rev,
        message: 'Another page in this folder has the same title',
        detail: { paths },
      })
  }

  const rank = (k: ProblemKind) => PROBLEM_KINDS.indexOf(k)
  return out
    .map((p, i) => ({ p, i }))
    .sort((a, b) => rank(a.p.kind) - rank(b.p.kind) || compare(a.p.path, b.p.path) || a.i - b.i)
    .map((x) => x.p)
}

// GET .../checks: the counts of every kind and a page of the problems.
export function checksPage(all: Problem[], kind: string | null, cursor: string | null) {
  if (kind !== null && kind !== '' && !PROBLEM_KINDS.includes(kind as ProblemKind))
    throw new HttpError(400, 'invalid', `unknown kind “${kind}”`)
  const counts = Object.fromEntries(PROBLEM_KINDS.map((k) => [k, all.filter((p) => p.kind === k).length])) as Record<
    ProblemKind,
    number
  >
  const rows = kind ? all.filter((p) => p.kind === kind) : all
  // The cursor is the position of the last problem given: its kind, path and place among those with the same.
  const key = (p: Problem, i: number) => {
    const before = rows.slice(0, i).filter((q) => q.kind === p.kind && q.path === p.path).length
    return encodeURIComponent(`${p.kind}|${before}|${p.path}`)
  }
  let from = 0
  if (cursor) {
    from = rows.findIndex((p, i) => key(p, i) === cursor) + 1
    if (from === 0) throw new HttpError(400, 'invalid', 'cursor is not one this listing gave')
  }
  const items = rows.slice(from, from + CHECK_PAGE)
  const last = from + items.length - 1
  return { counts, items, next: from + items.length < rows.length ? key(rows[last], last) : '' }
}
