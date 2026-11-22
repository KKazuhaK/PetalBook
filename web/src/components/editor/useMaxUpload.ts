import { useCallback } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/api/client'
import type { MetaResponse } from '@/api/types'

const META_KEY = ['meta'] as const

// The server's upload limit in bytes, as a getter: the editor reads it when a
// file is chosen, and it is undefined until `/api/meta` has answered (the server
// then checks the size itself).
export function useMaxUpload(): () => number | undefined {
  const qc = useQueryClient()
  useQuery({ queryKey: META_KEY, queryFn: ({ signal }) => api.meta(signal), staleTime: Infinity })
  return useCallback(() => qc.getQueryData<MetaResponse>(META_KEY)?.maxUpload, [qc])
}
