// What only an administrator's pages use: the calls and the hooks over them. A
// module of its own so the administration, which loads on demand, carries them and
// the main bundle does not. (One space an administrator opens without being a member
// of it is `api.adminSpace`: the space list needs it.)

import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, enc, request } from './client'
import { keys } from './hooks'
import type { AdminDaily, AdminSpaceList, DailyAllow, AdminSpacesParams, CreatedInvite, DailyApi, DailyDefault, DailyTryResult, DeletedSpace, Invite, LinesFormat, Role, ServerStatus, SpaceSize, User } from './types'

export const adminApi = {
  // --- users and invites ---
  // `q` keeps the users whose username or name contains it; `limit` caps the count (1 to 200);
  // `enabled` leaves out the disabled accounts before the limit counts.
  users: (params: { q?: string; limit?: number; enabled?: boolean } = {}, signal?: AbortSignal) => {
    const q = new URLSearchParams()
    if (params.q) q.set('q', params.q)
    if (params.limit) q.set('limit', String(params.limit))
    if (params.enabled) q.set('enabled', '1')
    const query = q.toString()
    return request<{ users: User[] }>(`/users${query ? `?${query}` : ''}`, { signal })
  },
  updateUser: (id: string, input: { role?: Role; disabled?: boolean }) => request<{ user: User }>(`/users/${enc(id)}`, { method: 'PATCH', body: input }),
  createInvite: (input: { role?: Role; days?: number } = {}) => request<CreatedInvite>('/invites', { method: 'POST', body: input }),
  invites: (signal?: AbortSignal) => request<{ invites: Invite[] }>('/invites', { signal }),
  revokeInvite: (id: string) => request<void>(`/invites/${enc(id)}`, { method: 'DELETE' }),

  // --- spaces ---
  adminSpaces: (params: AdminSpacesParams = {}, signal?: AbortSignal) => {
    const q = new URLSearchParams()
    if (params.q) q.set('q', params.q)
    if (params.cursor) q.set('cursor', params.cursor)
    if (params.limit) q.set('limit', String(params.limit))
    const qs = q.toString()
    return request<AdminSpaceList>(`/admin/spaces${qs ? `?${qs}` : ''}`, { signal })
  },
  // Measures the space's directory now.
  adminResizeSpace: (id: string) => request<SpaceSize>(`/admin/spaces/${enc(id)}/size`, { method: 'POST' }),
  adminTransferSpace: (id: string, userId: string) => request<void>(`/admin/spaces/${enc(id)}/transfer`, { method: 'POST', body: { userId } }),
  adminDisableSpace: (id: string) => request<void>(`/admin/spaces/${enc(id)}/disable`, { method: 'POST' }),
  adminEnableSpace: (id: string) => request<void>(`/admin/spaces/${enc(id)}/enable`, { method: 'POST' }),
  adminDeleteSpace: (id: string) => request<DeletedSpace>(`/admin/spaces/${enc(id)}`, { method: 'DELETE' }),
  adminStatus: (signal?: AbortSignal) => request<ServerStatus>('/admin/status', { signal }),

  // --- the daily text of the whole server ---
  daily: (signal?: AbortSignal) => request<AdminDaily>('/admin/daily', { signal }),
  putDaily: (input: { default?: DailyDefault; hosts?: string[]; languages?: string[]; allow?: DailyAllow }) => request<AdminDaily>('/admin/daily', { method: 'PUT', body: input }),
  // A header with a `value` sets it; one with only a `name` keeps what is saved; a header left out is removed.
  putDailyApi: (api: Omit<DailyApi, 'note'>) => request<AdminDaily>(`/admin/daily/apis/${enc(api.id)}`, { method: 'PUT', body: api }),
  deleteDailyApi: (id: string) => request<AdminDaily>(`/admin/daily/apis/${enc(id)}`, { method: 'DELETE' }),
  // A refused trial is a 400 that carries what the trial showed in `result`; it reads as a trial with errors.
  tryDailyApi: async (api: Omit<DailyApi, 'note'>, lang: string, date: string): Promise<DailyTryResult> => {
    try {
      return await request<DailyTryResult>('/admin/daily/try', { method: 'POST', body: { api, lang, date } })
    } catch (e) {
      const b = e instanceof ApiError && e.status === 400 ? (e.body as { result?: unknown; error?: string; field?: string; host?: string; header?: string } | undefined) : undefined
      if (b && typeof b.result === 'object' && b.result !== null) return { ...(b.result as DailyTryResult), error: b.error, field: b.field, host: b.host, header: b.header }
      throw e
    }
  },
  putDailyLines: (input: { lang: string; text: string; format: LinesFormat }) => request<AdminDaily>('/admin/daily/lines', { method: 'PUT', body: input }),
  deleteDailyLines: (lang: string) => request<AdminDaily>(`/admin/daily/lines?lang=${enc(lang)}`, { method: 'DELETE' }),
}

// --- users and invites ----------------------------------------------------------

export function useUsers(enabled = true) {
  return useQuery({ queryKey: keys.users, queryFn: ({ signal }) => adminApi.users({}, signal), staleTime: 30_000, enabled })
}

