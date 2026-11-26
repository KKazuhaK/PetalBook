import { useState } from 'react'
import { Eraser, KeyRound } from 'lucide-react'
import { toast } from 'sonner'
import { useChangeSecret, useSecrets, useTenantSettings } from '@/api/admin'
import { ApiError } from '@/api/client'
import type { SecretMeta } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { absoluteTime } from '@/lib/time'
import { tt } from '@/i18n/tenants'

const ts = tt.secrets

// Secrets that are changed where the service checks them, not here: listed as set or not.
const MANAGED = new Set(['github.appKey', 'github.clientSecret', 'github.webhookSecret'])
// A secret of a tenant that belongs to a feature, and the feature: the site has to allow it the tenant.
const FEATURE_OF: Record<string, string> = { 'mail.password': 'mail.ownSender' }

const nameOf = (name: string) => ts.names[name] ?? name

function explain(e: unknown): string {
  return e instanceof ApiError ? e.message || ts.failed : ts.failed
}

// The secrets of the site, or of one tenant (`tenant`): whether each is set and when it last changed, never its
// value. One component for the site's console and the tenant's.
export function SecretsSection({ tenant, name }: { tenant?: string; name?: string }) {
  const q = useSecrets(tenant)
  // What the tenant does not have the feature for cannot be set; the server says so too (403 feature_off).
  const settings = useTenantSettings(tenant ?? '', tenant !== undefined)
  const off = (name: string) => {
    const key = FEATURE_OF[name]
    if (!tenant || !key) return false
    return settings.data?.settings.find((s) => s.key === key)?.effective === false
  }
  const [replacing, setReplacing] = useState<SecretMeta | null>(null)
  const [clearing, setClearing] = useState<SecretMeta | null>(null)

  if (q.isPending) return <PageFallback />
  if (q.isError) return <ErrorState message={ts.loadFailed} onRetry={() => void q.refetch()} />
  return (
    <SettingsSection
      title={tenant ? ts.tenantTitle(name ?? '') : ts.title}
      description={tenant ? ts.tenantHelp : ts.help}
      id={tenant ? 'secrets-tenant-heading' : 'secrets-site-heading'}
    >
      <ul className="w-full min-w-0 divide-y rounded-lg border" data-slot="secrets">
        {q.data.map((s) => {
          const gated = off(s.name)
          const managed = !tenant && MANAGED.has(s.name)
          return (
            <li
              key={s.name}
              data-slot="secret"
              data-name={s.name}
              className="grid min-w-0 gap-3 px-4 py-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center"
            >
              <div className="min-w-0 space-y-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-medium break-words">{nameOf(s.name)}</span>
                  <Badge variant={s.set ? 'secondary' : 'outline'}>{s.set ? ts.set : ts.notSet}</Badge>
                </div>
                <p className="font-mono text-xs break-all text-muted-foreground">{s.name}</p>
                {s.set && s.updatedAt && (
                  <p className="text-xs text-muted-foreground">{ts.changed(absoluteTime(s.updatedAt))}</p>
                )}
                {managed && <p className="text-xs text-muted-foreground">{ts.managedBy}</p>}
                {gated && (
                  <p role="status" className="text-xs break-words text-muted-foreground" data-slot="feature-off">
                    {ts.featureOff}
                  </p>
                )}
              </div>
              {!managed && (
                <div className="flex min-w-0 flex-col gap-2 sm:flex-row">
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="max-sm:w-full"
                    aria-label={ts.replaceFor(nameOf(s.name))}
                    disabled={gated}
                    onClick={() => setReplacing(s)}
                  >
                    <KeyRound aria-hidden="true" />
                    {ts.replace}
                  </Button>
                  {s.set && (
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      className="max-sm:w-full"
                      aria-label={ts.clearFor(nameOf(s.name))}
                      onClick={() => setClearing(s)}
                    >
                      <Eraser aria-hidden="true" />
                      {ts.clear}
                    </Button>
                  )}
                </div>
              )}
            </li>
          )
        })}
      </ul>
      <ReplaceDialog tenant={tenant} secret={replacing} onClose={() => setReplacing(null)} />
      <ClearDialog tenant={tenant} secret={clearing} onClose={() => setClearing(null)} />
    </SettingsSection>
  )
}

// The new value is typed into a password field that starts empty and is not kept: closing the dialog forgets it.
function ReplaceDialog({
  tenant,
  secret,
  onClose,
}: {
  tenant?: string
  secret: SecretMeta | null
  onClose: () => void
}) {
  return (
    <Dialog open={secret !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        {secret && <ReplaceForm key={secret.name} tenant={tenant} secret={secret} onClose={onClose} />}
      </DialogContent>
    </Dialog>
  )
}

function ReplaceForm({ tenant, secret, onClose }: { tenant?: string; secret: SecretMeta; onClose: () => void }) {
  const change = useChangeSecret(tenant)
  const [value, setValue] = useState('')
  const [error, setError] = useState('')
  const name = nameOf(secret.name)
  const submit = () => {
    if (value === '' || change.isPending) return
    setError('')
    change.mutate(
      { name: secret.name, value },
      {
        onSuccess: () => {
          toast.success(ts.saved)
          onClose()
        },
        onError: (e) => setError(explain(e)),
      },
    )
  }
  return (
    <form
      className="grid min-w-0 gap-4"
      onSubmit={(e) => {
        e.preventDefault()
        submit()
      }}
    >
      <DialogHeader>
        <DialogTitle className="break-words">{ts.replaceTitle(name)}</DialogTitle>
        <DialogDescription>{ts.replaceHelp}</DialogDescription>
      </DialogHeader>
      <div className="grid gap-1.5">
        <Label htmlFor="secret-value">{ts.value}</Label>
        <Input
          id="secret-value"
          type="password"
          autoComplete="new-password"
          autoCapitalize="off"
          spellCheck={false}
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
      </div>
      <FormMessage>{error}</FormMessage>
      <DialogFooter className="gap-2">
        <Button type="button" variant="outline" onClick={onClose}>
          {ts.cancel}
        </Button>
        <Button type="submit" disabled={value === '' || change.isPending}>
          {change.isPending && <Spinner />}
          {ts.save}
        </Button>
      </DialogFooter>
    </form>
  )
}

function ClearDialog({ tenant, secret, onClose }: { tenant?: string; secret: SecretMeta | null; onClose: () => void }) {
  const change = useChangeSecret(tenant)
  const name = secret ? nameOf(secret.name) : ''
  const confirm = () => {
    if (!secret) return
    // The dialog closes with the click, so a refusal is told as a message.
    change.mutate(
      { name: secret.name, value: null },
      { onSuccess: () => toast.success(ts.cleared), onError: (e) => toast.error(explain(e)) },
    )
  }
  return (
    <AlertDialog open={secret !== null} onOpenChange={(open) => !open && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle className="break-words">{ts.clearTitle(name)}</AlertDialogTitle>
          <AlertDialogDescription>{ts.clearHelp}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{ts.cancel}</AlertDialogCancel>
          <AlertDialogAction variant="destructive" disabled={change.isPending} onClick={confirm}>
            {ts.confirmClear}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
