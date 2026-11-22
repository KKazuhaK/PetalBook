import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router'
import { observeElementRect, useVirtualizer } from '@tanstack/react-virtual'
import { ArrowDown, ArrowUp, ArrowUpDown, ExternalLink } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { usePatchProps, useSpacePermissions, useTablePages, useUrls } from '@/api/hooks'
import type { ListParams, PageSummary, PageType, PropFilter, TypeField } from '@/api/types'
import { ErrorState, Spinner } from '@/components/common'
import { CheckboxControl, DateControl, isWrong, NumberControl, SelectControl } from '@/components/props/PropControls'
import { columnsOf, hiddenKeyOf, type Column } from './columns'
import { TagChip } from '@/components/TagChip'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Skeleton } from '@/components/ui/skeleton'
import { hasValue, showValue } from '@/lib/pageTypes'
import { absoluteTime, relativeTime } from '@/lib/time'
import { t } from '@/i18n'
import { tp } from '@/i18n/props'

// The table view of a folder that has a type: a row per page, the type's fields
// as columns, with sorting and filtering done by the server and a hundred rows
// read at a time as the person scrolls. Only the rows in view are drawn, so
// thousands of pages cost no more than a screenful. Cells of the kinds that have
// a control (select, multiselect, date, number, checkbox) change the page's header
// in place, with the rev of the row, and without reading the table again.

const ROW_HEIGHT = 44
// How many rows from the end of what is loaded the next batch is asked for.
const AHEAD = 20

// What the scrolling box counts as when it has no size yet (not laid out, or hidden): a screenful.
const FALLBACK_RECT = { width: 960, height: 640 }

// The columns are in a module of their own (the view's settings need them too); the table's tests know them from here.
export { columnsOf, hiddenKeyOf }

// The sort in the address when it is one the table understands.
const TABLE_SORT = /^-?(updated|title|prop:.+)$/
export const tableSortOf = (raw: string | null): string | undefined => (raw && TABLE_SORT.test(raw) ? raw : undefined)

// What a click on a column's header changes the sort to: its ascending order, then
// the other way round, then back again. The table is newest first until chosen.
export function nextSort(current: string | undefined, asc: string): string {
  const now = current ?? '-updated'
  return now === asc ? `-${asc}` : asc
}

