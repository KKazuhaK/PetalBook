// What the in-memory server plays for GitHub (docs/api.md, "GitHub" and
// "Administration", "GitHub App"): the App's settings, each person's connection,
// a few accounts and repositories to choose from, and the failures a real GitHub
// has (a name taken, no permission, an installation that does not reach the new
// repository, an authorisation that ran out). There is no real GitHub behind it.

import { HttpError } from './store.ts'
import type {
  AdminGitHub,
  Connection,
  GitHubGap,
  ConnectionLeftover,
  CursorPage,
  Provider,
  GitHubInstallation,
  GitHubManifestStart,
  ConnectionOwner,
  ConnectionRepo,
  ConnectionVia,
  PutAdminGitHubInput,
} from '../types.ts'

export interface MockGitHubOptions {
  // The App is set up from the start (`npm run dev:mock:accounts`).
  configured?: boolean
}

interface AppRec {
  appId: string
  clientId: string
  clientSecret: boolean
  fingerprint: string
  permissions: Record<string, string>
  checked: number
  gaps: GitHubGap[]
  anyAccount: boolean | null
}
// What GitHub says about the App right now; the administration sees it only after a re-check.
interface Side {
  permissions: Record<string, string>
  gaps: GitHubGap[]
  anyAccount: boolean | null
}
interface Manifest {
  userId: string
  created: number
}
interface AuthRec {
  login: string
  githubId: number
  state: 'ok' | 'expired'
  connected: number
}
interface Attempt {
  userId: string
  ret: string
  created: number
}
interface RepoRec {
  id: number
  owner: string
  name: string
  private: boolean
}
interface InstRec {
  id: number
  login: string
  type: 'User' | 'Organization'
  selection: 'all' | 'selected'
  // The tenants it is shared with.
  tenants: string[]
  // The people who are members of the organisation (by the login of their GitHub account); `*` is everyone.
  members: string[]
}

const PAGE = 30
const PUBLIC_URL = 'https://notes.example.test'
const NAME_RE = /^[A-Za-z0-9._-]{1,100}$/

export const MOCK_GITHUB_APP = {
  name: 'PetalBook Notes',
  slug: 'petalbook-notes',
  htmlUrl: 'https://github.com/apps/petalbook-notes',
} as const

