import { useEffect, useRef } from 'react'
import { Link } from 'react-router'
import { FileText, Loader2, X } from 'lucide-react'
import { useBacklinks, useUrls } from '@/api/hooks'
import { ErrorState } from '@/components/common'
import { Button } from '@/components/ui/button'
import { t } from '@/i18n'

// The pages that link to this one, on the right of the page: the page's name, the paragraph the link is in, and
// twenty at a time. It is its own chunk and asks for nothing until it is opened.
//
// On a narrow screen (`overlay`) it covers the page: a backdrop behind it closes it, so does Esc, and the focus
// goes into it while it is open and back to where it was when it closes.
export default function BacklinksPanel({
  pageId,
  onClose,
  overlay = false,
}: {
  pageId: string
  onClose: () => void
  overlay?: boolean
}) {
  const urls = useUrls()
  const q = useBacklinks(pageId)
  const items = q.data?.pages.flatMap((p) => p.items) ?? []
  const total = q.data?.pages[0]?.total ?? 0
  const ref = useRef<HTMLElement>(null)
  const close = useRef(onClose)
  useEffect(() => {
    close.current = onClose
  })
  useEffect(() => {
    if (!overlay) return
    const before = document.activeElement instanceof HTMLElement ? document.activeElement : null
    ref.current?.focus()
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || e.isComposing) return
      e.preventDefault()
      close.current()
    }
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('keydown', onKey)
      if (before?.isConnected) before.focus()
    }
  }, [overlay])
  return (
    <>
      {overlay && (
        <div
          data-testid="backlinks-backdrop"
          aria-hidden="true"
          onClick={onClose}
          className="fixed inset-0 top-14 z-30 bg-black/30"
        />
      )}
      <aside
        ref={ref}
        tabIndex={-1}
        aria-label={t.pageLinks.backlinks}
        className="fixed top-14 right-0 bottom-0 z-30 flex w-80 max-w-[calc(100%-3rem)] flex-col border-l bg-background shadow-lg outline-none lg:shadow-none"
      >
        <header className="flex shrink-0 items-center gap-2 border-b px-3 py-2">
          <h2 className="flex-1 text-sm font-medium">{t.pageLinks.backlinks}</h2>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            onClick={onClose}
            title={t.pageLinks.backlinksClose}
            aria-label={t.pageLinks.backlinksClose}
          >
            <X />
          </Button>
        </header>
        <div className="min-h-0 flex-1 overflow-y-auto px-3 py-2">
          {q.isPending ? (
            <div role="status" className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" />
              {t.pageLinks.backlinksLoading}
            </div>
          ) : q.isError && items.length === 0 ? (
            <ErrorState
              message={t.fmt.labelled(t.pageLinks.backlinksError, q.error.message)}
              onRetry={() => void q.refetch()}
            />
          ) : items.length === 0 ? (
            <p className="py-6 text-sm text-muted-foreground">{t.pageLinks.backlinksEmpty}</p>
          ) : (
            <>
              <p className="pb-2 text-xs text-muted-foreground" data-testid="backlinks-total">
                {t.pageLinks.backlinksTotal(total)}
              </p>
              <ul className="flex flex-col gap-3">
                {items.map((b) => (
                  <li key={b.id}>
                    <Link
                      to={urls.page(b.id)}
                      className="flex items-center gap-1.5 text-sm font-medium hover:underline"
                    >
                      <FileText className="size-3.5 shrink-0 text-muted-foreground" />
                      <span className="truncate">{b.untitled ? b.title || t.editor.untitled : b.title}</span>
                    </Link>
                    <p className="mt-0.5 line-clamp-4 text-xs text-muted-foreground">
                      {b.excerpt || t.pageLinks.backlinksNoExcerpt}
                    </p>
                  </li>
                ))}
              </ul>
              {q.isError && (
                <ErrorState
                  message={t.fmt.labelled(t.pageLinks.backlinksError, q.error.message)}
                  onRetry={() => void q.fetchNextPage()}
                />
              )}
              {q.hasNextPage && (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="mt-3 w-full"
                  disabled={q.isFetchingNextPage}
                  onClick={() => void q.fetchNextPage()}
                >
                  {q.isFetchingNextPage ? t.pageLinks.backlinksLoading : t.pageLinks.backlinksMore}
                </Button>
              )}
            </>
          )}
        </div>
      </aside>
    </>
  )
}
