import { type ReactNode } from 'react'
import { Link } from 'react-router'
import { FileText, Folder as FolderIcon, Star } from 'lucide-react'
import { useCanFavorite, useSpacePermissions, useUrls } from '@/api/hooks'
import type { PageSummary, PageType } from '@/api/types'
import { DataTable, type Column } from '@/components/ui/data-table'
import { PageIcon } from '@/components/icons/PageIcon'
import { TagChip } from '@/components/TagChip'
import { useToggleFavorite } from '@/components/favorites/StarButton'
import { useTablePrefs } from '@/lib/tablePrefs'
import { fromHeaderSort, rowKey, toHeaderSort, type TableRow } from '@/lib/folderTable'
import { useFolderStyle, usePageStyle } from '@/lib/icons/useStyles'
import { absoluteTime, relativeTime } from '@/lib/time'
import { useDropTarget } from './useDropTarget'
import { MoreButton } from './FileContextMenu'
import { ENTRY_ATTR } from './FileContextMenu'
import { useFileOps } from './FileOps'
import { useDragSource, type EntryState } from './Entries'
import { folderItem, pageItem } from './Entries'
import { t } from '@/i18n'

// A chip row for a list of texts (the sources), quiet and one line.
function Chips({ values }: { values: readonly string[] }) {
  if (values.length === 0) return null
  return (
    <span className="flex min-w-0 flex-nowrap items-center gap-1 overflow-hidden">
      {values.map((v) => (
        <span
          key={v}
          className="inline-flex max-w-32 shrink-0 items-center truncate rounded-full border bg-secondary/60 px-2 py-0.5 text-xs leading-tight text-secondary-foreground"
        >
          {v}
        </span>
      ))}
    </span>
  )
}

const TagsCell = ({ tags }: { tags?: string[] }) => (
  <span className="flex min-w-0 flex-nowrap items-center gap-1 overflow-hidden">
    {tags?.map((tag) => (
      <TagChip key={tag} tag={tag} className="shrink-0" />
    ))}
  </span>
)

const dateCell = (iso?: string) =>
  iso ? (
    <time
      dateTime={iso}
      title={iso.length > 10 ? absoluteTime(iso) : iso}
      className="tabular-nums text-muted-foreground"
    >
      {iso.length > 10 ? relativeTime(iso) : iso}
    </time>
  ) : null

// The star at a row's right end: it fades in while the pointer (or the keyboard) is on the row, takes about 150 ms to do
// so, and on a touch screen, which has no hover, is always there but quiet. A favourite stays lit.
export function RowStar({ row }: { row: TableRow }) {
  const allowed = useCanFavorite()
  const ref =
    row.kind === 'page'
      ? ({ kind: 'page', target: row.page.id } as const)
      : ({ kind: 'folder', target: row.folder.path } as const)
  const { on, toggle, busy } = useToggleFavorite(allowed ? ref : null)
  if (!allowed) return null
  const name = row.kind === 'page' ? row.page.label : row.folder.name
  const label = on ? t.ftable.unstar(name) : t.ftable.star(name)
  return (
    <button
      type="button"
      data-slot="row-star"
      aria-label={label}
      title={label}
      aria-pressed={on}
      disabled={busy}
      onClick={toggle}
      className={`flex size-10 items-center justify-center rounded-md outline-none transition-opacity duration-150 hover:bg-muted focus-visible:opacity-100 focus-visible:ring-2 focus-visible:ring-ring/50 group-focus-within/row:opacity-100 group-hover/row:opacity-100 ${on ? 'opacity-100' : 'opacity-0 pointer-coarse:opacity-40'}`}
    >
      <Star className={`size-4 ${on ? 'fill-amber-400 text-amber-500' : 'text-muted-foreground'}`} />
    </button>
  )
}

