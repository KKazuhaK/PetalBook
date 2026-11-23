import { useEffect } from 'react'
import { Outlet, useParams } from 'react-router'
import { rememberSpace, useActiveSpace, useSpaces } from '@/api/hooks'
import { ErrorState, PageFallback } from '@/components/common'
import { NotFoundPage } from '@/pages/NotFoundPage'
import { t } from '@/i18n'
import { SettingsNoticeBanner } from './SettingsNoticeBanner'
import { SpaceDisabled } from './SpaceDisabled'

// Only a space the person can open: anything else is the same "not found",
// whether it does not exist or is someone else's. (An administrator can open
// any space; see useActiveSpace.) A space an administrator switched off is in
// the list, and says so instead of failing on every request.
export function SpaceGate() {
  const { space = '' } = useParams()
  const spaces = useSpaces()
  const { spaces: all, resolving } = useActiveSpace()
  const current = all.find((s) => s.id === space)
  useEffect(() => {
    if (current && !current.disabled) rememberSpace(space)
  }, [current, space])
  if (spaces.isPending || resolving) return <PageFallback />
  if (spaces.isError && !spaces.data) return <ErrorState message={spaces.error.message} onRetry={() => void spaces.refetch()} />
  if (!current) return <NotFoundPage message={t.editor.spaceMissing} />
  if (current.disabled) return <SpaceDisabled space={current} />
  // A new key per space: nothing of the old one stays mounted.
  return (
    <>
      <SettingsNoticeBanner key={`notice:${space}`} />
      <Outlet key={space} />
    </>
  )
}
