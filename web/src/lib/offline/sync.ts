// Bringing the copy of a space up to date (docs/api.md, "Offline sync"): walk the catalogue once, then ask only what
// changed after the cursor; fetch the text of what the scope wants and the device has room for. A catalogue and its
// cursor are written together, so a sync that stops anywhere is resumed from where the last one ended.

import { api, ApiError, spaceApi } from '@/api/client'
import { isRemoved, type CatalogEntry, type Page } from '@/api/types'
import { wantedText } from './scope'
import { OfflineStore, pageBytes } from './store'
import { totalBytes } from './registry'

// What this device keeps of text, over every account (docs/design.md, 4.1).
export const DEFAULT_CAP_BYTES = 200 * 1024 * 1024
const CATALOG_BATCH = 2000
const CHANGES_BATCH = 1000
const TEXT_BATCH = 100
// Bodies are fetched at most this many requests at a time.
const TEXT_PARALLEL = 4

// 'off': the server says offline copies are turned off for this account's tenant (403 offline_off).
export type Outcome = 'ok' | 'offline' | 'gone' | 'disabled' | 'off' | 'error'
export interface SyncResult {
  outcome: Outcome
  // The catalogue or the text changed: lists on screen are out of date.
  changed: boolean
  message?: string
}
export interface SyncOptions {
  signal?: AbortSignal
  cap?: number
  now?: () => number
  // Called while the catalogue is walked, with the pages seen so far.
  progress?: (seen: number) => void
  // The size of a batch of the catalogue (tests make it small).
  catalogBatch?: number
}

export function classify(e: unknown): { outcome: Outcome; message: string } {
  if (e instanceof ApiError) {
    if (e.status === 404) return { outcome: 'gone', message: e.message }
    if (e.status === 403 && e.code === 'offline_off') return { outcome: 'off', message: e.message }
    if (e.status === 403 && e.code === 'space_disabled') return { outcome: 'disabled', message: e.message }
    if (e.code === 'network' || e.code === 'timeout') return { outcome: 'offline', message: e.message }
    return { outcome: 'error', message: e.message }
  }
  if (e instanceof DOMException && e.name === 'AbortError') return { outcome: 'offline', message: e.message }
  return { outcome: 'error', message: e instanceof Error ? e.message : String(e) }
}

const isExpired = (e: unknown) => e instanceof ApiError && e.status === 410 && e.code === 'changes_expired'

// One sync of one space. It never throws: what went wrong is the outcome. A space that answers 404 is not the
// person's to read any more, and its copy is deleted; one that is switched off keeps its copy, read only.
export async function syncSpace(store: OfflineStore, opts: SyncOptions = {}): Promise<SyncResult> {
  const cfg = await store.config()
  if (!cfg) return { outcome: 'ok', changed: false }
  let changed = false
  try {
    changed = await run(store, cfg, opts)
    await store.setState({ error: '', disabled: false, syncedAt: (opts.now ?? Date.now)() })
    return { outcome: 'ok', changed }
  } catch (e) {
    const c = classify(e)
    if (c.outcome === 'gone') {
      await store.destroy()
    } else if (c.outcome === 'disabled') {
      await store.setState({ disabled: true, error: '' })
    } else if (c.outcome === 'error') {
      await store.setState({ error: c.message })
    }
    return { outcome: c.outcome, changed, message: c.message }
  }
}

async function run(store: OfflineStore, cfg: Awaited<ReturnType<OfflineStore['config']>> & object, opts: SyncOptions): Promise<boolean> {
  const sp = spaceApi(store.space)
  const signal = opts.signal
  let state = await store.state()
  let changed = false

  // The catalogue, from the first batch's cursor on. Each batch is written with where the walk is, so a walk that
  // is cut off (the network, a closed tab) goes on from its next batch; one whose cursor the server no longer
  // takes (the index was built again) starts from the first.
  const limit = opts.catalogBatch ?? CATALOG_BATCH
  const walk = async (fresh: boolean) => {
    let p = fresh ? undefined : await store.walkProgress()
    let batch: Awaited<ReturnType<typeof sp.syncCatalog>> | undefined
    if (p?.next) {
      try {
        batch = await sp.syncCatalog({ limit, cursor: p.next }, signal)
      } catch (e) {
        if (!isExpired(e) && !(e instanceof ApiError && e.status === 400)) throw e
        p = undefined
      }
    }
    if (!p) {
      await store.startWalk()
      batch = await sp.syncCatalog({ limit }, signal)
      p = { changes: batch.changes ?? '', next: '', seen: 0 }
    }
    while (batch) {
      p = { ...p, next: batch.next, seen: p.seen + batch.items.length }
      await store.applyWalkBatch(batch.items, p)
      opts.progress?.(p.seen)
      batch = batch.next ? await sp.syncCatalog({ limit, cursor: batch.next }, signal) : undefined
    }
    // What the catalogue has no more is gone; text with the same rev as before stays, only the rest is fetched.
    // The tree and the tags are read again by the catch-up below (their stamps are unknown now).
    await store.finishWalk({ cursor: p.changes, tree: '', settings: '', complete: true })
    state = await store.state()
    changed = true
  }
  // A walk under way (the first, or one after a 410) goes on before anything else.
  if (!state.cursor || (await store.walkProgress())) await walk(false)

  for (let guard = 0; ; guard++) {
    if (guard > 10_000) throw new Error('the changes never ended')
    let r
    try {
      r = await sp.changes(state.cursor, CHANGES_BATCH, signal)
    } catch (e) {
      if (!isExpired(e)) throw e
      await walk(true)
      continue
    }
    const ups = r.items.filter((i): i is CatalogEntry => !isRemoved(i))
    // A page is in an answer once; were it in as both, it is there.
    const here = new Set(ups.map((e) => e.id))
    const removed = r.items.filter(isRemoved).map((i) => i.id).filter((id) => !here.has(id))
    if (ups.length || removed.length) {
      changed = true
      await store.applyCatalog(ups, removed, { cursor: r.next })
    } else if (r.next !== state.cursor) {
      await store.setState({ cursor: r.next })
    }
    state = await store.state()
    if (r.more) continue
    // The folders and the settings: read again what the app shows from them when their stamps moved.
    if (r.tree !== state.tree || r.settings !== state.settings) {
      const [tree, tags] = await Promise.all([sp.tree(signal), sp.tags(signal)])
      await store.setTree(tree)
      await store.setTags(tags)
      await store.setState({ tree: r.tree, settings: r.settings })
      changed = true
    }
    break
  }

  if (cfg.mode === 'text') {
    if (await fillText(store, cfg, opts, changed || state.syncedAt === 0)) changed = true
  } else if ((await store.textCount()) > 0) {
    // From text back to the catalogue only: what was kept goes.
    await store.deletePages((await store.textMetas()).map((m) => m.id))
    changed = true
  }
  return changed
}

