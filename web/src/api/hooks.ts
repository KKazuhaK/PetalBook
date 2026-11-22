// TanStack Query hooks over the API client.

import { useMemo } from 'react'
import { useMatch } from 'react-router'
import { useInfiniteQuery, useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { waitForSaves } from '@/editor/saveController'
import { spaceUrls } from '@/lib/urls'
import { api, ApiError, spaceApi } from './client'
import { parseTagColor } from '../lib/tagColors.ts'
import type { BatchPagesInput, ConnectRemoteInput, CreatePageInput, ListParams, Remote, SessionInfo, SpaceInfo, SyncState, TagColor, TagsResponse, User } from './types'

// Everything of one space is under ['space', id, …], so switching space never
// shows another space's data and one invalidation covers a space.
export const keys = {
  session: ['session'] as const,
  spaces: ['spaces'] as const,
  account: ['account'] as const,
  users: ['users'] as const,
  invites: ['invites'] as const,
  space: (space: string) => ['space', space] as const,
  tree: (space: string) => ['space', space, 'tree'] as const,
  tags: (space: string) => ['space', space, 'tags'] as const,
  pages: (space: string) => ['space', space, 'pages'] as const,
  list: (space: string, params: ListParams) => ['space', space, 'pages', 'list', params] as const,
  page: (space: string, id: string) => ['space', space, 'page', id] as const,
  history: (space: string, id: string) => ['space', space, 'history', id, 'list'] as const,
  version: (space: string, id: string, commit: string) => ['space', space, 'history', id, 'version', commit] as const,
  remote: (space: string) => ['space', space, 'remote'] as const,
}

export const PAGE_SIZE = 50
export const HISTORY_PAGE_SIZE = 30

export const syncOf = (s: SpaceInfo | undefined): SyncState => s?.sync ?? 'local'

// --- session and spaces ------------------------------------------------------

// Fetched once and kept: it only changes when this app signs in or out, or a
// request comes back 401 (see App.tsx), and each of those updates the cache.
export function useSession() {
  return useQuery({ queryKey: keys.session, queryFn: ({ signal }) => api.session(signal), staleTime: Infinity, gcTime: Infinity })
}

// Changes with create and rename, which update the cache themselves. While a
// space follows a remote its sync state moves on the server (it fetches every
// minute), so the list is read again about as often; with only local spaces
// nothing is ever asked twice.
export function useSpaces() {
  return useQuery({
    queryKey: keys.spaces,
    queryFn: ({ signal }) => api.spaces(signal),
    staleTime: Infinity,
    refetchInterval: (q) => (q.state.data?.spaces.some((s) => syncOf(s) !== 'local') ? 60_000 : false),
  })
}

const LAST_SPACE_KEY = 'pb.space'
export function rememberSpace(id: string) {
  try {
    localStorage.setItem(LAST_SPACE_KEY, id)
  } catch {
    // Blocked storage: the next visit starts from the first space.
  }
}
export function lastSpace(): string | null {
  try {
    return localStorage.getItem(LAST_SPACE_KEY)
  } catch {
    return null
  }
}

// The space in the address, if the address is inside one.
export function useRouteSpace(): string | null {
  const m = useMatch({ path: '/s/:space', end: false })
  return m?.params.space ?? null
}

// The space whose pages are shown: the one in the address, else (on the
// account pages, which have none) the one used last if it still exists, else
// the first.
export function useActiveSpace(): { id: string | null; spaces: SpaceInfo[] } {
  const route = useRouteSpace()
  const spaces = useSpaces().data?.spaces ?? []
  // The storage is only read when the address has no space.
  const last = route ? null : lastSpace()
  const id = route ?? (last && spaces.some((s) => s.id === last) ? last : (spaces[0]?.id ?? null))
  return { id, spaces }
}

// The same as a string, for the hooks below: '' while the list of spaces is
// still on its way (their queries wait for it).
export function useSpaceId(): string {
  return useActiveSpace().id ?? ''
}

// Addresses of the space being shown.
export function useUrls() {
  const space = useSpaceId()
  return useMemo(() => spaceUrls(space), [space])
}

// What changes when someone signs in or out: nothing cached may carry over to
// another person.
export function applySession(qc: QueryClient, user: User | null) {
  const prev = qc.getQueryData<SessionInfo>(keys.session)
  qc.clear()
  qc.setQueryData<SessionInfo>(keys.session, { ...(prev ?? { mode: 'accounts' as const }), user, setup: false })
}

// --- space data ----------------------------------------------------------------

export function useSpaceApi() {
  const space = useSpaceId()
  return useMemo(() => spaceApi(space), [space])
}

export function useTree() {
  const space = useSpaceId()
  return useQuery({ queryKey: keys.tree(space), queryFn: ({ signal }) => spaceApi(space).tree(signal), staleTime: 15_000, enabled: space !== '' })
}

export function useTags() {
  const space = useSpaceId()
  return useQuery({ queryKey: keys.tags(space), queryFn: ({ signal }) => spaceApi(space).tags(signal), staleTime: 15_000, enabled: space !== '' })
}

// Tag name -> colour, for every tag that has one. Built from the tags listing
// the sidebar already asks for, so using it costs no request of its own; an
// empty map until that has arrived. Memoised on the data, so it is safe in a
// dependency list.
export function useTagColors(): ReadonlyMap<string, TagColor> {
  const { data } = useTags()
  return useMemo(() => {
    const m = new Map<string, TagColor>()
    for (const t of data?.tags ?? []) {
      const c = parseTagColor(t.color)
      if (c) m.set(t.tag, c.kind === 'preset' ? c.name : c.hex)
    }
    return m
  }, [data])
}

// Sets a tag's colour, or removes it with null. The cached listing is changed
// on the spot, so every chip repaints without another request.
export function useSetTagColor() {
  const qc = useQueryClient()
  const space = useSpaceId()
  return useMutation({
    mutationFn: ({ tag, color }: { tag: string; color: TagColor | null }) => spaceApi(space).setTagColor(tag, color),
    onSuccess: (res, { tag }) =>
      qc.setQueryData<TagsResponse>(keys.tags(space), (prev) => {
        if (!prev) return prev
        const found = prev.tags.some((t) => t.tag === tag)
        // A tag in no page is not in the listing; nothing to repaint.
        if (!found) return prev
        return { tags: prev.tags.map((t) => (t.tag === tag ? { tag: t.tag, pages: t.pages, ...(res.color ? { color: res.color } : {}) } : t)) }
      }),
  })
}

// Listings are fetched one cursor page at a time; nothing renders more rows
// than the person has asked for with "load more".
export function usePageList(params: Omit<ListParams, 'cursor' | 'limit'>) {
  const space = useSpaceId()
  return useInfiniteQuery({
    queryKey: keys.list(space, params),
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) => spaceApi(space).listPages({ ...params, limit: PAGE_SIZE, cursor: pageParam || undefined }, signal),
    getNextPageParam: (last) => last.next || undefined,
    staleTime: 10_000,
  })
}

