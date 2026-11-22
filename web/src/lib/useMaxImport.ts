import { useQuery } from '@tanstack/react-query'
import { api } from '@/api/client'

// The server's import limit in bytes; undefined until `/api/meta` has answered
// (the server then checks the size itself). Shares the query of useMaxUpload.
export function useMaxImport(): number | undefined {
  return useQuery({ queryKey: ['meta'], queryFn: ({ signal }) => api.meta(signal), staleTime: Infinity }).data?.maxImport
}
