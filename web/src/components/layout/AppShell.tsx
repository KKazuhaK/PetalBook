import { useEffect } from 'react'
import { Outlet, useLocation } from 'react-router'
import { SidebarInset, SidebarProvider, useSidebar } from '@/components/ui/sidebar'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import { AppSidebar } from './AppSidebar'

// On a phone the sidebar is a drawer; it should close once a destination is picked.
function CloseMobileOnNavigate() {
  const { setOpenMobile } = useSidebar()
  const { pathname, search } = useLocation()
  useEffect(() => setOpenMobile(false), [pathname, search, setOpenMobile])
  return null
}

export function AppShell() {
  return (
    <SidebarProvider>
      <CloseMobileOnNavigate />
      <AppSidebar />
      <SidebarInset className="min-w-0">
        <ErrorBoundary>
          <Outlet />
        </ErrorBoundary>
      </SidebarInset>
    </SidebarProvider>
  )
}
