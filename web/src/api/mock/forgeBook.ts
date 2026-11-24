// What the in-memory server plays for the hosts with an OAuth application (docs/api.md, "Connections to GitLab
// and Gitea"): the administrators' settings of each host, each person's connection, a few places and
// repositories to choose from, and the refusals the real server gives a configuration (a host that cannot be
// reached, one that is not public, a client it does not accept). There is no real host behind it.

import { HttpError } from './store.ts'
import type {
  AdminForge,
  AdminProviderRow,
  Connection,
  ConnectionLeftover,
  ConnectionOwner,
  ConnectionRepo,
  CreateAdminForgeInput,
  CursorPage,
  PatchAdminForgeInput,
  Provider,
} from '../types.ts'

interface HostRec {
  id: string
  type: string
  host: string
  name: string
  clientId: string
  hasSecret: boolean
  checked: number
  updated: number
  // Mock only: what the host says about itself now.
  down: boolean
}
interface AuthRec {
  login: string
  accountId: number
  state: 'ok' | 'expired'
  connected: number
  admin?: boolean
}
interface Attempt {
  provider: string
  userId: string
  ret: string
  created: number
}

const PUBLIC_URL = 'https://notes.example.test'
const PAGE = 30
const ID_RE = /^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$/
const HOST_RE = /^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*(?::\d{1,5})?$/

export const FORGE_COOKIE = 'pb_forge_'