// The title cell: the icon, the title in bold, and under it the first line of the text, muted. Dragging it moves the
// item; a folder takes what is dropped on it.
function TitleCell({ row, state, mobile = false }: { row: TableRow; state: EntryState; mobile?: boolean }) {
  const urls = useUrls()
  const ops = useFileOps()
  const folderStyle = useFolderStyle()
  const pageStyle = usePageStyle()
  const item = row.kind === 'page' ? pageItem(row.page) : folderItem(row.folder)
  const drag = useDragSource({ ...state, dragItems: () => (state.selected ? state.dragItems() : [item]) })
  const drop = useDropTarget(
    row.kind === 'folder' ? row.folder.path : '\u0000',
    ops?.index,
    (items, target) => void ops?.moveTo(items, target),
  )
  const style = row.kind === 'page' ? pageStyle(row.page) : folderStyle(row.folder.path)
  const name = row.kind === 'page' ? row.page.label || t.list.blankPage : row.folder.name
  const to = row.kind === 'page' ? urls.page(row.page.id) : urls.folder(row.folder.path)
  const sub = row.kind === 'page' ? (row.page.untitled ? '' : row.page.excerpt) : t.ftable.pages(row.folder.total)
  return (
    <Link
      to={to}
      {...drag.props}
      {...(row.kind === 'folder' ? drop.props : {})}
      className={`flex min-w-0 items-center gap-2.5 rounded-md py-1 outline-none focus-visible:ring-2 focus-visible:ring-ring/50 ${drop.over && row.kind === 'folder' ? 'bg-primary/15 ring-2 ring-primary/40' : ''} ${drag.dragging ? 'opacity-50' : ''}`}
    >
      <PageIcon
        icon={style.icon}
        color={style.color}
        fallback={
          row.kind === 'page' ? (
            <FileText className="size-[18px] text-muted-foreground" />
          ) : (
            <FolderIcon className="size-[18px] fill-current/15 text-muted-foreground" />
          )
        }
        className="size-[18px]"
      />
      <span className="min-w-0 flex-1">
        <span
          className={`block truncate text-[0.95rem] ${row.kind === 'page' && row.page.untitled ? 'text-muted-foreground' : 'font-semibold'}`}
        >
          {name}
        </span>
        {!mobile && sub && <span className="block truncate text-xs text-muted-foreground">{sub}</span>}
      </span>
    </Link>
  )
}

export interface FolderTableProps {
  folderPath: string
  rows: TableRow[]
  type?: PageType
  sort: string
  onSort: (sort: string) => void
  sortDisabled?: boolean
  stateOf: (row: TableRow) => EntryState
  selected: ReadonlySet<string>
  onSelect: (next: Set<string>, tick?: { id: string; shift: boolean }) => void
  loading: boolean
  error?: string
  onRetry?: () => void
  hasNext: boolean
  loadingMore: boolean
  onLoadMore: () => void
  empty: ReactNode
  // Columns the saved view hides, until the person chooses their own.
  defaultHidden?: string[]
}

// The folder as a table, like the list view of a file manager: a row for each subfolder and page with its title, a
// line of its text, the subtitle, tags, sources and dates, and the columns of its type's fields. What shows, in which
// order and how wide is the person's own, kept in this browser (see prefsStore); a click on a header sorts.
export function FolderTable(props: FolderTableProps) {
  const prefs = useTablePrefs(`files:${props.folderPath}`)
  const { write } = useSpacePermissions()
  const c = t.ftable.columns
  const fields = (props.type?.fields ?? []).filter((f) => f.kind !== 'other' && f.kind !== 'longtext')
  const columns: Column<TableRow>[] = [
    {
      id: 'title',
      header: c.title,
      required: true,
      min: 220,
      weight: 4,
      sortKey: 'title',
      cell: (r) => <TitleCell row={r} state={props.stateOf(r)} />,
    },
    {
      id: 'updated',
      header: c.updated,
      min: 110,
      weight: 1,
      priority: 2,
      sortKey: 'updated',
      cell: (r) => (r.kind === 'page' ? dateCell(r.page.updated) : null),
    },
    {
      id: 'tags',
      header: c.tags,
      min: 140,
      weight: 2,
      priority: 3,
      cell: (r) => (r.kind === 'page' ? <TagsCell tags={r.page.tags} /> : null),
    },
    {
      id: 'source',
      header: c.source,
      min: 130,
      weight: 2,
      priority: 4,
      sortKey: 'prop:source',
      cell: (r) => (r.kind === 'page' ? <Chips values={r.page.source} /> : null),
    },
    {
      id: 'created',
      header: c.created,
      min: 110,
      weight: 1,
      priority: 5,
      sortKey: 'created',
      cell: (r) => (r.kind === 'page' ? dateCell(r.page.created) : null),
    },
    {
      id: 'subtitle',
      header: c.subtitle,
      min: 140,
      weight: 2,
      priority: 6,
      cell: (r) => (r.kind === 'page' ? <span className="text-muted-foreground">{r.page.subtitle}</span> : null),
    },
    ...fields.map((f, i): Column<TableRow> => ({
      id: `prop:${f.key}`,
      header: f.title || f.key,
      min: 120,
      weight: 1,
      priority: 7 + i,
      sortKey: `prop:${f.key}`,
      cell: (r) => (r.kind === 'page' ? <PropValue page={r.page} k={f.key} /> : null),
    })),
  ]
  return (
    <DataTable
      prefs={prefs}
      columns={columns}
      rows={props.rows}
      rowId={rowKey}
      rowLabel={(r) => (r.kind === 'page' ? r.page.label : r.folder.name)}
      loading={props.loading}
      error={props.error}
      onRetry={props.onRetry}
      empty={props.empty}
      sort={toHeaderSort(props.sort)}
      onSort={(s) => !props.sortDisabled && props.onSort(fromHeaderSort(s))}
      // Selecting is for moving and deleting: without `write` there is nothing to do with it.
      selection={write ? { selected: props.selected, onChange: props.onSelect } : undefined}
      rowAttrs={(r) => ({ [ENTRY_ATTR]: rowKey(r) })}
      rowClass={() => 'group/entry'}
      actions={(r) => (
        <>
          <RowStar row={r} />
          <MoreButton label={r.kind === 'page' ? r.page.label : r.folder.name} className="size-10" />
        </>
      )}
      defaultHidden={props.defaultHidden}
      footer={
        <div className="flex flex-col items-center gap-2 py-3">
          {props.hasNext && (
            <button
              type="button"
              disabled={props.loadingMore}
              onClick={props.onLoadMore}
              className="h-10 rounded-lg border px-4 text-sm hover:bg-muted disabled:opacity-60"
            >
              {t.ftable.loadMore}
            </button>
          )}
        </div>
      }
    />
  )
}

