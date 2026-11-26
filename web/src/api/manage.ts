// What storage management uses (docs/api.md, "Storage management"): the people's list, the recycle bin, the
// previews of a deletion, the confirmation token and the bulk jobs, the exports and the audited view of a space.
// Kept apart from admin.ts, which is the older half of the administration's calls.

import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, enc, request } from './client'
import { keys } from './hooks'
import type {
  ConfirmRequest,
  ConfirmResult,
  ExportList,
  ExportRequest,
  Job,
  JobList,
  JobsParams,
  PeopleParams,
  RecycleItem,
  RecycleList,
  RestoreResult,
  SpaceDeletionPreview,
  SpaceView,
  StoragePeopleList,
  StoragePersonDetail,
  UserDeletionPlan,
} from './types'

const query = (o: Record<string, string | number | boolean | undefined>) => {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(o)) {
    if (v === undefined || v === '' || v === false) continue
    q.set(k, v === true ? '1' : String(v))
  }
  const s = q.toString()
  return s ? `?${s}` : ''
}

export const manageApi = {
  // --- people and what they hold ---
  people: (p: PeopleParams = {}, signal?: AbortSignal) =>
    request<StoragePeopleList>(
      `/admin/storage/people${query({ tenant: p.tenant, q: p.q, sort: p.sort, order: p.order, cursor: p.cursor, limit: p.limit, dormant: p.dormant, remote: p.remote, deadline: p.deadline, inactiveDays: p.inactiveDays, disabled: p.disabled === undefined ? undefined : p.disabled ? 1 : 0 })}`,
      { signal },
    ),
  person: (id: string, signal?: AbortSignal) =>
    request<StoragePersonDetail>(`/admin/storage/people/${enc(id)}`, {
      signal,
    }),

  // --- the recycle bin ---
  recycle: (
    p: {
      tenant?: string
      kind?: string
      q?: string
      cursor?: string
      limit?: number
    } = {},
    signal?: AbortSignal,
  ) => request<RecycleList>(`/admin/recycle${query(p)}`, { signal }),
  restore: (item: string, owner?: string) =>
    request<RestoreResult>(`/admin/recycle/${enc(item)}/restore`, {
      method: 'POST',
      body: owner ? { owner } : {},
    }),
  // Purges now; site administrators only. `confirm` is a token made for this item by `confirm`.
  purge: (item: string, confirm: string) =>
    request<RecycleItem>(`/admin/recycle/${enc(item)}?confirm=${enc(confirm)}`, { method: 'DELETE' }),

  // --- what a deletion would do ---
  spaceDeletion: (id: string, signal?: AbortSignal) =>
    request<SpaceDeletionPreview>(`/admin/spaces/${enc(id)}/deletion`, {
      signal,
    }),
  userDeletion: (id: string, heir?: string, signal?: AbortSignal) =>
    request<UserDeletionPlan>(`/admin/users/${enc(id)}/deletion${query({ heir })}`, { signal }),
  deleteUser: (
    id: string,
    input: {
      heir?: string
      transfers?: Record<string, string>
      reason?: string
    } = {},
  ) =>
    request<RecycleItem>(`/users/${enc(id)}`, {
      method: 'DELETE',
      body: input,
    }),

  // --- the second confirmation, and the bulk actions ---
  confirm: (input: ConfirmRequest) => request<ConfirmResult>('/admin/confirm', { method: 'POST', body: input }),
  bulk: (confirm: string) =>
    request<{ job: Job }>('/admin/bulk', {
      method: 'POST',
      body: { confirm },
    }),
  job: (id: string, signal?: AbortSignal) => request<{ job: Job }>(`/admin/jobs/${enc(id)}`, { signal }),
  jobs: (p: JobsParams = {}, signal?: AbortSignal) => request<JobList>(`/admin/jobs${query({ ...p })}`, { signal }),

  // --- exports ---
  createExport: (input: ExportRequest) => request<{ job: Job }>('/admin/exports', { method: 'POST', body: input }),
  exports: (cursor?: string, signal?: AbortSignal) =>
    request<ExportList>(`/admin/exports${query({ cursor })}`, { signal }),
  deleteExport: (id: string) => request<void>(`/admin/exports/${enc(id)}`, { method: 'DELETE' }),

  // --- the audited, read-only view of a space ---
  startView: (space: string, reason: string) =>
    request<SpaceView>(`/admin/spaces/${enc(space)}/view`, {
      method: 'POST',
      body: { reason },
    }),
  endView: (space: string) => request<void>(`/admin/spaces/${enc(space)}/view`, { method: 'DELETE' }),

  // --- viewing the app as a person (docs/api.md, "Viewing as a person") ---
  startViewAs: (user: string, reason: string) =>
    request<{ id: string; until: string }>(`/admin/users/${enc(user)}/view-as`, { method: 'POST', body: { reason } }),
  endViewAs: (user: string) => request<void>(`/admin/users/${enc(user)}/view-as`, { method: 'DELETE' }),
}

