import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router'
import { ExternalLink } from 'lucide-react'
import { toast } from 'sonner'
import { useAdminGitHub, useAdminGitHubInstallations, useCheckAdminGitHub, useDeleteAdminGitHub, useSaveAdminGitHub, useShareGitHubInstallation, useStartGitHubManifest, useTenants } from '@/api/admin'
import { ApiError } from '@/api/client'
import type { AdminGitHub, GitHubInstallation } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Textarea } from '@/components/ui/textarea'
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'
import { postManifest } from './githubManifest'
import { tg } from '@/i18n/github'

// The server's one GitHub App: its settings, and where it is installed with the
// tenants each organisation's installation is shared with (docs/api.md, "Administration").
export function GitHubPanel() {
  const app = useAdminGitHub()
  if (app.isPending) return <PageFallback />
  if (app.isError) return <ErrorState message={t.fmt.labelled(tg.admin.loadFailed, app.error.message)} onRetry={() => void app.refetch()} />
  return (
    <div className="space-y-8">
      <Returned />
      <AppSettings app={app.data} />
      {app.data.configured && <Installations />}
    </div>
  )
}

// `?github_app=created` or `?github_app_error=<code>` from the App Manifest flow: shown once, then taken out of the address.
function Returned() {
  const [params, setParams] = useSearchParams()
  const [made] = useState(() => params.get('github_app') === 'created')
  const [code] = useState(() => params.get('github_app_error') ?? '')
  const ref = useRef<HTMLParagraphElement>(null)
  useEffect(() => {
    if (params.has('github_app') || params.has('github_app_error'))
      setParams(
        (p) => {
          const next = new URLSearchParams(p)
          next.delete('github_app')
          next.delete('github_app_error')
          return next
        },
        { replace: true },
      )
    ref.current?.focus()
    // Once, on arrival.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  if (made) return <p role="status" className="rounded-md border px-3 py-2 text-sm">{tg.admin.created}</p>
  if (!code) return null
  // Own keys only: a code like `constructor` is not a message.
  const message = Object.hasOwn(tg.admin.returned, code) ? tg.admin.returned[code as keyof typeof tg.admin.returned] : tg.admin.returned.unknown
  return <FormMessage focusRef={ref}>{t.fmt.labelled(tg.admin.createFailed, message)}</FormMessage>
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
  const [error, setError] = useState('')
  const [confirmDelete, setConfirmDelete] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (save.isPending) return
    setError('')
    if (appId.trim() === '' || (!configured && key.trim() === '')) return setError(tg.admin.needIds)
    if (clientId.trim() !== '' && secret === '' && !(app.configured && app.clientSecret.set && app.clientId === clientId.trim())) return setError(tg.admin.needSecret)
    try {
      await save.mutateAsync({
        appId: appId.trim(),
        ...(key.trim() ? { privateKey: key.trim() } : {}),
        // An empty client id removes the client id and secret; leaving the field as it was changes nothing.
        clientId: clientId.trim(),
        ...(secret ? { clientSecret: secret } : {}),
      })
      setKey('')
      setSecret('')
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
    <SettingsSection title={tg.admin.title} id="github-app-title" description={tg.admin.intro}>
      {app.configured && (
        <div className="grid grid-cols-1 gap-2 rounded-lg border p-3 text-sm">
          <p className="flex flex-wrap items-center gap-2 font-medium break-all">
            {app.name}
            <Badge variant="secondary">{app.slug}</Badge>
            <a className="inline-flex items-center gap-1 text-xs font-normal underline underline-offset-4" href={app.htmlUrl} target="_blank" rel="noreferrer noopener">
              {tg.admin.openApp}
              <ExternalLink className="size-3 shrink-0" />
            </a>
          </p>
          <dl className="grid grid-cols-1 gap-x-4 gap-y-1 text-xs sm:grid-cols-[auto_minmax(0,1fr)]">
            <dt className="text-muted-foreground">{tg.admin.fingerprint}</dt>
            <dd className="font-mono break-all">{app.privateKey.fingerprint}</dd>
            <dt className="text-muted-foreground">{tg.admin.permissions}</dt>
            <dd className="break-words">{Object.entries(app.permissions).map(([k, v]) => `${k}: ${v}`).join(', ')}</dd>
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
          {app.anyAccount === null && <p className="text-xs text-muted-foreground break-words">{tg.admin.anyAccountUnknown}</p>}
          {!app.userAuthorization && <p className="text-xs text-muted-foreground">{tg.admin.userAuthOff}</p>}
          <Recheck checked={app.checked} />
        </div>
      )}

      <CreateApp app={app} />

      <form className="grid grid-cols-1 gap-4" onSubmit={(e) => void submit(e)} noValidate>
        <Field label={tg.admin.appId} help={tg.admin.appIdHelp} value={appId} onChange={(e) => setAppId(e.target.value)} autoComplete="off" spellCheck={false} className="w-full min-w-0" />
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
          <p className="text-xs text-muted-foreground">{configured ? tg.admin.privateKeyKept : tg.admin.privateKeyHelp}</p>
        </div>
        <Field label={tg.admin.clientId} help={tg.admin.clientIdHelp} value={clientId} onChange={(e) => setClientId(e.target.value)} autoComplete="off" spellCheck={false} className="w-full min-w-0" />
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
        <div className="grid grid-cols-1 gap-1 text-xs text-muted-foreground">
          <p>{tg.admin.callbackHelp}</p>
          <code className="font-mono break-all select-all">{app.callbackUrl || tg.admin.noPublicUrl}</code>
        </div>
        <FormMessage>{error}</FormMessage>
        <div className="flex flex-wrap gap-2">
          <Button type="submit" disabled={save.isPending}>
            {save.isPending && <Spinner />}
            {save.isPending ? tg.admin.checking : tg.admin.save}
          </Button>
          {configured && (
            <Button type="button" variant="outline" onClick={() => setConfirmDelete(true)}>
              {tg.admin.delete}
            </Button>
          )}
        </div>
      </form>

      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tg.admin.deleteTitle}</AlertDialogTitle>
            <AlertDialogDescription>{app.configured && app.spaces > 0 ? tg.admin.deleteBlocked(app.spaces) : tg.admin.deleteDescription(app.configured ? app.authorizations : 0)}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tg.admin.cancel}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" disabled={remove.isPending || (app.configured && app.spaces > 0)} onClick={() => void doDelete()}>
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
        {check.isPending && <Spinner />}
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
      const m = await start.mutateAsync({ ...(org.trim() ? { organization: org.trim() } : {}), ...(name.trim() ? { name: name.trim() } : {}) })
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
        <Field label={tg.admin.createOrg} help={tg.admin.createOrgHelp} value={org} onChange={(e) => setOrg(e.target.value)} autoComplete="off" spellCheck={false} className="w-full min-w-0" />
        <Field label={tg.admin.createName} help={tg.admin.createNameHelp} value={name} onChange={(e) => setName(e.target.value)} maxLength={34} autoComplete="off" className="w-full min-w-0" />
      </div>
      {inUse && <p className="text-xs text-muted-foreground">{tg.admin.createInUse}</p>}
      <FormMessage>{error}</FormMessage>
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" disabled={start.isPending || inUse} onClick={() => void go()}>
          {start.isPending && <Spinner />}
          {start.isPending ? tg.admin.createStarting : tg.admin.createButton}
        </Button>
        <span className="text-xs text-muted-foreground">{tg.admin.createManual}</span>
      </div>
    </div>
  )
}

