import { ArrowDown, ArrowUp, X } from 'lucide-react'
import type { Favorite } from '@/api/types'
import { favoriteKey } from '@/api/hooks'
import { favoriteName } from '@/lib/favorites'
import { t } from '@/i18n'

const btn =
  'flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground outline-none transition-opacity hover:bg-muted hover:text-foreground focus-visible:opacity-100 focus-visible:ring-2 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-30'

// The buttons at the end of a favourite: up, down, and (for one that is gone) clear. Up and down are the keyboard's
// way of ordering the list, so they are reachable by tabbing, and only drawn faintly until the row is in use.
export function FavoriteActions({
  favorite,
  index,
  count,
  onMove,
  onClear,
  reveal = true,
}: {
  favorite: Favorite
  index: number
  count: number
  onMove: (key: string, delta: number) => void
  onClear: (f: Favorite) => void
  reveal?: boolean
}) {
  const key = favoriteKey(favorite)
  const name = favoriteName(favorite)
  const hide = reveal
    ? 'opacity-0 group-focus-within/fav:opacity-100 group-hover/fav:opacity-100 pointer-coarse:opacity-100'
    : ''
  return (
    <>
      <button
        type="button"
        data-fav-action="up"
        aria-label={t.fav.moveUpLabel(name)}
        title={t.fav.moveUp}
        disabled={index === 0}
        onClick={() => onMove(key, -1)}
        className={`${btn} ${hide}`}
      >
        <ArrowUp className="size-3.5" />
      </button>
      <button
        type="button"
        data-fav-action="down"
        aria-label={t.fav.moveDownLabel(name)}
        title={t.fav.moveDown}
        disabled={index === count - 1}
        onClick={() => onMove(key, 1)}
        className={`${btn} ${hide}`}
      >
        <ArrowDown className="size-3.5" />
      </button>
      {favorite.missing && (
        <button
          type="button"
          data-fav-action="clear"
          aria-label={t.fav.clearLabel(name)}
          title={t.fav.clear}
          onClick={() => onClear(favorite)}
          className={btn}
        >
          <X className="size-3.5" />
        </button>
      )}
    </>
  )
}
