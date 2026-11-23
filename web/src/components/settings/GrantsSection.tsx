import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api, ApiError } from '@/api/client'
import { keys } from '@/api/hooks'
import type { Grant } from '@/api/types'
import { PageFallback } from '@/components/common'
import { GrantItem } from '@/components/tokens/GrantItem'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { oa } from '@/i18n/oauth'
import { SettingsSection } from './SettingsParts'

// The apps connected to the account through the approval page, and a way to
// disconnect one. The server answers 404 when it has no authorization server
// (no public address, or MCP switched off): then the section is not there.
export default function GrantsSection() {
  const qc = useQueryClient()
  const list = useQuery({
    queryKey: keys.grants,
    queryFn: ({ signal }) => api.grants(signal),
    staleTime: 15_000,
    retry: false,
  })
  const [revoking, setRevoking] = useState<Grant | null>(null)
  const [busy, setBusy] = useState(false)

  const revoke = async () => {
    if (!revoking) return
    setBusy(true)
    try {
      await api.revokeGrant(revoking.id)
      toast.success(oa.yours.revoked)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : oa.yours.revokeFailed)
    }
    void qc.invalidateQueries({ queryKey: keys.grants })
    setRevoking(null)
    setBusy(false)
  }

  if (list.isPending) return <PageFallback />
  // No authorization server here: nothing to show, and nothing to explain.
  if (list.isError && list.error instanceof ApiError && list.error.status === 404) return null
  return (
    <SettingsSection title={oa.yours.title} description={oa.yours.help}>
      {list.isError ? (
        <p className="text-sm text-destructive" role="alert">
          {`${oa.yours.loadFailed}: ${list.error.message}`}
        </p>
      ) : list.data.grants.length === 0 ? (
        <p className="text-sm text-muted-foreground">{oa.yours.none}</p>
      ) : (
        <ul className="w-full min-w-0 divide-y rounded-lg border">
          {list.data.grants.map((g) => (
            <GrantItem
              key={g.id}
              grant={g}
              action={
                <Button type="button" variant="outline" size="sm" className="max-sm:w-full" aria-label={oa.yours.revokeLabel(g.client.name)} onClick={() => setRevoking(g)}>
                  {oa.yours.revoke}
                </Button>
              }
            />
          ))}
        </ul>
      )}
      <AlertDialog open={revoking !== null} onOpenChange={(open) => !open && setRevoking(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{oa.yours.revokeTitle}</AlertDialogTitle>
            <AlertDialogDescription className="break-words">{revoking ? oa.yours.revokeHelp(revoking.client.name) : ''}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{oa.yours.cancel}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" disabled={busy} onClick={() => void revoke()}>
              {oa.yours.revokeConfirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsSection>
  )
}
