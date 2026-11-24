import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router'
import { PenLine } from 'lucide-react'
import { useReadingPages, useUrls, useSpaceId } from '@/api/hooks'
import type { ListParams, PageSummary } from '@/api/types'
import { ErrorState, Spinner } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { cardTitleLine } from '@/lib/listView'
import { t } from '@/i18n'
import { tb } from '@/i18n/boards'
import { StaticBody } from './StaticBody'

// Continuous reading: the pages of a folder or a tag, in the order of the view, one after the other as one long
// text. They come with their texts, twenty at a time as the person scrolls. Only the pages near the window are drawn
// in full; one that is far off is a blank of the height it had, so hundreds of pages cost the screen's worth.

// How far from the window (in pixels, above and below) a page is still drawn.
export const NEAR_PX = 1500
// What a page that was never drawn is guessed to measure.
const GUESS_HEIGHT = 480
// The first pages are drawn before the browser has said where anything is.
const FIRST = 3

const HAS_OBSERVER = () => typeof IntersectionObserver !== 'undefined'

// Whether an element is within `margin` pixels of the window. Where there is no way to know (no
// IntersectionObserver) it is `fallback`.
// `watch` is anything whose change means the element is to be looked at afresh: it was put into the page, or
// something around it moved. The observer starts again, which also answers with where the element is now (a browser
// only says when it crosses the margin, which an element that stays inside it never does).
export function useNear(
  ref: React.RefObject<Element | null>,
  margin: number,
  fallback: boolean,
  watch: unknown = true,
): boolean {
  const [near, setNear] = useState(fallback)
  useEffect(() => {
    const el = ref.current
    if (!el || !HAS_OBSERVER()) return
    // Until the observer has answered the element is taken to be where it was not known to be.
    setNear(fallback)
    const io = new IntersectionObserver(
      (entries) => {
        const last = entries[entries.length - 1]
        if (last) setNear(last.isIntersecting)
      },
      { rootMargin: `${margin}px 0px ${margin}px 0px` },
    )
    io.observe(el)
    return () => io.disconnect()
  }, [ref, margin, fallback, watch])
  return near
}

type Params = Omit<ListParams, 'cursor' | 'limit' | 'body'>

export default function ReadingView({ params }: { params: Params }) {
  const query = useReadingPages(params)
  const { hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage } = query
  const items = useMemo(() => query.data?.pages.flatMap((p) => p.items) ?? [], [query.data])

  // Reaching the end of what is loaded asks for the next batch. A failed batch is not asked for again by itself.
  const end = useRef<HTMLDivElement>(null)
  // Asked again whenever a batch has come: the end of the list has moved away, or it has not.
  const atEnd = useNear(end, NEAR_PX, false, items.length)
  useEffect(() => {
    if (atEnd && hasNextPage && !isFetchingNextPage && !isFetchNextPageError) void fetchNextPage()
  }, [atEnd, hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage])

  if (query.isPending)
    return (
      <div className="space-y-3" aria-busy="true" data-slot="reading-view">
        <Skeleton className="h-8 w-1/2" />
        <Skeleton className="h-40 w-full" />
      </div>
    )
  if (query.isError && items.length === 0)
    return (
      <ErrorState
        message={t.fmt.labelled(tb.reading.loadFailed, query.error.message)}
        onRetry={() => void query.refetch()}
      />
    )
  if (items.length === 0) return <p className="py-16 text-center text-sm text-muted-foreground">{tb.reading.empty}</p>

  return (
    <div data-slot="reading-view" aria-label={tb.reading.label}>
      <div className="divide-y divide-border/60">
        {items.map((p, i) => (
          <ReadingItem key={p.id} page={p} index={i} />
        ))}
      </div>
      <div ref={end} className="flex flex-col items-center gap-2 py-6 text-xs text-muted-foreground">
        {hasNextPage ? (
          <>
            <Button variant="outline" size="sm" onClick={() => void fetchNextPage()} disabled={isFetchingNextPage}>
              {isFetchingNextPage ? <Spinner /> : null}
              {isFetchingNextPage ? tb.reading.loading : tb.reading.loadMore}
            </Button>
            <span>{tb.reading.loaded(items.length)}</span>
          </>
        ) : (
          <span>{tb.reading.allLoaded(items.length)}</span>
        )}
        {isFetchNextPageError && <ErrorState message={query.error.message} onRetry={() => void fetchNextPage()} />}
      </div>
    </div>
  )
}

// One page. Far from the window it is an empty box of the height it last had (so the scroll bar and the position do
// not jump), near it the whole text. The text is parsed when the page comes near, and dropped when it goes away.
function ReadingItem({ page, index }: { page: PageSummary; index: number }) {
  const urls = useUrls()
  const space = useSpaceId()
  const box = useRef<HTMLElement>(null)
  const near = useNear(box, NEAR_PX, index < FIRST || !HAS_OBSERVER())
  // The height the page had while it was drawn.
  const [height, setHeight] = useState(0)

  // The height of the page while it is drawn, kept for when it is not.
  useLayoutEffect(() => {
    const el = box.current
    if (!el || !near) return
    if (el.offsetHeight > 0) setHeight(el.offsetHeight)
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(() => {
      if (el.offsetHeight > 0) setHeight(el.offsetHeight)
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [near])

  if (!near)
    return (
      <article
        ref={box}
        data-reading-id={page.id}
        data-drawn="false"
        aria-hidden
        style={{ height: height || GUESS_HEIGHT }}
      />
    )

  const line = cardTitleLine(page)
  const name = page.title || page.label || t.editor.untitled
  return (
    <article ref={box} data-reading-id={page.id} data-drawn="true" className="relative py-8 first:pt-2">
      {line ? (
        <h2 className="mb-4 text-xl leading-snug font-semibold">
          <Link
            to={urls.page(page.id)}
            aria-label={tb.reading.edit(name)}
            className="outline-none hover:underline focus-visible:underline"
          >
            {line}
          </Link>
        </h2>
      ) : (
        // An untitled page is its text; the way into the editor is a small mark.
        <Link
          to={urls.page(page.id)}
          aria-label={tb.reading.edit(name)}
          title={tb.reading.edit(name)}
          className="absolute top-8 right-0 rounded-md p-1 text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
        >
          <PenLine className="size-4" />
        </Link>
      )}
      {page.body && page.body.trim() !== '' ? (
        <StaticBody markdown={page.body} space={space} folder={page.folder} />
      ) : (
        <p className="text-sm text-muted-foreground">{tb.reading.emptyBody}</p>
      )}
    </article>
  )
}
