import { useCallback } from 'react'
import { SidebarTrigger, useSidebar } from '@/components/ui/sidebar'
import { t } from '@/i18n'

// The button that opens and closes the sidebar lives in two places: in the
// sidebar's own header, and in the page's top bar while the sidebar is out of
// sight (collapsed, or the off-canvas sheet on a phone). Only one is mounted at
// a time, so a click that moves it must hand focus to its other self.
let refocus = false
const hand = () => {
  refocus = true
  setTimeout(() => (refocus = false), 300)
}

export function SidebarToggle({ where }: { where: 'sidebar' | 'topbar' }) {
  const { isMobile, state } = useSidebar()
  const visible = where === 'sidebar' ? !isMobile && state === 'expanded' : isMobile || state === 'collapsed'
  // Takes focus when it appears because the other one was just clicked.
  const take = useCallback((el: HTMLButtonElement | null) => {
    if (el && refocus) {
      refocus = false
      el.focus()
    }
  }, [])
  if (!visible) return null
  // In the top bar it is as big as the other buttons there (the 40 px tap area); in the sidebar's header it stays small.
  return (
    <SidebarTrigger
      ref={take}
      aria-label={t.nav.toggleSidebar}
      className={where === 'topbar' ? 'size-10 shrink-0' : 'shrink-0'}
      onClick={hand}
    />
  )
}
