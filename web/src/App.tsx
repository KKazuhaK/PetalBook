import { lazy, Suspense, useEffect } from 'react'
import { createBrowserRouter, Navigate, RouterProvider } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ThemeProvider } from '@/providers/theme'
import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import { toast } from 'sonner'
import { ApiError, onForbidden } from '@/api/client'
import { AppShell } from '@/components/layout/AppShell'
import { PageFallback } from '@/components/common'
import { FolderList, RecentList, SearchList, TagList } from '@/pages/Lists'
import { NotFoundPage } from '@/pages/NotFoundPage'

// The editor drags in Tiptap, ProseMirror and the Markdown pipeline; the list
// views do not need any of it, so it loads only when a page is opened.
const PageEditor = lazy(() => import('@/pages/PageEditor'))

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      // A 4xx will not get better by asking again.
      retry: (count, error) => !(error instanceof ApiError && error.status >= 400 && error.status < 500) && count < 2,
    },
  },
})

// useBlocker (leave-with-unsaved-text guard) needs a data router.
const router = createBrowserRouter([
  {
    element: <AppShell />,
    children: [
      { index: true, element: <Navigate to="/recent" replace /> },
      { path: 'recent', element: <RecentList /> },
      { path: 'f/*', element: <FolderList /> },
      { path: 't/*', element: <TagList /> },
      { path: 'search', element: <SearchList /> },
      {
        path: 'p/:id',
        element: (
          <Suspense fallback={<PageFallback />}>
            <PageEditor />
          </Suspense>
        ),
      },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
])

export default function App() {
  // A refused origin makes every request fail the same way; one notice that
  // stays until the page is reloaded says why.
  useEffect(() => onForbidden((message) => void toast.error(message, { id: 'forbidden', duration: Infinity })), [])
  return (
    <ThemeProvider>
      <QueryClientProvider client={queryClient}>
        <TooltipProvider>
          <RouterProvider router={router} />
          <Toaster position="bottom-right" />
        </TooltipProvider>
      </QueryClientProvider>
    </ThemeProvider>
  )
}
