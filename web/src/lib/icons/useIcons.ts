import { useEffect, useState } from 'react'
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

// The whole set once it has loaded, for a name that is not in the core set (null while it loads, and for core names,
// which need no loading).
export function useFullIcons(name: string): All | null {
  const needed = !(name in CORE_ICONS) && isIconName(name)
  const [all, setAll] = useState<All | null>(loaded)
  useEffect(() => {
    if (!needed || all) return
    let live = true
    void loadAllIcons().then((m) => live && setAll(m))
    return () => {
      live = false
    }
  }, [needed, all])
  return needed ? all : null
}
