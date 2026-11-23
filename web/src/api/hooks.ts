// TanStack Query hooks over the API client.

import { useMemo } from 'react'
import { useMatch } from 'react-router'
import { keepPreviousData, useInfiniteQuery, useIsMutating, useMutation, useQuery, useQueryClient, type Query, type QueryClient } from '@tanstack/react-query'
import { waitForSaves } from '@/editor/saveController'
import { noteWrote, wroteKey } from '@/lib/homePrefs'
import { spaceUrls } from '@/lib/urls'
import { api, ApiError, spaceApi } from './client'
import { parseTagColor } from '../lib/tagColors.ts'
import { can, permissionsOf, type SpacePermissions } from '../lib/permissions.ts'
import { propsOfMeta } from '../lib/pageTypes.ts'
import type { BatchPagesInput, ConnectRemoteInput, DailyPrefs, DailySettings, Favorite, FavoriteList, FavoriteRef, CreatePageInput, GroupsParams, ListParams, Member, Page, PageList, PatchPropsInput, Permission, PutViewsInput, Remote, SessionInfo, SpaceInfo, SpaceRole, SyncState, TagColor, TagsResponse, TypeInput, User, ViewList, ViewScope } from './types'

// Everything of one space is under ['space', id, …], so switching space never
// shows another space's data and one invalidation covers a space.
export const keys = {
  session: ['session'] as const,
  spaces: ['spaces'] as const,
  account: ['account'] as const,
  users: ['users'] as const,
  userSearch: (q: string) => ['users', 'search', q] as const,
  invites: ['invites'] as const,
  adminSpaces: ['admin', 'spaces'] as const,
  adminSpaceList: (q: string) => ['admin', 'spaces', 'list', q] as const,
  adminStatus: ['admin', 'status'] as const,
  adminDaily: ['admin', 'daily'] as const,
  // A space an administrator opens without being a member of it.
  viewedSpace: (space: string) => ['admin', 'spaces', 'view', space] as const,
  space: (space: string) => ['space', space] as const,
  tree: (space: string) => ['space', space, 'tree'] as const,
  tags: (space: string) => ['space', space, 'tags'] as const,
  pages: (space: string) => ['space', space, 'pages'] as const,
  types: (space: string) => ['space', space, 'types'] as const,
  // The table view's listing: kept apart from the list and card listings, which
  // ask for less, and which an edit of a cell does not read again.
  table: (space: string, params: ListParams) => ['space', space, 'pages', 'table', params] as const,
  list: (space: string, params: ListParams) => ['space', space, 'pages', 'list', params] as const,
  // The saved views of a folder or a tag.
  views: (space: string, scope: ViewScope) => ['space', space, 'views', scope] as const,
  // A board's counts and the cards of one of its columns; under `pages`, so a write of a page reads them again.
  groups: (space: string, params: GroupsParams) => ['space', space, 'pages', 'groups', params] as const,
  column: (space: string, params: ListParams) => ['space', space, 'pages', 'board', params] as const,
  // Continuous reading: outside `pages`, since reading again every batch loaded (they hold whole texts) for each
  // change somewhere would be heavy; it is a snapshot that is taken afresh when the reading is opened.
  reading: (space: string, params: ListParams) => ['space', space, 'reading', params] as const,
  page: (space: string, id: string) => ['space', space, 'page', id] as const,
  // Under `pages`, so what writes a page reads them again.
  links: (space: string, id: string) => ['space', space, 'pages', 'links', id] as const,
  backlinks: (space: string, id: string) => ['space', space, 'pages', 'backlinks', id] as const,
  history: (space: string, id: string) => ['space', space, 'history', id, 'list'] as const,
  version: (space: string, id: string, commit: string) => ['space', space, 'history', id, 'version', commit] as const,
  remote: (space: string) => ['space', space, 'remote'] as const,
  members: (space: string) => ['space', space, 'members'] as const,
  // The home page: the day's text (by day, "another" and language), where it comes from, and a person's favourites.
  daily: (space: string, date: string, skip: number, lang: string) => ['space', space, 'daily', date, skip, lang] as const,
  dailySettings: (space: string) => ['space', space, 'dailySettings'] as const,
  dailyPrefs: (space: string) => ['space', space, 'dailyPrefs'] as const,
  favorites: (space: string) => ['space', space, 'favorites'] as const,
}

export const PAGE_SIZE = 50
// The table view reads its rows in batches of this many.
export const TABLE_PAGE_SIZE = 100
export const HISTORY_PAGE_SIZE = 30
// A column of a board is read this many cards at a time.
export const BOARD_PAGE_SIZE = 20
// Continuous reading takes the most the server gives with the bodies.
export const READING_PAGE_SIZE = 20

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
    // Someone else may have removed this person from a space, or deleted it: coming
    // back to the window asks once, however long it has been.
    refetchOnWindowFocus: 'always',
    refetchInterval: (q) => (q.state.data?.spaces.some((s) => syncOf(s) !== 'local') ? 60_000 : false),
  })
}

