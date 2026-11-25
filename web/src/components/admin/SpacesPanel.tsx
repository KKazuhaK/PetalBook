import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { MoreHorizontal, RefreshCw, Search } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import {
  useAdminSpacesPage,
  useEvictSpace,
  usePutSpaceStorage,
  useResizeSpace,
  useSetSpaceDisabled,
  useTransferSpace,
  useUserSearch,
  USER_SEARCH_LIMIT,
} from '@/api/admin'
import type { AdminSpace, CacheState, ConfirmAction, SpaceSort, User } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { Spinner } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import {
  AlertDialog,
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { DataTable, type Column } from '@/components/ui/data-table'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { formatBytes } from '@/lib/bytes'
import { absoluteTime } from '@/lib/time'
import { tg } from '@/i18n/manage'
import { BulkBar } from './manage/BulkBar'
import { BulkDialog } from './manage/BulkDialog'
import { DeleteSpaceDialog } from './manage/DeleteSpaceDialog'
import { ExportDialog } from './manage/ExportDialog'
import { LocationBadge, useIsSite } from './manage/shared'
import { ViewDialog } from './manage/ViewDialog'
import { spaceUrls } from '@/lib/urls'
import { useCursorPages } from '@/lib/useCursorPages'
import { useDebounced } from '@/lib/useDebounced'
import { useTablePrefs } from '@/lib/tablePrefs'
import { tc } from '@/i18n/console'
import { t } from '@/i18n'
import { ta } from '@/i18n/admin'
import { ts } from '@/i18n/storage'

const isSort = (v: string): v is SpaceSort => v === 'created' || v === 'name' || v === 'bytes' || v === 'lastUsed'

type Action = { kind: 'transfer' | 'disable' | 'delete' | 'evict' | 'view' | 'export'; space: AdminSpace }

// What a refusal of an administration call means, in plain words.
function actionError(e: unknown): string {
  const text = ta.spaces.errors
  if (!(e instanceof ApiError)) return ta.spaces.failed
  switch (e.code) {
    case 'personal_space':
      return text.personalSpace
    case 'user_not_found':
      return text.userNotFound
    case 'user_disabled':
      return text.userDisabled
    case 'busy':
      return text.busy
    case 'not_found':
      return text.notFound
    default:
      return e.message || ta.spaces.failed
  }
}

// Every space on the server, a page at a time, with what an administrator may do
// to one. The list is read from stored values only: the server never walks the
// spaces to answer it, so sizes are as old as the last measurement.
export function SpacesPanel() {
  const [text, setText] = useState('')
  const q = useDebounced(text.trim(), 300)
  // What to show: all spaces, those past their deadline for a remote, or those in one cache state.
  const [filter, setFilter] = useState('')
  const site = useIsSite()
  const prefs = useTablePrefs('admin-spaces')
  const [sort, setSort] = useState<{ key: SpaceSort; order: 'asc' | 'desc' }>({ key: 'created', order: 'asc' })
  const [remote, setRemote] = useState('')
  const [idle, setIdle] = useState('')
  const [pinnedOnly, setPinnedOnly] = useState(false)
  const [exemptOnly, setExemptOnly] = useState(false)
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [bulk, setBulk] = useState<ConfirmAction | null>(null)
  const params = {
    q: q || undefined,
    cache: filter === 'overdue' ? undefined : (filter as CacheState) || undefined,
    overdue: filter === 'overdue' || undefined,
    sort: sort.key,
    order: sort.order,
    remote: (remote || undefined) as 'none' | 'ok' | 'offline' | 'error' | undefined,
    idleDays: idle ? Number(idle) : undefined,
    pinned: pinnedOnly || undefined,
    exempt: exemptOnly || undefined,
    limit: prefs.prefs.pageSize,
  }
  const pages = useCursorPages(JSON.stringify(params))
  const spaces = useAdminSpacesPage({ ...params, cursor: pages.cursor || undefined })
  const setDisabled = useSetSpaceDisabled()
  const putStorage = usePutSpaceStorage()
  const [action, setAction] = useState<Action | null>(null)

  const items = spaces.data?.items ?? []
  const next = spaces.data?.next ?? ''
  const enable = async (s: AdminSpace) => {
    try {
      await setDisabled.mutateAsync({ id: s.id, disabled: false })
      toast.success(ta.spaces.enabledDone)
    } catch (e) {
      toast.error(actionError(e))
    }
  }

  const changeStorage = async (s: AdminSpace, input: { pin?: boolean; exempt?: boolean }, done: string) => {
    try {
      await putStorage.mutateAsync({ id: s.id, ...input })
      toast.success(done)
    } catch (e) {
      toast.error(e instanceof ApiError && e.message ? e.message : ts.spaces.changeFailed)
    }
  }

  const columns = useMemo<Column<AdminSpace>[]>(
    () => [
      {
        id: 'name',
        header: ta.spaces.name,
        required: true,
        min: 180,
        weight: 3,
        sortKey: 'name',
        cell: (s) => (
          <div className="flex min-w-0 items-center gap-x-2">
            <span className="truncate font-medium">{s.name}</span>
            {s.personal && <Badge variant="outline">{ta.spaces.personal}</Badge>}
          </div>
        ),
      },
      {
        id: 'owner',
        header: ta.spaces.owner,
        min: 140,
        weight: 2,
        priority: 2,
        cell: (s) =>
          s.owner ? (
            <>
              <div className="truncate">{s.owner.name}</div>
              <div className="truncate text-xs text-muted-foreground">{s.owner.username}</div>
            </>
          ) : (
            <span className="text-muted-foreground">{ta.spaces.noOwner}</span>
          ),
      },
      {
        id: 'members',
        header: ta.spaces.members,
        min: 80,
        weight: 0.8,
        priority: 5,
        align: 'right',
        cell: (s) => s.members,
      },
      {
        id: 'size',
        header: ta.spaces.size,
        min: 100,
        weight: 1.2,
        priority: 3,
        sortKey: 'bytes',
        cell: (s) => <SizeCell space={s} />,
      },
      {
        id: 'where',
        header: tg.spaces.where,
        min: 130,
        weight: 1.5,
        priority: 6,
        cell: (s) => <LocationBadge location={s.location} counted={s.counted} />,
      },
      {
        id: 'sync',
        header: ta.spaces.sync,
        min: 100,
        weight: 1,
        priority: 7,
        cell: (s) => ta.spaces.syncStates[s.sync],
      },
      {
        id: 'status',
        header: ta.spaces.status,
        min: 90,
        weight: 1,
        priority: 2,
        cell: (s) =>
          s.disabled ? (
            <Badge variant="secondary">{ta.spaces.disabled}</Badge>
          ) : (
            <span className="text-muted-foreground">{ta.spaces.active}</span>
          ),
      },
      {
        id: 'cache',
        header: ts.spaces.cache,
        min: 170,
        weight: 2,
        priority: 4,
        cell: (s) => (
          <div data-slot="space-cache">
            <CacheCell space={s} />
          </div>
        ),
      },
      {
        id: 'created',
        header: tc.users.cols.created,
        min: 140,
        weight: 1.5,
        priority: 8,
        sortKey: 'created',
        cell: (s) => <time dateTime={s.created}>{absoluteTime(s.created)}</time>,
      },
    ],
    [],
  )

  return (
    <SettingsSection title={ta.spaces.title} id="admin-spaces-heading" description={ta.spaces.help}>
      <div className="flex flex-wrap items-center gap-3">
        <div className="relative w-full max-w-sm min-w-0">
          <Search
            aria-hidden="true"
            className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
          />
          <Input
            type="search"
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder={ta.spaces.searchPlaceholder}
            aria-label={ta.spaces.search}
            className="pl-8"
            autoComplete="off"
          />
        </div>
        <NativeSelect
          aria-label={ts.spaces.filter}
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          wrapperClassName="w-48 max-sm:w-full"
          className="h-10"
        >
          <option value="">{ts.spaces.filterAll}</option>
          <option value="overdue">{ts.spaces.filterOverdue}</option>
          {(['local', 'full', 'partial', 'evicted', 'archived'] as const).map((c) => (
            <option key={c} value={c}>
              {ts.spaces.caches[c]}
            </option>
          ))}
        </NativeSelect>
        <NativeSelect
          aria-label={tg.spaces.remote}
          value={remote}
          onChange={(e) => setRemote(e.target.value)}
          wrapperClassName="w-48 max-sm:w-full"
          className="h-10"
        >
          {(['', 'none', 'ok', 'offline', 'error'] as const).map((k) => (
            <option key={k} value={k}>
              {tg.spaces.remote}：{tg.spaces.remotes[k]}
            </option>
          ))}
        </NativeSelect>
        <NativeSelect
          aria-label={tg.spaces.idle}
          value={idle}
          onChange={(e) => setIdle(e.target.value)}
          wrapperClassName="w-48 max-sm:w-full"
          className="h-10"
        >
          {(['', '30', '90', '180'] as const).map((k) => (
            <option key={k} value={k}>
              {k === '' ? `${tg.spaces.idle}：${tg.spaces.idles['']}` : tg.spaces.idles[k]}
            </option>
          ))}
        </NativeSelect>
        <label className="flex min-h-10 items-center gap-1.5 text-xs">
          <input
            type="checkbox"
            className="size-4 accent-[var(--color-primary)]"
            checked={pinnedOnly}
            onChange={(e) => setPinnedOnly(e.target.checked)}
          />
          {tg.spaces.pinned}
        </label>
        <label className="flex min-h-10 items-center gap-1.5 text-xs">
          <input
            type="checkbox"
            className="size-4 accent-[var(--color-primary)]"
            checked={exemptOnly}
            onChange={(e) => setExemptOnly(e.target.checked)}
          />
          {tg.spaces.exempt}
        </label>
      </div>
      {!site && <p className="text-xs text-muted-foreground">{tg.spaces.personalNote}</p>}
      <DataTable
        prefs={prefs}
        columns={columns}
        rows={items}
        rowId={(s) => s.id}
        rowLabel={(s) => s.name}
        rowAttrs={(s) => ({ 'data-space': s.id })}
        loading={spaces.isPending}
        fetching={spaces.isFetching && !spaces.isPending}
        error={spaces.isError ? t.fmt.labelled(ta.spaces.loadFailed, spaces.error.message) : undefined}
        onRetry={() => void spaces.refetch()}
        empty={q ? ta.spaces.noMatch(q) : ta.spaces.empty}
        paging={{
          page: pages.page,
          hasPrev: pages.hasPrev,
          hasNext: next !== '',
          onPrev: pages.prev,
          onNext: () => pages.next(next),
        }}
        sort={sort}
        onSort={(s) => isSort(s.key) && setSort({ key: s.key, order: s.order })}
        selection={{ selected: picked, onChange: setPicked }}
        bulk={
          <BulkBar
            count={picked.size}
            actions={
              site
                ? ['delete_spaces', 'disable_spaces', 'enable_spaces', 'export', 'evict', 'pin', 'unpin']
                : ['delete_spaces', 'disable_spaces', 'enable_spaces', 'export']
            }
            onAction={setBulk}
            onClear={() => setPicked(new Set())}
          />
        }
        actions={(s) => (
          <RowActions
            space={s}
            site={site}
            onAction={(kind) => setAction({ kind, space: s })}
            onEnable={() => void enable(s)}
            onPin={() =>
              void changeStorage(
                s,
                { pin: !s.storage.pinned },
                s.storage.pinned ? ts.spaces.unpinnedDone : ts.spaces.pinnedDone,
              )
            }
            onExempt={() => void changeStorage(s, { exempt: !s.storage.exempt }, ts.spaces.exemptDone)}
          />
        )}
      />

      {action?.kind === 'transfer' && <TransferDialog space={action.space} onClose={() => setAction(null)} />}
      {action?.kind === 'disable' && <DisableDialog space={action.space} onClose={() => setAction(null)} />}
      {action?.kind === 'delete' && (
        <DeleteSpaceDialog
          id={action.space.id}
          name={action.space.name}
          personal={action.space.personal}
          onClose={() => setAction(null)}
        />
      )}
      {action?.kind === 'view' && (
        <ViewDialog space={action.space.id} name={action.space.name} onClose={() => setAction(null)} />
      )}
      {action?.kind === 'export' && (
        <ExportDialog
          request={{ spaces: [action.space.id] }}
          name={action.space.name}
          onClose={() => setAction(null)}
        />
      )}
      {bulk && (
        <BulkDialog
          action={bulk}
          label={tg.bulk.actions[bulk]}
          request={{ spaces: [...picked] }}
          onClose={() => {
            setBulk(null)
            setPicked(new Set())
          }}
        />
      )}
      {action?.kind === 'evict' && <EvictDialog space={action.space} onClose={() => setAction(null)} />}
    </SettingsSection>
  )
}

function RowActions({
  space: s,
  site,
  onAction,
  onEnable,
  onPin,
  onExempt,
}: {
  space: AdminSpace
  site: boolean
  onAction: (kind: Action['kind']) => void
  onEnable: () => void
  onPin: () => void
  onExempt: () => void
}) {
  const sp = ta.spaces
  return (
    <>
      <Button
        variant="ghost"
        className="h-10 px-3"
        render={<Link to={spaceUrls(s.id).home()} aria-label={t.fmt.labelled(sp.open, s.name)} />}
        nativeButton={false}
      >
        {sp.open}
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger render={<Button variant="ghost" className="size-10" aria-label={sp.moreFor(s.name)} />}>
          <MoreHorizontal />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="min-w-48">
          <DropdownMenuItem disabled={s.personal} onClick={() => onAction('transfer')}>
            {sp.transfer}
          </DropdownMenuItem>
          {s.disabled ? (
            <DropdownMenuItem onClick={onEnable}>{sp.enable}</DropdownMenuItem>
          ) : (
            <DropdownMenuItem onClick={() => onAction('disable')}>{sp.disable}</DropdownMenuItem>
          )}
          <DropdownMenuItem onClick={() => onAction('view')}>{tg.spaces.view}</DropdownMenuItem>
          <DropdownMenuItem onClick={() => onAction('export')}>{tg.spaces.export}</DropdownMenuItem>
          {site && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={onPin}>{s.storage.pinned ? ts.spaces.unpin : ts.spaces.pin}</DropdownMenuItem>
              <DropdownMenuItem onClick={onExempt}>
                {s.storage.exempt ? ts.spaces.exemptOff : ts.spaces.exemptOn}
              </DropdownMenuItem>
              <DropdownMenuItem
                disabled={s.storage.cache === 'local' || s.storage.cache === 'evicted' || s.storage.pinned}
                onClick={() => onAction('evict')}
              >
                {ts.spaces.evict}
              </DropdownMenuItem>
            </>
          )}
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="destructive" onClick={() => onAction('delete')}>
            {tg.spaces.delete}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </>
  )
}

