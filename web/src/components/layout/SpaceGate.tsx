import { useEffect } from 'react'
import { Outlet, useLocation, useParams } from 'react-router'
import { rememberSpace, useActiveSpace, useSpaces } from '@/api/hooks'
import { ErrorState, PageFallback } from '@/components/common'
import { NotFoundPage } from '@/pages/NotFoundPage'
import { t } from '@/i18n'
import { SettingsNoticeBanner } from './SettingsNoticeBanner'
import { StorageNoticeBanner } from './StorageNoticeBanner'
import { SpaceDisabled } from './SpaceDisabled'
import { AdminViewBanner, AdminViewGate } from './AdminView'

// Only a space the person can open: anything else is the same "not found",
// whether it does not exist or is someone else's. (An administrator can open
// any space; see useActiveSpace.) A space an administrator switched off is in
// the list, and says so instead of failing on every request.
export function SpaceGate() {
  const { space = '' } = useParams()
  const spaces = useSpaces()
  const { spaces: all, resolving } = useActiveSpace()
  const current = all.find((s) => s.id === space)
  const { pathname } = useLocation()
  useEffect(() => {
    if (current && !current.disabled) rememberSpace(space)
  }, [current, space])
  if (spaces.isPending || resolving) return <PageFallback />
  if (spaces.isError && !spaces.data)
    return <ErrorState message={spaces.error.message} onRetry={() => void spaces.refetch()} />
  if (!current) return <NotFoundPage message={t.editor.spaceMissing} />
  if (current.disabled) return <SpaceDisabled space={current} />
  // An administrator who is not a member manages the space but reads it only in an audited view. The settings are the
  // managing: they open without one.
  const outsider = current.role === ''
  if (outsider && !current.permissions.includes('read') && !pathname.endsWith('/settings'))
    return <AdminViewGate space={current} />
  // A new key per space: nothing of the old one stays mounted.
  return (
    <>
      {outsider && <AdminViewBanner key={`view:${space}`} space={current} />}
      <StorageNoticeBanner key={`storage:${space}`} />
      <SettingsNoticeBanner key={`notice:${space}`} />
      <Outlet key={space} />
    </>
  )
}
