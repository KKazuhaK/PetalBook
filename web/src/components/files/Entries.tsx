// The entries of a listing: pages and folders, as rows and as cards. They all
// carry `data-file-key` (the context menu finds the entry by it), can be
// selected with a checkbox, dragged, and (folders) dropped onto.

import { useLayoutEffect, useRef, useState, type DragEvent, type MouseEvent, type ReactNode } from 'react'
import { Link } from 'react-router'
import { Check, FileText, Folder as FolderIcon } from 'lucide-react'
import { PageIcon } from '@/components/icons/PageIcon'
import { useFolderStyle, usePageStyle } from '@/lib/icons/useStyles'
import { useSpacePermissions } from '@/api/hooks'
import type { PageSummary } from '@/api/types'
import { TagChip } from '@/components/TagChip'
import { cardTitleLine } from '@/lib/listView'
import { itemKey, type FileItem, type FolderEntry } from '@/lib/folders'
import { absoluteTime, relativeTime } from '@/lib/time'
import type { SpaceUrls } from '@/lib/urls'
import { t } from '@/i18n'
import { endDrag, startDrag } from './dnd'
import { ENTRY_ATTR, MoreButton } from './FileContextMenu'
import { useFileOps } from './FileOps'
import { useDropTarget } from './useDropTarget'

export interface Picked {
  shift: boolean
}

// What an entry needs to know about the selection around it.
export interface EntryState {
  selected: boolean
  // Whether anything at all is selected: checkboxes then stay visible.
  selecting: boolean
  pick: (mods: Picked) => void
  // What dragging this entry takes along: the selection if it is in it.
  dragItems: () => FileItem[]
}

// Shift, ⌘ and Ctrl change what a click on an entry means; a plain click opens it.
function useEntryClick(state: EntryState) {
  const { write } = useSpacePermissions()
  return (e: MouseEvent) => {
    // Selecting is for moving and deleting: without `write` the click just opens.
    if (!write) return
    if (!(e.shiftKey || e.metaKey || e.ctrlKey)) return
    e.preventDefault()
    state.pick({ shift: e.shiftKey && !e.metaKey && !e.ctrlKey })
  }
}

export function useDragSource(state: EntryState) {
  const [dragging, setDragging] = useState(false)
  const { write } = useSpacePermissions()
  return {
    dragging,
    props: {
      draggable: write,
      onDragStart: (e: DragEvent) => {
        startDrag(e, state.dragItems())
        setDragging(true)
      },
      onDragEnd: () => {
        endDrag()
        setDragging(false)
      },
    },
  }
}

function Checkbox({
  checked,
  label,
  onPick,
  className,
}: {
  checked: boolean
  label: string
  onPick: (m: Picked) => void
  className: string
}) {
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={checked}
      aria-label={label}
      onClick={(e) => {
        e.preventDefault()
        e.stopPropagation()
        onPick({ shift: e.shiftKey })
      }}
      className={`flex size-5 items-center justify-center rounded border outline-none focus-visible:ring-2 focus-visible:ring-ring/50 ${checked ? 'border-primary bg-primary text-primary-foreground' : 'border-input bg-background'} ${className}`}
    >
      {checked && <Check className="size-3.5" strokeWidth={3} />}
    </button>
  )
}

// A row's first column: the kind icon, swapped for the checkbox while the row
// is hovered or focused, or whenever something is selected.
function Leading({ icon, state, label }: { icon: ReactNode; state: EntryState; label: string }) {
  const { write } = useSpacePermissions()
  const show = state.selected || state.selecting
  if (!write)
    return (
      <div className="absolute top-2.5 left-3 size-6">
        <span className="flex size-6 items-center justify-center text-muted-foreground">{icon}</span>
      </div>
    )
  return (
    <div className="absolute top-2.5 left-3 size-6">
      <span
        className={`flex size-6 items-center justify-center text-muted-foreground group-focus-within/entry:invisible group-hover/entry:invisible ${show ? 'invisible' : ''}`}
      >
        {icon}
      </span>
      <Checkbox
        checked={state.selected}
        label={t.manage.selectItem(label)}
        onPick={state.pick}
        className={`absolute inset-0 m-auto opacity-0 group-focus-within/entry:opacity-100 group-hover/entry:opacity-100 focus-visible:opacity-100 ${show ? 'opacity-100' : ''}`}
      />
    </div>
  )
}

const rowClass = (selected: boolean, over: boolean, dragging: boolean) =>
  `group/entry relative rounded-lg transition-colors ${over ? 'bg-primary/15 ring-2 ring-primary/40' : selected ? 'bg-primary/10' : 'hover:bg-muted/70'} ${dragging ? 'opacity-50' : ''}`

