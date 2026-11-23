import { useState } from 'react'
import { toast } from 'sonner'
import { adminApi, useAdminTokens, useMachineAccess, useSetMachineAccess } from '@/api/admin'
import { keys } from '@/api/hooks'
import { useQueryClient } from '@tanstack/react-query'
import { ApiError } from '@/api/client'
import type { AdminToken } from '@/api/types'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { SettingsRow, SettingsRows, SettingsSection } from '@/components/settings/SettingsParts'
import { TokenItem } from '@/components/tokens/TokenItem'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { t } from '@/i18n'
import { tk } from '@/i18n/tokens'

// What machines may do on this server: whether API tokens are accepted, and
// everyone's tokens, which an administrator may revoke.
export function AccessPanel() {
  return (
    <div className="grid w-full min-w-0 grid-cols-1 gap-8">
      <TokenSwitch />
      <AllTokens />
    </div>
  )
}

function TokenSwitch() {
  const access = useMachineAccess()
  const set = useSetMachineAccess()
  if (access.isPending) return <PageFallback />
  if (access.isError) return <ErrorState message={t.fmt.labelled(tk.admin.loadFailed, access.error.message)} onRetry={() => void access.refetch()} />
  return (
    <SettingsSection title={tk.admin.title}>
      <SettingsRows>
        <SettingsRow title={tk.admin.switchTitle} help={tk.admin.switchHelp}>
          <Switch
            aria-label={tk.admin.switchLabel}
            checked={access.data.tokens}
            disabled={set.isPending}
            onCheckedChange={(on) => set.mutate({ tokens: on }, { onError: (e) => toast.error(e instanceof ApiError ? e.message : tk.admin.switchFailed) })}
          />
        </SettingsRow>
      </SettingsRows>
    </SettingsSection>
  )
}

function AllTokens() {
  const qc = useQueryClient()
  const list = useAdminTokens()
  const [revoking, setRevoking] = useState<AdminToken | null>(null)
  const [busy, setBusy] = useState(false)

  const revoke = async () => {
    if (!revoking) return
    setBusy(true)
    try {
      await adminApi.adminRevokeToken(revoking.id)
      toast.success(tk.revoked)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : tk.revokeFailed)
    }
    void qc.invalidateQueries({ queryKey: keys.adminTokens })
    setRevoking(null)
    setBusy(false)
  }

  const items = list.data?.pages.flatMap((p) => p.items) ?? []
  return (
    <SettingsSection title={tk.admin.all} description={tk.admin.allHelp}>
      {list.isPending ? (
        <PageFallback />
      ) : list.isError ? (
        <ErrorState message={t.fmt.labelled(tk.admin.listFailed, list.error.message)} onRetry={() => void list.refetch()} />
      ) : items.length === 0 ? (
        <p className="text-sm text-muted-foreground">{tk.admin.empty}</p>
      ) : (
        <>
          <ul className="w-full min-w-0 divide-y rounded-lg border">
            {items.map((token) => (
              <TokenItem
                key={token.id}
                token={token}
                owner={token.owner.name && token.owner.name !== token.owner.username ? `${token.owner.name} (${token.owner.username})` : token.owner.username}
                action={
                  token.state === 'active' ? (
                    <Button type="button" variant="outline" size="sm" className="max-sm:w-full" aria-label={tk.revokeLabel(token.name)} onClick={() => setRevoking(token)}>
                      {tk.revoke}
                    </Button>
                  ) : undefined
                }
              />
            ))}
          </ul>
          {list.hasNextPage && (
            <div>
              <Button type="button" variant="outline" size="sm" disabled={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                {list.isFetchingNextPage && <Spinner />}
                {tk.admin.loadMore}
              </Button>
            </div>
          )}
        </>
      )}
      <AlertDialog open={revoking !== null} onOpenChange={(open) => !open && setRevoking(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tk.revokeTitle}</AlertDialogTitle>
            <AlertDialogDescription className="break-words">{revoking ? tk.revokeHelp(revoking.name) : ''}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tk.cancel}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" disabled={busy} onClick={() => void revoke()}>
              {tk.revokeConfirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsSection>
  )
}
