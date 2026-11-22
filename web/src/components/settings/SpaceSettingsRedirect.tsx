import { useEffect } from 'react'
import { Navigate, useParams } from 'react-router'
import { rememberSpace, useSession } from '@/api/hooks'
import { legacySettingsTarget, settingsPath } from './sections'

// `/s/<space>/settings` (and `/settings`, `/users`) became sections of the one
// settings dialog; the old address of a space's settings goes to the section
// that has what it had, with that space made the one in use.
export function SpaceSettingsRedirect() {
  const { space = '' } = useParams()
  const sessionQuery = useSession()
  const session = sessionQuery.data
  const target = legacySettingsTarget(`/s/${encodeURIComponent(space)}/settings`, {
    mode: session?.mode,
    signedIn: !!session?.user,
    hasSpace: true,
    canRead: true,
    canWrite: true,
  })
  useEffect(() => rememberSpace(space), [space])
  // Which section it is depends on the mode, so wait to know it.
  if (!session) return null
  return <Navigate to={settingsPath(target?.section ?? 'general')} replace />
}
