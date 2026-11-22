// An in-memory implementation of docs/api.md, for `npm run dev:mock` and for
// tests. Single-user mode is the default; accounts mode adds sessions (a
// cookie stand-in), invites, spaces per user and a fake OpenID Connect
// provider. It has no DOM or Node dependencies.

import type { AdminSpace, ConnectRemoteInput, Identity, Invite, Member, Page, Permission, Remote, RemoteKey, RemoteState, Role, ServerStatus, SpaceInfo, SpaceRole, User } from '../types.ts'
import { gitBlobId } from './sha1.ts'
import { ALPHABET, HttpError, compareCodePoints, createSpaceStore, isRecord, rfc3339, validFolder, type SpaceStore } from './store.ts'
import { SPACE_NAME } from './seed.ts'
import { createFileStore, DEFAULT_MAX_UPLOAD, type FileStore } from './files.ts'
import { DEFAULT_MAX_IMPORT, runImport } from './importer.ts'
import { folderRuleMessage } from '../../lib/folderRules.ts'

const DEFAULT_INBOX = 'Inbox'

export { validFolder } from './store.ts'

export interface MockResponse {
  status: number
  body?: unknown
  // Response headers other than the content type: Set-Cookie, Location, Retry-After.
  headers?: Record<string, string>
  // A file's bytes (GET .../files/...): sent as they are, with `headers`, instead of JSON.
  bytes?: Uint8Array
}

export interface MockOptions {
  now?: () => number
  // Start with no pages at all.
  empty?: boolean
  // Fixed ids (seed pages use deterministic ids so tests can address them).
  random?: () => number
  mode?: 'single' | 'accounts'
  // Accounts mode: the seeded administrator. `false` starts with no user at
  // all, which is the first-run state (`session.setup` is true).
  admin?: { username: string; password: string; name?: string } | false
  // The token the first-run page needs.
  setupToken?: string
  // Accounts mode: a configured OpenID Connect provider. The mock provider
  // always signs in the same person (`sub`).
  oidc?: { name: string; autoCreate?: boolean; sub?: string } | null
  // Accounts mode: an invite that exists from the start, with this token.
  invite?: { token: string; role?: Role }
  // Drafts the server commits by itself (docs/api.md: --draft-idle, --draft-max).
  // Accounts mode: how many spaces one person may own (POST /api/spaces is 403 beyond it).
  maxSpaces?: number
  // Single-user mode: the inbox folder (`--inbox`); default `Inbox`.
  inbox?: string
  draftIdleMs?: number
  draftMaxMs?: number
  // Following a remote repository (docs/api.md, "Remote repository").
  remote?: MockRemoteOptions
  // The largest upload in bytes (`--max-upload`); 10 MiB by default.
  maxUpload?: number
  // The largest import in bytes (`--max-import`); 100 MiB by default.
  maxImport?: number
}

// What the mock plays for the remote endpoints.
export interface MockRemoteOptions {
  // Hosts the administrator allows (`--remote-host`); `*` allows any. Default:
  // github.com, gitlab.com, codeberg.org, bitbucket.org.
  hosts?: string[]
  // What a connecting PUT finds at the address, unless the address says so
  // (it contains one of these words, which is handy by hand):
  //   no-branch    the remote has no such branch yet: the space's history is pushed
  //   ahead        the remote has commits the space lacks: the space moves forward
  //   behind       the space has commits the remote lacks: the remote is moved forward
  //   diverged     both have commits of their own: 409, unless the space is pristine
  //   unreachable  503: cannot be reached
  //   refused      503: the credential is refused
  find?: 'no-branch' | 'ahead' | 'behind' | 'diverged' | 'unreachable' | 'refused'
  // The first space starts out following a remote, in this state.
  start?: RemoteState
}

const DEFAULT_HOSTS = ['github.com', 'gitlab.com', 'codeberg.org', 'bitbucket.org']

export const MOCK_ADMIN = { username: 'admin', password: 'petalbook-admin', name: '管理员' }
export const MOCK_SETUP_TOKEN = 'mock-setup-token'
export const MOCK_INVITE_TOKEN = 'mock-invite-token'
export const SESSION_COOKIE = 'pb_session'
const OIDC_COOKIE = 'pb_oidc'
const LOCAL_USER: User = { id: 'local', username: 'local', name: 'Local user', email: '', role: 'admin', disabled: false, created: '2026-01-01T00:00:00Z' }

interface UserRec extends User {
  passwordHash?: string
  identities: (Identity & { sub: string })[]
}
interface InviteRec {
  id: string
  tokenHash: string
  role: Role
  created: number
  expires: number
  createdBy: string
  used: boolean
}
interface RemoteRec {
  url: string
  branch: string
  credential: { type: 'ssh' } | { type: 'token'; username: string; token: string }
  state: RemoteState
  lastSync?: number
  error: string
  hostKey?: string
  rewritten: { lastGood: string; remote: string } | null
}
interface MemberRec {
  role: SpaceRole
  // Order of joining: the first owner is the one who joined first.
  joined: number
}
interface SpaceRec {
  id: string
  name: string
  members: Map<string, MemberRec>
  personal: boolean
  created: number
  // Switched off by an administrator (docs/api.md, "Administration").
  disabled: boolean
  // The last stored measurement; the real server measures in the background,
  // the mock only when asked.
  size: { bytes: number; at: number } | null
  inbox: string
  store: SpaceStore
  files: FileStore
  remote: RemoteRec | null
  key: RemoteKey | null
  // Mock only: whether the pretend remote answers.
  reachable: boolean
}
interface OidcAttempt {
  intent: string
  token: string
  ret: string
  userId: string | null
  created: number
  used: boolean
}

// What each role may do: the one table the server has (internal/perm). The app
// reads `permissions` from the space and never this.
const ROLE_PERMISSIONS: Record<SpaceRole, Permission[]> = {
  owner: ['read', 'write', 'members', 'manage'],
  editor: ['read', 'write'],
  viewer: ['read'],
}
const ROLES = Object.keys(ROLE_PERMISSIONS)
const NO_PERMISSION = (need: Permission) => `your role in this space does not allow that; it needs the ${need} permission`

const USERNAME_RE = /^[a-z0-9][a-z0-9._-]{1,31}$/
const hashOf = (s: string) => gitBlobId(`mock:${s}`)
const codePoints = (s: string) => Array.from(s).length

const parseCookies = (header: string | null | undefined): Record<string, string> => {
  const out: Record<string, string> = {}
  for (const part of (header ?? '').split(';')) {
    const i = part.indexOf('=')
    if (i > 0) out[part.slice(0, i).trim()] = part.slice(i + 1).trim()
  }
  return out
}
const setCookie = (name: string, value: string, maxAge?: number) => `${name}=${value}; Path=/; HttpOnly; SameSite=Lax${maxAge === undefined ? '' : `; Max-Age=${maxAge}`}`

// What the sign-in cookie holds is meaningless to the client, as in the real server.
const plainPath = (p: unknown): string => (typeof p === 'string' && p.startsWith('/') && !p.startsWith('//') && !p.includes('\\') ? p : '/')

