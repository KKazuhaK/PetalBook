import { useEffect, useMemo, useRef, useState, type DragEvent, type KeyboardEvent, type RefObject } from 'react'
import { Link } from 'react-router'
import { MoveRight } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { useBoardColumn, useGroups, usePatchProps, useSpacePermissions, useUrls } from '@/api/hooks'
import type { ListParams, PageSummary, PageType, PropFilter } from '@/api/types'
import { ErrorState, Spinner } from '@/components/common'
import { TagChip } from '@/components/TagChip'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Skeleton } from '@/components/ui/skeleton'
import { cardTitleLine } from '@/lib/listView'
import { boardColumns, cardFieldsOf, cardMovable, columnDroppable, columnFilter, type BoardColumn } from '@/lib/savedViews'
import { hasValue, showValue } from '@/lib/pageTypes'
import { t } from '@/i18n'
import { tb } from '@/i18n/boards'

// The board of a folder or a tag: a column per value of one property, the pages as cards in them. Every column is
// asked for on its own, twenty cards at a time, by the filter that picks its pages (the value, "none of the options",
// "no value"); the counts at the heads come from the index, not from the cards, so a column nobody is in is not even
// asked for, and neither is one that has not come near the visible part of the board. Moving a card to another column
// changes that property of its page: to an option of a select (the value is the typed one), or to "empty". The columns of
// a board made of the values the pages have are text from the index, which is not what a page holds (a number, a
// cut value), so nothing is dropped on them. Without a mouse, a card is reached with Tab (and the arrow keys between
// cards), and its "Move to…" menu lists the columns it can be dropped on.

export type BoardScope = { folder: string } | { tag: string }

interface Props {
  scope: BoardScope
  // The type of the folder (a tag's pages have none): it gives the options, in order, and the fields of a card.
  type: PageType | undefined
  // The property the columns are made of.
  group: string
  sort?: string
  filters: PropFilter[]
  cardFields?: string[]
}

// How close to the bottom of a column (in pixels) the next cards are asked for.
const AHEAD_PX = 160
// How far (in pixels) from the visible part of the board a column is asked for.
const COLUMN_AHEAD_PX = 400

// Whether the element has been within `margin` pixels of the visible part of `root`, ever: a column that has been
// asked for stays asked for. Without IntersectionObserver every column is near.
function useSeenIn(root: RefObject<HTMLElement | null>, el: RefObject<HTMLElement | null>, margin: number): boolean {
  const [seen, setSeen] = useState(typeof IntersectionObserver === 'undefined')
  useEffect(() => {
    const target = el.current
    if (seen || !target || typeof IntersectionObserver === 'undefined') return
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) setSeen(true)
      },
      { root: root.current, rootMargin: `0px ${margin}px 0px ${margin}px` },
    )
    io.observe(target)
    return () => io.disconnect()
  }, [root, el, margin, seen])
  return seen
}

