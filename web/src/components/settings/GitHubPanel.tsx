import { useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router'
import { ExternalLink } from 'lucide-react'
import { toast } from 'sonner'
import { api, ApiError } from '@/api/client'
import { useDisconnectGitHub, useGitHub, useSession } from '@/api/hooks'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { absoluteTime, relativeTime } from '@/lib/time'
import { t } from '@/i18n'
import { tg } from '@/i18n/github'
import { SettingsNote, SettingsRow, SettingsRows, SettingsSection } from './SettingsParts'

// The account's GitHub connection (accounts mode): connect the GitHub account
// to the server's App, see how it stands, and disconnect, with a warning of how
// many spaces sync through it.
export function GitHubPanel() {
  const session = useSession()
  const info = useGitHub(session.data?.mode === 'accounts' && !!session.data.user)
  if (info.isPending || !session.data) return <PageFallback />
  if (info.isError) return <ErrorState message={t.fmt.labelled(t.settings.loadFailed, info.error.message)} onRetry={() => void info.refetch()} />
  const admin = session.data.user?.role === 'admin'
  const { app, userAuthorization, authorization } = info.data
  return (
    <div className="space-y-8">
      <Failure />
      {app === null ? (
        <SettingsNote>
          {tg.account.notConfigured}
          {admin ? ` ${tg.account.notConfiguredAdmin}` : ''}
        </SettingsNote>
      ) : (
        <SettingsSection title={tg.account.title} id="github-title" description={tg.account.intro}>
          {authorization === null ? (
            <Connect enabled={userAuthorization} admin={admin} label={tg.account.connect} />
          ) : (
            <Connected />
          )}
          <p className="text-xs text-muted-foreground">
            <a className="inline-flex items-center gap-1 underline underline-offset-4" href={app.installUrl} target="_blank" rel="noreferrer noopener">
              {tg.account.install(app.name)}
              <ExternalLink className="size-3 shrink-0" />
            </a>
          </p>
        </SettingsSection>
      )}
    </div>
  )
}

// `?github_error=` from a connection that failed at GitHub: shown once, then taken out of the address.
function Failure() {
  const [params, setParams] = useSearchParams()
  const [code] = useState(() => params.get('github_error') ?? '')
  const ref = useRef<HTMLParagraphElement>(null)
  useEffect(() => {
    if (params.has('github_error'))
      setParams(
        (p) => {
          const next = new URLSearchParams(p)
          next.delete('github_error')
          return next
        },
        { replace: true },
      )
    ref.current?.focus()
    // Once, on arrival.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  if (!code) return null
  // Own keys only: a code like `constructor` is not a message.
  const message = Object.hasOwn(tg.account.errors, code) ? tg.account.errors[code as keyof typeof tg.account.errors] : tg.account.errors.unknown
  return <FormMessage focusRef={ref}>{t.fmt.labelled(tg.account.failed, message)}</FormMessage>
}

function Connect({ enabled, admin, label }: { enabled: boolean; admin: boolean; label: string }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  if (!enabled)
    return (
      <SettingsNote>
        {tg.account.userAuthOff}
        {admin ? ` ${tg.account.userAuthOffAdmin}` : ''}
      </SettingsNote>
    )
  const go = async () => {
    setBusy(true)
    setError('')
    try {
      // Back here, on this section.
      const { url } = await api.githubStart('/settings/github')
      window.location.assign(url)
    } catch (e) {
      setBusy(false)
      setError(e instanceof ApiError ? e.message : tg.account.startFailed)
    }
  }
  return (
    <div className="grid grid-cols-1 gap-2">
      <p className="text-sm text-muted-foreground">{tg.account.notConnected}</p>
      <div>
        <Button type="button" onClick={() => void go()} disabled={busy}>
          {busy && <Spinner />}
          {label}
        </Button>
      </div>
      <FormMessage>{error}</FormMessage>
    </div>
  )
}

function Connected() {
  const info = useGitHub()
  const disconnect = useDisconnectGitHub()
  const [confirm, setConfirm] = useState(false)
  const a = info.data?.authorization
  if (!a) return null
  const expired = a.state === 'expired'
  const doDisconnect = async () => {
    try {
      await disconnect.mutateAsync()
      setConfirm(false)
      toast.success(tg.account.disconnected)
    } catch (e) {
      setConfirm(false)
      toast.error(t.fmt.labelled(tg.account.disconnectFailed, e instanceof Error ? e.message : ''))
    }
  }
  return (
    <div className="grid grid-cols-1 gap-3">
      <SettingsRows>
        <SettingsRow title={tg.account.account} help={tg.account.accountHelp}>
          <span className="flex min-w-0 flex-wrap items-center gap-2 text-sm font-medium break-all">
            {a.login}
            <Badge variant={expired ? 'destructive' : 'secondary'}>{expired ? tg.account.stateExpired : tg.account.stateOk}</Badge>
          </span>
        </SettingsRow>
        <SettingsRow title={tg.account.connectedAt}>
          <span className="text-sm" title={absoluteTime(a.connected)}>
            {relativeTime(a.connected)}
          </span>
        </SettingsRow>
        {a.refreshExpires && !expired && (
          <SettingsRow title={tg.account.renews} help={tg.account.renewsHelp}>
            <span className="text-sm" title={absoluteTime(a.refreshExpires)}>
              {absoluteTime(a.refreshExpires)}
            </span>
          </SettingsRow>
        )}
        <SettingsRow title={tg.account.usedBy} help={tg.account.usedByHelp}>
          <span className="text-sm tabular-nums">{tg.account.spaces(a.spaces)}</span>
        </SettingsRow>
      </SettingsRows>
      {expired && (
        <div role="alert" className="grid grid-cols-1 gap-1 rounded-lg border border-warning/50 bg-warning/5 p-3 text-sm">
          <p className="font-medium">{tg.account.expiredTitle}</p>
          <p className="text-muted-foreground">{tg.account.expiredBody}</p>
        </div>
      )}
      <div className="flex flex-wrap gap-2">
        {expired && <Connect enabled admin={false} label={tg.account.reconnect} />}
        <Button type="button" variant="outline" onClick={() => setConfirm(true)}>
          {tg.account.disconnect}
        </Button>
      </div>
      <AlertDialog open={confirm} onOpenChange={setConfirm}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tg.account.disconnectTitle}</AlertDialogTitle>
            <AlertDialogDescription>{a.spaces > 0 ? tg.account.disconnectWarn(a.spaces) : tg.account.disconnectNone}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tg.account.cancel}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" disabled={disconnect.isPending} onClick={() => void doDisconnect()}>
              {tg.account.disconnectConfirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
