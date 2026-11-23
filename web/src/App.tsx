import { lazy, Suspense, useEffect, type ReactNode } from 'react'
import { createBrowserRouter, Navigate, Outlet, RouterProvider, useLocation } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ThemeProvider } from '@/providers/theme'
import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import { toast } from 'sonner'
import { ApiError, onForbidden, onHold, onSignedOut, onSpaceAccess, onUnavailable } from '@/api/client'
import { keys, lastSpace, refreshSpaceAccess, useSession, useSpaces } from '@/api/hooks'
import { SpaceSettingsRedirect } from '@/components/settings/SpaceSettingsRedirect'
import { AdminGate } from '@/components/admin/AdminGate'
import { SpaceGate } from '@/components/layout/SpaceGate'
import type { SessionInfo } from '@/api/types'
import { AppShell } from '@/components/layout/AppShell'
import { ErrorState, PageFallback } from '@/components/common'
import { FolderList, RecentList, SearchList, TagList } from '@/pages/Lists'
import { NotFoundPage } from '@/pages/NotFoundPage'
import { loginUrl } from '@/lib/auth'
import { t } from '@/i18n'

// The editor drags in Tiptap, ProseMirror and the Markdown pipeline; the list
// views do not need any of it, so it loads only when a page is opened. The
// pages around signing in and the account are rarely visited and load on
// their own as well.
const PageEditor = lazy(() => import('@/pages/PageEditor'))
const LoginPage = lazy(() => import('@/pages/auth/LoginPage'))
const SetupPage = lazy(() => import('@/pages/auth/SetupPage'))
const InvitePage = lazy(() => import('@/pages/auth/InvitePage'))
const HeldPage = lazy(() => import('@/pages/auth/HeldPage'))
const ConsentPage = lazy(() => import('@/pages/auth/ConsentPage'))
const SettingsRoute = lazy(() => import('@/components/settings/SettingsRoute'))
const ImportPage = lazy(() => import('@/pages/ImportPage'))
const CheckPage = lazy(() => import('@/pages/CheckPage'))
// The home page of a space: the default landing, in a chunk of its own so the lists do not carry it.
const HomePage = lazy(() => import('@/components/home/HomePage'))

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
  // Only when there is nothing to go on: a failed refresh in the background
  // (offline for a moment) must not take away the page someone is typing in.
  if (session.isError && !session.data) return <ErrorState message={t.fmt.labelled(t.auth.loadFailed, session.error.message)} onRetry={() => void session.refetch()} />
  const s = session.data
  if (s.mode === 'accounts') {
    if (s.setup) return <Navigate to="/setup" replace />
    if (!s.user) return <Navigate to={loginUrl(pathname + search)} replace />
    // A person who must have a passkey and has none: nothing but the page to set one up.
    if (s.hold === 'passkey') return later(<HeldPage />)
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
  if (spaces.isError && !spaces.data) return <ErrorState message={spaces.error.message} onRetry={() => void spaces.refetch()} />
  const list = spaces.data.spaces
  const last = lastSpace()
  const space = list.find((s) => s.id === last) ?? list[0]
  if (!space) return <NotFoundPage message={t.editor.spaceMissing} />
  const target = pathname === '/' ? '' : pathname
  return <Navigate to={`/s/${encodeURIComponent(space.id)}${target}${search}${hash}`} replace />
}

// The sign-in page sends the person back to `/oauth/authorize?...`, an address of
// the server (an MCP client sent the browser there, and the server needed a
// session first). Now there is one: ask the server again, with a page load.
function ToTheServer() {
  const { pathname, search } = useLocation()
  useEffect(() => window.location.replace(pathname + search), [pathname, search])
  return <PageFallback />
}

// useBlocker (leave-with-unsaved-text guard) needs a data router.
const router = createBrowserRouter([
  { path: 'login', element: later(<LoginPage />) },
  { path: 'setup', element: later(<SetupPage />) },
  { path: 'invite', element: later(<InvitePage />) },
  { path: 'oauth/authorize', element: <ToTheServer /> },
  {
    element: <AuthGate />,
    children: [
      // The page an MCP client sends the person to; no app around it.
      { path: 'oauth/consent', element: later(<ConsentPage />) },
      {
        element: <AppShell />,
        children: [
          { index: true, element: <LegacyRedirect /> },
          { path: 'recent', element: <LegacyRedirect /> },
          { path: 'f/*', element: <LegacyRedirect /> },
          { path: 't/*', element: <LegacyRedirect /> },
          { path: 'search', element: <LegacyRedirect /> },
          { path: 'p/:id', element: <LegacyRedirect /> },
          // The settings dialog. `/settings` with no section picks the first one.
          { path: 'settings/:section?', element: later(<SettingsRoute />) },
          // Users and invites moved from the settings to the administration.
          { path: 'users', element: <Navigate to="/admin/users" replace /> },
          { path: 'settings/users', element: <Navigate to="/admin/users" replace /> },
          { path: 'admin/:section?', element: <AdminGate /> },
          {
            path: 's/:space',
            element: <SpaceGate />,
            children: [
              { index: true, element: later(<HomePage />) },
              { path: 'recent', element: <RecentList /> },
              { path: 'f/*', element: <FolderList /> },
              { path: 't/*', element: <TagList /> },
              { path: 'search', element: <SearchList /> },
              { path: 'settings', element: <SpaceSettingsRedirect /> },
              { path: 'import', element: later(<ImportPage />) },
              { path: 'check', element: later(<CheckPage />) },
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
  // Every request answers 403 `passkey_required` while the session is held: the session says so from then on.
  useEffect(
    () =>
      onHold(() => {
        queryClient.setQueryData<SessionInfo>(keys.session, (prev) => prev && { ...prev, hold: 'passkey' })
      }),
    [],
  )
  // A request refused because the space was switched off, or the person's role
  // no longer allows it, or a space that is not there: the list says so too, once it
  // is read again (and the space an administrator is viewing, which the list lacks).
  useEffect(() => onSpaceAccess((space, notFound) => void refreshSpaceAccess(queryClient, space, notFound)), [])
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