// The address a ready export is downloaded from: a plain link, so the browser saves it.
export const exportDownloadUrl = (id: string) => `/api/admin/exports/${enc(id)}/download`

export const MANAGE_PAGE_SIZE = 50
export const PEOPLE_KEY = ['admin', 'storage', 'people'] as const
const RECYCLE_KEY = ['admin', 'recycle'] as const
const EXPORTS_KEY = ['admin', 'exports'] as const
const JOB_KEY = ['admin', 'jobs'] as const

// How often a running job or export is asked about (a setting so tests need not wait the real second).
export const pollMs = { value: 1000 }

export function usePeople(params: Omit<PeopleParams, 'cursor' | 'limit'>, enabled = true) {
  return useInfiniteQuery({
    queryKey: [...PEOPLE_KEY, 'list', JSON.stringify(params)],
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) =>
      manageApi.people({ ...params, cursor: pageParam || undefined, limit: MANAGE_PAGE_SIZE }, signal),
    getNextPageParam: (last) => last.next || undefined,
    enabled,
    staleTime: 10_000,
  })
}
// One page of the people list, as the server cuts it for this search, filter, sort and page size.
export function usePeoplePage(params: PeopleParams) {
  return useQuery({
    queryKey: [...PEOPLE_KEY, 'page', JSON.stringify(params)],
    queryFn: ({ signal }) => manageApi.people(params, signal),
    staleTime: 10_000,
    placeholderData: (prev) => prev,
  })
}
export function usePerson(id: string, tenant?: string) {
  return useQuery({
    queryKey: [...PEOPLE_KEY, 'one', id, tenant ?? ''],
    queryFn: ({ signal }) => manageApi.person(id, signal),
    enabled: id !== '',
    staleTime: 5_000,
  })
}

// An item that is being moved, restored or purged shows its job (`item.job`, see useJob), which reads the list again
// when it ends: the list itself is not polled.
export function useRecycle(params: { tenant?: string; kind?: string; q?: string }) {
  return useInfiniteQuery({
    queryKey: [...RECYCLE_KEY, 'list', JSON.stringify(params)],
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) =>
      manageApi.recycle({ ...params, cursor: pageParam || undefined, limit: MANAGE_PAGE_SIZE }, signal),
    getNextPageParam: (last) => last.next || undefined,
    staleTime: 5_000,
  })
}

// Everything that moves a space or a person between the live tables and the bin changes these.
export function useStorageChanged() {
  const qc = useQueryClient()
  return () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: RECYCLE_KEY }),
      qc.invalidateQueries({ queryKey: keys.adminStorage }),
      qc.invalidateQueries({ queryKey: keys.adminSpaces }),
      qc.invalidateQueries({ queryKey: keys.spaces }),
      qc.invalidateQueries({ queryKey: keys.users }),
      qc.invalidateQueries({ queryKey: keys.adminStatus }),
      qc.invalidateQueries({ queryKey: ['admin', 'usage'] }),
    ])
}

