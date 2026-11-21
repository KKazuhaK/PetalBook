import { useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { FilePlus2, Folder as FolderIcon, Tag as TagIcon } from 'lucide-react'
import { toast } from 'sonner'
import { useCreatePage, usePageList, useUrls } from '@/api/hooks'
import type { PageSummary, SortKey } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { NativeSelect } from '@/components/ui/native-select'
import { Skeleton } from '@/components/ui/skeleton'
import { ErrorState, Spinner } from '@/components/common'
import { TopBar } from '@/components/layout/TopBar'
import { absoluteTime, relativeTime } from '@/lib/time'
import type { SpaceUrls } from '@/lib/urls'
import { t } from '@/i18n/zh-CN'
import { useDocumentTitle } from '@/lib/useDocumentTitle'

export type ListSource =
  | { kind: 'recent' }
  | { kind: 'folder'; folder: string }
  | { kind: 'tag'; tag: string }
  | { kind: 'search'; q: string }

const SORTS: SortKey[] = ['-updated', 'updated', '-created', 'created', 'title', '-title']

const sourceKey = (s: ListSource) => JSON.stringify(s)

function titleOf(s: ListSource): string {
  switch (s.kind) {
    case 'recent':
      return t.list.recentTitle
    case 'folder':
      return s.folder === '' ? t.nav.rootFolder : s.folder
    case 'tag':
      return t.list.tagTitle(s.tag)
    case 'search':
      return t.list.searchTitle(s.q)
  }
}

function emptyText(s: ListSource): { main: string; hint?: string } {
  switch (s.kind) {
    case 'recent':
      return { main: t.list.emptyRecent }
    case 'folder':
      return { main: t.list.emptyFolder }
    case 'tag':
      return { main: t.list.emptyTag }
    case 'search':
      return { main: t.list.emptySearch(s.q), hint: t.list.emptySearchHint }
  }
}

function Row({ page, showFolder, index, urls }: { page: PageSummary; showFolder: boolean; index: number; urls: SpaceUrls }) {
  return (
    <li>
      <Link
        to={urls.page(page.id)}
        data-index={index}
        className="group flex items-start gap-3 rounded-lg px-3 py-2.5 outline-none transition-colors hover:bg-muted/70 focus-visible:bg-muted/70 focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-2">
            {/* An untitled page shows its first line, in a lighter colour. */}
            <span className={`truncate text-[0.95rem] ${page.untitled ? 'text-muted-foreground' : 'font-medium'}`}>{page.label || '空白页面'}</span>
            {page.subtitle && <span className="hidden truncate text-sm text-muted-foreground sm:inline">{page.subtitle}</span>}
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
                <Badge key={tag} variant="secondary" className="h-5 px-1.5 text-xs font-normal">
                  {tag}
                </Badge>
              ))}
            </div>
          )}
        </div>
        <time dateTime={page.updated} title={absoluteTime(page.updated)} className="shrink-0 pt-0.5 text-xs whitespace-nowrap text-muted-foreground tabular-nums">
          {relativeTime(page.updated)}
        </time>
      </Link>
    </li>
  )
}

export function PageListView({ source }: { source: ListSource }) {
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
  const create = useCreatePage()
  const urls = useUrls()
  const [creating, setCreating] = useState(false)

  const sortParam = params.get('sort') as SortKey | null
  const sort = sortParam && SORTS.includes(sortParam) ? sortParam : undefined
  const isSearch = source.kind === 'search'

  const query = usePageList({
    ...(source.kind === 'folder' ? { folder: source.folder } : {}),
    // "最近" is every page, newest first, wherever it lives.
    ...(source.kind === 'recent' ? { folder: '', recursive: true } : {}),
    ...(source.kind === 'tag' ? { tag: [source.tag] } : {}),
    ...(source.kind === 'search' ? { q: source.q } : {}),
    ...(sort ? { sort } : {}),
  })
  // Read before the status checks below narrow `query` (a failed "load more" leaves the list in place).
  const nextError = query.isFetchNextPageError ? query.error : null
  const fetchNext = query.fetchNextPage
  const items = query.data?.pages.flatMap((p) => p.items) ?? []
  const title = titleOf(source)
  useDocumentTitle(`${title} · ${t.appName}`)

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
      toast.error(`${t.list.createFailed}：${e instanceof Error ? e.message : ''}`)
      setCreating(false)
    }
  }

  const Icon = source.kind === 'folder' ? FolderIcon : source.kind === 'tag' ? TagIcon : null
  const empty = emptyText(source)

  return (
    <>
      <TopBar
        actions={
          <>
            <div className="w-28">
            <NativeSelect
              className="h-7 text-[0.8rem]"
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
            <Button size="sm" onClick={newPage} disabled={creating}>
              {creating ? <Spinner /> : <FilePlus2 />}
              {creating ? t.list.creating : t.list.newPage}
            </Button>
          </>
        }
      >
        {Icon && <Icon className="size-4 shrink-0 text-muted-foreground" />}
        <h1 className="truncate text-sm font-medium">{title}</h1>
      </TopBar>

      <div className="mx-auto w-full max-w-3xl flex-1 px-2 py-4 sm:px-4" key={sourceKey(source)}>
        {query.isPending ? (
          <div className="space-y-2 px-3" aria-busy="true">
            {[0, 1, 2, 3, 4, 5].map((i) => (
              <Skeleton key={i} className="h-14 w-full" />
            ))}
          </div>
        ) : query.isError ? (
          <ErrorState message={`${t.list.loadFailed}：${query.error.message}`} onRetry={() => void query.refetch()} />
        ) : items.length === 0 ? (
          <div className="flex flex-col items-center gap-3 py-20 text-center">
            <p className="text-sm text-muted-foreground">{empty.main}</p>
            {empty.hint && <p className="text-xs text-muted-foreground">{empty.hint}</p>}
            {source.kind !== 'search' && (
              <Button variant="outline" size="sm" onClick={newPage} disabled={creating}>
                <FilePlus2 />
                {t.list.newPage}
              </Button>
            )}
          </div>
        ) : (
          <>
            <ul className="divide-y divide-border/60">
              {items.map((p, i) => (
                <Row key={p.id} page={p} index={i} urls={urls} showFolder={source.kind !== 'folder'} />
              ))}
            </ul>
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
        )}
      </div>
    </>
  )
}