// A request inside a space was refused for access (disabled, no permission) or
// said the space is not there: what the app holds about the space is out of date,
// the list and the space an administrator views alike. A 404 may only be a page
// that is gone, so each of the two looks again at most every few seconds, counted
// from the last time that query asked (answered or failed). A 404 inside that
// window is not dropped, since the viewed space is not read again by itself: one
// recheck waits for the end of the window, whatever number of 404s come meanwhile.
// The window is a setting, not a constant, only so that tests can shorten it.
export const notFoundRecheck = { ms: 5_000 }
const pendingRechecks = new WeakMap<QueryClient, Map<string, ReturnType<typeof setTimeout>>>()
function recheck(qc: QueryClient, key: readonly unknown[], notFound: boolean) {
  const asked = () => {
    const s = qc.getQueryState(key)
    return Math.max(s?.dataUpdatedAt ?? 0, s?.errorUpdatedAt ?? 0)
  }
  const wait = notFoundRecheck.ms - (Date.now() - asked())
  if (!notFound || wait <= 0) {
    void qc.invalidateQueries({ queryKey: key })
    return
  }
  const timers = pendingRechecks.get(qc) ?? new Map<string, ReturnType<typeof setTimeout>>()
  pendingRechecks.set(qc, timers)
  const id = JSON.stringify(key)
  if (timers.has(id)) return
  const seen = asked()
  const arm = (ms: number) =>
    timers.set(
      id,
      setTimeout(() => {
        timers.delete(id)
        // Asked again meanwhile (by anything): that answer is newer than the 404.
        if (asked() !== seen) return
        // A timer may go off a little before the clock (Date.now) says the window is
        // over, since the event loop counts from when it last looked at the time, which
        // after a long task is well before now. Dropping the recheck then would leave
        // the old state up with nothing to ask again, so wait for what is left.
        const left = notFoundRecheck.ms - (Date.now() - seen)
        if (left > 0) arm(left)
        else void qc.invalidateQueries({ queryKey: key })
      }, ms),
    )
  arm(wait)
}
export function refreshSpaceAccess(qc: QueryClient, space: string | null, notFound: boolean) {
  // The viewed space is only ever asked for by an administrator, and is one request.
  if (space) recheck(qc, keys.viewedSpace(space), notFound)
  recheck(qc, keys.spaces, notFound)
}

