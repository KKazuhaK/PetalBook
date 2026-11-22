import { lazy, Suspense, useEffect, type ReactNode } from 'react'
import { createBrowserRouter, Navigate, Outlet, RouterProvider, useLocation, useParams } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ThemeProvider } from '@/providers/theme'
import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import { toast } from 'sonner'
import { ApiError, onForbidden, onSignedOut, onUnavailable } from '@/api/client'
import { keys, lastSpace, rememberSpace, useSession, useSpaces } from '@/api/hooks'
import type { SessionInfo } from '@/api/types'
import { AppShell } from '@/components/layout/AppShell'
import { ErrorState, PageFallback } from '@/components/common'
import { FolderList, RecentList, SearchList, TagList } from '@/pages/Lists'
import { NotFoundPage } from '@/pages/NotFoundPage'
import { loginUrl } from '@/lib/auth'
import { t } from '@/i18n/zh-CN'

// The editor drags in Tiptap, ProseMirror and the Markdown pipeline; the list
// views do not need any of it, so it loads only when a page is opened. The
// pages around signing in and the account are rarely visited and load on
// their own as well.
const PageEditor = lazy(() => import('@/pages/PageEditor'))
const LoginPage = lazy(() => import('@/pages/auth/LoginPage'))
const SetupPage = lazy(() => import('@/pages/auth/SetupPage'))
const InvitePage = lazy(() => import('@/pages/auth/InvitePage'))
const SettingsPage = lazy(() => import('@/pages/auth/SettingsPage'))
const UsersPage = lazy(() => import('@/pages/auth/UsersPage'))
const SpaceSettingsPage = lazy(() => import('@/pages/SpaceSettingsPage'))

const later = (el: ReactNode) => <Suspense fallback={<PageFallback />}>{el}</Suspense>

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      // A 4xx will not get better by asking again.
      retry: (count, error) => !(error instanceof ApiError && error.status >= 400 && error.status < 500) && count < 2,
    },
  },
})

// In accounts mode nothing but the sign-in pages is shown without a session:
// the first run goes to setup, everyone else to sign in, with the path to come
// back to. The session is fetched once and cached; a 401 anywhere flips it to
// signed out (see below), which lands here and sends the person to sign in. The
// editor's drafts are in the browser's storage and are not touched by any of it.
function AuthGate() {
  const session = useSession()
  const { pathname, search } = useLocation()
  if (session.isPending) return <PageFallback />
  if (session.isError) return <ErrorState message={`${t.auth.loadFailed}：${session.error.message}`} onRetry={() => void session.refetch()} />
  const s = session.data
  if (s.mode === 'accounts') {
    if (s.setup) return <Navigate to="/setup" replace />
    if (!s.user) return <Navigate to={loginUrl(pathname + search)} replace />
  }
  return <Outlet />
}

// Old (milestone 1.1) addresses had no space: `/recent`, `/f/…`, `/t/…`,
// `/search`, `/p/<id>`. They go to the same place in the space used last, or
// the first one.
function LegacyRedirect() {
  const spaces = useSpaces()
  const { pathname, search, hash } = useLocation()
  if (spaces.isPending) return <PageFallback />
  if (spaces.isError) return <ErrorState message={spaces.error.message} onRetry={() => void spaces.refetch()} />
  const list = spaces.data.spaces
  const last = lastSpace()
  const space = list.find((s) => s.id === last) ?? list[0]
  if (!space) return <NotFoundPage message={t.editor.spaceMissing} />
  const target = pathname === '/' ? '/recent' : pathname
  return <Navigate to={`/s/${encodeURIComponent(space.id)}${target}${search}${hash}`} replace />
}

// Only a space the person can open: anything else is the same "not found",
// whether it does not exist or is someone else's.
function SpaceGate() {
  const { space = '' } = useParams()
  const spaces = useSpaces()
  const known = spaces.data?.spaces.some((s) => s.id === space)
  useEffect(() => {
    if (known) rememberSpace(space)
  }, [known, space])
  if (spaces.isPending) return <PageFallback />
  if (spaces.isError) return <ErrorState message={spaces.error.message} onRetry={() => void spaces.refetch()} />
  if (!known) return <NotFoundPage message={t.editor.spaceMissing} />
  // A new key per space: nothing of the old one stays mounted.
  return <Outlet key={space} />
}

// useBlocker (leave-with-unsaved-text guard) needs a data router.
const router = createBrowserRouter([
  { path: 'login', element: later(<LoginPage />) },
  { path: 'setup', element: later(<SetupPage />) },
  { path: 'invite', element: later(<InvitePage />) },
  {
    element: <AuthGate />,
    children: [
      {
        element: <AppShell />,
        children: [
          { index: true, element: <LegacyRedirect /> },
          { path: 'recent', element: <LegacyRedirect /> },
          { path: 'f/*', element: <LegacyRedirect /> },
          { path: 't/*', element: <LegacyRedirect /> },
          { path: 'search', element: <LegacyRedirect /> },
          { path: 'p/:id', element: <LegacyRedirect /> },
          { path: 'settings', element: later(<SettingsPage />) },
          { path: 'users', element: later(<UsersPage />) },
          {
            path: 's/:space',
            element: <SpaceGate />,
            children: [
              { index: true, element: <Navigate to="recent" replace /> },
              { path: 'recent', element: <RecentList /> },
              { path: 'f/*', element: <FolderList /> },
              { path: 't/*', element: <TagList /> },
              { path: 'search', element: <SearchList /> },
              { path: 'settings', element: later(<SpaceSettingsPage />) },
              { path: 'p/:id', element: later(<PageEditor />) },
              { path: '*', element: <NotFoundPage /> },
            ],
          },
          { path: '*', element: <NotFoundPage /> },
        ],
      },
    ],
  },
])

export default function App() {
  // A refused origin makes every request fail the same way; one notice that
  // stays until the page is reloaded says why.
  useEffect(() => onForbidden((message) => void toast.error(message, { id: 'forbidden', duration: Infinity })), [])
  // The first 401 (the client reports it once) marks the session as gone.
  useEffect(
    () =>
      onSignedOut(() => {
        queryClient.setQueryData<SessionInfo>(keys.session, (prev) => prev && { ...prev, user: null })
      }),
    [],
  )
  // A write refused because the remote cannot be reached says the sync state
  // changed: look again, so the indicator by the space name is not out of date.
  useEffect(
    () =>
      onUnavailable(() => {
        void queryClient.invalidateQueries({ queryKey: keys.spaces })
        void queryClient.invalidateQueries({ predicate: (q) => q.queryKey[0] === 'space' && q.queryKey[2] === 'remote' })
      }),
    [],
  )
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
