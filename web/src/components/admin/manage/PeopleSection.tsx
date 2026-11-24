import { useState } from 'react'
import { Search } from 'lucide-react'
import { usePeople, usePerson } from '@/api/manage'
import type { ConfirmAction, PeopleParams, PeopleSort, StoragePerson } from '@/api/types'
import { ErrorState, PageFallback } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { formatBytes } from '@/lib/bytes'
import { absoluteTime } from '@/lib/time'
import { useDebounced } from '@/lib/useDebounced'
import { tg } from '@/i18n/manage'
import { ts } from '@/i18n/storage'
import { BulkBar } from './BulkBar'
import { BulkDialog } from './BulkDialog'
import { DeletePersonDialog } from './DeletePersonDialog'
import { ListFooter, LocationBadge, useIsSite, tapTarget, fitScreen } from './shared'

type Filter = '' | 'dormant' | 'some' | 'none' | 'grace' | 'overdue' | 'inactive' | 'disabled'
const SORTS: PeopleSort[] = ['held', 'bytes', 'lastActive', 'created', 'spaces']
const INACTIVE_DAYS = 90

function paramsOf(filter: Filter): Partial<PeopleParams> {
  switch (filter) {
    case 'dormant':
      return { dormant: true }
    case 'some':
    case 'none':
      return { remote: filter }
    case 'grace':
    case 'overdue':
      return { deadline: filter }
    case 'inactive':
      return { inactiveDays: INACTIVE_DAYS }
    case 'disabled':
      return { disabled: true }
    default:
      return {}
  }
}