const LAST_SPACE_KEY = 'pb.space'
export function rememberSpace(id: string) {
  try {
    localStorage.setItem(LAST_SPACE_KEY, id)
  } catch {
    // Blocked storage: the next visit starts from the first space.
  }
}
// A space the person left is not the one used last any more (an administrator could
// still open it, which is not what leaving means).
export function forgetSpace(id: string) {
  try {
    if (localStorage.getItem(LAST_SPACE_KEY) === id) localStorage.removeItem(LAST_SPACE_KEY)
  } catch {
    // Blocked storage: nothing was kept.
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
export function useActiveSpace(): { id: string | null; spaces: SpaceInfo[]; resolving: boolean } {
  const route = useRouteSpace()
  const list = useSpaces()
  const mine = list.data?.spaces ?? []
  // The storage is only read when the address has no space.
  const last = route ? null : lastSpace()
  // A space in the address (or, off the space pages, the one used last) that is not
  // the person's own may still be one an administrator can open.
  const wanted = route ?? last
  const viewed = useViewedSpace(wanted, !list.isPending && !!wanted && !mine.some((s) => s.id === wanted))
  const spaces = viewed.data ? [...mine, viewed.data] : mine
  const id = route ?? (last && spaces.some((s) => s.id === last) ? last : (spaces[0]?.id ?? null))
  return { id, spaces, resolving: list.isPending || viewed.isLoading }
}

// An administrator may do everything in every space, but a space they are not a
// member of is not in their list. The administration knows it: one request, made
// when someone opens such a space by its address. It carries no role, and every
// permission. It is read again when a request in the space is refused or says it is
// gone (see refreshSpaceAccess) and after what an administrator does to a space;
// never from the administration's lists, whose pages are only as fresh as their
// search was.
function useViewedSpace(space: string | null, wanted: boolean) {
  const session = useSession().data
  const admin = session?.mode === 'accounts' && session.user?.role === 'admin'
  return useQuery({
    queryKey: keys.viewedSpace(space ?? ''),
    enabled: wanted && admin,
    staleTime: Infinity,
    queryFn: async ({ signal }): Promise<SpaceInfo | null> => {
      try {
        const s = await api.adminSpace(space ?? '', signal)
        return { id: s.id, name: s.name, role: '', permissions: ['read', 'write', 'members', 'manage'], personal: s.personal, sync: s.sync, inbox: s.inbox, disabled: s.disabled }
      } catch (e) {
        if (e instanceof ApiError && e.code === 'not_found') return null
        throw e
      }
    },
  })
}

// What the caller may do in the space being shown. Nothing while the list of
// spaces is still on its way, and nothing in a disabled space.
export function useSpacePermissions(): SpacePermissions {
  const { id, spaces } = useActiveSpace()
  const space = spaces.find((s) => s.id === id)
  return useMemo(() => permissionsOf(space), [space])
}
export const useCan = (permission: Permission): boolean => useSpacePermissions()[permission]
export { can }

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

// A space that is switched off (or one the list does not show yet) is not asked
// for its folders and tags: every request on it would be refused.
export function useTree() {
  const space = useSpaceId()
  const { read } = useSpacePermissions()
  return useQuery({ queryKey: keys.tree(space), queryFn: ({ signal }) => spaceApi(space).tree(signal), staleTime: 15_000, enabled: space !== '' && read })
}

export function useTags() {
  const space = useSpaceId()
  const { read } = useSpacePermissions()
  return useQuery({ queryKey: keys.tags(space), queryFn: ({ signal }) => spaceApi(space).tags(signal), staleTime: 15_000, enabled: space !== '' && read })
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
export function usePageList(params: Omit<ListParams, 'cursor' | 'limit'>, enabled = true) {
  const space = useSpaceId()
  return useInfiniteQuery({
    enabled,
    queryKey: keys.list(space, params),
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) => spaceApi(space).listPages({ ...params, limit: PAGE_SIZE, cursor: pageParam || undefined }, signal),
    getNextPageParam: (last) => last.next || undefined,
    staleTime: 10_000,
  })
}

// The listing of the table view: rows with their properties and revs, a hundred
// at a time, with the sort and the filter the person chose. It is its own query so
// that the other listings stay as small as they were.
export function useTablePages(params: Omit<ListParams, 'cursor' | 'limit' | 'props'>, enabled = true) {
  const space = useSpaceId()
  const full = { ...params, props: true }
  return useInfiniteQuery({
    enabled: enabled && space !== '',
    queryKey: keys.table(space, full),
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) => spaceApi(space).listPages({ ...full, limit: TABLE_PAGE_SIZE, cursor: pageParam || undefined }, signal),
    getNextPageParam: (last) => last.next || undefined,
    staleTime: 10_000,
    // A new sort or filter keeps the rows on screen until the new ones arrive.
    placeholderData: keepPreviousData,
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

// Where the links of a page lead, for marking the broken ones and for hover text.
export function usePageLinks(id: string, enabled = true) {
  const space = useSpaceId()
  return useQuery({
    enabled: enabled && space !== '' && id !== '',
    queryKey: keys.links(space, id),
    queryFn: ({ signal }) => spaceApi(space).links(id, signal),
    staleTime: 30_000,
  })
}

// The pages that link to this one, 20 at a time; nothing is asked until `enabled`.
export function useBacklinks(id: string, enabled = true) {
  const space = useSpaceId()
  return useInfiniteQuery({
    enabled: enabled && space !== '' && id !== '',
    queryKey: keys.backlinks(space, id),
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) => spaceApi(space).backlinks(id, pageParam || undefined, signal),
    getNextPageParam: (last) => last.next || undefined,
    staleTime: 10_000,
  })
}

// Pages move between listings, folder counts and tag counts when a file is
// written. Storing a draft does not: lists show the file.
export function invalidateListings(qc: QueryClient, space: string) {
  return Promise.all([qc.invalidateQueries({ queryKey: keys.pages(space) }), qc.invalidateQueries({ queryKey: keys.tree(space) }), qc.invalidateQueries({ queryKey: keys.tags(space) })])
}

// A favourite names a page, a folder or a view: what moves or goes changes what they say (and whether they are gone).
export const invalidateFavorites = (qc: QueryClient, space: string) => qc.invalidateQueries({ queryKey: keys.favorites(space) })

export function useCreatePage() {
  const qc = useQueryClient()
  const space = useSpaceId()
  const user = useSession().data?.user?.id
  return useMutation({
    mutationFn: (input: CreatePageInput) => spaceApi(space).createPage(input),
    onSuccess: (page) => {
      // The greeting's "just wrote" goes by what this browser saved, not by the space's recent pages.
      noteWrote(wroteKey(user, space), page.id)
      return invalidateListings(qc, space)
    },
  })
}

export function useMovePage() {
  const qc = useQueryClient()
  const space = useSpaceId()
  return useMutation({
    mutationFn: ({ id, folder }: { id: string; folder: string }) => spaceApi(space).movePage(id, folder),
    onSuccess: () => Promise.all([invalidateListings(qc, space), invalidateFavorites(qc, space)]),
  })
}

export function useDeletePage() {
  const qc = useQueryClient()
  const space = useSpaceId()
  return useMutation({
    mutationFn: (id: string) => spaceApi(space).deletePage(id),
    onSuccess: () => Promise.all([invalidateListings(qc, space), invalidateFavorites(qc, space)]),
  })
}

// Several pages at once (docs/api.md, `pages/batch`): move or delete.
export function useBatchPages() {
  const qc = useQueryClient()
  const space = useSpaceId()
  return useMutation({
    mutationFn: (input: BatchPagesInput) => spaceApi(space).batchPages(input),
    onSuccess: () => Promise.all([invalidateListings(qc, space), invalidateFavorites(qc, space)]),
  })
}

// A folder moves with its pages, which keep their ids; their paths change, so
// every listing and the tree are read again.
export function useMoveFolder() {
  const qc = useQueryClient()
  const space = useSpaceId()
  return useMutation({
    mutationFn: ({ from, to }: { from: string; to: string }) => spaceApi(space).moveFolder(from, to),
    // The views of a folder go with it.
    onSuccess: () => Promise.all([invalidateListings(qc, space), invalidateFavorites(qc, space), qc.invalidateQueries({ queryKey: ['space', space, 'views'] }), qc.invalidateQueries({ queryKey: keys.dailySettings(space) })]),
  })
}

export function useDeleteFolder() {
  const qc = useQueryClient()
  const space = useSpaceId()
  return useMutation({
    mutationFn: ({ path, recursive }: { path: string; recursive?: boolean }) => spaceApi(space).deleteFolder(path, recursive),
    onSuccess: () => Promise.all([invalidateListings(qc, space), invalidateFavorites(qc, space), qc.invalidateQueries({ queryKey: ['space', space, 'views'] })]),
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
    onSuccess: (space) => spaceChanged(qc, space),
  })
}

// Changes the name and/or the inbox folder of a space.
export function useUpdateSpace() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, ...patch }: { id: string; name?: string; inbox?: string }) => api.updateSpace(id, patch),
    onSuccess: (space) => spaceChanged(qc, space),
  })
}