const linkClass =
  'flex items-start gap-3 rounded-lg py-2.5 pr-10 pl-11 outline-none focus-visible:ring-2 focus-visible:ring-ring/50'

export function PageRow({
  page,
  showFolder,
  index,
  urls,
  state,
}: {
  page: PageSummary
  showFolder: boolean
  index: number
  urls: SpaceUrls
  state: EntryState
}) {
  const onClick = useEntryClick(state)
  const drag = useDragSource(state)
  const name = page.label || t.list.blankPage
  const style = usePageStyle()(page)
  return (
    <li {...{ [ENTRY_ATTR]: `p:${page.id}` }} className={rowClass(state.selected, false, drag.dragging)}>
      <Leading
        icon={
          <PageIcon
            icon={style.icon}
            color={style.color}
            fallback={<FileText className="size-[18px]" />}
            className="size-[18px]"
          />
        }
        state={state}
        label={name}
      />
      <Link to={urls.page(page.id)} data-index={index} onClick={onClick} className={linkClass} {...drag.props}>
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-2">
            {/* An untitled page shows its first line, in a lighter colour. */}
            <span className={`truncate text-[0.95rem] ${page.untitled ? 'text-muted-foreground' : 'font-medium'}`}>
              {name}
            </span>
            {page.subtitle && (
              <span className="hidden truncate text-sm text-muted-foreground sm:inline">{page.subtitle}</span>
            )}
          </div>
          {page.subtitle && <div className="truncate text-sm text-muted-foreground sm:hidden">{page.subtitle}</div>}
          {(page.tags?.length || showFolder) && (
            <div className="mt-1 flex flex-wrap items-center gap-1.5">
              {showFolder && page.folder && (
                <span className="inline-flex items-center gap-1 text-xs text-muted-foreground">
                  <FolderIcon className="size-3" />
                  {page.folder}
                </span>
              )}
              {page.tags?.map((tag) => (
                <TagChip key={tag} tag={tag} />
              ))}
            </div>
          )}
        </div>
        <time
          dateTime={page.updated}
          title={absoluteTime(page.updated)}
          className="shrink-0 pt-0.5 text-xs whitespace-nowrap text-muted-foreground tabular-nums"
        >
          {relativeTime(page.updated)}
        </time>
      </Link>
      <MoreButton label={name} className="absolute top-2 right-2" />
    </li>
  )
}

export function FolderRow({ folder, urls, state }: { folder: FolderEntry; urls: SpaceUrls; state: EntryState }) {
  const ops = useFileOps()
  const onClick = useEntryClick(state)
  const drag = useDragSource(state)
  const drop = useDropTarget(folder.path, ops?.index, (items, target) => void ops?.moveTo(items, target))
  const style = useFolderStyle()(folder.path)
  return (
    <li
      {...{ [ENTRY_ATTR]: `f:${folder.path}` }}
      className={rowClass(state.selected, drop.over, drag.dragging)}
      {...drop.props}
    >
      <Leading
        icon={
          <PageIcon
            icon={style.icon}
            color={style.color}
            fallback={<FolderIcon className="size-[18px] fill-current/15" />}
            className="size-[18px]"
          />
        }
        state={state}
        label={folder.name}
      />
      <Link
        to={urls.folder(folder.path)}
        onClick={onClick}
        className={`${linkClass} items-center py-3`}
        {...drag.props}
      >
        <span className="min-w-0 flex-1 truncate text-[0.95rem] font-medium">{folder.name}</span>
        <span className="shrink-0 text-xs whitespace-nowrap text-muted-foreground tabular-nums">
          {t.manage.folderPages(folder.total)}
        </span>
      </Link>
      <MoreButton label={folder.name} className="absolute top-2.5 right-2" />
    </li>
  )
}

// The text of a card is cut at this many lines; the cut is shown as a fade.
const CARD_LINES = 12

const cardClass = (selected: boolean, dragging: boolean) =>
  `group/entry relative rounded-xl border bg-card text-card-foreground transition-colors ${selected ? 'border-primary ring-2 ring-primary/50' : 'hover:bg-muted/50'} ${dragging ? 'opacity-50' : ''}`

// A card's checkbox sits in its corner and shows on hover, on focus, or while selecting.
function CardCheck({ state, label }: { state: EntryState; label: string }) {
  const { write } = useSpacePermissions()
  const show = state.selected || state.selecting
  if (!write) return null
  return (
    <Checkbox
      checked={state.selected}
      label={t.manage.selectItem(label)}
      onPick={state.pick}
      className={`absolute top-2 left-2 z-10 opacity-0 shadow-sm group-focus-within/entry:opacity-100 group-hover/entry:opacity-100 focus-visible:opacity-100 ${show ? 'opacity-100' : ''}`}
    />
  )
}

