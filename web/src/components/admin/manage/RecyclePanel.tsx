import { useEffect, useState } from 'react'
import { Download, RotateCcw, Search, Trash2 } from 'lucide-react'
import { Link } from 'react-router'
import { ApiError } from '@/api/client'
import {
  exportDownloadUrl,
  useConfirm,
  useDeleteExport,
  useExports,
  usePurge,
  useRecycle,
  useRestore,
} from '@/api/manage'
import type { ConfirmAction, RecycleItem, RestoreResult, User } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
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
import { NativeSelect } from '@/components/ui/native-select'
import { formatBytes } from '@/lib/bytes'
import { spaceUrls } from '@/lib/urls'
import { absoluteTime } from '@/lib/time'
import { useDebounced } from '@/lib/useDebounced'
import { tg } from '@/i18n/manage'
import { BulkBar } from './BulkBar'
import { BulkDialog } from './BulkDialog'
import { ExportDialog } from './ExportDialog'
import { JobWatch } from './JobProgress'
import { daysLeft, ListFooter, manageError, UserPicker, useIsSite, tapTarget, fitScreen } from './shared'

const nameOf = (i: RecycleItem) => (i.kind === 'user' ? (i.user?.username ?? '') : (i.space?.name ?? ''))

// The recycle bin: what was deleted, how many days it has left, and what can be done with it; then the exports.
export function RecyclePanel() {
  return (
    <div className="space-y-10">
      <Bin />
      <ExportsSection />
    </div>
  )
}

// The bin of the Users section, and the one of the Spaces section: the same list, opened on one kind. The exports (of
// spaces) are with the spaces.
export function DeletedPeople() {
  return <Bin initialKind="user" />
}
export function DeletedSpaces() {
  return (
    <div className="space-y-10">
      <Bin initialKind="space" />
      <ExportsSection />
    </div>
  )
}

