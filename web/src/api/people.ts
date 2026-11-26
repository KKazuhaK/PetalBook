// Finding people to add, and adding many at once (docs/api.md, "Adding people in batches").

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { enc, request } from './client'
import { keys } from './hooks'
import { tp } from '@/i18n/people'

export type { BatchAnswer, BatchRowIn, BatchRowOut, BatchStatus, PersonRef } from './peopleTypes'
import type { BatchAnswer, BatchRowIn, PersonRef } from './peopleTypes'
export { MAX_BATCH_ROWS } from './peopleTypes'

export const peopleApi = {
  search: (q: string, signal?: AbortSignal) =>
    request<{ people: PersonRef[] }>(`/people?q=${enc(q)}&limit=20`, { signal }),
  spaceBatch: (space: string, rows: BatchRowIn[], dryRun: boolean) =>
    request<BatchAnswer>(`/spaces/${enc(space)}/members/batch`, { method: 'POST', body: { rows, dryRun } }),
  groupBatch: (group: string, rows: BatchRowIn[], dryRun: boolean) =>
    request<BatchAnswer>(`/admin/groups/${enc(group)}/members/batch`, { method: 'POST', body: { rows, dryRun } }),
  tenantBatch: (rows: BatchRowIn[], dryRun: boolean, tenant?: string) =>
    request<BatchAnswer>('/admin/members/batch', { method: 'POST', body: { rows, dryRun, tenant } }),
}

// The people of the tenant whose username or name matches (two characters at least).
export function usePeopleSearch(q: string, enabled = true) {
  const text = q.trim()
  return useQuery({
    queryKey: ['people', 'search', text] as const,
    queryFn: ({ signal }) => peopleApi.search(text, signal),
    enabled: enabled && [...text].length >= 2,
    staleTime: 15_000,
    placeholderData: (prev) => prev,
  })
}

// A batch changes what the lists of members, groups, invites and people show.
export function useBatchChanged() {
  const qc = useQueryClient()
  return () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: keys.invites }),
      qc.invalidateQueries({ queryKey: keys.users }),
      qc.invalidateQueries({ queryKey: keys.adminGroups }),
      qc.invalidateQueries({ queryKey: keys.spaces }),
      qc.invalidateQueries({ predicate: (q) => q.queryKey[0] === 'space' && q.queryKey[2] === 'members' }),
    ])
}

export type BatchRunner = (rows: BatchRowIn[], dryRun: boolean) => Promise<BatchAnswer>

export function useBatchRunner(run: BatchRunner) {
  const changed = useBatchChanged()
  return useMutation({
    mutationFn: ({ rows, dryRun }: { rows: BatchRowIn[]; dryRun: boolean }) => run(rows, dryRun),
    onSuccess: (a) => (a.dryRun ? undefined : changed()),
  })
}

// The picker's way of adding: the picked people go in one batch, and a row that did not go in is told in words.
export function usePickRunner(run: BatchRunner) {
  const changed = useBatchChanged()
  return async (picks: { key: string }[], role?: string) => {
    const answer = await run(
      picks.map((p) => (role ? { user: p.key, role } : { user: p.key })),
      false,
    )
    await changed()
    const bad = answer.results.filter((r) => r.status !== 'ok' && r.status !== 'already_member')
    if (bad.length > 0)
      throw new Error(bad.map((r) => `${r.name || r.user}: ${tp.status[r.status] ?? r.status}`).join('; '))
  }
}