// The editor starts from what the server has right now (the file and the
// draft, if there is one): no cache is kept once nobody is looking at the
// page, and a request still on its way is waited for so what is read here is
// what it produced.
export function usePage(space: string, id: string) {
  return useQuery({
    queryKey: keys.page(space, id),
    queryFn: async ({ signal }) => {
      await waitForSaves(space, id)
      return spaceApi(space).getPage(id, signal)
    },
    gcTime: 0,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    retry: (count, e) => !(e as { status?: number }).status && count < 2,
  })
}

// Pages move between listings, folder counts and tag counts when a file is
// written. Storing a draft does not: lists show the file.
export function invalidateListings(qc: QueryClient, space: string) {
  return Promise.all([qc.invalidateQueries({ queryKey: keys.pages(space) }), qc.invalidateQueries({ queryKey: keys.tree(space) }), qc.invalidateQueries({ queryKey: keys.tags(space) })])
}

export function useCreatePage() {
  const qc = useQueryClient()
  const space = useSpaceId()
  return useMutation({
    mutationFn: (input: CreatePageInput) => spaceApi(space).createPage(input),
    onSuccess: () => invalidateListings(qc, space),
  })
}

export function useMovePage() {
  const qc = useQueryClient()
  const space = useSpaceId()
  return useMutation({
    mutationFn: ({ id, folder }: { id: string; folder: string }) => spaceApi(space).movePage(id, folder),
    onSuccess: () => invalidateListings(qc, space),
  })
}