export function useRestore() {
  const changed = useStorageChanged()
  return useMutation({
    mutationFn: ({ item, owner }: { item: string; owner?: string }) => manageApi.restore(item, owner),
    onSuccess: () => changed(),
  })
}
export function usePurge() {
  const changed = useStorageChanged()
  return useMutation({
    mutationFn: ({ item, confirm }: { item: string; confirm: string }) => manageApi.purge(item, confirm),
    onSuccess: () => changed(),
  })
}

export function useSpaceDeletion(id: string) {
  // Read afresh each time the dialog opens: what a deletion does is only true now.
  return useQuery({
    queryKey: ['admin', 'preview', 'space', id],
    queryFn: ({ signal }) => manageApi.spaceDeletion(id, signal),
    staleTime: 0,
    gcTime: 0,
  })
}
export function useUserDeletion(id: string, heir: string) {
  return useQuery({
    queryKey: ['admin', 'preview', 'user', id, heir],
    queryFn: ({ signal }) => manageApi.userDeletion(id, heir || undefined, signal),
    staleTime: 0,
    gcTime: 0,
  })
}
export function useDeleteUser() {
  const changed = useStorageChanged()
  return useMutation({
    mutationFn: ({
      id,
      ...input
    }: {
      id: string
      heir?: string
      transfers?: Record<string, string>
      reason?: string
    }) => manageApi.deleteUser(id, input),
    onSuccess: () => changed(),
  })
}

export function useConfirm() {
  return useMutation({
    mutationFn: (input: ConfirmRequest) => manageApi.confirm(input),
  })
}
export function useStartBulk() {
  return useMutation({
    mutationFn: (confirm: string) => manageApi.bulk(confirm),
  })
}
export const jobActive = (j: { state: string } | undefined) => !!j && (j.state === 'queued' || j.state === 'running')
// A refusal (403: no key for the kind; 404: no such job, or not the caller's) is not asked again: it would answer the
// same. A 429 and the server's own failures are asked again at the next tick.
const refused = (e: unknown) => e instanceof ApiError && e.status >= 400 && e.status < 500 && e.status !== 429

// A job is asked about while it is queued or running, and once more when it ends (so what it did shows: the lists it
// changed, the recycle bin and the exports, are read again then). It is the one poller of every kind of job.
export function useJob(id: string | null) {
  const changed = useStorageChanged()
  const qc = useQueryClient()
  return useQuery({
    queryKey: [...JOB_KEY, id ?? ''],
    enabled: !!id,
    queryFn: async ({ signal }) => {
      const { job } = await manageApi.job(id!, signal)
      if (!jobActive(job)) {
        void changed()
        void qc.invalidateQueries({ queryKey: EXPORTS_KEY })
      }
      return job
    },
    refetchInterval: (q) =>
      refused(q.state.error) || (q.state.data && !jobActive(q.state.data)) ? false : pollMs.value,
    staleTime: 0,
  })
}

// A page of jobs (newest first), read again only while one on it is queued or running.
export function useJobs(params: Omit<JobsParams, 'cursor' | 'limit'>, enabled = true) {
  return useInfiniteQuery({
    queryKey: [...JOB_KEY, 'list', JSON.stringify(params)],
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) =>
      manageApi.jobs({ ...params, cursor: pageParam || undefined, limit: MANAGE_PAGE_SIZE }, signal),
    getNextPageParam: (last) => last.next || undefined,
    enabled,
    staleTime: 0,
    refetchInterval: (q) =>
      !refused(q.state.error) && q.state.data?.pages.some((p) => p.items.some(jobActive)) ? pollMs.value : false,
  })
}

export function useExports(enabled = true) {
  return useInfiniteQuery({
    queryKey: [...EXPORTS_KEY, 'list'],
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) => manageApi.exports(pageParam || undefined, signal),
    getNextPageParam: (last) => last.next || undefined,
    enabled,
    staleTime: 5_000,
  })
}
export function useCreateExport() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: ExportRequest) => manageApi.createExport(input),
    onSuccess: () => qc.invalidateQueries({ queryKey: EXPORTS_KEY }),
  })
}
export function useDeleteExport() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => manageApi.deleteExport(id),
    onSuccess: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: EXPORTS_KEY }),
        qc.invalidateQueries({ queryKey: keys.adminStorage }),
      ]),
  })
}
