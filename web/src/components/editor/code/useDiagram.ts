// The picture of a diagram's text, kept up to date while the text is edited.

import { useEffect, useRef, useState, useSyncExternalStore } from 'react'
import { renderDiagram } from './mermaidRender'

export interface DiagramState {
  // The last picture that drew without error; stays while a later text fails.
  url: string | null
  error: string | null
  busy: boolean
}

// Typing pauses this long before the picture is redrawn.
export const DIAGRAM_DELAY_MS = 500

export function useDiagram(text: string, dark: boolean, delay = DIAGRAM_DELAY_MS): DiagramState {
  const [state, setState] = useState<DiagramState>({ url: null, error: null, busy: text.trim() !== '' })
  const drawn = useRef(false)

  useEffect(() => {
    if (text.trim() === '') return
    let stale = false
    // The first drawing of a block does not wait; later edits do.
    const timer = setTimeout(
      () => {
        setState((s) => ({ ...s, busy: true }))
        renderDiagram(text, dark).then(
          (url) => !stale && setState({ url, error: null, busy: false }),
          (e: unknown) => !stale && setState((s) => ({ ...s, error: e instanceof Error ? e.message : String(e), busy: false })),
        )
        drawn.current = true
      },
      drawn.current ? delay : 0,
    )
    return () => {
      stale = true
      clearTimeout(timer)
    }
  }, [text, dark, delay])

  // No text, no picture: nothing is kept from what was there before.
  return text.trim() === '' ? { url: null, error: null, busy: false } : state
}

// Whether the page is in its dark theme (a class on <html>, see providers/theme.tsx).
// One observer serves every block of the page.
const listeners = new Set<() => void>()
let observer: MutationObserver | null = null

function subscribe(onChange: () => void) {
  listeners.add(onChange)
  if (!observer) {
    observer = new MutationObserver(() => listeners.forEach((l) => l()))
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
  }
  return () => {
    listeners.delete(onChange)
    if (listeners.size === 0) {
      observer?.disconnect()
      observer = null
    }
  }
}

export const useDarkTheme = (): boolean => useSyncExternalStore(subscribe, () => document.documentElement.classList.contains('dark'), () => false)
