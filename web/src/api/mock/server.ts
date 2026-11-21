// An in-memory implementation of docs/api.md, for `npm run dev:mock` and for
// tests. Single-user mode is the default; accounts mode adds sessions (a
// cookie stand-in), invites, spaces per user and a fake OpenID Connect
// provider. It has no DOM or Node dependencies.

import type { Identity, Invite, Page, Role, SpaceInfo, User } from '../types.ts'
import { gitBlobId } from './sha1.ts'
import { ALPHABET, HttpError, compareCodePoints, createSpaceStore, isRecord, rfc3339, type SpaceStore } from './store.ts'
import { SPACE_NAME } from './seed.ts'

export { validFolder } from './store.ts'

export interface MockResponse {
  status: number
  body?: unknown
  // Response headers other than the content type: Set-Cookie, Location, Retry-After.
  headers?: Record<string, string>
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
  draftIdleMs?: number
  draftMaxMs?: number
}

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
interface SpaceRec {
  id: string
  name: string
  ownerId: string
  personal: boolean
  store: SpaceStore
}
interface OidcAttempt {
  intent: string
  token: string
  ret: string
  userId: string | null
  created: number
  used: boolean
}

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
  const oidc = accounts ? (options.oidc ?? null) : null
  let counter = 0

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
  const spaceInfo = (s: SpaceRec): SpaceInfo => ({ id: s.id, name: s.name, role: 'owner', personal: s.personal })

  const addSpace = (id: string, name: string, ownerId: string, personal: boolean, seed: boolean) => {
    const rec: SpaceRec = { id, name, ownerId, personal, store: createSpaceStore({ now, random, seed }) }
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
      .filter((s) => s.ownerId === u.id)
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
  const spaceOf = (u: UserRec, id: string): SpaceRec => {
    const s = spaces.get(id)
    // A space of someone else is the same 404 as one that does not exist.
    if (!s || s.ownerId !== u.id) throw new HttpError(404, 'not_found', '没有这个空间')
    return s
  }

  const routeSpace = (u: UserRec, space: SpaceRec, method: string, rest: string, url: URL, body: Record<string, unknown>): MockResponse => {
    const st = space.store
    if (rest === '/pages') {
      if (method === 'GET') return { status: 200, body: st.listPages(url.searchParams) }
      if (method === 'POST') return { status: 201, body: st.create(body) }
    }
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
    if (rest === '/tree' && method === 'GET') return { status: 200, body: st.tree() }
    if (rest === '/tags' && method === 'GET') return { status: 200, body: st.tags() }
    if (rest === '/folders' && method === 'POST') return { status: 201, body: st.createFolder(body.path) }
    if (rest === '/reindex' && method === 'POST') return { status: 200, body: st.reindex() }
    throw new HttpError(404, 'not_found', '没有这个接口')
  }

  const route = (method: string, url: URL, body: Record<string, unknown>, ctx: Ctx): MockResponse => {
    const path = url.pathname.replace(/^\/api/, '')
    const j = (b: unknown, status = 200): MockResponse => ({ status, body: b })

    if (method === 'GET' && path === '/meta') return j({ version: 'mock', spaces: callerSpaces(ctx.user).map(spaceInfo) })
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

    // --- spaces ---
    if (path === '/spaces') {
      if (method === 'GET') return j({ spaces: callerSpaces(requireUser(ctx)).map(spaceInfo) })
      if (method === 'POST') {
        accountsOnly()
        const me = requireUser(ctx)
        const name = checkName(body.name)
        if (options.maxSpaces !== undefined && callerSpaces(me).length >= options.maxSpaces) throw new HttpError(403, 'forbidden', `最多只能拥有 ${options.maxSpaces} 个空间`)
        return j(spaceInfo(addSpace(newId((x) => spaces.has(x)), name, me.id, false, false)), 201)
      }
    }
    const m = /^\/spaces\/([^/]+)(\/.*)?$/.exec(path)
    if (m) {
      const me = requireUser(ctx)
      const id = decodeURIComponent(m[1])
      const rest = m[2] ?? ''
      if (rest === '' && method === 'PATCH') {
        accountsOnly()
        const s = spaceOf(me, id)
        s.name = checkName(body.name)
        return j(spaceInfo(s))
      }
      return routeSpace(me, spaceOf(me, id), method, rest, url, body)
    }
    throw new HttpError(404, 'not_found', '没有这个接口')
  }

  const sweepAll = () => spaces.forEach((s) => s.store.sweep(idleMs, maxMs))

  return {
    // `target` is a path with query, as sent by the client ("/api/...").
    // `cookie` is the request's Cookie header.
    handle(method: string, target: string, bodyText?: string, contentType?: string | null, cookie?: string | null): MockResponse {
      counter++
      try {
        sweepAll()
        // Writes must say they are JSON, like the real server insists.
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
  }
}

export type MockServer = ReturnType<typeof createMockServer>
