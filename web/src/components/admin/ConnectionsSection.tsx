import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router'
import { ExternalLink, Plus, RefreshCw, Save, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import {
  useAdminGitHub,
  useCheckAdminGitHub,
  useDeleteAdminGitHub,
  useRetryGitHubManifest,
  useSaveAdminGitHub,
  useStartGitHubManifest,
} from '@/api/admin'
import { ApiError } from '@/api/client'
import type { AdminGitHub } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
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
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'
import { BrandIcon } from '@/components/brand/BrandIcon'
import { postManifest } from './githubManifest'
import { ForgeCards } from './ForgeCards'
import { tg } from '@/i18n/github'

// The connections of "Storage & sync": one card per platform the server can connect to. GitHub is the first;
// the hosts with an OAuth application (GitLab, Gitea) follow it, one card each. A card is the platform's settings. The
// GitHub App's installations are not the site's to hand out: each person and each organisation installs it on their
// own GitHub account and manages that themselves (docs/api.md, "Administration", "Connections").
export function ConnectionsSection() {
  return (
    <section aria-labelledby="connections-title" className="space-y-4">
      <h2 id="connections-title" className="text-base font-semibold">
        {tg.admin.connectionsTitle}
      </h2>
      <p className="text-sm text-muted-foreground">{tg.admin.connectionsIntro}</p>
      <GitHubCard />
      <ForgeCards />
    </section>
  )
}

function GitHubCard() {
  const app = useAdminGitHub()
  if (app.isPending) return <PageFallback />
  if (app.isError)
    return (
      <ErrorState message={t.fmt.labelled(tg.admin.loadFailed, app.error.message)} onRetry={() => void app.refetch()} />
    )
  return (
    <div className="space-y-8">
      <Returned />
      <AppSettings app={app.data} />
    </div>
  )
}

// `?connection_app=created` or `?connection_app_error=<code>` from the App Manifest flow: shown once, then taken out of the address.
function Returned() {
  const [params, setParams] = useSearchParams()
  const [made] = useState(() => params.get('connection_app') === 'created')
  const [code] = useState(() => params.get('connection_app_error') ?? '')
  const [pending] = useState(() => params.get('pending') === '1')
  const [slug] = useState(() => params.get('app') ?? '')
  const ref = useRef<HTMLParagraphElement>(null)
  useEffect(() => {
    if (params.has('connection_app') || params.has('connection_app_error'))
      setParams(
        (p) => {
          const next = new URLSearchParams(p)
          next.delete('connection_app')
          next.delete('connection_app_error')
          next.delete('pending')
          next.delete('app')
          return next
        },
        { replace: true },
      )
    ref.current?.focus()
    // Once, on arrival.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  if (made)
    return (
      <p role="status" className="rounded-md border px-3 py-2 text-sm">
        {tg.admin.created}
      </p>
    )
  if (!code) return null
  if (code === 'unavailable') return <MadeButNotSaved slug={slug} pending={pending} />
  // Own keys only: a code like `constructor` is not a message.
  const message = Object.hasOwn(tg.admin.returned, code)
    ? tg.admin.returned[code as keyof typeof tg.admin.returned]
    : tg.admin.returned.unknown
  return <FormMessage focusRef={ref}>{t.fmt.labelled(tg.admin.createFailed, message)}</FormMessage>
}

// GitHub made the App but the server could not ask GitHub about it: the App exists there, and what GitHub gave
// is kept sealed for ten minutes for one more try. Without it the administrator finishes by hand.
function MadeButNotSaved({ slug, pending }: { slug: string; pending: boolean }) {
  const retry = useRetryGitHubManifest()
  const [state, setState] = useState<'waiting' | 'saved' | 'gone' | 'error'>('waiting')
  const [error, setError] = useState('')
  const go = async () => {
    setError('')
    try {
      await retry.mutateAsync()
      setState('saved')
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) setState('gone')
      else setError(explain(e))
    }
  }
  if (state === 'saved')
    return (
      <p role="status" className="rounded-md border px-3 py-2 text-sm">
        {tg.admin.pendingSaved}
      </p>
    )
  return (
    <div role="alert" className="grid grid-cols-1 gap-2 rounded-lg border border-warning/50 bg-warning/5 p-3 text-sm">
      <p className="font-medium">{tg.admin.returned.unavailable}</p>
      {slug && <p>{tg.admin.createdOnGitHub(slug)}</p>}
      <p className="text-muted-foreground">
        {state === 'gone' ? tg.admin.pendingGone : pending ? tg.admin.pendingBody : tg.admin.pendingLost}
      </p>
      {pending && state !== 'gone' && (
        <div>
          <Button type="button" size="sm" onClick={() => void go()} disabled={retry.isPending}>
            {retry.isPending ? <Spinner /> : <RefreshCw aria-hidden="true" />}
            {retry.isPending ? tg.admin.pendingRetrying : tg.admin.pendingRetry}
          </Button>
        </div>
      )}
      <FormMessage>{error}</FormMessage>
    </div>
  )
}

function explain(e: unknown): string {
  if (!(e instanceof ApiError)) return tg.admin.errors.failed
  const code = e.code as string
  const known = tg.admin.errors.codes as Record<string, string>
  return Object.hasOwn(known, code) ? known[code] : e.message || tg.admin.errors.failed
}

function AppSettings({ app }: { app: AdminGitHub }) {
  const save = useSaveAdminGitHub()
  const remove = useDeleteAdminGitHub()
  const configured = app.configured
  const [appId, setAppId] = useState(app.configured ? app.appId : '')
  const [key, setKey] = useState('')
  const [clientId, setClientId] = useState(app.configured ? app.clientId : '')
  const [secret, setSecret] = useState('')
  const [hookSecret, setHookSecret] = useState('')
  const [error, setError] = useState('')
  const [confirmDelete, setConfirmDelete] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (save.isPending) return
    setError('')
    if (appId.trim() === '' || (!configured && key.trim() === '')) return setError(tg.admin.needIds)
    if (
      clientId.trim() !== '' &&
      secret === '' &&
      !(app.configured && app.clientSecret.set && app.clientId === clientId.trim())
    )
      return setError(tg.admin.needSecret)
    try {
      await save.mutateAsync({
        appId: appId.trim(),
        ...(key.trim() ? { privateKey: key.trim() } : {}),
        // An empty client id removes the client id and secret; leaving the field as it was changes nothing.
        clientId: clientId.trim(),
        ...(secret ? { clientSecret: secret } : {}),
        ...(hookSecret ? { webhookSecret: hookSecret } : {}),
      })
      setKey('')
      setSecret('')
      setHookSecret('')
      toast.success(tg.admin.saved)
    } catch (err) {
      setError(explain(err))
    } finally {
      // The mutation holds the request, key and secret included: let go of it.
      save.reset()
    }
  }
  const doDelete = async () => {
    try {
      await remove.mutateAsync()
      setConfirmDelete(false)
      setAppId('')
      setClientId('')
      toast.success(tg.admin.deleted)
    } catch (err) {
      setConfirmDelete(false)
      toast.error(explain(err))
    }
  }

  return (
    <SettingsSection
      title={tg.admin.title}
      id="github-app-title"
      description={tg.admin.intro}
      icon={<BrandIcon type="github" className="size-5" />}
    >
      {app.configured && (
        <div className="grid grid-cols-1 gap-2 rounded-lg border p-3 text-sm">
          <p className="flex flex-wrap items-center gap-2 font-medium break-all">
            {app.name}
            <Badge variant="secondary">{app.slug}</Badge>
            <a
              className="inline-flex items-center gap-1 text-xs font-normal underline underline-offset-4"
              href={app.htmlUrl}
              target="_blank"
              rel="noreferrer noopener"
            >
              {tg.admin.openApp}
              <ExternalLink className="size-3 shrink-0" />
            </a>
          </p>
          <dl className="grid grid-cols-1 gap-x-4 gap-y-1 text-xs sm:grid-cols-[auto_minmax(0,1fr)]">
            <dt className="text-muted-foreground">{tg.admin.fingerprint}</dt>
            <dd className="font-mono break-all">{app.privateKey.fingerprint}</dd>
            <dt className="text-muted-foreground">{tg.admin.permissions}</dt>
            <dd className="break-words">
              {Object.entries(app.permissions)
                .map(([k, v]) => `${k}: ${v}`)
                .join(', ')}
            </dd>
            <dt className="text-muted-foreground">{tg.admin.usage}</dt>
            <dd>{tg.admin.usageValue(app.spaces, app.authorizations)}</dd>
          </dl>
          {app.missing.length > 0 && (
            <p role="alert" className="rounded-md border border-warning/50 bg-warning/5 px-3 py-2 text-xs break-words">
              {tg.admin.missing(app.missing)}
            </p>
          )}
          {app.gaps.length > 0 && (
            <p role="alert" className="rounded-md border border-warning/50 bg-warning/5 px-3 py-2 text-xs break-words">
              {tg.admin.gaps(app.gaps.map((g) => `${g.account} (${g.missing.join(', ')})`))}
            </p>
          )}
          {app.anyAccount === false && (
            <p role="alert" className="rounded-md border border-warning/50 bg-warning/5 px-3 py-2 text-xs break-words">
              {tg.admin.anyAccountNo}
            </p>
          )}
          {app.anyAccount === null && (
            <p className="text-xs text-muted-foreground break-words">{tg.admin.anyAccountUnknown}</p>
          )}
          {!app.userAuthorization && <p className="text-xs text-muted-foreground">{tg.admin.userAuthOff}</p>}
          <Recheck checked={app.checked} />
        </div>
      )}

      <CreateApp app={app} />

      <form className="grid grid-cols-1 gap-4" onSubmit={(e) => void submit(e)} noValidate>
        <Field
          label={tg.admin.appId}
          help={tg.admin.appIdHelp}
          value={appId}
          onChange={(e) => setAppId(e.target.value)}
          autoComplete="off"
          spellCheck={false}
          className="w-full min-w-0"
        />
        <div className="grid grid-cols-1 gap-1.5">
          <Label htmlFor="github-private-key">{tg.admin.privateKey}</Label>
          <Textarea
            id="github-private-key"
            rows={4}
            value={key}
            onChange={(e) => setKey(e.target.value)}
            placeholder={configured ? tg.admin.keepKey : '-----BEGIN RSA PRIVATE KEY-----'}
            className="w-full min-w-0 font-mono text-xs break-all"
            autoComplete="off"
            spellCheck={false}
          />
          <p className="text-xs text-muted-foreground">
            {configured ? tg.admin.privateKeyKept : tg.admin.privateKeyHelp}
          </p>
        </div>
        <Field
          label={tg.admin.clientId}
          help={tg.admin.clientIdHelp}
          value={clientId}
          onChange={(e) => setClientId(e.target.value)}
          autoComplete="off"
          spellCheck={false}
          className="w-full min-w-0"
        />
        <Field
          label={tg.admin.clientSecret}
          help={app.configured && app.clientSecret.set ? tg.admin.secretKept : tg.admin.secretHelp}
          type="password"
          value={secret}
          onChange={(e) => setSecret(e.target.value)}
          placeholder={app.configured && app.clientSecret.set ? '••••••••' : ''}
          autoComplete="new-password"
          className="w-full min-w-0"
        />
        <Field
          label={tg.admin.webhookSecret}
          help={app.configured && app.webhookSecret.set ? tg.admin.webhookSecretKept : tg.admin.webhookSecretHelp}
          type="password"
          value={hookSecret}
          onChange={(e) => setHookSecret(e.target.value)}
          placeholder={app.configured && app.webhookSecret.set ? '••••••••' : ''}
          autoComplete="new-password"
          className="w-full min-w-0"
        />
        <div className="grid grid-cols-1 gap-1 text-xs text-muted-foreground">
          {app.webhookUrl !== undefined && (
            <>
              <p>{tg.admin.webhookHelp}</p>
              <code className="font-mono break-all select-all">{app.webhookUrl || tg.admin.noPublicUrl}</code>
            </>
          )}
          <p>{tg.admin.callbackHelp}</p>
          <code className="font-mono break-all select-all">{app.callbackUrl || tg.admin.noPublicUrl}</code>
          {app.signinCallbackUrl && (
            <>
              <p>{tg.admin.signinCallbackHelp}</p>
              <code className="font-mono break-all select-all">{app.signinCallbackUrl}</code>
            </>
          )}
        </div>
        <FormMessage>{error}</FormMessage>
        <div className="flex flex-wrap gap-2">
          <Button type="submit" disabled={save.isPending}>
            {save.isPending ? <Spinner /> : <Save aria-hidden="true" />}
            {save.isPending ? tg.admin.checking : tg.admin.save}
          </Button>
          {configured && (
            <Button type="button" variant="outline" onClick={() => setConfirmDelete(true)}>
              <Trash2 aria-hidden="true" />
              {tg.admin.delete}
            </Button>
          )}
        </div>
      </form>

      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tg.admin.deleteTitle}</AlertDialogTitle>
            <AlertDialogDescription>
              {app.configured && app.spaces > 0
                ? tg.admin.deleteBlocked(app.spaces)
                : tg.admin.deleteDescription(app.configured ? app.authorizations : 0)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tg.admin.cancel}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={remove.isPending || (app.configured && app.spaces > 0)}
              onClick={() => void doDelete()}
            >
              {tg.admin.deleteConfirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsSection>
  )
}

// Reads the App and its installations from GitHub again: the permissions shown are those of the day it was saved
// (or last checked), and an administrator who changed them on GitHub asks for today's.
function Recheck({ checked }: { checked: string }) {
  const check = useCheckAdminGitHub()
  const run = async () => {
    try {
      await check.mutateAsync()
      toast.success(tg.admin.rechecked)
    } catch (err) {
      toast.error(explain(err))
    }
  }
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button type="button" variant="outline" size="sm" disabled={check.isPending} onClick={() => void run()}>
        {check.isPending ? <Spinner /> : <RefreshCw aria-hidden="true" />}
        {check.isPending ? tg.admin.rechecking : tg.admin.recheck}
      </Button>
      <span className="text-xs text-muted-foreground">{t.fmt.labelled(tg.admin.checkedAt, absoluteTime(checked))}</span>
    </div>
  )
}

