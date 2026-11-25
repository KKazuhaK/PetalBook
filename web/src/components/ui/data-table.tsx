import { useEffect, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import {
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  ChevronUp,
  ListFilter,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { NativeSelect } from '@/components/ui/native-select'
import { Skeleton } from '@/components/ui/skeleton'
import { tc } from '@/i18n/console'
import { orderIds, PAGE_SIZES, shares, useTablePrefs, type Density } from '@/lib/tablePrefs'

// A table of rows the server pages by cursor, for lists that grow to hundreds and thousands. What it does, so a list
// does not have to:
//  - the columns an administrator shows, hides and orders, their widths (kept as shares of the table, never pixels,
//    each at least its own least width), the density and the page size, all kept in this browser only;
//  - columns of lower importance leave first when the table is narrow (`priority`), and what still does not fit
//    scrolls inside the table's own box, never the page;
//  - sorting by a header the server can sort by, a tick for each row, a row's common actions at its right end;
//  - previous and next, with the page size.
// The data and the page are the caller's: it asks the server, and tells the table which rows it has.

export interface Column<T> {
  id: string
  header: string
  cell: (row: T) => ReactNode
  // The least width, in pixels (default 96): the table never squeezes the column further, it scrolls.
  min?: number
  // The share of the width the column starts with, against the others (default 1).
  weight?: number
  // 1 is kept longest; a higher number leaves earlier when the table is narrow (default 1).
  priority?: number
  // The name the server sorts by; the header is a button when it is set.
  sortKey?: string
  align?: 'left' | 'right'
  // A column that cannot be hidden.
  required?: boolean
}

export interface DataTableSort {
  key: string
  order: 'asc' | 'desc'
}

export interface DataTableProps<T> {
  // The table's preferences in this browser (`useTablePrefs('<name>')`): the caller reads the page size from it.
  prefs: ReturnType<typeof useTablePrefs>
  columns: Column<T>[]
  rows: T[]
  rowId: (row: T) => string
  // What names a row to a screen reader (for the tick and the actions).
  rowLabel: (row: T) => string
  loading?: boolean
  // The rows shown are of the page before while the next is on its way.
  fetching?: boolean
  error?: string
  onRetry?: () => void
  empty: ReactNode
  // The page: its number, whether there is a previous and a next, and how to move. Left out, the table has no pager
  // and no page size: it shows `footer` (a "load more", say) instead.
  paging?: {
    page: number
    hasPrev: boolean
    hasNext: boolean
    onPrev: () => void
    onNext: () => void
  }
  sort?: DataTableSort
  onSort?: (s: DataTableSort) => void
  // `shift` is set when the tick of one row was clicked with Shift held: a list may then select the run from the last one.
  selection?: {
    selected: ReadonlySet<string>
    onChange: (next: Set<string>, tick?: { id: string; shift: boolean }) => void
  }
  // Shown above the table while something is ticked.
  bulk?: ReactNode
  // A row's common actions (at most one or two), at its right end.
  actions?: (row: T) => ReactNode
  rowAttrs?: (row: T) => Record<string, string>
  // More classes for a row (a row is `group/row`, so a cell can show something only while the pointer is on it).
  rowClass?: (row: T) => string
  footer?: ReactNode
  // The columns shown when the person has set none themselves (a saved view's choice, say): ids to hide.
  defaultHidden?: string[]
}

const ROW_HEIGHT: Record<Density, string> = { compact: 'h-10', comfortable: 'h-14' }
const SELECT_W = 40
const ACTIONS_W = 148
const SETTINGS_W = 48

export function DataTable<T>(props: DataTableProps<T>) {
  const {
    prefs: prefsApi,
    columns,
    rows,
    rowId,
    rowLabel,
    loading,
    fetching,
    error,
    onRetry,
    empty,
    paging,
    sort,
    onSort,
  } = props
  const { selection, bulk, actions, rowAttrs, rowClass, footer, defaultHidden } = props
  const { prefs, update, reset } = prefsApi

  // The box the table scrolls in: its width decides which columns fit.
  const box = useRef<HTMLDivElement>(null)
  const [boxW, setBoxW] = useState(0)
  useLayoutEffect(() => {
    const el = box.current
    if (!el) return
    setBoxW(el.clientWidth)
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(() => setBoxW(el.clientWidth))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  const byId = useMemo(() => new Map(columns.map((c) => [c.id, c])), [columns])
  const ordered = useMemo(
    () =>
      orderIds(
        columns.map((c) => c.id),
        prefs.order,
      ).map((i) => byId.get(i)!),
    [columns, prefs.order, byId],
  )
  const hiddenIds = prefs.custom || !defaultHidden ? prefs.hidden : defaultHidden
  const chosen = ordered.filter((c) => c.required || !hiddenIds.includes(c.id))
  const fixed = (selection ? SELECT_W : 0) + (actions ? ACTIONS_W : SETTINGS_W) + (actions ? SETTINGS_W : 0)
  const minOf = (c: Column<T>) => c.min ?? 96

  // What still fits: the least important column goes first (the last of those, when equal) until the rest fit. A
  // width of 0 is a box that was never measured (a test, a hidden tab): nothing is taken away.
  const shown = (() => {
    const list = [...chosen]
    if (boxW <= 0) return list
    const total = () => list.reduce((n, c) => n + minOf(c), fixed)
    while (total() > boxW) {
      let worst = -1
      list.forEach((c, i) => {
        if (c.required || (c.priority ?? 1) <= 1) return
        if (worst < 0 || (c.priority ?? 1) >= (list[worst].priority ?? 1)) worst = i
      })
      if (worst < 0) break
      list.splice(worst, 1)
    }
    return list
  })()
  const squeezed = chosen.length - shown.length

  // The widths, as shares: the saved ones while resizing are a draft, saved when the pointer is let go.
  const [draft, setDraft] = useState<Record<string, number> | null>(null)
  const have = shares(
    shown.map((c) => ({ id: c.id, weight: c.weight ?? 1 })),
    draft ?? prefs.widths,
  )
  const tableMin = shown.reduce((n, c) => n + minOf(c), fixed)
  const table = useRef<HTMLTableElement>(null)

  // What ends a drag in progress (it takes the listeners off the window), so a table that goes away mid-drag leaves none.
  const dragEnd = useRef<(() => void) | null>(null)
  useEffect(() => () => dragEnd.current?.(), [])
  const startResize = (i: number, clientX: number) => {
    dragEnd.current?.()
    // A table that was never measured (a test, a hidden tab) has no least share to keep.
    const measured = table.current?.clientWidth || boxW
    const w = measured || 1
    const left = shown[i]
    const right = shown[i + 1]
    if (!right) return
    const start = { ...have }
    const bound = (c: Column<T>) => (measured ? minOf(c) / w : 0)
    const move = (x: number) => {
      const delta = (x - clientX) / w
      const d = Math.max(bound(left) - start[left.id], Math.min(start[right.id] - bound(right), delta))
      setDraft({ ...start, [left.id]: start[left.id] + d, [right.id]: start[right.id] - d })
    }
    const onMove = (e: PointerEvent) => move(e.clientX)
    const stop = () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', done)
      window.removeEventListener('pointercancel', done)
      dragEnd.current = null
    }
    const done = (e: PointerEvent) => {
      stop()
      const delta = (e.clientX - clientX) / w
      const d = Math.max(bound(left) - start[left.id], Math.min(start[right.id] - bound(right), delta))
      update({ widths: { ...start, [left.id]: start[left.id] + d, [right.id]: start[right.id] - d } })
      setDraft(null)
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', done)
    window.addEventListener('pointercancel', done)
    dragEnd.current = stop
  }
  const nudge = (i: number, e: KeyboardEvent) => {
    const step = e.key === 'ArrowLeft' ? -0.01 : e.key === 'ArrowRight' ? 0.01 : 0
    const left = shown[i]
    const right = shown[i + 1]
    if (!step || !right) return
    e.preventDefault()
    const measured = table.current?.clientWidth || boxW
    const least = (c: Column<T>) => (measured ? minOf(c) / measured : 0)
    const d = Math.max(least(left) - have[left.id], Math.min(have[right.id] - least(right), step))
    update({ widths: { ...have, [left.id]: have[left.id] + d, [right.id]: have[right.id] - d } })
  }

  const selected = selection?.selected
  const allOnPage = rows.length > 0 && rows.every((r) => selected?.has(rowId(r)))
  const shiftHeld = useRef(false)
  const toggleRow = (rid: string) => {
    if (!selection) return
    const next = new Set(selection.selected)
    if (!next.delete(rid)) next.add(rid)
    selection.onChange(next, { id: rid, shift: shiftHeld.current })
  }
  const toggleAll = () => {
    if (!selection) return
    const next = new Set(selection.selected)
    for (const r of rows) {
      if (allOnPage) next.delete(rowId(r))
      else next.add(rowId(r))
    }
    selection.onChange(next)
  }
  const sortOf = (c: Column<T>): 'ascending' | 'descending' | 'none' | undefined =>
    !c.sortKey ? undefined : sort?.key !== c.sortKey ? 'none' : sort.order === 'asc' ? 'ascending' : 'descending'
  const clickSort = (c: Column<T>) => {
    if (!c.sortKey || !onSort) return
    onSort({ key: c.sortKey, order: sort?.key === c.sortKey && sort.order === 'asc' ? 'desc' : 'asc' })
  }

  const colCount = shown.length + (selection ? 1 : 0) + 1
  const busy = loading && rows.length === 0

  return (
    <div data-slot="data-table" className="min-w-0 space-y-3">
      {selection && selection.selected.size > 0 && bulk}
      <div
        ref={box}
        className={`overflow-x-auto rounded-lg border transition-opacity duration-150 ${fetching ? 'opacity-60' : ''}`}
        aria-busy={fetching || loading || undefined}
      >
        <table
          ref={table}
          className="w-full table-fixed border-collapse text-left text-sm"
          style={{ minWidth: tableMin }}
          data-density={prefs.density}
        >
          <colgroup>
            {selection && <col style={{ width: SELECT_W }} />}
            {shown.map((c) => (
              <col key={c.id} style={{ width: `calc((100% - ${fixed}px) * ${have[c.id]})` }} />
            ))}
            <col style={{ width: actions ? ACTIONS_W + SETTINGS_W : SETTINGS_W }} />
          </colgroup>
          <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
            <tr>
              {selection && (
                <th scope="col" className="px-0 py-0">
                  <label className="flex size-10 cursor-pointer items-center justify-center">
                    <input
                      type="checkbox"
                      aria-label={tc.table.selectAll}
                      checked={allOnPage}
                      disabled={rows.length === 0}
                      onChange={toggleAll}
                      className="size-4 accent-[var(--color-primary)]"
                    />
                  </label>
                </th>
              )}
              {shown.map((c, i) => (
                <th
                  key={c.id}
                  scope="col"
                  aria-sort={sortOf(c)}
                  data-column={c.id}
                  className={`relative px-3 py-2 font-medium ${c.align === 'right' ? 'text-right' : ''}`}
                >
                  {c.sortKey ? (
                    <button
                      type="button"
                      onClick={() => clickSort(c)}
                      aria-label={tc.table.sortBy(c.header)}
                      className="-mx-2 inline-flex h-10 max-w-full items-center gap-1 rounded-md px-2 outline-none transition-colors hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
                    >
                      <span className="truncate">{c.header}</span>
                      {sortOf(c) === 'ascending' ? (
                        <ArrowUp className="size-3.5 shrink-0" aria-hidden="true" />
                      ) : sortOf(c) === 'descending' ? (
                        <ArrowDown className="size-3.5 shrink-0" aria-hidden="true" />
                      ) : (
                        <ArrowUpDown className="size-3.5 shrink-0 opacity-50" aria-hidden="true" />
                      )}
                    </button>
                  ) : (
                    <span className="block truncate">{c.header}</span>
                  )}
                  {i < shown.length - 1 && (
                    <span
                      role="separator"
                      aria-orientation="vertical"
                      aria-label={tc.table.resize(c.header)}
                      tabIndex={0}
                      onPointerDown={(e) => {
                        e.preventDefault()
                        startResize(i, e.clientX)
                      }}
                      onKeyDown={(e) => nudge(i, e)}
                      className="absolute top-0 -right-2 z-10 flex h-full w-4 cursor-col-resize pointer-coarse:-right-3 pointer-coarse:w-6 touch-none justify-center outline-none after:h-full after:w-px after:bg-border after:transition-colors hover:after:bg-primary focus-visible:after:w-0.5 focus-visible:after:bg-primary"
                    />
                  )}
                </th>
              ))}
              <th
                scope="col"
                className="sticky right-0 z-10 bg-background px-1 py-1 text-right shadow-[-1px_0_0_var(--color-border)]"
              >
                <span className="sr-only">{tc.table.actions}</span>
                <TableSettings
                  all={ordered}
                  hidden={hiddenIds}
                  order={ordered.map((c) => c.id)}
                  density={prefs.density}
                  pageSize={prefs.pageSize}
                  onHidden={(hidden) => update({ hidden })}
                  onOrder={(order) => update({ order })}
                  onDensity={(density) => update({ density })}
                  onPageSize={(n) => update({ pageSize: n })}
                  onReset={reset}
                />
              </th>
            </tr>
          </thead>
          <tbody className="divide-y">
            {busy &&
              Array.from({ length: 5 }, (_, i) => (
                <tr key={i} className={ROW_HEIGHT[prefs.density]} aria-hidden="true">
                  <td colSpan={colCount} className="px-3">
                    <Skeleton className="h-4 w-full" />
                  </td>
                </tr>
              ))}
            {!busy && error && (
              <tr>
                <td colSpan={colCount} className="px-3 py-10 text-center text-sm text-muted-foreground">
                  <p>{error}</p>
                  {onRetry && (
                    <Button type="button" variant="outline" className="mt-3 h-10" onClick={onRetry}>
                      {tc.table.retry}
                    </Button>
                  )}
                </td>
              </tr>
            )}
            {!busy && !error && rows.length === 0 && (
              <tr>
                <td colSpan={colCount} className="px-3 py-10 text-center text-sm text-muted-foreground">
                  {empty}
                </td>
              </tr>
            )}
            {!busy &&
              !error &&
              rows.map((row) => {
                const rid = rowId(row)
                return (
                  <tr
                    key={rid}
                    {...rowAttrs?.(row)}
                    data-row={rid}
                    aria-selected={selected?.has(rid) || undefined}
                    className={`group/row ${ROW_HEIGHT[prefs.density]} transition-colors hover:bg-muted/40 ${selected?.has(rid) ? 'bg-primary/5' : ''} ${rowClass?.(row) ?? ''}`}
                  >
                    {selection && (
                      <td className="px-0">
                        <label className="flex size-10 cursor-pointer items-center justify-center">
                          <input
                            type="checkbox"
                            aria-label={tc.table.selectRow(rowLabel(row))}
                            checked={selected?.has(rid) ?? false}
                            onClick={(e) => (shiftHeld.current = e.shiftKey)}
                            onChange={() => toggleRow(rid)}
                            className="size-4 accent-[var(--color-primary)]"
                          />
                        </label>
                      </td>
                    )}
                    {shown.map((c, i) =>
                      i === 0 ? (
                        <th
                          key={c.id}
                          scope="row"
                          data-column={c.id}
                          className={`min-w-0 px-3 font-normal ${c.align === 'right' ? 'text-right' : ''}`}
                        >
                          <div className="min-w-0 truncate">{c.cell(row)}</div>
                        </th>
                      ) : (
                        <td
                          key={c.id}
                          data-column={c.id}
                          className={`min-w-0 px-3 ${c.align === 'right' ? 'text-right tabular-nums' : ''}`}
                        >
                          <div className="min-w-0 truncate">{c.cell(row)}</div>
                        </td>
                      ),
                    )}
                    <td className="sticky right-0 bg-background px-1 text-right shadow-[-1px_0_0_var(--color-border)]">
                      <div className="flex items-center justify-end gap-1">{actions?.(row)}</div>
                    </td>
                  </tr>
                )
              })}
          </tbody>
        </table>
      </div>
      {squeezed > 0 && (
        <p className="text-xs text-muted-foreground" data-slot="columns-hidden">
          {tc.table.hiddenNarrow(squeezed)}
        </p>
      )}
      {footer}
      {paging && (
        <div className="flex flex-wrap items-center justify-between gap-3" data-slot="table-footer">
          <label className="flex items-center gap-2 text-sm text-muted-foreground">
            {tc.table.pageSize}
            <span className="w-20">
              <NativeSelect
                className="h-10"
                value={String(prefs.pageSize)}
                onChange={(e) => update({ pageSize: Number(e.target.value) })}
              >
                {PAGE_SIZES.map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </NativeSelect>
            </span>
          </label>
          <div className="flex items-center gap-2">
            <span className="text-sm text-muted-foreground" aria-live="polite">
              {tc.table.page(paging.page)}
            </span>
            <Button type="button" variant="outline" className="h-10" disabled={!paging.hasPrev} onClick={paging.onPrev}>
              <ChevronLeft className="size-4" aria-hidden="true" />
              {tc.table.previous}
            </Button>
            <Button type="button" variant="outline" className="h-10" disabled={!paging.hasNext} onClick={paging.onNext}>
              {tc.table.next}
              <ChevronRight className="size-4" aria-hidden="true" />
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

// The menu at the table's far right: which columns, in which order, how tall a row, how many to a page, and back to
// how it was. A ghost icon: no border or fill until the pointer or the keyboard is on it.
function TableSettings<T>({
  all,
  hidden,
  order,
  density,
  pageSize,
  onHidden,
  onOrder,
  onDensity,
  onPageSize,
  onReset,
}: {
  all: Column<T>[]
  hidden: string[]
  order: string[]
  density: Density
  pageSize: number
  onHidden: (h: string[]) => void
  onOrder: (o: string[]) => void
  onDensity: (d: Density) => void
  onPageSize: (n: number) => void
  onReset: () => void
}) {
  const move = (i: number, by: -1 | 1) => {
    const next = [...order]
    const j = i + by
    if (j < 0 || j >= next.length) return
    ;[next[i], next[j]] = [next[j], next[i]]
    onOrder(next)
  }
  // Closes on Escape and on a click outside (the popover does that); focus returns to the button.
  const [open, setOpen] = useState(false)
  useEffect(() => {
    if (!open) return
    const onKey = (e: globalThis.KeyboardEvent) => e.key === 'Escape' && setOpen(false)
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open])
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        aria-label={tc.table.settings}
        data-slot="table-settings"
        className="inline-flex size-10 items-center justify-center rounded-md text-muted-foreground outline-none transition-colors hover:bg-muted hover:text-foreground focus-visible:bg-muted focus-visible:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 data-popup-open:bg-muted"
      >
        <ListFilter className="size-4" strokeWidth={1.75} aria-hidden="true" />
      </PopoverTrigger>
      <PopoverContent align="end" className="w-72 max-w-[calc(100vw-1rem)] gap-3 p-3">
        <div className="grid gap-1">
          <h3 className="text-sm font-medium">{tc.table.columns}</h3>
          <p className="text-xs text-muted-foreground">{tc.table.columnsHelp}</p>
          <ul className="mt-1 grid gap-0.5">
            {all.map((c, i) => (
              <li key={c.id} className="flex items-center gap-1">
                <label className="flex min-h-10 min-w-0 flex-1 items-center gap-2 rounded-md px-1 text-sm">
                  <input
                    type="checkbox"
                    className="size-4 accent-[var(--color-primary)]"
                    checked={c.required || !hidden.includes(c.id)}
                    disabled={c.required}
                    onChange={(e) => onHidden(e.target.checked ? hidden.filter((h) => h !== c.id) : [...hidden, c.id])}
                  />
                  <span className="truncate">{c.header}</span>
                </label>
                <button
                  type="button"
                  aria-label={tc.table.moveUp(c.header)}
                  disabled={i === 0}
                  onClick={() => move(i, -1)}
                  className="inline-flex size-10 items-center justify-center rounded-md text-muted-foreground outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-30"
                >
                  <ChevronUp className="size-4" aria-hidden="true" />
                </button>
                <button
                  type="button"
                  aria-label={tc.table.moveDown(c.header)}
                  disabled={i === all.length - 1}
                  onClick={() => move(i, 1)}
                  className="inline-flex size-10 items-center justify-center rounded-md text-muted-foreground outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-30"
                >
                  <ChevronDown className="size-4" aria-hidden="true" />
                </button>
              </li>
            ))}
          </ul>
        </div>
        <fieldset className="grid gap-1">
          <legend className="mb-1 text-sm font-medium">{tc.table.density}</legend>
          <div className="flex gap-1">
            {(['compact', 'comfortable'] as const).map((d) => (
              <button
                key={d}
                type="button"
                aria-pressed={density === d}
                onClick={() => onDensity(d)}
                className={`h-10 flex-1 rounded-md border text-sm outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 ${density === d ? 'border-primary bg-primary/10 font-medium' : 'hover:bg-muted'}`}
              >
                {tc.table[d]}
              </button>
            ))}
          </div>
        </fieldset>
        <label className="grid gap-1 text-sm font-medium">
          {tc.table.pageSize}
          <NativeSelect className="h-10" value={String(pageSize)} onChange={(e) => onPageSize(Number(e.target.value))}>
            {PAGE_SIZES.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </NativeSelect>
        </label>
        <Button type="button" variant="outline" className="h-10" onClick={onReset}>
          {tc.table.reset}
        </Button>
      </PopoverContent>
    </Popover>
  )
}
