// The copy of one space on this device: an IndexedDB database per account and space, so another account that
// signs in on the same browser sees nothing of it. Nothing here is encrypted (the offline help says so).
//
//   meta     small values by key: config, sync state, the last tree and tags, the favourites, the bytes of text
//   catalog  one light summary per page of the space (docs/api.md, "Catalogue entry")
//   pages    the pages whose text is kept: the page as the server gave it, with the caller's draft
//   textmeta the rev and size of each page kept, so nothing reads whole pages to know what it has
//   opened    the pages opened most recently (at most RECENT_KEPT), for the default scope
//   walk      the ids a walk of the catalogue that has not finished has seen, so a walk that is cut off goes on

import type { CatalogEntry, Page, TagsResponse, TreeResponse } from '@/api/types'
import { committed, deleteDb, eachRecord, openDb, req } from './idb'
import { dbName, dropEntry, setEntry } from './registry'

export const RECENT_KEPT = 200
// The one layout of a copy (see open). Until the first release a change of layout changes the stores made there,
// not this number: a copy of another version is not converted.
const DB_VERSION = 2

export type OfflineMode = 'catalog' | 'text'

export interface OfflineConfig {
  // `catalog`: the catalogue only; `text`: the catalogue and the text of what the scope says.
  mode: OfflineMode
  // The scope of `text`: folders (and below) and tags that are always kept, besides these three defaults.
  folders: string[]
  tags: string[]
  favourites: boolean
  inbox: boolean
  recent: boolean
}
export const DEFAULT_CONFIG: OfflineConfig = {
  mode: 'catalog',
  folders: [],
  tags: [],
  favourites: true,
  inbox: true,
  recent: true,
}

export interface Favourites {
  pages: string[]
  folders: string[]
}
export interface SyncState {
  // The change cursor the catalogue is at; "" before the first walk has finished.
  cursor: string
  tree: string
  settings: string
  syncedAt: number
  // The walk of the catalogue has finished at least once.
  complete: boolean
  // The space was switched off: the copy stays, read only, and nothing is asked until it is back in the heads.
  disabled: boolean
  // The space's inbox folder and the person's favourites, as the last sync saw them.
  inbox: string
  favourites: Favourites
  // The text wanted did not all fit under the cap on this device.
  overCap: boolean
  // What went wrong the last time, for the settings to say.
  error: string
}
export const EMPTY_STATE: SyncState = {
  cursor: '',
  tree: '',
  settings: '',
  syncedAt: 0,
  complete: false,
  disabled: false,
  inbox: '',
  favourites: { pages: [], folders: [] },
  overCap: false,
  error: '',
}

export interface WalkProgress {
  changes: string
  next: string
  seen: number
}

export interface PageRecord {
  id: string
  rev: string
  bytes: number
  page: Page
}
export interface TextMeta {
  id: string
  rev: string
  bytes: number
}
export interface Opened {
  id: string
  at: number
}

// UTF-8 bytes of a string, without making a buffer of it.
export function utf8Length(s: string): number {
  let n = 0
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i)
    if (c < 0x80) n += 1
    else if (c < 0x800) n += 2
    else if (c >= 0xd800 && c <= 0xdbff) {
      n += 4
      i++
    } else n += 3
  }
  return n
}
// What a page costs on the device: its text, header and draft.
export const pageBytes = (p: Page): number =>
  utf8Length(p.body) + utf8Length(p.header ?? '') + (p.draft ? utf8Length(p.draft.body) : 0) + 400

// The copy was closed (cleared, signed out, deleted from another tab) while something was still using it.
export class CopyClosedError extends Error {
  constructor() {
    super('the offline copy was closed')
    this.name = 'CopyClosedError'
  }
}

export class OfflineStore {
  private cache: CatalogEntry[] | null = null
  readonly user: string
  readonly space: string
  private db: IDBDatabase
  closed = false
  // Aborted when the copy is closed: the requests of a sync under way stop with it.
  private stop = new AbortController()
  // The work under way on the copy (a sync), which closing it waits out.
  private work = new Set<Promise<unknown>>()

