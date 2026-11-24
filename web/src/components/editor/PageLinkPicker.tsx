import { useEffect, useId, useLayoutEffect, useMemo, useState, useSyncExternalStore } from 'react'
import { createPortal } from 'react-dom'
import type { Editor } from '@tiptap/core'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { FileText } from 'lucide-react'
import { spaceApi } from '@/api/client'
import { keys } from '@/api/hooks'
import type { PageSummary } from '@/api/types'
import { useDebounced } from '@/lib/useDebounced'
import { cn } from '@/lib/utils'
import { t } from '@/i18n'
import { insertPageLink, pickerOf } from './pageLinkPicker'

const LIMIT = 8

// The list that opens at the cursor after `[[`: the pages whose title has what was typed (the
// recent ones before anything is), chosen with the arrow keys and Enter, or the pointer.
export function PageLinkPicker({
  editor,
  space,
  pageId,
  getFolder,
}: {
  editor: Editor
  space: string
  pageId: string
  getFolder: () => string
}) {
  const storage = pickerOf(editor)
  const trigger = useSyncExternalStore(
    (cb) => {
      storage.listeners.add(cb)
      return () => void storage.listeners.delete(cb)
    },
    () => storage.trigger,
  )
  return trigger ? (
    <Open
      editor={editor}
      space={space}
      pageId={pageId}
      getFolder={getFolder}
      from={trigger.from}
      to={trigger.to}
      query={trigger.query}
    />
  ) : null
}

const labelOf = (p: PageSummary) => p.title || p.label || t.editor.untitled

function Open({
  editor,
  space,
  pageId,
  getFolder,
  from,
  to,
  query,
}: {
  editor: Editor
  space: string
  pageId: string
  getFolder: () => string
  from: number
  to: number
  query: string
}) {
  const storage = pickerOf(editor)
  const q = useDebounced(query.trim(), 120)
  const found = useQuery({
    queryKey: [...keys.pages(space), 'link-picker', q],
    queryFn: ({ signal }) =>
      spaceApi(space).listPages({ ...(q ? { q, in: 'title' as const } : {}), limit: LIMIT + 1 }, signal),
    enabled: space !== '',
    placeholderData: keepPreviousData,
    staleTime: 5_000,
  })
  // The page itself is not a target.
  const items = useMemo(
    () => (found.data?.items ?? []).filter((p) => p.id !== pageId).slice(0, LIMIT),
    [found.data, pageId],
  )
  const [active, setActive] = useState(0)
  const [prevQuery, setPrevQuery] = useState(q)
  if (prevQuery !== q) {
    setPrevQuery(q)
    setActive(0)
  }
  const index = Math.min(active, Math.max(items.length - 1, 0))
  const base = useId()

  const choose = (p: PageSummary) =>
    insertPageLink(editor, { from, to }, getFolder(), { id: p.id, path: p.path, label: labelOf(p) })

  // The keys go to the list while it is open.
  useEffect(() => {
    storage.setKeyHandler((e) => {
      if (e.ctrlKey || e.metaKey || e.altKey) return false
      if (e.key === 'Escape') {
        storage.dismiss()
        return true
      }
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        if (items.length === 0) return true
        setActive(
          (i) => (Math.min(i, items.length - 1) + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length,
        )
        return true
      }
      if (e.key === 'Enter' || e.key === 'Tab') {
        const p = items[index]
        // Nothing to choose yet: Enter is not swallowed, so the line is not lost either.
        if (!p) return e.key === 'Tab'
        choose(p)
        return true
      }
      return false
    })
    return () => storage.setKeyHandler(null)
  })

  // The editor keeps the focus; the list says which row is chosen.
  const rowId = (i: number) => `${base}-row-${i}`
  useEffect(() => {
    const dom = editor.view.dom
    dom.setAttribute('aria-activedescendant', items.length > 0 ? rowId(index) : '')
    if (items.length === 0) dom.removeAttribute('aria-activedescendant')
    return () => dom.removeAttribute('aria-activedescendant')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editor, index, items.length, base])

  // Below the line the cursor is on; drawn again when the page scrolls or resizes.
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null)
  useLayoutEffect(() => {
    const place = () => {
      try {
        const c = editor.view.coordsAtPos(to)
        const width = 352
        setPos({ left: Math.max(8, Math.min(c.left, window.innerWidth - width - 8)), top: c.bottom + 6 })
      } catch {
        setPos(null)
      }
    }
    place()
    window.addEventListener('scroll', place, true)
    window.addEventListener('resize', place)
    return () => {
      window.removeEventListener('scroll', place, true)
      window.removeEventListener('resize', place)
    }
  }, [editor, to])

  const listId = `${base}-list`
  return createPortal(
    <div
      className="fixed z-50 w-[min(22rem,calc(100vw-1rem))] overflow-hidden rounded-lg bg-popover text-popover-foreground shadow-md ring-1 ring-foreground/10"
      style={pos ? { left: pos.left, top: pos.top } : { left: 8, top: 56 }}
      // Pressing on the list must not take the focus from the text.
      onMouseDown={(e) => e.preventDefault()}
      data-testid="page-link-picker"
    >
      <div id={listId} role="listbox" aria-label={t.pageLinks.pickerLabel} className="max-h-72 overflow-y-auto p-1">
        {items.map((p, i) => (
          <div
            key={p.id}
            id={rowId(i)}
            role="option"
            aria-selected={i === index}
            className={cn(
              'flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm',
              i === index && 'bg-accent text-accent-foreground',
            )}
            onMouseMove={() => i !== index && setActive(i)}
            onClick={() => choose(p)}
          >
            <FileText className="size-4 shrink-0 text-muted-foreground" />
            <span className="min-w-0 flex-1 truncate">{labelOf(p)}</span>
            {p.folder && (
              <span className="max-w-[40%] shrink-0 truncate text-xs text-muted-foreground">{p.folder}</span>
            )}
          </div>
        ))}
        {items.length === 0 && (
          <div role="status" className="px-2 py-2 text-sm text-muted-foreground">
            {found.isError
              ? t.pageLinks.pickerError
              : found.isFetching
                ? t.pageLinks.pickerLoading
                : t.pageLinks.pickerEmpty}
          </div>
        )}
      </div>
      <div className="border-t px-2 py-1 text-xs text-muted-foreground">{t.pageLinks.pickerHint}</div>
    </div>,
    document.body,
  )
}
