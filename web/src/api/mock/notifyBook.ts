// The notification centre and the person's own preferences of the in-memory server (docs/api.md, "Notifications" and
// "Preferences"). The rows are what the real server keeps in its `notifications` table; the problems of the spaces a
// person writes in are not rows but are worked out from the spaces when asked, with a cursor of what the person read.

import type { NotificationItem } from '../types.ts'
import { NOTIFY_KINDS } from '../types.ts'
import { HttpError, rfc3339 } from './store.ts'

interface Row {
  id: string
  userId: string
  kind: string
  ref: string
  payload: Record<string, unknown>
  at: number
  readAt: number | null
}

// A space a person writes in that has problems, as the server derives it.
export interface DerivedProblems {
  id: string
  name: string
  counts: Record<string, number>
}

const CAP = 100
const KEY_RE = /^[A-Za-z0-9._:/-]{1,120}$/

const sigOf = (counts: Record<string, number>) =>
  Object.keys(counts)
    .sort()
    .map((k) => `${k}=${counts[k]}`)
    .join(';')

export function createNotifyBook(opts: { now: () => number }) {
  const rows: Row[] = []
  let seq = 0
  // user -> key -> the signature last read
  const seen = new Map<string, Map<string, string>>()
  const prefs = new Map<string, Record<string, unknown>>()

  const derivedItems = (userId: string, derived: DerivedProblems[]): NotificationItem[] => {
    const mine = seen.get(userId)
    return derived
      .map((d) => ({ ...d, counts: Object.fromEntries(Object.entries(d.counts).filter(([, n]) => n > 0)) }))
      .filter((d) => Object.keys(d.counts).length > 0)
      .map((d): NotificationItem => {
        const sig = sigOf(d.counts)
        const total = Object.values(d.counts).reduce((a, b) => a + b, 0)
        return {
          id: `problems:${d.id}`,
          kind: 'problems',
          ref: d.id,
          payload: { space: d.name, total, counts: d.counts, sig },
          at: null,
          read: mine?.get(`problems:${d.id}`) === sig,
          derived: true,
        }
      })
  }
  const unreadOf = (userId: string, derived: NotificationItem[]) =>
    Math.min(
      CAP,
      Math.min(CAP, rows.filter((r) => r.userId === userId && r.readAt === null).length) +
        derived.filter((d) => !d.read).length,
    )

  return {
    // A row for a person (the mock has one tenant, so no tenant to keep apart).
    add(userId: string, kind: string, ref: string, payload: Record<string, unknown>) {
      if (!(NOTIFY_KINDS as readonly string[]).includes(kind) || kind === 'problems') throw new Error(`kind ${kind}`)
      // Ids sort by time, like the real server's.
      rows.push({
        id: `${opts.now().toString(16).padStart(12, '0')}${(++seq).toString(16).padStart(6, '0')}`,
        userId,
        kind,
        ref,
        payload,
        at: opts.now(),
        readAt: null,
      })
    },

    list(userId: string, derived: DerivedProblems[], q: URLSearchParams) {
      const kind = q.get('kind') ?? ''
      if (kind !== '' && !(NOTIFY_KINDS as readonly string[]).includes(kind))
        throw new HttpError(400, 'invalid', 'That is not a kind of notification.')
      let limit = 20
      if (q.get('limit') !== null) {
        const n = Number(q.get('limit'))
        if (!Number.isInteger(n) || n < 1 || n > 100)
          throw new HttpError(400, 'invalid', 'The limit must be a number from 1 to 100.')
        limit = n
      }
      const unreadOnly = q.get('unread') === '1'
      const cursor = q.get('cursor') ?? ''
      const all = derivedItems(userId, derived)
      const items: NotificationItem[] = []
      if (cursor === '' && (kind === '' || kind === 'problems'))
        items.push(...all.filter((d) => !unreadOnly || !d.read))
      let next = ''
      if (kind !== 'problems') {
        const mine = rows
          .filter((r) => r.userId === userId && (kind === '' || r.kind === kind) && (!unreadOnly || r.readAt === null))
          .filter((r) => cursor === '' || r.id < cursor)
          .sort((a, b) => (a.id < b.id ? 1 : -1))
        const page = mine.slice(0, limit)
        if (mine.length > limit) next = page[page.length - 1].id
        for (const r of page)
          items.push({
            id: r.id,
            kind: r.kind,
            ref: r.ref,
            payload: r.payload,
            at: rfc3339(r.at),
            read: r.readAt !== null,
          })
      }
      return { items, next, unread: unreadOf(userId, all) }
    },

    unread(userId: string, derived: DerivedProblems[]) {
      return { count: unreadOf(userId, derivedItems(userId, derived)) }
    },

    read(userId: string, derived: DerivedProblems[], ids: unknown) {
      if (
        !Array.isArray(ids) ||
        ids.length < 1 ||
        ids.length > 100 ||
        !ids.every((i) => typeof i === 'string' && i.length <= 200)
      )
        throw new HttpError(400, 'invalid', 'Name from 1 to 100 notifications.')
      const items = derivedItems(userId, derived)
      for (const id of ids as string[]) {
        if (id.startsWith('problems:')) {
          const d = items.find((x) => x.id === id)
          if (d) {
            const m = seen.get(userId) ?? new Map<string, string>()
            m.set(id, String(d.payload.sig))
            seen.set(userId, m)
          }
        } else {
          const r = rows.find((x) => x.id === id && x.userId === userId && x.readAt === null)
          if (r) r.readAt = opts.now()
        }
      }
      return { unread: unreadOf(userId, derivedItems(userId, derived)) }
    },

    readAll(userId: string, derived: DerivedProblems[], kind: unknown) {
      if (kind !== undefined && kind !== '' && !(NOTIFY_KINDS as readonly string[]).includes(String(kind)))
        throw new HttpError(400, 'invalid', 'That is not a kind of notification.')
      if (kind !== 'problems')
        for (const r of rows)
          if (r.userId === userId && r.readAt === null && (!kind || r.kind === kind)) r.readAt = opts.now()
      if (!kind || kind === 'problems') {
        const items = derivedItems(userId, derived)
        const m = new Map<string, string>()
        for (const d of items) m.set(d.id, String(d.payload.sig))
        seen.set(userId, m)
      }
      return { unread: unreadOf(userId, derivedItems(userId, derived)) }
    },

    // --- the person's own preferences (a file of their personal space on the real server) ---
    getPrefs(userId: string) {
      return { personal: true, prefs: { ...(prefs.get(userId) ?? {}) } }
    },
    patchPrefs(userId: string, body: Record<string, unknown>) {
      const set = body.set
      if (!set || typeof set !== 'object' || Array.isArray(set))
        throw new HttpError(400, 'invalid', 'Set from 1 to 100 preferences.')
      const entries = Object.entries(set as Record<string, unknown>)
      if (entries.length < 1 || entries.length > 100)
        throw new HttpError(400, 'invalid', 'Set from 1 to 100 preferences.')
      const mine = prefs.get(userId) ?? {}
      for (const [k, v] of entries) {
        if (!KEY_RE.test(k) || k.includes('..'))
          throw new HttpError(400, 'invalid', "A preference's name is up to 120 letters, digits and . _ : / -.")
        if (JSON.stringify(v ?? null).length > 32 << 10)
          throw new HttpError(400, 'invalid', 'A preference is at most 32 KiB.')
        if (v === null) delete mine[k]
        else mine[k] = v
      }
      prefs.set(userId, mine)
      return { personal: true }
    },
  }
}

export type NotifyBook = ReturnType<typeof createNotifyBook>
