import { useMemo, useRef, useState, type DragEvent } from 'react'
import { Link } from 'react-router'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { useBoardColumn, useGroups, usePatchProps, useSpacePermissions, useUrls } from '@/api/hooks'
import type { ListParams, PageSummary, PageType, PropFilter } from '@/api/types'
import { ErrorState, Spinner } from '@/components/common'
import { TagChip } from '@/components/TagChip'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { cardTitleLine } from '@/lib/listView'
import { boardColumns, cardFieldsOf, cardMovable, columnFilter, type BoardColumn } from '@/lib/savedViews'
import { hasValue, showValue } from '@/lib/pageTypes'
import { t } from '@/i18n'
import { tb } from '@/i18n/boards'

// The board of a folder or a tag: a column per value of one property, the pages as cards in them. Every column is
// asked for on its own, twenty cards at a time, by the filter that picks its pages (the value, "none of the options",
// "no value"); the counts at the heads come from the index, not from the cards, so a column nobody is in is not even
// asked for. Moving a card to another column changes that property of its page.

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

export default function BoardView({ scope, type, group, sort, filters, cardFields }: Props) {
  const { write } = useSpacePermissions()
  const field = type?.fields.find((f) => f.key === group)
  // A select field has its options, in the order of the file; any other property has the values the pages have.
  const options = useMemo(() => (field?.kind === 'select' ? (field.options ?? []) : null), [field])
  const where = useMemo(() => ('folder' in scope ? { folder: scope.folder } : { tag: [scope.tag] }), [scope])
  const groups = useGroups({ key: group, ...where, ...(filters.length ? { filter: filters } : {}) })
  const columns = useMemo(() => boardColumns(options, groups.data?.groups, groups.data?.empty), [options, groups.data])
  const shown = cardFieldsOf(type, cardFields, group)

  const patch = usePatchProps()
  // The card being dragged and the column it comes from.
  const drag = useRef<{ row: PageSummary; column: string } | null>(null)
  const [over, setOver] = useState<string | null>(null)
  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set())

  const move = (row: PageSummary, from: string, to: BoardColumn) => {
    if (!row.rev || from === to.id || to.kind === 'other' || !cardMovable(row.props, row.partialProps, group)) return
    const name = row.title || row.label || tb.board.untitled
    const mark = (on: boolean) =>
      setBusy((prev) => {
        const next = new Set(prev)
        if (on) next.add(row.id)
        else next.delete(row.id)
        return next
      })
    mark(true)
    patch.mutate(
      { id: row.id, baseRev: row.rev, ...(to.kind === 'empty' ? { unset: [group] } : { set: { [group]: to.value } }) },
      {
        onError: (e) => {
          // The hook has read the card again; nothing of the move was kept.
          if (e instanceof ApiError && e.code === 'conflict') toast.error(tb.board.conflict(name))
          else toast.error(tb.board.moveFailed(name, e instanceof Error ? e.message : ''))
        },
        onSettled: () => mark(false),
      },
    )
  }

  if (group === '') return <p className="py-16 text-center text-sm text-muted-foreground">{tb.board.needsGroup}</p>

  const label = (c: BoardColumn) => (c.kind === 'other' ? tb.board.other : c.kind === 'empty' ? tb.board.empty : (c.value as string))
  return (
    <div data-slot="board-view" data-group={group} className="grid gap-2">
      {groups.isError && <p className="text-xs text-destructive">{tb.board.groupsFailed}</p>}
      <div className="flex items-start gap-3 overflow-x-auto pb-3" role="group" aria-label={tb.board.label}>
        {columns.map((c) => (
          <Column
            key={c.id}
            column={c}
            title={label(c)}
            // Asked for once the counts say there is something in it (or they could not be had).
            enabled={groups.isError || (c.count !== undefined && c.count > 0)}
            params={{ ...where, ...(sort ? { sort: sort as ListParams['sort'] } : {}), filter: [...filters, columnFilter(c, group, options)] }}
            group={group}
            shown={shown.map((f) => ({ key: f.key, label: f.title || f.key }))}
            write={write}
            busy={busy}
            canDrop={(from) => drag.current !== null && c.kind !== 'other' && from !== c.id}
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
          />
        ))}
      </div>
      {columns.some((c) => c.kind === 'other') && write && <p className="sr-only">{tb.board.dropOther}</p>}
    </div>
  )
}

function Column({
  column,
  title,
  enabled,
  params,
  group,
  shown,
  write,
  busy,
  canDrop,
  over,
  onOver,
  onDragStart,
  onDragEnd,
  onDrop,
  dragSource,
}: {
  column: BoardColumn
  title: string
  enabled: boolean
  params: Omit<ListParams, 'cursor' | 'limit' | 'props'>
  group: string
  shown: { key: string; label: string }[]
  write: boolean
  busy: ReadonlySet<string>
  canDrop: (from: string) => boolean
  over: boolean
  onOver: (on: boolean) => void
  onDragStart: (row: PageSummary) => void
  onDragEnd: () => void
  onDrop: () => void
  dragSource: () => { column: string } | null
}) {
  const query = useBoardColumn(params, enabled)
  const rows = useMemo(() => query.data?.pages.flatMap((p) => p.items) ?? [], [query.data])
  const { hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage } = query
  const droppable = write && column.kind !== 'other'

  return (
    <section
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
        {query.isPending && enabled ? (
          <Skeleton className="h-20 w-full" aria-busy="true" />
        ) : query.isError && rows.length === 0 ? (
          <ErrorState message={t.fmt.labelled(tb.board.loadFailed, query.error.message)} onRetry={() => void query.refetch()} />
        ) : rows.length === 0 ? (
          <p className="px-1 py-3 text-center text-xs text-muted-foreground">{tb.board.noCards}</p>
        ) : (
          rows.map((row) => <Card key={row.id} row={row} group={group} shown={shown} write={write} busy={busy.has(row.id)} columnId={column.id} onDragStart={onDragStart} onDragEnd={onDragEnd} />)
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

function Card({ row, group, shown, write, busy, columnId, onDragStart, onDragEnd }: { row: PageSummary; group: string; shown: { key: string; label: string }[]; write: boolean; busy: boolean; columnId: string; onDragStart: (row: PageSummary) => void; onDragEnd: () => void }) {
  const urls = useUrls()
  const line = cardTitleLine(row)
  const partial = row.partialProps?.includes(group) === true
  const movable = write && !!row.rev && cardMovable(row.props, row.partialProps, group)
  const values = shown.map((f) => ({ ...f, value: row.props?.[f.key] })).filter((f) => hasValue(f.value))
  return (
    <div
      role="group"
      data-card={row.id}
      data-column-of={columnId}
      data-movable={movable || undefined}
      data-partial={partial || undefined}
      aria-busy={busy || undefined}
      aria-label={row.title || row.label || tb.board.untitled}
      title={write && partial ? tb.board.partial : undefined}
      draggable={movable && !busy}
      onDragStart={(e) => {
        if (!movable || busy) return e.preventDefault()
        onDragStart(row)
        e.dataTransfer?.setData('application/x-petalbook-card', row.id)
        if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
      }}
      onDragEnd={onDragEnd}
      className={`rounded-lg border bg-background p-3 text-sm shadow-xs ${movable ? 'cursor-grab active:cursor-grabbing' : ''} ${busy ? 'opacity-60' : ''}`}
    >
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
