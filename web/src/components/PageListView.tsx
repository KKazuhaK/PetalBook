import { useIsMutating } from '@tanstack/react-query'
import { lazy, Suspense, useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { ArrowUpDown, BookOpen, Columns3, FilePlus2, FolderPlus, LayoutGrid, List as ListIcon, PenLine, Table2, Tag as TagIcon } from 'lucide-react'
import { toast } from 'sonner'
import { useActiveSpace, useCreatePage, usePageList, useSpacePermissions, useTypes, useUrls, useViews } from '@/api/hooks'
import type { ListParams, PropFilter, SortKey, ViewScope } from '@/api/types'
import { Button } from '@/components/ui/button'
import { NativeSelect } from '@/components/ui/native-select'
import { Skeleton } from '@/components/ui/skeleton'
import { ErrorState, Spinner } from '@/components/common'
import { QuickCaptureFab, useQuickCapture } from '@/components/layout/QuickCapture'
import { TopBar } from '@/components/layout/TopBar'
import { ViewBar } from '@/components/views/ViewBar'
import { Breadcrumbs } from '@/components/files/Breadcrumbs'
import { FolderCard, FolderRow, PageCard, PageRow, folderItem, pageItem, type EntryState } from '@/components/files/Entries'
import { FileContextMenu } from '@/components/files/FileContextMenu'
import { useFileOps } from '@/components/files/FileOps'
import { SelectionBar } from '@/components/files/SelectionBar'
import { childFolders, itemKey, type FileItem } from '@/lib/folders'
import { loadView, saveView, viewKey, type ListViewMode } from '@/lib/listView'
import { folderTypeName } from '@/lib/pageTypes'
import { DEFAULT_STATE, groupFieldsOf, loadViewId, pickView, sameState, saveViewId, SORT_WORDS, snapshotOf, stateOfView, followSnapshot, isHandMark, viewIdKey, type ViewSnapshot, type ViewState } from '@/lib/savedViews'
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

function ViewSwitch({ mode, onChange, table, board, read }: { mode: ListViewMode; onChange: (m: ListViewMode) => void; table: boolean; board: boolean; read: boolean }) {
  const item = (m: ListViewMode, label: string, Icon: typeof ListIcon) => (
    <button
      type="button"
      aria-pressed={mode === m}
      aria-label={label}
      title={label}
      onClick={() => onChange(m)}
      className={`flex size-7 items-center justify-center rounded-md outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 ${mode === m ? 'bg-muted text-foreground' : 'text-muted-foreground hover:text-foreground'}`}
    >
      <Icon className="size-4" />
    </button>
  )
  return (
    <div role="group" aria-label={t.view.label} className="flex items-center rounded-lg border p-0.5">
      {item('list', t.view.list, ListIcon)}
      {item('cards', t.view.cards, LayoutGrid)}
      {table && item('table', t.view.table, Table2)}
      {board && item('board', t.view.board, Columns3)}
      {read && item('read', t.view.read, BookOpen)}
    </div>
  )
}

// What is changed in the view being shown without being saved: the working copy is the saved view with these on top.
interface Edits {
  // Whose: the id of the view, '' for the implicit one. Edits of a view that is not the one shown are ignored.
  id: string
  patch: Partial<ViewState>
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
  const scope: ViewScope | null = source.kind === 'folder' ? { folder: source.folder } : source.kind === 'tag' ? { tag: source.tag } : null
  const views = useViews(scope)
  const viewsPending = scope !== null && views.isPending
  const saved = useMemo(() => views.data?.views ?? [], [views.data])
  const idKey = viewIdKey(spaceId ?? '', source.kind, source.kind === 'folder' ? source.folder : source.kind === 'tag' ? source.tag : '')
  const [remembered, setRemembered] = useState(() => loadViewId(idKey))
  // The view shown, when it is one written by hand ("@n"), as it was read: where it is once the list changes is found by
  // its name and what it saves, never by its place (another person may delete, move or add views).
  const [snap, setSnap] = useState<ViewSnapshot | null>(null)
  // Where the person's own write put the view shown (the answer lists the views in the order sent, so this is exact where
  // the name and settings are not enough, for two views alike). It waits until the views of the answer are in the cache.
  const [hint, setHint] = useState<{ from: string; to: string } | null>(null)
  const [lostNotice, setLostNotice] = useState<{ name: string } | null>(null)
  const [edits, setEdits] = useState<Edits>({ id: '', patch: {} })
  const follow = scope && snap && views.data ? followSnapshot(saved, snap) : null
  // A save of the views is on its way: the list may already hold what it writes (a refetch after the server committed),
  // which is not another person's change.
  const saving = useIsMutating({ mutationKey: ['saveViews'] }) > 0
  const gone = ((follow?.gone ?? false) && !saving) || (hint !== null && snap !== null && hint.from === snap.id && saved.some((v) => v.id === hint.to))
  const hinted = hint && snap && hint.from === snap.id ? (saved.find((v) => v.id === hint.to) ?? null) : null
  const moved = hinted ?? follow?.to ?? null
  const active = scope ? (gone ? (moved ?? saved[0] ?? null) : pickView(saved, remembered)) : null
  const base: ViewState = active ? stateOfView(active) : { ...DEFAULT_STATE, layout: chosen }
  const patch = edits.id === (active?.id ?? '') ? edits.patch : {}
  const sortParam = params.get('sort')
  const urlSort = sortParam !== null && (SORTS.includes(sortParam as SortKey) || (scope !== null && SORT_WORDS.test(sortParam))) ? sortParam : null
  const state: ViewState = { ...base, ...patch, sort: urlSort ?? base.sort }
  const modified = active !== null && !sameState({ ...state, sort: normSort(state.sort) }, { ...base, sort: normSort(base.sort) })

  const setPatch = (p: Partial<ViewState>) => setEdits({ id: active?.id ?? '', patch: { ...patch, ...p } })
  const setSort = (to: string) =>
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        // What the view already says needs no word in the address.
        if (to === base.sort || (source.kind !== 'search' && normSort(to) === normSort(base.sort))) next.delete('sort')
        else next.set('sort', to)
        return next
      },
      { replace: true },
    )
  const clearEdits = () => {
    setEdits({ id: '', patch: {} })
    setParams((prev) => {
      if (!prev.has('sort')) return prev
      const next = new URLSearchParams(prev)
      next.delete('sort')
      return next
    }, { replace: true })
  }
  // Shows a view and drops the working copy of the one shown (a saved view has nothing left of it).
  const select = (id: string) => {
    // What a write of the person said about the old view (and the snapshot of it) does not apply to the one chosen now.
    setHint(null)
    setSnap(null)
    setRemembered(id)
    saveViewId(idKey, id)
    clearEdits()
  }
  // Clicking the tab that is already shown changes nothing, and does not drop what was changed in it.
  const pick = (id: string) => {
    if (id !== (active?.id ?? '')) select(id)
  }
  // The view shown got a new id (a view written by hand has "@n" until a write names it): its working copy goes with it.
  const remap = (from: string, to: string) => {
    setRemembered(to)
    saveViewId(idKey, to)
    setEdits((e) => (e.id === from ? { ...e, id: to } : e))
  }

  // The view shown was "@n" and the list changed under it (a write of the person, another person's write seen by a
  // refetch, or a refused save): the one view with its name and settings is the same view, and the remembered id, the
  // stored one and the working copy follow it. With none or several there is no telling, so the working copy is
  // dropped, the person is told, and the first view is shown.
  if (snap && gone) {
    if (moved) {
      remap(snap.id, moved.id)
      setHint(null)
      setSnap(isHandMark(moved.id) ? snapshotOf(moved) : null)
    } else {
      setRemembered('')
      saveViewId(idKey, '')
      if (edits.id === snap.id && Object.keys(edits.patch).length > 0) setLostNotice({ name: snap.name })
      setEdits({ id: '', patch: {} })
      setSnap(null)
    }
  } else if (active && isHandMark(active.id)) {
    if (snap?.id !== active.id) setSnap(snapshotOf(active))
  } else if (snap && !saving) setSnap(null)
  useEffect(() => {
    if (lostNotice) toast.error(t.views.lost(lostNotice.name))
  }, [lostNotice])

  // What the layouts need: the table and the board a folder with a type (the board a single-choice field of it), a tag's
  // board a field a view named, reading any folder or tag. While the types are on their way the choice is kept.
  const groupFields = groupFieldsOf(type)
  const group = state.group || groupFields[0]?.key || ''
  const typesPending = source.kind === 'folder' && types.isPending
  const tableOk = typeName !== ''
  const boardOk = source.kind === 'folder' ? group !== '' : source.kind === 'tag' && state.group !== ''
  const readOk = scope !== null
  const mode: ListViewMode = state.layout === 'table' && !tableOk && !typesPending ? 'list' : state.layout === 'board' && !boardOk && !typesPending ? 'list' : state.layout === 'read' && !readOk ? 'list' : state.layout
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

  const query = usePageList({
    ...(source.kind === 'folder' ? { folder: source.folder } : {}),
    // "最近" is every page, newest first, wherever it lives.
    ...(source.kind === 'recent' ? { folder: '', recursive: true } : {}),
    ...(source.kind === 'tag' ? { tag: [source.tag] } : {}),
    ...(source.kind === 'search' ? { ...source.filters, q: source.q } : {}),
    ...(sortQ ? { sort: sortQ as ListParams['sort'] } : {}),
    ...(filters.length ? { filter: filters } : {}),
  }, listLike && !viewsPending)
  // Read before the status checks below narrow `query` (a failed "load more" leaves the list in place).
  const nextError = query.isFetchNextPageError ? query.error : null
  const fetchNext = query.fetchNextPage
  const items = query.data?.pages.flatMap((p) => p.items) ?? []
  const spaceName = ops?.spaceName ?? t.nav.rootFolder
  const title = titleOf(source, spaceName)
  useDocumentTitle(`${title} · ${t.appName}`)

  // Subfolders, from the tree: it lists every folder with the pages directly in it.
  const folders = source.kind === 'folder' && ops ? childFolders(ops.index, source.folder) : []

  // Everything selectable, in the order shown (for shift-click ranges).
  const lookup = new Map<string, FileItem>()
  for (const f of folders) lookup.set(itemKey(folderItem(f)), folderItem(f))
  for (const p of items) lookup.set(itemKey(pageItem(p)), pageItem(p))
  const order = [...lookup.keys()]
  const selection = prune(rawSelection, order)
  const selected = [...selection.keys].map((k) => lookup.get(k)).filter((i): i is FileItem => !!i)
  const clear = () => setSelection(emptySelection())

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
      if ((e.target as Element | null)?.closest?.('input, textarea, select, [contenteditable="true"], [role="dialog"], [role="alertdialog"], [role="menu"]')) return
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
    try {
      const page = await create.mutateAsync({
        folder: source.kind === 'folder' ? source.folder : '',
        ...(source.kind === 'tag' ? { meta: { tags: [source.tag] } } : {}),
        body: '',
      })
      navigate(urls.page(page.id))
    } catch (e) {
      toast.error(t.fmt.labelled(t.list.createFailed, e instanceof Error ? e.message : ''))
      setCreating(false)
    }
  }

  const Icon = source.kind === 'tag' ? TagIcon : null
  const empty = emptyText(source, write)
  const treeReady = source.kind !== 'folder' || !ops || ops.ready
  const nothing = listLike && !query.isPending && !query.isError && items.length === 0 && folders.length === 0 && treeReady

  return (
    <>
      <TopBar
        actions={
          selection.keys.size > 0 && ops && write ? (
            <SelectionBar count={selection.keys.size} onMove={() => ops.chooseMove(selected, clear)} onDelete={() => ops.confirmDelete(selected, clear)} onClear={clear} />
          ) : (
          <>
            {/* On a phone the sort choice shrinks to an icon (the select is still what opens), leaving the room to the breadcrumbs. */}
            {/* In a table the headers of the columns sort. */}
            {!isTable && (
            <div className="relative w-28 max-sm:w-8 max-sm:[&>div>svg]:hidden">
            <ArrowUpDown className="pointer-events-none absolute top-1/2 left-1/2 size-4 -translate-x-1/2 -translate-y-1/2 text-muted-foreground sm:hidden" />
            <NativeSelect
              className="h-7 text-[0.8rem] max-sm:h-8 max-sm:opacity-0"
              aria-label={t.list.sort}
              value={isSearch ? state.sort : normSort(state.sort) === '-updated' ? '' : state.sort}
              onChange={(e) => setSort(isSearch ? e.target.value : e.target.value || '-updated')}
            >
              {/* Search defaults to relevance; everything else to newest first. */}
              <option value="">{isSearch ? t.list.sorts.relevance : t.list.sorts['-updated']}</option>
              {SORTS.filter((s) => isSearch || s !== '-updated').map((s) => (
                <option key={s} value={s}>
                  {t.list.sorts[s]}
                </option>
              ))}
              {/* A view can sort by a property, which the plain choices do not name. */}
              {state.sort !== '' && !SORTS.includes(state.sort as SortKey) && <option value={state.sort}>{t.views.customSort(state.sort.replace(/^-?(prop:)?/, ''))}</option>}
            </NativeSelect>
            </div>
            )}
            <ViewSwitch mode={mode} onChange={setMode} table={tableOk} board={boardOk || typesPending} read={readOk} />
            {write && source.kind === 'folder' && ops && (
              <Button variant="outline" size="icon-sm" className="max-sm:hidden" onClick={() => ops.newFolder(source.folder)} title={t.manage.newFolder} aria-label={t.manage.newFolder}>
                <FolderPlus />
              </Button>
            )}
            {write && (
              <Button size="sm" onClick={newPage} disabled={creating} aria-label={t.list.newPage}>
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
        <div className={`mx-auto w-full flex-1 px-2 py-4 max-md:pb-20 sm:px-4 ${mode === 'cards' ? 'max-w-7xl' : isTable || mode === 'board' ? 'max-w-[96rem]' : 'max-w-3xl'}`}>
          {scope && !viewsPending && (
            <ViewBar
              scope={scope}
              views={saved}
              rev={views.data?.rev ?? ''}
              active={active}
              working={state}
              modified={modified}
              write={write && views.data !== undefined}
              onPick={pick}
              onSaved={select}
              onRemap={(from, to) => setHint({ from, to })}
              onRevert={clearEdits}
            />
          )}
          {views.isError && <p className="mb-2 text-xs text-muted-foreground">{t.views.loadFailed}</p>}
          {scope && !viewsPending && (typeName !== '' || source.kind === 'tag' || filters.length > 0) && (
            <Suspense fallback={null}>
              <ViewTools isTag={source.kind === 'tag'} type={type} state={{ ...state, layout: mode }} group={group} onChange={setPatch} />
            </Suspense>
          )}
          {folders.length > 0 &&
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
                <TableView folder={source.folder} typeName={typeName} type={type} sort={sortQ || undefined} onSort={setSort} filters={filters} hidden={state.hidden} />
              </Suspense>
            )
          ) : mode === 'board' && scope ? (
            <Suspense fallback={<Skeleton className="h-40 w-full" />}>
              <BoardView scope={scope} type={type} group={source.kind === 'folder' ? group : state.group} sort={sortQ || undefined} filters={filters} cardFields={state.cardFields} />
            </Suspense>
          ) : mode === 'read' && scope ? (
            <Suspense fallback={<Skeleton className="h-40 w-full" />}>
              <ReadingView params={{ ...('folder' in scope ? { folder: scope.folder } : { tag: [scope.tag] }), ...(sortQ ? { sort: sortQ as ListParams['sort'] } : {}), ...(filters.length ? { filter: filters } : {}) }} />
            </Suspense>
          ) : query.isPending ? (
            <div className="space-y-2 px-3" aria-busy="true">
              {[0, 1, 2, 3, 4, 5].map((i) => (
                <Skeleton key={i} className="h-14 w-full" />
              ))}
            </div>
          ) : query.isError ? (
            <ErrorState message={t.fmt.labelled(t.list.loadFailed, query.error.message)} onRetry={() => void query.refetch()} />
          ) : nothing ? (
            <div className="flex flex-col items-center gap-3 py-20 text-center">
              <p className="text-sm text-muted-foreground">{isInbox ? t.inbox.empty : empty.main}</p>
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
                    <PageRow key={p.id} page={p} index={i} urls={urls} showFolder={source.kind !== 'folder'} state={stateOf(pageItem(p))} />
                  ))}
                </ul>
              )}
              <div className="flex flex-col items-center gap-2 py-6">
                {query.hasNextPage ? (
                  <Button variant="outline" size="sm" onClick={() => void fetchNext()} disabled={query.isFetchingNextPage}>
                    {query.isFetchingNextPage ? <Spinner /> : null}
                    {query.isFetchingNextPage ? t.list.loadingMore : t.list.loadMore}
                  </Button>
                ) : null}
                <span className="text-xs text-muted-foreground">{query.hasNextPage ? t.list.loaded(items.length) : t.list.allLoaded(items.length)}</span>
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