function Installations() {
  const list = useAdminGitHubInstallations()
  const items = list.data?.pages.flatMap((p) => p.items) ?? []
  return (
    <SettingsSection title={tg.admin.installations} id="github-installations-title" description={tg.admin.installationsHelp}>
      {list.isPending ? (
        <PageFallback />
      ) : list.isError ? (
        <ErrorState message={t.fmt.labelled(tg.admin.installationsFailed, explain(list.error))} onRetry={() => void list.refetch()} />
      ) : items.length === 0 ? (
        <p className="text-sm text-muted-foreground">{tg.admin.noInstallations}</p>
      ) : (
        <ul className="divide-y rounded-lg border">
          {items.map((i) => (
            <Installation key={i.id} installation={i} />
          ))}
        </ul>
      )}
      {list.hasNextPage && (
        <div>
          <Button type="button" variant="outline" size="sm" disabled={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
            {list.isFetchingNextPage && <Spinner />}
            {tg.admin.loadMore}
          </Button>
        </div>
      )}
    </SettingsSection>
  )
}

function Installation({ installation: i }: { installation: GitHubInstallation }) {
  const tenants = useTenants()
  const [choosing, setChoosing] = useState(false)
  const org = i.account.type === 'Organization'
  const names = t.fmt.list(i.tenants.map((id) => tenants.data?.find((x) => x.id === id)?.name ?? id))
  return (
    <li className="grid grid-cols-1 gap-2 px-3 py-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center">
      <div className="min-w-0">
        <p className="flex flex-wrap items-center gap-2 text-sm font-medium break-all">
          <a className="underline-offset-4 hover:underline" href={i.htmlUrl} target="_blank" rel="noreferrer noopener">
            {i.account.login}
          </a>
          <Badge variant="outline">{org ? tg.admin.organisation : tg.admin.user}</Badge>
          <Badge variant="outline">{i.repositorySelection === 'all' ? tg.admin.selectionAll : tg.admin.selectionSelected}</Badge>
          {i.suspended && <Badge variant="destructive">{tg.admin.suspended}</Badge>}
        </p>
        <p className="text-xs text-muted-foreground">{tg.admin.spacesUsing(i.spaces)}</p>
      </div>
      {org && (
        <div className="flex items-center gap-2">
          <Button type="button" variant="outline" size="sm" data-testid="share-choose" aria-label={tg.admin.chooseFor(i.account.login)} onClick={() => setChoosing(true)}>
            {tg.admin.choose}
          </Button>
        </div>
      )}
      {!org && <p className="text-xs text-muted-foreground sm:col-span-2">{tg.admin.notShareable}</p>}
      {org && <p className="text-xs text-muted-foreground sm:col-span-2">{i.tenants.length > 0 ? t.fmt.sentences(tg.admin.sharedWith(names), tg.admin.sharedHelpOn) : t.fmt.sentences(tg.admin.notShared, tg.admin.sharedHelpOff)}</p>}
      {choosing && <ShareDialog installation={i} onClose={() => setChoosing(false)} />}
    </li>
  )
}

// Ticks the tenants an installation is shared with; Save replaces the whole set.
function ShareDialog({ installation: i, onClose }: { installation: GitHubInstallation; onClose: () => void }) {
  const tenants = useTenants(true, true)
  const share = useShareGitHubInstallation()
  const [picked, setPicked] = useState<Set<string>>(() => new Set(i.tenants))
  const toggle = (id: string, on: boolean) =>
    setPicked((prev) => {
      const next = new Set(prev)
      if (on) next.add(id)
      else next.delete(id)
      return next
    })
  const save = async () => {
    try {
      await share.mutateAsync({ id: i.id, tenants: [...picked] })
      toast.success(tg.admin.sharedSaved(i.account.login))
      onClose()
    } catch (err) {
      toast.error(explain(err))
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{tg.admin.chooseTitle(i.account.login)}</DialogTitle>
          <DialogDescription>{tg.admin.chooseHelp}</DialogDescription>
        </DialogHeader>
        {tenants.isPending ? (
          <p className="text-sm text-muted-foreground">{tg.admin.chooseLoading}</p>
        ) : tenants.isError ? (
          <ErrorState message={explain(tenants.error)} onRetry={() => void tenants.refetch()} />
        ) : tenants.data.length === 0 ? (
          <p className="text-sm text-muted-foreground">{tg.admin.chooseNone}</p>
        ) : (
          <ul className="max-h-72 space-y-1 overflow-y-auto">
            {tenants.data.map((x) => (
              <li key={x.id}>
                <label className="flex items-center gap-2 text-sm">
                  <input type="checkbox" className="size-4" checked={picked.has(x.id)} onChange={(e) => toggle(x.id, e.target.checked)} />
                  <span>{x.name}</span>
                </label>
              </li>
            ))}
          </ul>
        )}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            {tg.admin.shareCancel}
          </Button>
          <Button type="button" disabled={share.isPending || tenants.isPending} onClick={() => void save()}>
            {share.isPending && <Spinner />}
            {tg.admin.shareSave}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