  private constructor(user: string, space: string, db: IDBDatabase) {
    this.user = user
    this.space = space
    this.db = db
    // Another tab deleting the database (signing out there) asks this connection to go away.
    db.onversionchange = () => this.close()
  }

  // `create: false` opens only a copy that is there: a database that is not is left unmade, and the open fails.
  static async open(user: string, space: string, create = true): Promise<OfflineStore> {
    const db = await openDb(dbName(user, space), DB_VERSION, (d, old, tx) => {
      // The stores are made only in a new database: one of another version is not converted, and does not open.
      if (old !== 0 || !create) return tx.abort()
      d.createObjectStore('meta', { keyPath: 'key' })
      d.createObjectStore('catalog', { keyPath: 'id' })
      d.createObjectStore('pages', { keyPath: 'id' })
      d.createObjectStore('textmeta', { keyPath: 'id' })
      d.createObjectStore('opened', { keyPath: 'id' }).createIndex('at', 'at')
      d.createObjectStore('walk', { keyPath: 'id' })
    })
    return new OfflineStore(user, space, db)
  }

  // Aborts when the copy is closed.
  get signal(): AbortSignal {
    return this.stop.signal
  }

  // Runs work on the copy that closing it stops (through `signal`) and waits out. A copy already closed runs nothing.
  async busy<T>(fn: () => Promise<T>): Promise<T> {
    if (this.closed) throw new CopyClosedError()
    const p = fn()
    this.work.add(p)
    try {
      return await p
    } finally {
      this.work.delete(p)
    }
  }

  // Nothing is asked of the database from now on: what is under way stops at its next step. Transactions already
  // started still commit (IndexedDB closes the connection after them).
  close() {
    if (this.closed) return
    this.closed = true
    this.stop.abort()
    this.db.close()
  }

  // Closes the copy and waits until the work under way on it has ended, so nothing writes to it afterwards.
  async closeAndWait() {
    this.close()
    await Promise.allSettled([...this.work])
  }

  // Deletes the copy: the database, and its line in the registry, once nothing is writing to it any more.
  async destroy() {
    await this.closeAndWait()
    this.cache = null
    await deleteDb(dbName(this.user, this.space))
    dropEntry(this.user, this.space)
  }

  private tx(names: string | string[], mode?: IDBTransactionMode): IDBTransaction {
    if (this.closed) throw new CopyClosedError()
    return this.db.transaction(names, mode)
  }

  private async get<T>(key: string): Promise<T | undefined> {
    const r = await req(this.tx('meta').objectStore('meta').get(key))
    return (r as { value: T } | undefined)?.value
  }
  private async put(key: string, value: unknown) {
    const tx = this.tx('meta', 'readwrite')
    tx.objectStore('meta').put({ key, value })
    await committed(tx)
  }

  async config(): Promise<OfflineConfig | undefined> {
    const c = await this.get<Partial<OfflineConfig>>('config')
    return c ? { ...DEFAULT_CONFIG, ...c } : undefined
  }
  setConfig(c: OfflineConfig) {
    return this.put('config', c)
  }
  async state(): Promise<SyncState> {
    return { ...EMPTY_STATE, ...(await this.get<Partial<SyncState>>('state')) }
  }
  async setState(patch: Partial<SyncState>) {
    await this.put('state', { ...(await this.state()), ...patch })
  }
  tree() {
    return this.get<TreeResponse>('tree')
  }
  setTree(t: TreeResponse) {
    return this.put('tree', t)
  }
  tags() {
    return this.get<TagsResponse>('tags')
  }
  setTags(t: TagsResponse) {
    return this.put('tags', t)
  }
  async bytes(): Promise<number> {
    return (await this.get<number>('bytes')) ?? 0
  }

  // --- the catalogue -------------------------------------------------------------

