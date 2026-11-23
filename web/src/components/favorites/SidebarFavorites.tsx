import { useState } from 'react'
import { NavLink, useLocation } from 'react-router'
import { ChevronRight, FileText, Folder, Star, Table2 } from 'lucide-react'
import { favoriteKey, useCanFavorite, useUrls } from '@/api/hooks'
import type { Favorite } from '@/api/types'
import { SidebarGroup, SidebarGroupContent, SidebarGroupLabel, SidebarMenu, SidebarMenuButton, SidebarMenuItem } from '@/components/ui/sidebar'
import { favoriteHref, favoriteName } from '@/lib/favorites'
import { t } from '@/i18n'
import { FavoriteActions } from './FavoriteActions'
import { useFavoriteList } from './useFavoriteList'

const OPEN_KEY = 'pb.favorites.open'

export const KIND_ICON = { page: FileText, folder: Folder, view: Table2 } as const

function loadOpen(): boolean {
  try {
    return localStorage.getItem(OPEN_KEY) !== 'closed'
  } catch {
    return true
  }
}

// The sidebar's "Favourites": the same list as the home page's block, in the person's own order. Drag a row, or use
// Alt+Up and Alt+Down (or the buttons that appear on it) to move it; one that is gone says so and has a clear button.
// Nothing is drawn until there is a favourite, and the group folds away.
export function SidebarFavorites() {
  const allowed = useCanFavorite()
  const [open, setOpen] = useState(loadOpen)
  const urls = useUrls()
  const { pathname, search } = useLocation()
  const { items, moveBy, clear, dragProps, keyProps } = useFavoriteList()
  if (!allowed || items.length === 0) return null
  const toggle = () =>
    setOpen((o) => {
      try {
        localStorage.setItem(OPEN_KEY, o ? 'closed' : 'open')
      } catch {
        // Not kept: it folds for this visit only.
      }
      return !o
    })
  return (
    <SidebarGroup data-slot="sidebar-favorites">
      <SidebarGroupLabel render={<button type="button" aria-expanded={open} aria-label={open ? t.fav.collapse : t.fav.expand} onClick={toggle} />} className="cursor-pointer hover:bg-sidebar-accent/70 hover:text-sidebar-foreground">
        <Star className="mr-1.5 size-3.5" />
        {t.fav.title}
        <ChevronRight className={`ml-auto size-3.5 transition-transform ${open ? 'rotate-90' : ''}`} />
      </SidebarGroupLabel>
      {open && (
        <SidebarGroupContent className="max-h-[min(16rem,35vh)] overflow-y-auto overscroll-contain">
          <SidebarMenu aria-label={t.fav.section}>
            {items.map((f, i) => (
              <Row key={favoriteKey(f)} f={f} index={i} count={items.length} href={favoriteHref(f, urls)} active={favoriteHref(f, urls) === pathname + search} moveBy={moveBy} clear={clear} drag={dragProps(favoriteKey(f))} keys={keyProps(favoriteKey(f))} />
            ))}
          </SidebarMenu>
        </SidebarGroupContent>
      )}
    </SidebarGroup>
  )
}

function Row({ f, index, count, href, active, moveBy, clear, drag, keys }: { f: Favorite; index: number; count: number; href: string | null; active: boolean; moveBy: (key: string, delta: number) => void; clear: (f: Favorite) => void; drag: object; keys: object }) {
  const Icon = KIND_ICON[f.kind]
  const name = favoriteName(f)
  return (
    <SidebarMenuItem className="group/fav" data-favorite={favoriteKey(f)} {...drag}>
      {href ? (
        <SidebarMenuButton isActive={active} render={<NavLink to={href} {...keys} />} title={f.kind === 'view' && f.of ? t.fav.view(f.of) : name} className="pr-[4.5rem]">
          <Icon />
          <span className="truncate">{name}</span>
        </SidebarMenuButton>
      ) : (
        <SidebarMenuButton render={<span role="link" aria-disabled="true" tabIndex={0} {...keys} />} title={t.fav.missingHelp} className="pr-[4.5rem] text-muted-foreground">
          <Icon />
          <span className="truncate">{name}</span>
          <span className="shrink-0 rounded bg-muted px-1 text-[0.65rem]">{t.fav.missing}</span>
        </SidebarMenuButton>
      )}
      <div className="absolute top-1/2 right-1 flex -translate-y-1/2 items-center">
        <FavoriteActions favorite={f} index={index} count={count} onMove={moveBy} onClear={clear} />
      </div>
    </SidebarMenuItem>
  )
}