// For short copy: the text itself, a muted line of where it is from, its tags
// and when it was changed. The whole card opens the page.
export function PageCard({ page, urls, state }: { page: PageSummary; urls: SpaceUrls; state: EntryState }) {
  const titleLine = cardTitleLine(page)
  const text = useRef<HTMLDivElement>(null)
  const [clipped, setClipped] = useState(false)
  const onClick = useEntryClick(state)
  const drag = useDragSource(state)

  // Whether the text runs past the cut depends on the width of the column.
  useLayoutEffect(() => {
    const el = text.current
    if (!el) return
    const measure = () => setClipped(el.scrollHeight > el.clientHeight + 1)
    measure()
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [page.excerpt])

  const empty = !page.excerpt && !titleLine
  return (
    <li
      {...{ [ENTRY_ATTR]: `p:${page.id}` }}
      className={`mb-3 break-inside-avoid ${cardClass(state.selected, drag.dragging)}`}
    >
      <CardCheck state={state} label={page.label || t.list.blankPage} />
      <Link
        to={urls.page(page.id)}
        onClick={onClick}
        className="block rounded-xl p-3.5 outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        {...drag.props}
      >
        {titleLine && (
          <div className="mb-1.5 pr-7 text-[0.95rem] leading-snug font-medium break-words">{titleLine}</div>
        )}
        {page.excerpt ? (
          <div
            ref={text}
            className="overflow-hidden text-[0.9rem] leading-relaxed break-words whitespace-pre-line"
            style={{
              maxHeight: `${CARD_LINES * 1.625}em`,
              ...(clipped
                ? {
                    maskImage: 'linear-gradient(to bottom, #000 75%, transparent)',
                    WebkitMaskImage: 'linear-gradient(to bottom, #000 75%, transparent)',
                  }
                : {}),
            }}
          >
            {page.excerpt}
          </div>
        ) : (
          empty && <div className="text-sm text-muted-foreground">{t.list.untitledHint}</div>
        )}
        <div className="mt-2.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground">
          {page.source.length > 0 && <span className="break-all">——{t.fmt.list(page.source)}</span>}
          {page.tags?.map((tag) => (
            <TagChip key={tag} tag={tag} />
          ))}
          <time dateTime={page.updated} title={absoluteTime(page.updated)} className="ml-auto tabular-nums">
            {relativeTime(page.updated)}
          </time>
        </div>
      </Link>
      <MoreButton label={page.label || t.list.blankPage} className="absolute top-1.5 right-1.5 bg-card/80" />
    </li>
  )
}

export function FolderCard({ folder, urls, state }: { folder: FolderEntry; urls: SpaceUrls; state: EntryState }) {
  const ops = useFileOps()
  const onClick = useEntryClick(state)
  const drag = useDragSource(state)
  const drop = useDropTarget(folder.path, ops?.index, (items, target) => void ops?.moveTo(items, target))
  const style = useFolderStyle()(folder.path)
  return (
    <li
      {...{ [ENTRY_ATTR]: `f:${folder.path}` }}
      className={`${cardClass(state.selected, drag.dragging)} ${drop.over ? 'border-primary bg-primary/15 ring-2 ring-primary/40' : ''}`}
      {...drop.props}
    >
      <CardCheck state={state} label={folder.name} />
      <Link
        to={urls.folder(folder.path)}
        onClick={onClick}
        className="flex items-center gap-2.5 rounded-xl px-3 py-2.5 pr-9 outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        {...drag.props}
      >
        <PageIcon
          icon={style.icon}
          color={style.color}
          fallback={<FolderIcon className="size-5 shrink-0 fill-current/15 text-muted-foreground" />}
          className="size-5"
        />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-[0.95rem] font-medium">{folder.name}</span>
          <span className="block truncate text-xs text-muted-foreground">{t.manage.folderPages(folder.total)}</span>
        </span>
      </Link>
      <MoreButton label={folder.name} className="absolute top-2.5 right-1.5 bg-card/80" />
    </li>
  )
}

// Entries as items, for the selection and the menu.
export const pageItem = (p: PageSummary): FileItem => ({ kind: 'page', id: p.id, folder: p.folder, label: p.label })
export const folderItem = (f: FolderEntry): FileItem => ({ kind: 'folder', path: f.path })
export const entryKey = itemKey
