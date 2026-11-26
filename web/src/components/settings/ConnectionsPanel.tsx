import { useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router'
import { Check, ExternalLink, Link2, RefreshCw, Unlink } from 'lucide-react'
import { toast } from 'sonner'
import { api, ApiError } from '@/api/client'
import type { ConnectionLeftover, Provider } from '@/api/types'
import {
  useConnectionOwners,
  useConnections,
  useCreateSpace,
  useDisconnect,
  useDismissLeftover,
  useProvider,
  useSession,
} from '@/api/hooks'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
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
import { BrandIcon } from '@/components/brand/BrandIcon'
import { Button } from '@/components/ui/button'
import { absoluteTime, relativeTime } from '@/lib/time'
import { administers } from '@/lib/access'
import { t } from '@/i18n'
import { tg } from '@/i18n/github'
import { tf } from '@/i18n/forge'
import { SettingsNote, SettingsRow, SettingsRows, SettingsSection } from './SettingsParts'

// The words of one platform's row. A platform with installations (an App people install) has its own words; the
// others take their name from the server, so the panel never names a platform by itself.
interface Labels {
  intro: string
  notConnected: string
  connect: string
  connectHelp: string
  // Only where there is an installation page to skip.
  authorizeOnly: string | null
  userAuthOff: string
  userAuthOffAdmin: string
  startFailed: string
  accountHelp: string
  usedByHelp: string
  expiredBody: string
  disconnectTitle: string
  disconnectNone: string
  disconnected: string
  leftoverHelp: string
  failed: string
  errors: Record<string, string>
}

export function labelsFor(p: Provider): Labels {
  if (p.capabilities.installations) {
    const a = tg.account
    return {
      intro: a.intro,
      notConnected: a.notConnected,
      connect: a.connect,
      connectHelp: a.connectHelp,
      authorizeOnly: a.authorizeOnly,
      userAuthOff: a.userAuthOff,
      userAuthOffAdmin: a.userAuthOffAdmin,
      startFailed: a.startFailed,
      accountHelp: a.accountHelp,
      usedByHelp: a.usedByHelp,
      expiredBody: a.expiredBody,
      disconnectTitle: a.disconnectTitle,
      disconnectNone: a.disconnectNone,
      disconnected: a.disconnected,
      leftoverHelp: a.leftoverHelp,
      failed: a.failed,
      errors: a.errors,
    }
  }
  const f = tf.account
  const n = p.name
  return {
    intro: f.intro(n),
    notConnected: f.notConnected(n),
    connect: f.connect(n),
    connectHelp: f.connectHelp(n),
    authorizeOnly: null,
    userAuthOff: f.userAuthOff(n),
    userAuthOffAdmin: f.userAuthOffAdmin,
    startFailed: f.startFailed(n),
    accountHelp: f.accountHelp(n),
    usedByHelp: f.usedByHelp,
    expiredBody: f.expiredBody(n),
    disconnectTitle: f.disconnectTitle(n),
    disconnectNone: f.disconnectNone(n),
    disconnected: f.disconnected(n),
    leftoverHelp: f.leftoverHelp(n),
    failed: f.failed(n),
    errors: {
      denied: f.errors.denied(n),
      expired: f.errors.expired(),
      provider_error: f.errors.provider_error(n),
      account_in_use: f.errors.account_in_use(n),
      different_account: f.errors.different_account(n),
      unknown: f.errors.unknown(),
    },
  }
}

// The account's connected accounts (accounts mode): one row per platform the server connects to. Connect an
// account, see how it stands, and disconnect, with a warning of how many spaces sync through it.
export function ConnectionsPanel() {
  const session = useSession()
  const info = useConnections(session.data?.mode === 'accounts' && !!session.data.user)
  if (info.isPending || !session.data) return <PageFallback />
  if (info.isError)
    return (
      <ErrorState
        message={t.fmt.labelled(t.settings.loadFailed, info.error.message)}
        onRetry={() => void info.refetch()}
      />
    )
  const admin = administers(session.data.user)
  // Only the platforms a person can connect an account to, and that the server has set up.
  const rows = info.data.providers.filter((p) => p.capabilities.connectAccount && p.configured)
  return (
    <div className="space-y-8">
      <Failure providers={info.data.providers} />
      {rows.length === 0 ? (
        <SettingsNote>
          {tg.account.notConfigured}
          {admin ? ` ${tg.account.notConfiguredAdmin}` : ''}
        </SettingsNote>
      ) : (
        rows.map((p) => <ProviderRow key={p.id} provider={p} admin={admin} />)
      )}
    </div>
  )
}

// One platform: its reminders of places that did not finish syncing, then the account.
function ProviderRow({ provider, admin }: { provider: Provider; admin: boolean }) {
  const { app, userAuthorization, connection, leftovers } = provider
  const labels = labelsFor(provider)
  return (
    <div className="space-y-6" data-provider={provider.id}>
      {leftovers.length > 0 && <Leftovers provider={provider.id} items={leftovers} help={labels.leftoverHelp} />}
      <SettingsSection
        title={provider.name}
        id={`connection-${provider.id}-title`}
        description={labels.intro}
        icon={<BrandIcon type={provider.type} className="size-4" />}
      >
        {connection === null ? (
          <Connect
            provider={provider.id}
            enabled={userAuthorization}
            admin={admin}
            label={labels.connect}
            labels={labels}
            install={provider.capabilities.installations}
          />
        ) : (
          <Connected provider={provider.id} labels={labels} installs={provider.capabilities.installations} />
        )}
        {app && (
          <p className="text-xs text-muted-foreground">
            <a
              className="inline-flex items-center gap-1 underline underline-offset-4"
              href={app.installUrl}
              target="_blank"
              rel="noreferrer noopener"
            >
              {tg.account.install(app.name)}
              <ExternalLink className="size-3 shrink-0" />
            </a>
          </p>
        )}
      </SettingsSection>
    </div>
  )
}

// `?connection_error=` from a connection that failed at the platform: shown once, then taken out of the address.
function Failure({ providers }: { providers: Provider[] }) {
  const [params, setParams] = useSearchParams()
  const [code] = useState(() => params.get('connection_error') ?? '')
  const [id] = useState(() => params.get('provider') ?? '')
  const ref = useRef<HTMLParagraphElement>(null)
  useEffect(() => {
    if (params.has('connection_error'))
      setParams(
        (p) => {
          const next = new URLSearchParams(p)
          next.delete('connection_error')
          next.delete('provider')
          return next
        },
        { replace: true },
      )
    ref.current?.focus()
    // Once, on arrival.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  if (!code) return null
  // The words of the platform the attempt was for (GitHub's when the address does not say).
  const named = providers.find((p) => p.id === id)
  const labels = named
    ? labelsFor(named)
    : labelsFor(providers.find((p) => p.capabilities.installations) ?? providers[0])
  // Own keys only: a code like `constructor` is not a message.
  const message = Object.hasOwn(labels.errors, code) ? labels.errors[code] : labels.errors.unknown
  return <FormMessage focusRef={ref}>{t.fmt.labelled(labels.failed, message)}</FormMessage>
}

// Connect goes to the App's installation page, which authorises the person in the same flow (so installing on
// their own account and connecting it are one step); `install` false goes to the authorisation page alone, for a
// person who has the App installed already.
function Connect({
  provider,
  enabled,
  admin,
  label,
  labels,
  install,
}: {
  provider: string
  enabled: boolean
  admin: boolean
  label: string
  labels: Labels
  // The platform has an installation page that authorises in the same step.
  install: boolean
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  if (!enabled)
    return (
      <SettingsNote>
        {labels.userAuthOff}
        {admin ? ` ${labels.userAuthOffAdmin}` : ''}
      </SettingsNote>
    )
  const go = async (viaInstall: boolean) => {
    setBusy(true)
    setError('')
    try {
      // Back here, on this section.
      const { url } = await api.connectionStart(provider, '/settings/connections', viaInstall)
      window.location.assign(url)
    } catch (e) {
      setBusy(false)
      setError(e instanceof ApiError ? e.message : labels.startFailed)
    }
  }
  return (
    <div className="grid grid-cols-1 gap-2">
      <p className="text-sm text-muted-foreground">{labels.notConnected}</p>
      <p className="text-xs text-muted-foreground">{labels.connectHelp}</p>
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" onClick={() => void go(install)} disabled={busy}>
          {busy && <Spinner />}
          {label}
        </Button>
        {labels.authorizeOnly && (
          <Button type="button" variant="ghost" size="sm" onClick={() => void go(false)} disabled={busy}>
            <Check aria-hidden="true" />
            {labels.authorizeOnly}
          </Button>
        )}
      </div>
      <FormMessage>{error}</FormMessage>
    </div>
  )
}

function Connected({ provider, labels, installs }: { provider: string; labels: Labels; installs: boolean }) {
  const info = useProvider(provider)
  const disconnect = useDisconnect()
  const [confirm, setConfirm] = useState(false)
  const a = info.data?.connection
  if (!a) return null
  const expired = a.state === 'expired'
  const doDisconnect = async () => {
    try {
      await disconnect.mutateAsync(provider)
      setConfirm(false)
      toast.success(labels.disconnected)
    } catch (e) {
      setConfirm(false)
      toast.error(t.fmt.labelled(tg.account.disconnectFailed, e instanceof Error ? e.message : ''))
    }
  }
  return (
    <div className="grid grid-cols-1 gap-3">
      <SettingsRows>
        <SettingsRow title={tg.account.account} help={labels.accountHelp}>
          <span className="flex min-w-0 flex-wrap items-center gap-2 text-sm font-medium break-all">
            {a.login}
            <Badge variant={expired ? 'destructive' : 'secondary'}>
              {expired ? tg.account.stateExpired : tg.account.stateOk}
            </Badge>
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
        <SettingsRow title={tg.account.usedBy} help={labels.usedByHelp}>
          <span className="text-sm tabular-nums">{tg.account.spaces(a.spaces)}</span>
        </SettingsRow>
      </SettingsRows>
      {!expired && installs && <NeedsInstall provider={provider} login={a.login} />}
      {!expired && a.admin && (
        <div
          role="alert"
          data-testid="connection-admin-warning"
          className="grid grid-cols-1 gap-1 rounded-lg border border-warning/50 bg-warning/5 p-3 text-sm"
        >
          <p className="font-medium">{tf.account.adminTitle}</p>
          <p className="text-muted-foreground">{tf.account.adminBody(info.data?.name ?? '')}</p>
        </div>
      )}
      {expired && (
        <div
          role="alert"
          className="grid grid-cols-1 gap-1 rounded-lg border border-warning/50 bg-warning/5 p-3 text-sm"
        >
          <p className="font-medium">{tg.account.expiredTitle}</p>
          <p className="text-muted-foreground">{labels.expiredBody}</p>
        </div>
      )}
      <div className="flex flex-wrap gap-2">
        {expired && (
          <Connect
            provider={provider}
            enabled
            admin={false}
            label={tg.account.reconnect}
            labels={labels}
            install={installs}
          />
        )}
        <Button type="button" variant="outline" onClick={() => setConfirm(true)}>
          <Unlink aria-hidden="true" />
          {tg.account.disconnect}
        </Button>
      </div>
      <AlertDialog open={confirm} onOpenChange={setConfirm}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{labels.disconnectTitle}</AlertDialogTitle>
            <AlertDialogDescription>
              {a.spaces > 0 ? tg.account.disconnectWarn(a.spaces) : labels.disconnectNone}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tg.account.cancel}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={disconnect.isPending}
              onClick={() => void doDisconnect()}
            >
              {tg.account.disconnectConfirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

// The person's own account, when the App is not installed there: GitHub lets nothing be made in it until it is.
// One installation page connects and installs in the same step.
function NeedsInstall({ provider, login }: { provider: string; login: string }) {
  const owners = useConnectionOwners(provider)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const own = owners.data?.pages[0]?.items.find((o) => o.type === 'User' && o.needsInstall)
  if (!own) return null
  const go = async () => {
    setBusy(true)
    setError('')
    try {
      const { url } = await api.connectionStart(provider, '/settings/connections', true)
      window.location.assign(url)
    } catch (e) {
      setBusy(false)
      setError(e instanceof ApiError ? e.message : tg.account.startFailed)
    }
  }
  return (
    <div role="status" className="grid grid-cols-1 gap-2 rounded-lg border border-warning/50 bg-warning/5 p-3 text-sm">
      <p className="font-medium break-words">{tg.account.needsInstallTitle(login)}</p>
      <p className="text-muted-foreground">{tg.account.needsInstallBody}</p>
      <div>
        <Button type="button" size="sm" onClick={() => void go()} disabled={busy}>
          {busy ? <Spinner /> : <Link2 aria-hidden="true" />}
          {tg.account.installOnMine}
        </Button>
      </div>
      <FormMessage>{error}</FormMessage>
    </div>
  )
}

// Places on the platform a failed attempt to make a space left empty. Nothing there was deleted;
// the person syncs a space to one and tries again, or says they deleted it.
function Leftovers({ provider, items, help }: { provider: string; items: ConnectionLeftover[]; help: string }) {
  return (
    <SettingsSection title={tg.account.leftoverTitle} id={`connection-${provider}-leftovers-title`} description={help}>
      <ul className="divide-y rounded-lg border">
        {items.map((l) => (
          <Leftover key={l.repoId} provider={provider} item={l} />
        ))}
      </ul>
    </SettingsSection>
  )
}

function Leftover({ provider, item: l }: { provider: string; item: ConnectionLeftover }) {
  const create = useCreateSpace()
  const dismiss = useDismissLeftover()
  const [error, setError] = useState('')
  const [owner, name] = l.fullName.split('/')
  const retry = async () => {
    setError('')
    try {
      const space = await create.mutateAsync({ name: l.spaceName, sync: { provider, owner, name, create: false } })
      toast.success(tg.account.leftoverRetried(space.name, l.fullName))
    } catch (e) {
      setError(t.fmt.labelled(tg.account.leftoverFailed, e instanceof ApiError ? e.message : tg.account.errors.unknown))
    }
  }
  const forget = async () => {
    setError('')
    try {
      await dismiss.mutateAsync({ provider, repoId: l.repoId })
    } catch (e) {
      setError(
        t.fmt.labelled(tg.account.leftoverDismissFailed, e instanceof ApiError ? e.message : tg.account.errors.unknown),
      )
    }
  }
  const busy = create.isPending || dismiss.isPending
  return (
    <li className="grid grid-cols-1 gap-2 px-3 py-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center">
      <div className="min-w-0">
        <p className="text-sm font-medium break-all">
          {/^https?:\/\//.test(l.htmlUrl) ? (
            <a
              className="inline-flex items-center gap-1 underline underline-offset-4"
              href={l.htmlUrl}
              target="_blank"
              rel="noreferrer noopener"
            >
              {l.fullName}
              <ExternalLink className="size-3 shrink-0" />
            </a>
          ) : (
            l.fullName
          )}
        </p>
        <p className="text-xs text-muted-foreground break-words">
          {tg.account.leftoverFor(l.spaceName, relativeTime(l.created))}
        </p>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button type="button" size="sm" disabled={busy} onClick={() => void retry()}>
          {create.isPending ? <Spinner /> : <RefreshCw aria-hidden="true" />}
          {tg.account.leftoverRetry}
        </Button>
        <Button type="button" size="sm" variant="outline" disabled={busy} onClick={() => void forget()}>
          {tg.account.leftoverDismiss}
        </Button>
      </div>
      {error && (
        <p role="alert" className="text-sm text-destructive sm:col-span-2">
          {error}
        </p>
      )}
    </li>
  )
}
