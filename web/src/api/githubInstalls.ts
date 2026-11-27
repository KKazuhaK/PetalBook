// The installations of the site's GitHub App, each connected to its one owner (docs/api.md, "The GitHub provider",
// "Installations"): a person's own, under /account/github, and an organisation's, under /admin/github (in the console's
// scope). The same calls serve both; `of` says whose.
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { enc, request } from './client'
import type {
  ConnectionRepo,
  CursorPage,
  GitHubInstallationList,
  GitHubInstallationsOf,
  GitHubInstallationSpaces,
} from './types'

const base = (of: GitHubInstallationsOf) => (of === 'mine' ? '/account/github' : '/admin/github')

export const githubInstallsApi = {
  // The address of the App's installation page; GitHub sends the browser back to `ret`. An organisation names the
  // GitHub organisation (`account`): the server connects only an installation there.
  start: (of: GitHubInstallationsOf, ret: string, account?: string) =>
    request<{ url: string }>(`${base(of)}/install`, {
      method: 'POST',
      body: { return: ret, ...(account ? { account } : {}) },
    }),
  list: (of: GitHubInstallationsOf, signal?: AbortSignal) =>
    request<GitHubInstallationList>(`${base(of)}/installations`, { signal }),
  spaces: (of: GitHubInstallationsOf, id: number, signal?: AbortSignal) =>
    request<GitHubInstallationSpaces>(`${base(of)}/installations/${id}/spaces`, { signal }),
  repos: (of: GitHubInstallationsOf, id: number, cursor?: string, signal?: AbortSignal) =>
    request<CursorPage<ConnectionRepo>>(
      `${base(of)}/installations/${id}/repos${cursor ? `?cursor=${enc(cursor)}` : ''}`,
      { signal },
    ),
  disconnect: (of: GitHubInstallationsOf, id: number) =>
    request<{ spaces: number }>(`${base(of)}/installations/${id}`, { method: 'DELETE' }),
}

export const githubInstallsKey = (of: GitHubInstallationsOf) => ['github', 'installations', of] as const

// While a claim waits for GitHub's event the list is read again every few seconds (the server never asks GitHub:
// the event comes to it); a claim waits ten minutes at most.
export const PENDING_POLL_MS = 5_000

export function useGitHubInstallations(of: GitHubInstallationsOf, enabled = true) {
  return useQuery({
    queryKey: githubInstallsKey(of),
    queryFn: ({ signal }) => githubInstallsApi.list(of, signal),
    enabled,
    staleTime: 15_000,
    refetchInterval: (q) => (q.state.data?.pending.some((p) => p.error === '') ? PENDING_POLL_MS : false),
  })
}

export function useGitHubInstallationSpaces(of: GitHubInstallationsOf, id: number, enabled: boolean) {
  return useQuery({
    queryKey: [...githubInstallsKey(of), id, 'spaces'],
    queryFn: ({ signal }) => githubInstallsApi.spaces(of, id, signal),
    enabled,
    staleTime: 0,
  })
}

// One page of GitHub's list at a time.
export function useGitHubInstallationRepos(of: GitHubInstallationsOf, id: number, enabled: boolean) {
  return useInfiniteQuery({
    queryKey: [...githubInstallsKey(of), id, 'repos'],
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) => githubInstallsApi.repos(of, id, pageParam || undefined, signal),
    getNextPageParam: (last) => last.next || undefined,
    enabled,
    staleTime: 30_000,
  })
}

export function useDisconnectGitHubInstallation(of: GitHubInstallationsOf) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => githubInstallsApi.disconnect(of, id),
    // The spaces on it show that they lost access; the person's lists of places to sync to change too.
    onSettled: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: githubInstallsKey(of) }),
        qc.invalidateQueries({ queryKey: ['connections'] }),
      ]),
  })
}