// One click makes the App on GitHub (the App Manifest flow): the browser posts the manifest to GitHub, GitHub
// asks the person to confirm and sends the browser back, and the server saves the App it was handed.
function CreateApp({ app }: { app: AdminGitHub }) {
  const start = useStartGitHubManifest()
  const [org, setOrg] = useState('')
  const [name, setName] = useState('')
  const [error, setError] = useState('')
  const inUse = app.configured && (app.spaces > 0 || app.authorizations > 0)
  const go = async () => {
    setError('')
    try {
      const m = await start.mutateAsync({
        ...(org.trim() ? { organization: org.trim() } : {}),
        ...(name.trim() ? { name: name.trim() } : {}),
      })
      postManifest(m)
    } catch (err) {
      setError(explain(err))
    }
  }
  return (
    <div className="grid grid-cols-1 gap-3 rounded-lg border p-3">
      <div className="grid gap-1">
        <h3 className="text-sm font-medium">{tg.admin.createTitle}</h3>
        <p className="text-xs text-muted-foreground">{tg.admin.createHelp}</p>
      </div>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Field
          label={tg.admin.createOrg}
          help={tg.admin.createOrgHelp}
          value={org}
          onChange={(e) => setOrg(e.target.value)}
          autoComplete="off"
          spellCheck={false}
          className="w-full min-w-0"
        />
        <Field
          label={tg.admin.createName}
          help={tg.admin.createNameHelp}
          value={name}
          onChange={(e) => setName(e.target.value)}
          maxLength={34}
          autoComplete="off"
          className="w-full min-w-0"
        />
      </div>
      {inUse && <p className="text-xs text-muted-foreground">{tg.admin.createInUse}</p>}
      <FormMessage>{error}</FormMessage>
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" disabled={start.isPending || inUse} onClick={() => void go()}>
          {start.isPending ? <Spinner /> : <Plus aria-hidden="true" />}
          {start.isPending ? tg.admin.createStarting : tg.admin.createButton}
        </Button>
        <span className="text-xs text-muted-foreground">{tg.admin.createManual}</span>
      </div>
    </div>
  )
}
