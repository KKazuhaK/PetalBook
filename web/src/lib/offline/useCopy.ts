// What the device keeps of one space, for the settings to show: the choice, where the sync is, how much room it
// takes. Read again when a sync of the space ends and when the settings change it.

import { useCallback, useEffect, useState, useSyncExternalStore } from 'react'
import { existingStore } from './manager'
import { onSynced, isSyncing, subscribeBusy } from './poller'
import { bytesOf, totalBytes } from './registry'
import type { OfflineConfig, SyncState } from './store'

export interface CopyInfo {
  config: OfflineConfig | null
  state: SyncState | null
  catalog: number
  texts: number
  // Bytes of text this space keeps, and every space on the device.
  bytes: number
  deviceBytes: number
  accountBytes: number
}
const NONE: CopyInfo = { config: null, state: null, catalog: 0, texts: 0, bytes: 0, deviceBytes: 0, accountBytes: 0 }

export function useCopy(user: string | undefined, space: string): { info: CopyInfo | null; syncing: boolean; reload: () => void; failed: boolean } {
  const [info, setInfo] = useState<CopyInfo | null>(null)
  const [failed, setFailed] = useState(false)
  const [rev, setRev] = useState(0)
  const reload = useCallback(() => setRev((r) => r + 1), [])
  useEffect(() => {
    if (!user || !space) return
    let alive = true
    void (async () => {
      try {
        const store = await existingStore(user, space)
        if (!store) return alive && (setInfo({ ...NONE, deviceBytes: totalBytes(), accountBytes: bytesOf(user) }), setFailed(false))
        const [config, state, catalog, texts, bytes] = await Promise.all([store.config(), store.state(), store.catalogCount(), store.textCount(), store.bytes()])
        if (alive) {
          setInfo({ config: config ?? null, state, catalog, texts, bytes, deviceBytes: totalBytes(), accountBytes: bytesOf(user) })
          setFailed(false)
        }
      } catch {
        if (alive) setFailed(true)
      }
    })()
    return () => {
      alive = false
    }
  }, [user, space, rev])
  useEffect(() => onSynced((e) => e.space === space && setRev((r) => r + 1)), [space])
  const syncing = useSyncExternalStore(subscribeBusy, () => isSyncing(space), () => false)
  return { info, syncing, reload, failed }
}