  // The whole catalogue, in memory until the next write: a list, a search and the tree are asked of it many times.
  async catalog(): Promise<CatalogEntry[]> {
    if (this.cache) return this.cache
    const out: CatalogEntry[] = []
    await eachRecord<CatalogEntry>(this.tx('catalog').objectStore('catalog'), (e) => void out.push(e))
    return (this.cache = out)
  }
  async catalogCount(): Promise<number> {
    return req(this.tx('catalog').objectStore('catalog').count())
  }
  async catalogEntry(id: string): Promise<CatalogEntry | undefined> {
    return req(this.tx('catalog').objectStore('catalog').get(id))
  }
  async catalogIds(): Promise<string[]> {
    return req(this.tx('catalog').objectStore('catalog').getAllKeys() as IDBRequest<string[]>)
  }

  // Puts and removes catalogue entries; a page removed loses its text. Its mark of being opened stays (the marks are
  // bounded, and only pages in the catalogue are kept for it): a page that comes back, as one in a folder that moved
  // can when a poll falls between two writes, is still among the recent ones. The sync
  // state (the cursor) is written in the same transaction, so a catalogue and its cursor are never apart.
  async applyCatalog(upserts: CatalogEntry[], removed: string[], state?: Partial<SyncState>, endWalk = false) {
    const cur = state ? { ...(await this.state()), ...state } : undefined
    const names = ['catalog', 'pages', 'textmeta', 'meta', ...(endWalk ? ['walk'] : [])]
    const tx = this.tx(names, 'readwrite')
    const cat = tx.objectStore('catalog')
    for (const e of upserts) cat.put(e)
    let delta = 0
    for (const id of removed) {
      cat.delete(id)
      const tm = (await req(tx.objectStore('textmeta').get(id))) as TextMeta | undefined
      if (tm) {
        delta -= tm.bytes
        tx.objectStore('textmeta').delete(id)
        tx.objectStore('pages').delete(id)
      }
    }
    if (delta !== 0) await this.addBytes(tx, delta)
    if (cur) tx.objectStore('meta').put({ key: 'state', value: cur })
    if (endWalk) {
      tx.objectStore('walk').clear()
      tx.objectStore('meta').delete('walk')
    }
    await committed(tx)
    this.cache = null
    await this.refreshRegistry()
  }

  // Removes catalogue entries that are not among `keep` (the end of a walk that started again), with their text.
  async dropCatalogExcept(keep: Set<string>): Promise<number> {
    const gone = (await this.catalogIds()).filter((id) => !keep.has(id))
    if (gone.length) await this.applyCatalog([], gone)
    return gone.length
  }

  // --- a walk of the catalogue -------------------------------------------------------

  // Where a walk that has not finished is: the change cursor of its first batch, the cursor of the next batch
  // ("" when every batch is in) and how many pages it has seen. Undefined when no walk is under way.
  walkProgress() {
    return this.get<WalkProgress>('walk')
  }
  // A walk from the first batch: what an earlier one saw is forgotten.
  async startWalk() {
    const tx = this.tx(['walk', 'meta'], 'readwrite')
    tx.objectStore('walk').clear()
    tx.objectStore('meta').delete('walk')
    await committed(tx)
  }
  // One batch of a walk, with where the walk is now, in one step: a walk cut off after it goes on from the next.
  async applyWalkBatch(items: CatalogEntry[], progress: WalkProgress) {
    const tx = this.tx(['catalog', 'walk', 'meta'], 'readwrite')
    const cat = tx.objectStore('catalog')
    const walk = tx.objectStore('walk')
    for (const e of items) {
      cat.put(e)
      walk.put({ id: e.id })
    }
    tx.objectStore('meta').put({ key: 'walk', value: progress })
    await committed(tx)
    this.cache = null
    await this.refreshRegistry()
  }
  // The end of a walk: what it did not see is gone (with its text), and the catalogue takes the walk's cursor, in
  // one step with forgetting the walk.
  async finishWalk(state: Partial<SyncState>) {
    const seen = new Set(await req(this.tx('walk').objectStore('walk').getAllKeys() as IDBRequest<string[]>))
    const gone = (await this.catalogIds()).filter((id) => !seen.has(id))
    await this.applyCatalog([], gone, state, true)
  }

