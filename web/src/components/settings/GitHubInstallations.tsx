import { useEffect, useId, useRef, useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router'
import { CircleAlert, ExternalLink, Lock, Plus, Unlink } from 'lucide-react'
import { useMutation } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { useProvider } from '@/api/hooks'
import {
  githubInstallsApi,
  useDisconnectGitHubInstallation,
  useGitHubInstallationRepos,
  useGitHubInstallations,
  useGitHubInstallationSpaces,
} from '@/api/githubInstalls'
import type { GitHubInstallation, GitHubInstallationsOf, GitHubPendingClaim } from '@/api/types'
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
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useFeature } from '@/lib/features'
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'
import { tg } from '@/i18n/github'
import { SettingsNote, SettingsSection } from './SettingsParts'

// The installations of the site's GitHub App that belong to one owner (docs/api.md, "The GitHub provider",
// "Installations"): the person's own, in their settings, and the organisation's, in its console. Both offer "Install
// on GitHub", show each installation with its owner, the spaces on it and its repositories, and disconnect it after
// saying which spaces lose GitHub access. The server decides who may: this only shows what it allows.
export function GitHubInstallations({ of, returnTo }: { of: GitHubInstallationsOf; returnTo: string }) {
  const c = tg.installs
  const provider = useProvider('github')
  const configured = provider.data?.configured === true
  return (
    <SettingsSection
      title={of === 'mine' ? c.titleMine : c.titleOrg}
      id={`github-installs-${of}-title`}
      description={of === 'mine' ? c.introMine : c.introOrg}
      icon={<BrandIcon type="github" className="size-4" />}
    >
      {of === 'org' && <Returned />}
      {provider.isPending ? null : !configured ? (
        <SettingsNote>{c.notConfigured}</SettingsNote>
      ) : (
        <>
          <Install of={of} returnTo={returnTo} />
          <List of={of} />
        </>
      )}
    </SettingsSection>
  )
}

const orgName = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$/