export function createMockServer(options: MockOptions = {}) {
  const now = options.now ?? Date.now
  const random = options.random ?? Math.random
  const mode = options.mode ?? 'single'
  const accounts = mode === 'accounts'
  const setupToken = options.setupToken ?? MOCK_SETUP_TOKEN
  const idleMs = options.draftIdleMs ?? 120_000
  const maxMs = options.draftMaxMs ?? 600_000
  const maxUpload = options.maxUpload ?? DEFAULT_MAX_UPLOAD
  const maxImport = options.maxImport ?? DEFAULT_MAX_IMPORT
  const oidc = accounts ? (options.oidc ?? null) : null
  let counter = 0
  const startedAt = now()

  const users = new Map<string, UserRec>()
  const sessions = new Map<string, string>()
  const invites = new Map<string, InviteRec>()
  const spaces = new Map<string, SpaceRec>()
  const oidcAttempts = new Map<string, OidcAttempt>()
  const failures = new Map<string, { count: number; first: number }>()

  const randomString = (n: number) => {
    let s = ''
    for (let i = 0; i < n; i++) s += ALPHABET[Math.floor(random() * 32)]
    return s
  }
  const newId = (taken: (id: string) => boolean) => {
    for (;;) {
      const id = '01' + randomString(8)
      if (!taken(id)) return id
    }
  }

  const publicUser = (u: UserRec): User => ({ id: u.id, username: u.username, name: u.name, email: u.email, role: u.role, disabled: u.disabled, created: u.created })
  // The first owner: who the space's own commits are credited to.
  const ownerOf = (s: SpaceRec): UserRec | undefined => {
    const owners = [...s.members].filter(([, m]) => m.role === 'owner').sort((a, b) => a[1].joined - b[1].joined)
    return owners[0] ? users.get(owners[0][0]) : undefined
  }
  const spaceInfo = (s: SpaceRec, u: UserRec): SpaceInfo => {
    const role = s.members.get(u.id)?.role
    return {
      id: s.id,
      name: s.name,
      role: role ?? '',
      // An administrator who is not a member may do everything.
      permissions: [...(role ? ROLE_PERMISSIONS[role] : ROLE_PERMISSIONS.owner)],
      personal: s.personal,
      sync: s.remote ? s.remote.state : 'local',
      inbox: s.inbox,
      disabled: s.disabled,
    }
  }

  // Why writes fail right now, if they do (docs/api.md: 503 `unavailable`).
  const blockedReason = (s: SpaceRec): string | null => {
    if (!s.remote) return null
    if (s.remote.state === 'offline') return `远端仓库现在连不上${s.remote.error ? `（${s.remote.error}）` : ''}。这次修改没有写入，已写的草稿还在。`
    if (s.remote.state === 'rewritten') return '远端仓库的历史被改写过，这个空间已暂停写入。请空间的所有者在空间设置里查看并决定是否接受远端的历史。'
    return null
  }

  const addSpace = (id: string, name: string, ownerId: string, personal: boolean, seed: boolean, inbox = options.inbox ?? DEFAULT_INBOX) => {
    const rec: SpaceRec = {
      id,
      name,
      members: new Map([[ownerId, { role: 'owner', joined: ++counter }]]),
      personal,
      created: now(),
      disabled: false,
      size: null,
      inbox,
      store: undefined as unknown as SpaceStore,
      files: createFileStore(now),
      remote: null,
      key: null,
      reachable: true,
    }
    rec.store = createSpaceStore({ now, random, seed, author: () => ownerOf(rec)?.name ?? 'Local user', blocked: () => blockedReason(rec) })
    spaces.set(id, rec)
    return rec
  }

  const addUser = (input: { username: string; name?: string; role: Role; password?: string; identity?: { sub: string; label: string } }, seedSpace: boolean) => {
    const id = newId((x) => users.has(x))
    const u: UserRec = {
      id,
      username: input.username,
      name: input.name || input.username,
      email: '',
      role: input.role,
      disabled: false,
      created: rfc3339(now()),
      passwordHash: input.password === undefined ? undefined : hashOf(input.password),
      identities: [],
    }
    if (input.identity) u.identities.push({ id: newId(() => false), provider: oidc?.name ?? 'SSO', label: input.identity.label, created: rfc3339(now()), sub: input.identity.sub })
    users.set(id, u)
    // Every account gets a personal space.
    addSpace(newId((x) => spaces.has(x)), SPACE_NAME, id, true, seedSpace)
    return u
  }

  // --- initial state ---
  if (!accounts) {
    users.set(LOCAL_USER.id, { ...LOCAL_USER, identities: [] })
    addSpace('default', SPACE_NAME, LOCAL_USER.id, true, !options.empty)
  } else {
    if (options.admin !== false) {
      const a = options.admin ?? MOCK_ADMIN
      addUser({ username: a.username, name: a.name, role: 'admin', password: a.password }, !options.empty)
    }
    if (options.invite) {
      const id = newId((x) => invites.has(x))
      invites.set(id, { id, tokenHash: hashOf(options.invite.token), role: options.invite.role ?? 'member', created: now(), expires: now() + 7 * 86_400_000, createdBy: [...users.keys()][0] ?? '', used: false })
    }
  }

  // --- request context ---
  interface Ctx {
    user: UserRec | null
    token: string | null
    cookies: Record<string, string>
  }
  const contextOf = (cookie: string | null | undefined): Ctx => {
    const cookies = parseCookies(cookie)
    if (!accounts) return { user: users.get('local') ?? null, token: null, cookies }
    const token = cookies[SESSION_COOKIE] ?? null
    const uid = token ? sessions.get(token) : undefined
    const user = uid ? (users.get(uid) ?? null) : null
    return { user: user && !user.disabled ? user : null, token, cookies }
  }
  const requireUser = (ctx: Ctx): UserRec => {
    if (!ctx.user) throw new HttpError(401, 'unauthenticated', '请先登录')
    return ctx.user
  }
  const requireAdmin = (ctx: Ctx): UserRec => {
    const u = requireUser(ctx)
    if (u.role !== 'admin') throw new HttpError(403, 'forbidden', '需要管理员权限')
    return u
  }
  // The sign-in, account, user, invite and space-management endpoints do not
  // exist in single-user mode.
  const accountsOnly = () => {
    if (!accounts) throw new HttpError(404, 'not_found', '没有这个接口')
  }

  const callerSpaces = (u: UserRec | null): SpaceRec[] => {
    if (!u) return []
    return [...spaces.values()]
      .filter((s) => s.members.has(u.id))
      .sort((a, b) => Number(b.personal) - Number(a.personal) || compareCodePoints(a.name, b.name))
  }

  const startSession = (u: UserRec): string => {
    const token = randomString(32)
    sessions.set(token, u.id)
    return setCookie(SESSION_COOKIE, token)
  }
  const endSessions = (userId: string, except?: string | null) => {
    for (const [t, uid] of [...sessions]) if (uid === userId && t !== except) sessions.delete(t)
  }

  // --- validation ---
  const checkUsername = (raw: unknown): string => {
    if (typeof raw !== 'string') throw new HttpError(400, 'invalid', '缺少用户名')
    const name = raw.toLowerCase()
    if (!USERNAME_RE.test(name)) throw new HttpError(400, 'invalid', '用户名是 2 到 32 个字符，只能用小写字母、数字、点、下划线和连字符，且以字母或数字开头')
    return name
  }
  const checkPassword = (raw: unknown, what = '密码'): string => {
    if (typeof raw !== 'string') throw new HttpError(400, 'invalid', `缺少${what}`)
    if (Array.from(raw).length < 8) throw new HttpError(400, 'invalid', `${what}至少 8 个字符`)
    if (new TextEncoder().encode(raw).length > 1024) throw new HttpError(400, 'invalid', `${what}太长`)
    return raw
  }
  const checkName = (raw: unknown, fallback?: string): string => {
    if (raw === undefined && fallback !== undefined) return fallback
    if (typeof raw !== 'string' || raw.trim() === '' || codePoints(raw.trim()) > 80) throw new HttpError(400, 'invalid', '名称需要 1 到 80 个字符')
    return raw.trim()
  }
  // The inbox is a folder path that is never empty.
  const checkInbox = (raw: unknown): string => {
    if (typeof raw !== 'string' || raw === '') throw new HttpError(400, 'invalid', '收件箱需要一个文件夹路径')
    const bad = folderRuleMessage(raw)
    if (bad) throw new HttpError(400, 'invalid', bad)
    return raw
  }
  const usernameTaken = (name: string) => [...users.values()].some((u) => u.username === name)

  // --- sign in ---
  const createFirstAdmin = (body: Record<string, unknown>): MockResponse => {
    if (users.size > 0) throw new HttpError(409, 'exists', '已经有用户了')
    if (typeof body.token !== 'string' || body.token !== setupToken) throw new HttpError(403, 'forbidden', '设置令牌不对')
    const username = checkUsername(body.username)
    const password = checkPassword(body.password)
    const name = body.name === undefined || body.name === '' ? username : checkName(body.name)
    const u = addUser({ username, name, role: 'admin', password }, false)
    return { status: 201, body: { user: publicUser(u) }, headers: { 'Set-Cookie': startSession(u) } }
  }

  const login = (body: Record<string, unknown>): MockResponse => {
    const username = typeof body.username === 'string' ? body.username.toLowerCase() : ''
    // Five failures in a minute against one name lock it for the rest of the minute.
    const rec = failures.get(username)
    if (rec && now() - rec.first >= 60_000) failures.delete(username)
    const live = failures.get(username)
    if (live && live.count >= 5) {
      const wait = Math.max(1, Math.ceil((live.first + 60_000 - now()) / 1000))
      throw new HttpError(429, 'rate_limited', '尝试次数太多，请稍后再试', { __retryAfter: wait })
    }
    const u = [...users.values()].find((x) => x.username === username)
    const ok = !!u && !u.disabled && u.passwordHash !== undefined && typeof body.password === 'string' && u.passwordHash === hashOf(body.password)
    if (!ok) {
      failures.set(username, { count: (live?.count ?? 0) + 1, first: live?.first ?? now() })
      throw new HttpError(401, 'unauthenticated', '用户名或密码不对')
    }
    failures.delete(username)
    return { status: 200, body: { user: publicUser(u) }, headers: { 'Set-Cookie': startSession(u) } }
  }

  const checkInviteToken = (token: unknown): InviteRec => {
    const h = typeof token === 'string' ? hashOf(token) : ''
    const inv = [...invites.values()].find((i) => i.tokenHash === h)
    if (!inv || inv.used || inv.expires <= now()) throw new HttpError(404, 'not_found', '邀请不存在或已失效')
    return inv
  }

  const acceptInvite = (body: Record<string, unknown>): MockResponse => {
    const inv = checkInviteToken(body.token)
    const username = checkUsername(body.username)
    const password = checkPassword(body.password)
    const name = body.name === undefined || body.name === '' ? username : checkName(body.name)
    if (usernameTaken(username)) throw new HttpError(409, 'exists', '这个用户名已经有人用了')
    const u = addUser({ username, name, role: inv.role, password }, false)
    inv.used = true
    return { status: 201, body: { user: publicUser(u) }, headers: { 'Set-Cookie': startSession(u) } }
  }

  // --- single sign-on (the provider is a fake that signs in one person) ---
  const oidcStart = (ctx: Ctx, body: Record<string, unknown>): MockResponse => {
    if (!oidc) throw new HttpError(404, 'not_found', '没有配置单点登录')
    const intent = body.intent === undefined || body.intent === '' ? 'login' : body.intent
    if (typeof intent !== 'string' || !['login', 'setup', 'invite', 'link'].includes(intent)) throw new HttpError(400, 'invalid', 'intent 不对')
    let userId: string | null = null
    if (intent === 'link') userId = requireUser(ctx).id
    const state = randomString(24)
    oidcAttempts.set(state, { intent, token: typeof body.token === 'string' ? body.token : '', ret: plainPath(body.return), userId, created: now(), used: false })
    const url = `/api/auth/oidc/callback?state=${state}&sub=${encodeURIComponent(oidc.sub ?? 'mock-sub-1')}`
    return { status: 200, body: { url }, headers: { 'Set-Cookie': setCookie(OIDC_COOKIE, state, 600) } }
  }

  const uniqueUsername = (wanted: string) => {
    let base = wanted.toLowerCase().replace(/[^a-z0-9._-]/g, '').replace(/^[^a-z0-9]+/, '')
    if (base.length < 2) base = (base + 'user').slice(0, 32)
    base = base.slice(0, 28)
    let name = base
    for (let n = 2; usernameTaken(name); n++) name = `${base}${n}`
    return name
  }

  const oidcCallback = (ctx: Ctx, q: URLSearchParams): MockResponse => {
    const state = q.get('state') ?? ''
    const attempt = oidcAttempts.get(state)
    const redirect = (to: string, session?: string): MockResponse => ({ status: 303, headers: { Location: to, ...(session ? { 'Set-Cookie': session } : {}) } })
    // Nothing changes unless this browser started the attempt, it is unused and young.
    if (!oidc || !attempt || attempt.used || ctx.cookies[OIDC_COOKIE] !== state || now() - attempt.created > 600_000) return redirect('/login?error=expired')
    attempt.used = true
    const fail = (code: string) => redirect(attempt.intent === 'link' ? `/settings?error=${code}` : `/login?error=${code}`)
    if (q.get('error')) return fail('idp_error')
    const sub = q.get('sub') ?? ''
    if (!sub) return fail('idp_error')
    const holder = [...users.values()].find((u) => u.identities.some((i) => i.sub === sub))
    const claims = { label: 'mock@example.com', username: 'mockuser', name: 'Mock User' }

    switch (attempt.intent) {
      case 'login': {
        if (holder) return holder.disabled ? fail('disabled') : redirect(attempt.ret, startSession(holder))
        if (!oidc.autoCreate) return fail('no_account')
        const u = addUser({ username: uniqueUsername(claims.username), name: claims.name, role: 'member', identity: { sub, label: claims.label } }, false)
        return redirect(attempt.ret, startSession(u))
      }
      case 'setup': {
        if (users.size > 0 || attempt.token !== setupToken) return fail('setup_invalid')
        const u = addUser({ username: uniqueUsername(claims.username), name: claims.name, role: 'admin', identity: { sub, label: claims.label } }, false)
        return redirect(attempt.ret, startSession(u))
      }
      case 'invite': {
        let inv: InviteRec
        try {
          inv = checkInviteToken(attempt.token)
        } catch {
          return fail('invite_invalid')
        }
        if (holder) return fail('identity_in_use')
        const u = addUser({ username: uniqueUsername(claims.username), name: claims.name, role: inv.role, identity: { sub, label: claims.label } }, false)
        inv.used = true
        return redirect(attempt.ret, startSession(u))
      }
      default: {
        // link: the session must still be the one that started it.
        const me = ctx.user
        if (!me || me.id !== attempt.userId) return fail('expired')
        if (holder && holder.id !== me.id) return fail('identity_in_use')
        if (!holder) me.identities.push({ id: newId(() => false), provider: oidc.name, label: claims.label, created: rfc3339(now()), sub })
        return redirect(attempt.ret)
      }
    }
  }

  // --- account ---
  const accountInfo = (u: UserRec) => ({
    user: publicUser(u),
    hasPassword: u.passwordHash !== undefined,
    identities: u.identities.map(({ sub: _sub, ...rest }) => {
      void _sub
      return rest
    }),
  })

  const patchAccount = (u: UserRec, body: Record<string, unknown>) => {
    if (body.name !== undefined) u.name = checkName(body.name)
    if (body.email !== undefined) {
      if (typeof body.email !== 'string' || body.email.length > 254) throw new HttpError(400, 'invalid', '邮箱不合法')
      u.email = body.email.trim()
    }
    return { user: publicUser(u) }
  }

  const changePassword = (ctx: Ctx, u: UserRec, body: Record<string, unknown>) => {
    const next = checkPassword(body.new, '新密码')
    if (u.passwordHash !== undefined) {
      if (typeof body.current !== 'string') throw new HttpError(400, 'invalid', '缺少当前密码')
      if (hashOf(body.current) !== u.passwordHash) throw new HttpError(403, 'forbidden', '当前密码不对')
    } else if (body.current !== undefined) {
      throw new HttpError(400, 'invalid', '这个账号还没有密码，不需要当前密码')
    }
    u.passwordHash = hashOf(next)
    // Other sessions end; this one stays.
    endSessions(u.id, ctx.token)
  }

  const removeIdentity = (u: UserRec, id: string) => {
    const i = u.identities.findIndex((x) => x.id === id)
    if (i < 0) throw new HttpError(404, 'not_found', '没有这个登录方式')
    if (u.passwordHash === undefined && u.identities.length <= 1) throw new HttpError(409, 'conflict', '这是你唯一的登录方式，移除后就无法登录了')
    u.identities.splice(i, 1)
  }

  // --- users and invites ---
  const patchUser = (me: UserRec, id: string, body: Record<string, unknown>) => {
    const u = users.get(id)
    if (!u) throw new HttpError(404, 'not_found', '没有这个用户')
    if (body.role !== undefined && body.role !== 'admin' && body.role !== 'member') throw new HttpError(400, 'invalid', 'role 不对')
    if (body.disabled !== undefined && typeof body.disabled !== 'boolean') throw new HttpError(400, 'invalid', 'disabled 要是布尔值')
    const role = (body.role as Role | undefined) ?? u.role
    const disabled = (body.disabled as boolean | undefined) ?? u.disabled
    if (disabled && !u.disabled && u.id === me.id) throw new HttpError(409, 'conflict', '不能停用自己')
    const enabledAdmins = (list: UserRec[]) => list.filter((x) => x.role === 'admin' && !x.disabled).length
    const after = [...users.values()].map((x) => (x.id === u.id ? { ...x, role, disabled } : x)) as UserRec[]
    if (enabledAdmins(after) === 0) throw new HttpError(409, 'conflict', '至少要留一个可用的管理员')
    u.role = role
    u.disabled = disabled
    if (disabled) endSessions(u.id)
    return { user: publicUser(u) }
  }

  const createInvite = (me: UserRec, body: Record<string, unknown>): MockResponse => {
    const role = body.role === undefined ? 'member' : body.role
    if (role !== 'admin' && role !== 'member') throw new HttpError(400, 'invalid', 'role 不对')
    const days = body.days === undefined ? 7 : body.days
    if (typeof days !== 'number' || !Number.isInteger(days) || days < 1 || days > 30) throw new HttpError(400, 'invalid', 'days 要在 1 到 30 之间')
    const id = newId((x) => invites.has(x))
    const token = randomString(32)
    const rec: InviteRec = { id, tokenHash: hashOf(token), role, created: now(), expires: now() + days * 86_400_000, createdBy: me.id, used: false }
    invites.set(id, rec)
    return { status: 201, body: { id, token, role, created: rfc3339(rec.created), expires: rfc3339(rec.expires) } }
  }
  const openInvites = (): Invite[] =>
    [...invites.values()]
      .filter((i) => !i.used && i.expires > now())
      .map((i) => ({ id: i.id, role: i.role, created: rfc3339(i.created), expires: rfc3339(i.expires), createdBy: i.createdBy }))

  // --- spaces ---
  // Opening a space (the real server's `handle`): someone who is no member, and
  // no administrator, is told it does not exist, the same as for a space that
  // does not; a disabled space opens for no one; a member without the
  // permission the route needs is refused.
  const openSpace = (u: UserRec, id: string, need: Permission): SpaceRec => {
    const s = spaces.get(id)
    if (!s || (!s.members.has(u.id) && u.role !== 'admin')) throw new HttpError(404, 'not_found', '没有这个空间')
    if (s.disabled) throw new HttpError(403, 'space_disabled', 'this space has been disabled by an administrator')
    if (!spaceInfo(s, u).permissions.includes(need)) throw new HttpError(403, 'no_permission', NO_PERMISSION(need))
    return s
  }
  // What a route on a space asks of the caller (the route table of the real server).
  const needOf = (me: UserRec, method: string, rest: string): Permission => {
    if ((rest === '' && method === 'PATCH') || rest === '/reindex' || rest === '/remote' || rest.startsWith('/remote/')) return 'manage'
    if (rest === '/members' || rest.startsWith('/members/')) {
      if (method === 'GET') return 'read'
      // Anyone may leave: removing yourself needs no more than being a member.
      if (method === 'DELETE' && decodeURIComponent(rest.slice('/members/'.length)) === me.id) return 'read'
      return 'members'
    }
    return method === 'GET' ? 'read' : 'write'
  }

  // --- members ---
  const memberOf = (s: SpaceRec, userId: string, you: string): Member => {
    const u = users.get(userId)!
    return { userId, username: u.username, name: u.name, role: s.members.get(userId)!.role, you: userId === you }
  }
  const checkRole = (raw: unknown): SpaceRole => {
    if (typeof raw !== 'string' || !ROLES.includes(raw)) throw new HttpError(400, 'invalid', `a role is one of ${ROLES.join(', ')}`)
    return raw as SpaceRole
  }
  // A space always keeps one owner.
  const keepOneOwner = (s: SpaceRec, leaving: SpaceRole) => {
    if (leaving === 'owner' && [...s.members.values()].filter((m) => m.role === 'owner').length <= 1) throw new HttpError(409, 'last_owner', 'a space needs at least one owner; make someone else an owner first')
  }
  const routeMembers = (me: UserRec, s: SpaceRec, method: string, rest: string, body: Record<string, unknown>): MockResponse => {
    const j = (b: unknown, status = 200): MockResponse => ({ status, body: b })
    if (rest === '/members' && method === 'GET') {
      const list = [...s.members.keys()].map((id) => memberOf(s, id, me.id))
      list.sort((a, b) => Number(b.role === 'owner') - Number(a.role === 'owner') || compareCodePoints(a.name, b.name))
      return j({ members: list })
    }
    if (rest === '/members' && method === 'POST') {
      const role = checkRole(body.role)
      if (s.personal) throw new HttpError(409, 'personal_space', 'a personal space belongs to one person and cannot be shared')
      const name = typeof body.username === 'string' ? body.username.trim().toLowerCase() : ''
      const u = [...users.values()].find((x) => x.username === name)
      if (!u || u.disabled) throw new HttpError(404, 'user_not_found', 'there is no such user')
      if (s.members.has(u.id)) throw new HttpError(409, 'exists', 'that user is a member of this space already')
      s.members.set(u.id, { role, joined: ++counter })
      return j(memberOf(s, u.id, me.id), 201)
    }
    const one = /^\/members\/([^/]+)$/.exec(rest)
    if (one) {
      const userId = decodeURIComponent(one[1])
      if (method === 'PATCH') {
        const role = checkRole(body.role)
        const cur = s.members.get(userId)
        if (!cur) throw new HttpError(404, 'not_found', 'that user is not a member of this space')
        if (cur.role !== role) {
          keepOneOwner(s, cur.role)
          cur.role = role
        }
        return j(memberOf(s, userId, me.id))
      }
      if (method === 'DELETE') {
        const cur = s.members.get(userId)
        if (!cur) throw new HttpError(404, 'not_found', 'that user is not a member of this space')
        keepOneOwner(s, cur.role)
        s.members.delete(userId)
        return { status: 204 }
      }
    }
    throw new HttpError(404, 'not_found', '没有这个接口')
  }

  // --- administration ---
  const spaceSync = (s: SpaceRec) => (s.remote ? s.remote.state : 'local')
  const adminSpaceOf = (id: string): SpaceRec => {
    const s = spaces.get(id)
    if (!s) throw new HttpError(404, 'not_found', 'no such space')
    return s
  }
  const adminItem = (s: SpaceRec): AdminSpace => {
    const owner = ownerOf(s)
    return {
      id: s.id,
      name: s.name,
      personal: s.personal,
      inbox: s.inbox,
      created: rfc3339(s.created),
      disabled: s.disabled,
      owner: owner ? { id: owner.id, username: owner.username, name: owner.name } : null,
      members: s.members.size,
      sizeBytes: s.size ? s.size.bytes : null,
      sizeAt: s.size ? rfc3339(s.size.at) : null,
      sync: spaceSync(s),
    }
  }
  const sizeOf = (s: SpaceRec) => {
    // The files of the pages, plus what a repository weighs on its own.
    let bytes = 4096
    for (const id of s.store.ids()) {
      const p = s.store.peek(id)
      if (p) bytes += new TextEncoder().encode(JSON.stringify(p.meta) + p.body).length
    }
    return bytes
  }
  const routeAdmin = (method: string, path: string, url: URL, body: Record<string, unknown>): MockResponse => {
    const j = (b: unknown, status = 200): MockResponse => ({ status, body: b })
    if (path === '/admin/spaces' && method === 'GET') {
      let limit = 50
      const raw = url.searchParams.get('limit')
      if (raw !== null && raw !== '') {
        const n = Number(raw)
        if (!Number.isInteger(n) || n < 1 || n > 200) throw new HttpError(400, 'invalid', 'limit is a number from 1 to 200')
        limit = n
      }
      const needle = (url.searchParams.get('q') ?? '').toLowerCase()
      const after = url.searchParams.get('cursor') ?? ''
      // In order of creation, as the real server's id order is.
      const all = [...spaces.values()]
      const from = after ? all.findIndex((s) => s.id === after) + 1 : 0
      const matching = all.slice(from).filter((s) => needle === '' || s.name.toLowerCase().includes(needle))
      const page = matching.slice(0, limit)
      const items = page.map(adminItem)
      return j({ items, next: matching.length > limit ? page[page.length - 1].id : '' })
    }
    if (path === '/admin/status' && method === 'GET') {
      const status: ServerStatus = {
        version: 'mock',
        startedAt: rfc3339(startedAt),
        uptimeSeconds: Math.max(0, Math.floor((now() - startedAt) / 1000)),
        database: 'sqlite',
        spaces: spaces.size,
        users: users.size,
        openSpaces: spaces.size,
        diskFreeBytes: 50 * 1024 ** 3,
        goVersion: 'go1.27.4',
      }
      return j(status)
    }
    const one = /^\/admin\/spaces\/([^/]+)(?:\/(size|transfer|disable|enable))?$/.exec(path)
    if (one) {
      const id = decodeURIComponent(one[1])
      const s = adminSpaceOf(id)
      switch (one[2]) {
        case 'size':
          if (method !== 'POST') break
          s.size = { bytes: sizeOf(s), at: now() }
          return j({ sizeBytes: s.size.bytes, sizeAt: rfc3339(s.size.at) })
        case 'transfer': {
          if (method !== 'POST') break
          if (s.personal) throw new HttpError(409, 'personal_space', 'a personal space belongs to one person and cannot be transferred or deleted')
          const u = typeof body.userId === 'string' ? users.get(body.userId) : undefined
          if (!u) throw new HttpError(404, 'user_not_found', 'there is no such user')
          if (u.disabled) throw new HttpError(409, 'user_disabled', 'that user is disabled and cannot own a space')
          for (const m of s.members.values()) if (m.role === 'owner') m.role = 'editor'
          const had = s.members.get(u.id)
          if (had) had.role = 'owner'
          else s.members.set(u.id, { role: 'owner', joined: ++counter })
          return { status: 204 }
        }
        case 'disable':
        case 'enable':
          if (method !== 'POST') break
          s.disabled = one[2] === 'disable'
          return { status: 204 }
        case undefined:
          if (method === 'GET') return j(adminItem(s))
          if (method !== 'DELETE') break
          if (s.personal) throw new HttpError(409, 'personal_space', 'a personal space belongs to one person and cannot be transferred or deleted')
          spaces.delete(id)
          {
            const stamp = rfc3339(now()).replace(/[-:]/g, '')
            return j({ trashed: `trash/${id}-${stamp}`, cacheTrashed: `trash/${id}-${stamp}.cache`, warnings: [] })
          }
      }
    }
    throw new HttpError(404, 'not_found', '没有这个接口')
  }

  // --- remote repository ---
  const hosts = options.remote?.hosts ?? DEFAULT_HOSTS
  const hostAllowed = (host: string) => hosts.includes('*') || hosts.includes(host.toLowerCase())
  const B64 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789'
  const b64 = (n: number) => Array.from({ length: n }, () => B64[Math.floor(random() * 64)]).join('')
  const fakeCommit = () => gitBlobId(`remote ${++counter} ${random()}`)
  const hostKeyOf = (host: string) => `SHA256:${btoa(gitBlobId(host)).replace(/=+$/, '').slice(0, 43)}`

  const makeKey = (space: SpaceRec): RemoteKey => ({ publicKey: `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI${b64(43)} petalbook-${space.id}`, fingerprint: `SHA256:${b64(43)}` })

  const remoteInfo = (s: SpaceRec): Remote => {
    const r = s.remote!
    return {
      url: r.url,
      branch: r.branch,
      credential: r.credential.type === 'ssh' ? { type: 'ssh', publicKey: s.key!.publicKey, fingerprint: s.key!.fingerprint } : { type: 'token', username: r.credential.username },
      state: r.state,
      ...(r.lastSync !== undefined ? { lastSync: rfc3339(r.lastSync) } : {}),
      error: r.error,
      ...(r.hostKey ? { hostKey: r.hostKey } : {}),
      rewritten: r.rewritten,
    }
  }

  // Mock only (also behind `/api/_mock/remote`): put the space's remote in a
  // state, making it up if the space was local.
  const setRemoteState = (space: SpaceRec, state: RemoteState | 'local', patch: { error?: string } = {}) => {
    if (state === 'local') {
      space.remote = null
      space.reachable = true
      return
    }
    space.key ??= makeKey(space)
    space.remote ??= { url: 'git@github.com:mock/notes.git', branch: 'main', credential: { type: 'ssh' }, state: 'ok', lastSync: now(), error: '', hostKey: hostKeyOf('github.com'), rewritten: null }
    const r = space.remote
    r.state = state
    space.reachable = state !== 'offline'
    r.error = patch.error ?? (state === 'offline' ? '连接超时' : state === 'error' ? '远端拒绝了这个凭据，请检查令牌或部署密钥' : '')
    r.rewritten = state === 'rewritten' ? { lastGood: fakeCommit(), remote: fakeCommit() } : null
    if (state === 'ok') r.lastSync = now()
  }

  // `https://host/path` or an SSH address, with no password in it.
  const parseRemoteUrl = (raw: unknown): { kind: 'https' | 'ssh'; host: string } => {
    const bad = () => new HttpError(400, 'invalid', '只接受 https://主机/路径 或 SSH 地址（git@主机:路径），地址里不能带密码')
    if (typeof raw !== 'string' || raw.trim() === '' || /\s/.test(raw.trim())) throw bad()
    const url = raw.trim()
    let m: RegExpExecArray | null
    if (/^https:\/\//i.test(url)) {
      let u: URL
      try {
        u = new URL(url)
      } catch {
        throw bad()
      }
      if (u.username || u.password || !u.hostname || u.pathname.length <= 1) throw bad()
      return { kind: 'https', host: u.hostname }
    }
    if ((m = /^[\w.-]+@([\w.-]+):(?!\/)\S+$/.exec(url))) return { kind: 'ssh', host: m[1] }
    if ((m = /^ssh:\/\/(?:[\w.-]+@)?([\w.-]+)(?::\d+)?\/\S+$/.exec(url))) return { kind: 'ssh', host: m[1] }
    throw bad()
  }

  const connectRemote = (space: SpaceRec, body: Record<string, unknown>): Remote => {
    const input = body as unknown as Partial<ConnectRemoteInput>
    const { kind, host } = parseRemoteUrl(input.url)
    if (!hostAllowed(host)) throw new HttpError(403, 'forbidden', `管理员没有允许连接 ${host}。需要在服务器启动时用 --remote-host 加上它。`)
    const branch = input.branch === undefined || input.branch === '' ? 'main' : input.branch
    if (typeof branch !== 'string' || /\s/.test(branch)) throw new HttpError(400, 'invalid', '分支名不合法')
    const cred = input.credential
    if (!cred || (cred.type !== 'ssh' && cred.type !== 'token')) throw new HttpError(400, 'invalid', '缺少 credential')
    if ((cred.type === 'ssh') !== (kind === 'ssh')) throw new HttpError(400, 'invalid', cred.type === 'ssh' ? 'SSH 密钥要配 SSH 地址' : '令牌要配 https 地址')
    let credential: RemoteRec['credential']
    if (cred.type === 'ssh') {
      space.key ??= makeKey(space)
      credential = { type: 'ssh' }
    } else {
      const kept = space.remote?.credential.type === 'token' ? space.remote.credential : null
      const token = typeof cred.token === 'string' && cred.token !== '' ? cred.token : kept?.token
      if (!token) throw new HttpError(400, 'invalid', '缺少令牌')
      const username = typeof cred.username === 'string' && cred.username.trim() !== '' ? cred.username.trim() : (kept?.username ?? 'x-access-token')
      credential = { type: 'token', username, token }
    }

    // What the pretend remote is like.
    const word = /unreachable|refused|diverged|behind|ahead|empty/.exec(String(input.url))?.[0]
    const found = word === 'empty' ? 'no-branch' : ((word as NonNullable<MockRemoteOptions['find']> | undefined) ?? options.remote?.find ?? 'no-branch')
    if (found === 'unreachable') throw new HttpError(503, 'unavailable', `连不上 ${host}：连接超时。什么也没有改。`)
    if (found === 'refused') throw new HttpError(503, 'unavailable', `${host} 拒绝了这个凭据（认证失败）。什么也没有改。`)
    if (found !== 'no-branch') {
      if (space.store.pristine()) {
        // A space nothing was ever made in takes the remote's content and history.
        space.store.adopt([
          { folder: '', title: '来自远端的页面', body: '这个页面原本就在远端仓库里。\n', tags: ['远端'] },
          { folder: '笔记', title: '远端的笔记', body: '远端仓库里的第二个页面。\n' },
        ])
      } else if (found === 'diverged') {
        throw new HttpError(409, 'conflict', '这个空间和远端仓库各自都有内容，两边的历史互不包含。PetalBook 不会合并两份历史，请为这个仓库新建一个空间。')
      } else if (found === 'ahead') {
        space.store.adopt([{ folder: '', title: '远端新增的页面', body: '这个页面是在远端新增的。\n' }])
      }
    }
    space.remote = { url: String(input.url).trim(), branch, credential, state: 'ok', lastSync: now(), error: '', hostKey: kind === 'ssh' ? hostKeyOf(host) : undefined, rewritten: null }
    space.reachable = true
    return remoteInfo(space)
  }

  const routeRemote = (space: SpaceRec, method: string, rest: string, body: Record<string, unknown>): MockResponse => {
    // Accounts mode only, like the other space-management endpoints.
    accountsOnly()
    const j = (b: unknown, status = 200): MockResponse => ({ status, body: b })
    const following = () => {
      if (!space.remote) throw new HttpError(404, 'not_found', '这个空间没有连接远端仓库')
      return space.remote
    }
    if (rest === '/remote') {
      if (method === 'GET') {
        following()
        return j(remoteInfo(space))
      }
      if (method === 'PUT') return j(connectRemote(space, body))
      if (method === 'DELETE') {
        following()
        space.remote = null
        space.reachable = true
        return { status: 204 }
      }
    }
    if (rest === '/remote/key' && method === 'POST') {
      if (body.rotate === true) {
        if (space.remote) throw new HttpError(409, 'conflict', '空间已经连着远端，不能更换密钥。先断开连接。')
        space.key = makeKey(space)
      } else space.key ??= makeKey(space)
      return j({ publicKey: space.key.publicKey, fingerprint: space.key.fingerprint })
    }
    if (rest === '/remote/sync' && method === 'POST') {
      const r = following()
      if (!space.reachable) {
        if (r.state !== 'rewritten') {
          r.state = 'offline'
          r.error ||= '连接超时'
        }
      } else {
        r.lastSync = now()
        if (r.state === 'offline') {
          r.state = 'ok'
          r.error = ''
        }
      }
      return j(remoteInfo(space))
    }
    if (rest === '/remote/accept' && method === 'POST') {
      const r = following()
      if (typeof body.commit !== 'string' || body.commit === '') throw new HttpError(400, 'invalid', '缺少 commit')
      if (r.state !== 'rewritten' || !r.rewritten || r.rewritten.remote !== body.commit) throw new HttpError(409, 'conflict', '远端已经不在这个提交上了，请刷新后再看')
      r.state = 'ok'
      r.rewritten = null
      r.error = ''
      r.lastSync = now()
      return j(remoteInfo(space))
    }
    throw new HttpError(404, 'not_found', '没有这个接口')
  }

  const routeSpace = (u: UserRec, space: SpaceRec, method: string, rest: string, url: URL, body: Record<string, unknown>, raw?: Uint8Array): MockResponse => {
    const st = space.store
    if (rest === '/pages') {
      if (method === 'GET') return { status: 200, body: st.listPages(url.searchParams) }
      if (method === 'POST') return { status: 201, body: st.create(body) }
    }
    if (rest === '/pages/batch' && method === 'POST') return { status: 200, body: st.batch(body) }
    const hist = /^\/pages\/([^/]+)\/history$/.exec(rest)
    if (hist && method === 'GET') return { status: 200, body: st.history(decodeURIComponent(hist[1]), url.searchParams) }
    const ver = /^\/pages\/([^/]+)\/versions\/([^/]+)$/.exec(rest)
    if (ver && method === 'GET') return { status: 200, body: st.version(decodeURIComponent(ver[1]), decodeURIComponent(ver[2])) }
    if (rest === '/remote' || rest.startsWith('/remote/')) return routeRemote(space, method, rest, body)
    const one = /^\/pages\/([^/]+)(\/(move|draft|commit))?$/.exec(rest)
    if (one) {
      const id = decodeURIComponent(one[1])
      switch (one[3]) {
        case undefined:
          if (method === 'GET') return { status: 200, body: st.getPage(u.id, id) }
          if (method === 'PUT') return { status: 200, body: st.save(u.id, id, body) }
          if (method === 'DELETE') {
            st.remove(id)
            return { status: 204 }
          }
          break
        case 'move':
          if (method === 'POST') return { status: 200, body: st.move(id, body) }
          break
        case 'draft':
          if (method === 'PUT') return { status: 200, body: st.putDraft(u.id, id, body) }
          if (method === 'DELETE') {
            st.deleteDraft(u.id, id, url.searchParams.get('version'), url.searchParams.get('rev'))
            return { status: 204 }
          }
          break
        case 'commit':
          if (method === 'POST') return { status: 200, body: st.commit(u.id, id, body) }
          break
      }
    }
    // Files: uploading one for a page, and reading any file of the space.
    const asset = /^\/pages\/([^/]+)\/assets$/.exec(rest)
    if (asset && method === 'POST') {
      const id = decodeURIComponent(asset[1])
      if (!st.peek(id)) throw new HttpError(404, 'not_found', '找不到这个页面')
      const why = blockedReason(space)
      if (why) throw new HttpError(503, 'unavailable', why)
      const name = url.searchParams.get('name') ?? ''
      if (name.trim() === '' || !(name.split(/[\\/]/).pop() ?? '').trim()) throw new HttpError(400, 'invalid', '缺少文件名')
      if (!raw || raw.length === 0) throw new HttpError(400, 'invalid', '文件是空的')
      if (raw.length > maxUpload) throw new HttpError(413, 'invalid', `文件太大，上限是 ${maxUpload} 字节`)
      const r = space.files.upload(id, name, raw)
      return { status: r.created ? 201 : 200, body: r.asset }
    }
    if (rest === '/import' && method === 'POST') {
      const why = blockedReason(space)
      if (why) throw new HttpError(503, 'unavailable', why)
      const folder = url.searchParams.get('folder') ?? ''
      if (!validFolder(folder)) throw new HttpError(400, 'invalid', folderRuleMessage(folder) ?? '文件夹路径不合法')
      if (!raw || raw.length === 0) throw new HttpError(400, 'invalid', '文件是空的')
      if (raw.length > maxImport) throw new HttpError(413, 'invalid', `文件太大，上限是 ${maxImport} 字节`)
      return { status: 200, body: runImport(st.create, raw, folder, url.searchParams.get('name') ?? '') }
    }
    if (rest.startsWith('/files/') && method === 'GET') {
      let segments: string[]
      try {
        segments = rest.slice('/files/'.length).split('/').map(decodeURIComponent)
      } catch {
        throw new HttpError(404, 'not_found', '没有这个文件')
      }
      const f = space.files.read(segments)
      if (!f) throw new HttpError(404, 'not_found', '没有这个文件')
      return { status: 200, bytes: f.bytes, headers: f.headers }
    }
    if (rest === '/tree' && method === 'GET') return { status: 200, body: st.tree() }
    if (rest === '/tags' && method === 'GET') return { status: 200, body: st.tags() }
    const tagOne = /^\/tags\/(.+)$/.exec(rest)
    if (tagOne && method === 'PUT') {
      let tag: string
      try {
        tag = decodeURIComponent(tagOne[1])
      } catch {
        throw new HttpError(400, 'invalid', '标签不合法')
      }
      return { status: 200, body: st.setTagColor(tag, body) }
    }
    if (rest === '/folders' && method === 'POST') return { status: 201, body: st.createFolder(body.path) }
    if (rest === '/folders/move' && method === 'POST') return { status: 200, body: st.moveFolder(body) }
    if (rest === '/folders' && method === 'DELETE') {
      st.removeFolder(url.searchParams.get('path'), url.searchParams.get('recursive') === 'true')
      return { status: 204 }
    }
    if (rest === '/reindex' && method === 'POST') return { status: 200, body: st.reindex() }
    throw new HttpError(404, 'not_found', '没有这个接口')
  }

  const route = (method: string, url: URL, body: Record<string, unknown>, ctx: Ctx, raw?: Uint8Array): MockResponse => {
    const path = url.pathname.replace(/^\/api/, '')
    const j = (b: unknown, status = 200): MockResponse => ({ status, body: b })

    if (method === 'GET' && path === '/meta') {
      const u = ctx.user
      return j({ version: 'mock', maxUpload, maxImport, spaces: u ? callerSpaces(u).map((s) => spaceInfo(s, u)) : [] })
    }
    if (method === 'GET' && path === '/session') {
      const base = { mode, user: ctx.user ? publicUser(ctx.user) : null, setup: accounts && users.size === 0 }
      return {
        status: 200,
        // Never cached: it says who is signed in.
        headers: { 'Cache-Control': 'no-store' },
        body: accounts ? { ...base, methods: { password: true, oidc: oidc ? { name: oidc.name } : null } } : base,
      }
    }

    // Mock only: change a page behind the app's back, as another editor or a
    // `git pull` would, to exercise the conflict path by hand.
    const touch = /^\/_mock\/touch\/([^/]+)$/.exec(path)
    if (method === 'POST' && touch) {
      const target = [...spaces.values()].find((s) => s.id === (url.searchParams.get('space') ?? '') || (!url.searchParams.get('space') && s.store.peek(touch[1])))
      if (!target) throw new HttpError(404, 'not_found', '没有这个页面')
      return j(target.store.touch(touch[1], body))
    }

    // Mock only: put a space's remote in a state, to see how the app shows it.
    if (method === 'POST' && path === '/_mock/remote') {
      const target = spaces.get(url.searchParams.get('space') ?? '') ?? [...spaces.values()][0]
      if (!target) throw new HttpError(404, 'not_found', '没有这个空间')
      const state = body.state
      if (state !== 'local' && state !== 'ok' && state !== 'offline' && state !== 'rewritten' && state !== 'error') throw new HttpError(400, 'invalid', 'state 不对')
      setRemoteState(target, state, { error: typeof body.error === 'string' ? body.error : undefined })
      return j(spaceInfo(target, ownerOf(target) ?? [...users.values()][0]))
    }

    // --- signing in (accounts mode) ---
    if (path === '/setup' && method === 'POST') {
      accountsOnly()
      return createFirstAdmin(body)
    }
    if (path === '/login' && method === 'POST') {
      accountsOnly()
      return login(body)
    }
    if (path === '/logout' && method === 'POST') {
      accountsOnly()
      if (ctx.token) sessions.delete(ctx.token)
      return { status: 204, headers: { 'Set-Cookie': setCookie(SESSION_COOKIE, '', 0) } }
    }
    if (path === '/auth/oidc/start' && method === 'POST') {
      accountsOnly()
      return oidcStart(ctx, body)
    }
    if (path === '/auth/oidc/callback' && method === 'GET') {
      accountsOnly()
      return oidcCallback(ctx, url.searchParams)
    }
    if (path === '/invites/check' && method === 'POST') {
      accountsOnly()
      const inv = checkInviteToken(body.token)
      return j({ role: inv.role, expires: rfc3339(inv.expires) })
    }
    if (path === '/invites/accept' && method === 'POST') {
      accountsOnly()
      return acceptInvite(body)
    }

    // --- account ---
    if (path === '/account' || path === '/account/password' || path.startsWith('/account/identities/')) {
      accountsOnly()
      const me = requireUser(ctx)
      if (path === '/account' && method === 'GET') return j(accountInfo(me))
      if (path === '/account' && method === 'PATCH') return j(patchAccount(me, body))
      if (path === '/account/password' && method === 'POST') {
        changePassword(ctx, me, body)
        return { status: 204 }
      }
      const ident = /^\/account\/identities\/([^/]+)$/.exec(path)
      if (ident && method === 'DELETE') {
        removeIdentity(me, decodeURIComponent(ident[1]))
        return { status: 204 }
      }
    }

    // --- users and invites ---
    if (path === '/users' || path.startsWith('/users/') || path === '/invites' || path.startsWith('/invites/')) {
      accountsOnly()
      const me = requireAdmin(ctx)
      if (path === '/users' && method === 'GET') return j({ users: [...users.values()].sort((a, b) => a.created.localeCompare(b.created)).map(publicUser) })
      const user = /^\/users\/([^/]+)$/.exec(path)
      if (user && method === 'PATCH') return j(patchUser(me, decodeURIComponent(user[1]), body))
      if (path === '/invites' && method === 'POST') return createInvite(me, body)
      if (path === '/invites' && method === 'GET') return j({ invites: openInvites() })
      const inv = /^\/invites\/([^/]+)$/.exec(path)
      if (inv && method === 'DELETE') {
        if (!invites.delete(decodeURIComponent(inv[1]))) throw new HttpError(404, 'not_found', '没有这个邀请')
        return { status: 204 }
      }
    }

    // --- administration (administrators only; accounts mode) ---
    if (path === '/admin/status' || path === '/admin/spaces' || path.startsWith('/admin/spaces/')) {
      accountsOnly()
      requireAdmin(ctx)
      return routeAdmin(method, path, url, body)
    }

    // --- spaces ---
    if (path === '/spaces') {
      if (method === 'GET') {
        const me = requireUser(ctx)
        return j({ spaces: callerSpaces(me).map((s) => spaceInfo(s, me)) })
      }
      if (method === 'POST') {
        accountsOnly()
        const me = requireUser(ctx)
        const name = checkName(body.name)
        if (options.maxSpaces !== undefined && callerSpaces(me).filter((s) => s.members.get(me.id)?.role === 'owner').length >= options.maxSpaces) throw new HttpError(403, 'forbidden', `最多只能拥有 ${options.maxSpaces} 个空间`)
        const inbox = body.inbox === undefined ? DEFAULT_INBOX : checkInbox(body.inbox)
        return j(spaceInfo(addSpace(newId((x) => spaces.has(x)), name, me.id, false, false, inbox), me), 201)
      }
    }
    const m = /^\/spaces\/([^/]+)(\/.*)?$/.exec(path)
    if (m) {
      const me = requireUser(ctx)
      const id = decodeURIComponent(m[1])
      const rest = m[2] ?? ''
      // These do not exist in single-user mode, whoever asks.
      if ((rest === '' && method === 'PATCH') || rest === '/remote' || rest.startsWith('/remote/') || rest === '/members' || rest.startsWith('/members/')) accountsOnly()
      const s = openSpace(me, id, needOf(me, method, rest))
      if (rest === '' && method === 'PATCH') {
        if (body.name === undefined && body.inbox === undefined) throw new HttpError(400, 'invalid', '至少要给出 name 或 inbox')
        // Check both before changing either, so a bad one leaves the space as it was.
        const name = body.name === undefined ? s.name : checkName(body.name)
        const inbox = body.inbox === undefined ? s.inbox : checkInbox(body.inbox)
        s.name = name
        s.inbox = inbox
        return j(spaceInfo(s, me))
      }
      if (rest === '/members' || rest.startsWith('/members/')) {
        // Leaving needs no more than being a member: the permission check above is waived for it.
        return routeMembers(me, s, method, rest, body)
      }
      return routeSpace(me, s, method, rest, url, body, raw)
    }
    throw new HttpError(404, 'not_found', '没有这个接口')
  }

  // The first space may start out following a remote, to see the states by hand.
  if (options.remote?.start) {
    const first = [...spaces.values()][0]
    if (first) setRemoteState(first, options.remote.start)
  }

  const sweepAll = () => spaces.forEach((s) => s.store.sweep(idleMs, maxMs))

  return {
    // `target` is a path with query, as sent by the client ("/api/...").
    // `cookie` is the request's Cookie header.
    // `raw` is the request body as bytes, for the one request that is not JSON (uploading a file).
    handle(method: string, target: string, bodyText?: string, contentType?: string | null, cookie?: string | null, raw?: Uint8Array): MockResponse {
      counter++
      try {
        sweepAll()
        const upload = method.toUpperCase() === 'POST' && /^\/api\/spaces\/[^/?]+\/(pages\/[^/?]+\/assets|import)(\?|$)/.test(target)
        // Writes must say they are JSON, like the real server insists; a file says it is bytes.
        if (upload) {
          if (!/^application\/octet-stream\s*(;|$)/i.test(contentType ?? '')) throw new HttpError(415, 'invalid', '上传文件和导入需要 Content-Type: application/octet-stream')
          return route('POST', new URL(target, 'http://mock.local'), {}, contextOf(cookie), raw ?? new TextEncoder().encode(bodyText ?? ''))
        }
        if (method.toUpperCase() !== 'GET' && !/^application\/json\s*(;|$)/i.test(contentType ?? '')) throw new HttpError(415, 'invalid', '请求需要 Content-Type: application/json')
        let body: Record<string, unknown> = {}
        if (bodyText) {
          try {
            const parsed: unknown = JSON.parse(bodyText)
            if (!isRecord(parsed)) throw new Error('not an object')
            body = parsed
          } catch {
            throw new HttpError(400, 'invalid', '请求体不是合法的 JSON')
          }
        }
        return route(method.toUpperCase(), new URL(target, 'http://mock.local'), body, contextOf(cookie))
      } catch (e) {
        if (e instanceof HttpError) {
          const { __retryAfter, ...extra } = e.extra ?? {}
          return {
            status: e.status,
            body: { error: e.code, message: e.message, ...extra },
            ...(typeof __retryAfter === 'number' ? { headers: { 'Retry-After': String(__retryAfter) } } : {}),
          }
        }
        throw e
      }
    },
    // Test helpers.
    get requestCount() {
      return counter
    },
    // The first space of the single-user server, or the one named.
    peek: (id: string, space?: string): Page | undefined => (space ? spaces.get(space) : [...spaces.values()].find((s) => s.store.peek(id)))?.store.peek(id),
    ids: (space = 'default') => spaces.get(space)?.store.ids() ?? [],
    spaceIds: () => [...spaces.keys()],
    peekDraft: (userId: string, id: string, space = 'default') => spaces.get(space)?.store.peekDraft(userId, id),
    userId: (username: string) => [...users.values()].find((u) => u.username === username)?.id,
    // Let the server's own draft commits run now (they also run on every request).
    sweep: sweepAll,
    // Put a space's remote in a state, as the dev route does.
    setRemote: (space: string, state: RemoteState | 'local', patch: { error?: string } = {}) => setRemoteState(spaces.get(space)!, state, patch),
    // Whether the pretend remote can be reached by `sync`.
    remoteReachable: (space: string, on: boolean) => {
      spaces.get(space)!.reachable = on
    },
    // The remote commit a `rewritten` space would accept.
    rewrittenCommit: (space: string) => spaces.get(space)?.remote?.rewritten?.remote,
    // The token as the mock keeps it (never returned by the API).
    storedToken: (space: string) => {
      const c = spaces.get(space)?.remote?.credential
      return c?.type === 'token' ? c.token : undefined
    },
  }
}

export type MockServer = ReturnType<typeof createMockServer>
