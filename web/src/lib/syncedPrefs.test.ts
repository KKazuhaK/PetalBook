// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createSyncedPrefs, type PrefsApi } from './syncedPrefs'
import { browserPrefs, prefsStore, setPrefsStore } from './prefsStore'
import { readPrefs, writePrefs, defaultPrefs } from './tablePrefs'
import { onboardingKey, readOnboarding, writeOnboarding } from './onboarding'

// The person's own preferences in their personal space, with this browser's storage as the fallback and the cache.

interface Fake extends PrefsApi {
  server: Record<string, unknown>
  personal: boolean
  down: boolean
  gets: number
  patches: Record<string, unknown>[]
}
function fake(prefs: Record<string, unknown> = {}, personal = true): Fake {
  const f: Fake = {
    server: { ...prefs },
    personal,
    down: false,
    gets: 0,
    patches: [],
    async get() {
      f.gets++
      if (f.down) throw new Error('offline')
      return { personal: f.personal, prefs: { ...f.server } }
    },
    async patch(set) {
      if (f.down) throw new Error('offline')
      f.patches.push(set)
      if (f.personal)
        for (const [k, v] of Object.entries(set)) {
          if (v === null) delete f.server[k]
          else f.server[k] = v
        }
      return { personal: f.personal }
    },
  }
  return f
}

beforeEach(() => {
  localStorage.clear()
  vi.useFakeTimers()
})
afterEach(() => {
  vi.useRealTimers()
  setPrefsStore(browserPrefs)
})

describe('reading', () => {
  it('fetches the file once, and then every read is this browser’s copy of it', async () => {
    const api = fake({ 'pb.table.users': { hidden: ['email'] }, other: 3 })
    const p = createSyncedPrefs({ api })
    await p.hydrate('U1')
    expect(api.gets).toBe(1)
    expect(p.read('pb.table.users')).toEqual({ hidden: ['email'] })
    expect(p.read('other')).toBe(3)
    expect(p.read('missing')).toBeNull()
    for (let i = 0; i < 20; i++) p.read('other')
    expect(api.gets).toBe(1)
    expect(p.status()).toEqual({ hydrated: true, personal: true, waiting: 0 })
  })

  it('the file wins over what this browser had, and what the file lost is gone', async () => {
    const api = fake({ a: 'server' })
    const p = createSyncedPrefs({ api })
    await p.hydrate('U1')
    expect(browserPrefs.read('a')).toBe('server')
    // Someone removed `a` on another device: the next session here drops it.
    api.server = {}
    const again = createSyncedPrefs({ api })
    await again.hydrate('U1')
    expect(again.read('a')).toBeNull()
  })

  it('falls back to this browser when there is no personal space', async () => {
    const api = fake({}, false)
    const p = createSyncedPrefs({ api })
    browserPrefs.write('pb.table.users', { hidden: ['x'] })
    await p.hydrate('U1')
    expect(p.status().personal).toBe(false)
    p.write('k', 1)
    p.write('pb.table.users', { hidden: ['y'] })
    await vi.advanceTimersByTimeAsync(60_000)
    expect(api.patches).toEqual([])
    expect(p.read('k')).toBe(1)
    expect(p.read('pb.table.users')).toEqual({ hidden: ['y'] })
  })

  it('falls back to this browser when the file cannot be read (offline), and keeps what is written meanwhile', async () => {
    const api = fake({ a: 'server' })
    api.down = true
    browserPrefs.write('a', 'cached')
    const p = createSyncedPrefs({ api })
    await p.hydrate('U1')
    expect(p.status().hydrated).toBe(false)
    expect(p.read('a')).toBe('cached')
    p.write('b', 2)
    expect(p.read('b')).toBe(2)
    expect(api.patches).toEqual([])
    // Not hydrated: a write waits for it, and goes with the next sign-in.
    api.down = false
    const next = createSyncedPrefs({ api })
    next.write('c', 3)
    await next.hydrate('U1')
    await vi.advanceTimersByTimeAsync(10)
    expect(api.server).toMatchObject({ a: 'server', c: 3 })
  })
})

