// Keeping every copy up to date: once a minute while the page is visible, and at once when it comes back to the
// foreground or the network returns, the heads of all the account's spaces are asked in one request; only a space
// whose head is not the cursor it holds is asked for its changes. No push, no per-device state on the server.

import { api, ApiError } from '@/api/client'
import { spacesOf } from './registry'
import { existingStore } from './manager'
import { classify, syncSpace, type SyncOptions, type SyncResult } from './sync'
import { noteNetworkFailure, noteNetworkOk } from './status'

export const POLL_MS = 60_000
const HEADS_AT_ONCE = 100

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

// One sync of one space, never two at once.
export async function syncNow(user: string, space: string, opts: SyncOptions = {}): Promise<SyncResult> {
  if (busy.has(space)) return { outcome: 'ok', changed: false }
  // Taken before anything is waited for, so two calls at once cannot both start.
  setBusy(space, true)
  try {
    const store = await existingStore(user, space)
    if (!store) return { outcome: 'ok', changed: false }
    const result = await syncSpace(store, opts)
    if (result.outcome === 'ok') noteNetworkOk()
    else if (result.outcome === 'offline') noteNetworkFailure()
    listeners.forEach((l) => l({ space, result }))
    return result
  } finally {
    setBusy(space, false)
  }
}

// One round: the heads, then the changes of the spaces that moved.
export async function pollOnce(user: string, signal?: AbortSignal): Promise<void> {
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
    if (classify(e).outcome === 'offline') noteNetworkFailure()
    return
  }
  noteNetworkOk()
  for (const space of spaces) {
    const store = await existingStore(user, space)
    if (!store) continue
    const state = await store.state()
    if (space in heads) {
      // Back in the heads: a space that was switched off is on again.
      if (heads[space] !== state.cursor || state.disabled || !state.complete) await syncNow(user, space, { signal })
    } else if (missing.includes(space) && !state.disabled) {
      // Not readable now: gone for this person (404: the copy is deleted), or switched off (kept, read only).
      await syncNow(user, space, { signal })
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
