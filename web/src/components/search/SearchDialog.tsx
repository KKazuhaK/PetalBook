import { useOffline } from '@/lib/offline/status'
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { Dialog as DialogPrimitive } from '@base-ui/react/dialog'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { ArrowRight, FileText, Folder, Loader2, Search, X } from 'lucide-react'
import { keys, useSpaceId, useUrls } from '@/api/hooks'
import { spaceApi } from '@/api/client'
import type { PageSummary } from '@/api/types'
import { TagChip } from '@/components/TagChip'
import { Button } from '@/components/ui/button'
import { DialogOverlay, DialogPortal, DialogTitle } from '@/components/ui/dialog'
import { NativeSelect } from '@/components/ui/native-select'
import { absoluteTime, relativeTime } from '@/lib/time'
import { useDebounced } from '@/lib/useDebounced'
import { t } from '@/i18n'
import { FilterChips } from './FilterChips'
import { buildSearchParams, effectiveSort, emptyFilters, filtersToQuery, groupByTime, hasFilters, snippetOf, type SearchFilters, type SearchSort } from './searchModel'

// ⌘K / Ctrl+K, and the sidebar's search box, open this dialog: results as you
// type, filters as chips, and on a wide screen a preview of the highlighted page.

const LIMIT = 40
const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)

interface SearchControl {
  open: () => void
}
const Ctx = createContext<SearchControl | null>(null)

// What the sidebar's search box calls. Outside the provider (a test that mounts
// the sidebar alone) it does nothing.
export function useSearchDialog(): SearchControl {
  return useContext(Ctx) ?? { open: () => {} }
}

// Another dialog is on screen (settings, the quick note): the shortcut must not
// stack the search on it. The search's own dialog is not counted.
const otherDialogOpen = () => [...document.querySelectorAll('[role="dialog"], [role="alertdialog"]')].some((el) => !el.hasAttribute('data-search-dialog'))

export function SearchDialogProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false)
  const show = useCallback(() => setOpen(true), [])
  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.altKey || e.shiftKey || e.key.toLowerCase() !== 'k') return
      // Not while a settings or other dialog has the screen.
      if (otherDialogOpen()) return
      e.preventDefault()
      setOpen(true)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])
  const value = useMemo(() => ({ open: show }), [show])
  return (
    <Ctx.Provider value={value}>
      {children}
      {/* Mounted only while open: its state (text, filters) starts fresh each time and nothing is asked before then. */}
      {open && <SearchDialog onClose={() => setOpen(false)} />}
    </Ctx.Provider>
  )
}

function Highlighted({ text, query }: { text: string | undefined; query: string }) {
  const segments = snippetOf(text, query)
  return (
    <>
      {segments.map((s, i) =>
        s.hit ? (
          <mark key={i} className="rounded-sm bg-brand/20 text-inherit">
            {s.text}
          </mark>
        ) : (
          <span key={i}>{s.text}</span>
        ),
      )}
    </>
  )
}

function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="rounded border bg-muted px-1 font-sans text-[10px] text-muted-foreground">{children}</kbd>
}

const optionId = (id: string) => `search-option-${id}`