describe('writing', () => {
  it('batches a burst into one request after a quiet moment', async () => {
    const api = fake()
    const p = createSyncedPrefs({ api, quietMs: 2000 })
    await p.hydrate('U1')
    for (let i = 0; i < 30; i++) {
      p.write('pb.table.users', { pageSize: i })
      await vi.advanceTimersByTimeAsync(100)
    }
    p.write('other', true)
    expect(api.patches).toHaveLength(0)
    expect(p.status().waiting).toBe(2)
    await vi.advanceTimersByTimeAsync(2100)
    expect(api.patches).toEqual([{ 'pb.table.users': { pageSize: 29 }, other: true }])
    expect(p.status().waiting).toBe(0)
  })

  it('never waits longer than the longest wait, however busy the person is', async () => {
    const api = fake()
    const p = createSyncedPrefs({ api, quietMs: 2000, maxWaitMs: 5000 })
    await p.hydrate('U1')
    for (let i = 0; i < 80; i++) {
      p.write('k', i)
      await vi.advanceTimersByTimeAsync(500)
    }
    expect(api.patches.length).toBeGreaterThanOrEqual(6)
    expect(api.patches.length).toBeLessThan(12)
  })

  it('a removal is sent as null', async () => {
    const api = fake({ a: 1, b: 2 })
    const p = createSyncedPrefs({ api, quietMs: 10 })
    await p.hydrate('U1')
    p.remove('a')
    expect(p.read('a')).toBeNull()
    await vi.advanceTimersByTimeAsync(20)
    expect(api.patches).toEqual([{ a: null }])
    expect(api.server).toEqual({ b: 2 })
  })

  it('what could not be sent is tried again, with anything newer on top', async () => {
    const api = fake()
    const p = createSyncedPrefs({ api, quietMs: 10, retryMs: 1000 })
    await p.hydrate('U1')
    api.down = true
    p.write('a', 1)
    await vi.advanceTimersByTimeAsync(20)
    expect(api.patches).toHaveLength(0)
    p.write('a', 2)
    p.write('b', 1)
    api.down = false
    await vi.advanceTimersByTimeAsync(2000)
    expect(api.server).toEqual({ a: 2, b: 1 })
  })

  it('sends what is waiting at once on flush (sign-out), and not twice', async () => {
    const api = fake()
    const p = createSyncedPrefs({ api, quietMs: 60_000 })
    await p.hydrate('U1')
    p.write('a', 1)
    await p.flush()
    await p.flush()
    expect(api.patches).toEqual([{ a: 1 }])
  })

  it('what was written before the person was known goes to their file once it is', async () => {
    const api = fake({ x: 'file' })
    const p = createSyncedPrefs({ api, quietMs: 10 })
    p.write('early', 1)
    p.write('x', 'mine')
    await p.hydrate('U1')
    await vi.advanceTimersByTimeAsync(20)
    expect(api.server).toEqual({ x: 'mine', early: 1 })
  })
})

describe('whose they are', () => {
  it('what this browser copied from one person’s file is gone when another signs in', async () => {
    const a = fake({ secretA: 'a-only', shared: 'a' })
    const pa = createSyncedPrefs({ api: a })
    await pa.hydrate('A')
    expect(browserPrefs.read('secretA')).toBe('a-only')
    await pa.reset()
    expect(pa.status().hydrated).toBe(false)
    const b = fake({ shared: 'b' })
    const pb = createSyncedPrefs({ api: b })
    await pb.hydrate('B')
    expect(pb.read('secretA')).toBeNull()
    expect(pb.read('shared')).toBe('b')
  })

  it('a person with an empty file adopts what older versions kept in this browser', async () => {
    browserPrefs.write('pb.table.users', { hidden: ['email'] })
    browserPrefs.write('pb.files.mixed', true)
    browserPrefs.write('pb.unrelated', 1)
    const api = fake()
    const p = createSyncedPrefs({ api, quietMs: 10 })
    await p.hydrate('U1')
    await vi.advanceTimersByTimeAsync(20)
    expect(api.server).toEqual({ 'pb.table.users': { hidden: ['email'] }, 'pb.files.mixed': true })
  })

  it('reset sends what is waiting first', async () => {
    const api = fake()
    const p = createSyncedPrefs({ api, quietMs: 60_000 })
    await p.hydrate('U1')
    p.write('a', 1)
    await p.reset()
    expect(api.server).toEqual({ a: 1 })
  })
})

describe('what is kept this way', () => {
  it('the columns of a table follow the person to another browser', async () => {
    const api = fake()
    const p = createSyncedPrefs({ api, quietMs: 10 })
    setPrefsStore(p)
    await p.hydrate('U1')
    writePrefs('users', { ...defaultPrefs(), hidden: ['email'], widths: { name: 0.4 }, custom: true })
    await vi.advanceTimersByTimeAsync(20)
    expect(Object.keys(api.server)).toEqual(['pb.table.users'])
    // Another browser: nothing there, the file brings it.
    localStorage.clear()
    const q = createSyncedPrefs({ api })
    setPrefsStore(q)
    expect(readPrefs('users').hidden).toEqual([])
    await q.hydrate('U1')
    expect(readPrefs('users').hidden).toEqual(['email'])
    expect(readPrefs('users').widths).toEqual({ name: 0.4 })
  })

  it('the state of the tour is per person, in the file', async () => {
    const api = fake()
    const p = createSyncedPrefs({ api, quietMs: 10 })
    setPrefsStore(p)
    await p.hydrate('U1')
    expect(readOnboarding('U1')).toBeNull()
    writeOnboarding('U1', 'skipped', new Date('2026-11-24T08:00:00Z'))
    await vi.advanceTimersByTimeAsync(20)
    expect(api.server[onboardingKey('U1')]).toEqual({ state: 'skipped', at: '2026-11-24T08:00:00.000Z' })
    expect(readOnboarding('U1')).toEqual({ state: 'skipped', at: '2026-11-24T08:00:00.000Z' })
    expect(readOnboarding('U2')).toBeNull()
    expect(prefsStore()).toBe(p)
  })

  it('storage that throws leaves the store working for the page', async () => {
    const api = fake({ a: 1 })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('full')
    })
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    const p = createSyncedPrefs({ api })
    await expect(p.hydrate('U1')).resolves.toBeUndefined()
    expect(() => p.write('a', 2)).not.toThrow()
    expect(p.read('a')).toBeNull()
    vi.restoreAllMocks()
  })
})
