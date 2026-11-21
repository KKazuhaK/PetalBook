// TanStack Query hooks over the API client.

import { useInfiniteQuery, useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { waitForSaves } from '@/editor/saveController'
import { api } from './client'
import type { CreatePageInput, ListParams } from './types'

export const keys = {
  meta: ['meta'] as const,
  tree: ['tree'] as const,
  tags: ['tags'] as const,
  pages: ['pages'] as const,
  list: (params: ListParams) => ['pages', 'list', params] as const,
  page: (id: string) => ['page', id] as const,
}

export const PAGE_SIZE = 50

export function useMeta() {
  return useQuery({ queryKey: keys.meta, queryFn: ({ signal }) => api.meta(signal), staleTime: Infinity })
}

export function useTree() {
  return useQuery({ queryKey: keys.tree, queryFn: ({ signal }) => api.tree(signal), staleTime: 15_000 })
}

export function useTags() {
  return useQuery({ queryKey: keys.tags, queryFn: ({ signal }) => api.tags(signal), staleTime: 15_000 })
}

// Listings are fetched one cursor page at a time; nothing renders more rows
// than the person has asked for with "load more".
export function usePageList(params: Omit<ListParams, 'cursor' | 'limit'>) {
  return useInfiniteQuery({
    queryKey: keys.list(params),
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) => api.listPages({ ...params, limit: PAGE_SIZE, cursor: pageParam || undefined }, signal),
    getNextPageParam: (last) => last.next || undefined,
    staleTime: 10_000,
  })
}

// The editor starts from what the server has right now: no cache is kept once
// nobody is looking at the page, and a save still on its way is waited for so
// the rev read here is the one that save produced.
export function usePage(id: string) {
  return useQuery({
    queryKey: keys.page(id),
    queryFn: async ({ signal }) => {
      await waitForSaves(id)
      return api.getPage(id, signal)
    },
    gcTime: 0,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    retry: (count, e) => !(e as { status?: number }).status && count < 2,
  })
}

// Pages move between listings, folder counts and tag counts on every write.
export function invalidateListings(qc: QueryClient) {
  return Promise.all([qc.invalidateQueries({ queryKey: keys.pages }), qc.invalidateQueries({ queryKey: keys.tree }), qc.invalidateQueries({ queryKey: keys.tags })])
}

export function useCreatePage() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: CreatePageInput) => api.createPage(input),
    onSuccess: () => invalidateListings(qc),
  })
}

export function useMovePage() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, folder }: { id: string; folder: string }) => api.movePage(id, folder),
    onSuccess: () => invalidateListings(qc),
  })
}

export function useDeletePage() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.deletePage(id),
    onSuccess: () => invalidateListings(qc),
  })
}

export function useCreateFolder() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (path: string) => api.createFolder(path),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.tree }),
  })
}