// A property of the page as the table shows it: text, a list of chips, a yes or no.
function PropValue({ page, k }: { page: PageSummary; k: string }) {
  const v = page.props?.[k]
  if (v === undefined || v === null || v === '') return null
  if (Array.isArray(v)) return <Chips values={v.map(String)} />
  if (typeof v === 'boolean') return <span>{v ? '✓' : ''}</span>
  return <span>{String(v)}</span>
}

// On a phone there is no room for columns: a row is the title and under it a small line with the time, the tags and the
// sources. Sort and filter are behind one button above (see FilterBar).
export function PhoneRows({
  rows,
  stateOf,
  hasNext,
  loadingMore,
  onLoadMore,
}: {
  rows: TableRow[]
  stateOf: (row: TableRow) => EntryState
  hasNext: boolean
  loadingMore: boolean
  onLoadMore: () => void
}) {
  return (
    <>
      <ul className="divide-y divide-border/60" data-slot="phone-rows">
        {rows.map((r) => {
          const meta =
            r.kind === 'page' ? (
              <>
                <time dateTime={r.page.updated} title={absoluteTime(r.page.updated)}>
                  {relativeTime(r.page.updated)}
                </time>
                {r.page.tags?.map((tag) => (
                  <TagChip key={tag} tag={tag} />
                ))}
                {r.page.source.map((s) => (
                  // Cut, whole on hover: a long address must not widen the row past the screen.
                  <span
                    key={s}
                    data-slot="source-chip"
                    title={s}
                    className="inline-block max-w-full min-w-0 truncate rounded-full border px-2 py-0.5 text-xs"
                  >
                    {s}
                  </span>
                ))}
              </>
            ) : (
              <span>{t.ftable.pages(r.folder.total)}</span>
            )
          return (
            <li key={rowKey(r)} {...{ [ENTRY_ATTR]: rowKey(r) }} className="group/row flex items-center gap-1 py-1.5">
              <div className="min-w-0 flex-1">
                <TitleCell row={r} state={stateOf(r)} mobile />
                <div className="mt-0.5 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 pl-[28px] text-xs text-muted-foreground">
                  {meta}
                </div>
              </div>
              <RowStar row={r} />
              <MoreButton label={r.kind === 'page' ? r.page.label : r.folder.name} className="size-10" />
            </li>
          )
        })}
      </ul>
      {hasNext && (
        <div className="flex justify-center py-4">
          <button
            type="button"
            disabled={loadingMore}
            onClick={onLoadMore}
            className="h-10 rounded-lg border px-4 text-sm hover:bg-muted disabled:opacity-60"
          >
            {t.ftable.loadMore}
          </button>
        </div>
      )}
    </>
  )
}