export function SearchDialog({ onClose }: { onClose: () => void }) {
  const navigate = useNavigate()
  const urls = useUrls()
  const space = useSpaceId()
  const [text, setText] = useState('')
  const [filters, setFilters] = useState<SearchFilters>(emptyFilters)
  const [sort, setSort] = useState<SearchSort>('relevance')
  const [activeId, setActiveId] = useState<string | null>(null)
  // The reader's clock when the dialog opened; the date presets and the time
  // groups are worked out from it.
  const [now] = useState(() => new Date())
  const input = useRef<HTMLInputElement>(null)
  const list = useRef<HTMLUListElement>(null)

  // The request follows the text a moment after typing stops. A request that is
  // still on its way when the text changes again is cancelled: the query layer
  // aborts the one whose key is no longer wanted.
  const debounced = useDebounced(text, 200)
  const params = useMemo(() => buildSearchParams(debounced, filters, sort, now), [debounced, filters, sort, now])
  const query = useQuery({
    queryKey: [...keys.pages(space), 'search', params],
    queryFn: ({ signal }) => spaceApi(space).listPages({ ...params, limit: LIMIT }, signal),
    enabled: space !== '',
    placeholderData: keepPreviousData,
    staleTime: 5_000,
  })
  const items = useMemo(() => query.data?.items ?? [], [query.data])
  const typing = debounced !== text
  const refreshing = query.isFetching || typing
  const byTime = effectiveSort(debounced, sort) === 'updated'
  const groups = useMemo(() => (byTime ? groupByTime(items, now) : [{ group: null, items }]), [byTime, items, now])

  const activeIndex = Math.max(
    0,
    items.findIndex((p) => p.id === activeId),
  )
  const active: PageSummary | undefined = items[activeIndex]

  useEffect(() => {
    if (active) list.current?.querySelector(`[id="${optionId(active.id)}"]`)?.scrollIntoView?.({ block: 'nearest' })
  }, [active])

  const open = (page: PageSummary, newTab: boolean) => {
    if (newTab) {
      window.open(urls.page(page.id), '_blank', 'noopener')
      return
    }
    onClose()
    navigate(urls.page(page.id))
  }

  const seeAll = () => {
    onClose()
    navigate(`${urls.base}/search?${filtersToQuery(text, filters, now).toString()}`)
  }

  const move = (delta: number) => {
    if (items.length === 0) return
    const next = (activeIndex + delta + items.length) % items.length
    setActiveId(items[next].id)
  }

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    // Keys that confirm an IME candidate belong to the IME.
    if (e.nativeEvent.isComposing) return
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      move(1)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      move(-1)
    } else if (e.key === 'Enter') {
      e.preventDefault()
      if (active) open(active, e.metaKey || e.ctrlKey)
      else if (text.trim()) seeAll()
    }
  }

  const offline = useOffline()
  const filtered = text.trim() !== '' || hasFilters(filters)
  const showSort = text.trim() !== ''

  return (
    <DialogPrimitive.Root open onOpenChange={(o) => !o && onClose()}>
      <DialogPortal>
        <DialogOverlay />
        <DialogPrimitive.Popup
          data-slot="search-dialog"
          data-search-dialog=""
          initialFocus={input}
          className="fixed inset-0 z-50 flex flex-col overflow-hidden bg-popover text-sm text-popover-foreground outline-none duration-100 data-open:animate-in data-open:fade-in-0 data-closed:animate-out data-closed:fade-out-0 md:inset-auto md:top-[10vh] md:left-1/2 md:h-[min(36rem,80dvh)] md:w-[min(58rem,calc(100vw-3rem))] md:-translate-x-1/2 md:rounded-xl md:shadow-2xl md:ring-1 md:ring-foreground/10 md:data-open:zoom-in-95"
        >
          <DialogTitle className="sr-only">{t.search.title}</DialogTitle>
          <div className="flex items-center gap-2 border-b px-3">
            <Search className="size-4 shrink-0 text-muted-foreground" />
            <input
              ref={input}
              type="text"
              role="combobox"
              aria-expanded="true"
              aria-controls="search-results"
              aria-activedescendant={active ? optionId(active.id) : undefined}
              aria-autocomplete="list"
              autoComplete="off"
              spellCheck={false}
              value={text}
              onChange={(e) => setText(e.target.value)}
              onKeyDown={onKeyDown}
              placeholder={t.search.placeholder}
              aria-label={t.search.title}
              className="h-12 min-w-0 flex-1 bg-transparent text-base outline-none placeholder:text-muted-foreground md:text-sm"
            />
            {refreshing && <Loader2 className="size-4 shrink-0 animate-spin text-muted-foreground" aria-label={t.search.loading} />}
            <DialogPrimitive.Close render={<Button variant="ghost" size="icon-sm" className="md:hidden" />}>
              <X />
              <span className="sr-only">{t.common.close}</span>
            </DialogPrimitive.Close>
          </div>
          <div className="flex flex-wrap items-center justify-between gap-2 border-b px-3 py-2">
            <FilterChips filters={filters} onChange={setFilters} />
            {showSort && (
              <div className="w-36">
                <NativeSelect className="h-7 text-xs" aria-label={t.search.sortLabel} value={sort} onChange={(e) => setSort(e.target.value as SearchSort)}>
                  <option value="relevance">{t.search.sorts.relevance}</option>
                  <option value="updated">{t.search.sorts.updated}</option>
                </NativeSelect>
              </div>
            )}
          </div>
          {offline && showSort && (
            <p role="note" className="border-b bg-muted/40 px-3 py-1.5 text-xs text-muted-foreground">
              {t.offline.searchNote}
            </p>
          )}
          <div className="flex min-h-0 flex-1">
            <div className="min-w-0 flex-1 overflow-y-auto overscroll-contain" aria-busy={refreshing}>
              {query.isError ? (
                <div className="flex flex-col items-center gap-3 px-4 py-12 text-center">
                  <p className="text-sm text-muted-foreground">{t.fmt.labelled(t.search.failed, query.error.message)}</p>
                  <Button variant="outline" size="sm" onClick={() => void query.refetch()}>
                    {t.list.retry}
                  </Button>
                </div>
              ) : query.isPending ? (
                <p className="px-4 py-10 text-center text-sm text-muted-foreground">{t.search.loading}</p>
              ) : items.length === 0 ? (
                <p className="px-4 py-12 text-center text-sm text-muted-foreground">{filtered ? t.search.empty : t.search.emptyRecent}</p>
              ) : (
                <ul id="search-results" ref={list} role="listbox" aria-label={filtered ? t.search.results : t.search.recent} className="py-1">
                  {groups.map((g) => (
                    <li key={g.group ?? 'all'} role="presentation">
                      <div role="presentation" className="px-4 pt-2.5 pb-1 text-xs font-medium text-muted-foreground">
                        {g.group ? t.search.groups[g.group] : text.trim() ? t.search.results : t.search.recent}
                      </div>
                      <ul role="presentation">
                        {g.items.map((p) => (
                          <Result key={p.id} page={p} query={debounced} selected={p.id === active?.id} onHover={() => setActiveId(p.id)} onOpen={(newTab) => open(p, newTab)} />
                        ))}
                      </ul>
                    </li>
                  ))}
                </ul>
              )}
            </div>
            <Preview page={active} />
          </div>
          <div className="flex items-center justify-between gap-3 border-t px-3 py-2 text-xs text-muted-foreground">
            <span className="hidden items-center gap-1.5 md:flex">
              <Kbd>↑</Kbd>
              <Kbd>↓</Kbd>
              {t.search.hintMove}
              <Kbd>↵</Kbd>
              {t.search.hintOpen}
              <Kbd>{isMac ? '⌘↵' : 'Ctrl+↵'}</Kbd>
              {t.search.hintNewTab}
              <Kbd>Esc</Kbd>
              {t.search.hintClose}
            </span>
            {filtered && (
              <Button variant="ghost" size="sm" className="ml-auto" onClick={seeAll}>
                {t.search.seeAll}
                <ArrowRight />
              </Button>
            )}
          </div>
        </DialogPrimitive.Popup>
      </DialogPortal>
    </DialogPrimitive.Root>
  )
}

