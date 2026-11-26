import { useEffect } from 'react'
import { Outlet, useLocation } from 'react-router'
import { SidebarInset, SidebarProvider, useSidebar } from '@/components/ui/sidebar'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import { FileOpsProvider } from '@/components/files/FileOps'
import { SearchDialogProvider } from '@/components/search/SearchDialog'
import { OnboardingHost } from '@/components/onboarding/OnboardingHost'
import { BrowserNotifier } from '@/components/notifications/BrowserNotifier'
import { AppSidebar } from './AppSidebar'
import { QuickCaptureProvider } from './QuickCapture'
import { OfflineBanner, OfflineSync } from './OfflineSync'
import { viewAsId } from '@/lib/viewAs'

// On a phone the sidebar is a drawer; it should close once a destination is picked.
function CloseMobileOnNavigate() {
  const { setOpenMobile } = useSidebar()
  const { pathname, search } = useLocation()
  useEffect(() => setOpenMobile(false), [pathname, search, setOpenMobile])
  return null
}

export function AppShell() {
  // The administration has a sidebar of its own, in place of the app's.
  const inAdmin = useLocation().pathname.startsWith('/admin')
  return (
    <SidebarProvider>
      <QuickCaptureProvider>
        <SearchDialogProvider>
          <FileOpsProvider>
            <CloseMobileOnNavigate />
            {/* None of these in a view as a person: the copies on the device, the notices and the tour are the
                administrator's own, or the person's (lib/viewAs). */}
            {viewAsId() === null && (
              <>
                <OfflineSync />
                <BrowserNotifier />
                <OnboardingHost />
              </>
            )}
            {!inAdmin && <AppSidebar />}
            <SidebarInset className="min-w-0">
              <OfflineBanner />
              <ErrorBoundary>
                <Outlet />
              </ErrorBoundary>
            </SidebarInset>
          </FileOpsProvider>
        </SearchDialogProvider>
      </QuickCaptureProvider>
    </SidebarProvider>
  )
}