export function createGitHubBook(
  opts: MockGitHubOptions,
  deps: {
    now: () => number
    random: () => number
    spacesUsing: (userId: string) => number
    spacesOnApp: () => number
    tenantOf: (userId: string) => string
    tenantExists: (id: string) => boolean
  },
) {
  let app: AppRec | null = opts.configured
    ? {
        appId: '1234567',
        clientId: 'Iv23liMockClientId',
        clientSecret: true,
        fingerprint: 'SHA256:mockFingerprint0123456789abcdefghijklmnopqrs',
        permissions: { administration: 'write', contents: 'write', metadata: 'read' },
        checked: deps.now(),
        gaps: [],
        anyAccount: true,
      }
    : null
  const side: Side = {
    permissions: { administration: 'write', contents: 'write', metadata: 'read' },
    gaps: [],
    anyAccount: true,
  }
  const manifests = new Map<string, Manifest>()
  // The personal accounts the App is installed on (by GitHub login).
  const installedOn = new Set<string>()
  const auths = new Map<string, AuthRec>()
  // The reminders of empty repositories, by person, newest first.
  const leftovers = new Map<string, (ConnectionLeftover & { created: string })[]>()
  const attempts = new Map<string, Attempt>()
  let nextId = 5000
  let repoId = 880_000_000
  const insts: InstRec[] = [
    { id: 5001, login: 'kazuhahub', type: 'Organization', selection: 'all', tenants: [], members: ['*'] },
    { id: 5002, login: 'petal-team', type: 'Organization', selection: 'selected', tenants: [], members: [] },
    { id: 5003, login: 'tang-poetry', type: 'Organization', selection: 'all', tenants: [], members: ['*'] },
  ]
  const repos: RepoRec[] = [
    { id: ++repoId, owner: 'kazuhahub', name: 'notes', private: true },
    { id: ++repoId, owner: 'kazuhahub', name: 'wiki', private: false },
    { id: ++repoId, owner: 'petal-team', name: 'docs', private: true },
  ]
  // Mock only: what the next creation or connection finds.
  const traps = {
    noPermission: false,
    notInInstallation: false,
    pushFails: false,
    missingPermission: false,
    personalNeedsInstall: false,
  }

  const key = (s: string) => s.toLowerCase()
  const invalid = (m: string) => new HttpError(400, 'invalid', m)
  const notConfigured = () =>
    new HttpError(409, 'provider_not_configured', '这台服务器没有设置 GitHub App（管理员在管理页里设置）')
  const notAuthorized = () => new HttpError(409, 'not_connected', '请先连接你的 GitHub 账号')
  const requireApp = () => {
    if (!app) throw notConfigured()
    return app
  }
  const userAuthorization = () => !!app && app.clientId !== '' && app.clientSecret
  // Everyone has a repository of their own to choose.
  const ensureHome = (login: string) => {
    if (!repos.some((r) => key(r.owner) === key(login)))
      repos.push({ id: ++repoId, owner: login, name: 'journal', private: true })
  }
  const connection = (userId: string): AuthRec | null => auths.get(userId) ?? null
  const usable = (userId: string): AuthRec | null => {
    const a = connection(userId)
    return a && a.state === 'ok' ? a : null
  }
  const sharedWith = (i: InstRec, userId: string) => i.tenants.includes(deps.tenantOf(userId))
  const member = (i: InstRec, login: string | undefined) =>
    !!login && (i.members.includes('*') || i.members.map(key).includes(key(login)))

  const authOf = (userId: string): Connection | null => {
    const a = connection(userId)
    if (!a) return null
    return {
      login: a.login,
      accountId: String(a.githubId),
      state: a.state,
      connected: new Date(a.connected).toISOString().replace(/\.\d+Z$/, 'Z'),
      refreshExpires: new Date(a.connected + 180 * 86_400_000).toISOString().replace(/\.\d+Z$/, 'Z'),
      spaces: deps.spacesUsing(userId),
    }
  }

  const info = (userId: string): Provider => ({
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
    configured: app !== null,
    app: app ? { ...MOCK_GITHUB_APP, installUrl: `${MOCK_GITHUB_APP.htmlUrl}/installations/new` } : null,
    userAuthorization: userAuthorization(),
    connection: authOf(userId),
    leftovers: [...(leftovers.get(userId) ?? [])],
  })

  // --- connecting -----------------------------------------------------------------

  const randomState = () =>
    Array.from({ length: 24 }, () => 'abcdefghijklmnopqrstuvwxyz0123456789'[Math.floor(deps.random() * 36)]).join('')

  // `install: true` plays the App's installation page, which authorises the person in the same flow.
  const start = (userId: string, body: Record<string, unknown>): { url: string; state: string } => {
    requireApp()
    if (!userAuthorization()) throw notConfigured()
    const state = randomState()
    attempts.set(state, {
      userId,
      ret:
        typeof body.return === 'string' && body.return.startsWith('/') && !body.return.startsWith('//')
          ? body.return
          : '/',
      created: deps.now(),
    })
    const install = body.install === true ? '&setup_action=install&installation_id=7001' : ''
    return { url: `/api/connections/github/authorization/callback?state=${state}${install}`, state }
  }

  // The browser comes back: always a redirect.
  const callback = (
    userId: string | null,
    cookieState: string | undefined,
    q: URLSearchParams,
  ): { to: string; clear: boolean } => {
    const fail = (c: string) => ({ to: `/settings?provider=github&connection_error=${c}`, clear: true })
    const state = q.get('state') ?? ''
    const at = attempts.get(state)
    if (!at || cookieState !== state || !userId || at.userId !== userId || deps.now() - at.created > 600_000)
      return { to: '/settings?provider=github&connection_error=expired', clear: false }
    attempts.delete(state)
    if (q.get('error')) return fail('denied')
    if (q.get('setup_action') === 'request') return fail('requested')
    const login = (q.get('login') ?? `${userId.slice(0, 6).toLowerCase()}-gh`).toLowerCase()
    if (q.get('setup_action') === 'install') installedOn.add(login)
    for (const [uid, a] of auths) if (uid !== userId && a.login === login) return fail('account_in_use')
    const prev = connection(userId)
    if (prev && prev.login !== login && deps.spacesUsing(userId) > 0) return fail('different_account')
    auths.set(userId, { login, githubId: prev?.githubId ?? ++nextId, state: 'ok', connected: deps.now() })
    ensureHome(login)
    return { to: at.ret, clear: true }
  }

  const disconnect = (userId: string) => {
    auths.delete(userId)
  }

  // --- the accounts and repositories to choose from ---------------------------------

  const page = <T>(all: T[], cursor: string | null): CursorPage<T> => {
    const start = cursor === null || cursor === '' ? 0 : Number(cursor.replace(/^c/, ''))
    if (!Number.isInteger(start) || start < 0) throw invalid('the cursor is not one this server made')
    const items = all.slice(start, start + PAGE)
    return { items, next: start + PAGE < all.length ? `c${start + PAGE}` : '' }
  }

  const owners = (userId: string, cursor: string | null): CursorPage<ConnectionOwner> => {
    requireApp()
    const a = usable(userId)
    const out: ConnectionOwner[] = []
    const seen = new Set<number>()
    if (a) {
      out.push({
        login: a.login,
        type: 'User',
        via: 'user',
        installationId: null,
        needsInstall: traps.personalNeedsInstall && !installedOn.has(key(a.login)),
      })
      for (const i of insts) {
        if (i.type === 'Organization' && member(i, a.login)) {
          out.push({ login: i.login, type: 'Organization', via: 'user', installationId: i.id, needsInstall: false })
          seen.add(i.id)
        }
      }
    }
    for (const i of insts)
      if (sharedWith(i, userId) && i.type === 'Organization' && !seen.has(i.id))
        out.push({ login: i.login, type: 'Organization', via: 'app', installationId: i.id, needsInstall: false })
    return page(out, cursor)
  }

  const asRepo = (r: RepoRec): ConnectionRepo => ({
    id: r.id,
    fullName: `${r.owner}/${r.name}`,
    private: r.private,
    defaultBranch: 'main',
    htmlUrl: `https://github.com/${r.owner}/${r.name}`,
    canPush: true,
  })

  const reposOf = (
    userId: string,
    which: { installation: number } | { mine: true },
    cursor: string | null,
  ): CursorPage<ConnectionRepo> => {
    requireApp()
    if ('mine' in which) {
      const a = usable(userId)
      if (!a) throw notAuthorized()
      return page(repos.filter((r) => key(r.owner) === key(a.login)).map(asRepo), cursor)
    }
    const i = insts.find((x) => x.id === which.installation)
    const a = usable(userId)
    if (!i || !(sharedWith(i, userId) || (a && member(i, a.login))))
      throw new HttpError(404, 'not_found', '没有这个安装')
    return page(repos.filter((r) => key(r.owner) === key(i.login)).map(asRepo), cursor)
  }

  // --- making and connecting repositories ---------------------------------------------

  // Whose token would create in `owner`; the answer a refused owner is.
  const plan = (userId: string, owner: string): { login: string; via: ConnectionVia; installation: InstRec | null } => {
    requireApp()
    const a = usable(userId)
    if (a && key(a.login) === key(owner)) return { login: a.login, via: 'user', installation: null }
    const i = insts.find((x) => key(x.login) === key(owner) && x.type === 'Organization')
    if (i && a && member(i, a.login)) return { login: i.login, via: 'user', installation: i }
    if (i && sharedWith(i, userId)) return { login: i.login, via: 'app', installation: i }
    const c = connection(userId)
    if (c && c.state === 'expired' && key(c.login) === key(owner)) throw notAuthorized()
    throw new HttpError(403, 'owner_not_allowed', `不能在 ${owner} 下建仓库`)
  }

  const create = (
    userId: string,
    input: { owner: string; name: string; private: boolean; spaceName: string },
  ): { repo: RepoRec; via: ConnectionVia; installationId: number } => {
    const p = plan(userId, input.owner)
    if (
      !NAME_RE.test(input.name) ||
      input.name === '.' ||
      input.name === '..' ||
      input.name.toLowerCase().endsWith('.git')
    )
      throw invalid('仓库名只能用字母、数字、点、下划线和连字符，最多 100 个字符')
    if (traps.noPermission)
      throw new HttpError(403, 'provider_refused', 'GitHub 拒绝建仓库：你在这个组织里没有建仓库的权限')
    if (traps.personalNeedsInstall && !p.installation && p.via === 'user' && !installedOn.has(key(p.login))) {
      throw new HttpError(
        409,
        'app_not_installed',
        `App 还没有装到你的账号 ${p.login} 上，装好之前 GitHub 不让在里面建仓库`,
        { installUrl: `${MOCK_GITHUB_APP.htmlUrl}/installations/new` },
      )
    }
    if (traps.missingPermission)
      throw new HttpError(403, 'app_missing_permission', 'App 或它的安装缺少 administration 权限', {
        missing: ['administration'],
      })
    if (repos.some((r) => key(r.owner) === key(p.login) && key(r.name) === key(input.name)))
      throw new HttpError(409, 'name_taken', `${p.login} 下已经有同名仓库`)
    const repo: RepoRec = { id: ++repoId, owner: p.login, name: input.name, private: input.private }
    repos.push(repo)
    // From here on the repository stays, and the person is reminded of it.
    const remember = () => {
      const list = (leftovers.get(userId) ?? []).filter((l) => l.repoId !== repo.id)
      list.unshift({
        repoId: repo.id,
        fullName: `${repo.owner}/${repo.name}`,
        htmlUrl: `https://github.com/${repo.owner}/${repo.name}`,
        spaceName: input.spaceName,
        created: new Date(deps.now()).toISOString().replace(/\.\d+Z$/, 'Z'),
      })
      leftovers.set(userId, list.slice(0, 20))
    }
    if (p.installation && p.installation.selection === 'selected' && traps.notInInstallation) {
      remember()
      throw new HttpError(
        409,
        'repo_not_in_installation',
        `${p.login} 的安装没有包含新仓库 ${p.login}/${input.name}，也没能加进去`,
        {
          repo: {
            id: repo.id,
            fullName: `${repo.owner}/${repo.name}`,
            htmlUrl: `https://github.com/${repo.owner}/${repo.name}`,
          },
        },
      )
    }
    if (traps.pushFails) {
      remember()
      // The repository was made and stays, empty: a retry with create: false connects to it.
      throw new HttpError(502, 'push_failed', `仓库 ${p.login}/${input.name} 已经建好，但第一次推送没有成功`, {
        repo: {
          id: repo.id,
          fullName: `${repo.owner}/${repo.name}`,
          htmlUrl: `https://github.com/${repo.owner}/${repo.name}`,
        },
      })
    }
    return { repo, via: p.installation ? 'app' : 'user', installationId: p.installation?.id ?? 0 }
  }

  // A repository that exists: the App when the installation reaches it and the caller may use it, else the caller's own connection.
  const existing = (
    userId: string,
    owner: string,
    name: string,
    via: ConnectionVia | '',
  ): { repo: RepoRec; via: ConnectionVia; installationId: number } => {
    requireApp()
    const repo = repos.find((r) => key(r.owner) === key(owner) && key(r.name) === key(name))
    const a = usable(userId)
    const i = insts.find((x) => key(x.login) === key(owner))
    const tryApp = () => {
      if (!repo || !i || (i.selection === 'selected' && traps.notInInstallation))
        throw new HttpError(409, 'repo_not_in_installation', 'App 没有装在这个仓库上，或者它的安装够不着这个仓库')
      if (!sharedWith(i, userId)) {
        if (!a) throw notAuthorized()
        if (!member(i, a.login))
          throw new HttpError(403, 'owner_not_allowed', '这个安装没有共享，而你的 GitHub 连接不能推送到这个仓库')
      }
      return { repo, via: 'app' as const, installationId: i.id }
    }
    const tryUser = () => {
      if (!a) throw notAuthorized()
      if (!repo || !(key(a.login) === key(owner) || (i && member(i, a.login))))
        throw new HttpError(403, 'owner_not_allowed', '你的 GitHub 连接不能推送到这个仓库')
      return { repo, via: 'user' as const, installationId: 0 }
    }
    if (via === 'app') return tryApp()
    if (via === 'user') return tryUser()
    try {
      return tryApp()
    } catch (e) {
      try {
        return tryUser()
      } catch {
        throw e
      }
    }
  }

  // --- the administration ----------------------------------------------------------------

  const admin = (): AdminGitHub => {
    const callbackUrl = `${PUBLIC_URL}/api/connections/github/authorization/callback`
    if (!app) return { configured: false, callbackUrl }
    const missing = ['administration', 'contents', 'metadata'].filter((k) => !app!.permissions[k])
    return {
      configured: true,
      appId: app.appId,
      clientId: app.clientId,
      slug: MOCK_GITHUB_APP.slug,
      name: MOCK_GITHUB_APP.name,
      owner: 'kazuhahub',
      htmlUrl: MOCK_GITHUB_APP.htmlUrl,
      installUrl: `${MOCK_GITHUB_APP.htmlUrl}/installations/new`,
      privateKey: { set: true, fingerprint: app.fingerprint },
      clientSecret: { set: app.clientSecret },
      callbackUrl,
      permissions: app.permissions,
      missing,
      userAuthorization: userAuthorization(),
      checked: new Date(app.checked).toISOString().replace(/\.\d+Z$/, 'Z'),
      spaces: deps.spacesOnApp(),
      authorizations: auths.size,
      gaps: app.gaps,
      anyAccount: app.anyAccount,
    }
  }

  // Reads what GitHub says about the App and its installations now.
  const check = (): AdminGitHub => {
    const a = requireApp()
    a.permissions = { ...side.permissions }
    a.gaps = side.gaps.map((g) => ({ ...g, missing: [...g.missing] }))
    a.anyAccount = side.anyAccount
    a.checked = deps.now()
    return admin()
  }

  // The manifest flow: the browser posts the manifest to GitHub and comes back with a code.
  const manifestStart = (
    userId: string,
    body: Record<string, unknown>,
  ): { start: GitHubManifestStart; state: string } => {
    const org = typeof body.organization === 'string' ? body.organization.trim() : ''
    const name =
      typeof body.name === 'string' && body.name.trim() !== '' ? body.name.trim() : 'PetalBook notes.example.test'
    if (org !== '' && !/^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$/.test(org)) throw invalid('organization 不是 GitHub 账号名')
    if (Array.from(name).length > 34) throw invalid('name 最多 34 个字符')
    if (app && (deps.spacesOnApp() > 0 || auths.size > 0))
      throw new HttpError(409, 'provider_in_use', '还有空间在用这个 App，或有人连着 GitHub：先改掉那些再换 App')
    const state = randomState()
    manifests.set(state, { userId, created: deps.now() })
    const ret = `${PUBLIC_URL}/api/connections/github/manifest/callback`
    return {
      state,
      start: {
        url: `https://github.com${org ? `/organizations/${org}` : ''}/settings/apps/new?state=${state}`,
        manifest: {
          name,
          url: PUBLIC_URL,
          hook_attributes: { url: PUBLIC_URL, active: false },
          redirect_url: ret,
          callback_urls: [`${PUBLIC_URL}/api/connections/github/authorization/callback`],
          setup_url: ret,
          public: true,
          request_oauth_on_install: true,
          default_permissions: { administration: 'write', contents: 'write', metadata: 'read' },
        },
      },
    }
  }

  // GitHub sends the administrator's browser back: always a redirect.
  const manifestCallback = (
    userId: string | null,
    siteAdmin: boolean,
    cookieState: string | undefined,
    q: URLSearchParams,
  ): { to: string; clear: boolean } => {
    const expired = { to: '/admin/storage?connection_app_error=expired', clear: false }
    const state = q.get('state') ?? ''
    const at = manifests.get(state)
    if (
      !at ||
      cookieState !== state ||
      !userId ||
      !siteAdmin ||
      at.userId !== userId ||
      deps.now() - at.created > 600_000
    )
      return expired
    manifests.delete(state)
    if (!q.get('code') || q.get('code') === 'bad')
      return { to: '/admin/storage?connection_app_error=provider_error', clear: true }
    if (app && (deps.spacesOnApp() > 0 || auths.size > 0))
      return { to: '/admin/storage?connection_app_error=app_in_use', clear: true }
    app = {
      appId: '7654321',
      clientId: 'Iv23liMockManifest',
      clientSecret: true,
      fingerprint: 'SHA256:mockManifestFingerprint0123456789abcdefghijk',
      permissions: { administration: 'write', contents: 'write', metadata: 'read' },
      checked: deps.now(),
      gaps: [],
      anyAccount: true,
    }
    side.permissions = { ...app.permissions }
    return { to: '/admin/storage?connection_app=created', clear: true }
  }

  const putAdmin = (input: PutAdminGitHubInput): AdminGitHub => {
    const appId = (input.appId ?? app?.appId ?? '').trim()
    const first = app === null
    if (first && (appId === '' || !input.privateKey)) throw invalid('第一次保存需要 appId 和 privateKey')
    if (
      input.privateKey !== undefined &&
      input.privateKey !== '' &&
      !/-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(input.privateKey)
    )
      throw new HttpError(400, 'key_invalid', '私钥必须是 PEM 格式的 RSA 密钥')
    if (!/^\d+$/.test(appId)) throw new HttpError(400, 'app_rejected', 'GitHub 不认这个 App 编号')
    const clientId = input.clientId === undefined ? (app?.clientId ?? '') : input.clientId.trim()
    const secretGiven = !!input.clientSecret
    if (clientId !== '' && !secretGiven && !(app && app.clientSecret && clientId === app.clientId))
      throw invalid('clientId 和 clientSecret 要一起给')
    if (clientId !== '' && secretGiven && input.clientSecret === 'wrong')
      throw new HttpError(400, 'client_rejected', 'GitHub 不认这对 client id 和 secret')
    if (
      app &&
      (appId !== app.appId || (app.clientId !== '' && clientId !== app.clientId)) &&
      (deps.spacesOnApp() > 0 || auths.size > 0)
    ) {
      throw new HttpError(409, 'provider_in_use', '还有空间在用这个 App，或有人连着 GitHub：先改掉那些再换 App')
    }
    app = {
      appId,
      clientId,
      clientSecret: clientId !== '' && (secretGiven || (app?.clientSecret ?? false)),
      fingerprint: input.privateKey
        ? `SHA256:${btoa(input.privateKey)
            .replace(/[^A-Za-z0-9]/g, '')
            .slice(-43)
            .padEnd(43, 'x')}`
        : (app?.fingerprint ?? ''),
      permissions: app?.permissions ?? { administration: 'write', contents: 'write', metadata: 'read' },
      checked: deps.now(),
      gaps: app?.gaps ?? [],
      anyAccount: app?.anyAccount ?? true,
    }
    return admin()
  }

  const deleteAdmin = () => {
    if (!app) return
    if (deps.spacesOnApp() > 0)
      throw new HttpError(409, 'provider_in_use', `还有 ${deps.spacesOnApp()} 个空间在用这个 App：先改掉它们`)
    app = null
    auths.clear()
    for (const i of insts) i.tenants = []
  }

  const asInstallation = (i: InstRec): GitHubInstallation => ({
    id: i.id,
    account: { login: i.login, type: i.type, id: i.id + 1000 },
    repositorySelection: i.selection,
    suspended: false,
    permissions: app?.permissions ?? {},
    htmlUrl: `https://github.com/organizations/${i.login}/settings/installations/${i.id}`,
    tenants: [...i.tenants].sort(),
    spaces: 0,
  })

  const installations = (cursor: string | null): CursorPage<GitHubInstallation> => {
    requireApp()
    return page(insts.map(asInstallation), cursor)
  }

  const share = (id: number, tenants: unknown): GitHubInstallation => {
    requireApp()
    if (!Array.isArray(tenants) || tenants.some((x) => typeof x !== 'string'))
      throw invalid('给 tenants：要共享给的租户 id（不共享就是 []）')
    const i = insts.find((x) => x.id === id)
    if (!i) throw new HttpError(404, 'not_found', 'GitHub 不认识这个安装')
    const set = [...new Set(tenants as string[])].filter((x) => x !== '')
    if (set.length > 0 && i.type !== 'Organization') throw invalid('只有组织的安装可以共享')
    for (const x of set) if (!deps.tenantExists(x)) throw invalid(`没有这个租户：${x}`)
    i.tenants = set
    return asInstallation(i)
  }

  // "I deleted it": forgets the person's reminder; there being none is fine.
  const dismiss = (userId: string, repoId: number) => {
    leftovers.set(
      userId,
      (leftovers.get(userId) ?? []).filter((l) => l.repoId !== repoId),
    )
  }
  // A space follows the repository: nobody needs reminding of it any more.
  const repoConnected = (repoId: number) => {
    for (const [u, list] of leftovers)
      leftovers.set(
        u,
        list.filter((l) => l.repoId !== repoId),
      )
  }

  // A deleted tenant stops being shared with.
  const tenantDeleted = (id: string) => {
    for (const i of insts) i.tenants = i.tenants.filter((x) => x !== id)
  }

  return {
    info,
    start,
    callback,
    disconnect,
    owners,
    reposOf,
    create,
    existing,
    admin,
    putAdmin,
    check,
    manifestStart,
    manifestCallback,
    deleteAdmin,
    installations,
    share,
    tenantDeleted,
    dismiss,
    repoConnected,
    // The attempt a browser holds: the cookie carries the state.
    // Mock only: what a test or a person playing with the mock can switch.
    expire: (userId: string) => {
      const a = connection(userId)
      if (a) a.state = 'expired'
    },
    setTrap: (name: keyof typeof traps, on: boolean) => {
      traps[name] = on
    },
    // Mock only: what GitHub says about the App now (seen by the administration after a re-check).
    setSide: (v: Partial<Side>) => {
      if (v.permissions) side.permissions = v.permissions
      if (v.gaps) side.gaps = v.gaps
      if (v.anyAccount !== undefined) side.anyAccount = v.anyAccount
    },
    // Mock only: a person is connected without going through GitHub.
    connect: (userId: string, login: string) => {
      auths.set(userId, { login: login.toLowerCase(), githubId: ++nextId, state: 'ok', connected: deps.now() })
      ensureHome(login.toLowerCase())
    },
    connectedLogin: (userId: string) => connection(userId)?.login ?? null,
    userOf: (userId: string) => usable(userId),
  }
}

export type GitHubBook = ReturnType<typeof createGitHubBook>