export default function BoardView({ scope, type, group, sort, filters, cardFields }: Props) {
  const { write } = useSpacePermissions()
  const field = type?.fields.find((f) => f.key === group)
  // A select field has its options, in the order of the file; any other property has the values the pages have.
  const options = useMemo(() => (field?.kind === 'select' ? (field.options ?? []) : null), [field])
  const where = useMemo(() => ('folder' in scope ? { folder: scope.folder } : { tag: [scope.tag] }), [scope])
  const groups = useGroups({ key: group, ...where, ...(filters.length ? { filter: filters } : {}) })
  const columns = useMemo(() => boardColumns(options, groups.data?.groups, groups.data?.empty, groups.data?.truncated), [options, groups.data])
  const scroller = useRef<HTMLDivElement>(null)
  const shown = cardFieldsOf(type, cardFields, group)

  const patch = usePatchProps()
  // The card being dragged and the column it comes from.
  const drag = useRef<{ row: PageSummary; column: string } | null>(null)
  const [over, setOver] = useState<string | null>(null)
  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set())

  // `said`: the move was asked for without a mouse, and the column it went to is told when it is done.
  const move = (row: PageSummary, from: string, to: BoardColumn, said?: string) => {
    if (!row.rev || from === to.id || !columnDroppable(to, options !== null) || !cardMovable(row.props, row.partialProps, group)) return
    const name = row.title || row.label || tb.board.untitled
    const mark = (on: boolean) =>
      setBusy((prev) => {
        const next = new Set(prev)
        if (on) next.add(row.id)
        else next.delete(row.id)
        return next
      })
    mark(true)
    // Not the callbacks of mutate(): the observer keeps only those of its latest call, so a card whose move was
    // overtaken by another would stay marked as busy, and its failure would not be told.
    patch
      .mutateAsync({ id: row.id, baseRev: row.rev, ...(to.kind === 'empty' ? { unset: [group] } : { set: { [group]: to.value } }) })
      .then(() => {
        if (said) toast.success(tb.board.moved(name, said))
      }, (e: unknown) => {
        // The hook has read the card again; nothing of the move was kept.
        if (e instanceof ApiError && e.code === 'conflict') toast.error(tb.board.conflict(name))
        else toast.error(tb.board.moveFailed(name, e instanceof Error ? e.message : ''))
      })
      .finally(() => mark(false))
  }

  if (group === '') return <p className="py-16 text-center text-sm text-muted-foreground">{tb.board.needsGroup}</p>

  const label = (c: BoardColumn) => (c.kind === 'other' ? tb.board.other : c.kind === 'empty' ? tb.board.empty : (c.value as string))
  // Where a card can go from the keyboard: the columns a drop is taken on.
  const targets = columns.filter((c) => columnDroppable(c, options !== null)).map((c) => ({ column: c, title: label(c) }))
  return (
    <div data-slot="board-view" data-group={group} className="grid gap-2">
      {groups.isError && <p className="text-xs text-destructive">{tb.board.groupsFailed}</p>}
      <div ref={scroller} className="flex items-start gap-3 overflow-x-auto pb-3" role="group" aria-label={tb.board.label}>
        {columns.map((c) => (
          <Column
            key={c.id}
            column={c}
            scroller={scroller}
            title={label(c)}
            // Asked for once the counts say there is something in it (or they could not be had).
            enabled={groups.isError || (c.count !== undefined && c.count > 0)}
            params={{ ...where, ...(sort ? { sort: sort as ListParams['sort'] } : {}), filter: [...filters, columnFilter(c, group, options)] }}
            group={group}
            shown={shown.map((f) => ({ key: f.key, label: f.title || f.key }))}
            write={write}
            busy={busy}
            droppable={columnDroppable(c, options !== null)}
            canDrop={(from) => drag.current !== null && columnDroppable(c, options !== null) && from !== c.id}
            over={over === c.id}
            onOver={(on) => setOver((prev) => (on ? c.id : prev === c.id ? null : prev))}
            onDragStart={(row) => (drag.current = { row, column: c.id })}
            onDragEnd={() => {
              drag.current = null
              setOver(null)
            }}
            onDrop={() => {
              const d = drag.current
              drag.current = null
              setOver(null)
              if (d) move(d.row, d.column, c)
            }}
            dragSource={() => drag.current}
            targets={targets}
            onMove={(row, to) => move(row, c.id, to, label(to))}
          />
        ))}
      </div>
      {columns.some((c) => c.kind === 'other') && write && <p className="sr-only">{tb.board.dropOther}</p>}
      {options === null && write && <p className="text-xs text-muted-foreground">{tb.board.valuesReadOnly}</p>}
    </div>
  )
}

