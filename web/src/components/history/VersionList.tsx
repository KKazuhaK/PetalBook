import { useEffect, useMemo, useRef } from 'react'
import type { HistoryItem } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/common'
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n/zh-CN'

const dirOf = (path: string) => (path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : '')

// What a version did, in a word, and the path change if it came with one.
// `older` is the version before it, when the list has it.
export function describeVersion(item: HistoryItem, older: HistoryItem | undefined): { kind: string; detail: string | null } {
  const moved = older && older.path !== item.path ? t.history.kindDetail(older.path, item.path) : null
  switch (item.change) {
    case 'add':
      return { kind: t.history.kind.add, detail: null }
    case 'delete':
      return { kind: t.history.kind.delete, detail: null }
    case 'move':
      return { kind: older && dirOf(older.path) === dirOf(item.path) ? t.history.kind.rename : t.history.kind.move, detail: moved ?? item.path }
    default:
      return { kind: t.history.kind.edit, detail: moved }
  }
}

const dayKey = (iso: string) => {
  const d = new Date(iso)
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`
}
function dayLabel(iso: string, now = new Date()): string {
  const d = new Date(iso)
  const days = Math.round((new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime() - new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()) / 86_400_000)
  if (days === 0) return t.history.dayToday
  if (days === 1) return t.history.dayYesterday
  const sameYear = d.getFullYear() === now.getFullYear()
  return t.history.dayOf(d.toLocaleDateString('zh-CN', { year: sameYear ? undefined : 'numeric', month: 'long', day: 'numeric', weekday: 'short' }))
}
const clock = (iso: string) => new Date(iso).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false })

export interface VersionListProps {
  items: HistoryItem[]
  selected: string | null
  onSelect: (commit: string) => void
  hasMore: boolean
  loadingMore: boolean
  onLoadMore: () => void
  onRefresh: () => void
  refreshing: boolean
  // The sheet on a phone has its own title.
  showTitle?: boolean
}

// Versions grouped by day, newest first; more are fetched when the end of the
// list scrolls into view.
export function VersionList({ items, selected, onSelect, hasMore, loadingMore, onLoadMore, onRefresh, refreshing, showTitle = true }: VersionListProps) {
  const groups = useMemo(() => {
    const out: { key: string; label: string; rows: { item: HistoryItem; index: number }[] }[] = []
    items.forEach((item, index) => {
      const key = dayKey(item.time)
      const last = out[out.length - 1]
      if (last && last.key === key) last.rows.push({ item, index })
      else out.push({ key, label: dayLabel(item.time), rows: [{ item, index }] })
    })
    return out
  }, [items])

  const end = useRef<HTMLDivElement>(null)
  // The latest handler, so the observer is made once.
  const more = useRef(onLoadMore)
  useEffect(() => {
    more.current = onLoadMore
  }, [onLoadMore])
  useEffect(() => {
    const el = end.current
    if (!el || !hasMore || typeof IntersectionObserver === 'undefined') return
    const io = new IntersectionObserver((entries) => entries.some((e) => e.isIntersecting) && more.current(), { rootMargin: '120px' })
    io.observe(el)
    return () => io.disconnect()
  }, [hasMore, items.length])

  return (
    <div className="flex flex-col">
      <div className="flex items-center justify-between gap-2 px-3 py-2">
        {showTitle ? <h2 className="text-sm font-medium">{t.history.list}</h2> : <span />}
        <Button variant="ghost" size="xs" onClick={onRefresh} disabled={refreshing} title={t.history.refreshHint}>
          {refreshing ? <Spinner className="size-3" /> : null}
          {t.history.refresh}
        </Button>
      </div>
      <ul className="flex flex-col pb-4">
        {groups.map((g) => (
          <li key={g.key} className="flex flex-col">
            <h3 className="sticky top-0 z-10 bg-background/95 px-3 py-1 text-xs font-medium text-muted-foreground backdrop-blur">{g.label}</h3>
            <ul>
              {g.rows.map(({ item, index }) => {
                const { kind, detail } = describeVersion(item, items[index + 1])
                const on = item.commit === selected
                return (
                  <li key={item.commit}>
                    <button
                      type="button"
                      onClick={() => onSelect(item.commit)}
                      aria-current={on ? 'true' : undefined}
                      title={absoluteTime(item.time)}
                      className={`flex w-full flex-col gap-0.5 px-3 py-2 text-left outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset ${on ? 'bg-muted' : ''}`}
                    >
                      <span className="flex items-baseline gap-2 text-sm">
                        <time dateTime={item.time} className="font-medium tabular-nums">
                          {clock(item.time)}
                        </time>
                        <span>{kind}</span>
                        {index === 0 && <Badge variant="secondary">{t.history.latest}</Badge>}
                        {on && <span className="sr-only">{t.history.selected}</span>}
                        <span className="ml-auto min-w-0 truncate text-xs text-muted-foreground">{item.author}</span>
                      </span>
                      {detail && <span className="truncate text-xs text-muted-foreground">{detail}</span>}
                    </button>
                  </li>
                )
              })}
            </ul>
          </li>
        ))}
      </ul>
      <div ref={end} className="px-3 pb-6 text-center text-xs text-muted-foreground">
        {hasMore ? (
          <Button variant="outline" size="sm" onClick={onLoadMore} disabled={loadingMore}>
            {loadingMore ? t.history.loadingMore : t.history.loadMore}
          </Button>
        ) : (
          items.length > 0 && t.history.allLoaded
        )}
      </div>
    </div>
  )
}
