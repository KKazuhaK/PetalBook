import { useEffect, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { ArrowUpDown, FilePlus2, FolderPlus, LayoutGrid, List as ListIcon, PenLine, Tag as TagIcon } from 'lucide-react'
import { toast } from 'sonner'
import { useActiveSpace, useCreatePage, usePageList, useSpacePermissions, useUrls } from '@/api/hooks'
import type { ListParams, SortKey } from '@/api/types'
import { Button } from '@/components/ui/button'
import { NativeSelect } from '@/components/ui/native-select'
import { Skeleton } from '@/components/ui/skeleton'
import { ErrorState, Spinner } from '@/components/common'
import { QuickCaptureFab, useQuickCapture } from '@/components/layout/QuickCapture'
import { TopBar } from '@/components/layout/TopBar'
import { Breadcrumbs } from '@/components/files/Breadcrumbs'
import { FolderCard, FolderRow, PageCard, PageRow, folderItem, pageItem, type EntryState } from '@/components/files/Entries'
import { FileContextMenu } from '@/components/files/FileContextMenu'
import { useFileOps } from '@/components/files/FileOps'
import { SelectionBar } from '@/components/files/SelectionBar'
import { childFolders, itemKey, type FileItem } from '@/lib/folders'
import { loadView, saveView, viewKey, type ListViewMode } from '@/lib/listView'
import { applyClick, emptySelection, prune, type Selection } from '@/lib/selection'
import { t } from '@/i18n'
import { useDocumentTitle } from '@/lib/useDocumentTitle'

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

function ViewSwitch({ mode, onChange }: { mode: ListViewMode; onChange: (m: ListViewMode) => void }) {
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
    </div>
  )
}

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
  const [mode, setMode] = useViewMode(spaceId ?? '', source)
  const capture = useQuickCapture()
  const isInbox = source.kind === 'folder' && source.folder === spaces.find((s) => s.id === spaceId)?.inbox
  const [rawSelection, setSelection] = useState<Selection>(emptySelection)

  const sortParam = params.get('sort') as SortKey | null
  const sort = sortParam && SORTS.includes(sortParam) ? sortParam : undefined
  const isSearch = source.kind === 'search'

  const query = usePageList({
    ...(source.kind === 'folder' ? { folder: source.folder } : {}),
    // "最近" is every page, newest first, wherever it lives.
    ...(source.kind === 'recent' ? { folder: '', recursive: true } : {}),
    ...(source.kind === 'tag' ? { tag: [source.tag] } : {}),
    ...(source.kind === 'search' ? { ...source.filters, q: source.q } : {}),
    ...(sort ? { sort } : {}),
  })
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
  const nothing = !query.isPending && !query.isError && items.length === 0 && folders.length === 0 && treeReady

  return (
    <>
      <TopBar
        actions={
          selection.keys.size > 0 && ops && write ? (
            <SelectionBar count={selection.keys.size} onMove={() => ops.chooseMove(selected, clear)} onDelete={() => ops.confirmDelete(selected, clear)} onClear={clear} />
          ) : (
          <>
            {/* On a phone the sort choice shrinks to an icon (the select is still what opens), leaving the room to the breadcrumbs. */}
            <div className="relative w-28 max-sm:w-8 max-sm:[&>div>svg]:hidden">
            <ArrowUpDown className="pointer-events-none absolute top-1/2 left-1/2 size-4 -translate-x-1/2 -translate-y-1/2 text-muted-foreground sm:hidden" />
            <NativeSelect
              className="h-7 text-[0.8rem] max-sm:h-8 max-sm:opacity-0"
              aria-label={t.list.sort}
              value={sort ?? ''}
              onChange={(e) =>
                setParams(
                  (prev) => {
                    const next = new URLSearchParams(prev)
                    if (e.target.value) next.set('sort', e.target.value)
                    else next.delete('sort')
                    return next
                  },
                  { replace: true },
                )
              }
            >
              {/* Search defaults to relevance; everything else to newest first. */}
              <option value="">{isSearch ? t.list.sorts.relevance : t.list.sorts['-updated']}</option>
              {SORTS.filter((s) => isSearch || s !== '-updated').map((s) => (
                <option key={s} value={s}>
                  {t.list.sorts[s]}
                </option>
              ))}
            </NativeSelect>
            </div>
            <ViewSwitch mode={mode} onChange={setMode} />
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
        <div className={`mx-auto w-full flex-1 px-2 py-4 max-md:pb-20 sm:px-4 ${mode === 'cards' ? 'max-w-7xl' : 'max-w-3xl'}`}>
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

          {query.isPending ? (
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
