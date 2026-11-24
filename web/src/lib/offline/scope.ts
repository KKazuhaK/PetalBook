// Which pages of a catalogue have their text kept on the device. Pure.

import type { CatalogEntry } from '@/api/types'
import { RECENT_KEPT, type Favourites, type Opened, type OfflineConfig } from './store'

const under = (folder: string, root: string) => folder === root || folder.startsWith(root + '/')

export interface Wanted {
  // Kept whatever else happens: favourites, the inbox, what was chosen.
  pinned: string[]
  // Opened lately, newest first; the first to go when the device is full.
  recent: string[]
}

export function wantedText(catalog: CatalogEntry[], cfg: OfflineConfig, inbox: string, favourites: Favourites, opened: Opened[]): Wanted {
  if (cfg.mode !== 'text') return { pinned: [], recent: [] }
  const favPages = new Set(favourites.pages)
  const pinned: string[] = []
  const isPinned = new Set<string>()
  const byId = new Set<string>()
  for (const e of catalog) {
    byId.add(e.id)
    const keep =
      (cfg.favourites && (favPages.has(e.id) || favourites.folders.some((f) => under(e.folder, f)))) ||
      (cfg.inbox && inbox !== '' && under(e.folder, inbox)) ||
      cfg.folders.some((f) => under(e.folder, f)) ||
      (cfg.tags.length > 0 && e.tags.some((t) => cfg.tags.includes(t)))
    if (keep) {
      pinned.push(e.id)
      isPinned.add(e.id)
    }
  }
  const recent = cfg.recent
    ? opened
        .filter((o) => byId.has(o.id) && !isPinned.has(o.id))
        .slice(0, RECENT_KEPT)
        .map((o) => o.id)
    : []
  return { pinned, recent }
}
