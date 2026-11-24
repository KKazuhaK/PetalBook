// Whether the app can reach its server, for the indicator and for the editor's read-only state. A network failure of
// any request says no; any answer, an error one included, says yes; the browser's own online and offline events
// are believed for what they say about the device (a device that is offline cannot reach anything).

import { useSyncExternalStore } from 'react'

let offline = typeof navigator !== 'undefined' && navigator.onLine === false
const listeners = new Set<() => void>()

function set(v: boolean) {
  if (offline === v) return
  offline = v
  listeners.forEach((l) => l())
}

export const isOffline = () => offline
export const noteNetworkFailure = () => set(true)
export const noteNetworkOk = () => set(false)
export function subscribeStatus(l: () => void) {
  listeners.add(l)
  return () => void listeners.delete(l)
}
// The state of the device itself, reset by tests.
export function resetStatus(to = false) {
  offline = to
  listeners.forEach((l) => l())
}

if (typeof window !== 'undefined') {
  // Going offline is believed at once; coming back is only a reason to try (the next request says).
  window.addEventListener('offline', () => set(true))
  window.addEventListener('online', () => set(false))
}

export function useOffline(): boolean {
  return useSyncExternalStore(subscribeStatus, isOffline, () => false)
}
