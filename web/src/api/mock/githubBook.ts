// What the in-memory server plays for GitHub (docs/api.md, "GitHub" and
// "Administration", "GitHub App"): the App's settings, each person's connection,
// a few accounts and repositories to choose from, and the failures a real GitHub
// has (a name taken, no permission, an installation that does not reach the new
// repository, an authorisation that ran out). There is no real GitHub behind it.

import { HttpError } from './store.ts'
import type { AdminGitHub, GitHubAuthorization, GitHubCursorPage, GitHubInfo, GitHubInstallation, GitHubOwner, GitHubRepo, GitHubVia, PutAdminGitHubInput } from '../types.ts'

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
  shared: boolean
  // The people who are members of the organisation (by the login of their GitHub account); `*` is everyone.
  members: string[]
}

const PAGE = 30
const PUBLIC_URL = 'https://notes.example.test'
const NAME_RE = /^[A-Za-z0-9._-]{1,100}$/

export const MOCK_GITHUB_APP = { name: 'PetalBook Notes', slug: 'petalbook-notes', htmlUrl: 'https://github.com/apps/petalbook-notes' } as const

export function createGitHubBook(opts: MockGitHubOptions, deps: { now: () => number; random: () => number; spacesUsing: (userId: string) => number; spacesOnApp: () => number }) {
  let app: AppRec | null = opts.configured
    ? { appId: '1234567', clientId: 'Iv23liMockClientId', clientSecret: true, fingerprint: 'SHA256:mockFingerprint0123456789abcdefghijklmnopqrs', permissions: { administration: 'write', contents: 'write', metadata: 'read' }, checked: deps.now() }
    : null
  const auths = new Map<string, AuthRec>()
  const attempts = new Map<string, Attempt>()
  let nextId = 5000
  let repoId = 880_000_000
  const insts: InstRec[] = [
    { id: 5001, login: 'kazuhahub', type: 'Organization', selection: 'all', shared: false, members: ['*'] },
    { id: 5002, login: 'petal-team', type: 'Organization', selection: 'selected', shared: false, members: [] },
    { id: 5003, login: 'tang-poetry', type: 'Organization', selection: 'all', shared: false, members: ['*'] },
  ]
  const repos: RepoRec[] = [
    { id: ++repoId, owner: 'kazuhahub', name: 'notes', private: true },
    { id: ++repoId, owner: 'kazuhahub', name: 'wiki', private: false },
    { id: ++repoId, owner: 'petal-team', name: 'docs', private: true },
  ]
  // Mock only: what the next creation or connection finds.
  const traps = { noPermission: false, notInInstallation: false, pushFails: false }

  const key = (s: string) => s.toLowerCase()
  const invalid = (m: string) => new HttpError(400, 'invalid', m)
  const notConfigured = () => new HttpError(409, 'github_not_configured', '这台服务器没有设置 GitHub App（管理员在管理页里设置）')
  const notAuthorized = () => new HttpError(409, 'github_not_authorized', '请先连接你的 GitHub 账号')
  const requireApp = () => {
    if (!app) throw notConfigured()
    return app
  }
  const userAuthorization = () => !!app && app.clientId !== '' && app.clientSecret
  // Everyone has a repository of their own to choose.
  const ensureHome = (login: string) => {
    if (!repos.some((r) => key(r.owner) === key(login))) repos.push({ id: ++repoId, owner: login, name: 'journal', private: true })
  }
  const connection = (userId: string): AuthRec | null => auths.get(userId) ?? null
  const usable = (userId: string): AuthRec | null => {
    const a = connection(userId)
    return a && a.state === 'ok' ? a : null
  }
  const member = (i: InstRec, login: string | undefined) => !!login && (i.members.includes('*') || i.members.map(key).includes(key(login)))

  const authOf = (userId: string): GitHubAuthorization | null => {
    const a = connection(userId)
    if (!a) return null
    return {
      login: a.login,
      githubId: a.githubId,
      state: a.state,
      connected: new Date(a.connected).toISOString().replace(/\.\d+Z$/, 'Z'),
      refreshExpires: new Date(a.connected + 180 * 86_400_000).toISOString().replace(/\.\d+Z$/, 'Z'),
      spaces: deps.spacesUsing(userId),
    }
  }

  const info = (userId: string): GitHubInfo => ({
    app: app ? { ...MOCK_GITHUB_APP, installUrl: `${MOCK_GITHUB_APP.htmlUrl}/installations/new` } : null,
    userAuthorization: userAuthorization(),
    authorization: authOf(userId),
  })

  // --- connecting -----------------------------------------------------------------

  const start = (userId: string, body: Record<string, unknown>): { url: string; state: string } => {
    requireApp()
    if (!userAuthorization()) throw notConfigured()
    const state = Array.from({ length: 24 }, () => 'abcdefghijklmnopqrstuvwxyz0123456789'[Math.floor(deps.random() * 36)]).join('')
    attempts.set(state, { userId, ret: typeof body.return === 'string' && body.return.startsWith('/') && !body.return.startsWith('//') ? body.return : '/', created: deps.now() })
    return { url: `/api/github/authorization/callback?state=${state}`, state }
  }

  // The browser comes back: always a redirect.
  const callback = (userId: string | null, cookieState: string | undefined, q: URLSearchParams): { to: string; clear: boolean } => {
    const fail = (c: string) => ({ to: `/settings?github_error=${c}`, clear: true })
    const state = q.get('state') ?? ''
    const at = attempts.get(state)
    if (!at || cookieState !== state || !userId || at.userId !== userId || deps.now() - at.created > 600_000) return { to: '/settings?github_error=expired', clear: false }
    attempts.delete(state)
    if (q.get('error')) return fail('denied')
    const login = (q.get('login') ?? `${userId.slice(0, 6).toLowerCase()}-gh`).toLowerCase()
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

  const page = <T>(all: T[], cursor: string | null): GitHubCursorPage<T> => {
    const start = cursor === null || cursor === '' ? 0 : Number(cursor.replace(/^c/, ''))
    if (!Number.isInteger(start) || start < 0) throw invalid('the cursor is not one this server made')
    const items = all.slice(start, start + PAGE)
    return { items, next: start + PAGE < all.length ? `c${start + PAGE}` : '' }
  }

  const owners = (userId: string, cursor: string | null): GitHubCursorPage<GitHubOwner> => {
    requireApp()
    const a = usable(userId)
    const out: GitHubOwner[] = []
    const seen = new Set<number>()
    if (a) {
      out.push({ login: a.login, type: 'User', via: 'user', installationId: null })
      for (const i of insts) {
        if (i.type === 'Organization' && member(i, a.login)) {
          out.push({ login: i.login, type: 'Organization', via: 'user', installationId: i.id })
          seen.add(i.id)
        }
      }
    }
    for (const i of insts) if (i.shared && i.type === 'Organization' && !seen.has(i.id)) out.push({ login: i.login, type: 'Organization', via: 'app', installationId: i.id })
    return page(out, cursor)
  }

  const asRepo = (r: RepoRec): GitHubRepo => ({ id: r.id, fullName: `${r.owner}/${r.name}`, private: r.private, defaultBranch: 'main', htmlUrl: `https://github.com/${r.owner}/${r.name}`, canPush: true })

  const reposOf = (userId: string, which: { installation: number } | { mine: true }, cursor: string | null): GitHubCursorPage<GitHubRepo> => {
    requireApp()
    if ('mine' in which) {
      const a = usable(userId)
      if (!a) throw notAuthorized()
      return page(repos.filter((r) => key(r.owner) === key(a.login)).map(asRepo), cursor)
    }
    const i = insts.find((x) => x.id === which.installation)
    const a = usable(userId)
    if (!i || !(i.shared || (a && member(i, a.login)))) throw new HttpError(404, 'not_found', '没有这个安装')
    return page(repos.filter((r) => key(r.owner) === key(i.login)).map(asRepo), cursor)
  }

  // --- making and connecting repositories ---------------------------------------------

  // Whose token would create in `owner`; the answer a refused owner is.
  const plan = (userId: string, owner: string): { login: string; via: GitHubVia; installation: InstRec | null } => {
    requireApp()
    const a = usable(userId)
    if (a && key(a.login) === key(owner)) return { login: a.login, via: 'user', installation: null }
    const i = insts.find((x) => key(x.login) === key(owner) && x.type === 'Organization')
    if (i && a && member(i, a.login)) return { login: i.login, via: 'user', installation: i }
    if (i && i.shared) return { login: i.login, via: 'app', installation: i }
    const c = connection(userId)
    if (c && c.state === 'expired' && key(c.login) === key(owner)) throw notAuthorized()
    throw new HttpError(403, 'github_owner_not_allowed', `不能在 ${owner} 下建仓库`)
  }

  const create = (userId: string, input: { owner: string; name: string; private: boolean }): { repo: RepoRec; via: GitHubVia; installationId: number } => {
    const p = plan(userId, input.owner)
    if (!NAME_RE.test(input.name) || input.name === '.' || input.name === '..' || input.name.toLowerCase().endsWith('.git')) throw invalid('仓库名只能用字母、数字、点、下划线和连字符，最多 100 个字符')
    if (traps.noPermission) throw new HttpError(403, 'github_no_permission', 'GitHub 拒绝建仓库：你在这个组织里没有建仓库的权限')
    if (repos.some((r) => key(r.owner) === key(p.login) && key(r.name) === key(input.name))) throw new HttpError(409, 'github_name_taken', `${p.login} 下已经有同名仓库`)
    const repo: RepoRec = { id: ++repoId, owner: p.login, name: input.name, private: input.private }
    repos.push(repo)
    if (p.installation && p.installation.selection === 'selected' && traps.notInInstallation) {
      throw new HttpError(409, 'github_repo_not_in_installation', `${p.login} 的安装没有包含新仓库 ${p.login}/${input.name}，也没能加进去`, {
        repo: { id: repo.id, fullName: `${repo.owner}/${repo.name}`, htmlUrl: `https://github.com/${repo.owner}/${repo.name}` },
      })
    }
    if (traps.pushFails) {
      // The repository was made and stays, empty: a retry with create: false connects to it.
      throw new HttpError(502, 'github_push_failed', `仓库 ${p.login}/${input.name} 已经建好，但第一次推送没有成功`, {
        repo: { id: repo.id, fullName: `${repo.owner}/${repo.name}`, htmlUrl: `https://github.com/${repo.owner}/${repo.name}` },
      })
    }
    return { repo, via: p.installation ? 'app' : 'user', installationId: p.installation?.id ?? 0 }
  }

  // A repository that exists: the App when the installation reaches it and the caller may use it, else the caller's own connection.
  const existing = (userId: string, owner: string, name: string, via: GitHubVia | ''): { repo: RepoRec; via: GitHubVia; installationId: number } => {
    requireApp()
    const repo = repos.find((r) => key(r.owner) === key(owner) && key(r.name) === key(name))
    const a = usable(userId)
    const i = insts.find((x) => key(x.login) === key(owner))
    const tryApp = () => {
      if (!repo || !i || (i.selection === 'selected' && traps.notInInstallation)) throw new HttpError(409, 'github_repo_not_in_installation', 'App 没有装在这个仓库上，或者它的安装够不着这个仓库')
      if (!i.shared) {
        if (!a) throw notAuthorized()
        if (!member(i, a.login)) throw new HttpError(403, 'github_owner_not_allowed', '这个安装没有共享，而你的 GitHub 连接不能推送到这个仓库')
      }
      return { repo, via: 'app' as const, installationId: i.id }
    }
    const tryUser = () => {
      if (!a) throw notAuthorized()
      if (!repo || !(key(a.login) === key(owner) || (i && member(i, a.login)))) throw new HttpError(403, 'github_owner_not_allowed', '你的 GitHub 连接不能推送到这个仓库')
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
    const callbackUrl = `${PUBLIC_URL}/api/github/authorization/callback`
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
    }
  }

  const putAdmin = (input: PutAdminGitHubInput): AdminGitHub => {
    const appId = (input.appId ?? app?.appId ?? '').trim()
    const first = app === null
    if (first && (appId === '' || !input.privateKey)) throw invalid('第一次保存需要 appId 和 privateKey')
    if (input.privateKey !== undefined && input.privateKey !== '' && !/-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(input.privateKey)) throw new HttpError(400, 'github_key_invalid', '私钥必须是 PEM 格式的 RSA 密钥')
    if (!/^\d+$/.test(appId)) throw new HttpError(400, 'github_app_rejected', 'GitHub 不认这个 App 编号')
    const clientId = input.clientId === undefined ? (app?.clientId ?? '') : input.clientId.trim()
    const secretGiven = !!input.clientSecret
    if (clientId !== '' && !secretGiven && !(app && app.clientSecret && clientId === app.clientId)) throw invalid('clientId 和 clientSecret 要一起给')
    if (clientId !== '' && secretGiven && input.clientSecret === 'wrong') throw new HttpError(400, 'github_client_rejected', 'GitHub 不认这对 client id 和 secret')
    if (app && (appId !== app.appId || (app.clientId !== '' && clientId !== app.clientId)) && (deps.spacesOnApp() > 0 || auths.size > 0)) {
      throw new HttpError(409, 'github_app_in_use', '还有空间在用这个 App，或有人连着 GitHub：先改掉那些再换 App')
    }
    app = {
      appId,
      clientId,
      clientSecret: clientId !== '' && (secretGiven || (app?.clientSecret ?? false)),
      fingerprint: input.privateKey ? `SHA256:${btoa(input.privateKey).replace(/[^A-Za-z0-9]/g, '').slice(-43).padEnd(43, 'x')}` : (app?.fingerprint ?? ''),
      permissions: app?.permissions ?? { administration: 'write', contents: 'write', metadata: 'read' },
      checked: deps.now(),
    }
    return admin()
  }

  const deleteAdmin = () => {
    if (!app) return
    if (deps.spacesOnApp() > 0) throw new HttpError(409, 'github_app_in_use', `还有 ${deps.spacesOnApp()} 个空间在用这个 App：先改掉它们`)
    app = null
    auths.clear()
    for (const i of insts) i.shared = false
  }

  const asInstallation = (i: InstRec): GitHubInstallation => ({
    id: i.id,
    account: { login: i.login, type: i.type, id: i.id + 1000 },
    repositorySelection: i.selection,
    suspended: false,
    permissions: app?.permissions ?? {},
    htmlUrl: `https://github.com/organizations/${i.login}/settings/installations/${i.id}`,
    shared: i.shared,
    spaces: 0,
  })

  const installations = (cursor: string | null): GitHubCursorPage<GitHubInstallation> => {
    requireApp()
    return page(insts.map(asInstallation), cursor)
  }

  const share = (id: number, shared: boolean): GitHubInstallation => {
    requireApp()
    const i = insts.find((x) => x.id === id)
    if (!i) throw new HttpError(404, 'not_found', 'GitHub 不认识这个安装')
    if (shared && i.type !== 'Organization') throw invalid('只有组织的安装可以共享')
    i.shared = shared
    return asInstallation(i)
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
    deleteAdmin,
    installations,
    share,
    // The attempt a browser holds: the cookie carries the state.
    // Mock only: what a test or a person playing with the mock can switch.
    expire: (userId: string) => {
      const a = connection(userId)
      if (a) a.state = 'expired'
    },
    setTrap: (name: keyof typeof traps, on: boolean) => {
      traps[name] = on
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
