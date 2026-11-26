// Scenes for the platforms a space can be stored on (GitHub, GitLab, Gitea...): the provider as a person sees it, the
// accounts and repositories to choose from, and the administrators' side of it. Nothing here goes through a route.

import type {
  AdminForge,
  Connection,
  ConnectionLeftover,
  ConnectionOwner,
  ConnectionRepo,
  GitHubInstallation,
  Provider,
} from '../../types.ts'
import type { AreaData } from './area.ts'

type Scene = { area: AreaData }
export type GitHubState = 'off' | 'app' | 'connected' | 'expired'

const SETTLED = '2026-11-01T00:00:00Z'

export const GITHUB_APP = {
  name: 'PetalBook Notes',
  slug: 'petalbook-notes',
  htmlUrl: 'https://github.com/apps/petalbook-notes',
  installUrl: 'https://github.com/apps/petalbook-notes/installations/new',
}

function connection(login: string, state: 'ok' | 'expired', accountId: string, extra: Partial<Connection> = {}) {
  const c: Connection = { login, accountId, state, connected: SETTLED, spaces: 0, ...extra }
  return c
}

const repo = (id: number, full: string, isPrivate = true): ConnectionRepo => ({
  id,
  fullName: full,
  private: isPrivate,
  defaultBranch: 'main',
  htmlUrl: `https://github.com/${full}`,
  canPush: true,
})

const owner = (login: string, type: ConnectionOwner['type'], via: ConnectionOwner['via'], id: number | null) => ({
  login,
  type,
  via,
  installationId: id,
  needsInstall: false,
})

export const installation = (
  id: number,
  login: string,
  over: Partial<GitHubInstallation> = {},
): GitHubInstallation => ({
  id,
  account: { login, type: 'Organization', id: id + 1000 },
  repositorySelection: 'all',
  suspended: false,
  permissions: { administration: 'write', contents: 'write', metadata: 'read' },
  htmlUrl: `https://github.com/organizations/${login}/settings/installations/${id}`,
  tenants: [],
  spaces: 0,
  ...over,
})

// The server's GitHub App as the administrators see it, with the installations it has.
export function githubApp(d: Scene) {
  const c = d.area.connections
  c.github = {
    configured: true,
    appId: '1234567',
    clientId: 'Iv23liMockClientId',
    ...GITHUB_APP,
    owner: 'kazuhahub',
    privateKey: { set: true, fingerprint: 'SHA256:fixtureFingerprint0123456789abcdefghijklmnopq' },
    clientSecret: { set: true },
    callbackUrl: 'https://notes.example.test/api/connections/github/authorization/callback',
    permissions: { administration: 'write', contents: 'write', metadata: 'read' },
    missing: [],
    userAuthorization: true,
    checked: SETTLED,
    spaces: 0,
    authorizations: 0,
    gaps: [],
    anyAccount: true,
  }
  c.installations = [
    installation(5001, 'kazuhahub'),
    installation(5002, 'petal-team', { repositorySelection: 'selected' }),
    installation(5003, 'tang-poetry'),
  ]
}

// GitHub as the person sees it: no App on the server, an App and no connection, a connection, or one that expired.
// A person who is connected can make repositories under their own account and under two organisations.
export function githubProvider(d: Scene, state: GitHubState): Provider {
  const c = d.area.connections
  const on = state !== 'off'
  const p: Provider = {
    id: 'github',
    type: 'github',
    host: 'github.com',
    name: 'GitHub',
    owner: 'site',
    capabilities: {
      createRepo: true,
      installations: true,
      connectAccount: true,
      storageTarget: true,
      partialSync: 'yes',
    },
    configured: on,
    app: on ? { ...GITHUB_APP } : null,
    userAuthorization: on,
    connection:
      state === 'connected' || state === 'expired'
        ? connection('tang-gh', state === 'expired' ? 'expired' : 'ok', '5001')
        : null,
    leftovers: [],
  }
  c.providers = c.providers.filter((x) => x.id !== 'github')
  c.providers.push(p)
  if (on) githubApp(d)
  if (state === 'connected') {
    c.owners['github'] = [
      owner('tang-gh', 'User', 'user', null),
      owner('kazuhahub', 'Organization', 'app', 5001),
      owner('tang-poetry', 'Organization', 'user', 5003),
    ]
    c.repos['github:5001'] = [repo(880_000_001, 'kazuhahub/notes'), repo(880_000_002, 'kazuhahub/wiki', false)]
    c.repos['github:mine'] = [repo(880_000_010, 'tang-gh/journal')]
  }
  return p
}

// An empty repository a failed attempt left, and the reminder of it.
export function leaveRepository(d: Scene, fullName: string, spaceName = 'Poems', provider = 'github') {
  const p = d.area.connections.providers.find((x) => x.id === provider)!
  const left: ConnectionLeftover = {
    repoId: 880_100_000 + p.leftovers.length,
    fullName,
    htmlUrl: `https://github.com/${fullName}`,
    spaceName,
    created: SETTLED,
  }
  p.leftovers.unshift(left)
  return left
}

// A host with an OAuth application (GitLab by default), as the administrators and the people see it.
export function forgeHost(d: Scene, over: Partial<AdminForge> = {}): AdminForge {
  const c = d.area.connections
  const f: AdminForge = {
    id: 'gitlab-acme',
    type: 'gitlab',
    host: 'gitlab.acme.example',
    name: 'Acme GitLab',
    owner: 'site',
    configured: true,
    clientId: 'c',
    hasSecret: true,
    callbackUrl: 'https://notes.example.test/api/connections/gitlab-acme/authorization/callback',
    scopes: ['api'],
    userAuthorization: true,
    version: '',
    checked: SETTLED,
    partialSync: 'unknown',
    connections: 0,
    spaces: 0,
    updated: SETTLED,
    ...over,
  }
  c.forges.push(f)
  c.providers.push(forgeProvider(f))
  return f
}

// What the person sees of a host the administrators set up.
export function forgeProvider(f: AdminForge): Provider {
  return {
    id: f.id,
    type: f.type,
    host: f.host,
    name: f.name,
    owner: f.owner,
    capabilities: {
      createRepo: true,
      installations: false,
      connectAccount: true,
      storageTarget: true,
      partialSync: f.partialSync,
    },
    configured: f.configured,
    app: null,
    userAuthorization: f.userAuthorization,
    connection: null,
    leftovers: [],
  }
}

// The person is connected to a host (or their connection expired); the host counts it.
export function connectForge(
  d: Scene,
  id: string,
  login: string,
  extra: Partial<Connection> & { expired?: boolean } = {},
) {
  const { expired, ...rest } = extra
  const c = d.area.connections
  const p = c.providers.find((x) => x.id === id)!
  p.connection = connection(login, expired ? 'expired' : 'ok', '9001', rest)
  const f = c.forges.find((x) => x.id === id)
  if (f) f.connections = 1
}
