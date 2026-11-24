// Light / dark / follow-system, as a class on <html>, and a colour palette, as
// `data-palette` on <html> (src/lib/palettes.ts; the colours are CSS variables
// in index.css). Small enough to own: the usual library injects a <script> from
// inside a React component, which React 19 logs as an error on every load.
//
// The light/dark class is also applied before first paint by
// public/theme-init.js, so there is no flash of the wrong mode. That file knows
// nothing of palettes: the palette is applied here, as this module loads, which
// is as soon as the bundle runs. A person who chose another palette may see
// the default colours for a moment before that, on the very first paint.

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { isPaletteId, type PaletteId } from '@/lib/palettes'

export type Theme = 'light' | 'dark' | 'system'
const KEY = 'theme'
const query = '(prefers-color-scheme: dark)'

function stored(): Theme {
  try {
    const v = localStorage.getItem(KEY)
    return v === 'light' || v === 'dark' ? v : 'system'
  } catch {
    return 'system'
  }
}

const PALETTE_KEY = 'pb.palette'

export function storedPalette(): PaletteId {
  try {
    const v = localStorage.getItem(PALETTE_KEY)
    return isPaletteId(v) ? v : 'default'
  } catch {
    return 'default'
  }
}

// The default palette is no attribute at all: it is the CSS as it always was.
export function applyPalette(id: PaletteId) {
  if (typeof document === 'undefined') return
  if (id === 'default') document.documentElement.removeAttribute('data-palette')
  else document.documentElement.setAttribute('data-palette', id)
}

// At module load, before React renders anything.
applyPalette(storedPalette())

interface ThemeContext {
  theme: Theme
  setTheme: (t: Theme) => void
  // Whether the dark form is what is showing right now (the choice, or the system's when following it).
  dark: boolean
  palette: PaletteId
  setPalette: (p: PaletteId) => void
}
const Ctx = createContext<ThemeContext>({
  theme: 'system',
  setTheme: () => {},
  dark: false,
  palette: 'default',
  setPalette: () => {},
})

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(stored)
  const [palette, setPaletteState] = useState<PaletteId>(storedPalette)
  const [systemDark, setSystemDark] = useState(() => window.matchMedia(query).matches)

  useEffect(() => {
    const mql = window.matchMedia(query)
    const on = () => setSystemDark(mql.matches)
    mql.addEventListener('change', on)
    return () => mql.removeEventListener('change', on)
  }, [])

  const dark = theme === 'dark' || (theme === 'system' && systemDark)
  useEffect(() => {
    document.documentElement.classList.toggle('dark', dark)
    document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
  }, [dark])

  const setTheme = useCallback((t: Theme) => {
    setThemeState(t)
    try {
      localStorage.setItem(KEY, t)
    } catch {
      // Storage blocked: the choice lasts until the page is closed.
    }
  }, [])

  const setPalette = useCallback((p: PaletteId) => {
    setPaletteState(p)
    applyPalette(p)
    try {
      if (p === 'default') localStorage.removeItem(PALETTE_KEY)
      else localStorage.setItem(PALETTE_KEY, p)
    } catch {
      // Storage blocked: the choice lasts until the page is closed.
    }
  }, [])

  const value = useMemo(
    () => ({ theme, setTheme, dark, palette, setPalette }),
    [theme, setTheme, dark, palette, setPalette],
  )
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

export const useTheme = () => useContext(Ctx)
