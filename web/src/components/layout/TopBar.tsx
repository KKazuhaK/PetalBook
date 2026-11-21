import type { ReactNode } from 'react'
import { SidebarTrigger } from '@/components/ui/sidebar'
import { t } from '@/i18n/zh-CN'

// The strip above every page: sidebar toggle on the left, the page's own
// title and actions beside it.
export function TopBar({ children, actions }: { children?: ReactNode; actions?: ReactNode }) {
  return (
    <header className="sticky top-0 z-30 flex h-12 shrink-0 items-center gap-2 border-b bg-background/85 px-3 backdrop-blur">
      <SidebarTrigger aria-label={t.nav.toggleSidebar} />
      <div className="flex min-w-0 flex-1 items-center gap-2">{children}</div>
      <div className="flex shrink-0 items-center gap-1.5">{actions}</div>
    </header>
  )
}