// Fetches the text the scope wants and the device has room for; drops what it does not want any more.
async function fillText(store: OfflineStore, cfg: NonNullable<Awaited<ReturnType<OfflineStore['config']>>>, opts: SyncOptions, refreshScope: boolean): Promise<boolean> {
  const sp = spaceApi(store.space)
  const signal = opts.signal
  let state = await store.state()
  if (refreshScope || state.inbox === '') {
    // What the scope is made of besides the catalogue: the person's favourites and the space's inbox.
    const [favs, spaces] = await Promise.all([cfg.favourites ? sp.favorites(signal).catch(() => null) : null, state.inbox === '' || refreshScope ? api.spaces(signal).catch(() => null) : null])
    const patch: Partial<typeof state> = {}
    if (favs) patch.favourites = { pages: favs.items.filter((f) => f.kind === 'page' && !f.missing).map((f) => f.target), folders: favs.items.filter((f) => f.kind === 'folder' && !f.missing).map((f) => f.target) }
    const inbox = spaces?.spaces.find((s) => s.id === store.space)?.inbox
    if (inbox) patch.inbox = inbox
    if (Object.keys(patch).length) {
      await store.setState(patch)
      state = await store.state()
    }
  }
  const catalog = await store.catalog()
  const rev = new Map(catalog.map((e) => [e.id, e.rev]))
  const wanted = wantedText(catalog, cfg, state.inbox, state.favourites, await store.opened())
  const all = new Set([...wanted.pinned, ...wanted.recent])
  const have = new Map((await store.textMetas()).map((m) => [m.id, m]))
  let changed = false
  const unwanted = [...have.keys()].filter((id) => !all.has(id))
  if (unwanted.length) {
    await store.deletePages(unwanted)
    for (const id of unwanted) have.delete(id)
    changed = true
  }
  // Pinned first, then the recent ones, newest first.
  const needed = [...wanted.pinned, ...wanted.recent].filter((id) => have.get(id)?.rev !== rev.get(id))
  const pinned = new Set(wanted.pinned)
  const cap = opts.cap ?? DEFAULT_CAP_BYTES
  let overCap = false

  const queue = [...needed]
  const take = () => queue.splice(0, TEXT_BATCH)
  while (queue.length) {
    const group = Array.from({ length: TEXT_PARALLEL }, take).filter((g) => g.length > 0)
    const answers = await Promise.all(group.map((ids) => sp.syncPages(ids, signal)))
    const got: Page[] = []
    for (const a of answers) {
      got.push(...a.pages)
      if (a.rest?.length) queue.push(...a.rest)
    }
    // Room: what is over the cap makes the oldest recent text go, then the page is left out.
    const room: Page[] = []
    let used = totalBytes()
    for (const p of got) {
      const bytes = pageBytes(p)
      const old = have.get(p.id)?.bytes ?? 0
      while (used - old + bytes > cap) {
        // What was accepted so far is in the store before anything is chosen to go.
        if (room.length) {
          await store.putPages(room)
          room.length = 0
        }
        const victim = await evictable(store, have, pinned, p.id)
        if (!victim) break
        await store.deletePages([victim.id])
        used -= victim.bytes
        have.delete(victim.id)
      }
      if (used - old + bytes > cap) {
        overCap = true
        continue
      }
      used += bytes - old
      room.push(p)
      have.set(p.id, { id: p.id, rev: p.rev, bytes })
    }
    await store.putPages(room)
    if (room.length) changed = true
  }
  await store.setState({ overCap })
  return changed
}

// The text that can go first: kept only because it was opened lately, the longest ago (and before the incoming one).
async function evictable(store: OfflineStore, have: Map<string, { id: string; bytes: number }>, pinned: Set<string>, incoming: string) {
  const opened = new Map((await store.opened()).map((o) => [o.id, o.at]))
  // A page that is only recent never pushes out one opened after it.
  const limit = pinned.has(incoming) ? Infinity : (opened.get(incoming) ?? 0)
  let best: { id: string; bytes: number } | undefined
  let bestAt = Infinity
  for (const m of have.values()) {
    if (pinned.has(m.id) || m.id === incoming) continue
    const at = opened.get(m.id) ?? 0
    if (at >= limit) continue
    if (at < bestAt) {
      best = m
      bestAt = at
    }
  }
  return best
}
