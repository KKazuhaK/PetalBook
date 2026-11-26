import {
  cloneElement,
  createContext,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactElement,
  type ReactNode,
} from 'react'
import { scrollFadeTouchClass, useScrollFade } from '@/components/ui/scroll-fade'
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
import {
  autoWidths,
  leastWidths,
  clampShares,
  dropColumn,
  fitColumn,
  dropGap,
  pixelWidths,
  resetPair,
  resizePair,
  roomOf,
  stepColumn,
} from '@/lib/columnLayout'
import { orderIds, PAGE_SIZES, shares, useTablePrefs, type Density } from '@/lib/tablePrefs'

// A table of rows the server pages by cursor, for lists that grow to hundreds and thousands. What it does, so a list
// does not have to:
//  - the columns an administrator shows, hides and orders (by dragging a header, or Alt and an arrow on it), their widths (kept as shares of the table, never pixels,
//    each at least its own least width), the density and the page size, all kept in this browser only;
//  - until a width is set, each column is as wide as its content on the page shown (one measure of the visible cells
//    after the rows or the columns change), up to a cap, the room left over shared by weight; a double click on a
//    column's edge, or Enter on it, fits that column again;
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
  // The least width in a narrow box (under 640px) whose columns do not fit at their least widths (default 140, or
  // `min` if that is less): the column gives way to it before the table scrolls.
  narrowMin?: number
  // The share of the room left over once every column has its content's width, against the others (default 1).
  weight?: number
  // The most width the column takes to fit its content, in pixels (default 320): a longer text is cut there, ends in
  // an ellipsis and shows in full as a tooltip. A column of badges or dates never reaches it.
  maxAutoWidth?: number
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
  // The sticky last column has an opaque background (rows scroll under it); it paints the row's tint, --row-tint, over it.
  rowClass?: (row: T) => string
  footer?: ReactNode
  // The columns shown when the person has set none themselves (a saved view's choice, say): ids to hide.
  defaultHidden?: string[]
}

// A cell cut short shows what it says in full as a tooltip, set when the pointer comes to it (never for every cell).
function titleIfClipped(e: ReactPointerEvent<HTMLElement>) {
  const el = e.currentTarget
  if (el.scrollWidth > el.clientWidth + 1) el.title = el.textContent ?? ''
  else el.removeAttribute('title')
}

const NarrowContext = createContext(false)

// A row's action as a button with an icon and a label: the label goes while the table's box is narrow (it stays as
// the button's accessible name and tooltip, and the button keeps a 40px target). `render` makes it a link.
export function RowAction({
  icon,
  label,
  ariaLabel,
  render,
  onClick,
}: {
  icon?: ReactNode
  label: string
  ariaLabel?: string
  render?: ReactElement<Record<string, unknown>>
  onClick?: () => void
}) {
  const narrow = useContext(NarrowContext)
  const name = ariaLabel ?? label
  return (
    <Button
      type={render ? undefined : 'button'}
      variant="ghost"
      className={narrow ? 'size-10 px-0' : 'h-10 px-3'}
      title={narrow ? label : undefined}
      aria-label={narrow ? name : ariaLabel}
      render={render && cloneElement(render, { 'aria-label': name, title: narrow ? label : undefined })}
      nativeButton={render ? false : undefined}
      onClick={onClick}
    >
      {icon}
      {!narrow && label}
    </Button>
  )
}

const ROW_HEIGHT: Record<Density, string> = { compact: 'h-10', comfortable: 'h-14' }
const SELECT_W = 40
const ACTIONS_W = 148
const SETTINGS_W = 48
// A box narrower than this (the table's own box, not the screen) collapses its row actions to icons.
const NARROW_BOX = 640
const NARROW_ACTIONS_W = 96
const AUTO_MAX = 320
// The padding of a cell (px-3) and of a header's sort button (px-2 with a -mx-2), in pixels, added to what is measured.
const CELL_PAD = 24
const SORT_PAD = 8
// A mouse starts a column drag after this many pixels; a finger after a press this long (milliseconds) without moving
// further than the slop, so a tap still sorts and a swipe still scrolls.
const DRAG_PX = 5
const PRESS_MS = 350
const PRESS_SLOP = 8
// The soft shade at the sticky column's left edge while rows scroll under it (never a line).
const STICKY_SHADE =
  'before:pointer-events-none before:absolute before:inset-y-0 before:right-full before:w-4 before:bg-linear-to-l before:from-foreground/[0.06] before:to-transparent before:opacity-(--sticky-shade,0) before:transition-opacity before:duration-150'

