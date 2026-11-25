// The person's own preferences, kept in their personal space (docs/api.md, "Preferences"): behind the PrefsStore
// interface (see prefsStore.ts), so the screens that read a column width or the tour's state do not know where it lives.
//
//   - Read once per session: after sign-in the server's copy is fetched once and put in this browser's storage; every
//     read after that is a synchronous read of what is there. Nothing is asked of the server again.
//   - Writes go to this browser's storage at once and to the server in batches (a quiet 2 seconds, never more than 15
//     seconds after the first), and the server commits them in one quiet commit of its own. Nothing is ever in a
//     database.
//   - Where there is no personal space, before sign-in, or offline, this browser's storage alone is the store, and the
//     writes made meanwhile go to the server when it can be reached.
//   - What this browser copied from one person's file is not left for the next person who signs in on it.

import { api } from '@/api/client'
import { browserPrefs, setPrefsStore, type PrefsStore } from './prefsStore'

export interface PrefsApi {
  get(): Promise<{ personal: boolean; prefs: Record<string, unknown> }>
  patch(set: Record<string, unknown>): Promise<{ personal: boolean }>
}

export interface SyncedPrefs extends PrefsStore {
  // Fetches the person's file once and makes this browser's copy match it. Resolves when done; never rejects (offline
  // leaves the local copy as it is).
  hydrate(userId: string): Promise<void>
  // Sends what is waiting now.
  flush(): Promise<void>
  // Forgets who the session was (sign-out): what is waiting is sent first when it can be.
  reset(): Promise<void>
  status(): { hydrated: boolean; personal: boolean; waiting: number }
}

const MIRROR_KEY = 'pb.prefs.mirror'
// What older versions kept in this browser alone, adopted into the person's file the first time they sign in.
const ADOPT_PREFIXES = ['pb.table.', 'pb.files.', 'pb.onboarding']

interface Mirror {
  user: string
  keys: string[]
}

function readMirror(local: PrefsStore): Mirror | null {
  const v = local.read(MIRROR_KEY) as Partial<Mirror> | null
  return v && typeof v.user === 'string' && Array.isArray(v.keys) ? { user: v.user, keys: v.keys.map(String) } : null
}

function localKeys(): string[] {
  try {
    const out: string[] = []
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i)
      if (k) out.push(k)
    }
    return out
  } catch {
    return []
  }
}

export function createSyncedPrefs(o: {
  api: PrefsApi
  local?: PrefsStore
  quietMs?: number
  maxWaitMs?: number
  retryMs?: number
}): SyncedPrefs {
  const local = o.local ?? browserPrefs
  const quiet = o.quietMs ?? 2000
  const maxWait = o.maxWaitMs ?? 15_000
  const retry = o.retryMs ?? 30_000
  let user = ''
  let hydrated = false
  let personal = false
  // Written, and not on the server yet. null is a removal.
  let waiting: Record<string, unknown> = {}
  // Written before the person was known: sent once hydration says where it goes.
  const early: Record<string, unknown> = {}
  let timer: ReturnType<typeof setTimeout> | null = null
  let first = 0
  let sending: Promise<void> | null = null

  const track = (key: string) => {
    if (!user || !personal) return
    const m = readMirror(local) ?? { user, keys: [] }
    if (m.user === user && !m.keys.includes(key)) local.write(MIRROR_KEY, { user, keys: [...m.keys, key] })
  }

  const schedule = (ms = quiet) => {
    if (timer) clearTimeout(timer)
    const now = Date.now()
    if (first === 0) first = now
    timer = setTimeout(() => void store.flush(), Math.max(0, Math.min(ms, first + maxWait - now)))
  }

  const queue = (key: string, value: unknown) => {
    if (!hydrated) {
      early[key] = value
      return
    }
    if (!personal) return
    waiting[key] = value
    track(key)
    schedule()
  }

  const store: SyncedPrefs = {
    read: (key) => local.read(key),
    write(key, value) {
      local.write(key, value)
      queue(key, value)
    },
    remove(key) {
      local.remove(key)
      queue(key, null)
    },
    async flush() {
      if (timer) clearTimeout(timer)
      timer = null
      first = 0
      if (sending) await sending.catch(() => undefined)
      const set = waiting
      if (!hydrated || !personal || Object.keys(set).length === 0) return
      waiting = {}
      sending = o.api
        .patch(set)
        .then((r) => {
          if (!r.personal) personal = false
        })
        .catch(() => {
          // Not sent (offline, or the server is away): what came in meanwhile is newer, the rest waits to be tried again.
          waiting = { ...set, ...waiting }
          schedule(retry)
        })
        .finally(() => {
          sending = null
        })
      await sending
    },
    async hydrate(userId) {
      user = userId
      let answer: Awaited<ReturnType<PrefsApi['get']>>
      try {
        answer = await o.api.get()
      } catch {
        return // this browser's copy stands; asked again at the next sign-in
      }
      personal = answer.personal
      const mirror = readMirror(local)
      // What a person before copied here is not this person's.
      if (mirror && mirror.user !== userId) {
        for (const k of mirror.keys) local.remove(k)
        local.remove(MIRROR_KEY)
      }
      if (personal) {
        const had = readMirror(local)
        const keys = new Set(had?.user === userId ? had.keys : [])
        // The first time on this browser: what older versions kept here goes into the file.
        const adopt: Record<string, unknown> = {}
        if (!had) {
          for (const k of localKeys())
            if (ADOPT_PREFIXES.some((p) => k.startsWith(p)) && !(k in answer.prefs)) {
              const v = local.read(k)
              if (v !== null) adopt[k] = v
            }
        }
        // The file wins; what this browser had of it that the file no longer has is gone.
        for (const k of keys) if (!(k in answer.prefs) && !(k in early)) local.remove(k)
        for (const [k, v] of Object.entries(answer.prefs)) {
          if (k in early) continue // written a moment ago: newer than the file
          local.write(k, v)
          keys.add(k)
        }
        for (const k of [...Object.keys(adopt), ...Object.keys(early)]) keys.add(k)
        local.write(MIRROR_KEY, { user: userId, keys: [...keys] })
        waiting = { ...waiting, ...adopt, ...early }
      }
      for (const k of Object.keys(early)) delete early[k]
      hydrated = true
      if (Object.keys(waiting).length > 0) schedule(0)
    },
    async reset() {
      await store.flush().catch(() => undefined)
      user = ''
      hydrated = false
      personal = false
      waiting = {}
      first = 0
    },
    status: () => ({ hydrated, personal, waiting: Object.keys(waiting).length }),
  }
  return store
}

// The one the app uses: installed in place of the browser-only store, and sent whatever is waiting when the page is hidden
// or closed (best effort: this browser's copy has it anyway).
let installed: SyncedPrefs | null = null
export function installSyncedPrefs(): SyncedPrefs {
  if (installed) return installed
  const s = createSyncedPrefs({ api: { get: () => api.prefs(), patch: (set) => api.patchPrefs(set) } })
  installed = s
  setPrefsStore(s)
  if (typeof document !== 'undefined') {
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'hidden') void s.flush()
    })
    window.addEventListener('pagehide', () => void s.flush())
  }
  return s
}
export const syncedPrefs = (): SyncedPrefs | null => installed
