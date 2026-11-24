// The copy of a space on this device (milestone 4.1): a catalogue of its pages and the text of some of them, kept in
// IndexedDB per account and space, brought up to date by asking the server what changed, and read from when the
// server cannot be reached. Reading and searching work offline; writing does not (4.2).

import { currentUserId, setOfflineHooks } from '@/api/client'
import type { Page } from '@/api/types'
import { clearAccount, existingStore } from './manager'
import { readOffline } from './reader'
import { isOffline, noteNetworkFailure } from './status'
import { DEFAULT_CAP_BYTES } from './sync'
import { totalBytes } from './registry'
import { pageBytes } from './store'

export { DEFAULT_CAP_BYTES } from './sync'
export { clearAccount, configure, dropCopy, existingStore, hasCopy, storeFor } from './manager'
export { onOfflineOff, onSynced, pollOnce, startPolling, syncNow, isSyncing, anySyncing, subscribeBusy, POLL_MS } from './poller'
export { isOffline, useOffline } from './status'
export { useOfflineAllowed } from './allowed'
export { DEFAULT_CONFIG, RECENT_KEPT, type OfflineConfig, type OfflineMode, type SyncState } from './store'

// Registers what the API client needs to answer reads from the device and to delete the copies on sign-out.
export function installOffline() {
  setOfflineHooks({ read: readOffline, signedOut: clearAccount, answered: noteNetworkFailure })
}
installOffline()

// A page the person opened: it counts among the last opened, and its text is kept at once when the space keeps
// the recent ones and there is room. Nothing happens for a space that has no copy.
export async function noteOpened(space: string, page: Page, now: () => number = Date.now) {
  const user = currentUserId()
  if (!user) return
  const store = await existingStore(user, space)
  if (!store) return
  const cfg = await store.config()
  if (!cfg || cfg.mode !== 'text' || !cfg.recent) return
  await store.touchOpened(page.id, now())
  if (isOffline()) return
  const have = (await store.textMetas()).find((m) => m.id === page.id)
  if (have?.rev === page.rev) return
  if (totalBytes() + pageBytes(page) > DEFAULT_CAP_BYTES) return
  await store.putPages([page])
}