// A space that changed (name, inbox) in the list, and in the cache of the one an
// administrator is viewing without being a member, if that is it.
function spaceChanged(qc: QueryClient, space: SpaceInfo) {
  qc.setQueryData<{ spaces: SpaceInfo[] }>(keys.spaces, (prev) => ({ spaces: (prev?.spaces ?? []).map((s) => (s.id === space.id ? space : s)) }))
  qc.setQueryData<SpaceInfo | null | undefined>(keys.viewedSpace(space.id), (prev) => (prev ? space : prev))
}

export function useAccount(enabled = true) {
  return useQuery({ queryKey: keys.account, queryFn: ({ signal }) => api.account(signal), staleTime: Infinity, enabled })
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
    qc.setQueryData<SpaceInfo | null | undefined>(keys.viewedSpace(space), (prev) => prev && { ...prev, sync: remote ? remote.state : 'local' })
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

// --- members ---------------------------------------------------------------------

// The people of one space, owners first. Asked for only where they are shown.
export function useMembers(space: string, enabled = true) {
  return useQuery({ queryKey: keys.members(space), queryFn: ({ signal }) => spaceApi(space).members(signal), enabled: enabled && space !== '', staleTime: 10_000 })
}

// A change of members changes the list of members, and what the space list
// says of the caller (their own role and permissions, or the space itself when
// they leave).
function useMembersChanged(space: string) {
  const qc = useQueryClient()
  return () => Promise.all([qc.invalidateQueries({ queryKey: keys.members(space) }), qc.invalidateQueries({ queryKey: keys.spaces })])
}

export function useAddMember(space: string) {
  const changed = useMembersChanged(space)
  return useMutation({ mutationFn: ({ username, role }: { username: string; role: SpaceRole }) => spaceApi(space).addMember(username, role), onSuccess: changed })
}
export function useSetMemberRole(space: string) {
  const changed = useMembersChanged(space)
  return useMutation({ mutationFn: ({ user, role }: { user: string; role: SpaceRole }) => spaceApi(space).setMemberRole(user, role), onSuccess: changed })
}
export function useRemoveMember(space: string) {
  const changed = useMembersChanged(space)
  return useMutation({ mutationFn: (user: string) => spaceApi(space).removeMember(user), onSuccess: changed })
}

// Updates one member in the cached list (a role change), without asking again.
export function putMember(qc: QueryClient, space: string, member: Member) {
  qc.setQueryData<{ members: Member[] }>(keys.members(space), (prev) => prev && { members: prev.members.map((m) => (m.userId === member.userId ? member : m)) })
}

// --- page types ------------------------------------------------------------------

// The types of the space and the folders bound to them. Asked for where a page
// has a type, in the types settings, and in a folder's view.
export function useTypes(enabled = true) {
  const space = useSpaceId()
  const { read } = useSpacePermissions()
  return useQuery({ queryKey: keys.types(space), queryFn: ({ signal }) => spaceApi(space).types(signal), staleTime: 15_000, enabled: enabled && space !== '' && read })
}

// A change of a type or of a binding changes what pages are shown with: the
// types, and every summary's `effectiveType`.
function useTypesChanged() {
  const qc = useQueryClient()
  const space = useSpaceId()
  return () => Promise.all([qc.invalidateQueries({ queryKey: keys.types(space) }), qc.invalidateQueries({ queryKey: keys.pages(space) }), qc.invalidateQueries({ queryKey: ['space', space, 'page'] })])
}

export function useSaveType() {
  const space = useSpaceId()
  const changed = useTypesChanged()
  // A 409 means the type is not as it was opened: read it again.
  return useMutation({ mutationFn: ({ name, input }: { name: string; input: TypeInput }) => spaceApi(space).putType(name, input), onSuccess: changed, onError: (e) => (e instanceof ApiError && e.code === 'conflict' ? void changed() : undefined) })
}
export function useDeleteType() {
  const space = useSpaceId()
  const changed = useTypesChanged()
  return useMutation({ mutationFn: ({ name, baseRev }: { name: string; baseRev: string }) => spaceApi(space).deleteType(name, baseRev), onSuccess: changed, onError: (e) => (e instanceof ApiError && e.code === 'conflict' ? void changed() : undefined) })
}
export function useBindFolder() {
  const space = useSpaceId()
  const changed = useTypesChanged()
  return useMutation({ mutationFn: ({ folder, type }: { folder: string; type: string }) => spaceApi(space).bindFolder(folder, type), onSuccess: changed })
}

// A row with `partialProps` as the server gave them for the page (a response leaves them out when there
// are none). Only the server can say: it sees how the file spells a value, which the header as the browser
// has it does not show (0x10, a tag of its own, an integer past 2^53).
function withPartial<T extends { partialProps?: string[] }>(row: T, partial: string[] = []): T {
  const next = { ...row }
  if (partial.length) next.partialProps = partial
  else delete next.partialProps
  return next
}

// Puts a page as the server has it now into the rows of every table that holds
// it, without reading anything again (a table of thousands of rows is not
// refetched for one cell).
export function putTableRow(qc: QueryClient, space: string, page: Page) {
  const put = (prev: { pages: PageList[]; pageParams: unknown[] } | undefined) => {
    if (!prev || !prev.pages.some((p) => p.items.some((i) => i.id === page.id))) return prev
    return {
      ...prev,
      pages: prev.pages.map((p) => ({ ...p, items: p.items.map((i) => (i.id === page.id ? withPartial({ ...i, props: propsOfMeta(page.meta), rev: page.rev, updated: page.updated }, page.partialProps) : i)) })),
    }
  }
  // The cards of a board hold the same rows (with their props) as the table does.
  for (const part of ['table', 'board']) qc.setQueriesData<{ pages: PageList[]; pageParams: unknown[] }>({ queryKey: [...keys.pages(space), part] }, put)
}

// Whether a loaded column of a board can be changed by the patch: it holds the page, or its filters would now pick it.
// What the filters of a column cannot be told to exclude is taken to be affected.
function columnAffected(q: Query, id: string, input: PatchPropsInput): boolean {
  const data = q.state.data as { pages: PageList[] } | undefined
  if (data?.pages.some((p) => p.items.some((i) => i.id === id))) return true
  const params = q.queryKey[4] as ListParams | undefined
  const set = input.set ?? {}
  const keysChanged = new Set([...Object.keys(set), ...(input.unset ?? [])])
  return (params?.filter ?? []).some((f) => {
    if (!keysChanged.has(f.key)) return false
    const now = f.key in set ? String(set[f.key]) : undefined
    switch (f.op) {
      case 'in':
        return now !== undefined && (f.values ?? []).map(String).includes(now)
      case 'notIn':
        return now !== undefined && !(f.values ?? []).map(String).includes(now)
      case 'empty':
        return now === undefined
      default:
        return true
    }
  })
}

// Changes properties of one page (a table cell). The answer, or the page a 409
// brings, replaces that row; the other listings are read again by and by.
export function usePatchProps() {
  const qc = useQueryClient()
  const space = useSpaceId()
  return useMutation({
    mutationFn: ({ id, ...input }: { id: string } & PatchPropsInput) => spaceApi(space).patchProps(id, input),
    onSuccess: (page, input) => {
      putTableRow(qc, space, page)
      void qc.invalidateQueries({ queryKey: [...keys.pages(space), 'list'] })
      // A card that changed its column: the column it left and the one it joined are read again, and the counts.
      void qc.invalidateQueries({ queryKey: [...keys.pages(space), 'board'], predicate: (q) => columnAffected(q, page.id, input) })
      // The counts of the column heads change only when the property they count by, or one they filter by, was changed.
      const changed = new Set([...Object.keys(input.set ?? {}), ...(input.unset ?? [])])
      void qc.invalidateQueries({
        queryKey: [...keys.pages(space), 'groups'],
        predicate: (q) => {
          const p = q.queryKey[4] as GroupsParams | undefined
          return !p || changed.has(p.key) || (p.filter ?? []).some((f) => changed.has(f.key))
        },
      })
    },
    onError: (e) => {
      if (e instanceof ApiError && e.code === 'conflict' && e.current) {
        putTableRow(qc, space, e.current)
        void qc.invalidateQueries({ queryKey: [...keys.pages(space), 'board'] })
        void qc.invalidateQueries({ queryKey: [...keys.pages(space), 'groups'] })
      }
    },
  })
}

// --- saved views ---------------------------------------------------------------------

// The views of a folder or a tag. Anyone who may read asks; only a writer saves.
export function useViews(scope: ViewScope | null) {
  const space = useSpaceId()
  const { read } = useSpacePermissions()
  return useQuery({
    queryKey: keys.views(space, scope ?? { folder: '' }),
    queryFn: ({ signal }) => spaceApi(space).views(scope!, signal),
    enabled: scope !== null && space !== '' && read,
    staleTime: 15_000,
  })
}

// What a write of the views of one folder or tag is known by: while one is on its way, the page takes nothing in and
// lets nothing be changed (see useWorkingCopy).
export const viewsWriteKey = (space: string, scope: ViewScope) => ['saveViews', space, scope] as const

// Whether a write of these views is on its way (also one started before the page was opened again).
export function useViewsBusy(scope: ViewScope | null) {
  const space = useSpaceId()
  return useIsMutating({ mutationKey: viewsWriteKey(space, scope ?? { folder: '' }) }) > 0 && scope !== null
}

// How long the person's own write of the views, and the read made after it, may take before it is called off (a
// connection that hangs must not keep the tabs off); and how the views are read again in the background when that read
// did not come: `retries` times, the first after `retryMs`, each wait twice the one before. Tests shorten them.
export const viewsTiming = { timeoutMs: 20_000, retryMs: 1_000, retries: 5 }

// The views read again after a write of the person's (with or without an answer): a read on its way is called off
// first (it may have been served before the write), so what arrives from then on was served after it. When the read
// does not come and the write was answered, the answer is the views as they are now: it is newer than what the cache
// has, unless a read made after it arrived meanwhile. Gives what went wrong with the read, or null.
async function readViewsAfter(qc: QueryClient, space: string, scope: ViewScope, answer: ViewList | null): Promise<unknown> {
  const key = keys.views(space, scope)
  await qc.cancelQueries({ queryKey: key })
  const count = qc.getQueryState(key)?.dataUpdateCount ?? 0
  try {
    qc.setQueryData(key, await spaceApi(space).views(scope, AbortSignal.timeout(viewsTiming.timeoutMs)))
    return null
  } catch (e) {
    if (answer && (qc.getQueryState(key)?.dataUpdateCount ?? 0) === count) qc.setQueryData(key, answer)
    return e
  }
}

// Reads the views again in the background, a few times with growing waits, until a read arrives. Never while a write
// of these views is on its way (it reads after itself, and calls off a read on its way); not at all when nothing shows
// these views any more (they are marked stale, and read when shown).
function readViewsLater(qc: QueryClient, space: string, scope: ViewScope, attempt = 0) {
  if (attempt >= viewsTiming.retries) return
  setTimeout(() => {
    void (async () => {
      const key = keys.views(space, scope)
      if (qc.isMutating({ mutationKey: viewsWriteKey(space, scope) }) > 0) return
      const before = qc.getQueryState(key)?.dataUpdateCount ?? 0
      await qc.invalidateQueries({ queryKey: key, exact: true })
      if ((qc.getQueryState(key)?.dataUpdateCount ?? 0) > before) return
      if (!qc.getQueryCache().find({ queryKey: key, exact: true })?.isActive()) return
      readViewsLater(qc, space, scope, attempt + 1)
    })()
  }, viewsTiming.retryMs * 2 ** attempt)
}

// What a write of the views gives: the answer, and what went wrong with the read made after it (null: none). When that
// read did not come, the answer is in the cache as the views (see readViewsAfter), and they are read again later.
export interface ViewsWritten {
  list: ViewList
  readFailed: unknown
}

// Replaces the whole list. Exclusive and short: a read of the views on its way is called off first (its answer could
// come after the write's), and when the answer is there (or a refusal, or no answer at all) the views are read again;
// the write settles once they are, or once that read failed or ran out of time. Either way the cache then has the views
// as they are after the write, as far as is known. The write and that read each have `viewsTiming.timeoutMs`; a write
// that ran out of time may have been made or not (and may still be), so the views are read again later too.
export function useSaveViews(scope: ViewScope) {
  const qc = useQueryClient()
  const space = useSpaceId()
  return useMutation({
    mutationKey: viewsWriteKey(space, scope),
    mutationFn: async (input: PutViewsInput): Promise<ViewsWritten> => {
      await qc.cancelQueries({ queryKey: keys.views(space, scope) })
      let list: ViewList
      try {
        list = await spaceApi(space).putViews(scope, input, AbortSignal.timeout(viewsTiming.timeoutMs))
      } catch (e) {
        // No answer from the server at all (no connection, or out of time): whether it wrote is not known.
        const unknown = !(e instanceof ApiError) || e.status === 0
        const readFailed = await readViewsAfter(qc, space, scope, null)
        if (readFailed !== null || unknown) readViewsLater(qc, space, scope)
        throw e
      }
      const readFailed = await readViewsAfter(qc, space, scope, list)
      if (readFailed !== null) readViewsLater(qc, space, scope)
      return { list, readFailed }
    },
  })
}

// --- board and reading ---------------------------------------------------------------

// How many pages hold each value of a property, for the heads of a board's columns.
export function useGroups(params: GroupsParams, enabled = true) {
  const space = useSpaceId()
  return useQuery({ queryKey: keys.groups(space, params), queryFn: ({ signal }) => spaceApi(space).groups(params, signal), enabled: enabled && space !== '', staleTime: 10_000, placeholderData: keepPreviousData })
}

// One column of a board: its cards, twenty at a time.
export function useBoardColumn(params: Omit<ListParams, 'cursor' | 'limit' | 'props'>, enabled = true) {
  const space = useSpaceId()
  const full = { ...params, props: true }
  return useInfiniteQuery({
    enabled: enabled && space !== '',
    queryKey: keys.column(space, full),
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) => spaceApi(space).listPages({ ...full, limit: BOARD_PAGE_SIZE, cursor: pageParam || undefined }, signal),
    getNextPageParam: (last) => last.next || undefined,
    staleTime: 10_000,
  })
}