function Bin({ initialKind = '' }: { initialKind?: '' | 'user' | 'space' }) {
  const site = useIsSite()
  const [text, setText] = useState('')
  const q = useDebounced(text.trim(), 300)
  const [kind, setKind] = useState<string>(initialKind)
  const list = useRecycle({ q: q || undefined, kind: kind || undefined })
  const items = list.data?.pages.flatMap((p) => p.items) ?? []
  const [restoring, setRestoring] = useState<RecycleItem | null>(null)
  const [purge, setPurge] = useState<RecycleItem | null>(null)
  const [exporting, setExporting] = useState<RecycleItem | null>(null)
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [bulk, setBulk] = useState<ConfirmAction | null>(null)
  const toggle = (id: string) =>
    setPicked((prev) => {
      const next = new Set(prev)
      if (!next.delete(id)) next.add(id)
      return next
    })
  const allPicked = items.length > 0 && items.every((i) => picked.has(i.id))

  return (
    <SettingsSection title={tg.recycle.title} id="recycle-heading" description={tg.recycle.help}>
      <div className="flex flex-wrap items-center gap-3">
        <div className="relative w-full max-w-xs min-w-0">
          <Search
            aria-hidden="true"
            className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
          />
          <Input
            type="search"
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder={tg.recycle.search}
            aria-label={tg.recycle.search}
            className="pl-8"
            autoComplete="off"
          />
        </div>
        <NativeSelect
          aria-label={tg.people.filter}
          value={kind}
          onChange={(e) => setKind(e.target.value)}
          wrapperClassName="w-48 max-sm:w-full"
          className="h-10"
        >
          <option value="">{tg.recycle.kindAll}</option>
          <option value="space">{tg.recycle.kinds.space}</option>
          <option value="user">{tg.recycle.kinds.user}</option>
        </NativeSelect>
      </div>
      <BulkBar
        count={picked.size}
        actions={site ? ['restore', 'purge'] : ['restore']}
        onAction={setBulk}
        onClear={() => setPicked(new Set())}
      />
      {list.isPending ? (
        <PageFallback />
      ) : list.isError && !list.data ? (
        <ErrorState message={tg.recycle.loadFailed} onRetry={() => void list.refetch()} />
      ) : items.length === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground">{q ? tg.recycle.noMatch(q) : tg.recycle.empty}</p>
      ) : (
        <>
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <input
              type="checkbox"
              aria-label={tg.recycle.selectAll}
              checked={allPicked}
              onChange={() => setPicked(allPicked ? new Set() : new Set(items.map((i) => i.id)))}
              className="size-4 accent-[var(--color-primary)]"
            />
          </div>
          <ul className="divide-y rounded-lg border" data-slot="recycle-list">
            {items.map((i) => {
              const left = daysLeft(i.purgeAt)
              const beingPurged = i.state === 'purging'
              return (
                <li
                  key={i.id}
                  className="flex flex-wrap items-start gap-x-4 gap-y-2 px-3 py-3 transition-colors hover:bg-muted/40"
                  data-item={i.id}
                  data-kind={i.kind}
                >
                  <input
                    type="checkbox"
                    aria-label={tg.recycle.selectFor(nameOf(i))}
                    checked={picked.has(i.id)}
                    onChange={() => toggle(i.id)}
                    className="mt-1 size-4 accent-[var(--color-primary)]"
                  />
                  <div className="min-w-0 flex-1 basis-56">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="truncate text-sm font-medium">{nameOf(i)}</span>
                      <Badge variant="outline">{tg.recycle.kinds[i.kind]}</Badge>
                      {i.space?.personal && <Badge variant="outline">{tg.recycle.personal}</Badge>}
                      <Badge
                        variant={left <= 3 ? 'destructive' : 'secondary'}
                        data-slot="days-left"
                        title={tg.recycle.leftOn(absoluteTime(i.purgeAt))}
                      >
                        {tg.recycle.left(left)}
                      </Badge>
                    </div>
                    <div className="text-xs text-muted-foreground">
                      {formatBytes(i.bytes)} · {tg.recycle.deletedBy(i.deletedBy.username, absoluteTime(i.deletedAt))}
                      {i.with.length > 0 && ` · ${tg.recycle.with(i.with.length)}`}
                    </div>
                    {i.reason && <div className="text-xs text-muted-foreground">{tg.recycle.reason(i.reason)}</div>}
                    {!i.restorable && !beingPurged && (
                      <div className="text-xs text-warning">
                        {tg.recycle.notRestorable}：{i.problems.join('；')}
                      </div>
                    )}
                    {beingPurged && (
                      <div role="status" className="text-xs text-muted-foreground">
                        {tg.recycle.purging}
                      </div>
                    )}
                    {i.job && i.state !== 'ready' && (
                      <div className="mt-1 max-w-sm" data-slot="item-job">
                        <JobWatch id={i.job} />
                      </div>
                    )}
                  </div>
                  <div className="flex flex-wrap items-center gap-1">
                    <Button
                      className={tapTarget}
                      type="button"
                      variant="outline"
                      size="sm"
                      aria-label={tg.recycle.restoreFor(nameOf(i))}
                      disabled={!i.restorable || beingPurged}
                      onClick={() => setRestoring(i)}
                    >
                      <RotateCcw aria-hidden="true" />
                      {tg.recycle.restore}
                    </Button>
                    {i.kind === 'space' && (
                      <Button
                        className={tapTarget}
                        type="button"
                        variant="ghost"
                        size="sm"
                        disabled={beingPurged}
                        onClick={() => setExporting(i)}
                      >
                        <Download aria-hidden="true" />
                        {tg.recycle.export}
                      </Button>
                    )}
                    {site && (
                      <Button
                        className={tapTarget}
                        type="button"
                        variant="ghost"
                        size="sm"
                        aria-label={tg.recycle.purgeFor(nameOf(i))}
                        disabled={beingPurged}
                        onClick={() => setPurge(i)}
                      >
                        <Trash2 aria-hidden="true" />
                        {tg.recycle.purge}
                      </Button>
                    )}
                  </div>
                </li>
              )
            })}
          </ul>
          <ListFooter
            shown={items.length}
            more={!!list.hasNextPage}
            loading={list.isFetchingNextPage}
            onMore={() => void list.fetchNextPage()}
          />
        </>
      )}
      {restoring && <RestoreDialog item={restoring} onClose={() => setRestoring(null)} />}
      {purge && <PurgeDialog item={purge} onClose={() => setPurge(null)} />}
      {exporting && (
        <ExportDialog request={{ items: [exporting.id] }} name={nameOf(exporting)} onClose={() => setExporting(null)} />
      )}
      {bulk && (
        <BulkDialog
          action={bulk}
          label={tg.bulk.actions[bulk]}
          request={{ items: [...picked] }}
          onClose={() => {
            setBulk(null)
            setPicked(new Set())
          }}
        />
      )}
    </SettingsSection>
  )
}

