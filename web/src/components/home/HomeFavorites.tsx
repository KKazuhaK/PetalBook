import { Link } from 'react-router'
import { favoriteKey, useCanFavorite, useUrls } from '@/api/hooks'
import type { Favorite } from '@/api/types'
import { FavoriteActions } from '@/components/favorites/FavoriteActions'
import { KIND_ICON } from '@/components/favorites/SidebarFavorites'
import { useFavoriteList } from '@/components/favorites/useFavoriteList'
import { Skeleton } from '@/components/ui/skeleton'
import { favoriteHref, favoriteName } from '@/lib/favorites'
import { t } from '@/i18n'
import { th } from '@/i18n/home'

// The person's own favourites, in their order (the same list and the same moves as in the sidebar).
export function HomeFavorites() {
  const allowed = useCanFavorite()
  const { list, items, moveBy, clear, dragProps, keyProps } = useFavoriteList(allowed)
  const urls = useUrls()
  // Not a member: no star anywhere, so no list and no hint about stars.
  if (!allowed) return null
  if (list.isPending) return <Skeleton className="h-16 w-full" />
  if (items.length === 0) return <p className="text-sm text-muted-foreground">{th.favorites.empty}</p>
  return (
    <ul className="grid gap-1 sm:grid-cols-2" aria-label={t.fav.section}>
      {items.map((f, i) => (
        <Item key={favoriteKey(f)} f={f} index={i} count={items.length} href={favoriteHref(f, urls)} moveBy={moveBy} clear={clear} drag={dragProps(favoriteKey(f))} keys={keyProps(favoriteKey(f))} />
      ))}
    </ul>
  )
}

function Item({ f, index, count, href, moveBy, clear, drag, keys }: { f: Favorite; index: number; count: number; href: string | null; moveBy: (key: string, delta: number) => void; clear: (f: Favorite) => void; drag: object; keys: object }) {
  const Icon = KIND_ICON[f.kind]
  const name = favoriteName(f)
  const body = (
    <>
      <Icon className="size-4 shrink-0 text-muted-foreground" />
      <span className="truncate">{name}</span>
      {f.kind === 'view' && f.of && !f.missing && <span className="truncate text-xs text-muted-foreground">{f.of}</span>}
      {f.missing && <span className="shrink-0 rounded bg-muted px-1 text-[0.65rem] text-muted-foreground">{t.fav.missing}</span>}
    </>
  )
  const row = 'flex min-w-0 flex-1 items-center gap-2 rounded-md px-2 py-1.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50'
  return (
    <li className="group/fav flex items-center rounded-lg border bg-card/50 hover:bg-muted/50" data-favorite={favoriteKey(f)} {...drag}>
      {href ? (
        <Link to={href} className={row} {...keys}>
          {body}
        </Link>
      ) : (
        <span role="link" aria-disabled="true" tabIndex={0} title={t.fav.missingHelp} className={`${row} text-muted-foreground`} {...keys}>
          {body}
        </span>
      )}
      <FavoriteActions favorite={f} index={index} count={count} onMove={moveBy} onClear={clear} />
    </li>
  )
}
