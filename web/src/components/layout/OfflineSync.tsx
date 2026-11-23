import { useEffect } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { CloudOff } from 'lucide-react'
import { keys, useSession } from '@/api/hooks'
import { t } from '@/i18n'
import { onSynced, startPolling } from '@/lib/offline'
import { useOffline } from '@/lib/offline/status'

// Keeps the copies of the spaces on this device up to date while the app is open (src/lib/offline/poller.ts): the
// heads of all of them once a minute while the page is visible, and at once when it comes back or the network does.
// Renders nothing; what a sync changed is read again by the lists on screen.
export function OfflineSync() {
  const user = useSession().data?.user?.id
  const qc = useQueryClient()
  useEffect(() => {
    if (!user) return
    return startPolling(user)
  }, [user])
  useEffect(
    () =>
      onSynced(({ space, result }) => {
        if (result.changed) {
          void qc.invalidateQueries({ queryKey: keys.tree(space) })
          void qc.invalidateQueries({ queryKey: keys.tags(space) })
          void qc.invalidateQueries({ queryKey: keys.pages(space) })
        }
        // The person cannot read the space any more, or it was switched off: the list of spaces says so.
        if (result.outcome === 'gone' || result.outcome === 'disabled') void qc.invalidateQueries({ queryKey: keys.spaces })
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
    <div role="status" data-offline-banner="" className="flex items-start gap-2 border-b bg-muted px-3 py-1.5 text-xs text-muted-foreground">
      <CloudOff className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
      <span>{t.offline.banner}</span>
    </div>
  )
}