// The pages of continuous reading, with their texts, a batch at a time.
export function useReadingPages(params: Omit<ListParams, 'cursor' | 'limit' | 'body'>, enabled = true) {
  const space = useSpaceId()
  const full = { ...params, body: true }
  return useInfiniteQuery({
    enabled: enabled && space !== '',
    queryKey: keys.reading(space, full),
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) => spaceApi(space).listPages({ ...full, limit: READING_PAGE_SIZE, cursor: pageParam || undefined }, signal),
    getNextPageParam: (last) => last.next || undefined,
    gcTime: 0,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  })
}

// --- home: favourites and the daily text ---------------------------------------------

// Only for someone who is a member: an administrator who opens a space they do not belong to has no favourites,
// and the server refuses to add any.
export function useCanFavorite(): boolean {
  const { id, spaces } = useActiveSpace()
  const space = spaces.find((s) => s.id === id)
  return can(space, 'read') && !!space && space.role !== ''
}

export function useFavorites(enabled = true) {
  const space = useSpaceId()
  const { read } = useSpacePermissions()
  return useQuery({ queryKey: keys.favorites(space), queryFn: ({ signal }) => spaceApi(space).favorites(signal), staleTime: 30_000, enabled: enabled && space !== '' && read })
}

export const favoriteKey = (f: FavoriteRef) => `${f.kind}:${f.target}`

