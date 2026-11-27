// The platforms a space can be stored on: what a person sees of them and the accounts and repositories they can
// choose (their own side), and the administrators' side: the GitHub App, its installations and the hosts with an OAuth
// application. The handlers read and write the stored records and know nothing of any platform's rules: a name
// taken, a refused key, a missing permission, a host that cannot be reached are what a test says with `fx.fail`.

import type { AdminGitHub, AdminProviderRow, GitHubInstallation, GitHubManifestStart } from '../../types.ts'
import { forgeProvider } from '../data/connections.ts'
import { find, paged, reply, Refusal, remove, rfc3339, type Ctx, type Route } from '../kit.ts'

const conn = (c: Ctx) => c.data.area.connections
const providerOf = (c: Ctx) => find(conn(c).providers, (p) => p.id === c.params.provider, 'the provider')
// The installations of one kind of owner (the fixtures have one person and one organisation); another owner's is
// not found.
const owned = (c: Ctx, kind: GitHubInstallation['owner']['kind']) =>
  conn(c).installations.filter((i) => i.owner.kind === kind)
const ownedOne = (c: Ctx, kind: GitHubInstallation['owner']['kind']) =>
  find(owned(c, kind), (i) => String(i.id) === c.params.id, 'the installation')

function githubView(c: Ctx): AdminGitHub {
  const g = conn(c).github
  return (
    g ?? {
      configured: false,
      callbackUrl: 'https://notes.example.test/api/account/links/github/callback',
      signinCallbackUrl: 'https://notes.example.test/api/auth/github/github/callback',
    }
  )
}

function rows(c: Ctx): AdminProviderRow[] {
  const a = conn(c)
  const out: AdminProviderRow[] = []
  if (a.github)
    out.push({
      id: 'github',
      type: 'github',
      name: 'GitHub',
      host: 'github.com',
      configured: true,
      partialSync: 'yes',
      connections: a.github.authorizations,
      spaces: a.github.spaces,
    })
  for (const f of a.forges)
    out.push({
      id: f.id,
      type: f.type,
      name: f.name,
      host: f.host,
      configured: f.configured,
      partialSync: f.partialSync,
      connections: f.connections,
      spaces: f.spaces,
    })
  return out
}

export const connectionRoutes: Route[] = [
  // --- the person's side (the list and the start are the account's: routes/account.ts) ---
  [
    'DELETE',
    '/api/account/leftovers/:provider/:repo',
    (c) => {
      const p = providerOf(c)
      p.leftovers = p.leftovers.filter((l) => String(l.repoId) !== c.params.repo)
    },
  ],
  ['GET', '/api/account/links/:provider/owners', (c) => paged(conn(c).owners[c.params.provider] ?? [], c.query)],
  [
    'GET',
    '/api/account/links/:provider/repos',
    (c) => {
      const which = c.query.get('installation') ?? 'mine'
      return paged(conn(c).repos[`${c.params.provider}:${which}`] ?? [], c.query)
    },
  ],

  // --- the administrators: the GitHub App ---
  ['GET', '/api/admin/providers/github', (c) => githubView(c)],
  [
    'PUT',
    '/api/admin/providers/github',
    (c): AdminGitHub => {
      const a = conn(c)
      const cur = a.github
      const b = c.body
      const next = {
        configured: true as const,
        appId: String(b.appId ?? cur?.appId ?? ''),
        clientId: String(b.clientId ?? cur?.clientId ?? ''),
        slug: cur?.slug ?? 'petalbook-notes',
        name: cur?.name ?? 'PetalBook Notes',
        owner: cur?.owner ?? '',
        htmlUrl: cur?.htmlUrl ?? 'https://github.com/apps/petalbook-notes',
        installUrl: cur?.installUrl ?? 'https://github.com/apps/petalbook-notes/installations/new',
        privateKey: b.privateKey
          ? { set: true, fingerprint: 'SHA256:fixtureFingerprint0123456789abcdefghijklmnopq' }
          : (cur?.privateKey ?? { set: false, fingerprint: '' }),
        clientSecret: { set: b.clientSecret ? true : (cur?.clientSecret.set ?? false) },
        callbackUrl: cur?.callbackUrl ?? 'https://notes.example.test/api/account/links/github/callback',
        signinCallbackUrl: cur?.signinCallbackUrl ?? 'https://notes.example.test/api/auth/github/github/callback',
        permissions: cur?.permissions ?? {},
        missing: cur?.missing ?? [],
        userAuthorization: String(b.clientId ?? cur?.clientId ?? '') !== '',
        checked: rfc3339(c.now()),
        spaces: cur?.spaces ?? 0,
        authorizations: cur?.authorizations ?? 0,
        gaps: cur?.gaps ?? [],
        anyAccount: cur?.anyAccount ?? null,
      }
      a.github = next
      return next
    },
  ],
  [
    'DELETE',
    '/api/admin/providers/github',
    (c) => {
      conn(c).github = null
      conn(c).installations = []
    },
  ],
  [
    'POST',
    '/api/admin/providers/github/check',
    (c): AdminGitHub => {
      const g = conn(c).github
      if (!g) throw new Refusal(409, 'provider_not_configured', 'no App is set up')
      Object.assign(g, conn(c).githubSide, { checked: rfc3339(c.now()) })
      return g
    },
  ],
  [
    'POST',
    '/api/admin/providers/github/manifest',
    (c): GitHubManifestStart => {
      const org = typeof c.body.organization === 'string' ? c.body.organization : ''
      const base = 'https://notes.example.test/api/providers/github'
      return {
        url: `https://github.com${org ? `/organizations/${org}` : ''}/settings/apps/new?state=fixture`,
        manifest: { name: 'PetalBook', url: 'https://notes.example.test', redirect_url: `${base}/manifest/callback` },
      }
    },
  ],
  [
    'POST',
    '/api/admin/providers/github/manifest/retry',
    (c): AdminGitHub => {
      const g = conn(c).github
      if (!g) throw new Refusal(404, 'not_found', 'nothing was kept')
      return g
    },
  ],
  // --- the person's own GitHub installations ---
  [
    'POST',
    '/api/account/github/install',
    () => ({ url: 'https://github.com/apps/petalbook-notes/installations/new?state=fixture' }),
  ],
  ['GET', '/api/account/github/installations', (c) => ({ items: owned(c, 'person') })],
  [
    'GET',
    '/api/account/github/installations/:id/spaces',
    (c) => {
      const i = ownedOne(c, 'person')
      const items = conn(c).installationSpaces[i.id] ?? []
      return { items, total: items.length }
    },
  ],
  [
    'GET',
    '/api/account/github/installations/:id/repos',
    (c) => paged(conn(c).repos[`github:${ownedOne(c, 'person').id}`] ?? [], c.query),
  ],
  [
    'DELETE',
    '/api/account/github/installations/:id',
    (c) => {
      const a = conn(c)
      const i = ownedOne(c, 'person')
      remove(a.installations, i)
      const n = (a.installationSpaces[i.id] ?? []).length
      delete a.installationSpaces[i.id]
      return { spaces: n }
    },
  ],
  // --- the organisation's own GitHub installations ---
  [
    'POST',
    '/api/admin/github/install',
    () => ({ url: 'https://github.com/apps/petalbook-notes/installations/new?state=fixture' }),
  ],
  ['GET', '/api/admin/github/installations', (c) => ({ items: owned(c, 'organization') })],
  [
    'GET',
    '/api/admin/github/installations/:id/spaces',
    (c) => {
      const i = ownedOne(c, 'organization')
      const items = conn(c).installationSpaces[i.id] ?? []
      return { items, total: items.length }
    },
  ],
  [
    'GET',
    '/api/admin/github/installations/:id/repos',
    (c) => paged(conn(c).repos[`github:${ownedOne(c, 'organization').id}`] ?? [], c.query),
  ],
  [
    'DELETE',
    '/api/admin/github/installations/:id',
    (c) => {
      const a = conn(c)
      const i = ownedOne(c, 'organization')
      remove(a.installations, i)
      const n = (a.installationSpaces[i.id] ?? []).length
      delete a.installationSpaces[i.id]
      return { spaces: n }
    },
  ],
]

