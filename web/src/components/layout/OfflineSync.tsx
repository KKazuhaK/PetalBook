import { useEffect } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { CloudOff } from 'lucide-react'
import { keys, useSession } from '@/api/hooks'
import { t } from '@/i18n'
import { clearAccount, onOfflineOff, onSynced, startPolling } from '@/lib/offline'
import { featuresKey, useFeature, useFeatures } from '@/lib/features'
import { useOffline } from '@/lib/offline/status'

// Keeps the copies of the spaces on this device up to date while the app is open (src/lib/offline/poller.ts): the
// heads of all of them once a minute while the page is visible, and at once when it comes back or the network does.
// Renders nothing; what a sync changed is read again by the lists on screen.
export function OfflineSync() {
  const user = useSession().data?.user?.id
  const qc = useQueryClient()
  // When the administrators turn offline copies off for the tenant, nothing is synced or polled and this account's
  // copies are deleted (at the start, or when the setting is read again, or when a sync is refused).
  const features = useFeatures()
  const allowed = useFeature('features.offline')
  // Not before the answer is in (a request that is sure to be refused is not sent); when it cannot be had (the
  // server cannot be reached), the polling goes on and a refusal of its own deletes the copies.
  const asked = !features.isPending
  useEffect(() => {
    if (!user || !asked || allowed === false) return
    return startPolling(user)
  }, [user, asked, allowed])
  useEffect(() => {
    if (user && allowed === false) void clearAccount(user).catch(() => undefined)
  }, [user, allowed])
  useEffect(() => onOfflineOff(() => void qc.invalidateQueries({ queryKey: featuresKey })), [qc])
  useEffect(
    () =>
      onSynced(({ space, result }) => {
        if (result.changed) {
          void qc.invalidateQueries({ queryKey: keys.tree(space) })
          void qc.invalidateQueries({ queryKey: keys.tags(space) })
          void qc.invalidateQueries({ queryKey: keys.pages(space) })
        }
        // The person cannot read the space any more, or it was switched off: the list of spaces says so.
        if (result.outcome === 'gone' || result.outcome === 'disabled')
          void qc.invalidateQueries({ queryKey: keys.spaces })
      }),
    [qc],
  )
  return null
}

// Says plainly that what is on the screen is what the device has, and that nothing can be changed now.
export function OfflineBanner() {
  const offline = useOffline()
  if (!offline) return null
  return (
    <div
      role="status"
      data-offline-banner=""
      className="flex items-start gap-2 border-b bg-muted px-3 py-1.5 text-xs text-muted-foreground"
    >
      <CloudOff className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
      <span>{t.offline.banner}</span>
    </div>
  )
}