// The favourite that is this page, folder or view, if there is one.
export function useFavorite(ref: FavoriteRef | null): Favorite | undefined {
  const { data } = useFavorites(ref !== null)
  return ref ? data?.items.find((f) => f.kind === ref.kind && f.target === ref.target) : undefined
}

// Every write answers with the list as it is now, which replaces the cached one.
function useFavoritesWrite<V>(write: (space: string, v: V) => Promise<FavoriteList>, optimistic?: (prev: FavoriteList, v: V) => FavoriteList) {
  const qc = useQueryClient()
  const space = useSpaceId()
  return useMutation({
    mutationFn: (v: V) => write(space, v),
    onMutate: async (v) => {
      if (!optimistic) return undefined
      await qc.cancelQueries({ queryKey: keys.favorites(space) })
      const prev = qc.getQueryData<FavoriteList>(keys.favorites(space))
      if (prev) qc.setQueryData<FavoriteList>(keys.favorites(space), optimistic(prev, v))
      return { prev }
    },
    onError: (_e, _v, ctx) => {
      if (ctx?.prev) qc.setQueryData(keys.favorites(space), ctx.prev)
      void qc.invalidateQueries({ queryKey: keys.favorites(space) })
    },
    onSuccess: (list) => qc.setQueryData(keys.favorites(space), list),
  })
}
export const useAddFavorite = () => useFavoritesWrite((space, ref: FavoriteRef) => spaceApi(space).addFavorite(ref))
export const useRemoveFavorite = () =>
  useFavoritesWrite(
    (space, ref: FavoriteRef) => spaceApi(space).removeFavorite(ref),
    (prev, ref) => ({ ...prev, items: prev.items.filter((f) => !(f.kind === ref.kind && f.target === ref.target)) }),
  )
