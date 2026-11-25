import { useState } from 'react'
import { Link } from 'react-router'
import { MoreHorizontal, RefreshCw, Search } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import {
  useAdminSpaces,
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
import { ErrorState, PageFallback, Spinner } from '@/components/common'
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
import { useDebounced } from '@/lib/useDebounced'
import { t } from '@/i18n'
import { ta } from '@/i18n/admin'
import { ts } from '@/i18n/storage'

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
  const [sort, setSort] = useState<SpaceSort>('created')
  const [order, setOrder] = useState<'asc' | 'desc'>('asc')
  const [remote, setRemote] = useState('')
  const [idle, setIdle] = useState('')
  const [pinnedOnly, setPinnedOnly] = useState(false)
  const [exemptOnly, setExemptOnly] = useState(false)
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [bulk, setBulk] = useState<ConfirmAction | null>(null)
  const spaces = useAdminSpaces(q, true, {
    cache: filter === 'overdue' ? undefined : (filter as CacheState) || undefined,
    overdue: filter === 'overdue',
    sort,
    order,
    remote: (remote || undefined) as 'none' | 'ok' | 'offline' | 'error' | undefined,
    idleDays: idle ? Number(idle) : undefined,
    pinned: pinnedOnly,
    exempt: exemptOnly,
  })
  const setDisabled = useSetSpaceDisabled()
  const putStorage = usePutSpaceStorage()
  const [action, setAction] = useState<Action | null>(null)

  const items = spaces.data?.pages.flatMap((p) => p.items) ?? []
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
          className="w-auto max-sm:w-full"
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
          aria-label={tg.spaces.sort}
          value={sort}
          onChange={(e) => {
            setSort(e.target.value as SpaceSort)
            setOrder(e.target.value === 'created' || e.target.value === 'name' ? 'asc' : 'desc')
          }}
          className="w-auto max-sm:w-full"
        >
          {(['created', 'name', 'bytes', 'lastUsed'] as const).map((k) => (
            <option key={k} value={k}>
              {tg.spaces.sorts[k]}
            </option>
          ))}
        </NativeSelect>
        <NativeSelect
          aria-label={`${tg.spaces.sort} ↕`}
          value={order}
          onChange={(e) => setOrder(e.target.value as 'asc' | 'desc')}
          className="w-auto max-sm:w-full"
        >
          <option value="asc">{tg.spaces.order.asc}</option>
          <option value="desc">{tg.spaces.order.desc}</option>
        </NativeSelect>
        <NativeSelect
          aria-label={tg.spaces.remote}
          value={remote}
          onChange={(e) => setRemote(e.target.value)}
          className="w-auto max-sm:w-full"
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
          className="w-auto max-sm:w-full"
        >
          {(['', '30', '90', '180'] as const).map((k) => (
            <option key={k} value={k}>
              {k === '' ? `${tg.spaces.idle}：${tg.spaces.idles['']}` : tg.spaces.idles[k]}
            </option>
          ))}
        </NativeSelect>
        <label className="flex items-center gap-1.5 text-xs">
          <input
            type="checkbox"
            className="size-4 accent-[var(--color-primary)]"
            checked={pinnedOnly}
            onChange={(e) => setPinnedOnly(e.target.checked)}
          />
          {tg.spaces.pinned}
        </label>
        <label className="flex items-center gap-1.5 text-xs">
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

      {spaces.isPending ? (
        <PageFallback />
      ) : spaces.isError && !spaces.data ? (
        <ErrorState
          message={t.fmt.labelled(ta.spaces.loadFailed, spaces.error.message)}
          onRetry={() => void spaces.refetch()}
        />
      ) : items.length === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground">{q ? ta.spaces.noMatch(q) : ta.spaces.empty}</p>
      ) : (
        <>
          <div className="overflow-x-auto rounded-lg border">
            <table className="w-full min-w-[62rem] text-left text-sm">
              <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
                <tr>
                  <th scope="col" className="w-8 px-3 py-2">
                    <input
                      type="checkbox"
                      aria-label={tg.spaces.selectAll}
                      className="size-4 accent-[var(--color-primary)]"
                      checked={items.length > 0 && items.every((s) => picked.has(s.id))}
                      onChange={() =>
                        setPicked(items.every((s) => picked.has(s.id)) ? new Set() : new Set(items.map((s) => s.id)))
                      }
                    />
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {ta.spaces.name}
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {ta.spaces.owner}
                  </th>
                  <th scope="col" className="px-3 py-2 text-right font-medium">
                    {ta.spaces.members}
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {ta.spaces.size}
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {tg.spaces.where}
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {ta.spaces.sync}
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {ta.spaces.status}
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {ts.spaces.cache}
                  </th>
                  <th scope="col" className="px-3 py-2 text-right font-medium">
                    {ta.spaces.actions}
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y">
                {items.map((s) => (
                  <SpaceRow
                    key={s.id}
                    space={s}
                    picked={picked.has(s.id)}
                    onPick={() =>
                      setPicked((prev) => {
                        const next = new Set(prev)
                        if (!next.delete(s.id)) next.add(s.id)
                        return next
                      })
                    }
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
                ))}
              </tbody>
            </table>
          </div>
          <div className="flex flex-col items-center gap-2">
            {spaces.hasNextPage && (
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={spaces.isFetchingNextPage}
                onClick={() => void spaces.fetchNextPage()}
              >
                {spaces.isFetchingNextPage && <Spinner />}
                {spaces.isFetchingNextPage ? ta.spaces.loadingMore : ta.spaces.loadMore}
              </Button>
            )}
            <span className="text-xs text-muted-foreground">
              {spaces.hasNextPage ? ta.spaces.loaded(items.length) : ta.spaces.allLoaded(items.length)}
            </span>
            {spaces.isFetchNextPageError && (
              <ErrorState message={spaces.error.message} onRetry={() => void spaces.fetchNextPage()} />
            )}
          </div>
        </>
      )}

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

