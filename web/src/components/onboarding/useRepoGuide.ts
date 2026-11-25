import { useActiveSpace, useConnections, useSpaces } from '@/api/hooks'
import type { Provider, RemoteNotice, SpaceInfo } from '@/api/types'

export type RepoStepId = 'connect' | 'repo' | 'sync'

export interface RepoGuide {
  // What the site or the organisation asks that is not done yet: null when nothing is asked, or it is all done.
  note: RemoteNotice | null
  space: SpaceInfo | null
  // The platform asked for, when one is named ('any' names none).
  provider: Provider | null
  providerName: string
  steps: { id: RepoStepId; done: boolean; auto: boolean }[]
  // The step to take now; the number of steps that are the person's to take and are not done.
  current: RepoStepId
  left: number
  // Past its date: the space is read-only until it is done.
  forced: boolean
}

// Where a person stands in connecting their own repository (milestone 3.1's storage policy asks for it, as a
// suggestion until its date and as a requirement after). The steps are worked out from what is known: whether their
// account is connected to the platform, and whether the space still has the request on it.
export function useRepoGuide(): RepoGuide {
  const { id: activeId } = useActiveSpace()
  const spaces = useSpaces().data?.spaces ?? []
  const space =
    spaces.find((s) => s.id === activeId && s.storage?.remote) ?? spaces.find((s) => s.storage?.remote) ?? null
  const note = space?.storage?.remote ?? null
  const providers = useConnections(!!note).data?.providers ?? []
  const named = note && note.required !== 'any' ? providers.find((p) => p.id === note.required) : undefined
  const provider =
    named ?? providers.find((p) => p.connection) ?? providers.find((p) => p.configured && p.userAuthorization) ?? null
  const connected = named ? !!named.connection : providers.some((p) => p.connection)
  return {
    note,
    space,
    provider,
    providerName: provider?.name ?? note?.required ?? '',
    steps: [
      { id: 'connect', done: connected, auto: false },
      { id: 'repo', done: false, auto: false },
      { id: 'sync', done: false, auto: true },
    ],
    current: connected ? 'repo' : 'connect',
    left: connected ? 1 : 2,
    forced: !!note?.overdue,
  }
}