// The new order is shown at once and put back if the server refuses it.
export const useOrderFavorites = () =>
  useFavoritesWrite(
    (space, items: FavoriteRef[]) => spaceApi(space).orderFavorites(items),
    (prev, items) => {
      const rank = new Map(items.map((r, i) => [favoriteKey(r), i]))
      return { ...prev, items: [...prev.items].sort((a, b) => (rank.get(favoriteKey(a)) ?? Infinity) - (rank.get(favoriteKey(b)) ?? Infinity)) }
    },
  )

// The day the browser is in, as the server wants it, and how far east of UTC it is.
export function dayOf(now: Date): { date: string; tz: number } {
  const p = (n: number) => String(n).padStart(2, '0')
  return { date: `${now.getFullYear()}-${p(now.getMonth() + 1)}-${p(now.getDate())}`, tz: -now.getTimezoneOffset() }
}

// The text of the day. `skip` is "another one", and only this browser sees it. The server says how long the answer
// stays the same, so asking again within the day costs nothing; the last text stays on screen while another is read.
export function useDaily(date: string, tz: number, skip: number, lang: string, enabled = true) {
  const space = useSpaceId()
  const { read } = useSpacePermissions()
  return useQuery({
    queryKey: keys.daily(space, date, skip, lang),
    queryFn: ({ signal }) => spaceApi(space).daily({ date, skip, lang, tz }, signal),
    enabled: enabled && space !== '' && read,
    staleTime: 5 * 60_000,
    placeholderData: keepPreviousData,
    // Nothing here is worth asking twice over a failure: the block just is not there.
    retry: false,
  })
}

