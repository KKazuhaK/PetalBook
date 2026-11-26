// Keeping every copy up to date: once a minute while the page is visible, and at once when it comes back to the
// foreground or the network returns, the heads of all the account's spaces are asked in one request; only a space
// whose head is not the cursor it holds is asked for its changes. No push, no per-device state on the server.

import { api, ApiError } from '@/api/client'
import { spacesOf } from './registry'
import { clearAccount, existingStore } from './manager'
import { classify, syncSpace, type SyncOptions, type SyncResult } from './sync'
import { noteNetworkFailure, noteNetworkOk } from './status'

export const POLL_MS = 60_000
const HEADS_AT_ONCE = 100
// A space whose sync fails (not for want of a network) is asked again after a poll period, then after twice as
// long each time, up to this; one that works is asked as usual again.
export const BACKOFF_MAX_MS = 30 * 60_000

const failing = new Map<string, { fails: number; until: number }>()
const failKey = (user: string, space: string) => `${user}\u0000${space}`
// For the tests: every space as if it had never failed.
export const resetBackoff = () => failing.clear()
const noteOutcome = (user: string, space: string, r: SyncResult, now: number) => {
  const k = failKey(user, space)
  if (r.outcome === 'error') {
    const fails = (failing.get(k)?.fails ?? 0) + 1
    failing.set(k, { fails, until: now + Math.min(BACKOFF_MAX_MS, POLL_MS * 2 ** (fails - 1)) })
  } else if (r.outcome !== 'offline') failing.delete(k)
}

export interface SyncEvent {
  space: string
  result: SyncResult
}

type Listener = (e: SyncEvent) => void
const listeners = new Set<Listener>()
export const onSynced = (l: Listener) => {
  listeners.add(l)
  return () => void listeners.delete(l)
}

// The server says offline copies are turned off (403 feature_off): everything this account keeps on the device is
// deleted, and the ones listening (the app, to ask the setting again) are told.
const offListeners = new Set<(user: string) => void>()
export const onOfflineOff = (l: (user: string) => void) => {
  offListeners.add(l)
  return () => void offListeners.delete(l)
}
async function refused(user: string) {
  await clearAccount(user).catch(() => undefined)
  offListeners.forEach((l) => l(user))
}

// Spaces being synced right now, for the settings to say so.
const busy = new Set<string>()
const busyListeners = new Set<() => void>()
export const isSyncing = (space: string) => busy.has(space)
export const anySyncing = () => busy.size > 0
export function subscribeBusy(l: () => void) {
  busyListeners.add(l)
  return () => void busyListeners.delete(l)
}
const setBusy = (space: string, on: boolean) => {
  if (on) busy.add(space)
  else busy.delete(space)
  busyListeners.forEach((l) => l())
}

// Spaces whose copy was asked to sync while a sync of it was under way: that sync read the choice before the ask,
// so it is followed by another.
const again = new Set<string>()

// One sync of one space, never two at once. A call that finds one under way does not start another, and does not
// get lost either: the one under way is followed by a sync of what was asked since.
export async function syncNow(user: string, space: string, opts: SyncOptions = {}): Promise<SyncResult> {
  if (busy.has(space)) {
    again.add(space)
    return { outcome: 'ok', changed: false }
  }
  // Taken before anything is waited for, so two calls at once cannot both start.
  setBusy(space, true)
  let changed = false
  try {
    for (;;) {
      again.delete(space)
      const store = await existingStore(user, space)
      if (!store) return { outcome: 'ok', changed: false }
      const run = await syncSpace(store, opts)
      changed ||= run.changed
      // What the run changed, and what the ones before it did, is one change to whoever asked.
      const result = { ...run, changed }
      noteOutcome(user, space, result, (opts.now ?? Date.now)())
      if (result.outcome === 'ok') noteNetworkOk()
      else if (result.outcome === 'offline') noteNetworkFailure()
      listeners.forEach((l) => l({ space, result }))
      if (result.outcome === 'off') await refused(user)
      // Asked again meanwhile: only after a sync that got through (a failing one is left to the next poll).
      if (result.outcome === 'ok' && again.has(space) && !opts.signal?.aborted) continue
      return result
    }
  } finally {
    again.delete(space)
    setBusy(space, false)
  }
}

// One round: the heads, then the changes of the spaces that moved (and are not waiting after a failure).
export async function pollOnce(user: string, signal?: AbortSignal, now: () => number = Date.now): Promise<void> {
  const spaces = spacesOf(user)
  if (!spaces.length) return
  const heads: Record<string, string> = {}
  const missing: string[] = []
  try {
    for (let i = 0; i < spaces.length; i += HEADS_AT_ONCE) {
      const r = await api.syncHeads(spaces.slice(i, i + HEADS_AT_ONCE), signal)
      Object.assign(heads, r.heads)
      missing.push(...(r.missing ?? []))
    }
  } catch (e) {
    const c = classify(e).outcome
    if (c === 'offline') noteNetworkFailure()
    else if (c === 'off') {
      noteNetworkOk()
      await refused(user)
    }
    return
  }
  noteNetworkOk()
  for (const space of spaces) {
    if ((failing.get(failKey(user, space))?.until ?? 0) > now()) continue
    const store = await existingStore(user, space)
    if (!store) continue
    const state = await store.state()
    if (space in heads) {
      // Back in the heads: a space that was switched off is on again.
      if (heads[space] !== state.cursor || state.disabled || !state.complete)
        await syncNow(user, space, { signal, now })
    } else if (missing.includes(space) && !state.disabled) {
      // Not readable now: gone for this person (404: the copy is deleted), or switched off (kept, read only).
      await syncNow(user, space, { signal, now })
    }
  }
}

// Starts the polling of an account. Returns what stops it.
export function startPolling(user: string, opts: { every?: number } = {}): () => void {
  let stopped = false
  let timer: ReturnType<typeof setTimeout> | undefined
  let ctl: AbortController | undefined
  const visible = () => typeof document === 'undefined' || document.visibilityState === 'visible'
  const round = async () => {
    if (stopped || !visible()) return
    ctl = new AbortController()
    try {
      await pollOnce(user, ctl.signal)
    } catch (e) {
      if (!(e instanceof ApiError) && !(e instanceof DOMException)) console.error(e)
    }
  }
  const schedule = () => {
    clearTimeout(timer)
    if (stopped) return
    timer = setTimeout(async () => {
      await round()
      schedule()
    }, opts.every ?? POLL_MS)
  }
  const now = () => {
    void round().then(schedule)
  }
  const onVisible = () => {
    if (visible()) now()
  }
  document.addEventListener('visibilitychange', onVisible)
  window.addEventListener('online', now)
  now()
  return () => {
    stopped = true
    clearTimeout(timer)
    ctl?.abort()
    document.removeEventListener('visibilitychange', onVisible)
    window.removeEventListener('online', now)
  }
}
