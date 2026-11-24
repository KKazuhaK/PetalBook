// The check page of a space: the files that have a problem, from the index, grouped by kind. The two kinds that can
// be mended in place have a button. The code is a chunk of its own, loaded when the page is opened.

import { useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { CheckCircle2 } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { errorMessage, useActiveSpace, useChecks, useFixCheck, useSpacePermissions, useUrls } from '@/api/hooks'
import type { Problem, ProblemKind } from '@/api/types'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { TopBar } from '@/components/layout/TopBar'
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
import { Button } from '@/components/ui/button'
import { useDocumentTitle } from '@/lib/useDocumentTitle'
import { ct } from '@/i18n/checks'
import { t } from '@/i18n'

// In the order the server lists them.
const KINDS: readonly ProblemKind[] = [
  'no_id',
  'duplicate_id',
  'bad_header',
  'type_mismatch',
  'not_in_type',
  'broken_link',
  'missing_asset',
  'duplicate_title',
]
const PAGE = 50
const isKind = (v: unknown): v is ProblemKind => KINDS.some((k) => k === v)

const texts = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [])
const text = (v: unknown): string => (typeof v === 'string' ? v : '')
const lineOf = (v: unknown): number => (typeof v === 'number' && Number.isFinite(v) && v > 0 ? v : 0)
const folderOf = (path: string) => (path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : '')