// Restoring: what comes back, what is left out; a space with no owner left needs a new one named.
function RestoreDialog({ item, onClose }: { item: RecycleItem; onClose: () => void }) {
  const restore = useRestore()
  const [owner, setOwner] = useState<User | null>(null)
  const [needsOwner, setNeedsOwner] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState<RestoreResult | null>(null)
  const name = nameOf(item)
  const go = () => {
    setError('')
    restore.mutate(
      { item: item.id, ...(owner ? { owner: owner.id } : {}) },
      {
        onSuccess: setDone,
        onError: (e) => {
          if (e instanceof ApiError && e.code === 'owner_required') setNeedsOwner(true)
          setError(manageError(e))
        },
      },
    )
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className={`${fitScreen} sm:max-w-lg`} data-slot="restore-dialog">
        {done ? (
          <div className="grid gap-4">
            <DialogHeader>
              <DialogTitle>{tg.recycle.restoreDone(name)}</DialogTitle>
              {done.restored.spaces && (
                <DialogDescription>{tg.recycle.restoreSpaces(done.restored.spaces.length)}</DialogDescription>
              )}
            </DialogHeader>
            {done.skipped.length > 0 && (
              <section
                role="alert"
                className="grid gap-1 rounded-lg border border-warning/50 bg-warning/5 p-3 text-sm"
                data-slot="restore-skipped"
              >
                <h4 className="font-medium">{tg.recycle.restoreSkipped}</h4>
                <ul className="list-disc pl-5 text-xs">
                  {done.skipped.map((s, i) => (
                    <li key={i}>
                      {s.user
                        ? tg.recycle.skippedUser(s.name)
                        : s.group
                          ? tg.recycle.skippedGroup(s.name)
                          : tg.recycle.skippedSpace(s.name, s.reason)}
                    </li>
                  ))}
                </ul>
              </section>
            )}
            {(done.transfers?.length ?? 0) > 0 && (
              <section className="grid gap-1 text-sm" data-slot="restore-transfers">
                <h4 className="font-medium">{tg.recycle.restoreTransfers}</h4>
                <ul className="list-disc pl-5 text-xs">
                  {done.transfers!.map((t) => (
                    <li key={t.space}>
                      {t.name} → {t.toUsername}
                    </li>
                  ))}
                </ul>
              </section>
            )}
            {(done.left?.length ?? 0) > 0 && (
              <section className="grid gap-1 text-sm" data-slot="restore-left">
                <h4 className="font-medium">{tg.recycle.restoreLeft}</h4>
                <ul className="list-disc pl-5 text-xs">
                  {done.left!.map((l) => (
                    <li key={l.space}>{l.name}</li>
                  ))}
                </ul>
              </section>
            )}
            <DialogFooter>
              <Button className={tapTarget} type="button" onClick={onClose}>
                {tg.bulk.close}
              </Button>
            </DialogFooter>
          </div>
        ) : (
          <div className="grid gap-4">
            <DialogHeader>
              <DialogTitle>{tg.recycle.restoreTitle(name)}</DialogTitle>
              <DialogDescription>
                {item.kind === 'user' ? tg.recycle.restoreUserHelp : tg.recycle.restoreHelp}
              </DialogDescription>
            </DialogHeader>
            {needsOwner && (
              <div className="grid gap-1" data-slot="restore-owner">
                <p className="text-sm">{tg.recycle.restoreOwnerHelp}</p>
                <UserPicker label={tg.recycle.restoreOwner} value={owner} onChange={setOwner} />
              </div>
            )}
            <FormMessage>{error}</FormMessage>
            <DialogFooter>
              <Button className={tapTarget} type="button" variant="outline" onClick={onClose}>
                {tg.cancel}
              </Button>
              <Button
                className={tapTarget}
                type="button"
                disabled={restore.isPending || (needsOwner && !owner)}
                onClick={go}
              >
                {restore.isPending && <Spinner />}
                {tg.recycle.restoreConfirm}
              </Button>
            </DialogFooter>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

// Purging early, for site administrators: a fresh confirmation each time the dialog opens, then the name typed.
function PurgeDialog({ item, onClose }: { item: RecycleItem; onClose: () => void }) {
  const confirm = useConfirm()
  const purge = usePurge()
  const [token, setToken] = useState<string | null>(null)
  const [typed, setTyped] = useState('')
  const [error, setError] = useState('')
  const [started, setStarted] = useState(false)
  const name = nameOf(item)
  const { mutate: ask } = confirm
  const look = () => {
    setError('')
    setToken(null)
    ask(
      { action: 'purge', items: [item.id] },
      {
        onSuccess: (r) => setToken(r.confirm),
        onError: (e) => setError(manageError(e)),
      },
    )
  }
  // A fresh confirmation each time the dialog opens.
  useEffect(() => {
    ask(
      { action: 'purge', items: [item.id] },
      {
        onSuccess: (r) => setToken(r.confirm),
        onError: (e) => setError(manageError(e)),
      },
    )
  }, [ask, item.id])
  const go = () => {
    if (!token) return
    setError('')
    purge.mutate(
      { item: item.id, confirm: token },
      {
        onSuccess: () => {
          setStarted(true)
        },
        onError: (e) => {
          if (e instanceof ApiError && (e.code === 'confirm_stale' || e.status === 404)) {
            setToken(null)
            setError(`${manageError(e)} ${tg.bulk.lookAgain}`)
          } else setError(manageError(e))
        },
      },
    )
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className={`${fitScreen} sm:max-w-md`} data-slot="purge-dialog">
        <DialogHeader>
          <DialogTitle>{tg.recycle.purgeTitle(name)}</DialogTitle>
          <DialogDescription>{tg.recycle.purgeHelp}</DialogDescription>
        </DialogHeader>
        {started ? (
          <p role="status" className="text-sm">
            {tg.recycle.purgeStarted}
          </p>
        ) : (
          <div className="grid gap-3">
            <p className="text-xs text-muted-foreground">{tg.recycle.purgeBytes(formatBytes(item.bytes))}</p>
            <div className="grid gap-1.5">
              <Label htmlFor="purge-name">{tg.recycle.purgeType}</Label>
              <code className="rounded-md bg-muted px-2 py-1 text-sm break-all select-all">{name}</code>
              <Input
                id="purge-name"
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                autoComplete="off"
                spellCheck={false}
              />
            </div>
            {!token && !error && <p className="text-xs text-muted-foreground">{tg.recycle.purgeChecking}</p>}
            <FormMessage>{error}</FormMessage>
            {!token && error && (
              <Button type="button" variant="outline" size="sm" className={`w-fit ${tapTarget}`} onClick={look}>
                {tg.bulk.lookAgain}
              </Button>
            )}
          </div>
        )}
        <DialogFooter>
          {started ? (
            <Button className={tapTarget} type="button" onClick={onClose}>
              {tg.bulk.close}
            </Button>
          ) : (
            <>
              <Button className={tapTarget} type="button" variant="outline" onClick={onClose}>
                {tg.cancel}
              </Button>
              <Button
                className={tapTarget}
                type="button"
                variant="destructive"
                disabled={!token || purge.isPending || typed.trim().normalize('NFC') !== name.normalize('NFC')}
                onClick={go}
              >
                {purge.isPending && <Spinner />}
                {tg.recycle.purgeConfirm}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ExportsSection() {
  const list = useExports()
  const del = useDeleteExport()
  const items = list.data?.pages.flatMap((p) => p.items) ?? []
  return (
    <SettingsSection title={tg.exports.title} id="exports-heading" description={tg.exports.help}>
      {list.isPending ? (
        <PageFallback />
      ) : list.isError && !list.data ? (
        <ErrorState message={tg.exports.loadFailed} onRetry={() => void list.refetch()} />
      ) : items.length === 0 ? (
        <p className="py-4 text-center text-sm text-muted-foreground">{tg.exports.empty}</p>
      ) : (
        <ul className="divide-y rounded-lg border" data-slot="export-list">
          {items.map((e) => (
            <li
              key={e.id}
              className="flex flex-wrap items-start gap-x-4 gap-y-2 px-3 py-3 transition-colors hover:bg-muted/40"
              data-export={e.id}
              data-state={e.state}
            >
              <div className="min-w-0 flex-1 basis-56">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="truncate text-sm font-medium">
                    {e.spaces.map((s, i) => (
                      <span key={`${s.spaceId}:${i}`}>
                        {i > 0 && '、'}
                        {s.open ? (
                          <Link
                            to={spaceUrls(s.spaceId).home()}
                            className="underline underline-offset-4 hover:text-primary"
                            data-slot="export-space-link"
                          >
                            {s.name || s.spaceId}
                          </Link>
                        ) : (
                          s.name || s.spaceId
                        )}
                      </span>
                    ))}
                  </span>
                  <Badge variant={e.state === 'failed' ? 'destructive' : e.state === 'ready' ? 'secondary' : 'outline'}>
                    {tg.exports.state[e.state]}
                  </Badge>
                  {e.history && <Badge variant="outline">{tg.exports.history}</Badge>}
                </div>
                <div className="text-xs text-muted-foreground">
                  {e.state === 'ready' && `${formatBytes(e.bytes)} · `}
                  <span data-slot="export-expires">{tg.exports.expires(daysLeft(e.expires))}</span> ·{' '}
                  {tg.exports.madeBy(e.madeBy.username, absoluteTime(e.created))}
                </div>
                {(e.state === 'queued' || e.state === 'running') && (
                  <div className="mt-1 max-w-sm" data-slot="export-job">
                    <JobWatch id={e.id} />
                  </div>
                )}
                {e.state === 'failed' && e.error && <div className="text-xs text-destructive">{e.error}</div>}
                {e.warnings.length > 0 && (
                  <div className="text-xs text-warning" data-slot="export-warnings">
                    {tg.exports.warnings}：{e.warnings.join('；')}
                  </div>
                )}
              </div>
              <div className="flex items-center gap-1">
                {e.state === 'ready' && (
                  <Button
                    className={tapTarget}
                    variant="outline"
                    size="sm"
                    render={<a href={exportDownloadUrl(e.id)} download aria-label={tg.exports.downloadFor(e.id)} />}
                  >
                    <Download aria-hidden="true" />
                    {tg.exports.download}
                  </Button>
                )}
                <Button
                  className={tapTarget}
                  type="button"
                  variant="ghost"
                  size="sm"
                  aria-label={tg.exports.deleteFor(e.id)}
                  disabled={del.isPending}
                  onClick={() => del.mutate(e.id)}
                >
                  <Trash2 aria-hidden="true" />
                  {tg.exports.delete}
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </SettingsSection>
  )
}