// Every person and what they hold, a page at a time, in the order asked for. The numbers are stored measurements.
export function PeopleSection() {
  const [text, setText] = useState('')
  const q = useDebounced(text.trim(), 300)
  const [filter, setFilter] = useState<Filter>('')
  const [sort, setSort] = useState<PeopleSort>('held')
  const [order, setOrder] = useState<'asc' | 'desc'>('desc')
  const list = usePeople({
    q: q || undefined,
    sort,
    order,
    ...paramsOf(filter),
  })
  const [open, setOpen] = useState<string | null>(null)
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [bulk, setBulk] = useState<ConfirmAction | null>(null)
  const items = list.data?.pages.flatMap((p) => p.items) ?? []
  const toggle = (id: string) =>
    setPicked((prev) => {
      const next = new Set(prev)
      if (!next.delete(id)) next.add(id)
      return next
    })
  const allPicked = items.length > 0 && items.every((p) => picked.has(p.user.id))

  return (
    <SettingsSection title={tg.people.title} id="manage-people-heading" description={tg.people.help}>
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
            placeholder={tg.people.searchPlaceholder}
            aria-label={tg.people.search}
            className="pl-8"
            autoComplete="off"
          />
        </div>
        <NativeSelect
          aria-label={tg.people.filter}
          value={filter}
          onChange={(e) => setFilter(e.target.value as Filter)}
          className="w-auto max-sm:w-full"
        >
          <option value="">{tg.people.all}</option>
          <option value="dormant">{tg.people.dormant}</option>
          <option value="some">{tg.people.withRemote}</option>
          <option value="none">{tg.people.noRemote}</option>
          <option value="grace">{tg.people.grace}</option>
          <option value="overdue">{tg.people.overdue}</option>
          <option value="inactive">{tg.people.inactive(INACTIVE_DAYS)}</option>
          <option value="disabled">{tg.people.disabled}</option>
        </NativeSelect>
        <NativeSelect
          aria-label={tg.people.sort}
          value={sort}
          onChange={(e) => setSort(e.target.value as PeopleSort)}
          className="w-auto max-sm:w-full"
        >
          {SORTS.map((s) => (
            <option key={s} value={s}>
              {tg.people.sorts[s]}
            </option>
          ))}
        </NativeSelect>
        <NativeSelect
          aria-label={tg.people.sort + ' ↕'}
          value={order}
          onChange={(e) => setOrder(e.target.value as 'asc' | 'desc')}
          className="w-auto max-sm:w-full"
        >
          <option value="desc">{tg.people.order.desc}</option>
          <option value="asc">{tg.people.order.asc}</option>
        </NativeSelect>
      </div>
      <BulkBar
        count={picked.size}
        actions={['delete_users', 'disable_users', 'enable_users']}
        onAction={setBulk}
        onClear={() => setPicked(new Set())}
      />
      {list.isPending ? (
        <PageFallback />
      ) : list.isError && !list.data ? (
        <ErrorState message={tg.people.loadFailed} onRetry={() => void list.refetch()} />
      ) : items.length === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground">{q ? tg.people.noMatch(q) : tg.people.empty}</p>
      ) : (
        <>
          <div className="overflow-x-auto rounded-lg border">
            <table className="w-full min-w-[48rem] text-left text-sm" data-slot="people-table">
              <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
                <tr>
                  <th scope="col" className="w-8 px-3 py-2">
                    <input
                      type="checkbox"
                      aria-label={tg.people.selectAll}
                      checked={allPicked}
                      onChange={() => setPicked(allPicked ? new Set() : new Set(items.map((p) => p.user.id)))}
                      className="size-4 accent-[var(--color-primary)]"
                    />
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {tg.people.name}
                  </th>
                  <th scope="col" className="px-3 py-2 text-right font-medium">
                    {tg.people.spaces}
                  </th>
                  <th scope="col" className="px-3 py-2 text-right font-medium" title={tg.summary.usageHelp}>
                    {tg.people.usage}
                  </th>
                  <th scope="col" className="px-3 py-2 text-right font-medium" title={tg.summary.heldHelp}>
                    {tg.people.held}
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {tg.people.lastActive}
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {tg.people.status}
                  </th>
                  <th scope="col" className="px-3 py-2" />
                </tr>
              </thead>
              <tbody className="divide-y">
                {items.map((p) => (
                  <tr key={p.user.id} data-person={p.user.id}>
                    <td className="px-3 py-2.5">
                      <input
                        type="checkbox"
                        aria-label={tg.people.selectFor(p.user.name)}
                        checked={picked.has(p.user.id)}
                        onChange={() => toggle(p.user.id)}
                        className="size-4 accent-[var(--color-primary)]"
                      />
                    </td>
                    <th scope="row" className="max-w-48 px-3 py-2.5 font-normal">
                      <div className="truncate font-medium">{p.user.name}</div>
                      <div className="truncate text-xs text-muted-foreground">{p.user.username}</div>
                    </th>
                    <td className="px-3 py-2.5 text-right tabular-nums">{p.spaces}</td>
                    <td className="px-3 py-2.5 text-right tabular-nums">{formatBytes(p.usageBytes)}</td>
                    <td className="px-3 py-2.5 text-right tabular-nums">{formatBytes(p.heldBytes)}</td>
                    <td className="px-3 py-2.5 text-xs whitespace-nowrap">
                      {p.lastActive ? (
                        absoluteTime(p.lastActive)
                      ) : (
                        <span className="text-muted-foreground">{tg.people.never}</span>
                      )}
                    </td>
                    <td className="px-3 py-2.5">
                      <div className="flex flex-wrap gap-1">
                        {p.dormant && <Badge variant="outline">{tg.people.isDormant}</Badge>}
                        {p.user.disabled && <Badge variant="secondary">{tg.people.isDisabled}</Badge>}
                      </div>
                    </td>
                    <td className="px-3 py-2.5 text-right">
                      <Button
                        className={tapTarget}
                        type="button"
                        variant="outline"
                        size="sm"
                        aria-label={tg.people.openFor(p.user.name)}
                        onClick={() => setOpen(p.user.id)}
                      >
                        {tg.people.open}
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <ListFooter
            shown={items.length}
            more={!!list.hasNextPage}
            loading={list.isFetchingNextPage}
            onMore={() => void list.fetchNextPage()}
            error={
              list.isFetchNextPageError ? (
                <ErrorState message={list.error.message} onRetry={() => void list.fetchNextPage()} />
              ) : undefined
            }
          />
        </>
      )}
      {open && <PersonDialog id={open} row={items.find((p) => p.user.id === open)} onClose={() => setOpen(null)} />}
      {bulk && (
        <BulkDialog
          action={bulk}
          label={tg.bulk.actions[bulk]}
          request={{ users: [...picked] }}
          onClose={() => {
            setBulk(null)
            setPicked(new Set())
          }}
        />
      )}
    </SettingsSection>
  )
}

function PersonDialog({ id, row, onClose }: { id: string; row?: StoragePerson; onClose: () => void }) {
  const one = usePerson(id)
  const p = one.data ?? row
  const [deleting, setDeleting] = useState(false)
  const site = useIsSite()
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className={`${fitScreen} sm:max-w-2xl`} data-slot="person-dialog">
        <DialogHeader>
          <DialogTitle>{p ? `${p.user.name}（${p.user.username}）` : ''}</DialogTitle>
          <DialogDescription>
            {p ? tg.people.spacesLine(p.spaces, p.remoteSpaces, p.evictedSpaces, p.pinnedSpaces) : ''}
          </DialogDescription>
        </DialogHeader>
        {!p ? (
          <PageFallback />
        ) : (
          <div className="grid gap-4 text-sm">
            <dl className="grid grid-cols-2 gap-3" data-slot="person-numbers">
              <div>
                <dt className="text-xs text-muted-foreground">{tg.people.usage}</dt>
                <dd className="tabular-nums">{formatBytes(p.usageBytes)}</dd>
              </div>
              <div>
                <dt className="text-xs text-muted-foreground">{tg.people.held}</dt>
                <dd className="tabular-nums">{formatBytes(p.heldBytes)}</dd>
              </div>
            </dl>
            <p className="text-xs text-muted-foreground">
              {p.connected ? tg.people.connected : tg.people.notConnected} ·{' '}
              {p.firstWrite ? tg.people.firstWrite(absoluteTime(p.firstWrite)) : tg.people.neverWrote} ·{' '}
              {tg.people.deadlines(p.graceSpaces, p.overdueSpaces)}
            </p>
            <h4 className="font-medium">{tg.people.detailSpaces}</h4>
            {one.isPending ? (
              <PageFallback />
            ) : one.isError ? (
              <ErrorState message={tg.people.loadFailed} onRetry={() => void one.refetch()} />
            ) : (one.data?.items.length ?? 0) === 0 ? (
              <p className="text-muted-foreground">{tg.people.detailNone}</p>
            ) : (
              <ul className="divide-y rounded-lg border" data-slot="person-spaces">
                {one.data!.items.map((s) => (
                  <li key={s.id} className="grid gap-1 px-3 py-2 sm:grid-cols-[1fr_auto]" data-space={s.id}>
                    <div className="min-w-0">
                      <div className="truncate font-medium">
                        {s.name} {s.personal && <Badge variant="outline">{tg.recycle.personal}</Badge>}
                      </div>
                      <div className="text-xs text-muted-foreground">
                        {s.sizeBytes !== null ? formatBytes(s.sizeBytes) : '—'} · {ts.spaces.caches[s.storage.cache]}
                      </div>
                    </div>
                    <LocationBadge location={s.location} counted={s.counted} />
                  </li>
                ))}
              </ul>
            )}
            {!site && <p className="text-xs text-muted-foreground">{tg.people.tenantNote}</p>}
          </div>
        )}
        <div className="flex justify-between gap-2">
          <Button
            className={tapTarget}
            type="button"
            variant="destructive"
            disabled={!p}
            onClick={() => setDeleting(true)}
          >
            {tg.people.deletePerson}
          </Button>
          <Button className={tapTarget} type="button" variant="outline" onClick={onClose}>
            {tg.people.close}
          </Button>
        </div>
        {deleting && p && (
          <DeletePersonDialog id={p.user.id} username={p.user.username} onClose={() => setDeleting(false)} />
        )}
      </DialogContent>
    </Dialog>
  )
}
