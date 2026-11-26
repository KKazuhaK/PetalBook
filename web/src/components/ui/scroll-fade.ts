import { useEffect, type RefObject } from 'react'

const FADE = 24
const MASK =
  '[mask-image:linear-gradient(to_right,transparent,#000_var(--fade-left,0px),#000_calc(100%-var(--fade-right,0px)),transparent)]'

// The classes that go with the hook: no scrollbar, and the mask the fade is drawn with.
export const scrollFadeClass = `overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden ${MASK}`
// For tables: the bar and the fade apply on touch screens only, and a mouse keeps its scrollbar.
export const scrollFadeTouchClass = `overflow-x-auto pointer-coarse:[scrollbar-width:none] pointer-coarse:[&::-webkit-scrollbar]:hidden pointer-coarse:${MASK}`

// A strip that scrolls sideways without a scrollbar: where there is more to see, that edge fades out (the start, the
// end, or both, by where the scroll is). Give the scrolling element a ref and the class above:
//   const strip = useRef<HTMLDivElement>(null)
//   useScrollFade(strip)
//   <div ref={strip} className={`${scrollFadeClass} flex gap-2`}>
export function useScrollFade(ref: RefObject<HTMLElement | null>) {
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const update = () => {
      const max = el.scrollWidth - el.clientWidth
      const left = Math.abs(el.scrollLeft)
      const before = max > 1 && left > 1 ? FADE : 0
      const after = max > 1 && left < max - 1 ? FADE : 0
      // In a right-to-left strip the start is on the right.
      const rtl = getComputedStyle(el).direction === 'rtl'
      el.style.setProperty('--fade-left', `${rtl ? after : before}px`)
      el.style.setProperty('--fade-right', `${rtl ? before : after}px`)
    }
    update()
    el.addEventListener('scroll', update, { passive: true })
    // (Where there is no ResizeObserver, a test environment say, the fade follows the scroll only.)
    const ro = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(update)
    ro?.observe(el)
    for (const c of Array.from(el.children)) ro?.observe(c)
    return () => {
      el.removeEventListener('scroll', update)
      ro?.disconnect()
    }
  })
}
