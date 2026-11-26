import { useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import { ArrowRight, Inbox as InboxIcon } from 'lucide-react'
import { useActiveSpace, useSession, useSpacePermissions, useTopPages, useTree, useUrls } from '@/api/hooks'
import type { PageSummary } from '@/api/types'
import { ErrorState } from '@/components/common'
import { TopBar } from '@/components/layout/TopBar'
import { Skeleton } from '@/components/ui/skeleton'
import { getVariety, visitKey, wroteKey } from '@/lib/homePrefs'
import { relativeTime } from '@/lib/time'
import { useDocumentTitle } from '@/lib/useDocumentTitle'
import { t } from '@/i18n'
import { th } from '@/i18n/home'
import { DailyBlock } from './DailyBlock'
import { Greeting } from './Greeting'
import { HomeFavorites } from './HomeFavorites'
import { QuickNote } from './QuickNote'
import { viewAsId } from '@/lib/viewAs'

const CONTINUE_COUNT = 8
const INBOX_COUNT = 5
// Home takes the first few of each: the number of requests does not grow with the pages.
const RECENT = { folder: '', recursive: true } as const

function Block({
  title,
  children,
  action,
  id,
}: {
  title: string
  children: ReactNode
  action?: ReactNode
  id: string
}) {
  return (
    <section aria-labelledby={id} className="grid grid-cols-1 gap-2">
      <div className="flex items-center justify-between gap-2">
        <h2 id={id} className="text-sm font-semibold">
          {title}
        </h2>
        {action}
      </div>
      {children}
    </section>
  )
}

function PageLines({ pages, empty }: { pages: PageSummary[]; empty: string }) {
  const urls = useUrls()
  if (pages.length === 0) return <p className="text-sm text-muted-foreground">{empty}</p>
  return (
    <ul className="divide-y divide-border/60 rounded-lg border">
      {pages.map((p) => (
        <li key={p.id}>
          <Link
            to={urls.page(p.id)}
            className="flex items-baseline gap-3 px-3 py-2 text-sm outline-none hover:bg-muted/50 focus-visible:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring/50"
          >
            <span className={`min-w-0 flex-1 truncate ${p.untitled ? 'text-muted-foreground' : 'font-medium'}`}>
              {p.label || th.cont.untitled}
            </span>
            <span className="shrink-0 text-xs text-muted-foreground tabular-nums">{relativeTime(p.updated)}</span>
          </Link>
        </li>
      ))}
    </ul>
  )
}

// The home page of a space: a greeting with the text of the day, a place to jot a note, the pages being worked on,
// the inbox and the favourites. At most five requests, whatever the size of the space: the text of the day, the
// recent pages, the first of the inbox, the favourites, and the folder counts (which the sidebar asks for too).
// Someone who may only read has no note box; single-user mode has all of it.
export default function HomePage() {
  const { id: space, spaces } = useActiveSpace()
  const current = spaces.find((s) => s.id === space)
  const { read, write } = useSpacePermissions()
  const session = useSession().data
  const urls = useUrls()
  useDocumentTitle(`${th.title} · ${current?.name ?? t.appName}`)

  // The clock is read once: the page does not change its greeting under the person while it stays open.
  const [now] = useState(() => new Date())
  const user = session?.user
  const accounts = session?.mode === 'accounts'
  const [variety] = useState(getVariety)

  const recent = useTopPages(RECENT, CONTINUE_COUNT)
  const inbox = current?.inbox ?? ''
  const tree = useTree()
  const inboxPages = useTopPages({ folder: inbox }, INBOX_COUNT, inbox !== '')
  const inboxCount = tree.data?.folders.find((f) => f.path === inbox)?.pages ?? 0

  if (!current || !read) return null

  return (
    <>
      <TopBar>
        <h1 className="sr-only">{th.title}</h1>
        <span className="truncate text-sm font-medium">{current.name}</span>
      </TopBar>
      <div
        className="mx-auto grid w-full max-w-3xl grid-cols-1 flex-1 content-start gap-8 px-4 py-8 md:px-8 md:py-10"
        data-slot="home"
      >
        <header className="grid gap-4">
          <Greeting
            now={now}
            name={accounts ? user?.name : undefined}
            registered={accounts ? user?.created : undefined}
            visitKey={visitKey(user?.id, space ?? '')}
            wroteKey={wroteKey(user?.id, space ?? '')}
            variety={variety}
          />
          <DailyBlock now={now} />
        </header>

        {write && !!inbox && (
          <Block id="home-quick" title={th.quick.title}>
            <QuickNote space={space ?? ''} inbox={inbox} />
          </Block>
        )}

        <Block
          id="home-continue"
          title={th.cont.title}
          action={
            <Link
              to={urls.recent()}
              className="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
            >
              {th.cont.all}
              <ArrowRight className="size-3" />
            </Link>
          }
        >
          {recent.isPending ? (
            <Skeleton className="h-24 w-full" />
          ) : recent.isError ? (
            <ErrorState message={th.loadFailed} onRetry={() => void recent.refetch()} />
          ) : (
            <PageLines pages={recent.data.items} empty={th.cont.empty} />
          )}
        </Block>

        {inbox !== '' && (
          <Block
            id="home-inbox"
            title={th.inbox.title}
            action={
              <Link
                to={urls.folder(inbox)}
                className="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
              >
                <InboxIcon className="size-3" />
                {th.inbox.tidy}
              </Link>
            }
          >
            {inboxCount > 0 && <p className="text-xs text-muted-foreground">{th.inbox.count(inboxCount)}</p>}
            {inboxPages.isPending ? (
              <Skeleton className="h-16 w-full" />
            ) : inboxPages.isError ? (
              <ErrorState message={th.loadFailed} onRetry={() => void inboxPages.refetch()} />
            ) : (
              <PageLines pages={inboxPages.data.items} empty={th.inbox.empty} />
            )}
          </Block>
        )}

        {viewAsId() === null && (
          <Block id="home-favorites" title={th.favorites.title}>
            <HomeFavorites />
          </Block>
        )}
      </div>
    </>
  )
}
