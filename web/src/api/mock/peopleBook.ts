// The mock's side of "Adding people in batches" (docs/api.md): the picker's search and the rows of a batch looked
// up by username or address. The mock has one tenant; so that the "other tenant" answer can be seen, a username
// that starts with `other-` and an address at other.example belong to another one.

import type { BatchAnswer, BatchRowOut, BatchStatus, PersonRef } from '../peopleTypes.ts'
import { HttpError } from './store.ts'

export interface PeopleUser {
  id: string
  username: string
  name: string
  email: string
  disabled: boolean
  role?: string
}

export const MAX_ROWS = 1000

export interface BatchInput {
  dryRun: boolean
  rows: { user: string; role?: string; name?: string; groups?: string[] }[]
}

export function readBatch(body: Record<string, unknown>): BatchInput {
  const rows = body.rows
  if (!Array.isArray(rows) || rows.length < 1 || rows.length > MAX_ROWS)
    throw new HttpError(400, 'invalid', `Rows holds 1 to ${MAX_ROWS} rows.`)
  return {
    dryRun: body.dryRun === true,
    rows: rows.map((r) => {
      const o = (r ?? {}) as Record<string, unknown>
      return {
        user: typeof o.user === 'string' ? o.user.trim() : '',
        role: typeof o.role === 'string' ? o.role : undefined,
        name: typeof o.name === 'string' ? o.name : undefined,
        groups: Array.isArray(o.groups) ? o.groups.filter((g): g is string => typeof g === 'string') : undefined,
      }
    }),
  }
}

export function createPeopleBook(o: { users: () => Map<string, PeopleUser> }) {
  const foreign = (key: string) => /^other-/i.test(key) || /@other\.example$/i.test(key)

  const search = (q: string, limit: number): PersonRef[] => {
    const text = q.trim().toLowerCase()
    if ([...text].length < 2 || [...text].length > 80) throw new HttpError(400, 'invalid', 'Q is 2 to 80 characters.')
    return [...o.users().values()]
      .filter((u) => !u.disabled && (u.username.startsWith(text) || u.name.toLowerCase().includes(text)))
      .sort((a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()))
      .slice(0, limit)
      .map((u) => ({ id: u.id, username: u.username, name: u.name }))
  }

  // Each key as the server's ResolveKeys answers it.
  const resolve = (keys: string[]): { status: BatchStatus; hit?: PeopleUser }[] => {
    const all = [...o.users().values()]
    const seen = new Set<string>()
    return keys.map((k) => {
      const key = k.trim().toLowerCase()
      if (key === '' || key.length > 254) return { status: 'invalid' }
      const hit = key.includes('@')
        ? all.find((u) => u.email.toLowerCase() === key)
        : all.find((u) => u.username === key)
      if (!hit) return { status: foreign(key) ? 'other_tenant' : 'not_found' }
      if (hit.disabled) return { status: 'disabled' }
      if (seen.has(hit.id)) return { status: 'duplicate' }
      seen.add(hit.id)
      return { status: 'ok', hit }
    })
  }

  const answer = (
    dryRun: boolean,
    results: BatchRowOut[],
    extra: Partial<Pick<BatchAnswer, 'mail'>> = {},
  ): BatchAnswer => {
    const summary: BatchAnswer['summary'] = {}
    for (const r of results) summary[r.status] = (summary[r.status] ?? 0) + 1
    return { dryRun, results, summary, ...extra }
  }

  return { search, resolve, answer, foreign }
}

// What a space or group batch tells of a row (as the server's hideOutside): another tenant's person and a disabled one
// are nobody, so that such a batch cannot ask the site which accounts exist.
export function hideOutside(s: BatchStatus): BatchStatus {
  return s === 'other_tenant' || s === 'disabled' ? 'not_found' : s
}
