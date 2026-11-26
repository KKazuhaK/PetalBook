import { useMemo, useRef } from 'react'
import { scrollFadeClass, useScrollFade } from '@/components/ui/scroll-fade'
import { topBarButton, topBarTextButton } from '@/components/layout/topBarStyles'
import { Link, useSearchParams } from 'react-router'
import { ArrowLeft, CheckCheck } from 'lucide-react'
import { useMarkAllNotificationsRead, useNotificationList } from '@/api/hooks'
import { ErrorState, PageFallback } from '@/components/common'
import { TopBar } from '@/components/layout/TopBar'
import { Button } from '@/components/ui/button'
import { useDocumentTitle } from '@/lib/useDocumentTitle'
import { NOTIFY_KINDS } from '@/api/types'
import { tn } from '@/i18n/notify'
import { NotificationRow } from './NotificationRow'

// The filters: everything, only what is unread, and one per kind of the registry. The ones with nothing made yet (mentions) are there
// all the same, so the place is known before the first one comes.
const FILTERS = ['all', 'unread', ...NOTIFY_KINDS] as const
type Filter = (typeof FILTERS)[number]

const isFilter = (v: string | null): v is Filter => FILTERS.includes(v as Filter)

// The full page of notifications: filters by kind, and the history, a cursor page at a time. On a phone it is where the
// bell goes; on a desktop it is behind "see all" in the popover.
export default function NotificationsPage() {
  const fadeRef = useRef<HTMLDivElement>(null)
  useScrollFade(fadeRef)
  useDocumentTitle(tn.title)
  const [params, setParams] = useSearchParams()
  const raw = params.get('show')
  const filter: Filter = isFilter(raw) ? raw : 'all'
  const kind = filter === 'all' || filter === 'unread' ? '' : filter
  const list = useNotificationList(kind, filter === 'unread', 20)
  const markAll = useMarkAllNotificationsRead()
  const items = useMemo(() => list.data?.pages.flatMap((p) => p.items) ?? [], [list.data])
  const unread = list.data?.pages[0]?.unread ?? 0
  const moreProblems = list.data?.pages[0]?.moreProblems ?? 0
  const pick = (f: Filter) =>
    setParams(
      (p) => {
        const next = new URLSearchParams(p)
        if (f === 'all') next.delete('show')
        else next.set('show', f)
        return next
      },
      { replace: true },
    )
  return (
    <div className="flex min-h-svh flex-col" data-slot="notifications-page">
      <TopBar
        actions={
          <Button
            variant="ghost"
            className={topBarTextButton}
            disabled={unread === 0 || markAll.isPending}
            onClick={() => markAll.mutate(kind || undefined)}
          >
            <CheckCheck aria-hidden="true" />
            {tn.markAll}
          </Button>
        }
      >
        <Link to="/" aria-label={tn.back} className={`${topBarButton} md:hidden`}>
          <ArrowLeft className="size-5" />
        </Link>
        <h1 className="truncate text-base font-semibold">{tn.title}</h1>
      </TopBar>
      <main className="mx-auto w-full max-w-2xl flex-1 px-4 py-4">
        <div
          ref={fadeRef}
          role="group"
          aria-label={tn.filterLabel}
          className={`${scrollFadeClass} -mx-4 mb-4 flex gap-2 px-4 pb-1`}
        >
          {FILTERS.map((f) => (
            <button
              key={f}
              type="button"
              aria-pressed={filter === f}
              data-filter={f}
              onClick={() => pick(f)}
              className={`h-10 shrink-0 rounded-full border px-4 text-sm whitespace-nowrap outline-none transition-colors duration-150 focus-visible:ring-2 focus-visible:ring-ring/50 ${filter === f ? 'border-brand bg-brand/10 font-medium text-brand' : 'text-muted-foreground hover:bg-accent hover:text-foreground'}`}
            >
              {tn.filters[f]}
            </button>
          ))}
        </div>
        {list.isPending && <PageFallback />}
        {list.isError && !list.data && <ErrorState message={tn.loadFailed} onRetry={() => void list.refetch()} />}
        {list.isSuccess && items.length === 0 && (
          <div className="rounded-xl border px-4 py-10 text-center text-sm">
            <p className="font-medium">
              {filter === 'unread' ? tn.emptyUnread : filter === 'all' ? tn.empty : tn.emptyKind}
            </p>
            <p className="mt-1 text-muted-foreground">{tn.emptyHelp}</p>
          </div>
        )}
        {items.length > 0 && (
          <div className="overflow-hidden rounded-xl border bg-card" role="list" aria-label={tn.title}>
            {items.map((n) => (
              <div role="listitem" key={n.id}>
                <NotificationRow item={n} />
              </div>
            ))}
          </div>
        )}
        {moreProblems > 0 && (
          <p className="mt-2 px-1 text-sm text-muted-foreground" data-slot="more-problems">
            {tn.moreProblems(moreProblems)}
          </p>
        )}
        {list.hasNextPage && (
          <div className="mt-4 flex justify-center">
            <Button
              variant="outline"
              className="h-10"
              disabled={list.isFetchingNextPage}
              onClick={() => void list.fetchNextPage()}
            >
              {list.isFetchingNextPage ? tn.loading : tn.more}
            </Button>
          </div>
        )}
        <p className="mt-6 text-xs text-muted-foreground">{tn.historyHelp}</p>
      </main>
    </div>
  )
}
