import type { ReactNode } from 'react'
import { NotificationBell } from '@/components/notifications/NotificationBell'
import { SidebarToggle } from './SidebarToggle'

// The strip above every page: sidebar toggle on the left (only while the sidebar is hidden), the page's own
// title and actions beside it, and the bell of the notification centre at the end.
export function TopBar({ children, actions }: { children?: ReactNode; actions?: ReactNode }) {
  return (
    <header className="sticky top-0 z-30 flex h-12 shrink-0 items-center gap-2 border-b bg-background/85 px-3 backdrop-blur">
      <SidebarToggle where="topbar" />
      <div className="flex min-w-0 flex-1 items-center gap-2">{children}</div>
      <div className="flex shrink-0 items-center gap-1.5">
        {actions}
        <NotificationBell />
      </div>
    </header>
  )
}
