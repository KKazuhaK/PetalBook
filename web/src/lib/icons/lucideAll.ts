// The whole Lucide set and its tags, as one chunk of its own: nothing imports this module statically, so it is
// fetched (from this server, never from the web) only when an icon outside the core set has to be drawn or the picker
// opens. The tags are Lucide's own (lucide-static 1.78 `tags.json`, kept as iconTags.json).
import { icons } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import tags from './iconTags.json'
import { ZH_ALIASES } from './zhAliases'
import { kebab } from './check'

export const ALL_ICONS: Record<string, LucideIcon> = Object.fromEntries(
  Object.entries(icons).map(([name, icon]) => [kebab(name), icon]),
)
export const ALL_NAMES: string[] = Object.keys(ALL_ICONS).sort()
// What a name is searched by: itself (with the dashes as spaces), Lucide's tags and the Chinese aliases.
export const SEARCH_TEXT: Record<string, string> = Object.fromEntries(
  ALL_NAMES.map((n) => [
    n,
    `${n.replace(/-/g, ' ')} ${(tags as Record<string, string>)[n] ?? ''} ${ZH_ALIASES[n] ?? ''}`.toLowerCase(),
  ]),
)
