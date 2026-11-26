// Whether the administrators allow offline copies for this account's tenant (the setting features.offline, read from
// GET /api/settings). `undefined` while it is not known (not asked yet, or the server cannot be reached): nothing is
// deleted on a guess, only on an answer of false, or of 403 offline_off from a sync.

import { useQuery } from '@tanstack/react-query'
import { api } from '@/api/client'

export const offlineAllowedKey = ['settings', 'offline-copies'] as const

export function useOfflineSetting(user: string | undefined) {
  return useQuery({
    queryKey: [...offlineAllowedKey, user],
    enabled: !!user,
    staleTime: 60_000,
    refetchOnWindowFocus: true,
    queryFn: async ({ signal }) => {
      const r = await api.settings(undefined, signal)
      return r.settings.find((x) => x.key === 'features.offline')?.effective === true
    },
  })
}

export const useOfflineAllowed = (user: string | undefined): boolean | undefined => useOfflineSetting(user).data