  // --- the text ------------------------------------------------------------------

  private async addBytes(tx: IDBTransaction, delta: number) {
    const cur = ((await req(tx.objectStore('meta').get('bytes'))) as { value: number } | undefined)?.value ?? 0
    tx.objectStore('meta').put({ key: 'bytes', value: Math.max(0, cur + delta) })
  }

  async page(id: string): Promise<PageRecord | undefined> {
    return req(this.tx('pages').objectStore('pages').get(id))
  }
  async textMetas(): Promise<TextMeta[]> {
    return req(this.tx('textmeta').objectStore('textmeta').getAll())
  }
  async textCount(): Promise<number> {
    return req(this.tx('textmeta').objectStore('textmeta').count())
  }

  async putPages(pages: Page[]) {
    if (!pages.length) return
    const tx = this.tx(['pages', 'textmeta', 'meta'], 'readwrite')
    let delta = 0
    for (const p of pages) {
      const bytes = pageBytes(p)
      const old = (await req(tx.objectStore('textmeta').get(p.id))) as TextMeta | undefined
      delta += bytes - (old?.bytes ?? 0)
      tx.objectStore('pages').put({ id: p.id, rev: p.rev, bytes, page: p } satisfies PageRecord)
      tx.objectStore('textmeta').put({ id: p.id, rev: p.rev, bytes } satisfies TextMeta)
    }
    await this.addBytes(tx, delta)
    await committed(tx)
    await this.refreshRegistry()
  }

  async deletePages(ids: string[]) {
    if (!ids.length) return
    const tx = this.tx(['pages', 'textmeta', 'meta'], 'readwrite')
    let delta = 0
    for (const id of ids) {
      const old = (await req(tx.objectStore('textmeta').get(id))) as TextMeta | undefined
      if (!old) continue
      delta -= old.bytes
      tx.objectStore('pages').delete(id)
      tx.objectStore('textmeta').delete(id)
    }
    await this.addBytes(tx, delta)
    await committed(tx)
    await this.refreshRegistry()
  }

  // The text of every page, for a search in what is here: one page at a time.
  async eachPage(fn: (p: PageRecord) => void | false): Promise<void> {
    await eachRecord<PageRecord>(this.tx('pages').objectStore('pages'), fn)
  }

  // --- the pages opened ----------------------------------------------------------

  async touchOpened(id: string, at: number) {
    const tx = this.tx('opened', 'readwrite')
    const os = tx.objectStore('opened')
    os.put({ id, at } satisfies Opened)
    const n = await req(os.count())
    if (n > RECENT_KEPT) {
      // The oldest go.
      let over = n - RECENT_KEPT
      await new Promise<void>((resolve, reject) => {
        const r = os.index('at').openCursor()
        r.onsuccess = () => {
          const c = r.result
          if (!c || over <= 0) return resolve()
          c.delete()
          over--
          c.continue()
        }
        r.onerror = () => reject(r.error)
      })
    }
    await committed(tx)
  }
  async opened(): Promise<Opened[]> {
    const all = (await req(this.tx('opened').objectStore('opened').getAll())) as Opened[]
    return all.sort((a, b) => b.at - a.at)
  }

  // --- what is kept ---------------------------------------------------------------

  async refreshRegistry() {
    if (this.closed) return
    const [bytes, texts, catalog] = await Promise.all([this.bytes(), this.textCount(), this.catalogCount()])
    // Closed while it counted (the copy was cleared): its line in the registry is not written back.
    if (this.closed) return
    setEntry(this.user, this.space, { bytes, texts, catalog, at: Date.now() })
  }
}
