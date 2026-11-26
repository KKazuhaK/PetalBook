// The offline copy's feed: a catalogue of the space, the changes since a cursor, the pages by id, and the cursor each
// space is at. A cursor is `epoch-seq`; a reindex starts a new epoch, so an older cursor is 410 `changes_expired`.
// Like the real index, the feed numbers what differs from what it saw last, whenever it is read.

import type { CatalogEntry, ChangeItem } from '../../types.ts'
import type { SpaceData, StoredPage } from '../data/model.ts'
import { hash, Refusal, type Route } from '../kit.ts'
import { draftView, pageOf, pageView, revOf, spaceOf, summary } from './files.ts'

const catalogOf = (s: SpaceData, p: StoredPage): CatalogEntry => {
  const x = summary(s, p)
  return {
    id: x.id,
    path: x.path,
    folder: x.folder,
    ...(x.title ? { title: x.title } : {}),
    label: x.label ?? '',
    untitled: x.untitled,
    tags: x.tags ?? [],
    type: x.type ?? '',
    ...(x.created ? { created: x.created } : {}),
    updated: x.updated,
    rev: revOf(p),
  }
}

// Numbers what changed since the last look, and returns the entries as they are now.
function sweep(s: SpaceData): Map<string, CatalogEntry> {
  const f = s.feed
  const now = new Map(s.pages.map((p) => [p.id, catalogOf(s, p)]))
  const fp = (e: CatalogEntry) => JSON.stringify(e)
  const seen = f.seen
  for (const [id, e] of now) {
    if (seen[id]?.fp !== fp(e)) {
      seen[id] = { fp: fp(e), seq: ++f.seq }
      f.gone = f.gone.filter((g) => g.id !== id)
    }
  }
  for (const id of Object.keys(seen))
    if (!now.has(id)) {
      delete seen[id]
      f.gone.push({ id, seq: ++f.seq })
    }
  return now
}

const cursorOf = (s: SpaceData, n: number) => `${s.feed.epoch}-${n}`
export const feedHead = (s: SpaceData) => (sweep(s), cursorOf(s, s.feed.seq))
const treeToken = (s: SpaceData) =>
  `${s.feed.epoch}.${hash(JSON.stringify([...s.folders].sort().map((f) => [f, s.pages.filter((p) => p.folder === f).length]))).slice(0, 8)}`
const settingsToken = (s: SpaceData) =>
  hash(JSON.stringify([s.types, s.bindings, s.templates, s.defaults, s.tagColors])).slice(0, 12)

const expired = () => new Refusal(410, 'changes_expired', 'the change cursor is too old; read the catalogue again')
const inFolders = (folder: string, folders: string[]) =>
  folders.length === 0 || folders.some((f) => folder === f || folder.startsWith(f + '/'))

export const syncRoutes: Route[] = [
  [
    'GET',
    '/api/sync/heads',
    (c) => {
      const heads: Record<string, string> = {}
      const missing: string[] = []
      for (const id of c.query.getAll('space')) {
        const s = c.data.spaces.find((x) => x.info.id === id)
        if (s) heads[id] = feedHead(s)
        else missing.push(id)
      }
      return { heads, ...(missing.length ? { missing } : {}) }
    },
  ],
  [
    'GET',
    '/api/spaces/:space/changes',
    (c) => {
      const s = spaceOf(c)
      sweep(s)
      const m = /^([0-9a-f]{8})-(\d+)$/.exec(c.query.get('since') ?? '')
      if (!m || m[1] !== s.feed.epoch || Number(m[2]) < s.feed.floor || Number(m[2]) > s.feed.seq) throw expired()
      const since = Number(m[2])
      const limit = Number(c.query.get('limit')) || 1000
      const seen = s.feed.seen
      const all: { seq: number; item: ChangeItem }[] = [
        ...s.pages
          .filter((p) => seen[p.id].seq > since)
          .map((p) => ({ seq: seen[p.id].seq, item: catalogOf(s, p) as ChangeItem })),
        ...s.feed.gone
          .filter((g) => g.seq > since)
          .map((g) => ({ seq: g.seq, item: { id: g.id, deleted: true } as ChangeItem })),
      ].sort((a, b) => a.seq - b.seq)
      const items = all.slice(0, limit)
      const more = all.length > limit
      return {
        items: items.map((i) => i.item),
        next: more ? cursorOf(s, items[items.length - 1].seq) : cursorOf(s, s.feed.seq),
        more,
        tree: treeToken(s),
        settings: settingsToken(s),
      }
    },
  ],
  [
    'GET',
    '/api/spaces/:space/sync/catalog',
    (c) => {
      const s = spaceOf(c)
      const entries = sweep(s)
      const limit = Number(c.query.get('limit')) || 2000
      const scope = c.query.getAll('folder').map((f) => f.replace(/^\/+|\/+$/g, ''))
      const folders = scope.includes('') ? [] : [...new Set(scope)].sort()
      const key = hash(folders.join('\0')).slice(0, 8)
      const cursor = c.query.get('cursor')
      let from = 0
      if (cursor) {
        const m = /^([0-9a-f]{8})\.(\d+)\.([0-9a-f]{8})$/.exec(cursor)
        if (!m || m[3] !== key) throw new Refusal(400, 'invalid', 'not a cursor this server made for these parameters')
        if (m[1] !== s.feed.epoch) throw expired()
        from = Number(m[2])
      }
      const all = [...entries.values()].filter((e) => inFolders(e.folder, folders))
      const items = all.slice(from, from + limit)
      const end = from + items.length
      return {
        items,
        next: end < all.length ? `${s.feed.epoch}.${end}.${key}` : '',
        ...(cursor ? {} : { changes: cursorOf(s, s.feed.seq) }),
        tree: treeToken(s),
        settings: settingsToken(s),
      }
    },
  ],
  [
    'GET',
    '/api/spaces/:space/sync/pages',
    (c) => {
      const s = spaceOf(c)
      const ids = (c.query.get('ids') ?? '').split(',').filter(Boolean)
      const missing = ids.filter((id) => !s.pages.some((p) => p.id === id))
      const pages = ids
        .filter((id) => !missing.includes(id))
        .map((id) => {
          const p = pageOf(s, id)
          const draft = draftView(s, p)
          return { ...pageView(s, p), ...(draft ? { draft } : {}) }
        })
      return { pages, missing }
    },
  ],
  [
    'POST',
    '/api/spaces/:space/reindex',
    (c) => {
      const s = spaceOf(c)
      s.feed = { epoch: hash(`epoch ${++c.data.counter}`).slice(0, 8), seq: 0, floor: 0, seen: {}, gone: [] }
      sweep(s)
      return { pages: s.pages.length, ms: 0 }
    },
  ],
]
