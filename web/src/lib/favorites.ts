// What a favourite is, in the app's terms: where it goes, what it is called, and the order moves.

import type { Favorite, FavoriteRef } from '@/api/types'
import type { SpaceUrls } from './urls'
import { t } from '@/i18n'

const VIEW = /^(folder|tag):(.*)#([^#]+)$/s

// Where the favourite leads; null for one that is gone.
export function favoriteHref(f: Favorite, urls: SpaceUrls): string | null {
  if (f.missing) return null
  if (f.kind === 'page') return urls.page(f.target)
  if (f.kind === 'folder') return urls.folder(f.target)
  const m = VIEW.exec(f.target)
  if (!m) return null
  return `${m[1] === 'folder' ? urls.folder(m[2]) : urls.tag(m[2])}?view=${encodeURIComponent(m[3])}`
}

// The address of a saved view as a favourite names it.
export const viewTarget = (scope: { folder: string } | { tag: string }, id: string): string => ('folder' in scope ? `folder:${scope.folder}#${id}` : `tag:${scope.tag}#${id}`)

export function favoriteName(f: Favorite): string {
  if (f.name) return f.name
  if (f.kind === 'folder') return f.target.split('/').pop() || f.target
  return t.fav.unnamed
}

export const sameRef = (a: FavoriteRef, b: FavoriteRef) => a.kind === b.kind && a.target === b.target

// The list with `from` moved to where `to` is (both indices); the refs in their new order.
export function moved(items: readonly Favorite[], from: number, to: number): FavoriteRef[] | null {
  if (from === to || from < 0 || to < 0 || from >= items.length || to >= items.length) return null
  const next = [...items]
  const [item] = next.splice(from, 1)
  next.splice(to, 0, item)
  return next.map(({ kind, target }) => ({ kind, target }))
}
