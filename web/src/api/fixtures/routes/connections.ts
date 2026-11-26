// The platforms a space can be stored on: what a person sees of them and the accounts and repositories they can
// choose (their own side), and the administrators' side: the GitHub App, its installations and the hosts with an OAuth
// application. The handlers read and write the stored records and know nothing of any platform's rules: a name
// taken, a refused key, a missing permission, a host that cannot be reached are what a test says with `fx.fail`.

import type { AdminGitHub, AdminProviderRow, GitHubInstallation, GitHubManifestStart } from '../../types.ts'
import { forgeProvider } from '../data/connections.ts'
import { find, paged, reply, Refusal, remove, rfc3339, type Ctx, type Route } from '../kit.ts'

const conn = (c: Ctx) => c.data.area.connections
const providerOf = (c: Ctx) => find(conn(c).providers, (p) => p.id === c.params.provider, 'the provider')

function githubView(c: Ctx): AdminGitHub {
  const g = conn(c).github
  return (
    g ?? { configured: false, callbackUrl: 'https://notes.example.test/api/connections/github/authorization/callback' }
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
  // --- the person's side ---
  ['GET', '/api/connections', (c) => ({ providers: conn(c).providers })],
  [
    'POST',
    '/api/connections/:provider/authorization/start',
    (c) => (providerOf(c), { url: `/api/connections/${c.params.provider}/authorization/callback?state=fixture` }),
  ],
  [
    'DELETE',
    '/api/connections/:provider/authorization',
    (c) => {
      const p = providerOf(c)
      p.connection = null
      const f = conn(c).forges.find((x) => x.id === p.id)
      if (f) f.connections = 0
    },
  ],
  [
    'DELETE',
    '/api/connections/:provider/leftovers/:repo',
    (c) => {
      const p = providerOf(c)
      p.leftovers = p.leftovers.filter((l) => String(l.repoId) !== c.params.repo)
    },
  ],
  ['GET', '/api/connections/:provider/owners', (c) => paged(conn(c).owners[c.params.provider] ?? [], c.query)],
  [
    'GET',
    '/api/connections/:provider/repos',
    (c) => {
      const which = c.query.get('installation') ?? 'mine'
      return paged(conn(c).repos[`${c.params.provider}:${which}`] ?? [], c.query)
    },
  ],

  // --- the administrators: the GitHub App ---
  ['GET', '/api/admin/connections/github', (c) => githubView(c)],
  [
    'PUT',
    '/api/admin/connections/github',
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
        callbackUrl: cur?.callbackUrl ?? 'https://notes.example.test/api/connections/github/authorization/callback',
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
    '/api/admin/connections/github',
    (c) => {
      conn(c).github = null
      conn(c).installations = []
    },
  ],
  [
    'POST',
    '/api/admin/connections/github/check',
    (c): AdminGitHub => {
      const g = conn(c).github
      if (!g) throw new Refusal(409, 'provider_not_configured', 'no App is set up')
      Object.assign(g, conn(c).githubSide, { checked: rfc3339(c.now()) })
      return g
    },
  ],
  [
    'POST',
    '/api/admin/connections/github/manifest',
    (c): GitHubManifestStart => {
      const org = typeof c.body.organization === 'string' ? c.body.organization : ''
      const base = 'https://notes.example.test/api/connections/github'
      return {
        url: `https://github.com${org ? `/organizations/${org}` : ''}/settings/apps/new?state=fixture`,
        manifest: { name: 'PetalBook', url: 'https://notes.example.test', redirect_url: `${base}/manifest/callback` },
      }
    },
  ],
  [
    'POST',
    '/api/admin/connections/github/manifest/retry',
    (c): AdminGitHub => {
      const g = conn(c).github
      if (!g) throw new Refusal(404, 'not_found', 'nothing was kept')
      return g
    },
  ],
  [
    'GET',
    '/api/admin/connections/github/installations',
    (c) => paged<GitHubInstallation>(conn(c).installations, c.query),
  ],
  [
    'PUT',
    '/api/admin/connections/github/installations/:id',
    (c): GitHubInstallation => {
      const i = find(conn(c).installations, (x) => String(x.id) === c.params.id, 'the installation')
      i.tenants = [...(c.body.tenants as string[])].sort()
      return i
    },
  ],

  // --- the administrators: the hosts with an OAuth application ---
  ['GET', '/api/admin/connections', (c) => ({ providers: rows(c) })],
  [
    'POST',
    '/api/admin/connections',
    (c) => {
      const a = conn(c)
      const b = c.body
      const host = String(b.host ?? '')
      const f = {
        id: String(b.id ?? ''),
        type: String(b.type ?? 'gitlab'),
        host,
        name: typeof b.name === 'string' && b.name.trim() ? b.name.trim() : host,
        owner: 'site' as const,
        configured: true,
        clientId: String(b.clientId ?? ''),
        hasSecret: true,
        callbackUrl: `https://notes.example.test/api/connections/${b.id}/authorization/callback`,
        scopes: b.type === 'gitlab' ? ['api'] : [],
        userAuthorization: true,
        version: b.type === 'gitlab' ? '' : '1.22.0',
        checked: rfc3339(c.now()),
        partialSync: 'unknown' as const,
        connections: 0,
        spaces: 0,
        updated: rfc3339(c.now()),
      }
      a.forges.push(f)
      a.providers.push(forgeProvider(f))
      return reply(201, f)
    },
  ],
  ['GET', '/api/admin/connections/:id', (c) => find(conn(c).forges, (f) => f.id === c.params.id, 'the host')],
  [
    'PATCH',
    '/api/admin/connections/:id',
    (c) => {
      const a = conn(c)
      const f = find(a.forges, (x) => x.id === c.params.id, 'the host')
      if (typeof c.body.name === 'string' && c.body.name.trim()) f.name = c.body.name.trim()
      if (typeof c.body.clientId === 'string' && c.body.clientId) f.clientId = c.body.clientId
      if (typeof c.body.clientSecret === 'string' && c.body.clientSecret) f.hasSecret = true
      f.updated = rfc3339(c.now())
      const p = a.providers.find((x) => x.id === f.id)
      if (p) p.name = f.name
      return f
    },
  ],
  [
    'POST',
    '/api/admin/connections/:id/check',
    (c) => {
      const f = find(conn(c).forges, (x) => x.id === c.params.id, 'the host')
      f.checked = rfc3339(c.now())
      return f
    },
  ],
  [
    'DELETE',
    '/api/admin/connections/:id',
    (c) => {
      const a = conn(c)
      remove(
        a.forges,
        find(a.forges, (x) => x.id === c.params.id, 'the host'),
      )
      a.providers = a.providers.filter((p) => p.id !== c.params.id)
    },
  ],
]