export function useDeletePage() {
  const qc = useQueryClient()
  const space = useSpaceId()
  return useMutation({
    mutationFn: (id: string) => spaceApi(space).deletePage(id),
    onSuccess: () => invalidateListings(qc, space),
  })
}

// Several pages at once (docs/api.md, `pages/batch`): move or delete.
export function useBatchPages() {
  const qc = useQueryClient()
  const space = useSpaceId()
  return useMutation({
    mutationFn: (input: BatchPagesInput) => spaceApi(space).batchPages(input),
    onSuccess: () => invalidateListings(qc, space),
  })
}

// A folder moves with its pages, which keep their ids; their paths change, so
// every listing and the tree are read again.
export function useMoveFolder() {
  const qc = useQueryClient()
  const space = useSpaceId()
  return useMutation({
    mutationFn: ({ from, to }: { from: string; to: string }) => spaceApi(space).moveFolder(from, to),
    onSuccess: () => invalidateListings(qc, space),
  })
}

export function useDeleteFolder() {
  const qc = useQueryClient()
  const space = useSpaceId()
  return useMutation({
    mutationFn: ({ path, recursive }: { path: string; recursive?: boolean }) => spaceApi(space).deleteFolder(path, recursive),
    onSuccess: () => invalidateListings(qc, space),
  })
}

export function useCreateFolder() {
  const qc = useQueryClient()
  const space = useSpaceId()
  return useMutation({
    mutationFn: (path: string) => spaceApi(space).createFolder(path),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.tree(space) }),
  })
}

// --- spaces, account, users ----------------------------------------------------

export function useCreateSpace() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: string | { name: string; inbox?: string }) => (typeof input === 'string' ? api.createSpace(input) : api.createSpace(input.name, input.inbox)),
    // Another space, in the same list order the server gives: personal first, then by name.
    onSuccess: (space) =>
      qc.setQueryData<{ spaces: SpaceInfo[] }>(keys.spaces, (prev) => ({
        spaces: [...(prev?.spaces ?? []), space].sort((a, b) => Number(b.personal) - Number(a.personal) || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0)),
      })),
  })
}

export function useRenameSpace() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, name }: { id: string; name: string }) => api.renameSpace(id, name),
    onSuccess: (space) => qc.setQueryData<{ spaces: SpaceInfo[] }>(keys.spaces, (prev) => ({ spaces: (prev?.spaces ?? []).map((s) => (s.id === space.id ? space : s)) })),
  })
}

// Changes the name and/or the inbox folder of a space.
export function useUpdateSpace() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, ...patch }: { id: string; name?: string; inbox?: string }) => api.updateSpace(id, patch),
    onSuccess: (space) => qc.setQueryData<{ spaces: SpaceInfo[] }>(keys.spaces, (prev) => ({ spaces: (prev?.spaces ?? []).map((s) => (s.id === space.id ? space : s)) })),
  })
}

export function useAccount(enabled = true) {
  return useQuery({ queryKey: keys.account, queryFn: ({ signal }) => api.account(signal), staleTime: Infinity, enabled })
}

export function useUsers(enabled = true) {
  return useQuery({ queryKey: keys.users, queryFn: ({ signal }) => api.users(signal), staleTime: 30_000, enabled })
}

