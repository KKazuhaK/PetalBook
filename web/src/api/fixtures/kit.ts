// What the routes are written with: the shape of an answer, a refusal, the request a handler gets, and the generic
// operations on a collection (list in pages, find, add, change, remove).

import type { ErrorCode } from '../types.ts'
import type { FixtureData } from './data/model.ts'
import type { FixtureServer } from './server.ts'

export interface Reply {
  status: number
  body?: unknown
  headers?: Record<string, string>
  bytes?: Uint8Array
}

const REPLY = Symbol('reply')
export const reply = (status: number, body?: unknown, headers?: Record<string, string>): Reply =>
  ({ status, body, headers, [REPLY]: true }) as Reply
export const isReply = (v: unknown): v is Reply => typeof v === 'object' && v !== null && REPLY in v

// A refusal: what a route throws, and what `fx.fail` makes.
export class Refusal extends Error {
  status: number
  code: ErrorCode | 'not_found' | 'invalid' | 'conflict' | 'unauthenticated'
  extra: Record<string, unknown>
  constructor(
    status: number,
    code: ErrorCode | 'not_found' | 'invalid' | 'conflict' | 'unauthenticated',
    message = '',
    extra: Record<string, unknown> = {},
  ) {
    super(message)
    this.status = status
    this.code = code
    this.extra = extra
  }
}

export interface Ctx {
  fx: FixtureServer
  data: FixtureData
  method: string
  path: string
  params: Record<string, string>
  query: URLSearchParams
  // The JSON the request carried ({} when it carried none).
  body: any
  raw?: Uint8Array
  now: () => number
}
export type Handler = (c: Ctx) => unknown
export type Route = [method: string, pattern: string, handler: Handler]

const segments = (p: string) => p.split('/').filter((s) => s !== '')
// `:name` takes one segment; a trailing `*` takes the rest.
export function match(pattern: string, path: string): Record<string, string> | null {
  const a = segments(pattern)
  const b = segments(path)
  const params: Record<string, string> = {}
  for (let i = 0; i < a.length; i++) {
    if (a[i] === '*') return params
    if (i >= b.length) return null
    if (a[i].startsWith(':')) params[a[i].slice(1)] = decodeURIComponent(b[i])
    else if (a[i] !== b[i]) return null
  }
  return a.length === b.length ? params : null
}

// --- generic operations on a collection ---------------------------------------------------------------------------

// A list in pages of `limit`; the cursor is only an offset.
export function paged<T>(all: T[], query: URLSearchParams, limit = 50): { items: T[]; next: string } {
  const size = Number(query.get('limit')) || limit
  const from = Number(query.get('cursor') ? atob(query.get('cursor')!) : 0) || 0
  const items = all.slice(from, from + size)
  return { items, next: from + items.length < all.length ? btoa(String(from + items.length)) : '' }
}

export const find = <T>(rows: T[], is: (row: T) => boolean, what = 'it'): T => {
  const row = rows.find(is)
  if (!row) throw new Refusal(404, 'not_found', `${what} is not there`)
  return row
}
export const add = <T>(rows: T[], row: T): T => (rows.push(row), row)
export const change = <T extends object>(row: T, patch: Partial<T>): T => Object.assign(row, patch)
export const remove = <T>(rows: T[], row: T) => void rows.splice(rows.indexOf(row), 1)

// --- small helpers every area uses --------------------------------------------------------------------------------

export const rfc3339 = (ms: number) => new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z')

// A stand-in for a Git blob id: 40 hex digits that depend only on the text, so the same content is the same rev.
export function hash(text: string): string {
  let out = ''
  for (let round = 0; round < 5; round++) {
    let h = 0x811c9dc5 ^ (round * 0x9e3779b1)
    for (let i = 0; i < text.length; i++) h = Math.imul(h ^ text.charCodeAt(i), 0x01000193)
    out += (h >>> 0).toString(16).padStart(8, '0')
  }
  return out
}

const ALPHABET = '0123456789ABCDEFGHJKMNPQRSTVWXYZ'
// A new id, 10 characters of the ULID alphabet, from the run's counter (so a run is repeatable).
export function newId(data: FixtureData, prefix = '01FZ'): string {
  let n = ++data.counter
  let tail = ''
  for (let i = 0; i < 6; i++, n = Math.floor(n / 32)) tail = ALPHABET[n % 32] + tail
  return prefix + tail
}