// --- the administrators: the hosts with an OAuth application (the routes are the one list of providers: routes/admin.ts) ---

// The hosts as the repository side of the list of providers says them.
export function repoItems(c: Ctx) {
  return rows(c).map((r) => ({
    id: r.id,
    slug: r.id,
    kind: r.type,
    type: r.type,
    name: r.name,
    owner: 'site' as const,
    enabled: true,
    signin: false,
    repos: r.configured,
    host: r.host,
    configured: r.configured,
    partialSync: r.partialSync,
    connections: r.connections,
    spaces: r.spaces,
  }))
}

export const isForge = (c: Ctx, id: string) => conn(c).forges.some((f) => f.id === id)

export function forgeCreate(c: Ctx) {
  const a = conn(c)
  const b = c.body
  const host = String(b.host ?? '')
  const type = String(b.type ?? b.kind ?? 'gitlab')
  const id = String(b.id ?? b.slug ?? '')
  const f = {
    id,
    type,
    host,
    name: typeof b.name === 'string' && b.name.trim() ? b.name.trim() : host,
    owner: 'site' as const,
    configured: true,
    clientId: String(b.clientId ?? ''),
    hasSecret: true,
    callbackUrl: `https://notes.example.test/api/account/links/${id}/callback`,
    scopes: type === 'gitlab' ? ['api'] : [],
    userAuthorization: true,
    version: type === 'gitlab' ? '' : '1.22.0',
    checked: rfc3339(c.now()),
    partialSync: 'unknown' as const,
    connections: 0,
    spaces: 0,
    updated: rfc3339(c.now()),
  }
  a.forges.push(f)
  a.providers.push(forgeProvider(f))
  return reply(201, f)
}

export const forgeGet = (c: Ctx) => find(conn(c).forges, (f) => f.id === c.params.id, 'the host')

export function forgePatch(c: Ctx) {
  const a = conn(c)
  const f = find(a.forges, (x) => x.id === c.params.id, 'the host')
  if (typeof c.body.name === 'string' && c.body.name.trim()) f.name = c.body.name.trim()
  if (typeof c.body.clientId === 'string' && c.body.clientId) f.clientId = c.body.clientId
  if (typeof c.body.clientSecret === 'string' && c.body.clientSecret) f.hasSecret = true
  f.updated = rfc3339(c.now())
  const p = a.providers.find((x) => x.id === f.id)
  if (p) p.name = f.name
  return f
}

export function forgeCheck(c: Ctx) {
  const f = find(conn(c).forges, (x) => x.id === c.params.id, 'the host')
  f.checked = rfc3339(c.now())
  return f
}

export function forgeDelete(c: Ctx) {
  const a = conn(c)
  remove(
    a.forges,
    find(a.forges, (x) => x.id === c.params.id, 'the host'),
  )
  a.providers = a.providers.filter((p) => p.id !== c.params.id)
}