export function useInvites(enabled = true) {
  return useQuery({ queryKey: keys.invites, queryFn: ({ signal }) => api.invites(signal), staleTime: 30_000, enabled })
}

// What a person is told about a failed request, for the pages that show errors inline.
export function errorMessage(e: unknown, fallback: string): string {
  if (e instanceof ApiError) return e.code === 'network' ? e.message : e.message || fallback
  return fallback
}

// --- history ---------------------------------------------------------------------

// The versions of one page, newest first, a cursor page at a time. Nothing is
// fetched until `enabled` (the panel is open); the list is read again each time
// it opens, because the page may have been saved since.
export function useHistory(space: string, id: string, enabled: boolean) {
  return useInfiniteQuery({
    queryKey: keys.history(space, id),
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) => spaceApi(space).history(id, { limit: HISTORY_PAGE_SIZE, cursor: pageParam || undefined }, signal),
    getNextPageParam: (last) => last.next || undefined,
    enabled,
    staleTime: 0,
    gcTime: 5 * 60_000,
  })
}

// One version. A commit never changes, so it is kept as it is for as long as
// it is likely to be looked at again, and never refetched.
export function useVersion(space: string, id: string, commit: string | null) {
  return useQuery({
    queryKey: keys.version(space, id, commit ?? ''),
    queryFn: ({ signal }) => spaceApi(space).version(id, commit!, signal),
    enabled: commit !== null,
    staleTime: Infinity,
    gcTime: 30 * 60_000,
  })
}

// --- remote repository -----------------------------------------------------------

// `null` while the space is local (the server answers 404).
export function useRemote(space: string, enabled = true) {
  return useQuery({
    queryKey: keys.remote(space),
    queryFn: async ({ signal }): Promise<Remote | null> => {
      try {
        return await spaceApi(space).getRemote(signal)
      } catch (e) {
        if (e instanceof ApiError && e.code === 'not_found') return null
        throw e
      }
    },
    enabled: enabled && space !== '',
    staleTime: 10_000,
  })
}

// What the server says about the remote also says what the space list shows
// next to the space name.
function useRemoteCache(space: string) {
  const qc = useQueryClient()
  return (remote: Remote | null) => {
    qc.setQueryData(keys.remote(space), remote)
    qc.setQueryData<{ spaces: SpaceInfo[] }>(keys.spaces, (prev) => prev && { spaces: prev.spaces.map((s) => (s.id === space ? { ...s, sync: remote ? remote.state : 'local' } : s)) })
  }
}

// For a mutation whose variables hold a secret (a token). A finished mutation is
// otherwise kept in the cache, variables included, for minutes; with this it is
// dropped as soon as nothing shows it.
export const secretMutation = { gcTime: 0 } as const

export function useConnectRemote(space: string) {
  const put = useRemoteCache(space)
  const qc = useQueryClient()
  return useMutation({
    ...secretMutation,
    mutationFn: (input: ConnectRemoteInput) => spaceApi(space).connectRemote(input),
    onSuccess: (remote) => {
      put(remote)
      // A space that had nothing takes the remote's pages; one that was behind moves forward.
      void invalidateListings(qc, space)
    },
  })
}
export function useDisconnectRemote(space: string) {
  const put = useRemoteCache(space)
  return useMutation({ mutationFn: () => spaceApi(space).disconnectRemote(), onSuccess: () => put(null) })
}
export function useSyncRemote(space: string) {
  const put = useRemoteCache(space)
  return useMutation({ mutationFn: () => spaceApi(space).syncRemote(), onSuccess: put })
}
export function useAcceptRemote(space: string) {
  const put = useRemoteCache(space)
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (commit: string) => spaceApi(space).acceptRemote(commit),
    onSuccess: (remote) => {
      put(remote)
      // Pages, lists and counts may all have moved with the remote's history.
      void invalidateListings(qc, space)
    },
  })
}