// Where the space takes its daily text from (anyone who reads sees it; changing it is for those who manage).
export function useDailySettings(enabled = true) {
  const space = useSpaceId()
  const { read } = useSpacePermissions()
  return useQuery({ queryKey: keys.dailySettings(space), queryFn: ({ signal }) => spaceApi(space).dailySettings(signal), enabled: enabled && space !== '' && read, staleTime: 15_000 })
}
export function useSaveDailySettings() {
  const qc = useQueryClient()
  const space = useSpaceId()
  return useMutation({
    mutationFn: (daily: DailySettings | null) => spaceApi(space).putDailySettings(daily),
    onSuccess: (res) => {
      qc.setQueryData(keys.dailySettings(space), res)
      void qc.invalidateQueries({ queryKey: ['space', space, 'daily'] })
    },
  })
}

// What a space takes of the administrators' settings. 404 in single-user mode, where there are none: that is `null`.
export function useDailyPrefs(enabled = true) {
  const space = useSpaceId()
  const { read } = useSpacePermissions()
  return useQuery({
    queryKey: keys.dailyPrefs(space),
    queryFn: async ({ signal }): Promise<DailyPrefs | null> => {
      try {
        return await spaceApi(space).dailyPrefs(signal)
      } catch (e) {
        if (e instanceof ApiError && e.code === 'not_found') return null
        throw e
      }
    },
    enabled: enabled && space !== '' && read,
    staleTime: 15_000,
  })
}
export function useSaveDailyPrefs() {
  const qc = useQueryClient()
  const space = useSpaceId()
  return useMutation({
    mutationFn: (input: { inherit: boolean; apis: string[] }) => spaceApi(space).putDailyPrefs(input),
    onSuccess: (res) => {
      qc.setQueryData(keys.dailyPrefs(space), res)
      void qc.invalidateQueries({ queryKey: ['space', space, 'daily'] })
    },
  })
}

// The first few pages of a listing, once (the home page's blocks): no cursor, no "load more". Under `pages`, so a
// write of a page reads it again like every listing.
export function useTopPages(params: Omit<ListParams, 'cursor' | 'limit'>, limit: number, enabled = true) {
  const space = useSpaceId()
  const { read } = useSpacePermissions()
  return useQuery({
    queryKey: [...keys.pages(space), 'top', limit, params] as const,
    queryFn: ({ signal }) => spaceApi(space).listPages({ ...params, limit }, signal),
    enabled: enabled && space !== '' && read,
    staleTime: 10_000,
  })
}
