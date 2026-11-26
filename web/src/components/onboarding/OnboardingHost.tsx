import { lazy, Suspense, useEffect, useRef } from 'react'
import { useLocation } from 'react-router'
import { useSession, useSpaces } from '@/api/hooks'
import { administers } from '@/lib/access'
import {
  readOnboarding,
  requestOnboarding,
  useOnboardingRequest,
  usePrefsSettled,
  writeOnboarding,
} from '@/lib/onboarding'
import { useRepoGuide } from './useRepoGuide'

// The tour and the guide are loaded when they are first wanted, not with the app.
const Tour = lazy(() => import('./Tour').then((m) => ({ default: m.Tour })))
const RepoGuideDialog = lazy(() => import('./RepoGuideDialog').then((m) => ({ default: m.RepoGuideDialog })))

// Starts the tour on the first sign-in of a person who has not done or skipped it (once the spaces are known, so the
// repository step is right), and shows it or the repository guide whenever the help menu or a notice asks.
export function OnboardingHost() {
  const user = useSession().data?.user
  const spaces = useSpaces()
  const request = useOnboardingRequest()
  const { pathname } = useLocation()
  const guide = useRepoGuide()
  // The tour waits for the person's file (or for its read to have failed): this browser may not have their copy yet.
  const prefsKnown = usePrefsSettled()
  const started = useRef<string | null>(null)
  const admin = administers(user)
  // Not over the administration or the settings, which are where a person goes on purpose.
  const calm = !pathname.startsWith('/admin') && !pathname.startsWith('/settings')
  useEffect(() => {
    if (!user || !spaces.isSuccess || !calm || !prefsKnown || started.current === user.id) return
    started.current = user.id
    if (readOnboarding(user.id) === null) requestOnboarding({ kind: 'tour', auto: true })
  }, [user, spaces.isSuccess, calm, prefsKnown])
  // What was asked is not asked again by the next person on this page, nor after a sign-out.
  useEffect(() => () => requestOnboarding(null), [])
  if (!user || !request) return null
  const close = (finished: boolean) => {
    // However it ended, the person has seen it: done if they went to the end, skipped otherwise.
    if (request.kind === 'tour' && (request.auto || readOnboarding(user.id) === null))
      writeOnboarding(user.id, finished ? 'done' : 'skipped')
    requestOnboarding(null)
  }
  return (
    <Suspense fallback={null}>
      {request.kind === 'tour' ? (
        <Tour admin={admin} repo={!!guide.note} onClose={close} />
      ) : (
        <RepoGuideDialog onClose={() => requestOnboarding(null)} />
      )}
    </Suspense>
  )
}