// `sort`, `filters` and `hidden` are the view's: the listing above chooses them (and shows the filters).
export default function TableView({ folder, typeName, type, sort, onSort, filters, hidden = [] }: { folder: string; typeName: string; type: PageType | undefined; sort?: string; onSort: (to: string) => void; filters: PropFilter[]; hidden?: readonly string[] }) {
  const { write } = useSpacePermissions()
  const urls = useUrls()
  const all = useMemo(() => columnsOf(type), [type])
  // The title is always there; the other columns can be hidden by the view.
  const columns = useMemo(() => all.filter((c) => c.id === 'title' || !hidden.includes(hiddenKeyOf(c))), [all, hidden])

  const query = useTablePages({ folder, ...(sort ? { sort: sort as ListParams['sort'] } : {}), ...(filters.length ? { filter: filters } : {}) })
  const rows = useMemo(() => query.data?.pages.flatMap((p) => p.items) ?? [], [query.data])

  const setSort = (asc: string) => onSort(nextSort(sort, asc))

  const patch = usePatchProps()
  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set())
  const change = useCallback(
    (row: PageSummary, key: string, value: unknown) => {
      if (!row.rev) return
      const mark = (on: boolean) =>
        setBusy((prev) => {
          const next = new Set(prev)
          if (on) next.add(row.id)
          else next.delete(row.id)
          return next
        })
      mark(true)
      // Not the callbacks of mutate(): the observer keeps only those of its latest call, so a row whose change was
      // overtaken by another would stay marked as busy, and its failure would not be told.
      patch
        .mutateAsync({ id: row.id, baseRev: row.rev, ...(value === undefined ? { unset: [key] } : { set: { [key]: value } }) })
        .then(undefined, (e: unknown) => {
          const name = row.title || row.label || t.editor.untitled
          // The row has been read again by the hook; nothing of the change was kept.
          if (e instanceof ApiError && e.code === 'conflict') toast.error(tp.table.conflict(name))
          else toast.error(t.fmt.labelled(tp.table.changeFailed(name), e instanceof Error ? e.message : ''))
        })
        .finally(() => mark(false))
    },
    [patch],
  )

  const total = columns.reduce((n, c) => n + c.width, 0)
  const grid = columns.map((c) => `${c.width}px`).join(' ')

  const scroller = useRef<HTMLDivElement>(null)
  // The virtualizer's functions are used only here, in this component's own render.
  // eslint-disable-next-line react-hooks/incompatible-library
  const virt = useVirtualizer({ count: rows.length, getScrollElement: () => scroller.current, estimateSize: () => ROW_HEIGHT, overscan: 8, observeElementRect: (instance, cb) => observeElementRect(instance, (rect) => cb(rect.height > 0 ? rect : FALLBACK_RECT)), getItemKey: (i) => rows[i]?.id ?? i })
  const visible = virt.getVirtualItems()
  const last = visible.length ? visible[visible.length - 1].index : -1
  const { hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage } = query
  useEffect(() => {
    // A failed batch is not asked for again by itself: the person retries.
    if (last >= 0 && last >= rows.length - AHEAD && hasNextPage && !isFetchingNextPage && !isFetchNextPageError) void fetchNextPage()
  }, [last, rows.length, hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage])

  const sortValue = sort ?? '-updated'

  return (
    <div className="grid gap-3" data-slot="table-view" data-type={typeName}>
      {!type && <p className="text-xs text-muted-foreground">{tp.table.noType(typeName)}</p>}
      {type?.error && <p className="text-xs text-destructive">{type.error}</p>}
      {query.isPending ? (
        <div className="space-y-2" aria-busy="true">
          {[0, 1, 2, 3, 4].map((i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </div>
      ) : query.isError && rows.length === 0 ? (
        <ErrorState message={t.fmt.labelled(t.list.loadFailed, query.error.message)} onRetry={() => void query.refetch()} />
      ) : rows.length === 0 ? (
        <p className="py-16 text-center text-sm text-muted-foreground">{filters.length ? t.list.emptyFiltered : t.list.emptyFolder}</p>
      ) : (
        <>
          <div ref={scroller} aria-busy={query.isPlaceholderData || undefined} className={`relative max-h-[calc(100dvh-14rem)] min-h-64 overflow-auto rounded-lg border transition-opacity ${query.isPlaceholderData ? 'opacity-60' : ''}`} data-slot="table-scroller">
            <div role="table" aria-label={tp.table.label} aria-rowcount={rows.length + 1} style={{ width: total, minWidth: '100%' }}>
              <div role="row" aria-rowindex={1} className="sticky top-0 z-10 grid border-b bg-muted/80 text-xs font-medium backdrop-blur" style={{ gridTemplateColumns: grid }}>
                {columns.map((c) => {
                  const asc = c.sort
                  const active = asc !== undefined && (sortValue === asc || sortValue === `-${asc}`)
                  const dir = sortValue.startsWith('-') ? 'descending' : 'ascending'
                  return (
                    <div key={c.id} role="columnheader" aria-sort={active ? dir : asc ? 'none' : undefined} className="flex h-9 min-w-0 items-center border-r px-3 last:border-r-0">
                      {asc ? (
                        <button type="button" onClick={() => setSort(asc)} title={tp.table.sortBy(c.label)} className="flex min-w-0 items-center gap-1 rounded outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50">
                          <span className="truncate">{c.label}</span>
                          {active ? dir === 'ascending' ? <ArrowUp className="size-3.5 shrink-0" /> : <ArrowDown className="size-3.5 shrink-0" /> : <ArrowUpDown className="size-3.5 shrink-0 opacity-40" />}
                        </button>
                      ) : (
                        <span className="truncate">{c.label}</span>
                      )}
                    </div>
                  )
                })}
              </div>
              <div style={{ height: virt.getTotalSize(), position: 'relative' }}>
                {visible.map((v) => {
                  const row = rows[v.index]
                  if (!row) return null
                  return (
                    <div key={row.id} role="row" aria-rowindex={v.index + 2} data-row={row.id} className="absolute top-0 left-0 grid items-center border-b hover:bg-muted/30" style={{ gridTemplateColumns: grid, height: v.size, transform: `translateY(${v.start}px)`, width: total }}>
                      {columns.map((c) => (
                        <div key={c.id} role="cell" data-col={c.id} className="flex h-full min-w-0 items-center border-r px-2 text-sm last:border-r-0">
                          <Cell col={c} row={row} readOnly={!write || !row.rev} busy={busy.has(row.id)} onChange={change} href={urls.page(row.id)} />
                        </div>
                      ))}
                    </div>
                  )
                })}
              </div>
            </div>
          </div>
          <div className="flex flex-col items-center gap-2 text-xs text-muted-foreground">
            {query.hasNextPage ? (
              <>
                <Button variant="outline" size="sm" onClick={() => void query.fetchNextPage()} disabled={query.isFetchingNextPage}>
                  {query.isFetchingNextPage ? <Spinner /> : null}
                  {query.isFetchingNextPage ? t.list.loadingMore : t.list.loadMore}
                </Button>
                <span>{t.list.loaded(rows.length)}</span>
              </>
            ) : (
              <span>{t.list.allLoaded(rows.length)}</span>
            )}
            {query.isFetchNextPageError && <ErrorState message={query.error.message} onRetry={() => void query.fetchNextPage()} />}
          </div>
        </>
      )}
    </div>
  )
}

// --- the cells -------------------------------------------------------------------------

function Cell({ col, row, readOnly, busy, onChange, href }: { col: Column; row: PageSummary; readOnly: boolean; busy: boolean; onChange: (row: PageSummary, key: string, value: unknown) => void; href: string }) {
  if (col.base === 'title') {
    return (
      <Link to={href} className="min-w-0 truncate font-medium outline-none hover:underline focus-visible:underline" title={row.title ?? row.label}>
        {row.title || row.label || <span className="text-muted-foreground">{t.editor.untitled}</span>}
      </Link>
    )
  }
  if (col.base === 'tags') {
    return (
      <span className="flex min-w-0 flex-wrap gap-1 overflow-hidden">
        {(row.tags ?? []).map((tag) => (
          <TagChip key={tag} tag={tag} />
        ))}
      </span>
    )
  }
  if (col.base === 'updated') {
    return (
      <time dateTime={row.updated} title={absoluteTime(row.updated)} className="truncate text-xs text-muted-foreground">
        {relativeTime(row.updated)}
      </time>
    )
  }
  const field = col.field!
  const value = row.props?.[field.key]
  const label = tp.table.cellLabel(col.label, row.title || row.label || t.editor.untitled)
  const set = (v: unknown) => onChange(row, field.key, v)
  const wrong = isWrong(field, value)

  // What the listing holds of this value is not all of it (cut, or not a plain value): showing it is
  // all that is safe, a change would write the shortened copy over the page's value.
  if (row.partialProps?.includes(field.key)) {
    const text = hasValue(value) ? showValue(value) : ''
    return (
      <span className="flex min-w-0 items-center gap-1 text-muted-foreground" title={tp.table.partial} data-partial="true">
        <span className="min-w-0 truncate" aria-label={`${label}: ${tp.table.partial}`}>
          {text}
        </span>
        <span aria-hidden className="shrink-0">…</span>
      </span>
    )
  }

  // Read only, or a kind with no control here: the value as text, marked when it does not fit.
  const plain = (text: string) => (
    <span className={`min-w-0 truncate ${wrong ? 'text-destructive' : ''}`} title={text} data-wrong={wrong || undefined}>
      {text}
    </span>
  )
  if (field.kind === 'url' && typeof value === 'string' && /^(https?:\/\/|mailto:)/i.test(value.trim())) {
    return (
      <a href={value.trim()} target="_blank" rel="noreferrer noopener" className={`flex min-w-0 items-center gap-1 hover:underline ${wrong ? 'text-destructive' : ''}`} title={value}>
        <span className="truncate">{value}</span>
        <ExternalLink className="size-3 shrink-0" />
      </a>
    )
  }
  if (field.kind === 'checkbox') {
    if (readOnly) return value === undefined ? null : plain(value === true ? '✓' : value === false ? '–' : showValue(value))
    return <CheckboxControl field={field} value={value} onChange={set} label={label} readOnly={busy} />
  }
  if (readOnly) return plain(hasValue(value) ? showValue(value) : '')
  switch (field.kind) {
    case 'select':
      return <SelectControl field={field} value={value} onChange={set} label={label} readOnly={busy} className="h-7 text-sm" />
    case 'multiselect':
      return <MultiCell field={field} value={value} onChange={set} label={label} busy={busy} />
    case 'date':
      return <DateControl field={field} value={value} onChange={set} label={label} readOnly={busy} className="h-7 text-sm" />
    case 'number':
      // One request per number, not per keystroke: it is sent when the person leaves the cell or presses Enter.
      return <NumberControl field={field} value={value} onChange={set} label={label} readOnly={busy} commit="blur" className="h-7 text-sm" />
    default:
      return plain(hasValue(value) ? showValue(value) : '')
  }
}

// A list of choices in a cell: the picked ones as they are, and a small menu to pick from.
// While a change of the page is on its way the controls cannot be used again: the next one needs its new rev.
function MultiCell({ field, value, onChange, label, busy }: { field: TypeField; value: unknown; onChange: (v: unknown) => void; label: string; busy: boolean }) {
  const options = field.options ?? []
  const list: unknown[] = Array.isArray(value) ? value : hasValue(value) ? [value] : []
  const wrong = isWrong(field, value)
  const toggle = (o: string) => {
    const next = list.includes(o) ? list.filter((x) => x !== o) : [...list, o]
    onChange(next.length ? next : undefined)
  }
  return (
    <Popover>
      <PopoverTrigger render={<button type="button" disabled={busy} aria-label={label} aria-invalid={wrong || undefined} className={`flex h-7 min-w-0 flex-1 items-center truncate rounded-md px-1.5 text-left text-sm outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50 ${wrong ? 'text-destructive' : ''}`} />}>
        <span className="truncate">{list.length ? list.map(showValue).join(', ') : <span className="text-muted-foreground">{tp.panel.none}</span>}</span>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-56 gap-1">
        {options.map((o) => (
          <label key={o} className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-sm hover:bg-muted">
            <input type="checkbox" checked={list.includes(o)} onChange={() => toggle(o)} className="size-4" />
            {o}
          </label>
        ))}
        {list
          .filter((x) => !options.includes(x as string))
          .map((x, i) => (
            <label key={`${showValue(x)}-${i}`} data-wrong="true" className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-sm text-destructive hover:bg-muted">
              <input type="checkbox" checked onChange={() => onChange(list.filter((y) => y !== x).length ? list.filter((y) => y !== x) : undefined)} className="size-4" />
              {showValue(x)}
            </label>
          ))}
      </PopoverContent>
    </Popover>
  )
}