// "Install on GitHub": to the App's installation page; GitHub sends the browser back here. An organisation names its
// GitHub organisation first: only an installation there is connected to it.
function Install({ of, returnTo }: { of: GitHubInstallationsOf; returnTo: string }) {
  const c = tg.installs
  const on = useFeature('features.githubConnect')
  const [account, setAccount] = useState('')
  const [error, setError] = useState('')
  const field = useId()
  const start = useMutation({
    mutationFn: () => githubInstallsApi.start(of, returnTo, of === 'org' ? account.trim() : undefined),
    onSuccess: ({ url }) => window.location.assign(url),
    onError: (e) => setError(e instanceof ApiError ? e.message : c.startFailed),
  })
  if (on === false) return <SettingsNote>{c.off}</SettingsNote>
  const submit = (e: FormEvent) => {
    e.preventDefault()
    setError('')
    if (of === 'org' && !orgName.test(account.trim())) {
      setError(c.accountInvalid)
      return
    }
    start.mutate()
  }
  return (
    <form className="grid grid-cols-1 gap-2" onSubmit={submit} noValidate>
      {of === 'org' && (
        <div className="grid grid-cols-1 gap-1.5">
          <Label htmlFor={field}>{c.account}</Label>
          <Input
            id={field}
            value={account}
            onChange={(e) => setAccount(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            aria-describedby={`${field}-help`}
            className="max-w-sm"
          />
          <p id={`${field}-help`} className="text-xs text-muted-foreground">
            {c.accountHelp}
          </p>
        </div>
      )}
      <div>
        <Button type="submit" disabled={start.isPending}>
          {start.isPending ? <Spinner /> : <Plus aria-hidden="true" />}
          {start.isPending ? c.installing : c.install}
        </Button>
      </div>
      <FormMessage>{error}</FormMessage>
    </form>
  )
}

// `?connection_error=` on the organisation's console, from an installation that was not connected: shown once, then
// taken out of the address. (The person's own come back to their settings, which say it there.) `?connection_pending=`
// says the claim waits for GitHub: the list below shows it as connecting, so it is only taken out.
function Returned() {
  const [params, setParams] = useSearchParams()
  const [code] = useState(() => params.get('connection_error') ?? '')
  const ref = useRef<HTMLParagraphElement>(null)
  useEffect(() => {
    if (params.has('connection_error') || params.has('connection_pending'))
      setParams(
        (p) => {
          const next = new URLSearchParams(p)
          next.delete('connection_error')
          next.delete('connection_pending')
          return next
        },
        { replace: true },
      )
    ref.current?.focus()
    // Once, on arrival.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  if (!code) return null
  const r = tg.installs.returned as Record<string, string>
  return <FormMessage focusRef={ref}>{Object.hasOwn(r, code) ? r[code] : r.unknown}</FormMessage>
}

function List({ of }: { of: GitHubInstallationsOf }) {
  const c = tg.installs
  const list = useGitHubInstallations(of)
  if (list.isPending) return <PageFallback />
  if (list.isError)
    return <ErrorState message={t.fmt.labelled(c.loadFailed, list.error.message)} onRetry={() => void list.refetch()} />
  const pending = list.data.pending.filter((p) => !list.data.items.some((i) => i.id === p.id))
  if (list.data.items.length === 0 && pending.length === 0)
    return <p className="text-sm text-muted-foreground">{c.none}</p>
  return (
    <ul className="grid grid-cols-1 gap-3">
      {pending.map((p) => (
        <PendingRow key={`pending-${p.id}`} claim={p} />
      ))}
      {list.data.items.map((i) => (
        <Row key={i.id} of={of} item={i} />
      ))}
    </ul>
  )
}

// A claim that waits for GitHub's signed event to say who installed the installation ("connecting"; the list is read
// again until it is connected or the claim expires), or that failed when the event came.
function PendingRow({ claim: p }: { claim: GitHubPendingClaim }) {
  const c = tg.installs
  const r = c.returned as Record<string, string>
  const failed = p.error !== ''
  return (
    <li className="min-w-0 rounded-md border border-dashed p-3" data-pending={p.id}>
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <span className="min-w-0 font-medium break-all">{p.account}</span>
        <Badge variant={failed ? 'destructive' : 'outline'}>{failed ? c.pendingFailed : c.pending}</Badge>
      </div>
      {failed ? (
        <p className="mt-1 flex items-start gap-1.5 text-xs break-words text-destructive">
          <CircleAlert className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
          {Object.hasOwn(r, p.error) ? r[p.error] : r.unknown}
        </p>
      ) : (
        <p className="mt-1 flex items-start gap-1.5 text-xs break-words text-muted-foreground" role="status">
          <Spinner className="mt-0.5 size-3.5 shrink-0" />
          {c.pendingHelp(absoluteTime(p.expires))}
        </p>
      )}
    </li>
  )
}

function Row({ of, item: i }: { of: GitHubInstallationsOf; item: GitHubInstallation }) {
  const c = tg.installs
  const [repos, setRepos] = useState(false)
  const [confirm, setConfirm] = useState(false)
  return (
    <li className="min-w-0 rounded-md border p-3" data-installation={i.id}>
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <span className="min-w-0 font-medium break-all">{i.account}</span>
        <Badge variant="secondary">{i.owner.kind === 'person' ? c.ownerPerson : c.ownerOrg}</Badge>
      </div>
      <p className="mt-1 text-xs text-muted-foreground">
        {c.connectedAt(absoluteTime(i.connected))} · {c.spaces(i.spaces)}
      </p>
      <div className="mt-2 flex flex-wrap items-center gap-2">
        <Button type="button" variant="outline" size="sm" aria-expanded={repos} onClick={() => setRepos((v) => !v)}>
          {repos ? c.hideRepos : c.repos}
        </Button>
        <Button variant="ghost" size="sm" render={<a href={i.manageUrl} target="_blank" rel="noreferrer noopener" />}>
          {c.manage}
          <ExternalLink aria-hidden="true" />
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="text-destructive"
          aria-label={c.disconnectFor(i.account)}
          onClick={() => setConfirm(true)}
        >
          <Unlink aria-hidden="true" />
          {c.disconnect}
        </Button>
      </div>
      {repos && <Repos of={of} id={i.id} />}
      {confirm && <Disconnect of={of} item={i} onClose={() => setConfirm(false)} />}
    </li>
  )
}

function Repos({ of, id }: { of: GitHubInstallationsOf; id: number }) {
  const c = tg.installs
  const q = useGitHubInstallationRepos(of, id, true)
  if (q.isPending) return <PageFallback />
  if (q.isError)
    return <ErrorState message={t.fmt.labelled(c.reposFailed, q.error.message)} onRetry={() => void q.refetch()} />
  const items = q.data.pages.flatMap((p) => p.items)
  if (items.length === 0) return <p className="mt-2 text-sm text-muted-foreground">{c.noRepos}</p>
  return (
    <div className="mt-2 grid grid-cols-1 gap-2">
      <ul className="grid grid-cols-1 gap-1 text-sm">
        {items.map((r) => (
          <li key={r.id} className="flex min-w-0 items-center gap-1.5">
            <a
              className="min-w-0 break-all underline-offset-4 hover:underline"
              href={r.htmlUrl}
              target="_blank"
              rel="noreferrer noopener"
            >
              {r.fullName}
            </a>
            {r.private && (
              <span className="inline-flex shrink-0 items-center gap-0.5 text-xs text-muted-foreground">
                <Lock className="size-3" aria-hidden="true" />
                {c.private}
              </span>
            )}
          </li>
        ))}
      </ul>
      {q.hasNextPage && (
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={q.isFetchingNextPage}
            onClick={() => void q.fetchNextPage()}
          >
            {q.isFetchingNextPage && <Spinner />}
            {c.moreRepos}
          </Button>
        </div>
      )}
    </div>
  )
}

// The question before disconnecting: which spaces lose GitHub access (asked of the server when it opens).
function Disconnect({
  of,
  item: i,
  onClose,
}: {
  of: GitHubInstallationsOf
  item: GitHubInstallation
  onClose: () => void
}) {
  const c = tg.installs
  const spaces = useGitHubInstallationSpaces(of, i.id, true)
  const disconnect = useDisconnectGitHubInstallation(of)
  const go = async () => {
    try {
      await disconnect.mutateAsync(i.id)
      onClose()
      toast.success(c.disconnected(i.account))
    } catch (e) {
      onClose()
      toast.error(t.fmt.labelled(c.disconnectFailed, e instanceof Error ? e.message : ''))
    }
  }
  const shown = spaces.data?.items ?? []
  const more = (spaces.data?.total ?? 0) - shown.length
  return (
    <AlertDialog open onOpenChange={(o) => !o && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{c.disconnectTitle(i.account)}</AlertDialogTitle>
          <AlertDialogDescription>{c.disconnectBody}</AlertDialogDescription>
        </AlertDialogHeader>
        <div className="grid grid-cols-1 gap-2 text-sm" aria-live="polite">
          {spaces.isPending ? (
            <p className="text-muted-foreground">{c.loadingSpaces}</p>
          ) : spaces.isError ? (
            <p className="text-destructive">{c.spacesFailed}</p>
          ) : spaces.data.total === 0 ? (
            <p className="text-muted-foreground">{c.disconnectNone}</p>
          ) : (
            <>
              <p>{c.disconnectSpaces(spaces.data.total)}</p>
              <ul className="max-h-48 list-disc overflow-y-auto ps-5" data-testid="affected-spaces">
                {shown.map((s) => (
                  <li key={s.id} className="break-all">
                    {s.name}
                  </li>
                ))}
              </ul>
              {more > 0 && <p className="text-muted-foreground">{c.disconnectMore(more)}</p>}
              <p className="text-xs text-muted-foreground">{c.disconnectGrace}</p>
            </>
          )}
        </div>
        <AlertDialogFooter>
          <AlertDialogCancel>{c.cancel}</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={spaces.isPending || spaces.isError || disconnect.isPending}
            onClick={(e) => {
              e.preventDefault()
              void go()
            }}
          >
            {disconnect.isPending && <Spinner />}
            {c.disconnectConfirm}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