// A few users by (part of) a username or name, for a picker: the server does the
// filtering and the limit (disabled accounts never count), so the whole list of
// accounts is never loaded for it.
export const USER_SEARCH_LIMIT = 20
export function useUserSearch(q: string, enabled = true) {
  return useQuery({ queryKey: keys.userSearch(q), queryFn: ({ signal }) => adminApi.users({ q, limit: USER_SEARCH_LIMIT, enabled: true }, signal), staleTime: 15_000, enabled })
}

export function useInvites(enabled = true) {
  return useQuery({ queryKey: keys.invites, queryFn: ({ signal }) => adminApi.invites(signal), staleTime: 30_000, enabled })
}

// --- administration ----------------------------------------------------------------

export const ADMIN_PAGE_SIZE = 50

// Every space on the server, a cursor page at a time; `q` is the name filter.
export function useAdminSpaces(q: string, enabled = true) {
  return useInfiniteQuery({
    queryKey: keys.adminSpaceList(q),
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) => adminApi.adminSpaces({ q: q || undefined, cursor: pageParam || undefined, limit: ADMIN_PAGE_SIZE }, signal),
    getNextPageParam: (last) => last.next || undefined,
    enabled,
    staleTime: 15_000,
  })
}

export function useAdminStatus(enabled = true) {
  return useQuery({ queryKey: keys.adminStatus, queryFn: ({ signal }) => adminApi.adminStatus(signal), enabled, staleTime: 5_000 })
}

// What an administrator does to a space changes the administration's list, and
// what the members see of it (the space list: a marker, a new owner, gone).
function useAdminSpaceChanged() {
  const qc = useQueryClient()
  return (space: string) =>
    Promise.all([
      qc.invalidateQueries({ queryKey: keys.adminSpaces }),
      qc.invalidateQueries({ queryKey: keys.spaces }),
      qc.invalidateQueries({ queryKey: keys.members(space) }),
      qc.invalidateQueries({ queryKey: keys.adminStatus }),
    ])
}

// Measuring answers with the one space's numbers: they are put into the cached
// pages, so the table does not reload to show them.
export function useResizeSpace() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => adminApi.adminResizeSpace(id),
    onSuccess: (size, id) => {
      const patch = (list: AdminSpaceList) => ({ ...list, items: list.items.map((s) => (s.id === id ? { ...s, ...size } : s)) })
      qc.setQueriesData<{ pages: AdminSpaceList[]; pageParams: unknown[] }>({ queryKey: keys.adminSpaces }, (prev) => prev && 'pages' in prev ? { ...prev, pages: prev.pages.map(patch) } : prev)
    },
  })
}
export function useTransferSpace() {
  const changed = useAdminSpaceChanged()
  return useMutation({ mutationFn: ({ id, userId }: { id: string; userId: string }) => adminApi.adminTransferSpace(id, userId), onSuccess: (_r, { id }) => changed(id) })
}
export function useSetSpaceDisabled() {
  const changed = useAdminSpaceChanged()
  return useMutation({ mutationFn: ({ id, disabled }: { id: string; disabled: boolean }) => (disabled ? adminApi.adminDisableSpace(id) : adminApi.adminEnableSpace(id)), onSuccess: (_r, { id }) => changed(id) })
}
export function useDeleteSpace() {
  const changed = useAdminSpaceChanged()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => adminApi.adminDeleteSpace(id),
    onSuccess: (_r, id) => {
      qc.removeQueries({ queryKey: keys.space(id) })
      return changed(id)
    },
  })
}

// --- the daily text of the whole server ----------------------------------------------

export function useAdminDaily(enabled = true) {
  return useQuery({ queryKey: keys.adminDaily, queryFn: ({ signal }) => adminApi.daily(signal), enabled, staleTime: 15_000 })
}

// Every write answers with the settings as they are now, which replace the cached ones.
function useDailyWrite<V>(write: (v: V) => Promise<AdminDaily>) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: write,
    onSuccess: (data) => {
      qc.setQueryData(keys.adminDaily, data)
      // What spaces may choose from, and what they show, comes from these.
      void qc.invalidateQueries({ predicate: (q) => q.queryKey[0] === 'space' && String(q.queryKey[2]).startsWith('daily') })
    },
  })
}
export const useSaveDailySite = () => useDailyWrite((input: { default?: DailyDefault; hosts?: string[]; languages?: string[]; allow?: DailyAllow }) => adminApi.putDaily(input))
export const useSaveDailyApi = () => useDailyWrite((api: Omit<DailyApi, 'note'>) => adminApi.putDailyApi(api))
export const useDeleteDailyApi = () => useDailyWrite((id: string) => adminApi.deleteDailyApi(id))
export const useSaveDailyLines = () => useDailyWrite((input: { lang: string; text: string; format: LinesFormat }) => adminApi.putDailyLines(input))
export const useDeleteDailyLines = () => useDailyWrite((lang: string) => adminApi.deleteDailyLines(lang))
export function useTryDailyApi() {
  return useMutation({ mutationFn: ({ api, lang, date }: { api: Omit<DailyApi, 'note'>; lang: string; date: string }) => adminApi.tryDailyApi(api, lang, date) })
}