function Column({
  column,
  scroller,
  title,
  enabled,
  params,
  group,
  shown,
  write,
  busy,
  droppable: dropTarget,
  canDrop,
  over,
  onOver,
  onDragStart,
  onDragEnd,
  onDrop,
  dragSource,
  targets,
  onMove,
}: {
  column: BoardColumn
  scroller: RefObject<HTMLElement | null>
  title: string
  enabled: boolean
  params: Omit<ListParams, 'cursor' | 'limit' | 'props'>
  group: string
  shown: { key: string; label: string }[]
  write: boolean
  busy: ReadonlySet<string>
  droppable: boolean
  canDrop: (from: string) => boolean
  over: boolean
  onOver: (on: boolean) => void
  onDragStart: (row: PageSummary) => void
  onDragEnd: () => void
  onDrop: () => void
  dragSource: () => { column: string } | null
  targets: { column: BoardColumn; title: string }[]
  onMove: (row: PageSummary, to: BoardColumn) => void
}) {
  const box = useRef<HTMLElement>(null)
  const near = useSeenIn(scroller, box, COLUMN_AHEAD_PX)
  const asked = enabled && near
  const query = useBoardColumn(params, asked)
  const rows = useMemo(() => query.data?.pages.flatMap((p) => p.items) ?? [], [query.data])
  const { hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage } = query
  const droppable = write && dropTarget

  return (
    <section
      ref={box}
      data-column={column.id}
      aria-label={tb.board.columnLabel(title, column.count ?? '…')}
      className={`flex w-72 shrink-0 flex-col rounded-xl border bg-muted/30 transition-colors ${over ? 'border-ring bg-muted/60' : ''}`}
      onDragOver={(e: DragEvent) => {
        if (!droppable || !dragSource() || !canDrop(dragSource()!.column)) return
        e.preventDefault()
        onOver(true)
      }}
      onDragLeave={(e: DragEvent) => {
        if (!(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node | null)) onOver(false)
      }}
      onDrop={(e: DragEvent) => {
        if (!droppable || !dragSource()) return
        e.preventDefault()
        if (canDrop(dragSource()!.column)) onDrop()
        else onDragEnd()
      }}
    >
      <header className="flex items-center justify-between gap-2 px-3 py-2 text-sm font-medium">
        <span className={`truncate ${column.kind !== 'option' ? 'text-muted-foreground' : ''}`}>{title}</span>
        <span className="rounded-full bg-secondary px-2 text-xs tabular-nums text-muted-foreground" data-slot="column-count">
          {column.count ?? '…'}
        </span>
      </header>
      <div
        className="max-h-[calc(100dvh-17rem)] min-h-16 space-y-2 overflow-y-auto px-2 pb-2"
        onScroll={(e) => {
          const el = e.currentTarget
          if (el.scrollHeight - el.scrollTop - el.clientHeight < AHEAD_PX && hasNextPage && !isFetchingNextPage && !isFetchNextPageError) void fetchNextPage()
        }}
      >
        {query.isPending && asked ? (
          <Skeleton className="h-20 w-full" aria-busy="true" />
        ) : query.isError && rows.length === 0 ? (
          <ErrorState message={t.fmt.labelled(tb.board.loadFailed, query.error.message)} onRetry={() => void query.refetch()} />
        ) : rows.length === 0 && !asked && enabled ? (
          <Skeleton className="h-20 w-full" aria-hidden="true" />
        ) : rows.length === 0 ? (
          <p className="px-1 py-3 text-center text-xs text-muted-foreground">{tb.board.noCards}</p>
        ) : (
          rows.map((row) => (
            <Card key={row.id} row={row} group={group} shown={shown} write={write} busy={busy.has(row.id)} columnId={column.id} onDragStart={onDragStart} onDragEnd={onDragEnd} targets={targets.filter((x) => x.column.id !== column.id)} onMove={(to) => onMove(row, to)} />
          ))
        )}
        {hasNextPage && (
          <Button variant="ghost" size="sm" className="w-full" onClick={() => void fetchNextPage()} disabled={isFetchingNextPage}>
            {isFetchingNextPage ? <Spinner /> : null}
            {isFetchingNextPage ? tb.board.loadingMore : tb.board.loadMore}
          </Button>
        )}
        {isFetchNextPageError && <ErrorState message={query.error.message} onRetry={() => void fetchNextPage()} />}
      </div>
    </section>
  )
}

