import { lazy, Suspense } from 'react'
import { useSession } from '@/api/hooks'
import { useConsoleAccess } from '@/api/admin'
import { PageFallback } from '@/components/common'
import { NotFoundPage } from '@/pages/NotFoundPage'

// The administration is a chunk of its own, fetched only by an administrator:
// the users, the spaces and the server's state are pages nobody else sees.
const AdminRoute = lazy(() => import('./AdminRoute'))

// The administration is for administrators of an accounts server. Anyone else,
// and a single-user server, gets the page that every unknown address gets, and
// never fetches the chunk.
export function AdminGate() {
  const session = useSession().data
  const access = useConsoleAccess()
  if (access.pending) return <PageFallback />
  if (session?.mode !== 'accounts' || !access.open) return <NotFoundPage />
  return (
    <Suspense fallback={<PageFallback />}>
      <AdminRoute />
    </Suspense>
  )
}
