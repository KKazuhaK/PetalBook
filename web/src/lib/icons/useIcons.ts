import { useEffect, useState } from 'react'
import type { LucideIcon } from 'lucide-react'
import { CORE_ICONS } from './coreIcons'

type All = typeof import('./lucideAll')
let loaded: All | null = null
let loading: Promise<All> | null = null

// The whole set, loaded once as a chunk of its own, from this server.
export function loadAllIcons(): Promise<All> {
  loading ??= import('./lucideAll').then((m) => (loaded = m))
  return loading
}

// A Lucide name is lower case with dashes; anything else (an emoji) is text.
export const isIconName = (v: string) => /^[a-z][a-z0-9-]*$/.test(v)

// The icon of a name: at once when it is in the core set (what the app itself uses and the common ones), after the
// full set has loaded when it is not. null while it loads, or when there is no such name.
export function useIconComponent(name: string): LucideIcon | null {
  const core = CORE_ICONS[name] as LucideIcon | undefined
  const [all, setAll] = useState<All | null>(loaded)
  useEffect(() => {
    if (core !== undefined || !isIconName(name) || all) return
    let live = true
    void loadAllIcons().then((m) => live && setAll(m))
    return () => {
      live = false
    }
  }, [core, name, all])
  return core ?? all?.ALL_ICONS[name] ?? null
}