// What the copy on the server is, with the marks that keep it, and what the
// storage policy asks of the space.
function CacheCell({ space: s }: { space: AdminSpace }) {
  const st = s.storage
  const c = ts.spaces
  return (
    <div className="grid gap-0.5">
      <div className="flex flex-wrap items-center gap-1">
        <span title={c.cacheHelp[st.cache]}>{c.caches[st.cache]}</span>
        {st.pinned && <Badge variant="secondary">{c.pinned}</Badge>}
        {st.exempt && <Badge variant="outline">{c.exempt}</Badge>}
      </div>
      <span className="text-xs text-muted-foreground">
        {st.lastUsed ? c.lastUsed(absoluteTime(st.lastUsed)) : c.neverUsed}
      </span>
      {st.cache !== 'local' && (
        <span className="text-xs text-muted-foreground">
          {c.sizes(formatBytes(st.cloneBytes), formatBytes(st.indexBytes))}
        </span>
      )}
      {st.assets === 'repository' && (
        <span className="text-xs text-muted-foreground">{c.assetsIn(formatBytes(st.assetCacheBytes))}</span>
      )}
      {st.remote && (
        <span className={`text-xs ${st.remote.overdue ? 'text-destructive' : 'text-warning'}`}>
          {c.deadline(absoluteTime(st.remote.deadline), st.remote.overdue)}
        </span>
      )}
    </div>
  )
}

