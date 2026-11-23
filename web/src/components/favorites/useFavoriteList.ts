import { useCallback, useMemo, useRef } from 'react'
import { toast } from 'sonner'
import { favoriteKey, useFavorites, useOrderFavorites, useRemoveFavorite } from '@/api/hooks'
import type { Favorite } from '@/api/types'
import { moved } from '@/lib/favorites'
import { t } from '@/i18n'

// What the favourites of the space (the sidebar's group and the home page's block) share: the list, moving one
// (by dragging or by the keyboard) and clearing one that is gone.
export function useFavoriteList() {
  const list = useFavorites()
  const order = useOrderFavorites()
  const remove = useRemoveFavorite()
  const items: Favorite[] = useMemo(() => list.data?.items ?? [], [list.data])
  const drag = useRef<string | null>(null)

  const reorder = useCallback(
    (from: number, to: number) => {
      const next = moved(items, from, to)
      if (!next) return
      order.mutate(next, { onError: () => void toast.error(t.fav.reorderFailed) })
    },
    [items, order],
  )
  const moveBy = (key: string, delta: number) => {
    const i = items.findIndex((f) => favoriteKey(f) === key)
    if (i >= 0) reorder(i, i + delta)
  }
  const dropOn = (key: string) => {
    const from = drag.current
    drag.current = null
    if (!from || from === key) return
    reorder(
      items.findIndex((f) => favoriteKey(f) === from),
      items.findIndex((f) => favoriteKey(f) === key),
    )
  }
  const clear = (f: Favorite) => remove.mutate(f, { onError: () => void toast.error(t.fav.failed) })

  // Attributes that make a row draggable and a place to drop.
  const dragProps = (key: string) => ({
    draggable: true,
    onDragStart: (e: React.DragEvent) => {
      drag.current = key
      e.dataTransfer.effectAllowed = 'move'
      e.dataTransfer.setData('application/x-petalbook-favorite', key)
    },
    onDragOver: (e: React.DragEvent) => {
      if (drag.current && drag.current !== key) e.preventDefault()
    },
    onDrop: (e: React.DragEvent) => {
      if (!drag.current) return
      e.preventDefault()
      dropOn(key)
    },
    onDragEnd: () => (drag.current = null),
  })

  // Alt+Up and Alt+Down on a row move it, for people who do not drag.
  const keyProps = (key: string) => ({
    onKeyDown: (e: React.KeyboardEvent) => {
      if (!e.altKey || e.metaKey || e.ctrlKey || e.shiftKey || (e.key !== 'ArrowUp' && e.key !== 'ArrowDown')) return
      e.preventDefault()
      moveBy(key, e.key === 'ArrowUp' ? -1 : 1)
    },
  })

  return { list, items, moveBy, clear, dragProps, keyProps, busy: order.isPending }
}