function Result({ page, query, selected, onHover, onOpen }: { page: PageSummary; query: string; selected: boolean; onHover: () => void; onOpen: (newTab: boolean) => void }) {
  const name = page.label || t.list.blankPage
  return (
    <li
      id={optionId(page.id)}
      role="option"
      aria-selected={selected}
      onMouseMove={onHover}
      onClick={(e) => onOpen(e.metaKey || e.ctrlKey)}
      className={`mx-1.5 flex cursor-default items-start gap-3 rounded-lg px-2.5 py-2 ${selected ? 'bg-accent text-accent-foreground' : ''}`}
    >
      <FileText className="mt-0.5 size-[18px] shrink-0 text-muted-foreground" />
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline gap-2">
          <span className={`truncate text-[0.95rem] ${page.untitled ? 'text-muted-foreground' : 'font-medium'}`}>
            <Highlighted text={name} query={query} />
          </span>
          <time dateTime={page.updated} title={absoluteTime(page.updated)} className="ml-auto shrink-0 text-xs whitespace-nowrap text-muted-foreground tabular-nums">
            {relativeTime(page.updated)}
          </time>
        </div>
        <div className="mt-0.5 flex items-center gap-1 text-xs text-muted-foreground">
          <Folder className="size-3 shrink-0" />
          <span className="truncate">{page.folder || t.nav.rootFolder}</span>
        </div>
        {page.excerpt && (
          <div className="mt-1 line-clamp-2 text-[0.8rem] leading-snug break-words text-muted-foreground">
            <Highlighted text={page.excerpt} query={query} />
          </div>
        )}
        {page.tags && page.tags.length > 0 && (
          <div className="mt-1.5 flex flex-wrap gap-1">
            {page.tags.slice(0, 4).map((tag) => (
              <TagChip key={tag} tag={tag} />
            ))}
          </div>
        )}
      </div>
    </li>
  )
}

// What the listing already carries about the highlighted page; nothing more is asked for.
function Preview({ page }: { page: PageSummary | undefined }) {
  return (
    <aside aria-label={t.search.preview} className="hidden w-[22rem] shrink-0 overflow-y-auto overscroll-contain border-l bg-muted/30 p-5 lg:block">
      {page ? (
        <div className="grid gap-3">
          <div>
            <h3 className={`text-base leading-snug font-semibold ${page.untitled ? 'text-muted-foreground' : ''}`}>{page.label || t.list.blankPage}</h3>
            {page.subtitle && <p className="mt-0.5 text-sm text-muted-foreground">{page.subtitle}</p>}
          </div>
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
            <dt className="text-muted-foreground">{t.search.previewFolder}</dt>
            <dd className="truncate">{page.folder || t.nav.rootFolder}</dd>
            <dt className="text-muted-foreground">{t.search.previewUpdated}</dt>
            <dd>{absoluteTime(page.updated)}</dd>
            {page.created && (
              <>
                <dt className="text-muted-foreground">{t.search.previewCreated}</dt>
                <dd>{page.created}</dd>
              </>
            )}
            {page.source && (
              <>
                <dt className="text-muted-foreground">{t.search.previewSource}</dt>
                <dd className="break-all">{page.source}</dd>
              </>
            )}
          </dl>
          {page.tags && page.tags.length > 0 && (
            <div className="flex flex-wrap gap-1">
              {page.tags.map((tag) => (
                <TagChip key={tag} tag={tag} />
              ))}
            </div>
          )}
          {page.excerpt ? <p className="text-[0.85rem] leading-relaxed break-words whitespace-pre-line">{page.excerpt}</p> : <p className="text-sm text-muted-foreground">{t.search.previewEmpty}</p>}
        </div>
      ) : (
        <p className="text-sm text-muted-foreground">{t.search.previewNone}</p>
      )}
    </aside>
  )
}