function EvictDialog({ space, onClose }: { space: AdminSpace; onClose: () => void }) {
  const c = ts.spaces
  const evict = useEvictSpace()
  const [error, setError] = useState('')
  const confirm = async () => {
    setError('')
    try {
      await evict.mutateAsync(space.id)
      toast.success(c.evictedDone)
      onClose()
    } catch (e) {
      if (!(e instanceof ApiError)) return setError(c.changeFailed)
      // busy says what keeps the space (in the server's words); the others have a sentence of their own.
      if (e.code === 'busy') setError(e.serverMessage ? `${c.errors.busy} ${e.serverMessage}` : c.errors.busy)
      else if (e.code === 'conflict') setError(c.errors.conflict)
      else if (e.code === 'unavailable') setError(c.errors.unavailable)
      else setError(e.message || c.changeFailed)
    }
  }
  return (
    <AlertDialog open onOpenChange={(o) => !o && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{c.evictTitle(space.name)}</AlertDialogTitle>
          <AlertDialogDescription>{c.evictHelp}</AlertDialogDescription>
        </AlertDialogHeader>
        <FormMessage>{error}</FormMessage>
        <AlertDialogFooter>
          <AlertDialogCancel>{ta.spaces.cancel}</AlertDialogCancel>
          <Button type="button" variant="destructive" disabled={evict.isPending} onClick={() => void confirm()}>
            {evict.isPending && <Spinner />}
            {c.evictConfirm}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

// The stored size, or a dash until the server has measured it, and a way to
// measure this one space now.
function SizeCell({ space: s }: { space: AdminSpace }) {
  const sp = ta.spaces
  const resize = useResizeSpace()
  const measure = async () => {
    try {
      await resize.mutateAsync(s.id)
    } catch (e) {
      toast.error(t.fmt.labelled(sp.measureFailed, e instanceof Error ? e.message : ''))
    }
  }
  const measured = s.sizeBytes !== null
  return (
    <div className="flex items-center gap-1.5 whitespace-nowrap">
      <span
        className={`tabular-nums ${measured ? '' : 'text-muted-foreground'}`}
        title={s.sizeAt ? `${sp.measuredAt(absoluteTime(s.sizeAt))}. ${sp.measureHelp}` : sp.measureHelp}
      >
        {s.sizeBytes !== null ? formatBytes(s.sizeBytes) : sp.notMeasured}
      </span>
      {measured ? (
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          disabled={resize.isPending}
          onClick={() => void measure()}
          aria-label={t.fmt.labelled(sp.measure, s.name)}
          title={sp.measure}
        >
          {resize.isPending ? <Spinner className="size-3" /> : <RefreshCw className="size-3" />}
        </Button>
      ) : (
        <Button
          type="button"
          variant="ghost"
          size="xs"
          disabled={resize.isPending}
          onClick={() => void measure()}
          aria-label={t.fmt.labelled(sp.measure, s.name)}
        >
          {resize.isPending ? sp.measuring : sp.measure}
        </Button>
      )}
    </div>
  )
}

function TransferDialog({ space, onClose }: { space: AdminSpace; onClose: () => void }) {
  const sp = ta.spaces
  const transfer = useTransferSpace()
  // Who to hand the space to is looked for on the server, a few at a time: a server
  // with many accounts is never asked for all of them.
  const [term, setTerm] = useState('')
  const found = useUserSearch(useDebounced(term.trim(), 250))
  const [chosen, setChosen] = useState<User | null>(null)
  const [error, setError] = useState('')
  const userId = chosen?.id ?? ''
  const matches = (found.data?.users ?? []).filter((u) => u.id !== space.owner?.id)
  // The person already picked stays in the list while the search moves on.
  const choices = chosen && !matches.some((u) => u.id === chosen.id) ? [chosen, ...matches] : matches
  const truncated = (found.data?.users.length ?? 0) >= USER_SEARCH_LIMIT

  const submit = async () => {
    if (!userId || transfer.isPending) return
    setError('')
    try {
      await transfer.mutateAsync({ id: space.id, userId })
      toast.success(sp.transferred(chosen?.name ?? ''))
      onClose()
    } catch (e) {
      setError(actionError(e))
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            void submit()
          }}
        >
          <DialogHeader>
            <DialogTitle>{sp.transferTitle(space.name)}</DialogTitle>
            <DialogDescription>{sp.transferHelp}</DialogDescription>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="transfer-search">{sp.transferSearch}</Label>
            <Input
              id="transfer-search"
              type="search"
              value={term}
              onChange={(e) => setTerm(e.target.value)}
              placeholder={sp.transferSearchHint}
              autoComplete="off"
              spellCheck={false}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="transfer-to">{sp.transferTo}</Label>
            <NativeSelect
              id="transfer-to"
              value={userId}
              onChange={(e) => setChosen(choices.find((u) => u.id === e.target.value) ?? null)}
              disabled={found.isPending}
            >
              <option value="">{sp.transferChoose}</option>
              {choices.map((u) => (
                <option key={u.id} value={u.id}>
                  {u.name} ({u.username})
                </option>
              ))}
            </NativeSelect>
            {found.isError ? (
              <p className="text-xs text-destructive">{t.fmt.labelled(sp.transferSearchFailed, found.error.message)}</p>
            ) : (
              !found.isPending && (
                <p className="text-xs text-muted-foreground">
                  {matches.length === 0 ? sp.transferNoMatch : truncated ? sp.transferNarrow(USER_SEARCH_LIMIT) : ''}
                </p>
              )
            )}
          </div>
          <FormMessage>{error}</FormMessage>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {sp.cancel}
            </Button>
            <Button type="submit" disabled={!userId || transfer.isPending}>
              {transfer.isPending && <Spinner />}
              {sp.transferConfirm}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function DisableDialog({ space, onClose }: { space: AdminSpace; onClose: () => void }) {
  const sp = ta.spaces
  const disable = useSetSpaceDisabled()
  const [error, setError] = useState('')
  const confirm = async () => {
    setError('')
    try {
      await disable.mutateAsync({ id: space.id, disabled: true })
      toast.success(sp.disabledDone)
      onClose()
    } catch (e) {
      setError(actionError(e))
    }
  }
  return (
    <AlertDialog open onOpenChange={(o) => !o && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{sp.disableTitle(space.name)}</AlertDialogTitle>
          <AlertDialogDescription>{sp.disableHelp}</AlertDialogDescription>
        </AlertDialogHeader>
        <FormMessage>{error}</FormMessage>
        <AlertDialogFooter>
          <AlertDialogCancel>{sp.cancel}</AlertDialogCancel>
          {/* Not AlertDialogAction: the dialog stays open if the server refuses. */}
          <Button type="button" variant="destructive" disabled={disable.isPending} onClick={() => void confirm()}>
            {disable.isPending && <Spinner />}
            {sp.disableConfirm}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