export function createForgeBook(deps: {
  now: () => number
  random: () => number
  spacesUsing: (userId: string, provider: string) => number
  hasSpaces: (provider: string) => number
}) {
  const hosts = new Map<string, HostRec>()
  // By provider, then by person.
  const auths = new Map<string, Map<string, AuthRec>>()
  const attempts = new Map<string, Attempt>()
  const leftovers = new Map<string, Map<string, ConnectionLeftover[]>>()
  let nextAccount = 9000
  const key = (s: string) => s.toLowerCase()
  const iso = (ms: number) => new Date(ms).toISOString().replace(/\.\d+Z$/, 'Z')
  const invalid = (reason: string, m: string) => new HttpError(400, 'invalid', m, { reason })

  const has = (id: string) => hosts.has(id)
  const need = (id: string): HostRec => {
    const h = hosts.get(id)
    if (!h) throw new HttpError(404, 'not_found', 'no such provider', { provider: id })
    return h
  }
  const authsOf = (id: string) => {
    let m = auths.get(id)
    if (!m) auths.set(id, (m = new Map()))
    return m
  }
  const connectionsOf = (id: string) => authsOf(id).size
  const configured = (h: HostRec) => !h.down

  // --- the administrators' side ------------------------------------------------------

  const partial = (h: HostRec): 'yes' | 'no' | 'unknown' => (h.host.includes('nofilter') ? 'no' : 'unknown')

  const row = (h: HostRec): AdminProviderRow => ({
    id: h.id,
    type: h.type,
    name: h.name,
    host: h.host,
    configured: configured(h),
    partialSync: partial(h),
    connections: connectionsOf(h.id),
    spaces: deps.hasSpaces(h.id),
  })
  const list = (): AdminProviderRow[] => [...hosts.values()].map(row)

  const view = (h: HostRec): AdminForge => ({
    id: h.id,
    type: h.type,
    host: h.host,
    name: h.name,
    owner: 'site',
    configured: configured(h),
    clientId: h.clientId,
    hasSecret: h.hasSecret,
    callbackUrl: `${PUBLIC_URL}/api/connections/${h.id}/authorization/callback`,
    scopes: h.type === 'gitlab' ? ['api'] : [],
    userAuthorization: configured(h),
    version: h.type === 'gitlab' ? '' : '1.22.0',
    checked: iso(h.checked),
    partialSync: partial(h),
    connections: connectionsOf(h.id),
    spaces: deps.hasSpaces(h.id),
    updated: iso(h.updated),
  })
  const admin = (id: string): AdminForge => view(need(id))

  // The checks the server makes of a host before it keeps anything; a host whose name says what is wrong plays it.
  const checkHost = (host: string, secret: string) => {
    if (/(^|\.)(localhost|private)(\.|:|$)|^\d+\.\d+\.\d+\.\d+/.test(host))
      throw invalid('host_not_public', 'that host is not on a public address')
    if (host.includes('down')) throw invalid('host_unreachable', 'the host could not be asked')
    if (host.includes('other')) throw invalid('not_this_platform', 'the host does not answer as this kind of host')
    if (host.includes('old')) throw invalid('unsupported_version', 'that version is too old')
    if (secret === 'wrong') throw invalid('client_rejected', 'the host does not accept this client id and secret')
  }

  const create = (input: Partial<CreateAdminForgeInput>): AdminForge => {
    const id = typeof input.id === 'string' ? input.id : ''
    if (!ID_RE.test(id) || id === 'git' || id === 'github' || id.startsWith('t-'))
      throw new HttpError(400, 'invalid', 'id is lowercase letters, digits and "-"')
    if (input.type !== 'gitlab' && input.type !== 'gitea') throw invalid('type_invalid', 'type is gitlab or gitea')
    const host = String(input.host ?? '')
      .trim()
      .toLowerCase()
      .replace(/^https:\/\//, '')
      .replace(/\/$/, '')
    if (!HOST_RE.test(host)) throw invalid('host_invalid', 'the host is a name, with a port if it has one')
    if (
      typeof input.clientId !== 'string' ||
      !input.clientId ||
      typeof input.clientSecret !== 'string' ||
      !input.clientSecret
    )
      throw new HttpError(400, 'invalid', 'clientId and clientSecret are required')
    if (hosts.has(id)) throw new HttpError(409, 'exists', 'a provider with this id exists already')
    for (const h of hosts.values())
      if (h.host === host) throw new HttpError(409, 'exists', `this host has a provider already (${h.id})`)
    if (hosts.size >= 20)
      throw new HttpError(409, 'provider_limit', 'the server has 20 such providers, the most there can be')
    checkHost(host, input.clientSecret)
    const now = deps.now()
    const name = typeof input.name === 'string' && input.name.trim() ? input.name.trim() : host
    const h: HostRec = {
      id,
      type: input.type,
      host,
      name,
      clientId: input.clientId,
      hasSecret: true,
      checked: now,
      updated: now,
      down: false,
    }
    hosts.set(id, h)
    return view(h)
  }

  const patch = (id: string, input: PatchAdminForgeInput): AdminForge => {
    const h = need(id)
    if (input.clientSecret !== undefined || input.clientId !== undefined)
      checkHost(h.host, input.clientSecret ?? 'kept')
    if (typeof input.name === 'string' && input.name.trim()) h.name = input.name.trim()
    if (typeof input.clientId === 'string' && input.clientId) h.clientId = input.clientId
    if (typeof input.clientSecret === 'string' && input.clientSecret) h.hasSecret = true
    h.checked = h.updated = deps.now()
    h.down = false
    return view(h)
  }

  const check = (id: string): AdminForge => {
    const h = need(id)
    if (h.host.includes('down') && !h.down) {
      h.down = true
      throw invalid('host_not_public', 'the host fails its check')
    }
    h.down = false
    h.checked = deps.now()
    return view(h)
  }

  const remove = (id: string) => {
    const h = need(id)
    const connections = connectionsOf(id)
    const spaces = deps.hasSpaces(id)
    if (connections > 0 || spaces > 0)
      throw new HttpError(409, 'provider_in_use', 'people are connected or spaces sync through it', {
        provider: id,
        connections,
        spaces,
      })
    hosts.delete(h.id)
    auths.delete(id)
  }

  // --- the people's side -------------------------------------------------------------

  const connectionOf = (id: string, userId: string): Connection | null => {
    const a = authsOf(id).get(userId)
    if (!a) return null
    return {
      login: a.login,
      accountId: String(a.accountId),
      state: a.state,
      connected: iso(a.connected),
      spaces: deps.spacesUsing(userId, id),
      ...(a.admin ? { admin: true } : {}),
    }
  }

  const info = (userId: string): Provider[] =>
    [...hosts.values()].map((h) => ({
      id: h.id,
      type: h.type,
      host: h.host,
      name: h.name,
      owner: 'site' as const,
      capabilities: {
        createRepo: true,
        installations: false,
        connectAccount: true,
        storageTarget: true,
        partialSync: partial(h),
      },
      configured: configured(h),
      app: null,
      userAuthorization: configured(h),
      connection: connectionOf(h.id, userId),
      leftovers: [...(leftovers.get(h.id)?.get(userId) ?? [])],
    }))

  const randomState = () =>
    Array.from({ length: 24 }, () => 'abcdefghijklmnopqrstuvwxyz0123456789'[Math.floor(deps.random() * 36)]).join('')

  const requireHost = (id: string): HostRec => {
    const h = need(id)
    if (!configured(h))
      throw new HttpError(409, 'provider_not_configured', 'this provider is not set up', { provider: id })
    return h
  }
  const notConnected = (id: string) =>
    new HttpError(409, 'not_connected', 'connect your account first', { provider: id })

  const start = (id: string, userId: string, body: Record<string, unknown>): { url: string; state: string } => {
    requireHost(id)
    const state = randomState()
    attempts.set(state, {
      provider: id,
      userId,
      ret:
        typeof body.return === 'string' && body.return.startsWith('/') && !body.return.startsWith('//')
          ? body.return
          : '/',
      created: deps.now(),
    })
    return { url: `/api/connections/${id}/authorization/callback?state=${state}`, state }
  }

  const callback = (
    id: string,
    userId: string | null,
    cookieState: string | undefined,
    q: URLSearchParams,
  ): { to: string; clear: boolean } => {
    const fail = (c: string) => ({ to: `/settings?provider=${id}&connection_error=${c}`, clear: true })
    const state = q.get('state') ?? ''
    const at = attempts.get(state)
    if (
      !at ||
      at.provider !== id ||
      cookieState !== state ||
      !userId ||
      at.userId !== userId ||
      deps.now() - at.created > 600_000
    )
      return { to: `/settings?provider=${id}&connection_error=expired`, clear: false }
    attempts.delete(state)
    if (q.get('error')) return fail('denied')
    const login = (q.get('login') ?? `${userId.slice(0, 6).toLowerCase()}-fg`).toLowerCase()
    for (const [uid, a] of authsOf(id)) if (uid !== userId && a.login === login) return fail('account_in_use')
    const prev = authsOf(id).get(userId)
    if (prev && prev.login !== login && deps.spacesUsing(userId, id) > 0) return fail('different_account')
    authsOf(id).set(userId, { login, accountId: prev?.accountId ?? ++nextAccount, state: 'ok', connected: deps.now() })
    return { to: at.ret, clear: true }
  }

  const disconnect = (id: string, userId: string) => {
    need(id)
    authsOf(id).delete(userId)
  }
  const dismiss = (id: string, userId: string, repoId: number) => {
    need(id)
    const m = leftovers.get(id)?.get(userId)
    if (m)
      leftovers.get(id)!.set(
        userId,
        m.filter((l) => l.repoId !== repoId),
      )
  }

  const page = <T>(all: T[], cursor: string | null): CursorPage<T> => {
    const start = cursor === null || cursor === '' ? 0 : Number(cursor.replace(/^c/, ''))
    if (!Number.isInteger(start) || start < 0)
      throw new HttpError(400, 'invalid', 'the cursor is not one this server made')
    return { items: all.slice(start, start + PAGE), next: start + PAGE < all.length ? `c${start + PAGE}` : '' }
  }
  const usable = (id: string, userId: string): AuthRec => {
    const a = authsOf(id).get(userId)
    if (!a || a.state !== 'ok') throw notConnected(id)
    return a
  }

  const owners = (id: string, userId: string, cursor: string | null, q: string): CursorPage<ConnectionOwner> => {
    requireHost(id)
    const a = usable(id, userId)
    const all: ConnectionOwner[] = [
      { login: a.login, type: 'User', via: 'user', installationId: null, needsInstall: false },
      { login: `${a.login}-team`, type: 'Organization', via: 'user', installationId: null, needsInstall: false },
    ]
    return page(
      all.filter((o) => key(o.login).includes(key(q))),
      cursor,
    )
  }

  const repos = (id: string, userId: string, cursor: string | null, q: string): CursorPage<ConnectionRepo> => {
    const h = requireHost(id)
    const a = usable(id, userId)
    const all: ConnectionRepo[] = ['journal', 'notes'].map((name, i) => ({
      id: 7000 + i,
      fullName: `${a.login}/${name}`,
      private: true,
      defaultBranch: 'main',
      htmlUrl: `https://${h.host}/${a.login}/${name}`,
      canPush: true,
    }))
    return page(
      all.filter((r) => key(r.fullName).includes(key(q))),
      cursor,
    )
  }

  // Mock only: connect a person without the round trip, or end their connection.
  const connect = (id: string, userId: string, login: string, admin = false) => {
    need(id)
    authsOf(id).set(userId, { login, accountId: ++nextAccount, state: 'ok', connected: deps.now(), admin })
  }
  const expire = (id: string, userId: string) => {
    const a = authsOf(id).get(userId)
    if (a) a.state = 'expired'
  }
  const setDown = (id: string, down: boolean) => {
    need(id).down = down
  }

  return {
    has,
    list,
    admin,
    create,
    patch,
    check,
    remove,
    info,
    start,
    callback,
    disconnect,
    dismiss,
    owners,
    repos,
    connect,
    expire,
    setDown,
  }
}
