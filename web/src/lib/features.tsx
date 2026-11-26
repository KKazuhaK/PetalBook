// The features of the signed-in person's tenant (docs/api.md, "Features"), read from GET /api/settings: whole things an
// administrator may have switched off. What needs one that is off is not shown (no disabled stand-ins: a control that
// does nothing is a lie), and the server refuses it anyway (403 feature_off). The answer carries the version of the
// tenant's settings as its ETag, so reading it again costs the browser a 304 unless something changed.

import type { ReactNode } from 'react'
import { useQuery, type QueryClient } from '@tanstack/react-query'
import { api, ApiError } from '@/api/client'
import { useSession } from '@/api/hooks'
import type { FeatureKey } from '@/api/types'

export const featuresKey = ['settings', 'features'] as const

// The features as the tenant has them, by key; undefined until they are known (not asked yet, or the server cannot be
// reached).
export function useFeatures() {
  const user = useSession().data?.user?.id
  return useQuery({
    queryKey: [...featuresKey, user],
    enabled: !!user,
    staleTime: 60_000,
    refetchOnWindowFocus: true,
    queryFn: async ({ signal }) => (await api.settings(undefined, signal)).features,
  })
}

// Whether the tenant has a feature: false when an administrator turned it off, true when it is on, and undefined while
// it is not known. A feature the server does not list is on (only what a feature gates can be switched off).
export function useFeature(key: FeatureKey): boolean | undefined {
  const q = useFeatures()
  return q.data === undefined ? undefined : q.data[key] !== false
}

// Shows its children while the tenant has the feature, `fallback` (nothing) when it does not. Before the answer is in
// nothing is shown, and when it cannot be had (the server cannot be reached) the children are: the server decides.
export function Feature({
  name,
  children,
  fallback = null,
}: {
  name: FeatureKey
  children: ReactNode
  fallback?: ReactNode
}) {
  const q = useFeatures()
  const on = q.data === undefined ? undefined : q.data[name] !== false
  if (on === undefined) return q.isError ? children : null
  return on ? children : fallback
}

// What a request answered 403 feature_off means for what is on screen: the features are read again, so the parts that
// need the feature go away (queryClient's cache and mutation handlers call this).
export function refreshFeaturesOnRefusal(qc: QueryClient, error: unknown) {
  if (error instanceof ApiError && error.status === 403 && error.code === 'feature_off')
    void qc.invalidateQueries({ queryKey: featuresKey })
}
