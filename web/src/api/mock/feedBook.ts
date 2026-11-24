// The change feed of one mock space (docs/api.md, "Offline sync"). The real server gives every write of a page a
// sequence number in its index; the mock has no index, so it looks at the pages whenever the feed is read and
// numbers what differs from what it saw last time. The answers have the real shapes: cursors, 410 when the epoch
// changed (a reindex) or when the removals the mock keeps do not reach back, the removal records, folders of a
// catalogue, the 8 MiB-style cut of a batch of pages (by a size given to the book).

import type { CatalogEntry, ChangeItem } from '../types.ts'
import { gitBlobId } from './sha1.ts'

const GONE_MAX = 100_000
const EPOCH_RE = /^[0-9a-f]{8}$/

export interface FeedOptions {
  // The pages as they are now, in the order the space holds them.
  entries: () => CatalogEntry[]
  // Change when a folder, or the number of pages in one, changes; when a settings file does.
  tree: () => string
  settings: () => string
  random: () => number
  // The error an answer of the server is (the store's HttpError).
  fail: (status: number, code: string, message: string) => Error
}

export function createFeedBook({ entries, tree, settings, random, fail }: FeedOptions) {
  const bad = (m: string) => fail(400, 'invalid', m)
  const expired = () => fail(410, 'changes_expired', 'the change cursor is too old; read the catalogue again')
  const newEpoch = () => Array.from({ length: 8 }, () => '0123456789abcdef'[Math.floor(random() * 16)]).join('')
  let epoch = newEpoch()
  let seq = 0
  let floor = 0
  const known = new Map<string, { fp: string; seq: number; entry: CatalogEntry }>()
  let gone: { id: string; seq: number }[] = []

  // Numbers what changed since the last look.
  const sweep = () => {
    const now = new Map(entries().map((e) => [e.id, e]))
    for (const [id, e] of now) {
      const fp = JSON.stringify(e)
      const k = known.get(id)
      if (!k || k.fp !== fp) {
        known.set(id, { fp, seq: ++seq, entry: e })
        gone = gone.filter((g) => g.id !== id)
      }
    }
    for (const id of [...known.keys()]) {
      if (!now.has(id)) {
        known.delete(id)
        gone.push({ id, seq: ++seq })
      }
    }
    if (gone.length > GONE_MAX) {
      const dropped = gone.splice(0, gone.length - GONE_MAX)
      floor = Math.max(floor, dropped[dropped.length - 1].seq)
    }
  }

  const cursorOf = (s: number) => `${epoch}-${s}`
  const head = () => {
    sweep()
    return cursorOf(seq)
  }
  const treeToken = () => `${epoch}.${gitBlobId(tree()).slice(0, 8)}`

  const parseSince = (since: string | null): number => {
    if (since === null || since === '') throw bad('since is required: a change cursor')
    const m = /^([0-9a-f]{8})-(\d{1,18})$/.exec(since)
    if (!m || !EPOCH_RE.test(m[1])) throw bad('not a cursor this server made')
    const n = Number(m[2])
    if (m[1] !== epoch || n < floor || n > seq) throw expired()
    return n
  }

  const limitOf = (v: string | null, def: number, max: number): number => {
    if (v === null || v === '') return def
    const n = Number(v)
    if (!/^\d+$/.test(v) || n < 1 || n > max) throw bad(`limit must be a number from 1 to ${max}`)
    return n
  }

  const inFolders = (folder: string, folders: string[]) =>
    folders.length === 0 || folders.some((f) => folder === f || folder.startsWith(f + '/'))

  return {
    head,
    reindex: () => {
      epoch = newEpoch()
      gone = []
      floor = 0
      known.clear()
      sweep()
    },
    changes: (q: URLSearchParams) => {
      sweep()
      const since = parseSince(q.get('since'))
      const limit = limitOf(q.get('limit'), 1000, 5000)
      const all: { seq: number; item: ChangeItem }[] = [
        ...[...known.values()].filter((k) => k.seq > since).map((k) => ({ seq: k.seq, item: k.entry as ChangeItem })),
        ...gone
          .filter((g) => g.seq > since)
          .map((g) => ({ seq: g.seq, item: { id: g.id, deleted: true } as ChangeItem })),
      ].sort((a, b) => a.seq - b.seq)
      const more = all.length > limit
      const items = all.slice(0, limit)
      return {
        items: items.map((i) => i.item),
        next: more ? cursorOf(items[items.length - 1].seq) : cursorOf(seq),
        more,
        tree: treeToken(),
        settings: settings(),
      }
    },
    catalog: (q: URLSearchParams) => {
      sweep()
      const limit = limitOf(q.get('limit'), 2000, 5000)
      const folders = q.getAll('folder').map((f) => f.replace(/^\/+|\/+$/g, ''))
      if (folders.length > 50) throw bad('at most 50 folders')
      const scope = folders.some((f) => f === '') ? [] : [...new Set(folders)].sort()
      const key = gitBlobId(scope.join('\0')).slice(0, 8)
      const cursor = q.get('cursor')
      let from = 0
      if (cursor) {
        const m = /^([0-9a-f]{8})\.(\d{1,12})\.([0-9a-f]{8})$/.exec(cursor)
        if (!m || m[3] !== key) throw bad('not a cursor this server made, or one made for other parameters')
        if (m[1] !== epoch) throw expired()
        from = Number(m[2])
      }
      const all = entries().filter((e) => inFolders(e.folder, scope))
      const items = all.slice(from, from + limit)
      const end = from + items.length
      return {
        items,
        next: end < all.length ? `${epoch}.${end}.${key}` : '',
        ...(cursor ? {} : { changes: cursorOf(seq) }),
        tree: treeToken(),
        settings: settings(),
      }
    },
  }
}

// How many bytes of pages one answer of sync/pages may hold (8 MiB on the real server).
export const SYNC_PAGES_BYTES = 8 << 20
