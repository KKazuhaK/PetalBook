import { lazy, Suspense, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import {
  ArrowUpDown,
  BookOpen,
  Columns3,
  FilePlus2,
  FolderPlus,
  LayoutGrid,
  List as ListIcon,
  PenLine,
  Table2,
  Tag as TagIcon,
} from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { toast } from 'sonner'
import { tzOffset } from '@/lib/templates'
import {
  useActiveSpace,
  useCreatePage,
  usePageList,
  useSpacePermissions,
  useTypes,
  useUrls,
  useViews,
  useViewsBusy,
} from '@/api/hooks'
import type { ListParams, PropFilter, SortKey, ViewScope } from '@/api/types'
import { Button } from '@/components/ui/button'
import { NativeSelect } from '@/components/ui/native-select'
import { Skeleton } from '@/components/ui/skeleton'
import { ErrorState, Spinner } from '@/components/common'
import { QuickCaptureFab, useQuickCapture } from '@/components/layout/QuickCapture'
import { topBarButton, topBarGroup, topBarGroupItem, topBarRoom } from '@/components/layout/topBarStyles'
import { TopBar } from '@/components/layout/TopBar'
import { ViewBar } from '@/components/views/ViewBar'
import { useWorkingCopy } from '@/components/views/useWorkingCopy'
import { Breadcrumbs } from '@/components/files/Breadcrumbs'
import {
  FolderCard,
  FolderRow,
  PageCard,
  PageRow,
  folderItem,
  pageItem,
  type EntryState,
} from '@/components/files/Entries'
import { FileContextMenu } from '@/components/files/FileContextMenu'
import { useFileOps } from '@/components/files/FileOps'
import { SelectionBar } from '@/components/files/SelectionBar'
import { childFolders, itemKey, type FileItem } from '@/lib/folders'
import { loadView, saveView, viewKey, type ListViewMode } from '@/lib/listView'
import {
  arrangeRows,
  filterCount,
  filterParams,
  noFilters,
  rowKey,
  type TableFilters,
  type TableRow,
} from '@/lib/folderTable'
import { prefsStore } from '@/lib/prefsStore'
import { useIsMobile } from '@/hooks/use-mobile'
import { FilterBar } from '@/components/files/FilterBar'
import { FolderTable, PhoneRows } from '@/components/files/FolderTable'
import { folderTypeName } from '@/lib/pageTypes'
import {
  DEFAULT_STATE,
  groupFieldsOf,
  sameState,
  SORT_WORDS,
  stateOfView,
  viewAtMark,
  viewIdKey,
  type ViewState,
} from '@/lib/savedViews'
import { applyClick, emptySelection, prune, type Selection } from '@/lib/selection'
import { t } from '@/i18n'
import { useDocumentTitle } from '@/lib/useDocumentTitle'

// The table view, with the virtual list it needs, is a chunk of its own: it loads when a table is first shown.
const TableView = lazy(() => import('@/components/table/TableView'))
// So are the board, continuous reading and the tools of a view: each is fetched when first shown.
const BoardView = lazy(() => import('@/components/board/BoardView'))
const ReadingView = lazy(() => import('@/components/reading/ReadingView'))
const ViewTools = lazy(() => import('@/components/views/ViewTools'))

export type ListSource =
  | { kind: 'recent' }
  | { kind: 'folder'; folder: string }
  | { kind: 'tag'; tag: string }
  // `filters` are what the search dialog added: title only, folder, tags, date ranges.
  | { kind: 'search'; q: string; filters?: Omit<ListParams, 'cursor' | 'limit' | 'q' | 'sort'> }

const SORTS: SortKey[] = ['-updated', 'updated', '-created', 'created', 'title', '-title']

const sourceKey = (s: ListSource) => JSON.stringify(s)

function titleOf(s: ListSource, spaceName: string): string {
  switch (s.kind) {
    case 'recent':
      return t.list.recentTitle
    case 'folder':
      return s.folder === '' ? spaceName : s.folder
    case 'tag':
      return t.list.tagTitle(s.tag)
    case 'search':
      return s.q ? t.list.searchTitle(s.q) : t.list.filteredTitle
  }
}

function emptyText(s: ListSource, write: boolean): { main: string; hint?: string } {
  switch (s.kind) {
    case 'recent':
      // "Make the first one" is not for someone who may only read.
      return { main: write ? t.list.emptyRecent : t.list.emptyRecentReadOnly }
    case 'folder':
      return { main: t.list.emptyFolder }
    case 'tag':
      return { main: t.list.emptyTag }
    case 'search':
      return s.q ? { main: t.list.emptySearch(s.q), hint: t.list.emptySearchHint } : { main: t.list.emptyFiltered }
  }
}

// The list or cards choice of one listing, remembered in the browser. The
// listing component stays mounted when the folder or tag changes, so the choice
// is read again whenever the key does.
function useViewMode(space: string, source: ListSource): [ListViewMode, (m: ListViewMode) => void] {
  const name = source.kind === 'folder' ? source.folder : source.kind === 'tag' ? source.tag : ''
  const key = viewKey(space, source.kind, name)
  const [stored, setStored] = useState(() => ({ key, mode: loadView(key) }))
  let mode = stored.mode
  if (stored.key !== key) {
    mode = loadView(key)
    setStored({ key, mode })
  }
  return [
    mode,
    (m) => {
      saveView(key, m)
      setStored({ key, mode: m })
    },
  ]
}

function ViewSwitch({
  mode,
  onChange,
  table,
  board,
  read,
  disabled,
  sort,
}: {
  mode: ListViewMode
  onChange: (m: ListViewMode) => void
  table: boolean
  board: boolean
  read: boolean
  disabled?: boolean
  // The sort choices, for the menu of a bar too narrow for the sort button (which is then hidden).
  sort?: { value: string; onChange: (v: string) => void; choices: (readonly [string, string])[] }
}) {
  const trigger = useRef<HTMLButtonElement>(null)
  const [room, setRoom] = useState(Infinity)
  const item = (m: ListViewMode, label: string, Icon: typeof ListIcon) => (
    <button
      key={m}
      type="button"
      aria-pressed={mode === m}
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={() => onChange(m)}
      className={topBarGroupItem(mode === m)}
    >
      <Icon className="size-4" />
    </button>
  )
  const modes = (
    [
      ['list', t.view.list, ListIcon, true],
      ['cards', t.view.cards, LayoutGrid, true],
      ['table', t.view.table, Table2, table],
      ['board', t.view.board, Columns3, board],
      ['read', t.view.read, BookOpen, read],
    ] as const
  ).filter(([, , , on]) => on)
  const Current = modes.find(([m]) => m === mode)?.[2] ?? ListIcon
  return (
    <>
      {/* Where the top bar is wide enough, one button per view; where it is narrow (a phone, or a narrow window beside
          the sidebar), one button that opens the same choices, so the actions never run over the title. */}
      <div
        role="group"
        aria-label={t.view.label}
        aria-busy={disabled || undefined}
        className={`hidden ${topBarGroup} @[30rem]/topbar:flex`}
      >
        {modes.map(([m, label, Icon]) => item(m, label, Icon))}
      </div>
      <DropdownMenu onOpenChange={(open) => open && setRoom(topBarRoom(trigger.current))}>
        <DropdownMenuTrigger
          ref={trigger}
          disabled={disabled}
          aria-label={t.view.label}
          title={t.view.label}
          className={`${topBarButton} @[30rem]/topbar:hidden`}
        >
          <Current className="size-4" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="min-w-40">
          <DropdownMenuRadioGroup value={mode} onValueChange={(v) => onChange(v as ListViewMode)}>
            {modes.map(([m, label, Icon]) => (
              <DropdownMenuRadioItem key={m} value={m}>
                <Icon />
                {label}
              </DropdownMenuRadioItem>
            ))}
          </DropdownMenuRadioGroup>
          {sort && room < 26 && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuGroup>
                <DropdownMenuLabel>{t.list.sort}</DropdownMenuLabel>
                <DropdownMenuRadioGroup
                  value={sort.value || 'default'}
                  onValueChange={(v) => sort.onChange(v === 'default' ? '' : v)}
                >
                  {sort.choices.map(([v, label]) => (
                    <DropdownMenuRadioItem key={v} value={v || 'default'}>
                      {label}
                    </DropdownMenuRadioItem>
                  ))}
                </DropdownMenuRadioGroup>
              </DropdownMenuGroup>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
    </>
  )
}

const normSort = (s: string) => s || '-updated'

// What is in a folder is shown like a file manager: its subfolders first, then
// its pages. The other listings (recent, a tag, a search) hold pages only.
// Remounted when the listing changes, so a selection never carries over.
export function PageListView({ source }: { source: ListSource }) {
  return <Listing key={sourceKey(source)} source={source} />
}

function Listing({ source }: { source: ListSource }) {
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
  const create = useCreatePage()
  const urls = useUrls()
  const ops = useFileOps()
  const { write } = useSpacePermissions()
  const [creating, setCreating] = useState(false)
  const { id: spaceId, spaces } = useActiveSpace()
  // What a folder that has no saved view remembers in the browser (the 2.1 choice): the layout of the implicit view.
  const [chosen, setImplicit] = useViewMode(spaceId ?? '', source)
  // The types are only asked for in a folder: the table and the board are views of a folder that has one.
  const types = useTypes(source.kind === 'folder')
  const typeName = source.kind === 'folder' && types.data ? folderTypeName(types.data.bindings, source.folder) : ''
  const type = typeName ? types.data?.types.find((x) => x.name === typeName) : undefined

  // --- the view being shown ---
  // A folder or a tag has saved views; the one last used here is shown, else the first. Without any, the implicit
  // view is shown: the layout remembered above and what is adjusted on the page.
  const scope: ViewScope | null =
    source.kind === 'folder' ? { folder: source.folder } : source.kind === 'tag' ? { tag: source.tag } : null
  const views = useViews(scope)
  const writingViews = useViewsBusy(scope)
  const viewsPending = scope !== null && views.isPending
  const idKey = viewIdKey(
    spaceId ?? '',
    source.kind,
    source.kind === 'folder' ? source.folder : source.kind === 'tag' ? source.tag : '',
  )
  const sortParam = params.get('sort')
  // In a folder or a tag "sort=" is a change too: the listing's default order on a view that sorts otherwise (a sort set
  // back to what the view said before a save of the person).
  const urlSort =
    sortParam !== null &&
    (SORTS.includes(sortParam as SortKey) || (scope !== null && (sortParam === '' || SORT_WORDS.test(sortParam))))
      ? sortParam
      : null
  const implicit: ViewState = { ...DEFAULT_STATE, layout: chosen }
  // The working copy of the view shown, and the tabs as they go with it (see useWorkingCopy and lib/workingCopy).
  const working = useWorkingCopy(
    scope !== null,
    idKey,
    views.data,
    { view: params.get('view'), sort: urlSort },
    writingViews,
    (list, wc) => {
      const v = list.find((x) => x.id === wc.viewKey)
      return v ? stateOfView(v) : implicit
    },
  )
  const saved = useMemo(() => working.list?.views ?? [], [working.list])
  const wc = working.wc
  // While the person's own write of the views is on its way, the tabs, the tools of the view and the sort are off.
  const busy = scope !== null && working.busy
  // The view of the working copy; when the views read cannot place it (they did not come after a write), another view
  // may be shown, without the working copy (it is never put on another view).
  const own = scope ? (saved.find((v) => v.id === wc.viewKey) ?? null) : null
  const active = scope ? (own ?? viewAtMark(saved, wc.viewKey) ?? saved[0] ?? null) : null
  const base: ViewState = active ? stateOfView(active) : implicit
  const mine = own !== null || (active === null && wc.viewKey === '')
  const patch = scope && mine ? wc.patch : {}
  const state: ViewState = scope ? { ...base, ...patch } : { ...base, sort: urlSort ?? base.sort }
  const modified = active !== null && !sameState(state, base)

  const setPatch = (p: Partial<ViewState>) => {
    if (mine) working.edit(p, base)
  }
  const setSort = (to: string) => {
    if (scope) return setPatch({ sort: to })
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        // What the listing already does needs no word in the address.
        if (to === base.sort || (source.kind !== 'search' && normSort(to) === normSort(base.sort))) next.delete('sort')
        else next.set('sort', to)
        return next
      },
      { replace: true },
    )
  }
  // The address shows the sort of the working copy, with the view it belongs to, so a reload or "back" finds it again
  // and never puts it on another view.
  const scoped = scope !== null
  const wantSort = wc.patch.sort ?? null
  const wantView = wantSort !== null && wc.viewKey !== '' ? wc.viewKey : null
  useEffect(() => {
    if (!scoped || (params.get('sort') === wantSort && params.get('view') === wantView)) return
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        for (const [k, v] of [
          ['sort', wantSort],
          ['view', wantView],
        ] as const) {
          if (v === null) next.delete(k)
          else next.set(k, v)
        }
        return next
      },
      { replace: true },
    )
  }, [scoped, params, setParams, wantSort, wantView])

  // What the layouts need: the table and the board a folder with a type (the board a single-choice field of it), a tag's
  // board a field a view named, reading any folder or tag. While the types are on their way the choice is kept.
  const groupFields = groupFieldsOf(type, source.kind === 'folder' ? t.ftable.columns.source : '')
  const group = state.group || groupFields[0]?.key || ''
  const typesPending = source.kind === 'folder' && types.isPending
  const tableOk = typeName !== ''
  const boardOk = source.kind === 'folder' ? group !== '' : source.kind === 'tag' && state.group !== ''
  const readOk = scope !== null
  const mode: ListViewMode =
    state.layout === 'table' && !tableOk && !typesPending
      ? 'list'
      : state.layout === 'board' && !boardOk && !typesPending
        ? 'list'
        : state.layout === 'read' && !readOk
          ? 'list'
          : state.layout
  const setMode = (m: ListViewMode) => {
    if (active) setPatch({ layout: m })
    else setImplicit(m)
  }
  const isTable = mode === 'table'
  const listLike = mode === 'list' || mode === 'cards'
  // Which parts of the sort are sent: the listing's own default needs no word.
  const sortQ = source.kind === 'search' ? state.sort : normSort(state.sort) === '-updated' ? '' : state.sort
  const filters: PropFilter[] = state.filter
  const capture = useQuickCapture()
  const isInbox = source.kind === 'folder' && source.folder === spaces.find((s) => s.id === spaceId)?.inbox
  const [rawSelection, setSelection] = useState<Selection>(emptySelection)

  const isSearch = source.kind === 'search'
  const mobile = useIsMobile()
  // What the folder's listing is filtered by besides the saved view's filters: chips above the table.
  const [tableFilters, setTableFilters] = useState<TableFilters>(noFilters)
  const [mixed, setMixedState] = useState(() => prefsStore().read('pb.files.mixed') === true)
  const setMixed = (v: boolean) => {
    prefsStore().write('pb.files.mixed', v)
    setMixedState(v)
  }
  const isFolder = source.kind === 'folder'
  const filesTable = isFolder && mode === 'list' && !mobile
  const filesPhone = isFolder && mode === 'list' && mobile
  // The sort select and, in a narrow bar, the same choices in the view menu. Search defaults to relevance; everything
  // else to newest first; a view can sort by a property, which the plain choices do not name.
  const sortValue = isSearch ? state.sort : normSort(state.sort) === '-updated' ? '' : state.sort
  const onSort = (v: string) => setSort(isSearch ? v : v || '-updated')
  const sortChoices: (readonly [string, string])[] = [
    ['', isSearch ? t.list.sorts.relevance : t.list.sorts['-updated']],
    ...SORTS.filter((k) => isSearch || k !== '-updated').map((k) => [k, t.list.sorts[k]] as const),
    ...(state.sort !== '' && !SORTS.includes(state.sort as SortKey)
      ? [[state.sort, t.views.customSort(state.sort.replace(/^-?(prop:)?/, ''))] as const]
      : []),
  ]
  const extra = isFolder ? filterParams(tableFilters) : {}
  const filtered = filterCount(tableFilters) > 0

  const query = usePageList(
    {
      ...(source.kind === 'folder' ? { folder: source.folder } : {}),
      // "最近" is every page, newest first, wherever it lives.
      ...(source.kind === 'recent' ? { folder: '', recursive: true } : {}),
      ...(source.kind === 'tag' ? { tag: [source.tag] } : {}),
      ...(source.kind === 'search' ? { ...source.filters, q: source.q } : {}),
      ...(sortQ ? { sort: sortQ as ListParams['sort'] } : {}),
      ...(filters.length || extra.filter ? { filter: [...filters, ...(extra.filter ?? [])] } : {}),
      ...(extra.tag ? { tag: extra.tag } : {}),
      ...(extra.createdFrom ? { createdFrom: extra.createdFrom } : {}),
      ...(extra.createdTo ? { createdTo: extra.createdTo } : {}),
      ...(extra.updatedFrom ? { updatedFrom: extra.updatedFrom } : {}),
      ...(extra.updatedTo ? { updatedTo: extra.updatedTo } : {}),
      ...(filesTable && type ? { props: true } : {}),
    },
    listLike && !viewsPending,
    isFolder,
  )
  // Read before the status checks below narrow `query` (a failed "load more" leaves the list in place).
  const nextError = query.isFetchNextPageError ? query.error : null
  const fetchNext = query.fetchNextPage
  const items = query.data?.pages.flatMap((p) => p.items) ?? []
  const spaceName = ops?.spaceName ?? t.nav.rootFolder
  const title = titleOf(source, spaceName)
  useDocumentTitle(`${title} · ${t.appName}`)

  // Subfolders, from the tree: it lists every folder with the pages directly in it.
  // They are not listed while the pages are filtered: a folder has none of the things a filter asks about.
  const folders = source.kind === 'folder' && ops && !filtered ? childFolders(ops.index, source.folder) : []
  const tableRows: TableRow[] =
    filesTable || filesPhone ? arrangeRows(folders, items, mixed, state.sort || '-updated') : []

  // Everything selectable, in the order shown (for shift-click ranges).
  const lookup = new Map<string, FileItem>()
  for (const f of folders) lookup.set(itemKey(folderItem(f)), folderItem(f))
  for (const p of items) lookup.set(itemKey(pageItem(p)), pageItem(p))
  // Ranges (shift-click) follow what is on the screen.
  const order = filesTable || filesPhone ? tableRows.map(rowKey) : [...lookup.keys()]
  const selection = prune(rawSelection, order)
  const selected = [...selection.keys].map((k) => lookup.get(k)).filter((i): i is FileItem => !!i)
  const clear = () => setSelection(emptySelection())

  const stateOfRow = (r: TableRow): EntryState => stateOf(r.kind === 'page' ? pageItem(r.page) : folderItem(r.folder))
  const stateOf = (item: FileItem): EntryState => {
    const key = itemKey(item)
    return {
      selected: selection.keys.has(key),
      selecting: selection.keys.size > 0,
      pick: (mods) => setSelection(applyClick(order, selection, key, mods)),
      // Dragging something that is selected drags the whole selection.
      dragItems: () => (selection.keys.has(key) ? selected : [item]),
    }
  }

  // Delete or Backspace asks to delete the selection; Escape lets go of it.
  // Not while typing, or while a dialog or menu has the keyboard.
  useEffect(() => {
    if (selection.keys.size === 0 || !ops || !write) return
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented) return
      if (
        (e.target as Element | null)?.closest?.(
          'input, textarea, select, [contenteditable="true"], [role="dialog"], [role="alertdialog"], [role="menu"]',
        )
      )
        return
      if (e.key === 'Escape') clear()
      else if (e.key === 'Delete' || e.key === 'Backspace') {
        e.preventDefault()
        ops.confirmDelete(selected, clear)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
    // `selected` and `clear` are rebuilt on every render; what matters is the selection itself.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selection, ops, write])

  const newPage = async () => {
    setCreating(true)
    const folder = source.kind === 'folder' ? source.folder : ''
    const meta = source.kind === 'tag' ? { tags: [source.tag] } : undefined
    // With the file operations the person may be asked for a template first; they open the page (and tell a failure).
    if (ops) {
      try {
        await ops.newPage(folder, meta)
      } finally {
        setCreating(false)
      }
      return
    }
    try {
      const page = await create.mutateAsync({ folder, ...(meta ? { meta } : {}), body: '', tz: tzOffset() })
      navigate(urls.page(page.id))
    } catch (e) {
      toast.error(t.fmt.labelled(t.list.createFailed, e instanceof Error ? e.message : ''))
      setCreating(false)
    }
  }

  const Icon = source.kind === 'tag' ? TagIcon : null
  const empty = emptyText(source, write)
  const treeReady = source.kind !== 'folder' || !ops || ops.ready
  const nothing =
    listLike && !query.isPending && !query.isError && items.length === 0 && folders.length === 0 && treeReady

  return (
    <>
      <TopBar
        actions={
          selection.keys.size > 0 && ops && write ? (
            <SelectionBar
              count={selection.keys.size}
              onMove={() => ops.chooseMove(selected, clear)}
              onDelete={() => ops.confirmDelete(selected, clear)}
              onClear={clear}
            />
          ) : (
            <>
              {/* On a phone the sort choice shrinks to an icon (the select is still what opens), leaving the room to the breadcrumbs. */}
              {/* In a table the headers of the columns sort. */}
              {/* As wide as its longest choice (a select sizes itself so when its width is auto), so no choice is cut. */}
              {!isTable && !filesPhone && (
                <div className="relative max-sm:size-9 max-sm:pointer-coarse:size-10 max-sm:shrink-0 max-sm:rounded-lg max-sm:border max-sm:[&>div>svg]:hidden @max-[26rem]/topbar:hidden">
                  <ArrowUpDown className="pointer-events-none absolute top-1/2 left-1/2 size-4 -translate-x-1/2 -translate-y-1/2 text-muted-foreground sm:hidden" />
                  <NativeSelect
                    wrapperClassName="w-auto max-sm:size-full"
                    className="h-9 pointer-coarse:h-10 w-auto text-[0.8rem] pointer-coarse:text-base! max-sm:size-full max-sm:opacity-0"
                    aria-label={t.list.sort}
                    disabled={busy}
                    value={sortValue}
                    onChange={(e) => onSort(e.target.value)}
                  >
                    {/* Search defaults to relevance; everything else to newest first. */}
                    <option value="">{isSearch ? t.list.sorts.relevance : t.list.sorts['-updated']}</option>
                    {SORTS.filter((s) => isSearch || s !== '-updated').map((s) => (
                      <option key={s} value={s}>
                        {t.list.sorts[s]}
                      </option>
                    ))}
                    {/* A view can sort by a property, which the plain choices do not name. */}
                    {state.sort !== '' && !SORTS.includes(state.sort as SortKey) && (
                      <option value={state.sort}>{t.views.customSort(state.sort.replace(/^-?(prop:)?/, ''))}</option>
                    )}
                  </NativeSelect>
                </div>
              )}
              <ViewSwitch
                mode={mode}
                onChange={setMode}
                table={tableOk}
                board={boardOk || typesPending}
                read={readOk}
                disabled={busy}
                sort={
                  !isTable && !filesPhone ? { value: sortValue, onChange: onSort, choices: sortChoices } : undefined
                }
              />
              {write && source.kind === 'folder' && ops && (
                <Button
                  variant="ghost"
                  className={`${topBarButton} max-sm:hidden`}
                  onClick={() => ops.newFolder(source.folder)}
                  title={t.manage.newFolder}
                  aria-label={t.manage.newFolder}
                >
                  <FolderPlus />
                </Button>
              )}
              {write && (
                <Button
                  size="lg"
                  onClick={newPage}
                  disabled={creating}
                  aria-label={t.list.newPage}
                  className="h-9 pointer-coarse:h-10 max-sm:w-9 max-sm:pointer-coarse:w-10 max-sm:px-0"
                >
                  {creating ? <Spinner /> : <FilePlus2 />}
                  <span className="max-sm:hidden">{creating ? t.list.creating : t.list.newPage}</span>
                </Button>
              )}
            </>
          )
        }
      >
        {source.kind === 'folder' ? (
          <Breadcrumbs folder={source.folder} />
        ) : (
          <>
            {Icon && <Icon className="size-4 shrink-0 text-muted-foreground" />}
            <h1 className="truncate text-sm font-medium">{title}</h1>
          </>
        )}
      </TopBar>

      <FileContextMenu
        className="flex w-full flex-1 flex-col"
        resolve={(key) => {
          const item = lookup.get(key)
          return item ? (selection.keys.has(key) ? selected : [item]) : null
        }}
        emptyParent={source.kind === 'folder' ? source.folder : undefined}
        onSelect={(key) => setSelection({ keys: new Set([key]), anchor: key })}
        afterAction={clear}
      >
        <div
          className={`mx-auto w-full flex-1 px-2 py-4 max-md:pb-20 sm:px-4 ${mode === 'cards' ? 'max-w-7xl' : isTable || filesTable || mode === 'board' ? 'max-w-[96rem]' : 'max-w-3xl'}`}
        >
          {/* The view tabs (or just the button for a new view) and the Sort and filter button share one row, which
              wraps when there is no room, so a phone does not spend a row on a lone "+". */}
          <div className="mb-2 flex flex-wrap items-center gap-x-3 gap-y-2 empty:hidden [&>[data-slot=filter-bar]]:mb-0 [&>[data-slot=view-bar]]:mb-0">
            {scope && !viewsPending && (
              <ViewBar
                scope={scope}
                views={saved}
                rev={working.list?.rev ?? ''}
                active={active}
                working={state}
                modified={modified && mine}
                write={write && working.list !== null}
                busy={busy}
                onPick={working.pick}
                onBegin={working.begin}
                onEnd={working.end}
                onRevert={working.revert}
              />
            )}
            {isFolder && (mode === 'list' || mode === 'cards') && !viewsPending && (
              <FilterBar
                filters={tableFilters}
                onChange={setTableFilters}
                type={type}
                sort={state.sort}
                onSort={setSort}
                sortDisabled={busy}
                mobile={mobile}
                headerSort={filesTable}
                folders={filesTable ? { mixed, onToggle: () => setMixed(!mixed) } : undefined}
              />
            )}
          </div>
          {views.isError && <p className="mb-2 text-xs text-muted-foreground">{t.views.loadFailed}</p>}
          {scope && !viewsPending && (typeName !== '' || source.kind === 'tag' || filters.length > 0) && (
            <Suspense fallback={null}>
              <ViewTools
                isTag={source.kind === 'tag'}
                type={type}
                state={{ ...state, layout: mode }}
                group={group}
                onChange={setPatch}
                disabled={busy}
              />
            </Suspense>
          )}
          {!filesTable &&
            !filesPhone &&
            folders.length > 0 &&
            (mode === 'cards' ? (
              <ul className="mb-4 grid grid-cols-1 gap-3 min-[480px]:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
                {folders.map((f) => (
                  <FolderCard key={f.path} folder={f} urls={urls} state={stateOf(folderItem(f))} />
                ))}
              </ul>
            ) : (
              <ul className={`divide-y divide-border/60 ${items.length > 0 ? 'mb-2' : ''}`}>
                {folders.map((f) => (
                  <FolderRow key={f.path} folder={f} urls={urls} state={stateOf(folderItem(f))} />
                ))}
              </ul>
            ))}

          {viewsPending || (typesPending && !listLike) ? (
            <Skeleton className="h-40 w-full" />
          ) : isTable && source.kind === 'folder' ? (
            typeName === '' ? (
              <Skeleton className="h-40 w-full" />
            ) : (
              <Suspense fallback={<Skeleton className="h-40 w-full" />}>
                <TableView
                  folder={source.folder}
                  typeName={typeName}
                  type={type}
                  sort={sortQ || undefined}
                  onSort={setSort}
                  sortDisabled={busy}
                  filters={filters}
                  hidden={state.hidden}
                />
              </Suspense>
            )
          ) : mode === 'board' && scope ? (
            <Suspense fallback={<Skeleton className="h-40 w-full" />}>
              <BoardView
                scope={scope}
                type={type}
                group={source.kind === 'folder' ? group : state.group}
                sort={sortQ || undefined}
                filters={filters}
                cardFields={state.cardFields}
              />
            </Suspense>
          ) : mode === 'read' && scope ? (
            <Suspense fallback={<Skeleton className="h-40 w-full" />}>
              <ReadingView
                params={{
                  ...('folder' in scope ? { folder: scope.folder } : { tag: [scope.tag] }),
                  ...(sortQ ? { sort: sortQ as ListParams['sort'] } : {}),
                  ...(filters.length ? { filter: filters } : {}),
                }}
              />
            </Suspense>
          ) : query.isPending ? (
            <div className="space-y-2 px-3" aria-busy="true">
              {[0, 1, 2, 3, 4, 5].map((i) => (
                <Skeleton key={i} className="h-14 w-full" />
              ))}
            </div>
          ) : query.isError ? (
            <ErrorState
              message={t.fmt.labelled(t.list.loadFailed, query.error.message)}
              onRetry={() => void query.refetch()}
            />
          ) : nothing ? (
            <div className="flex flex-col items-center gap-3 py-20 text-center">
              <p className="text-sm text-muted-foreground">
                {filtered ? t.ftable.nothingMatches : isInbox ? t.inbox.empty : empty.main}
              </p>
              {filtered && (
                <Button variant="outline" size="sm" onClick={() => setTableFilters(noFilters())}>
                  {t.ftable.clearFilters}
                </Button>
              )}
              {empty.hint && <p className="text-xs text-muted-foreground">{empty.hint}</p>}
              {write && isInbox && (
                <Button size="sm" onClick={capture.open} disabled={!capture.available}>
                  <PenLine />
                  {t.inbox.emptyAction}
                </Button>
              )}
              {write && source.kind === 'folder' && !isInbox && ops && (
                <div className="flex flex-wrap justify-center gap-2">
                  <Button variant="outline" size="sm" onClick={newPage} disabled={creating}>
                    <FilePlus2 />
                    {t.manage.newPage}
                  </Button>
                  <Button variant="outline" size="sm" onClick={() => ops.newFolder(source.folder)}>
                    <FolderPlus />
                    {t.manage.newFolder}
                  </Button>
                </div>
              )}
              {write && source.kind !== 'search' && source.kind !== 'folder' && !isInbox && (
                <Button variant="outline" size="sm" onClick={newPage} disabled={creating}>
                  <FilePlus2 />
                  {t.list.newPage}
                </Button>
              )}
            </div>
          ) : (filesTable || filesPhone) && tableRows.length > 0 ? (
            <>
              {filesTable ? (
                <FolderTable
                  folderPath={source.kind === 'folder' ? source.folder : ''}
                  rows={tableRows}
                  type={type}
                  sort={state.sort}
                  onSort={setSort}
                  sortDisabled={busy}
                  stateOf={stateOfRow}
                  selected={selection.keys}
                  onSelect={(next, tick) =>
                    setSelection(
                      tick?.shift
                        ? applyClick(order, selection, tick.id, { shift: true })
                        : { keys: next, anchor: tick && next.has(tick.id) ? tick.id : null },
                    )
                  }
                  loading={false}
                  hasNext={!!query.hasNextPage}
                  loadingMore={query.isFetchingNextPage}
                  onLoadMore={() => void fetchNext()}
                  empty={null}
                  defaultHidden={state.hidden}
                />
              ) : (
                <PhoneRows
                  rows={tableRows}
                  stateOf={stateOfRow}
                  hasNext={!!query.hasNextPage}
                  loadingMore={query.isFetchingNextPage}
                  onLoadMore={() => void fetchNext()}
                />
              )}
              {nextError && <ErrorState message={nextError.message} onRetry={() => void fetchNext()} />}
              <p className="py-2 text-center text-xs text-muted-foreground">
                {query.hasNextPage ? t.list.loaded(items.length) : t.list.allLoaded(items.length)}
              </p>
            </>
          ) : items.length > 0 ? (
            <>
              {mode === 'cards' ? (
                // Columns flow top to bottom, so short and long pieces pack together.
                <ul className="columns-1 gap-3 sm:columns-2 xl:columns-3 2xl:columns-4">
                  {items.map((p) => (
                    <PageCard key={p.id} page={p} urls={urls} state={stateOf(pageItem(p))} />
                  ))}
                </ul>
              ) : (
                <ul className="divide-y divide-border/60">
                  {items.map((p, i) => (
                    <PageRow
                      key={p.id}
                      page={p}
                      index={i}
                      urls={urls}
                      showFolder={source.kind !== 'folder'}
                      state={stateOf(pageItem(p))}
                    />
                  ))}
                </ul>
              )}
              <div className="flex flex-col items-center gap-2 py-6">
                {query.hasNextPage ? (
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => void fetchNext()}
                    disabled={query.isFetchingNextPage}
                  >
                    {query.isFetchingNextPage ? <Spinner /> : null}
                    {query.isFetchingNextPage ? t.list.loadingMore : t.list.loadMore}
                  </Button>
                ) : null}
                <span className="text-xs text-muted-foreground">
                  {query.hasNextPage ? t.list.loaded(items.length) : t.list.allLoaded(items.length)}
                </span>
                {nextError && <ErrorState message={nextError.message} onRetry={() => void fetchNext()} />}
              </div>
            </>
          ) : null}
        </div>
      </FileContextMenu>
      <QuickCaptureFab />
    </>
  )
}
