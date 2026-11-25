import { useSyncExternalStore } from 'react'
import { prefsStore } from './prefsStore'

// The first-sign-in tour: whether this person has done it or skipped it, and the requests to open it (the help menu,
// the notice about their repository). The state is a preference of the person: it goes through the prefs store, so it is
// in their personal space when they have one and in this browser when they do not, never in the database.

export interface Onboarding {
  state: 'done' | 'skipped'
  at: string
}

// Per person, because this browser's copy is shared by whoever signs in on it.
export const onboardingKey = (userId: string) => `pb.onboarding.${userId}`

export function readOnboarding(userId: string): Onboarding | null {
  const v = prefsStore().read(onboardingKey(userId)) as Partial<Onboarding> | null
  return v && (v.state === 'done' || v.state === 'skipped') && typeof v.at === 'string'
    ? { state: v.state, at: v.at }
    : null
}

// Whether the person's preferences are known well enough to decide on the tour: the store says when its first read of
// the person's file has ended (read, or failed and this browser's copy stands); a store that only knows this browser
// knows at once. Without it a slow file would start the tour again for someone who finished it.
const noSub = () => () => {}
export function usePrefsSettled(): boolean {
  const store = prefsStore() as Partial<{
    subscribe: (fn: () => void) => () => void
    status: () => { settled: boolean }
  }>
  const subscribe = store.subscribe ?? noSub
  return useSyncExternalStore(subscribe, () => (store.status ? store.status().settled : true))
}

export function writeOnboarding(userId: string, state: Onboarding['state'], at = new Date()): void {
  prefsStore().write(onboardingKey(userId), { state, at: at.toISOString() } satisfies Onboarding)
}

export type TourStep = 'welcome' | 'page' | 'editor' | 'favorites' | 'search' | 'repo' | 'admin' | 'done'

// The steps of the tour for this person: the repository step only when the site or the organisation asks for one that is
// not connected yet, the administrators' step only for administrators.
export function tourSteps(o: { admin: boolean; repo: boolean }): TourStep[] {
  return [
    'welcome',
    'page',
    'editor',
    'favorites',
    'search',
    ...(o.repo ? (['repo'] as const) : []),
    ...(o.admin ? (['admin'] as const) : []),
    'done',
  ]
}

// --- requests to open ---------------------------------------------------------------------

export type OnboardingRequest = { kind: 'tour' | 'repo'; auto?: boolean } | null

let request: OnboardingRequest = null
const listeners = new Set<() => void>()

export function requestOnboarding(r: OnboardingRequest): void {
  request = r
  for (const l of [...listeners]) l()
}

export const useOnboardingRequest = (): OnboardingRequest =>
  useSyncExternalStore(
    (l) => {
      listeners.add(l)
      return () => void listeners.delete(l)
    },
    () => request,
    () => null,
  )