function SpaceRow({
  space: s,
  picked,
  onPick,
  site,
  onAction,
  onEnable,
  onPin,
  onExempt,
}: {
  space: AdminSpace
  picked: boolean
  onPick: () => void
  site: boolean
  onAction: (kind: Action['kind']) => void
  onEnable: () => void
  onPin: () => void
  onExempt: () => void
}) {
  const sp = ta.spaces
  return (
    <tr data-space={s.id}>
      <td className="px-3 py-2.5">
        <input
          type="checkbox"
          aria-label={tg.spaces.selectFor(s.name)}
          className="size-4 accent-[var(--color-primary)]"
          checked={picked}
          onChange={onPick}
        />
      </td>
      <th scope="row" className="max-w-56 px-3 py-2.5 font-normal">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span className="truncate font-medium">{s.name}</span>
          {s.personal && <Badge variant="outline">{sp.personal}</Badge>}
        </div>
      </th>
      <td className="max-w-40 px-3 py-2.5">
        {s.owner ? (
          <>
            <div className="truncate">{s.owner.name}</div>
            <div className="truncate text-xs text-muted-foreground">{s.owner.username}</div>
          </>
        ) : (
          <span className="text-muted-foreground">{sp.noOwner}</span>
        )}
      </td>
      <td className="px-3 py-2.5 text-right tabular-nums">{s.members}</td>
      <td className="px-3 py-2.5">
        <SizeCell space={s} />
      </td>
      <td className="px-3 py-2.5">
        <LocationBadge location={s.location} counted={s.counted} />
      </td>
      <td className="px-3 py-2.5 whitespace-nowrap">{sp.syncStates[s.sync]}</td>
      <td className="px-3 py-2.5">
        {s.disabled ? (
          <Badge variant="secondary">{sp.disabled}</Badge>
        ) : (
          <span className="text-muted-foreground">{sp.active}</span>
        )}
      </td>
      <td className="px-3 py-2.5" data-slot="space-cache">
        <CacheCell space={s} />
      </td>
      <td className="px-3 py-2.5">
        <div className="flex items-center justify-end gap-1">
          <Button
            variant="outline"
            size="sm"
            render={<Link to={spaceUrls(s.id).home()} aria-label={t.fmt.labelled(sp.open, s.name)} />}
          >
            {sp.open}
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label={sp.moreFor(s.name)} />}>
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
                  <DropdownMenuItem onClick={onPin}>
                    {s.storage.pinned ? ts.spaces.unpin : ts.spaces.pin}
                  </DropdownMenuItem>
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
        </div>
      </td>
    </tr>
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