interface HeaderDrag {
  id: string
  // The pointer, and where on the header it took hold; the header's top and width, for the ghost.
  x: number
  grabX: number
  top: number
  width: number
  label: string
  gap: number
}

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
  useScrollFade(box)
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

  // Whether rows scroll under the sticky column (more to the right than shows): only then is it shaded.
  useEffect(() => {
    const el = box.current
    if (!el) return
    const update = () => {
      const max = el.scrollWidth - el.clientWidth
      el.style.setProperty('--sticky-shade', max > 1 && Math.abs(el.scrollLeft) < max - 1 ? '1' : '0')
    }
    update()
    el.addEventListener('scroll', update, { passive: true })
    const ro = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(update)
    ro?.observe(el)
    if (el.firstElementChild) ro?.observe(el.firstElementChild)
    return () => {
      el.removeEventListener('scroll', update)
      ro?.disconnect()
    }
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
  // A box measured and narrower than NARROW_BOX has its row actions as icons; one never measured keeps them in full.
  const narrow = boxW > 0 && boxW < NARROW_BOX
  const lastW = actions ? (narrow ? NARROW_ACTIONS_W : ACTIONS_W + SETTINGS_W) : SETTINGS_W
  const fixed = (selection ? SELECT_W : 0) + lastW
  const declaredMin = (c: Column<T>) => c.min ?? 96

  // What still fits: the least important column goes first (the last of those, when equal) until the rest fit. A
  // width of 0 is a box that was never measured (a test, a hidden tab): nothing is taken away.
  const shown = (() => {
    const list = [...chosen]
    if (boxW <= 0) return list
    const total = () => list.reduce((n, c) => n + declaredMin(c), fixed)
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
  // The least widths as they are in this box: the declared ones, or in a narrow box where those do not fit, the narrow ones.
  const least = leastWidths(
    shown.map((c) => ({ id: c.id, min: declaredMin(c), narrowMin: c.narrowMin ?? Math.min(declaredMin(c), 140) })),
    Math.max(0, boxW - fixed),
    narrow,
  )
  const minOf = (c: Column<T>) => least[c.id] ?? declaredMin(c)
  const tableMin = shown.reduce((n, c) => n + minOf(c), fixed)
  const table = useRef<HTMLTableElement>(null)

  // What each shown column's header and cells need to show in full, in pixels. The elements marked `data-fit` are
  // given their natural width for the one read (all at once, so the browser lays out once) and put back.
  const measureFit = () => {
    const els = Array.from(table.current?.querySelectorAll<HTMLElement>('[data-fit]') ?? [])
    const kept = els.map((e) => [e.style.width, e.style.maxWidth])
    for (const e of els) {
      e.style.width = 'max-content'
      e.style.maxWidth = 'none'
    }
    const out: Record<string, number> = {}
    for (const e of els) {
      const id = e.closest<HTMLElement>('[data-column]')?.dataset.column
      if (!id) continue
      const w = Math.ceil(e.getBoundingClientRect().width) + (e.tagName === 'BUTTON' ? SORT_PAD : CELL_PAD)
      out[id] = Math.max(out[id] ?? 0, w)
    }
    els.forEach((e, i) => {
      e.style.width = kept[i][0]
      e.style.maxWidth = kept[i][1]
    })
    return out
  }
  // Measured once after the rows, the shown columns or the density change; never in a box that has no width.
  const [natural, setNatural] = useState<Record<string, number>>({})
  const measurable = boxW > 0
  const keepNatural = (next: Record<string, number>) =>
    setNatural((prev) => {
      const ids = Object.keys(next)
      return ids.length === Object.keys(prev).length && ids.every((k) => prev[k] === next[k]) ? prev : next
    })
  // What the rows are, not which array holds them: a list that builds a new array each render does not measure again.
  const shownKey = shown.map((c) => c.id).join()
  const rowsKey = rows.map(rowId).join('\u0001')
  const hasSaved = Object.keys(prefs.widths).length > 0
  useLayoutEffect(() => {
    if (!measurable || hasSaved) return
    // Layout is read from the DOM after it is committed, before it is painted: the one place this can be known.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    keepNatural(measureFit())
  }, [measurable, hasSaved, rowsKey, shownKey, prefs.density, loading, error])
  // The columns' widths are whole pixels of the room they share. (A `calc()` with a percentage is not a width a
  // `<col>` understands: the browser took it for auto, every column came out the same and no drag changed anything.)
  // A box never measured (a test, a hidden tab) keeps plain percentages.
  // Nobody has set a width (and none is being dragged): each column starts as wide as its content.
  const auto =
    boxW > 0 && !draft && !hasSaved && shown.length > 0
      ? autoWidths(
          shown.map((c) => ({
            id: c.id,
            content: natural[c.id] ?? 0,
            min: minOf(c),
            max: c.maxAutoWidth ?? AUTO_MAX,
            weight: c.weight ?? 1,
          })),
          Math.max(0, boxW - fixed),
        )
      : null
  const room = auto ? auto.total : roomOf(boxW > 0 ? Math.max(boxW, tableMin) : 0, fixed)
  const shownMins = Object.fromEntries(shown.map((c) => [c.id, minOf(c)]))
  const have = auto
    ? Object.fromEntries(shown.map((c) => [c.id, auto.widths[c.id] / (room || 1)]))
    : clampShares(
        shares(
          shown.map((c) => ({ id: c.id, weight: c.weight ?? 1 })),
          draft ?? prefs.widths,
        ),
        shownMins,
        room,
      )
  const px = auto
    ? auto.widths
    : boxW > 0
      ? pixelWidths(
          shown.map((c) => c.id),
          have,
          room,
        )
      : null
  // The room as it is now, and the least widths against it: a table never measured (a test, a hidden tab) has no
  // room to speak of, so a width there moves by shares of a nominal 100 and has no least.
  const geometry = (left: Column<T>, right: Column<T>) => {
    const r = roomOf(table.current?.clientWidth || boxW, fixed)
    return r > 0 ? { r, lo: minOf(left), hi: minOf(right) } : { r: 100, lo: 0, hi: 0 }
  }

  // What ends a drag in progress (it takes the listeners off the window), so a table that goes away mid-drag leaves none.
  const dragEnd = useRef<(() => void) | null>(null)
  useEffect(() => () => dragEnd.current?.(), [])
  // Two presses on an edge close together are a double click, counted here because a press whose default was
  // prevented does not always go on to make the browser's own `dblclick`.
  const lastPress = useRef({ at: 0, x: 0, i: -1 })
  const startResize = (i: number, e: ReactPointerEvent) => {
    dragEnd.current?.()
    const prev = lastPress.current
    lastPress.current = { at: e.timeStamp, x: e.clientX, i }
    if (prev.i === i && e.timeStamp - prev.at < 400 && Math.abs(e.clientX - prev.x) < 6) {
      lastPress.current = { at: 0, x: 0, i: -1 }
      resetEdge(i)
      return
    }
    const clientX = e.clientX
    const pid = e.pointerId
    const left = shown[i]
    const right = shown[i + 1]
    if (!right) return
    const start = { ...have }
    const at = (x: number) => {
      const g = geometry(left, right)
      return resizePair(start, left.id, right.id, x - clientX, g.r, g.lo, g.hi)
    }
    const onMove = (ev: PointerEvent) => {
      if (ev.pointerId === pid || !pid) setDraft(at(ev.clientX))
    }
    const stop = () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', done)
      window.removeEventListener('pointercancel', cancel)
      dragEnd.current = null
    }
    const cancel = () => {
      stop()
      setDraft(null)
    }
    const done = (ev: PointerEvent) => {
      stop()
      const next = at(ev.clientX)
      // A press with no movement (the first half of a double click) changes nothing.
      if (next[left.id] !== start[left.id]) update({ widths: next })
      setDraft(null)
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', done)
    window.addEventListener('pointercancel', cancel)
    dragEnd.current = stop
  }
  // A double click on an edge fits the column to its left to its content; in a table that was never measured (a test,
  // a hidden tab) there is no content width, and it puts the two columns beside it back to their usual proportion.
  const resetEdge = (i: number) => {
    const left = shown[i]
    const right = shown[i + 1]
    if (!right) return
    if (boxW > 0) fitEdge(i)
    else update({ widths: resetPair(have, left.id, right.id, left.weight ?? 1, right.weight ?? 1) })
  }
  const fitEdge = (i: number) => {
    const c = shown[i]
    const want = measureFit()[c.id] ?? 0
    const target = Math.min(Math.max(want, minOf(c)), Math.max(c.maxAutoWidth ?? AUTO_MAX, minOf(c)))
    update({ widths: fitColumn(have, c.id, target, room, shownMins) })
  }
  const nudge = (i: number, e: KeyboardEvent) => {
    if (e.key === 'Enter' && !e.altKey) {
      e.preventDefault()
      resetEdge(i)
      return
    }
    const step = e.key === 'ArrowLeft' ? -1 : e.key === 'ArrowRight' ? 1 : 0
    const left = shown[i]
    const right = shown[i + 1]
    if (!step || !right || e.altKey) return
    e.preventDefault()
    const g = geometry(left, right)
    update({ widths: resizePair(have, left.id, right.id, step * 0.01 * g.r, g.r, g.lo, g.hi) })
  }

  // Moving a column: by dragging its header, or Alt and an arrow on it. The order is all the columns', hidden ones too.
  const [drag, setDrag] = useState<HeaderDrag | null>(null)
  const [moved, setMoved] = useState('')
  const refocus = useRef<string | null>(null)
  const order = ordered.map((c) => c.id)
  const shownIds = shown.map((c) => c.id)
  // The gap a dragged column would drop into, unless that leaves it where it is.
  const from = drag ? shownIds.indexOf(drag.id) : -1
  const dropAt = drag && drag.gap !== from && drag.gap !== from + 1 ? drag.gap : -1
  const headCells = () => Array.from(table.current?.querySelectorAll<HTMLElement>('thead th[data-column]') ?? [])
  const startMove = (c: Column<T>, e: ReactPointerEvent<HTMLElement>) => {
    if (shown.length < 2 || (e.pointerType === 'mouse' && e.button !== 0)) return
    dragEnd.current?.()
    const th = e.currentTarget
    const { clientX: sx, clientY: sy, pointerId: pid } = e
    const touch = e.pointerType !== 'mouse'
    const rect = th.getBoundingClientRect()
    let active = false
    let timer = 0
    const gapAt = (x: number) =>
      dropGap(
        headCells().map((h) => h.getBoundingClientRect().left + h.offsetWidth / 2),
        x,
      )
    const begin = (x: number) => {
      active = true
      window.getSelection()?.removeAllRanges()
      setDrag({ id: c.id, x, grabX: sx - rect.left, top: rect.top, width: rect.width, label: c.header, gap: gapAt(x) })
    }
    const onMove = (ev: PointerEvent) => {
      if (ev.pointerId !== pid) return
      if (!active) {
        const far = Math.hypot(ev.clientX - sx, ev.clientY - sy)
        // A finger that moves before the press is long enough is scrolling the page; leave it be.
        if (touch) {
          if (far > PRESS_SLOP) cancel()
        } else if (far >= DRAG_PX) begin(ev.clientX)
        return
      }
      setDrag((d) => d && { ...d, x: ev.clientX, gap: gapAt(ev.clientX) })
    }
    // While a finger holds a column, the page must not scroll under it.
    const noScroll = (ev: Event) => {
      if (active && ev.cancelable) ev.preventDefault()
    }
    const noMenu = (ev: Event) => ev.preventDefault()
    const onKey = (ev: globalThis.KeyboardEvent) => {
      if (ev.key === 'Escape') cancel()
    }
    const stop = () => {
      window.clearTimeout(timer)
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', done)
      window.removeEventListener('pointercancel', cancel)
      window.removeEventListener('touchmove', noScroll)
      window.removeEventListener('keydown', onKey)
      th.removeEventListener('contextmenu', noMenu)
      dragEnd.current = null
    }
    const cancel = () => {
      stop()
      active = false
      setDrag(null)
    }
    const done = (ev: PointerEvent) => {
      const was = active
      const gap = gapAt(ev.clientX)
      cancel()
      if (!was) return
      // The click that ends a drag is not a click on the sort button.
      const eat = (k: Event) => {
        k.stopPropagation()
        k.preventDefault()
      }
      window.addEventListener('click', eat, { capture: true, once: true })
      window.setTimeout(() => window.removeEventListener('click', eat, { capture: true }), 0)
      const next = dropColumn(order, shownIds, c.id, gap)
      if (next.join() !== order.join()) {
        update({ order: next })
        const at = next.filter((id) => shownIds.includes(id))
        setMoved(tc.table.moved(c.header, at.indexOf(c.id) + 1, at.length))
      }
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', done)
    window.addEventListener('pointercancel', cancel)
    window.addEventListener('keydown', onKey)
    if (touch) {
      window.addEventListener('touchmove', noScroll, { passive: false })
      th.addEventListener('contextmenu', noMenu)
      timer = window.setTimeout(() => begin(sx), PRESS_MS)
    }
    dragEnd.current = stop
  }
  const moveByKey = (c: Column<T>, e: KeyboardEvent) => {
    if (!e.altKey || e.ctrlKey || e.metaKey || (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight')) return
    e.preventDefault()
    const next = stepColumn(order, shownIds, c.id, e.key === 'ArrowLeft' ? -1 : 1)
    if (next.join() === order.join()) return
    refocus.current = c.id
    update({ order: next })
    const at = next.filter((id) => shownIds.includes(id))
    setMoved(tc.table.moved(c.header, at.indexOf(c.id) + 1, at.length))
  }
  // Focus stays on the header that moved.
  useLayoutEffect(() => {
    const id = refocus.current
    if (!id) return
    refocus.current = null
    const th = headCells().find((h) => h.dataset.column === id)
    ;(th?.querySelector<HTMLElement>('button') ?? th)?.focus()
  })

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
        className={`${scrollFadeTouchClass} [--fade-right:0px]! rounded-lg border transition-opacity duration-150 ${fetching ? 'opacity-60' : ''}`}
        aria-busy={fetching || loading || undefined}
      >
        <table
          ref={table}
          className="w-full table-fixed border-collapse text-left text-sm"
          style={{ minWidth: auto && auto.total > boxW - fixed ? fixed + auto.total : tableMin }}
          data-density={prefs.density}
        >
          <colgroup>
            {selection && <col style={{ width: SELECT_W }} />}
            {shown.map((c) => (
              <col key={c.id} style={{ width: px ? px[c.id] : `${have[c.id] * 100}%` }} />
            ))}
            <col style={{ width: lastW }} />
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
                  tabIndex={c.sortKey ? undefined : 0}
                  aria-keyshortcuts={shown.length > 1 ? 'Alt+ArrowLeft Alt+ArrowRight' : undefined}
                  onPointerDown={(e) => startMove(c, e)}
                  onKeyDown={(e) => moveByKey(c, e)}
                  className={`group/th relative px-3 py-2 font-medium select-none focus-visible:bg-muted/60 focus-visible:outline-none ${drag?.id === c.id ? 'opacity-40' : ''} ${c.align === 'right' ? 'text-right' : ''}`}
                >
                  {dropAt === i && (
                    <span
                      data-slot="drop-indicator"
                      className="pointer-events-none absolute inset-y-1 left-0 z-20 w-0.5 rounded-full bg-primary"
                    />
                  )}
                  {dropAt === shown.length && i === shown.length - 1 && (
                    <span
                      data-slot="drop-indicator"
                      className="pointer-events-none absolute inset-y-1 right-0 z-20 w-0.5 rounded-full bg-primary"
                    />
                  )}
                  {c.sortKey ? (
                    <button
                      type="button"
                      onClick={() => clickSort(c)}
                      aria-label={tc.table.sortBy(c.header)}
                      data-fit
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
                    <span data-fit className="block truncate">
                      {c.header}
                    </span>
                  )}
                  {i < shown.length - 1 && (
                    <span
                      role="separator"
                      aria-orientation="vertical"
                      aria-label={tc.table.resize(c.header)}
                      tabIndex={0}
                      onPointerDown={(e) => {
                        // Not a drag of the header, and not a text selection.
                        e.preventDefault()
                        e.stopPropagation()
                        startResize(i, e)
                      }}
                      onDoubleClick={() => resetEdge(i)}
                      onKeyDown={(e) => nudge(i, e)}
                      className="group/grip absolute top-0 -right-2 z-10 flex h-full w-4 cursor-col-resize touch-none items-center justify-center outline-none pointer-coarse:-right-3 pointer-coarse:w-6"
                    >
                      <span className="h-5 w-0.5 rounded-full bg-muted-foreground/40 opacity-0 transition-opacity duration-150 group-focus-within/th:opacity-100 group-hover/th:opacity-100 group-hover/grip:bg-primary group-focus-visible/grip:bg-primary" />
                    </span>
                  )}
                </th>
              ))}
              <th
                scope="col"
                className={`sticky right-0 z-10 bg-background bg-[linear-gradient(color-mix(in_oklab,var(--color-muted)_40%,transparent),color-mix(in_oklab,var(--color-muted)_40%,transparent))] px-1 py-1 text-right ${STICKY_SHADE}`}
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
                  onResetColumns={() => update({ order: [], hidden: [], widths: {}, custom: false })}
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
                    className={`group/row ${ROW_HEIGHT[prefs.density]} transition-colors hover:bg-muted/40 hover:[--row-tint:color-mix(in_oklab,var(--color-muted)_40%,transparent)] ${selected?.has(rid) ? 'bg-primary/5 [--row-tint:color-mix(in_oklab,var(--color-primary)_5%,transparent)]' : ''} ${rowClass?.(row) ?? ''}`}
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
                          <div data-fit className="min-w-0 truncate" onPointerEnter={titleIfClipped}>
                            {c.cell(row)}
                          </div>
                        </th>
                      ) : (
                        <td
                          key={c.id}
                          data-column={c.id}
                          className={`min-w-0 px-3 ${c.align === 'right' ? 'text-right tabular-nums' : ''}`}
                        >
                          <div data-fit className="min-w-0 truncate" onPointerEnter={titleIfClipped}>
                            {c.cell(row)}
                          </div>
                        </td>
                      ),
                    )}
                    <td
                      className={`sticky right-0 bg-background bg-[linear-gradient(var(--row-tint,transparent),var(--row-tint,transparent))] px-1 text-right ${STICKY_SHADE}`}
                    >
                      <div className="flex items-center justify-end gap-1">
                        <NarrowContext.Provider value={narrow}>{actions?.(row)}</NarrowContext.Provider>
                      </div>
                    </td>
                  </tr>
                )
              })}
          </tbody>
        </table>
      </div>
      <div role="status" className="sr-only">
        {moved}
      </div>
      {drag && (
        <div
          data-slot="column-ghost"
          aria-hidden="true"
          className="pointer-events-none fixed z-50 truncate rounded-md border bg-background/95 px-3 py-2 text-xs font-medium text-foreground shadow-lg"
          style={{ left: drag.x - drag.grabX, top: drag.top, width: drag.width }}
        >
          {drag.label}
        </div>
      )}
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
  onResetColumns,
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
  onResetColumns: () => void
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
          <Button type="button" variant="outline" className="mt-1 h-10" onClick={onResetColumns}>
            {tc.table.resetColumns}
          </Button>
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