export default function CheckPage() {
  const urls = useUrls()
  const { id: space, spaces } = useActiveSpace()
  const { write } = useSpacePermissions()
  const [params, setParams] = useSearchParams()
  const asked = params.get('kind')
  const kind: ProblemKind | '' = isKind(asked) ? asked : ''
  const checks = useChecks(kind)
  const fix = useFixCheck()
  // The problem whose fields are about to be removed, waiting for a yes.
  const [asking, setAsking] = useState<Problem | null>(null)
  useDocumentTitle(`${ct.pageTitle(spaces.find((s) => s.id === space)?.name ?? space ?? '')} · ${t.appName}`)

  const pick = (next: ProblemKind | '') => setParams(next ? { kind: next } : {}, { replace: true })

  const run = (p: Problem) => {
    if (!p.fix) return
    fix.mutate(
      { kind: p.fix === 'add_id' ? 'no_id' : 'not_in_type', path: p.path, baseRev: p.rev },
      {
        onSuccess: () => toast.success(p.fix === 'add_id' ? ct.fix.addIdDone(p.path) : ct.fix.removeFieldsDone(p.path)),
        onError: (e) => {
          const code = e instanceof ApiError ? (e.code as string) : ''
          toast.error(
            code === 'conflict'
              ? ct.fix.conflict
              : code === 'not_found'
                ? ct.fix.gone
                : code === 'not_fixable'
                  ? ct.fix.notFixable
                  : errorMessage(e, ct.fix.failed),
          )
        },
      },
    )
  }

  let body
  if (checks.isPending) body = <PageFallback />
  else if (checks.isError) body = <ErrorState message={ct.loadFailed} onRetry={() => void checks.refetch()} />
  else {
    const counts = checks.data.pages[0].counts
    const items = checks.data.pages.flatMap((page) => page.items)
    const total = KINDS.reduce((n, k) => n + (counts[k] ?? 0), 0)
    const shown = kind ? (counts[kind] ?? 0) : total
    const fixable = (counts.no_id ?? 0) + (counts.not_in_type ?? 0)
    // The listing is by kind, so the runs of one kind are the groups.
    const groups: { kind: ProblemKind; items: Problem[] }[] = []
    for (const p of items) {
      const last = groups[groups.length - 1]
      if (last && last.kind === p.kind) last.items.push(p)
      else groups.push({ kind: p.kind, items: [p] })
    }
    const busy = fix.isPending
    body =
      total === 0 ? (
        <div className="flex flex-col items-center gap-2 py-16 text-center" data-testid="check-empty">
          <CheckCircle2 className="size-8 text-muted-foreground" />
          <p className="text-sm font-medium">{ct.empty}</p>
          <p className="text-xs text-muted-foreground">{ct.emptyHelp}</p>
        </div>
      ) : (
        <>
          <p className="text-sm text-muted-foreground">{ct.intro}</p>
          <nav aria-label={ct.filterLabel} className="flex flex-wrap gap-1.5" data-testid="check-summary">
            <Chip active={kind === ''} onClick={() => pick('')} label={ct.all} count={total} kind="all" />
            {KINDS.filter((k) => (counts[k] ?? 0) > 0 || k === kind).map((k) => (
              <Chip
                key={k}
                active={kind === k}
                onClick={() => pick(k)}
                label={ct.kinds[k].title}
                count={counts[k] ?? 0}
                kind={k}
              />
            ))}
          </nav>
          {!write && fixable > 0 && (
            <p className="rounded-lg border p-3 text-xs text-muted-foreground">{ct.fix.readOnly}</p>
          )}
          {shown === 0 && kind !== '' && (
            <div className="flex flex-col items-center gap-3 py-10 text-center">
              <p className="text-sm text-muted-foreground">{ct.emptyKind}</p>
              <Button variant="outline" size="sm" onClick={() => pick('')}>
                {ct.showAll}
              </Button>
            </div>
          )}
          {groups.map((g) => (
            <Group key={g.kind} kind={g.kind} count={counts[g.kind] ?? g.items.length}>
              {g.items.map((p, i) => (
                <Row
                  key={`${p.kind}:${p.path}:${i}`}
                  problem={p}
                  pageUrl={urls.page}
                  folderUrl={urls.folder}
                  canFix={write}
                  working={busy && fix.variables?.path === p.path}
                  disabled={busy}
                  onFix={(x) => (x.fix === 'remove_fields' ? setAsking(x) : run(x))}
                />
              ))}
            </Group>
          ))}
          {checks.hasNextPage && (
            <div className="flex flex-col items-center gap-2 pt-1">
              {checks.isFetchNextPageError && <p className="text-xs text-destructive">{ct.moreFailed}</p>}
              <Button
                variant="outline"
                disabled={checks.isFetchingNextPage}
                onClick={() => void checks.fetchNextPage()}
              >
                {checks.isFetchingNextPage ? (
                  <>
                    <Spinner />
                    {ct.moreLoading}
                  </>
                ) : checks.isFetchNextPageError ? (
                  ct.retry
                ) : (
                  ct.more(Math.max(1, Math.min(PAGE, shown - items.length)))
                )}
              </Button>
            </div>
          )}
        </>
      )
  }

  const fields = asking ? texts(asking.detail?.fields).join(', ') : ''
  return (
    <>
      <TopBar>
        <h1 className="truncate text-sm font-medium">{ct.title}</h1>
        {checks.data && (
          <span className="shrink-0 text-xs text-muted-foreground">
            {ct.summary(KINDS.reduce((n, k) => n + (checks.data.pages[0].counts[k] ?? 0), 0))}
          </span>
        )}
      </TopBar>
      <div className="mx-auto w-full max-w-[760px] min-w-0 flex-1 space-y-5 px-4 py-6 sm:px-5">{body}</div>
      <AlertDialog open={asking !== null} onOpenChange={(open) => !open && setAsking(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{ct.fix.confirmTitle}</AlertDialogTitle>
            <AlertDialogDescription className="break-words">
              {asking ? ct.fix.confirmHelp(asking.path, fields) : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{ct.fix.cancel}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (asking) run(asking)
                setAsking(null)
              }}
            >
              {ct.fix.confirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function Chip({
  active,
  onClick,
  label,
  count,
  kind,
}: {
  active: boolean
  onClick: () => void
  label: string
  count: number
  kind: string
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      data-chip={kind}
      onClick={onClick}
      className={`inline-flex max-w-full items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 ${active ? 'border-brand bg-brand/10 font-medium' : 'hover:bg-muted/50'}`}
    >
      <span className="truncate">{label}</span>
      <span className="tabular-nums text-muted-foreground">{count}</span>
    </button>
  )
}

function Group({ kind, count, children }: { kind: ProblemKind; count: number; children: React.ReactNode }) {
  const id = `check-${kind}`
  return (
    <section aria-labelledby={id} data-kind={kind} className="grid gap-2">
      <div className="grid gap-0.5">
        <h2 id={id} className="flex items-baseline gap-2 text-sm font-semibold">
          <span>{ct.kinds[kind].title}</span>
          <span className="text-xs font-normal tabular-nums text-muted-foreground">{count}</span>
        </h2>
        <p className="text-xs text-muted-foreground">{ct.kinds[kind].help}</p>
      </div>
      <ul className="divide-y divide-border/60 rounded-lg border">{children}</ul>
    </section>
  )
}

const linkClass = 'break-all underline-offset-2 hover:underline focus-visible:underline'

function Row({
  problem: p,
  pageUrl,
  folderUrl,
  canFix,
  working,
  disabled,
  onFix,
}: {
  problem: Problem
  pageUrl: (id: string) => string
  folderUrl: (path: string) => string
  canFix: boolean
  working: boolean
  disabled: boolean
  onFix: (p: Problem) => void
}) {
  // A file that is a page opens in the editor; one that is not (no id, a header that cannot be read, an id another
  // file has) can only be shown in its folder.
  const isPage = !!p.id && p.kind !== 'duplicate_id'
  const to = isPage ? pageUrl(p.id!) : folderUrl(folderOf(p.path))
  const d = p.detail ?? {}
  return (
    <li data-problem={p.kind} data-path={p.path} className="grid min-w-0 gap-1.5 px-3 py-2.5">
      <div className="flex min-w-0 flex-wrap items-start justify-between gap-x-3 gap-y-1.5">
        <Link
          to={to}
          title={isPage ? ct.openPage : ct.openFolder}
          className={`min-w-0 text-sm font-medium ${linkClass}`}
        >
          {p.path}
        </Link>
        {canFix && p.fix && (
          <Button variant="outline" size="sm" disabled={disabled} data-fix={p.fix} onClick={() => onFix(p)}>
            {working && <Spinner />}
            {working ? ct.fix.working : p.fix === 'add_id' ? ct.fix.addId : ct.fix.removeFields}
          </Button>
        )}
      </div>
      <Detail problem={p} detail={d} pageUrl={pageUrl} folderUrl={folderUrl} />
    </li>
  )
}

function Detail({
  problem: p,
  detail: d,
  pageUrl,
  folderUrl,
}: {
  problem: Problem
  detail: Record<string, unknown>
  pageUrl: (id: string) => string
  folderUrl: (path: string) => string
}) {
  const muted = 'text-xs text-muted-foreground'
  const code = 'rounded bg-muted px-1.5 py-0.5 font-mono text-xs break-all'
  switch (p.kind) {
    case 'no_id':
      return null
    case 'bad_header': {
      const line = lineOf(d.line)
      return (
        <div className="grid gap-1">
          {line > 0 && <p className={muted}>{ct.line(line)}</p>}
          {p.message && (
            <p className={muted}>
              {ct.header}: <span className="font-mono break-words">{p.message}</span>
            </p>
          )}
        </div>
      )
    }
    case 'type_mismatch':
    case 'not_in_type': {
      const fields = texts(d.fields)
      return (
        <div className="flex min-w-0 flex-wrap items-center gap-1.5">
          <span className={muted}>{ct.fields}</span>
          {fields.length > 0 ? (
            fields.map((f) => (
              <code key={f} className={code}>
                {f}
              </code>
            ))
          ) : (
            <span className={muted}>{p.message}</span>
          )}
        </div>
      )
    }
    case 'broken_link': {
      const line = lineOf(d.line)
      const target = text(d.target)
      return (
        <p className={`${muted} break-words`}>
          {ct.target} <code className={code}>{target || p.message}</code>
          {line > 0 && <> · {ct.line(line)}</>}
        </p>
      )
    }
    case 'missing_asset': {
      const target = text(d.target)
      return (
        <p className={`${muted} break-words`}>
          {ct.target} <code className={code}>{target || p.message}</code>
        </p>
      )
    }
    case 'duplicate_id':
    case 'duplicate_title': {
      const paths = texts(d.paths)
      return (
        <div className="grid gap-1">
          {p.kind === 'duplicate_id' && p.id && (
            <p className={muted}>
              <Link to={pageUrl(p.id)} className={linkClass}>
                {ct.sharedId(p.id)}
              </Link>
            </p>
          )}
          {paths.length > 0 && (
            <>
              <p className={muted}>{ct.files}</p>
              <ul className="grid gap-0.5">
                {paths.map((x) => (
                  <li key={x} className="min-w-0 text-xs">
                    <Link to={folderUrl(folderOf(x))} title={ct.openFolder} className={linkClass}>
                      {x}
                    </Link>
                  </li>
                ))}
              </ul>
            </>
          )}
        </div>
      )
    }
  }
}
