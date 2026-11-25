import { useEffect, useState } from 'react'
import { CORE_ICONS } from './coreIcons'
import { isIconName } from './check'

export { firstGrapheme, isIconName, isTextIcon, MAX_ICON_BYTES } from './check'

type All = typeof import('./lucideAll')
let loaded: All | null = null
let loading: Promise<All> | null = null

// The whole set, loaded once as a chunk of its own, from this server.
export function loadAllIcons(): Promise<All> {
  loading ??= import('./lucideAll').then((m) => (loaded = m))
  return loading
}

// The whole set once it has loaded, for a name that is not in the core set (null while it loads, and for core names,
// which need no loading).
export function useFullIcons(name: string): All | null {
  const needed = isIconName(name) && !Object.hasOwn(CORE_ICONS, name)
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