// The arrow keys move the focus between cards: up and down in a column, left and right to the next column that has
// cards (at the same place, or its last card).
function focusNear(e: KeyboardEvent<HTMLElement>) {
  if (e.target !== e.currentTarget || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return
  const dir = { ArrowUp: [0, -1], ArrowDown: [0, 1], ArrowLeft: [-1, 0], ArrowRight: [1, 0] }[e.key]
  if (!dir) return
  const card = e.currentTarget
  const board = card.closest('[data-slot="board-view"]')
  const column = card.closest('[data-column]')
  if (!board || !column) return
  const cardsOf = (c: Element) => [...c.querySelectorAll<HTMLElement>('[data-card]')]
  const here = cardsOf(column)
  const at = here.indexOf(card)
  let next: HTMLElement | undefined
  if (dir[1] !== 0) next = here[at + dir[1]]
  else {
    const cols = [...board.querySelectorAll('[data-column]')]
    for (let i = cols.indexOf(column) + dir[0]; i >= 0 && i < cols.length && !next; i += dir[0]) {
      const there = cardsOf(cols[i])
      next = there[Math.min(at, there.length - 1)]
    }
  }
  if (!next) return
  e.preventDefault()
  next.focus()
}

function Card({ row, group, shown, write, busy, columnId, onDragStart, onDragEnd, targets, onMove }: { row: PageSummary; group: string; shown: { key: string; label: string }[]; write: boolean; busy: boolean; columnId: string; onDragStart: (row: PageSummary) => void; onDragEnd: () => void; targets: { column: BoardColumn; title: string }[]; onMove: (to: BoardColumn) => void }) {
  const urls = useUrls()
  const line = cardTitleLine(row)
  const partial = row.partialProps?.includes(group) === true
  const movable = write && !!row.rev && cardMovable(row.props, row.partialProps, group)
  const values = shown.map((f) => ({ ...f, value: row.props?.[f.key] })).filter((f) => hasValue(f.value))
  const name = row.title || row.label || tb.board.untitled
  return (
    <div
      role="group"
      tabIndex={0}
      onKeyDown={focusNear}
      data-card={row.id}
      data-column-of={columnId}
      data-movable={movable || undefined}
      data-partial={partial || undefined}
      aria-busy={busy || undefined}
      aria-label={name}
      title={write && partial ? tb.board.partial : undefined}
      draggable={movable && !busy}
      onDragStart={(e) => {
        if (!movable || busy) return e.preventDefault()
        onDragStart(row)
        e.dataTransfer?.setData('application/x-petalbook-card', row.id)
        if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
      }}
      onDragEnd={onDragEnd}
      className={`relative rounded-lg border bg-background p-3 text-sm shadow-xs outline-none focus-visible:ring-2 focus-visible:ring-ring/50 ${movable ? 'cursor-grab active:cursor-grabbing' : ''} ${busy ? 'opacity-60' : ''}`}
    >
      {movable && targets.length > 0 && (
        <DropdownMenu>
          <DropdownMenuTrigger
            disabled={busy}
            render={<button type="button" aria-label={tb.board.moveTo(name)} title={tb.board.moveTo(name)} className="float-right -mt-1 -mr-1 ml-1 flex size-6 items-center justify-center rounded-md text-muted-foreground outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-50" />}
          >
            <MoveRight className="size-3.5" />
          </DropdownMenuTrigger>
          <DropdownMenuContent className="w-auto min-w-40" aria-label={tb.board.moveTo(name)}>
            {targets.map((x) => (
              <DropdownMenuItem key={x.column.id} data-target={x.column.id} onClick={() => onMove(x.column)}>
                {x.title}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      )}
      <Link to={urls.page(row.id)} draggable={false} className="block outline-none focus-visible:underline">
        {line ? (
          <>
            <div className="font-medium leading-snug break-words">{line}</div>
            {row.excerpt && <div className="mt-1 line-clamp-2 text-xs leading-relaxed break-words text-muted-foreground">{row.excerpt}</div>}
          </>
        ) : (
          // Without a title the start of the text stands in for it.
          <div className="line-clamp-2 leading-snug break-words">{row.excerpt || <span className="text-muted-foreground">{tb.board.untitled}</span>}</div>
        )}
      </Link>
      {(row.tags?.length || values.length > 0) && (
        <div className="mt-2 grid gap-1 text-xs text-muted-foreground">
          {values.map((f) => (
            <div key={f.key} data-field={f.key} className="flex min-w-0 gap-1">
              <span className="shrink-0">{f.label}</span>
              <span className="truncate text-foreground">{showValue(f.value)}</span>
            </div>
          ))}
          {row.tags && row.tags.length > 0 && (
            <div className="flex flex-wrap gap-1">
              {row.tags.map((tag) => (
                <TagChip key={tag} tag={tag} />
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  )
}
