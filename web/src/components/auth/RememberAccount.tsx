import { useEffect } from 'react'
import { useSession } from '@/api/hooks'
import { noteSession } from '@/lib/lastAccount'
import { viewAsId } from '@/lib/viewAs'

// Renders nothing: while someone is signed in (accounts mode), keeps who they are in this browser for the sign-in
// page to offer again if the session ends by itself (see lib/lastAccount).
export function RememberAccount() {
  const s = useSession().data
  const user = s?.mode === 'accounts' ? s.user : null
  useEffect(() => {
    // A view tab shows someone else: they are not this browser's last account.
    if (user && viewAsId() === null) noteSession(user)
  }, [user])
  return null
}
