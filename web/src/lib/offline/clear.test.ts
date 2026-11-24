import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { DEFAULT_CONFIG, type OfflineConfig } from './store'
import { clearAccount, configure, dropCopy, existingStore, hasCopy } from './manager'
import { syncNow } from './poller'
import { syncSpace } from './sync'
import { make, network, type Net } from './testkit'
import { setEntry } from './registry'

// A copy cleared (or an account signed out) while a sync of it is half done: the sync stops, writes nothing into
// the closed or deleted database and nothing back into the registry, and ends with an outcome, not a rejection.

let net: Net
beforeEach(() => {
  net = network()
})
afterEach(() => net.restore())

// Holds the first request whose URL matches until it is released; a request that is aborted meanwhile fails as
// fetch does.
function hold(pattern: RegExp) {
  const inner = globalThis.fetch
  let arrived!: () => void
  const reached = new Promise<void>((r) => (arrived = r))
  let release!: () => void
  const released = new Promise<void>((r) => (release = r))
  let used = false
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    if (!used && pattern.test(url)) {
      used = true
      arrived()
      const signal = init?.signal
      await new Promise<void>((resolve, reject) => {
        void released.then(resolve)
        if (signal?.aborted) reject(new DOMException('aborted', 'AbortError'))
        signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
      })
    }
    return inner(input, init)
  }) as typeof fetch
  return { reached, release }
}

const dbs = async () =>
  (await indexedDB.databases()).map((d) => d.name).filter((n) => n?.startsWith('petalbook-offline'))
const cfg = (over: Partial<OfflineConfig> = {}): OfflineConfig => ({ ...DEFAULT_CONFIG, ...over })

describe('clearing a copy while it syncs', () => {
  it('the catalogue walk is held, the copy cleared, then the answer comes: the sync ends quietly and the copy stays gone', async () => {
    await make('', '甲')
    await configure('local', 'default', cfg())
    const h = hold(/sync\/catalog/)
    const sync = syncNow('local', 'default')
    await h.reached
    const drop = dropCopy('local', 'default')
    h.release()
    await drop
    const r = await sync
    expect(r.outcome).not.toBe('error')
    expect(hasCopy('local', 'default')).toBe(false)
    expect(await dbs()).toEqual([])
    expect(await existingStore('local', 'default')).toBeUndefined()
  })

  it('the text is held, the copy chosen as nothing: the same', async () => {
    await make('', '甲')
    const s = (await configure('local', 'default', cfg({ mode: 'text', folders: [''] })))!
    const h = hold(/sync\/pages/)
    const sync = syncSpace(s)
    await h.reached
    const drop = configure('local', 'default', null)
    h.release()
    await drop
    const r = await sync
    expect(r.outcome).not.toBe('error')
    expect(hasCopy('local', 'default')).toBe(false)
    expect(await dbs()).toEqual([])
  })

  it('signing out while a sync is held: the same, and clearing waits for the sync to stop', async () => {
    await make('', '甲')
    await configure('local', 'default', cfg())
    const h = hold(/sync\/catalog/)
    let ended = false
    const sync = syncNow('local', 'default').finally(() => void (ended = true))
    await h.reached
    // Not released: clearing stops the request itself.
    await clearAccount('local')
    expect(ended).toBe(true)
    const r = await sync
    expect(r.outcome).not.toBe('error')
    expect(hasCopy('local', 'default')).toBe(false)
    expect(await dbs()).toEqual([])
    h.release()
  })

  it('a copy chosen again after the clear is a fresh one, untouched by the sync that was stopped', async () => {
    await make('', '甲')
    await configure('local', 'default', cfg())
    const h = hold(/sync\/catalog/)
    const sync = syncNow('local', 'default')
    await h.reached
    await dropCopy('local', 'default')
    const again = (await configure('local', 'default', cfg({ mode: 'text' })))!
    h.release()
    expect((await sync).outcome).not.toBe('error')
    expect(hasCopy('local', 'default')).toBe(true)
    expect(await again.catalogCount()).toBe(0)
    expect((await again.config())!.mode).toBe('text')
    expect((await again.state()).error).toBe('')
  })

  it('opening a copy that is not there (cleared while it was being opened) makes no database', async () => {
    setEntry('local', 'default', { bytes: 0, texts: 0, catalog: 0, at: 0 })
    expect(await existingStore('local', 'default')).toBeUndefined()
    expect(await dbs()).toEqual([])
  })
})

// A choice made while a sync runs must not be lost: the sync under way read the choice before it, so the choice is
// synced once that sync has ended.
describe('choosing while a sync runs', () => {
  it('a folder chosen while the first sync is held has its page kept when that sync ends', async () => {
    await make('选定', '甲')
    const none = { mode: 'text' as const, folders: [], tags: [], favourites: false, inbox: false, recent: false }
    const s = (await configure('local', 'default', cfg(none)))!
    const h = hold(/sync\/catalog/)
    const first = syncNow('local', 'default')
    await h.reached
    await configure('local', 'default', cfg({ ...none, folders: ['选定'] }))
    // What the choice's own sync does: it finds one under way.
    const second = await syncNow('local', 'default')
    expect(second.outcome).toBe('ok')
    h.release()
    await first
    expect(await s.textCount()).toBe(1)
  })
})
