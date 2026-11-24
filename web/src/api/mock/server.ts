// An in-memory implementation of docs/api.md, for `npm run dev:mock` and for
// tests. Single-user mode is the default; accounts mode adds sessions (a
// cookie stand-in), invites, spaces per user and a fake OpenID Connect
// provider. It has no DOM or Node dependencies.

import type {
  AssetsRemote,
  CacheState,
  AdminSpace,
  CaptchaContext,
  ConnectRemoteInput,
  DailyNotice,
  FieldKind,
  Identity,
  Invite,
  Member,
  Page,
  Permission,
  Remote,
  RemoteKey,
  RemoteState,
  Role,
  ServerStatus,
  SettingsNotice,
  SpaceInfo,
  SpaceRole,
  User,
  AdminGrant,
  AdminToken,
  ApiToken,
  ConsentInfo,
  Grant,
  GitHubGap,
  Provider,
} from '../types.ts'
import { gitBlobId } from './sha1.ts'
import {
  ALPHABET,
  HttpError,
  compareCodePoints,
  createSpaceStore,
  isRecord,
  rfc3339,
  validFolder,
  type SpaceStore,
} from './store.ts'
import { SPACE_NAME } from './seed.ts'
import { createGitHubBook, type MockGitHubOptions } from './githubBook.ts'
import { createForgeBook, FORGE_COOKIE } from './forgeBook.ts'
import { pickNumber, type DailySource } from './homeBook.ts'
import { canonicalTag, matchTag } from '../../lib/langTags.ts'
import { parseAttribution } from './attribution.ts'
import { createFileStore, DEFAULT_MAX_UPLOAD, type FileStore } from './files.ts'
import { DEFAULT_MAX_IMPORT, runImport } from './importer.ts'
import { folderRuleMessage } from '../../lib/folderRules.ts'
import { createGroupBook } from './groupBook.ts'
import { createTenantBook, DEFAULT_TENANT } from './tenantBook.ts'
import { createQuotaBook } from './quotaBook.ts'
import { createMailBook } from './mailBook.ts'
import { createSignInBook, type PasskeyRec, type ProviderRec, type SignInBookOptions } from './signInBook.ts'

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
  // Accounts mode: sign-in providers that exist from the start (the pretend identity provider signs in as `sub`).
  providers?: SignInBookOptions['providers']
  // Accounts mode: security settings that differ from the defaults.
  security?: SignInBookOptions['security']
  // What the administration's security page shows as notices.
  securityNotices?: SignInBookOptions['notices']
  // The server's `--public-url`; `null` for none (providers and third-party captchas are then refused). Default https://notes.example.com.
  publicUrl?: string | null
  // Whether passkeys can work on this server (default true).
  passkeysAvailable?: boolean
  // Accounts mode: an invite that exists from the start, with this token.
  invite?: { token: string; role?: Role; fill?: { username: string; name: string; email: string } }
  // Drafts the server commits by itself (docs/api.md: --draft-idle, --draft-max).
  // Accounts mode: --max-spaces, the site's quota.spacesPerPerson (0 is no limit); the default is 20. POST /api/spaces is 403 quota_exceeded beyond it.
  maxSpaces?: number
  // Single-user mode: the inbox folder (`--inbox`); default `Inbox`.
  inbox?: string
  draftIdleMs?: number
  draftMaxMs?: number
  // Following a remote repository (docs/api.md, "Remote repository").
  remote?: MockRemoteOptions
  // GitHub (docs/api.md, "GitHub"): whether the App is set up from the start.
  github?: MockGitHubOptions
  // The largest upload in bytes (`--max-upload`); 10 MiB by default.
  maxUpload?: number
  // The largest import in bytes (`--max-import`); 100 MiB by default.
  maxImport?: number
  // Accounts mode: the server has an authorization server for MCP clients (it was given
  // --public-url), so the consent endpoints and the grants answer; otherwise they are 404.
  oauth?: boolean
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
const GITHUB_COOKIE = 'pb_github'
// The provider that is a plain address: nothing to connect (docs/api.md, "Connections").
const isConnectionCode = (c: string | undefined) =>
  [
    'connection_expired',
    'connection_missing',
    'installation_gone',
    'app_missing_permission',
    'provider_not_configured',
    'repo_not_in_installation',
  ].includes(c ?? '')
const GIT_PROVIDER: Provider = {
  id: 'git',
  type: 'git',
  host: '',
  name: 'Git',
  owner: 'site',
  capabilities: {
    createRepo: false,
    installations: false,
    connectAccount: false,
    storageTarget: false,
    partialSync: 'unknown',
  },
  configured: true,
  app: null,
  userAuthorization: false,
  connection: null,
  leftovers: [],
}
const GITHUB_MANIFEST_COOKIE = 'pb_github_manifest'
const LOCAL_USER: User = {
  id: 'local',
  username: 'local',
  name: 'Local user',
  email: '',
  emailVerified: false,
  role: 'admin',
  disabled: false,
  passkeyRequired: false,
  created: '2026-01-01T00:00:00Z',
}

interface UserRec extends Omit<User, 'passkeyRequired' | 'passkeys' | 'sso'> {
  passwordHash?: string
  // Set for this person alone; the role's requirement is in the security settings.
  passkeyRequired: boolean
  passkeys: PasskeyRec[]
  identities: (Pick<Identity, 'id' | 'label' | 'created'> & { providerSlug: string; sub: string })[]
}
interface InviteRec {
  id: string
  tokenHash: string
  role: Role
  created: number
  expires: number
  createdBy: string
  used: boolean
  // What the invite starts the form with (a new tenant's first administrator).
  fill?: { username: string; name: string; email: string }
}
interface TokenRec {
  id: string
  userId: string
  name: string
  prefix: string
  access: 'read' | 'write'
  spaces: string[]
  created: number
  expires: number | null
  revoked: number | null
}
interface GrantRec {
  id: string
  userId: string
  clientId: string
  clientName: string
  access: 'read' | 'write'
  spaces: string[]
  created: number
  lastUsed: number
}
// An authorization request waiting on the consent page.
interface PendingRec {
  userId: string
  clientName: string
  clientId: string
  redirectUri: string
  state: string
  scopes: ('read' | 'write')[]
  expires: number
}
interface StartOptions {
  clientName?: string
  clientId?: string
  redirectUri?: string
  state?: string
  scopes?: ('read' | 'write')[]
}
interface RemoteRec {
  url: string
  branch: string
  credential:
    | { type: 'ssh' }
    | { type: 'token'; username: string; token: string }
    | { type: 'github'; via: 'app'; installationId: number; repoId: number }
    | { type: 'github'; via: 'user'; userId: string; repoId: number }
  state: RemoteState
  // What the error state needs (docs/api.md, "Remote repository"); "" otherwise.
  errorCode?: string
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
  // The groups that are members of the space, with their roles (3.2).
  groups: Map<string, SpaceRole>
  personal: boolean
  created: number
  // Switched off by an administrator (docs/api.md, "Administration").
  disabled: boolean
  // The last stored measurement; the real server measures in the background,
  // the mock only when asked.
  size: { bytes: number; at: number; pages: number } | null
  inbox: string
  store: SpaceStore
  files: FileStore
  remote: RemoteRec | null
  key: RemoteKey | null
  // Mock only: whether the pretend remote answers.
  reachable: boolean
  // Server storage (docs/api.md, "Server storage"): what the copy on the server is, when it was last used,
  // and the administrators' pin and exemption.
  cache: 'full' | 'partial' | 'evicted'
  lastUsed: number
  pinned: boolean
  exempt: boolean
  // The attachment repository (docs/api.md, "Server storage"), or null.
  assets: AssetsRec | null
}
// Mock only: a move is played one step each time it is asked for (0 %, 50 %, done).
interface AssetsRec {
  url: string
  branch: string
  credential: RemoteRec['credential']
  used: boolean
  move: {
    state: 'copying' | 'switching' | 'done' | 'failed'
    files: number
    filesDone: number
    bytes: number
    bytesDone: number
    error: string
    step: number
  } | null
  // Bytes of the space's attachments in the cache (a mock number).
  cacheBytes: number
}
interface OidcAttempt {
  provider: string
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
const NO_PERMISSION = (need: Permission) =>
  `your role in this space does not allow that; it needs the ${need} permission`

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
const setCookie = (name: string, value: string, maxAge?: number) =>
  `${name}=${value}; Path=/; HttpOnly; SameSite=Lax${maxAge === undefined ? '' : `; Max-Age=${maxAge}`}`

// What the sign-in cookie holds is meaningless to the client, as in the real server.
const plainPath = (p: unknown): string =>
  typeof p === 'string' && p.startsWith('/') && !p.startsWith('//') && !p.includes('\\') ? p : '/'

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
  const apiTokens = new Map<string, TokenRec>()
  // What an administrator allows machines (docs/api.md, "Machine access").
  const machine = { tokens: true, mcp: true }
  const grants = new Map<string, GrantRec>()
  const pending = new Map<string, PendingRec>()
  const spaces = new Map<string, SpaceRec>()
  // What the server says about settings files it could not use, by space (set by tests).
  const settingsNotices = new Map<string, SettingsNotice[]>()
  const oidcAttempts = new Map<string, OidcAttempt>()
  const failures = new Map<string, { count: number; first: number }>()
  const github = createGitHubBook(options.github ?? {}, {
    now,
    random,
    spacesUsing: (uid) =>
      [...spaces.values()].filter(
        (s) =>
          s.remote?.credential.type === 'github' &&
          s.remote.credential.via === 'user' &&
          s.remote.credential.userId === uid,
      ).length,
    // The mock keeps a connection through GitHub as type "github" (the API shows it as type "connection", provider "github").
    spacesOnApp: () => [...spaces.values()].filter((s) => s.remote?.credential.type === 'github').length,
    // The mock has one world: everyone is in the default tenant.
    tenantOf: () => DEFAULT_TENANT,
    tenantExists: (id) => tenantBook.exists(id),
  })
  // The hosts with an OAuth application (GitLab, Gitea): a spaces here never sync through one, so they are not counted.
  const forge = createForgeBook({ now, random, spacesUsing: () => 0, hasSpaces: () => 0 })
  // Sessions held until a passkey is registered, and when each session began (registering needs a recent sign-in).
  const held = new Set<string>()
  const sessionAt = new Map<string, number>()
  const ceremonies = new Map<
    string,
    { kind: 'login' | 'register'; username?: string; userId?: string; name?: string; created: number }
  >()
  // Failures counted toward a captcha, by context and key (the username for a login, `ip` otherwise).
  const captchaFails = new Map<string, number>()
  let auditCounter = 0
  const publicUrl = options.publicUrl === undefined ? 'https://notes.example.com' : options.publicUrl
  const book = createSignInBook({
    now,
    publicUrl,
    passkeysAvailable: options.passkeysAvailable ?? true,
    users: () => users.values(),
    newAuditId: () => `A${String(++auditCounter).padStart(8, '0')}`,
    security: options.security,
    providers: options.providers,
    notices: options.securityNotices,
    groupSource: (id) => groupBook.groups.get(id)?.source,
    onProviderDeleted: (slug) => groupBook.dropProvider(slug),
    onMappingRemoved: (slug, value, keep) => groupBook.dropMapped(slug, value, keep),
    registrationOpen: () => tenantBook.registrationStored(),
  })
  const groupBook = createGroupBook({
    now: () => new Date(now()).toISOString().replace(/\.\d{3}Z$/, 'Z'),
    users: () => users,
    spaces: () => spaces,
    newId: (taken) => newId(taken),
    book: () => book,
  })
  if (oidc) book.addLegacy(oidc.name, oidc.autoCreate === true, oidc.sub ?? 'mock-sub-1')

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

  // An administrator is one of the whole server unless a site administrator said otherwise (3.3).
  const isSiteAdmin = (u: UserRec | undefined) => u?.role === 'admin' && u.siteAdmin !== false
  const publicUser = (u: UserRec): User => ({
    id: u.id,
    username: u.username,
    name: u.name,
    email: u.email,
    emailVerified: u.emailVerified,
    role: u.role,
    disabled: u.disabled,
    passkeyRequired: book.requiredOf(u),
    siteAdmin: isSiteAdmin(u),
    tenant: { id: DEFAULT_TENANT, slug: 'default', name: 'Default' },
    created: u.created,
    ...(u.remoteExempt ? { remoteExempt: true } : {}),
  })
  // In the users list a person also has how many passkeys and sign-in identities.
  const listedUser = (u: UserRec): User => ({
    ...publicUser(u),
    passkeys: u.passkeys.length,
    sso: u.identities.length,
    quota: quotaBook.ownOf(u.id),
  })
  // The first owner: who the space's own commits are credited to.
  const ownerOf = (s: SpaceRec): UserRec | undefined => {
    const owners = [...s.members].filter(([, m]) => m.role === 'owner').sort((a, b) => a[1].joined - b[1].joined)
    return owners[0] ? users.get(owners[0][0]) : undefined
  }
  const spaceInfo = (s: SpaceRec, u: UserRec): SpaceInfo => {
    const { role, direct, via } = groupBook.reach(s, u.id)
    return {
      id: s.id,
      name: s.name,
      role: role ?? '',
      directRole: direct ?? '',
      viaGroups: via,
      // An administrator who is not a member may do everything.
      permissions: [...(role ? ROLE_PERMISSIONS[role] : ROLE_PERMISSIONS.owner)],
      personal: s.personal,
      sync: s.remote ? s.remote.state : 'local',
      inbox: s.inbox,
      disabled: s.disabled,
      ...(accounts
        ? {
            storage: {
              cache: cacheOf(s),
              assets: (s.assets ? 'repository' : 'space') as 'space' | 'repository',
              remote: noticeOf(s),
            },
          }
        : {}),
    }
  }

  // --- server storage (docs/api.md, "Server storage") ---
  const GIB = 1024 ** 3
  const DAY = 86_400_000
  const storage = {
    remotes: {
      require: null as 'any' | 'github' | null,
      scope: 'new' as 'new' | 'all',
      graceDays: 30,
      since: null as number | null,
    },
    hosts: { allowed: [...(options.remote?.hosts ?? DEFAULT_HOSTS)] },
    localSpaces: { maxBytes: null as number | null },
    cache: {
      partial: true,
      evictAfterDays: 14 as number | null,
      minIdleHours: 24,
      clonesBytes: null as number | null,
      minFreeBytes: (10 * GIB) as number | null,
      assetsBytes: 5 * GIB,
    },
    assets: { newSpaces: false },
  }
  const cacheOf = (s: SpaceRec): CacheState => (!s.remote ? 'local' : s.cache)
  const ownerExempt = (s: SpaceRec) =>
    [...s.members].some(([id, m]) => m.role === 'owner' && users.get(id)?.remoteExempt === true)
  // What the requirement asks of a space that does not meet it yet: the deadline is the space's creation (or the time
  // the requirement took its form, for an older space under scope "all") plus the grace period.
  const noticeOf = (s: SpaceRec): { required: 'any' | 'github'; deadline: string; overdue: boolean } | null => {
    const req = storage.remotes.require
    if (!req || s.exempt || ownerExempt(s)) return null
    const cred = s.remote?.credential.type
    if (req === 'github' ? cred === 'github' : s.remote !== null) return null
    const since = storage.remotes.since ?? 0
    const base = s.created >= since ? s.created : storage.remotes.scope === 'all' ? since : null
    if (base === null) return null
    const deadline = base + storage.remotes.graceDays * DAY
    return { required: req, deadline: rfc3339(deadline), overdue: now() >= deadline }
  }
  const requiredBlock = (s: SpaceRec) => {
    const n = noticeOf(s)
    if (!n?.overdue) return null
    return {
      message: '这个空间已经过了必须跟随远端仓库的期限，现在只读；连上远端后就能再改。',
      extra: { require: n.required, deadline: n.deadline },
    }
  }
  const storageBody = () => {
    const all = [...spaces.values()]
    const notices = all.map(noticeOf).filter((n) => n !== null)
    const asked = storage.remotes.require
    const exempt = asked
      ? all.filter(
          (s) =>
            (s.exempt || ownerExempt(s)) &&
            (asked === 'github' ? s.remote?.credential.type !== 'github' : s.remote === null),
        ).length
      : 0
    return {
      remotes: { ...storage.remotes, since: storage.remotes.since === null ? null : rfc3339(storage.remotes.since) },
      hosts: { allowed: storage.hosts.allowed, fromFlag: false },
      localSpaces: storage.localSpaces,
      cache: storage.cache,
      assets: storage.assets,
      git: { version: '2.53.0', partialClone: true },
      affected: {
        inGrace: notices.filter((n) => !n.overdue).length,
        overdue: notices.filter((n) => n.overdue).length,
        exempt,
      },
    }
  }
  const storageStatus = () => {
    const all = [...spaces.values()]
    const count = (f: (s: SpaceRec) => boolean) => all.filter(f).length
    const aff = storageBody().affected
    const sizeOfClone = (s: SpaceRec) => (s.remote && s.cache === 'evicted' ? 0 : (s.size?.bytes ?? 0))
    return {
      spaces: {
        local: count((s) => !s.remote),
        full: count((s) => !!s.remote && s.cache === 'full'),
        partial: count((s) => !!s.remote && s.cache === 'partial'),
        evicted: count((s) => !!s.remote && s.cache === 'evicted'),
        restoring: 0,
      },
      cloneBytes: all.reduce((n, s) => n + sizeOfClone(s), 0),
      indexBytes: all.reduce((n, s) => n + (s.size ? Math.round(s.size.bytes / 8) : 0), 0),
      assetCacheBytes: all.reduce((n, s) => n + (s.assets?.cacheBytes ?? 0), 0),
      clonesBudget: storage.cache.clonesBytes,
      assetsBudget: storage.cache.assetsBytes,
      restoresWaiting: 0,
      remotes: { inGrace: aff.inGrace, overdue: aff.overdue },
    }
  }
  const putStorage = (body: Record<string, unknown>) => {
    const section = (v: unknown, known: string[], name: string): Record<string, unknown> => {
      if (v === undefined) return {}
      if (!isRecord(v)) throw new HttpError(400, 'invalid', `${name} 要是一个对象`)
      for (const k of Object.keys(v))
        if (!known.includes(k)) throw new HttpError(400, 'invalid', `${name}.${k} 不是这里的设置`)
      return v
    }
    const int = (v: unknown, lo: number, hi: number, what: string): number => {
      if (typeof v !== 'number' || !Number.isInteger(v) || v < lo || v > hi)
        throw new HttpError(400, 'invalid', `${what} 要在 ${lo} 到 ${hi} 之间`)
      return v
    }
    const nullable = (v: unknown, lo: number, what: string): number | null => {
      if (v === null) return null
      if (typeof v !== 'number' || !Number.isInteger(v) || v < lo)
        throw new HttpError(400, 'invalid', `${what} 要么是 null，要么至少 ${lo}`)
      return v
    }
    for (const k of Object.keys(body))
      if (!['remotes', 'hosts', 'localSpaces', 'cache', 'assets'].includes(k))
        throw new HttpError(400, 'invalid', `不认识 ${k}`)
    const rem = section(body.remotes, ['require', 'scope', 'graceDays'], 'remotes')
    const hosts = section(body.hosts, ['allowed'], 'hosts')
    const local = section(body.localSpaces, ['maxBytes'], 'localSpaces')
    const cache = section(
      body.cache,
      ['partial', 'evictAfterDays', 'minIdleHours', 'clonesBytes', 'minFreeBytes', 'assetsBytes'],
      'cache',
    )
    const assets = section(body.assets, ['newSpaces'], 'assets')
    const next = structuredClone(storage)
    if ('require' in rem) {
      // null, "any", or the id of a provider the server has (the mock has GitHub only).
      if (rem.require !== null && rem.require !== 'any' && rem.require !== 'github')
        throw new HttpError(400, 'invalid', 'remotes.require 是 null、"any" 或一个提供方的 id')
      if (rem.require === 'github' && !github.admin().configured)
        throw new HttpError(409, 'provider_not_configured', '服务器还没有配好 GitHub，不能要求用它', {
          provider: 'github',
        })
      next.remotes.require = rem.require
    }
    if ('scope' in rem) {
      if (rem.scope !== 'new' && rem.scope !== 'all')
        throw new HttpError(400, 'invalid', 'remotes.scope 是 "new" 或 "all"')
      next.remotes.scope = rem.scope
    }
    if ('graceDays' in rem) next.remotes.graceDays = int(rem.graceDays, 0, 365, 'remotes.graceDays')
    if ('allowed' in hosts) {
      if (
        !Array.isArray(hosts.allowed) ||
        hosts.allowed.length === 0 ||
        !hosts.allowed.every((h) => typeof h === 'string' && h.trim() !== '' && !/[\s/:@]/.test(h))
      )
        throw new HttpError(400, 'invalid', 'hosts.allowed 要是一个主机名的列表')
      next.hosts.allowed = hosts.allowed.map((h) => (h as string).trim().toLowerCase())
    }
    if ('maxBytes' in local) next.localSpaces.maxBytes = nullable(local.maxBytes, 1024 ** 2, 'localSpaces.maxBytes')
    if ('partial' in cache) {
      if (typeof cache.partial !== 'boolean') throw new HttpError(400, 'invalid', 'cache.partial 要是布尔值')
      next.cache.partial = cache.partial
    }
    if ('evictAfterDays' in cache)
      next.cache.evictAfterDays =
        cache.evictAfterDays === null ? null : int(cache.evictAfterDays, 1, 365, 'cache.evictAfterDays')
    if ('minIdleHours' in cache) next.cache.minIdleHours = int(cache.minIdleHours, 1, 720, 'cache.minIdleHours')
    if ('clonesBytes' in cache) next.cache.clonesBytes = nullable(cache.clonesBytes, GIB, 'cache.clonesBytes')
    if ('minFreeBytes' in cache) next.cache.minFreeBytes = nullable(cache.minFreeBytes, 0, 'cache.minFreeBytes')
    if ('assetsBytes' in cache)
      next.cache.assetsBytes = int(cache.assetsBytes, 64 * 1024 ** 2, Number.MAX_SAFE_INTEGER, 'cache.assetsBytes')
    if ('newSpaces' in assets) {
      if (typeof assets.newSpaces !== 'boolean') throw new HttpError(400, 'invalid', 'assets.newSpaces 要是布尔值')
      next.assets.newSpaces = assets.newSpaces
    }
    // The requirement's "since" is when it took its present form.
    if (next.remotes.require === null) next.remotes.since = null
    else if (storage.remotes.require !== next.remotes.require || storage.remotes.scope !== next.remotes.scope)
      next.remotes.since = now()
    Object.assign(storage, next)
  }

  // Why writes fail right now, if they do (docs/api.md: 503 `unavailable`).
  const blockedReason = (s: SpaceRec): string | null => {
    if (!s.remote) return null
    if (s.remote.state === 'offline')
      return `远端仓库现在连不上${s.remote.error ? `（${s.remote.error}）` : ''}。这次修改没有写入，已写的草稿还在。`
    if (s.remote.state === 'rewritten')
      return '远端仓库的历史被改写过，这个空间已暂停写入。请空间的所有者在空间设置里查看并决定是否接受远端的历史。'
    return null
  }

  const addSpace = (
    id: string,
    name: string,
    ownerId: string,
    personal: boolean,
    seed: boolean,
    inbox = options.inbox ?? DEFAULT_INBOX,
  ) => {
    const rec: SpaceRec = {
      id,
      name,
      members: new Map([[ownerId, { role: 'owner', joined: ++counter }]]),
      groups: new Map(),
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
      cache: 'partial',
      lastUsed: now(),
      pinned: false,
      exempt: false,
      assets: null,
    }
    rec.store = createSpaceStore({
      now,
      random,
      seed,
      author: () => ownerOf(rec)?.name ?? 'Local user',
      blocked: () => blockedReason(rec),
      required: () => requiredBlock(rec),
      inbox: () => rec.inbox,
      fileExists: (path) => rec.files.read(path.split('/')) !== null,
    })
    spaces.set(id, rec)
    return rec
  }

  const addUser = (
    input: {
      username: string
      name?: string
      role: Role
      password?: string
      identity?: { sub: string; label: string; provider: string }
    },
    seedSpace: boolean,
  ) => {
    const id = newId((x) => users.has(x))
    const u: UserRec = {
      id,
      username: input.username,
      name: input.name || input.username,
      email: '',
      emailVerified: false,
      role: input.role,
      disabled: false,
      passkeyRequired: false,
      created: rfc3339(now()),
      passwordHash: input.password === undefined ? undefined : hashOf(input.password),
      identities: [],
      passkeys: [],
    }
    if (input.identity)
      u.identities.push({
        id: newId(() => false),
        providerSlug: input.identity.provider,
        label: input.identity.label,
        created: rfc3339(now()),
        sub: input.identity.sub,
      })
    users.set(id, u)
    // Every account gets a personal space.
    addSpace(
      newId((x) => spaces.has(x)),
      SPACE_NAME,
      id,
      true,
      seedSpace,
    )
    return u
  }

  // --- initial state ---
  if (!accounts) {
    users.set(LOCAL_USER.id, { ...LOCAL_USER, identities: [], passkeys: [] })
    addSpace('default', SPACE_NAME, LOCAL_USER.id, true, !options.empty)
  } else {
    if (options.admin !== false) {
      const a = options.admin ?? MOCK_ADMIN
      addUser({ username: a.username, name: a.name, role: 'admin', password: a.password }, !options.empty)
    }
    if (options.invite) {
      const id = newId((x) => invites.has(x))
      invites.set(id, {
        id,
        tokenHash: hashOf(options.invite.token),
        role: options.invite.role ?? 'member',
        created: now(),
        expires: now() + 7 * 86_400_000,
        createdBy: [...users.keys()][0] ?? '',
        used: false,
        fill: options.invite.fill,
      })
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
      .filter((s) => groupBook.has(s, u.id))
      .sort((a, b) => Number(b.personal) - Number(a.personal) || compareCodePoints(a.name, b.name))
  }

  const startSession = (u: UserRec, opts: { held?: boolean } = {}): string => {
    const token = randomString(32)
    sessions.set(token, u.id)
    sessionAt.set(token, now())
    if (opts.held) held.add(token)
    return setCookie(SESSION_COOKIE, token)
  }
  const endSessions = (userId: string, except?: string | null) => {
    for (const [t, uid] of [...sessions]) if (uid === userId && t !== except) sessions.delete(t)
  }

  // --- validation ---
  const checkUsername = (raw: unknown): string => {
    if (typeof raw !== 'string') throw new HttpError(400, 'invalid', '缺少用户名')
    const name = raw.toLowerCase()
    if (!USERNAME_RE.test(name))
      throw new HttpError(
        400,
        'invalid',
        '用户名是 2 到 32 个字符，只能用小写字母、数字、点、下划线和连字符，且以字母或数字开头',
      )
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
    if (typeof raw !== 'string' || raw.trim() === '' || codePoints(raw.trim()) > 80)
      throw new HttpError(400, 'invalid', '名称需要 1 到 80 个字符')
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

  // --- the captcha ---
  const captchaSettings = (c: CaptchaContext) => book.security.captcha.contexts[c]
  const captchaNeeded = (c: CaptchaContext, key: string) => {
    const x = captchaSettings(c)
    return x.on && (x.when === 'always' || (captchaFails.get(`${c}:${key}`) ?? 0) >= x.failures)
  }
  const noteCaptchaFailure = (c: CaptchaContext, key: string) =>
    captchaFails.set(`${c}:${key}`, (captchaFails.get(`${c}:${key}`) ?? 0) + 1)
  // What a form sent with its request must be right when the context asked for one.
  const checkCaptcha = (c: CaptchaContext, key: string, sent: unknown) => {
    if (!captchaNeeded(c, key)) return
    const x = isRecord(sent) ? sent : {}
    const ok =
      book.security.captcha.provider === 'image'
        ? book.takeChallenge(x.id, x.answer)
        : typeof x.token === 'string' && x.token !== '' && x.token !== 'bad'
    if (!ok) throw new HttpError(400, 'captcha_failed', '验证码不对或已过期')
  }
  const captchaFor = (q: URLSearchParams): MockResponse => {
    const c = q.get('ctx') ?? ''
    if (c !== 'login' && c !== 'invite' && c !== 'setup' && c !== 'forgot' && c !== 'register')
      throw new HttpError(400, 'invalid', 'ctx 不对')
    const key = c === 'login' ? (q.get('account') ?? '').toLowerCase() : 'ip'
    // `login` asks about a name; with none given, only an `always` context is known to need one.
    if (!captchaNeeded(c, key))
      return { status: 200, headers: { 'Cache-Control': 'no-store' }, body: { required: false } }
    const provider = book.security.captcha.provider
    if (provider !== 'image')
      return {
        status: 200,
        headers: { 'Cache-Control': 'no-store' },
        body: { required: true, provider, siteKey: book.security.captcha.siteKey },
      }
    return {
      status: 200,
      headers: { 'Cache-Control': 'no-store' },
      body: { required: true, provider, siteKey: '', ...book.issue() },
    }
  }

  // --- sign in ---
  const createFirstAdmin = (body: Record<string, unknown>): MockResponse => {
    if (users.size > 0) throw new HttpError(409, 'exists', '已经有用户了')
    checkCaptcha('setup', 'ip', body.captcha)
    if (typeof body.token !== 'string' || body.token !== setupToken) {
      noteCaptchaFailure('setup', 'ip')
      throw new HttpError(403, 'forbidden', '设置令牌不对')
    }
    const username = checkUsername(body.username)
    const password = checkPassword(body.password)
    const name = body.name === undefined || body.name === '' ? username : checkName(body.name)
    const u = addUser({ username, name, role: 'admin', password }, false)
    book.record('login', { user: u.id, username, detail: { method: 'password', created: true } })
    return { status: 201, body: { user: publicUser(u) }, headers: { 'Set-Cookie': startSession(u) } }
  }

  const login = (body: Record<string, unknown>): MockResponse => {
    const username = typeof body.username === 'string' ? body.username.toLowerCase() : ''
    checkCaptcha('login', username, body.captcha)
    // Five failures in a minute against one name lock it for the rest of the minute.
    const rec = failures.get(username)
    if (rec && now() - rec.first >= 60_000) failures.delete(username)
    const live = failures.get(username)
    if (live && live.count >= 5) {
      const wait = Math.max(1, Math.ceil((live.first + 60_000 - now()) / 1000))
      book.record('login_failed', { username, detail: { method: 'password', reason: 'rate_limited' } })
      throw new HttpError(429, 'rate_limited', '尝试次数太多，请稍后再试', { __retryAfter: wait })
    }
    const u = [...users.values()].find((x) => x.username === username)
    const ok =
      !!u &&
      !u.disabled &&
      u.passwordHash !== undefined &&
      typeof body.password === 'string' &&
      u.passwordHash === hashOf(body.password)
    if (!ok) {
      failures.set(username, { count: (live?.count ?? 0) + 1, first: live?.first ?? now() })
      noteCaptchaFailure('login', username)
      book.record('login_failed', {
        user: u?.id,
        username,
        detail: { method: 'password', reason: u?.disabled ? 'disabled' : 'bad_password' },
      })
      throw new HttpError(401, 'unauthenticated', '用户名或密码不对')
    }
    // The password is right: the rules of who may use one come next.
    if (book.ssoOnlyInEffect() && u.role !== 'admin') {
      book.record('login_failed', {
        user: u.id,
        username,
        detail: { method: 'password', reason: 'local_login_refused' },
      })
      throw new HttpError(403, 'local_login_refused', '这个服务器只允许通过单点登录进入')
    }
    const required = book.requiredOf(u)
    if (required && u.passkeys.length > 0 && book.passkeyOn()) {
      book.record('login_failed', { user: u.id, username, detail: { method: 'password', reason: 'passkey_required' } })
      throw new HttpError(403, 'passkey_required', '这个账号要用 passkey 登录')
    }
    failures.delete(username)
    captchaFails.delete(`login:${username}`)
    book.record('login', { user: u.id, username, detail: { method: 'password' } })
    return {
      status: 200,
      body: { user: publicUser(u) },
      headers: { 'Set-Cookie': startSession(u, { held: required && u.passkeys.length === 0 }) },
    }
  }

  const mailBook = createMailBook({
    now,
    publicUrl,
    random: (n) => randomString(n),
    hash: hashOf,
    users: () => users.values(),
    user: (id) => users.get(id),
    isSiteAdmin: (id) => isSiteAdmin(users.get(id)),
    usernameTaken,
    checkUsername,
    checkPassword: (raw) => checkPassword(raw),
    checkName: (raw) => checkName(raw),
    checkCaptcha,
    noteCaptchaFailure,
    makeAccount: (p) => {
      const u = addUser({ username: p.username, name: p.name, role: 'member' }, false)
      u.passwordHash = p.passwordHash
      u.email = p.email
      u.emailVerified = true
      book.record('login', { user: u.id, username: u.username, detail: { method: 'password', created: true } })
      return { user: publicUser(u), cookie: startSession(u) }
    },
    setPassword: (id, hash) => {
      const u = users.get(id)
      if (u) u.passwordHash = hash
    },
    endSessions: (id) => endSessions(id),
    ssoOnlyInEffect: () => book.ssoOnlyInEffect(),
    ssoOnlyStored: () => book.security.login.ssoOnly,
    providerName: () => 'single sign-on',
    rules: () => ({
      open: tenantBook.effective('registration.open') === true,
      domains: tenantBook.effective('registration.domains') as string[],
      termsUrl: tenantBook.effective('legal.termsUrl') as string,
      privacyUrl: tenantBook.effective('legal.privacyUrl') as string,
      perHour: tenantBook.effective('registration.perHour') as number,
    }),
    record: (kind, o) => book.record(kind, o),
  })

  const checkInviteToken = (token: unknown): InviteRec => {
    const h = typeof token === 'string' ? hashOf(token) : ''
    const inv = [...invites.values()].find((i) => i.tokenHash === h)
    if (!inv || inv.used || inv.expires <= now()) {
      noteCaptchaFailure('invite', 'ip')
      throw new HttpError(404, 'not_found', '邀请不存在或已失效')
    }
    return inv
  }

  const acceptInvite = (body: Record<string, unknown>): MockResponse => {
    checkCaptcha('invite', 'ip', body.captcha)
    const inv = checkInviteToken(body.token)
    if (book.ssoOnlyInEffect() && inv.role === 'member')
      throw new HttpError(403, 'local_login_refused', '这个服务器只允许通过单点登录创建账号', { ssoOnly: true })
    // A field left empty takes what the invite fills in.
    const username = checkUsername(
      body.username === undefined || body.username === '' ? (inv.fill?.username ?? body.username) : body.username,
    )
    const password = checkPassword(body.password)
    const given = body.name === undefined || body.name === '' ? inv.fill?.name : body.name
    const name = given === undefined || given === '' ? username : checkName(given)
    if (usernameTaken(username)) throw new HttpError(409, 'exists', '这个用户名已经有人用了')
    const u = addUser({ username, name, role: inv.role, password }, false)
    if (inv.fill?.email) u.email = inv.fill.email
    inv.used = true
    book.record('login', { user: u.id, username, detail: { method: 'password', created: true } })
    return { status: 201, body: { user: publicUser(u) }, headers: { 'Set-Cookie': startSession(u) } }
  }

  // --- single sign-on (the provider is a fake that signs in one person) ---
  const ssoStart = (ctx: Ctx, rec: ProviderRec, body: Record<string, unknown>, legacy: boolean): MockResponse => {
    const intent = body.intent === undefined || body.intent === '' ? 'login' : body.intent
    if (typeof intent !== 'string' || !['login', 'setup', 'invite', 'link'].includes(intent))
      throw new HttpError(400, 'invalid', 'intent 不对')
    let userId: string | null = null
    if (intent === 'link') userId = requireUser(ctx).id
    const state = randomString(24)
    oidcAttempts.set(state, {
      provider: rec.view.slug,
      intent,
      token: typeof body.token === 'string' ? body.token : '',
      ret: plainPath(body.return),
      userId,
      created: now(),
      used: false,
    })
    const callback = legacy ? '/api/auth/oidc/callback' : `/api/auth/${rec.view.kind}/${rec.view.slug}/callback`
    const url = `${callback}?state=${state}&sub=${encodeURIComponent(rec.sub)}`
    return { status: 200, body: { url }, headers: { 'Set-Cookie': setCookie(OIDC_COOKIE, state, 600) } }
  }
  // A navigation (`GET …/start`) that answers with a redirect to the pretend identity provider.
  const ssoNavigate = (ctx: Ctx, rec: ProviderRec | undefined, q: URLSearchParams): MockResponse => {
    if (!rec || !rec.view.enabled)
      return { status: 303, headers: { Location: '/login?sso_error=provider_unavailable' } }
    const r = ssoStart(ctx, rec, { intent: 'login', return: q.get('return') ?? '/' }, false)
    return {
      status: 303,
      headers: { Location: (r.body as { url: string }).url, 'Set-Cookie': r.headers!['Set-Cookie'] },
    }
  }

  const uniqueUsername = (wanted: string) => {
    let base = wanted
      .toLowerCase()
      .replace(/[^a-z0-9._-]/g, '')
      .replace(/^[^a-z0-9]+/, '')
    if (base.length < 2) base = (base + 'user').slice(0, 32)
    base = base.slice(0, 28)
    let name = base
    for (let n = 2; usernameTaken(name); n++) name = `${base}${n}`
    return name
  }

  const ssoCallback = (ctx: Ctx, slug: string, q: URLSearchParams): MockResponse => {
    const state = q.get('state') ?? ''
    const attempt = oidcAttempts.get(state)
    const rec = book.providers.get(slug)
    const redirect = (to: string, session?: string): MockResponse => ({
      status: 303,
      headers: { Location: to, ...(session ? { 'Set-Cookie': session } : {}) },
    })
    // Nothing changes unless this browser started the attempt, it is unused and young.
    if (
      !attempt ||
      attempt.provider !== slug ||
      attempt.used ||
      ctx.cookies[OIDC_COOKIE] !== state ||
      now() - attempt.created > 600_000
    )
      return redirect('/login?sso_error=expired')
    attempt.used = true
    const fail = (code: string) => {
      book.record('login_failed', { detail: { method: rec?.view.kind ?? 'oidc', provider: slug, reason: code } })
      return redirect(attempt.intent === 'link' ? `/settings?sso_error=${code}` : `/login?sso_error=${code}`)
    }
    if (!rec || !rec.view.enabled) return fail('provider_unavailable')
    if (q.get('error')) return fail('idp_error')
    const sub = q.get('sub') ?? ''
    if (!sub) return fail('idp_error')
    const holder = [...users.values()].find((u) => u.identities.some((i) => i.sub === sub))
    const claims = { label: 'mock@example.com', username: 'mockuser', name: 'Mock User' }
    const identity = { sub, label: claims.label, provider: slug }
    const method = rec.view.kind

    switch (attempt.intent) {
      case 'login': {
        if (holder) {
          if (holder.disabled) return fail('disabled')
          book.record('login', { user: holder.id, username: holder.username, detail: { method, provider: slug } })
          return redirect(attempt.ret, startSession(holder))
        }
        // The one account with this verified address, when the provider is allowed to merge by it.
        const merge = rec.view.mergeVerifiedEmail
          ? [...users.values()].filter((u) => u.email.toLowerCase() === claims.label && u.emailVerified)
          : []
        if (merge.length === 1 && merge[0].role !== 'admin' && !merge[0].disabled) {
          merge[0].identities.push({
            id: newId(() => false),
            providerSlug: slug,
            label: claims.label,
            created: rfc3339(now()),
            sub,
          })
          book.record('identity_linked', { user: merge[0].id, detail: { provider: slug, by: 'email' } })
          return redirect(attempt.ret, startSession(merge[0]))
        }
        if (merge.length > 1 || !rec.view.autoCreate) return fail('no_account')
        const u = addUser(
          { username: uniqueUsername(claims.username), name: claims.name, role: 'member', identity },
          false,
        )
        book.record('login', { user: u.id, username: u.username, detail: { method, provider: slug, created: true } })
        return redirect(attempt.ret, startSession(u))
      }
      case 'setup': {
        if (users.size > 0 || attempt.token !== setupToken) return fail('setup_invalid')
        const u = addUser(
          { username: uniqueUsername(claims.username), name: claims.name, role: 'admin', identity },
          false,
        )
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
        const u = addUser(
          { username: uniqueUsername(claims.username), name: claims.name, role: inv.role, identity },
          false,
        )
        inv.used = true
        return redirect(attempt.ret, startSession(u))
      }
      default: {
        // link: the session must still be the one that started it.
        const me = ctx.user
        if (!me || me.id !== attempt.userId) return fail('expired')
        if (holder && holder.id !== me.id) return fail('identity_in_use')
        if (!holder) {
          me.identities.push({
            id: newId(() => false),
            providerSlug: slug,
            label: claims.label,
            created: rfc3339(now()),
            sub,
          })
          book.record('identity_linked', { user: me.id, detail: { provider: slug, by: 'user' } })
        }
        return redirect(attempt.ret)
      }
    }
  }

  // --- passkeys ---
  const b64u = (s: string) => btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  const passkeyView = (p: PasskeyRec) => ({
    id: p.id,
    name: p.name,
    created: p.created,
    lastUsed: p.lastUsed,
    synced: p.synced,
  })
  const needPasskeys = () => {
    if (!book.passkeyOn()) throw new HttpError(404, 'not_found', '没有这个接口')
  }
  const newCeremony = (c: { kind: 'login' | 'register'; username?: string; userId?: string; name?: string }) => {
    const id = randomString(24)
    ceremonies.set(id, { ...c, created: now() })
    return id
  }
  const takeCeremony = (id: string | null, kind: 'login' | 'register') => {
    const c = id ? ceremonies.get(id) : undefined
    if (id) ceremonies.delete(id)
    if (!c || c.kind !== kind || now() - c.created > 300_000)
      throw new HttpError(kind === 'login' ? 401 : 400, 'passkey_failed', 'passkey 没有通过验证')
    return c
  }
  const passkeyBegin = (body: Record<string, unknown>): MockResponse => {
    needPasskeys()
    const username = typeof body.username === 'string' ? body.username.toLowerCase() : ''
    const u = username ? [...users.values()].find((x) => x.username === username) : undefined
    const ids =
      u && u.passkeys.length > 0 ? u.passkeys.map((p) => p.credId) : username ? [b64u(`made-up:${username}`)] : null
    const options = {
      challenge: b64u(randomString(16)),
      rpId: new URL(publicUrl ?? 'http://localhost').hostname,
      timeout: 300_000,
      userVerification: 'required',
      ...(ids ? { allowCredentials: ids.map((id) => ({ type: 'public-key', id })) } : {}),
    }
    return { status: 200, body: { ceremony: newCeremony({ kind: 'login', username }), options } }
  }
  const passkeyFinish = (q: URLSearchParams, body: Record<string, unknown>): MockResponse => {
    needPasskeys()
    const c = takeCeremony(q.get('ceremony'), 'login')
    const credId = typeof body.id === 'string' ? body.id : ''
    const owner = [...users.values()].find((u) => u.passkeys.some((p) => p.credId === credId))
    const key = owner?.passkeys.find((p) => p.credId === credId)
    const fail = (reason: string) => {
      book.record('login_failed', {
        user: owner?.id,
        username: c.username || owner?.username,
        detail: { method: 'passkey', reason },
      })
      return new HttpError(401, 'passkey_failed', 'passkey 没有通过验证')
    }
    if (!owner || !key || owner.disabled || (c.username && owner.username !== c.username)) throw fail('passkey_failed')
    if (book.ssoOnlyInEffect() && owner.role !== 'admin')
      throw new HttpError(403, 'local_login_refused', '这个服务器只允许通过单点登录进入')
    key.lastUsed = rfc3339(now())
    book.record('login', { user: owner.id, username: owner.username, detail: { method: 'passkey' } })
    return {
      status: 200,
      body: { user: publicUser(owner), return: plainPath(q.get('return')) },
      headers: { 'Set-Cookie': startSession(owner) },
    }
  }
  const RECENT_MS = 600_000
  const registerBegin = (ctx: Ctx, me: UserRec, body: Record<string, unknown>): MockResponse => {
    needPasskeys()
    const name = body.name === undefined || body.name === '' ? 'Passkey' : body.name
    if (typeof name !== 'string' || Array.from(name.trim()).length < 1 || Array.from(name).length > 64)
      throw new HttpError(400, 'invalid', 'name 要 1 到 64 个字符')
    if (now() - (sessionAt.get(ctx.token ?? '') ?? 0) > RECENT_MS)
      throw new HttpError(403, 'recent_login_required', '需要刚刚登录过')
    if (me.passkeys.length >= 20) throw new HttpError(409, 'too_many_passkeys', '最多 20 个 passkey')
    const options = {
      challenge: b64u(randomString(16)),
      rp: { name: 'PetalBook', id: new URL(publicUrl ?? 'http://localhost').hostname },
      user: { id: b64u(me.id), name: me.username, displayName: me.name },
      pubKeyCredParams: [
        { type: 'public-key', alg: -7 },
        { type: 'public-key', alg: -257 },
      ],
      timeout: 300_000,
      attestation: 'none',
      authenticatorSelection: { residentKey: 'preferred', userVerification: 'required' },
      excludeCredentials: me.passkeys.map((p) => ({ type: 'public-key', id: p.credId })),
    }
    return {
      status: 200,
      body: { ceremony: newCeremony({ kind: 'register', userId: me.id, name: name.trim() }), options },
    }
  }
  const registerFinish = (ctx: Ctx, me: UserRec, q: URLSearchParams, body: Record<string, unknown>): MockResponse => {
    needPasskeys()
    const c = takeCeremony(q.get('ceremony'), 'register')
    const credId = typeof body.id === 'string' ? body.id : ''
    if (c.userId !== me.id || credId === '') throw new HttpError(400, 'passkey_failed', 'passkey 没有通过验证')
    if ([...users.values()].some((u) => u.passkeys.some((p) => p.credId === credId)))
      throw new HttpError(409, 'exists', '这个 passkey 已经登记过了')
    const rec: PasskeyRec = {
      id: newId(() => false),
      credId,
      name: c.name ?? 'Passkey',
      created: rfc3339(now()),
      lastUsed: null,
      synced: body.synced === true,
    }
    me.passkeys.push(rec)
    // The first passkey lifts the hold at once.
    if (ctx.token) held.delete(ctx.token)
    book.record('passkey_added', { actor: me.id, user: me.id, detail: { passkey: rec.id, by: 'user' } })
    return { status: 201, body: { passkeys: me.passkeys.map(passkeyView) } }
  }
  // Without a password, another identity or another passkey, nothing is left to sign in with.
  const lastWay = (u: UserRec, except: 'passkey' | 'identity') =>
    u.passwordHash === undefined &&
    u.identities.length + u.passkeys.length <= 1 &&
    (except === 'passkey' ? u.passkeys.length === 1 : u.identities.length === 1)

  // --- account ---
  const accountInfo = (u: UserRec) => ({
    user: publicUser(u),
    hasPassword: u.passwordHash !== undefined,
    identities: u.identities.map((i) => {
      const p = book.providers.get(i.providerSlug)
      return {
        id: i.id,
        provider: p?.view.name ?? 'sso.example',
        providerSlug: p ? i.providerSlug : '',
        kind: p?.view.kind ?? '',
        label: i.label,
        created: i.created,
      }
    }),
    passkeys: u.passkeys.map(passkeyView),
    groups: groupBook.inGroups(u.id),
  })

  const patchAccount = (u: UserRec, body: Record<string, unknown>) => {
    if (body.name !== undefined) u.name = checkName(body.name)
    if (body.email !== undefined) {
      if (typeof body.email !== 'string' || body.email.length > 254) throw new HttpError(400, 'invalid', '邮箱不合法')
      const email = body.email.trim()
      // Changing the address takes the "verified" away.
      if (email !== u.email) {
        mailBook.noteChange({ email: u.email, emailVerified: u.emailVerified })
        mailBook.cleanUserTokens(u.id, 'verify')
        u.emailVerified = false
      }
      u.email = email
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
    mailBook.cleanUserTokens(u.id, 'reset')
    // Other sessions end; this one stays.
    endSessions(u.id, ctx.token)
  }

  const removeIdentity = (u: UserRec, id: string) => {
    const i = u.identities.findIndex((x) => x.id === id)
    if (i < 0) throw new HttpError(404, 'not_found', '没有这个登录方式')
    if (lastWay(u, 'identity')) throw new HttpError(409, 'conflict', '这是你唯一的登录方式，移除后就无法登录了')
    book.record('identity_unlinked', {
      actor: u.id,
      user: u.id,
      detail: { provider: u.identities[i].providerSlug, by: 'user' },
    })
    u.identities.splice(i, 1)
  }

  // --- users and invites ---
  const patchUser = (me: UserRec, id: string, body: Record<string, unknown>) => {
    const u = users.get(id)
    if (!u) throw new HttpError(404, 'not_found', '没有这个用户')
    if (body.role !== undefined && body.role !== 'admin' && body.role !== 'member')
      throw new HttpError(400, 'invalid', 'role 不对')
    if (body.disabled !== undefined && typeof body.disabled !== 'boolean')
      throw new HttpError(400, 'invalid', 'disabled 要是布尔值')
    if (body.passkeyRequired !== undefined && typeof body.passkeyRequired !== 'boolean')
      throw new HttpError(400, 'invalid', 'passkeyRequired 要是布尔值')
    if (body.emailVerified !== undefined && typeof body.emailVerified !== 'boolean')
      throw new HttpError(400, 'invalid', 'emailVerified 要是布尔值')
    if (body.siteAdmin !== undefined && typeof body.siteAdmin !== 'boolean')
      throw new HttpError(400, 'invalid', 'siteAdmin 要是布尔值')
    if (body.remoteExempt !== undefined && typeof body.remoteExempt !== 'boolean')
      throw new HttpError(400, 'invalid', 'remoteExempt 要是布尔值')
    // The requirement of a remote is the site's: only a site administrator exempts anyone from it.
    if (body.remoteExempt !== undefined && !isSiteAdmin(me))
      throw new HttpError(403, 'forbidden', '只有全站管理员能免除远端要求')
    if (body.email !== undefined && (typeof body.email !== 'string' || body.email.length > 254))
      throw new HttpError(400, 'invalid', '邮箱不合法')
    // A refused quota changes nothing: it is checked before the rest is applied.
    const applyQuota = body.quota === undefined ? null : quotaBook.patch(u.id, body.quota, isSiteAdmin(me))
    const role = (body.role as Role | undefined) ?? u.role
    const disabled = (body.disabled as boolean | undefined) ?? u.disabled
    if (disabled && !u.disabled && u.id === me.id) throw new HttpError(409, 'conflict', '不能停用自己')
    const enabledAdmins = (list: UserRec[]) => list.filter((x) => x.role === 'admin' && !x.disabled).length
    const after = [...users.values()].map((x) => (x.id === u.id ? { ...x, role, disabled } : x)) as UserRec[]
    if (enabledAdmins(after) === 0) throw new HttpError(409, 'conflict', '至少要留一个可用的管理员')
    // While only single sign-on lets people in, someone with a password has to be left to fix it.
    if (
      book.ssoOnlyInEffect() &&
      after.filter((x) => x.role === 'admin' && !x.disabled && x.passwordHash !== undefined).length === 0
    )
      throw new HttpError(409, 'conflict', '至少要留一个带密码的可用管理员')
    if (body.passkeyRequired === true && !(book.security.passkeys.enabled && book.view().passkeys.available))
      throw new HttpError(400, 'passkey_unavailable', '现在不能使用 passkey')
    const changed: Record<string, unknown> = {}
    const note = (k: string, v: unknown) => (changed[k] = v)
    // A site administrator is an administrator of the whole server (3.3): only one may make or end another.
    if ((body.siteAdmin !== undefined || isSiteAdmin(u)) && !isSiteAdmin(me))
      throw new HttpError(403, 'forbidden', '只有全站管理员可以改全站管理员')
    if (body.siteAdmin === true && role !== 'admin') throw new HttpError(400, 'invalid', '全站管理员必须是管理员')
    const wasSite = isSiteAdmin(u)
    const nowSite = role === 'admin' && (body.siteAdmin !== undefined ? body.siteAdmin === true : u.siteAdmin !== false)
    if (
      wasSite &&
      !u.disabled &&
      (!nowSite || disabled) &&
      [...users.values()].filter((x) => x.id !== u.id && isSiteAdmin(x) && !x.disabled).length === 0
    )
      throw new HttpError(409, 'conflict', '至少要留一个可用的全站管理员')
    if (nowSite !== wasSite) note('siteAdmin', nowSite)
    u.siteAdmin = nowSite
    if (role !== u.role) note('role', role)
    if (disabled !== u.disabled) note('disabled', disabled)
    u.role = role
    u.disabled = disabled
    if (typeof body.remoteExempt === 'boolean') u.remoteExempt = body.remoteExempt
    if (body.passkeyRequired !== undefined && body.passkeyRequired !== u.passkeyRequired) {
      u.passkeyRequired = body.passkeyRequired as boolean
      note('passkeyRequired', u.passkeyRequired)
    }
    if (body.email !== undefined && body.email.toString().trim() !== u.email) {
      u.email = (body.email as string).trim()
      // An address set without saying it is verified is not.
      u.emailVerified = false
      note('email', u.email)
    }
    if (body.emailVerified !== undefined && body.emailVerified !== u.emailVerified) {
      u.emailVerified = body.emailVerified as boolean
      note('emailVerified', u.emailVerified)
    }
    if (disabled) endSessions(u.id)
    if (applyQuota) {
      applyQuota()
      note('quota', quotaBook.ownOf(u.id))
    }
    if (Object.keys(changed).length > 0) book.record('user_changed', { actor: me.id, user: u.id, detail: changed })
    return { user: { ...publicUser(u), quota: quotaBook.ownOf(u.id) } }
  }

  const createInvite = (me: UserRec, body: Record<string, unknown>): MockResponse => {
    const role = body.role === undefined ? 'member' : body.role
    if (role !== 'admin' && role !== 'member') throw new HttpError(400, 'invalid', 'role 不对')
    const days = body.days === undefined ? 7 : body.days
    if (typeof days !== 'number' || !Number.isInteger(days) || days < 1 || days > 30)
      throw new HttpError(400, 'invalid', 'days 要在 1 到 30 之间')
    const id = newId((x) => invites.has(x))
    const token = randomString(32)
    const rec: InviteRec = {
      id,
      tokenHash: hashOf(token),
      role,
      created: now(),
      expires: now() + days * 86_400_000,
      createdBy: me.id,
      used: false,
    }
    invites.set(id, rec)
    return { status: 201, body: { id, token, role, created: rfc3339(rec.created), expires: rfc3339(rec.expires) } }
  }
  const openInvites = (): Invite[] =>
    [...invites.values()]
      .filter((i) => !i.used && i.expires > now())
      .map((i) => ({
        id: i.id,
        role: i.role,
        created: rfc3339(i.created),
        expires: rfc3339(i.expires),
        createdBy: i.createdBy,
      }))

  // --- API tokens ---
  const TOKEN_LIMIT = 50
  const tokenState = (t: TokenRec): ApiToken['state'] =>
    t.revoked !== null ? 'revoked' : t.expires !== null && t.expires <= now() ? 'expired' : 'active'
  const publicToken = (t: TokenRec): ApiToken => {
    const owner = users.get(t.userId)
    return {
      id: t.id,
      name: t.name,
      prefix: t.prefix,
      access: t.access,
      // A space is named only while its owner is a member of it.
      spaces: t.spaces.map((id) => ({
        id,
        name: groupBook.has(spaces.get(id), t.userId) && owner ? spaces.get(id)!.name : '',
      })),
      created: rfc3339(t.created),
      expires: t.expires === null ? null : rfc3339(t.expires),
      lastUsed: null,
      revoked: t.revoked === null ? null : rfc3339(t.revoked),
      state: tokenState(t),
    }
  }
  const newestFirst = (a: TokenRec, b: TokenRec) => b.created - a.created || (a.id < b.id ? 1 : a.id > b.id ? -1 : 0)
  const createToken = (me: UserRec, body: Record<string, unknown>): MockResponse => {
    const name = typeof body.name === 'string' ? body.name.trim() : ''
    if (codePoints(name) < 1 || codePoints(name) > 80) throw new HttpError(400, 'invalid', '名称要 1 到 80 个字')
    const access = body.access === undefined || body.access === '' ? 'read' : body.access
    if (access !== 'read' && access !== 'write') throw new HttpError(400, 'invalid', 'access 要是 read 或 write')
    const days = body.expiresInDays === undefined ? 90 : body.expiresInDays
    if (typeof days !== 'number' || !Number.isInteger(days) || days < 0 || days > 365)
      throw new HttpError(400, 'invalid', 'expiresInDays 要在 1 到 365 之间，0 是永不过期')
    if (
      !Array.isArray(body.spaces) ||
      body.spaces.length < 1 ||
      body.spaces.length > 100 ||
      !body.spaces.every((x) => typeof x === 'string')
    )
      throw new HttpError(400, 'invalid', '要选 1 到 100 个空间')
    const ids = [...new Set(body.spaces as string[])]
    for (const id of ids)
      if (!groupBook.has(spaces.get(id), me.id)) throw new HttpError(404, 'not_found', `没有这个空间：${id}`)
    const live = [...apiTokens.values()].filter((t) => t.userId === me.id && tokenState(t) === 'active').length
    if (live >= TOKEN_LIMIT) throw new HttpError(400, 'token_limit', '有效的令牌已有 50 个，先吊销一个')
    const secret = 'pbk_' + randomString(43)
    const rec: TokenRec = {
      id: newId((x) => apiTokens.has(x)),
      userId: me.id,
      name,
      prefix: secret.slice(0, 12),
      access,
      spaces: ids,
      created: now(),
      expires: days === 0 ? null : now() + days * 86_400_000,
      revoked: null,
    }
    apiTokens.set(rec.id, rec)
    book.record('token_created', { actor: me.id, user: me.id, detail: { token: rec.id, name, access, spaces: ids } })
    return { status: 201, body: { token: publicToken(rec), secret } }
  }
  const revokeToken = (id: string, owner: string | null, actor: string) => {
    const t = apiTokens.get(id)
    if (!t || (owner !== null && t.userId !== owner)) throw new HttpError(404, 'not_found', '没有这个令牌')
    if (t.revoked !== null) return
    t.revoked = now()
    book.record('token_revoked', {
      actor,
      user: t.userId,
      detail: { token: t.id, name: t.name, by: owner === null ? 'admin' : 'user' },
    })
  }
  const recordGrantRevoked = (g: GrantRec, actor: string, by: 'user' | 'admin') =>
    book.record('grant_revoked', { actor, user: g.userId, detail: { grant: g.id, client: g.clientName, by } })
  const adminTokens = (url: URL): MockResponse => {
    let limit = 50
    if (url.searchParams.has('limit')) {
      const raw = url.searchParams.get('limit') ?? ''
      const n = /^\d+$/.test(raw) ? Number(raw) : NaN
      if (!(n >= 1 && n <= 200)) throw new HttpError(400, 'invalid', 'limit 要在 1 到 200 之间')
      limit = n
    }
    const user = url.searchParams.get('user')
    const after = url.searchParams.get('cursor') ?? ''
    const all = [...apiTokens.values()].filter((t) => !user || t.userId === user).sort(newestFirst)
    const from = after ? all.findIndex((t) => t.id === after) + 1 : 0
    const rest = all.slice(from)
    const page = rest.slice(0, limit)
    const items: AdminToken[] = page.map((t) => {
      const o = users.get(t.userId)
      return { ...publicToken(t), owner: { id: t.userId, username: o?.username ?? '', name: o?.name ?? '' } }
    })
    return { status: 200, body: { items, next: rest.length > limit ? page[page.length - 1].id : '' } }
  }

  // --- OAuth for MCP clients: grants and the consent page ---
  const oauthHere = () => accounts && options.oauth === true
  // The consent endpoints are off with MCP; the grants are only there with an authorization server.
  const requireOAuth = () => {
    if (!oauthHere() || !machine.mcp) throw new HttpError(404, 'not_found', 'no such endpoint')
  }
  const requireGrants = () => {
    if (!oauthHere()) throw new HttpError(404, 'not_found', 'no such endpoint')
  }
  const publicGrant = (g: GrantRec): Grant => ({
    id: g.id,
    client: { name: g.clientName, id: g.clientId },
    access: g.access,
    spaces: g.spaces.map((id) => ({ id, name: groupBook.has(spaces.get(id), g.userId) ? spaces.get(id)!.name : '' })),
    created: rfc3339(g.created),
    lastUsed: rfc3339(g.lastUsed),
  })
  const grantsOf = (userId: string | null) =>
    [...grants.values()]
      .filter((g) => userId === null || g.userId === userId)
      .sort((a, b) => b.created - a.created || (a.id < b.id ? 1 : a.id > b.id ? -1 : 0))
  const startPending = (u: UserRec, o: StartOptions): string => {
    const req = randomString(32)
    pending.set(req, {
      userId: u.id,
      clientName: o.clientName ?? 'Notes helper',
      clientId: o.clientId ?? 'pbc_mock',
      redirectUri: o.redirectUri ?? 'http://localhost:7777/callback',
      state: o.state ?? 'st4te',
      scopes: o.scopes ?? ['read', 'write'],
      expires: now() + 600_000,
    })
    return req
  }
  const takePending = (me: UserRec, id: string | null, keep: boolean): PendingRec => {
    const p = id ? pending.get(id) : undefined
    if (!id || !p || p.expires <= now() || p.userId !== me.id)
      throw new HttpError(
        404,
        'not_found',
        'this authorization request has expired, was decided already or is not yours; start again from the client',
      )
    if (!keep) pending.delete(id)
    return p
  }
  const consentInfo = (me: UserRec, p: PendingRec): ConsentInfo => ({
    client: { name: p.clientName, id: p.clientId, host: /^https:\/\/([^/:]+)/.exec(p.clientId)?.[1] ?? '' },
    redirectHost: new URL(p.redirectUri).host || p.redirectUri,
    punycode: /(^|\.)xn--/i.test(new URL(p.redirectUri).hostname),
    scopes: p.scopes,
    spaces: callerSpaces(me).map((s) => {
      const info = spaceInfo(s, me)
      return { id: s.id, name: s.name, permissions: info.permissions, personal: info.personal }
    }),
    user: { id: me.id, name: me.name },
  })
  const decideConsent = (me: UserRec, body: Record<string, unknown>): MockResponse => {
    const id = typeof body.req === 'string' ? body.req : null
    const p = takePending(me, id, true)
    const back = (params: Record<string, string>) => {
      const u = new URL(p.redirectUri)
      for (const [k, v] of Object.entries(params)) u.searchParams.set(k, v)
      if (p.state) u.searchParams.set('state', p.state)
      u.searchParams.set('iss', 'https://notes.example.test')
      return u.toString()
    }
    if (body.approve !== true) {
      pending.delete(id!)
      return {
        status: 200,
        body: { redirect: back({ error: 'access_denied', error_description: 'the user denied the request' }) },
      }
    }
    const access = body.access === undefined || body.access === '' ? 'read' : body.access
    if (access !== 'read' && access !== 'write') throw new HttpError(400, 'invalid', 'access must be read or write')
    if (access === 'write' && !p.scopes.includes('write'))
      throw new HttpError(400, 'invalid', 'the client asked for read only')
    if (
      !Array.isArray(body.spaces) ||
      body.spaces.length < 1 ||
      body.spaces.length > 100 ||
      !body.spaces.every((x) => typeof x === 'string')
    )
      throw new HttpError(400, 'invalid', 'choose 1 to 100 spaces')
    const ids = [...new Set(body.spaces as string[])]
    for (const sid of ids)
      if (!groupBook.has(spaces.get(sid), me.id))
        throw new HttpError(400, 'invalid', `you are not a member of the space ${sid}`)
    pending.delete(id!)
    // The real server makes the grant when the client exchanges the code; the mock has no client.
    const g: GrantRec = {
      id: newId((x) => grants.has(x)),
      userId: me.id,
      clientId: p.clientId,
      clientName: p.clientName,
      access,
      spaces: ids,
      created: now(),
      lastUsed: now(),
    }
    grants.set(g.id, g)
    return { status: 200, body: { redirect: back({ code: randomString(43) }) } }
  }

  // --- spaces ---
  // Opening a space (the real server's `handle`): someone who is no member, and
  // no administrator, is told it does not exist, the same as for a space that
  // does not; a disabled space opens for no one; a member without the
  // permission the route needs is refused.
  const openSpace = (u: UserRec, id: string, need: Permission): SpaceRec => {
    const s = spaces.get(id)
    if (!s || (!groupBook.has(s, u.id) && u.role !== 'admin')) throw new HttpError(404, 'not_found', '没有这个空间')
    if (s.disabled) throw new HttpError(403, 'space_disabled', 'this space has been disabled by an administrator')
    if (!spaceInfo(s, u).permissions.includes(need)) throw new HttpError(403, 'no_permission', NO_PERMISSION(need))
    return s
  }
  // What a route on a space asks of the caller (the route table of the real server).
  const needOf = (me: UserRec, method: string, rest: string): Permission => {
    // Types and their bindings are for those who manage the space; reading them is reading.
    if (rest.startsWith('/templates/') && method !== 'GET') return 'manage'
    if ((rest.startsWith('/types/') && method !== 'GET') || (rest === '/folders/type' && method === 'PUT'))
      return 'manage'
    if (
      (rest === '/daily/settings' && method === 'PUT') ||
      (rest === '/daily/prefs' && method === 'PUT') ||
      (rest === '/favorites' && method !== 'GET') ||
      rest === '/favorites/order'
    )
      return rest.startsWith('/favorites') ? 'read' : 'manage'
    if ((rest === '' && method === 'PATCH') || rest === '/reindex' || rest === '/remote' || rest.startsWith('/remote/'))
      return 'manage'
    if (rest === '/groups' || rest.startsWith('/groups/')) return 'members'
    if (rest === '/members' || rest.startsWith('/members/')) {
      if (method === 'GET') return 'read'
      // Anyone may leave: removing yourself needs no more than being a member.
      if (method === 'DELETE' && decodeURIComponent(rest.slice('/members/'.length)) === me.id) return 'read'
      return 'members'
    }
    return method === 'GET' ? 'read' : 'write'
  }

  // --- the daily text of the whole server (what administrators give) ---
  // The default that is a space is not kept here: the real server also takes a space as the default, which a
  // mock does not need. Languages are canonical BCP 47 tags, as on the server.
  interface SiteApi {
    id: string
    name: string
    lang: string
    languages?: string[]
    url: string
    headers: { name: string; value: string }[]
    text: string
    source: string
    author: string
    link: string
    langMap: Record<string, string>
    refresh: string
    refreshMinutes: number
    skipNew: boolean
    rps: number
    enabled: boolean
    builtin?: boolean
  }
  type OptionChoice = Record<string, Record<string, string[]>>
  type MockSource =
    | { kind: 'api'; id: string; options?: Record<string, string[]> }
    | { kind: 'list' }
    | { kind: 'space'; space: string; source: unknown }
    // Pages of the space asking: only a space's own list has it.
    | { kind: 'own'; source: DailySource }
  type SiteDefault =
    | null
    | { kind: 'list' }
    | { kind: 'api'; apis: Record<string, string[]>; options?: OptionChoice }
    | { kind: 'sources'; sources: Record<string, MockSource[]> }
  interface ProviderState {
    enabled: boolean
    refresh?: string
    minutes?: number
    rps?: number
    skipNew?: boolean
  }
  const tenantBook = createTenantBook({
    now: () => rfc3339(now()),
    daily: { get: () => site.allow, set: (patch) => void Object.assign(site.allow, patch) },
    isSiteAdmin: (id) => isSiteAdmin(users.get(id)),
    mailReady: () => mailBook.ready(),
    ssoOnlyStored: () => book.security.login.ssoOnly,
    onRegistrationChanged: () => mailBook.cancelClosed(),
    usageOf: () => quotaBook.tenantUsage(),
    onQuotaChanged: () => quotaBook.evaluate(),
    onTenantDeleted: (id) => github.tenantDeleted(id),
    maxSpacesFlag: options.maxSpaces,
    makeInvite: (by, tenant, fill) => {
      const id = newId((x) => invites.has(x))
      const token = randomString(32)
      const rec: InviteRec = {
        id,
        tokenHash: hashOf(token),
        role: 'admin',
        created: now(),
        expires: now() + 7 * 86_400_000,
        createdBy: by,
        used: false,
        fill,
      }
      invites.set(id, rec)
      return {
        id,
        token,
        role: 'admin',
        created: rfc3339(rec.created),
        expires: rfc3339(rec.expires),
        tenant,
        siteAdmin: false,
        ...fill,
      }
    },
  })
  // The four sync endpoints are refused when offline copies are off for the caller's tenant (offline.copies).
  const offlineAllowed = () => {
    if (tenantBook.effective('offline.copies') === false)
      throw new HttpError(403, 'offline_off', 'offline copies are turned off for this tenant')
  }
  // What people and the tenant use and may use (docs/api.md, "Quotas"); the numbers are the spaces' last measurements.
  const quotaBook = createQuotaBook({
    now,
    rfc3339: (ms) => rfc3339(ms),
    tenant: DEFAULT_TENANT,
    people: () => [...users.values()].map((u) => ({ id: u.id, username: u.username, name: u.name })),
    spaces: () =>
      [...spaces.values()].map((s) => ({
        id: s.id,
        ownerId: ownerOf(s)?.id ?? null,
        bytes: s.size?.bytes ?? 0,
        pages: s.size?.pages ?? 0,
        measuredAt: s.size?.at ?? null,
      })),
    setting: (key) => tenantBook.effective(key) as number | null,
    lowerOf: (key, site) => tenantBook.lowerFor(key, site),
    tenantQuota: () => tenantBook.quotaOf(DEFAULT_TENANT),
    record: (kind, o) => book.record(kind, o),
  })
  const site = {
    lines: new Map<string, { body: string; author: string; source: string }[]>(),
    default: null as SiteDefault,
    allow: { own: true, api: true },
    languages: ['zh-Hans', 'en'],
    apis: [] as SiteApi[],
    providers: {} as Record<string, ProviderState>,
    notices: [] as DailyNotice[],
  }
  const spaceDaily = new Map<
    string,
    { inherit: boolean; apis: string[]; options?: OptionChoice; sources?: Record<string, MockSource[]> }
  >()
  const KNOWN_LANGUAGES = [
    { tag: 'zh-Hans', name: '中文（简体）', english: 'Chinese (Simplified)' },
    { tag: 'zh-Hant', name: '中文（繁體）', english: 'Chinese (Traditional)' },
    { tag: 'en', name: 'English', english: 'English' },
    { tag: 'ja', name: '日本語', english: 'Japanese' },
    { tag: 'ko', name: '한국어', english: 'Korean' },
    { tag: 'fr', name: 'français', english: 'French' },
    { tag: 'de', name: 'Deutsch', english: 'German' },
    { tag: 'es', name: 'español', english: 'Spanish' },
    { tag: 'pt-BR', name: 'português (Brasil)', english: 'Portuguese (Brazil)' },
    { tag: 'pt-PT', name: 'português (Portugal)', english: 'Portuguese (Portugal)' },
    { tag: 'ru', name: 'русский', english: 'Russian' },
    { tag: 'it', name: 'italiano', english: 'Italian' },
    { tag: 'nl', name: 'Nederlands', english: 'Dutch' },
    { tag: 'pl', name: 'polski', english: 'Polish' },
    { tag: 'tr', name: 'Türkçe', english: 'Turkish' },
    { tag: 'uk', name: 'українська', english: 'Ukrainian' },
    { tag: 'cs', name: 'čeština', english: 'Czech' },
    { tag: 'sv', name: 'svenska', english: 'Swedish' },
    { tag: 'da', name: 'dansk', english: 'Danish' },
    { tag: 'nb', name: 'norsk bokmål', english: 'Norwegian Bokmål' },
    { tag: 'fi', name: 'suomi', english: 'Finnish' },
    { tag: 'el', name: 'Ελληνικά', english: 'Greek' },
    { tag: 'he', name: 'עברית', english: 'Hebrew' },
    { tag: 'ar', name: 'العربية', english: 'Arabic' },
    { tag: 'fa', name: 'فارسی', english: 'Persian' },
    { tag: 'hi', name: 'हिन्दी', english: 'Hindi' },
    { tag: 'bn', name: 'বাংলা', english: 'Bengali' },
    { tag: 'ta', name: 'தமிழ்', english: 'Tamil' },
    { tag: 'th', name: 'ไทย', english: 'Thai' },
    { tag: 'vi', name: 'Tiếng Việt', english: 'Vietnamese' },
    { tag: 'id', name: 'Indonesia', english: 'Indonesian' },
    { tag: 'ms', name: 'Melayu', english: 'Malay' },
    { tag: 'fil', name: 'Filipino', english: 'Filipino' },
    { tag: 'ro', name: 'română', english: 'Romanian' },
    { tag: 'hu', name: 'magyar', english: 'Hungarian' },
    { tag: 'bg', name: 'български', english: 'Bulgarian' },
    { tag: 'ca', name: 'català', english: 'Catalan' },
    { tag: 'la', name: 'Latina', english: 'Latin' },
    { tag: 'en-GB', name: 'English (United Kingdom)', english: 'English (United Kingdom)' },
    { tag: 'es-419', name: 'español (Latinoamérica)', english: 'Spanish (Latin America)' },
    { tag: 'yue-Hant', name: '粵語（繁體）', english: 'Cantonese (Traditional)' },
  ]
  const PROVIDERS = [
    {
      id: 'hitokoto',
      name: '一言',
      languages: ['zh-Hans', 'zh-Hant'],
      site: 'https://hitokoto.cn',
      note: 'Free, no key.',
      url: 'https://international.v1.hitokoto.cn/?max_length=80',
      text: 'hitokoto',
      source: 'from',
      author: 'from_who',
      link: 'https://hitokoto.cn?uuid={uuid}',
      rps: 20,
      options: [
        {
          key: 'categories',
          param: 'c',
          kind: 'multi',
          values: [
            ['a', '动画', 'Anime'],
            ['b', '漫画', 'Comics'],
            ['c', '游戏', 'Games'],
            ['d', '文学', 'Literature'],
            ['e', '原创', 'Original'],
            ['f', '来自网络', 'From the web'],
            ['g', '其他', 'Other'],
            ['h', '影视', 'Film & TV'],
            ['i', '诗词', 'Poetry'],
            ['j', '网易云', 'NetEase Music'],
            ['k', '哲学', 'Philosophy'],
            ['l', '抖机灵', 'Wit'],
          ].map(([value, zh, en]) => ({ value, label: { zh, en } })),
        },
      ],
    },
    {
      id: 'zenquotes',
      name: 'ZenQuotes',
      languages: ['en'],
      site: 'https://zenquotes.io/',
      note: 'Free, no key. Quote of the day. Its terms require a link back to ZenQuotes, which PetalBook does not show: enable it only if you accept that. 5 requests per 30 seconds per address.',
      url: 'https://zenquotes.io/api/today',
      text: '[0].q',
      source: '',
      author: '[0].a',
      link: 'https://zenquotes.io/',
      rps: 0.1,
      skipNew: false,
    },
    {
      id: 'favqs',
      name: 'FavQs',
      languages: ['en'],
      site: 'https://favqs.com/',
      note: 'Free, no key. Quote of the day.',
      url: 'https://favqs.com/api/qotd',
      text: 'quote.body',
      source: '',
      author: 'quote.author',
      link: 'https://favqs.com/quotes/{quote.author_permalink}/{quote.id}',
      rps: 1,
      skipNew: false,
    },
    {
      id: 'animechan',
      name: 'Animechan',
      languages: ['en'],
      site: 'https://animechan.io/',
      note: 'Free, no key, anime quotes; 100 requests a day per address.',
      url: 'https://api.animechan.io/v1/quotes/random',
      text: 'data.content',
      source: 'data.anime.name',
      author: 'data.character.name',
      link: '',
      rps: 1 / 60,
      skipNew: false,
    },
    {
      id: 'dummyjson',
      name: 'DummyJSON',
      languages: ['en'],
      site: 'https://dummyjson.com/docs/quotes',
      note: "Free, no key. A fixed collection of about 1,400 quotes from a service made for developers' tests; quality varies.",
      url: 'https://dummyjson.com/quotes/random',
      text: 'quote',
      source: '',
      author: 'author',
      link: '',
      rps: 1,
      skipNew: true,
    },
    {
      id: 'jinrishici',
      name: '今日诗词',
      languages: ['zh-Hans', 'zh-Hant'],
      site: 'https://www.jinrishici.com/',
      note: "Free, no key; classical Chinese poetry. Uses the service's older interface, which serves categories.",
      url: 'https://v1.jinrishici.com/all.json',
      text: 'content',
      source: 'origin',
      author: 'author',
      link: '',
      rps: 1,
      skipNew: true,
      options: [
        {
          key: 'category',
          kind: 'path',
          default: 'all',
          values: [
            ['all', '全部', 'All'],
            ['shuqing', '抒情', 'Lyrical'],
            ['shuqing/libie', '抒情-离别', '抒情-离别'],
            ['siji', '四季', 'Seasons'],
            ['rensheng', '人生', 'Life'],
          ].map(([value, zh, en]) => ({ value, label: { zh, en } })),
        },
      ],
    },
    {
      id: 'shanbay',
      name: '扇贝每日一句',
      languages: ['zh-Hans', 'en'],
      site: 'https://web.shanbay.com/op/quotes/',
      note: "Free, no key. This is the interface Shanbay's own mini program uses, not a published API, so it may change. English with a Chinese translation: each reader gets one, in their language.",
      url: 'https://apiv3.shanbay.com/weapps/dailyquote/quote/',
      text: 'content',
      source: '',
      author: 'author',
      link: 'https://web.shanbay.com/op/quotes/{assign_date}/',
      rps: 1,
      skipNew: false,
    },
    {
      id: 'iciba',
      name: '金山词霸每日一句',
      languages: ['zh-Hans', 'en'],
      site: 'https://www.iciba.com/',
      note: 'Free, no key. A daily sentence in English with a Chinese note: each reader gets one, in their language. The service labels its JSON as text/html, which is accepted for this interface only.',
      url: 'https://open.iciba.com/dsapi/',
      text: 'content',
      source: '',
      author: '',
      link: '',
      rps: 1,
      skipNew: false,
    },
  ]
  const badLang = (tag: string) => new HttpError(400, 'bad_language', `“${tag}” 不是 zh-Hans、en、pt-BR 这样的语言标签`)
  const tagOf = (raw: unknown): string => {
    const tag = typeof raw === 'string' ? canonicalTag(raw) : null
    if (tag === null) throw badLang(String(raw))
    return tag
  }
  const providerApi = (p: (typeof PROVIDERS)[number], st: ProviderState): SiteApi => ({
    id: p.id,
    name: p.name,
    lang: p.languages[0],
    languages: p.languages,
    url: p.url,
    headers: [],
    text: p.text,
    source: p.source,
    author: p.author,
    link: p.link,
    langMap: {},
    refresh: st.refresh ?? 'daily',
    refreshMinutes: st.refresh === 'minutes' ? (st.minutes ?? 0) : 0,
    skipNew: st.skipNew ?? p.skipNew ?? true,
    rps: st.rps ?? p.rps,
    enabled: true,
    builtin: true,
  })
  // The interfaces that are set up: the administrators' own, and the built-in ones that are switched on.
  const allApis = (): SiteApi[] => [
    ...site.apis,
    ...PROVIDERS.filter((p) => site.providers[p.id]?.enabled).map((p) => providerApi(p, site.providers[p.id])),
  ]
  const apiTags = (a: SiteApi): string[] => a.languages ?? (a.lang === '' ? [] : [a.lang])
  const siteView = () => {
    const byLang: Record<string, number> = {}
    let total = 0
    for (const [l, list] of site.lines) {
      if (list.length) byLang[l] = list.length
      total += list.length
    }
    return {
      default: site.default === null ? null : { ...site.default },
      languages: [...site.languages],
      knownLanguages: KNOWN_LANGUAGES,
      allow: { ...site.allow },
      apis: allApis().map(({ headers, ...rest }) => ({
        ...rest,
        headers: headers.map((h) => ({ name: h.name, set: true })),
      })),
      providers: PROVIDERS.map((p) => {
        const st = site.providers[p.id] ?? { enabled: false }
        const a = providerApi(p, st)
        return {
          id: p.id,
          name: p.name,
          languages: p.languages,
          site: p.site,
          note: p.note,
          enabled: st.enabled,
          refresh: a.refresh,
          minutes: a.refreshMinutes,
          rps: a.rps,
          maxRps: p.rps,
          skipNew: a.skipNew,
          options: p.options ?? [],
        }
      }),
      lines: { total, byLang, max: 5000 },
      notices: site.notices.map((n) => ({ ...n })),
    }
  }
  // The interfaces for a language of a space's choice, in the order it listed them: those made for the language the
  // reader's is matched to, else those for every language, else all of them.
  const chooseApis = (chosen: SiteApi[], lang: string): SiteApi[] => {
    const m = matchTag(lang, chosen.flatMap(apiTags))
    if (m !== null) return chosen.filter((a) => apiTags(a).includes(m))
    // Those that take every language; never one made for another language.
    return chosen.filter((a) => a.lang === '')
  }
  const valuesOf = (
    p: { options?: { key: string; kind: string; default?: string; values: { value: string }[] }[] } | undefined,
    chosen: unknown,
  ) => {
    // Canonical as the server keeps it: only declared values, in declared order; none or all is nothing.
    const out: Record<string, string[]> = {}
    if (chosen === undefined) return out
    if (!isRecord(chosen)) throw new HttpError(400, 'invalid', 'options are values by key')
    for (const [key, values] of Object.entries(chosen)) {
      const opt = p?.options?.find((o) => o.key === key)
      if (!opt) throw new HttpError(400, 'invalid', `没有选项 ${key}`)
      if (!Array.isArray(values)) throw new HttpError(400, 'invalid', 'values are a list')
      for (const v of values)
        if (typeof v !== 'string' || !opt.values.some((o) => o.value === v))
          throw new HttpError(400, 'invalid', `${String(v)} 不是 ${key} 的值`)
      const picked = opt.values.map((o) => o.value).filter((v) => values.includes(v))
      if (opt.kind === 'path') {
        if (picked.length > 1) throw new HttpError(400, 'invalid', `${key} 只能选一个`)
        if (picked.length === 1 && picked[0] !== opt.default) out[key] = picked
      } else if (picked.length > 0 && picked.length < opt.values.length) out[key] = picked
    }
    return out
  }
  // The options chosen for the built-in interfaces among ids, checked.
  const checkOptions = (ids: string[], given: unknown): OptionChoice => {
    const out: OptionChoice = {}
    if (given === undefined || given === null) return out
    if (!isRecord(given)) throw new HttpError(400, 'invalid', 'options are by interface id')
    for (const [id, chosen] of Object.entries(given)) {
      const p = PROVIDERS.find((x) => x.id === id)
      if (!p || !ids.includes(id) || !site.providers[id]?.enabled)
        throw new HttpError(400, 'invalid', `选项只给选了的内置接口：${id}`)
      const one = valuesOf(p, chosen)
      if (Object.keys(one).length > 0) out[id] = one
    }
    return out
  }
  const scopeNumber = (text: string) => [...text].reduce((h, c) => (h * 31 + c.charCodeAt(0)) >>> 0, 7)
  const quoteFrom = (
    api: SiteApi,
    date: string,
    skip: number,
    options?: Record<string, string[]>,
  ): Record<string, unknown> => {
    // An external interface knows only today and the days next to it: another date is no error, there is just no text.
    if (
      Math.abs(
        Date.parse(date + 'T00:00:00Z') - Date.parse(new Date(now()).toISOString().slice(0, 10) + 'T00:00:00Z'),
      ) > 86400_000
    )
      return { page: null, reason: 'out_of_range' }
    const quote: Record<string, unknown> = {
      text: `${api.name} ${date}${skip ? ` #${skip}` : ''}${options ? ` [${Object.values(options).flat().join(',')}]` : ''}`,
      ...(api.source ? { source: 'src' } : {}),
      ...(api.author ? { author: 'au' } : {}),
    }
    if (api.link) quote.link = api.link.replaceAll('{uuid}', 'u1')
    if (api.id.includes('stale')) Object.assign(quote, { stale: true, fetched: '2026-11-20T08:00:00Z' })
    return { page: null, quote, origin: 'external' }
  }
  const dailyText = (space: SpaceRec, q: URLSearchParams): Record<string, unknown> => {
    const home = space.store.home
    const { date, skip, lang } = home.checkQuery(q)
    const locked = accounts ? site.allow : { own: true, api: true } // a locked source or choice is kept, and not used
    const own = locked.own ? home.sourceOf(lang) : undefined
    if (own) return home.pick(space.id, q, own) ?? { page: null, reason: 'no_match' }
    // An interface the space chose. The mock makes no requests: the text names the interface, and an id with
    // "stale" in it plays one that did not answer.
    const apis = allApis()
    const mine = spaceDaily.get(space.id)
    const chosen = (mine?.apis ?? [])
      .map((id) => apis.find((a) => a.id === id))
      .filter((a): a is SiteApi => a !== undefined && a.enabled && locked.api)
    // What a source gives: an item (with its reason when it has none), as the server's fromSource.
    const fromLines = (): Record<string, unknown> => {
      const have = [...site.lines.keys()].filter((l) => l !== '' && (site.lines.get(l)?.length ?? 0) > 0)
      const m = matchTag(lang, have)
      // The language of the reader, or the lines for all the others; never those of another language.
      const use = m ?? ((site.lines.get('')?.length ?? 0) > 0 ? '' : undefined)
      const list = use === undefined ? [] : (site.lines.get(use) ?? [])
      if (!list.length) return { page: null }
      const line = list[pickNumber('site:' + use, date, skip, list.length)]
      return {
        page: null,
        quote: {
          text: line.body,
          ...(line.author ? { author: line.author } : {}),
          ...(line.source ? { source: line.source } : {}),
        },
        origin: 'site',
      }
    }
    const fromSource = (src: MockSource, lap: number, options: OptionChoice | undefined): Record<string, unknown> => {
      if (src.kind === 'list') return fromLines()
      if (src.kind === 'space') return { page: null } // the mock has no space to draw from
      if (src.kind === 'own') {
        const got = home.pick(space.id, q, src.source)
        const page = got?.page as { body: string; title?: string; label: string; source?: string } | undefined
        if (!page) return { page: null, reason: 'no_match' }
        return {
          page: null,
          quote: { text: page.body.trim() || page.label, ...(page.title ? { source: page.title } : {}) },
          origin: 'space',
        }
      }
      const a = apis.find((x) => x.id === src.id && x.enabled)
      // A built-in interface gives text in the languages it has only (一言 is never used for English).
      if (a?.builtin && !servesLang(a.id, lang)) return { page: null }
      return a ? quoteFrom(a, date, lap, src.options ?? options?.[a.id]) : { page: null, reason: 'unavailable' }
    }
    // Sources take turns by day, one text at a time; one with nothing is passed over for the next, and "another"
    // moves to the next. A stale answer is used only when no other has a fresh one.
    const ask = (list: MockSource[], scope: string, options?: OptionChoice): Record<string, unknown> => {
      const n = list.length
      const day = Math.floor(Date.parse(date + 'T00:00:00Z') / 86400_000)
      const first = (day + (scopeNumber(scope) % n) + skip) % n
      let stale: Record<string, unknown> | undefined
      let reason = ''
      for (let i = 0; i < n; i++) {
        const r = fromSource(list[(first + i) % n], Math.floor(skip / n), options)
        if (r.quote === undefined) {
          if (typeof r.reason === 'string' && r.reason > reason) reason = r.reason
          continue
        }
        if ((r.quote as { stale?: boolean }).stale) stale ??= r
        else return r
      }
      return stale ?? (reason ? { page: null, reason } : { page: null, reason: 'no_source' })
    }
    // The space's own list of sources, for the reader's language, without what is locked.
    if (accounts && mine?.sources) {
      const m = matchTag(
        lang,
        Object.keys(mine.sources).filter((k) => k !== ''),
      )
      const list = (m !== null ? mine.sources[m] : mine.sources['']) ?? []
      // An interface that is gone or switched off is not there: when that is all there is, nothing else is picked.
      const lost = list.some(
        (src) => src.kind === 'api' && locked.api && !apis.some((a) => a.id === src.id && a.enabled),
      )
      const usable = list.filter((src) =>
        src.kind === 'api'
          ? locked.api && apis.some((a) => a.id === src.id && a.enabled)
          : src.kind === 'own'
            ? locked.own
            : true,
      )
      if (usable.length > 0) return ask(usable, 'space:' + space.id)
      if (lost) return { page: null, reason: 'unavailable' }
    }
    if (accounts && locked.api && (mine?.apis.length ?? 0) > 0 && chosen.length === 0)
      return { page: null, reason: 'unavailable' }
    const own_ = accounts ? chooseApis(chosen, lang) : []
    if (own_.length > 0)
      return ask(
        own_.map((a): MockSource => ({ kind: 'api', id: a.id })),
        'space:' + space.id,
        mine?.options,
      )
    if (accounts && site.default !== null && (spaceDaily.get(space.id)?.inherit ?? true)) {
      const d = site.default
      let list: MockSource[] | undefined
      if (d.kind === 'list') list = [{ kind: 'list' }]
      else {
        const map: Record<string, MockSource[]> =
          d.kind === 'api'
            ? Object.fromEntries(
                Object.entries(d.apis).map(([l, ids]) => [l, ids.map((id): MockSource => ({ kind: 'api', id }))]),
              )
            : d.sources
        const m = matchTag(
          lang,
          Object.keys(map).filter((k) => k !== ''),
        )
        list = m !== null ? map[m] : map['']
      }
      if (list === undefined || list.length === 0) return { page: null, reason: 'no_source' }
      return ask(list, 'site', d.kind === 'api' ? d.options : undefined)
    }
    return { page: null, reason: 'no_source' }
  }
  // Whether a built-in interface has text in a language (matched as languages are): 一言 has Chinese only.
  const servesLang = (id: string, lang: string): boolean => {
    const have = PROVIDERS.find((p) => p.id === id)?.languages
    return have === undefined || matchTag(lang, have) !== null
  }
  // How an interface stands: set up and on, switched off, or not there at all.
  const statusOf = (id: string): 'ok' | 'disabled' | 'gone' => {
    const custom = site.apis.find((a) => a.id === id)
    if (custom) return custom.enabled ? 'ok' : 'disabled'
    if (PROVIDERS.some((p) => p.id === id)) return site.providers[id]?.enabled ? 'ok' : 'disabled'
    return 'gone'
  }
  const routeDailyPrefs = (space: SpaceRec, method: string, body: Record<string, unknown>): MockResponse => {
    const available = allApis().filter((a) => a.enabled)
    if (method === 'PUT') {
      if (typeof body.inherit !== 'boolean' || !Array.isArray(body.apis))
        throw new HttpError(400, 'invalid', '给 inherit 和 apis')
      const usable = new Set(available.map((a) => a.id))
      // What the space chose and was switched off since is kept when the page sends it back.
      const before = spaceDaily.get(space.id)
      for (const id of [
        ...(before?.apis ?? []),
        ...Object.values(before?.sources ?? {}).flatMap((l) => l.flatMap((x) => (x.kind === 'api' ? [x.id] : []))),
      ])
        if (statusOf(id) === 'disabled') usable.add(id)
      for (const id of body.apis)
        if (typeof id !== 'string' || !usable.has(id))
          throw new HttpError(400, 'invalid', `没有接口 ${String(id)}：只能用管理员设好并启用的`)
      const ids = [...new Set(body.apis as string[])]
      const options = checkOptions(ids, body.options)
      let sources: Record<string, MockSource[]> | undefined
      if (body.sources !== undefined) {
        if (!isRecord(body.sources)) throw new HttpError(400, 'invalid', 'sources are lists by language')
        sources = {}
        for (const [k, value] of Object.entries(body.sources)) {
          const tag = k === '' ? '' : tagOf(k)
          const list = Array.isArray(value) ? value : [value]
          if (list.length > 8) throw new HttpError(400, 'invalid', '每种语言最多 8 个来源')
          sources[tag] = list.map((e): MockSource => {
            if (!isRecord(e)) throw new HttpError(400, 'invalid', '来源是 {kind, …}')
            if (e.kind === 'list') {
              if (e.id !== undefined || e.options !== undefined || e.source !== undefined)
                throw new HttpError(400, 'invalid', '管理员的文案没有别的字段')
              return { kind: 'list' }
            }
            if (e.kind === 'own') {
              if (!isRecord(e.source)) throw new HttpError(400, 'invalid', '要给出 folder、tag 或 type')
              return { kind: 'own', source: { filter: [], ...e.source } as unknown as DailySource }
            }
            if (e.kind !== 'api') throw new HttpError(400, 'invalid', '来源的 kind 是 api、list 或 own')
            if (typeof e.id !== 'string' || !usable.has(e.id))
              throw new HttpError(400, 'invalid', `没有接口 ${String(e.id)}：只能用管理员设好并启用的`)
            const opts = checkOptions([e.id], e.options === undefined ? undefined : { [e.id]: e.options })[e.id]
            return { kind: 'api', id: e.id, ...(opts ? { options: opts } : {}) }
          })
        }
      }
      spaceDaily.set(space.id, {
        inherit: body.inherit,
        apis: ids,
        ...(Object.keys(options).length > 0 ? { options } : {}),
        ...(sources && Object.keys(sources).length > 0 ? { sources } : {}),
      })
    } else if (method !== 'GET') throw new HttpError(404, 'not_found', '没有这个接口')
    const now = spaceDaily.get(space.id) ?? { inherit: true, apis: [] }
    const unavailable: Record<string, string> = {}
    const kept = now.apis.filter((id) => {
      const st = statusOf(id)
      if (st !== 'ok') unavailable[id] = st
      return st !== 'gone'
    })
    const sources = now.sources
      ? Object.fromEntries(
          Object.entries(now.sources).map(([l, list]) => [
            l,
            list.flatMap((src): Record<string, unknown>[] => {
              if (src.kind !== 'api') return [src]
              const st = statusOf(src.id)
              if (st === 'ok') return [src]
              unavailable[src.id] = st
              return st === 'gone' ? [] : [{ ...src, available: false, reason: 'disabled' }]
            }),
          ]),
        )
      : undefined
    return {
      status: 200,
      body: {
        inherit: now.inherit,
        apis: kept,
        ...(sources ? { sources } : {}),
        ...(Object.keys(unavailable).length > 0 ? { unavailable } : {}),
        options: Object.fromEntries(Object.entries(now.options ?? {}).filter(([id]) => kept.includes(id))),
        available: available.map((a) => ({
          id: a.id,
          name: a.name,
          lang: a.lang,
          ...(a.builtin ? { languages: PROVIDERS.find((p) => p.id === a.id)?.languages } : {}),
          ...(a.builtin && PROVIDERS.find((p) => p.id === a.id)?.options
            ? { options: PROVIDERS.find((p) => p.id === a.id)?.options }
            : {}),
        })),
        languages: [...site.languages],
        allowed: { ...site.allow },
        default: site.default === null ? null : { kind: site.default.kind },
      },
    }
  }
  // A built-in interface is switched and tuned; what is fixed in it is not for changing.
  const putProvider = (p: (typeof PROVIDERS)[number], body: Record<string, unknown>, fromApi: boolean): void => {
    const bad = (m: string) => new HttpError(400, 'invalid', m)
    const st: ProviderState = { ...(site.providers[p.id] ?? { enabled: false }) }
    if (typeof body.enabled === 'boolean') st.enabled = body.enabled
    else if (fromApi && !st.enabled) st.enabled = true
    else if (!fromApi) throw bad('给 enabled（true 或 false）')
    if (body.refresh !== undefined && body.refresh !== '') {
      if (!['daily', 'hourly', 'minutes'].includes(String(body.refresh)))
        throw bad('refresh 是 daily、hourly 或 minutes')
      st.refresh = String(body.refresh)
      st.minutes = 0
      if (st.refresh === 'minutes') {
        const m = Number(body.minutes ?? body.refreshMinutes)
        if (!(Number.isInteger(m) && m >= 1 && m <= 1440)) throw bad('minutes 是 1 到 1440')
        st.minutes = m
      }
    }
    if (body.rps !== undefined && body.rps !== 0) {
      const r = Number(body.rps)
      if (!(r >= 0.01 && r <= p.rps)) throw bad(`rps 是 0.01 到 ${p.rps}，是 ${p.name} 允许的最多`)
      st.rps = r
    }
    if (typeof body.skipNew === 'boolean') st.skipNew = body.skipNew
    site.providers[p.id] = st
  }
  const routeSiteDaily = (method: string, path: string, url: URL, body: Record<string, unknown>): MockResponse => {
    if (path === '/admin/daily' && method === 'GET') return { status: 200, body: siteView() }
    if (path === '/admin/daily' && method === 'PUT') {
      // A `hosts` sent by an older client is ignored: there is no list of hosts.
      for (const k of Object.keys(body))
        if (!['default', 'hosts', 'languages', 'allow', 'notices'].includes(k))
          throw new HttpError(400, 'invalid', `不认识 ${k}`)
      let languages = site.languages
      if ('languages' in body) {
        if (!Array.isArray(body.languages))
          throw new HttpError(400, 'invalid', 'languages 是 zh-Hans、en 这样的语言标签的列表')
        if (body.languages.length > 30) throw new HttpError(400, 'invalid', '最多 30 种语言')
        languages = [...new Set((body.languages as unknown[]).map(tagOf))]
      }
      let allow = site.allow
      if ('allow' in body) {
        const a = body.allow
        if (
          !isRecord(a) ||
          (a.own === undefined && a.api === undefined) ||
          (a.own !== undefined && typeof a.own !== 'boolean') ||
          (a.api !== undefined && typeof a.api !== 'boolean')
        )
          throw new HttpError(400, 'invalid', 'allow 是 {own: true 或 false, api: true 或 false}')
        allow = {
          own: typeof a.own === 'boolean' ? a.own : allow.own,
          api: typeof a.api === 'boolean' ? a.api : allow.api,
        }
      }
      let next = site.default
      if ('default' in body) {
        const d = body.default
        if (d === null) next = null
        else if (
          isRecord(d) &&
          d.kind === 'list' &&
          d.space === undefined &&
          d.source === undefined &&
          d.apis === undefined
        )
          next = { kind: 'list' }
        else if (
          isRecord(d) &&
          d.kind === 'api' &&
          d.space === undefined &&
          d.source === undefined &&
          isRecord(d.apis) &&
          Object.keys(d.apis).length > 0
        ) {
          const ids = new Set(allApis().map((a) => a.id))
          const apis: Record<string, string[]> = {}
          for (const [k, value] of Object.entries(d.apis)) {
            const tag = k === '' ? '' : tagOf(k)
            if (tag in apis) throw new HttpError(400, 'invalid', `${tag} 写了两次`)
            const list = typeof value === 'string' ? [value] : value
            if (!Array.isArray(list) || list.length < 1 || list.length > 8)
              throw new HttpError(400, 'invalid', '每种语言 1 到 8 个接口')
            for (const id of list) {
              if (typeof id !== 'string' || !ids.has(id))
                throw new HttpError(400, 'api_not_found', `没有接口 ${String(id)}`)
              if (tag !== '' && !servesLang(id, tag)) throw new HttpError(400, 'invalid', `${id} 没有 ${tag} 的文案`)
            }
            apis[tag] = [...new Set(list as string[])]
          }
          const options = checkOptions([...new Set(Object.values(apis).flat())], d.options)
          next = { kind: 'api', apis, ...(Object.keys(options).length > 0 ? { options } : {}) }
        } else if (isRecord(d) && d.kind === 'sources' && isRecord(d.sources) && Object.keys(d.sources).length > 0) {
          if (d.options !== undefined)
            throw new HttpError(400, 'invalid', 'options go with a source, not with the default')
          const ids = new Set(allApis().map((a) => a.id))
          const sources: Record<string, MockSource[]> = {}
          for (const [k, value] of Object.entries(d.sources)) {
            const tag = k === '' ? '' : tagOf(k)
            if (tag in sources) throw new HttpError(400, 'invalid', `${tag} 写了两次`)
            const list = Array.isArray(value) ? value : [value]
            if (list.length < 1 || list.length > 8) throw new HttpError(400, 'invalid', '每种语言 1 到 8 个来源')
            sources[tag] = list.map((e): MockSource => {
              const src = typeof e === 'string' ? { kind: 'api', id: e } : e
              if (!isRecord(src)) throw new HttpError(400, 'invalid', '来源是 {kind, …}')
              if (src.kind === 'list') return { kind: 'list' }
              if (src.kind === 'space') return { kind: 'space', space: String(src.space), source: src.source }
              if (src.kind !== 'api') throw new HttpError(400, 'invalid', '来源的 kind 是 api、list 或 space')
              if (typeof src.id !== 'string' || !ids.has(src.id))
                throw new HttpError(400, 'api_not_found', `没有接口 ${String(src.id)}`)
              if (tag !== '' && !servesLang(src.id, tag))
                throw new HttpError(400, 'invalid', `${src.id} 没有 ${tag} 的文案`)
              const options = checkOptions([src.id], src.options === undefined ? undefined : { [src.id]: src.options })[
                src.id
              ]
              return { kind: 'api', id: src.id, ...(options ? { options } : {}) }
            })
          }
          next = { kind: 'sources', sources }
        } else
          throw new HttpError(400, 'invalid', 'the mock takes a default that is a list, interfaces or sources, or null')
      }
      // The notices are dismissed with an empty list.
      if ('notices' in body) {
        if (!Array.isArray(body.notices) || body.notices.length !== 0)
          throw new HttpError(400, 'invalid', 'notices 只能是 []（表示知道了）')
        site.notices = []
      }
      site.default = next
      site.languages = languages
      site.allow = allow
      return { status: 200, body: siteView() }
    }
    const providerOne = /^\/admin\/daily\/providers\/([^/]+)$/.exec(path)
    if (providerOne && method === 'PUT') {
      const p = PROVIDERS.find((x) => x.id === decodeURIComponent(providerOne[1]))
      if (!p) throw new HttpError(404, 'not_found', '没有这个内置接口')
      putProvider(p, body, false)
      return { status: 200, body: siteView() }
    }
    const apiOne = /^\/admin\/daily\/apis\/([^/]+)$/.exec(path)
    if (apiOne && method === 'DELETE') {
      const id = decodeURIComponent(apiOne[1])
      if (PROVIDERS.some((p) => p.id === id))
        throw new HttpError(400, 'builtin', '内置接口不能删除，请关闭它', { field: '' })
      if (!site.apis.some((a) => a.id === id)) throw new HttpError(404, 'not_found', '没有这个接口')
      site.apis = site.apis.filter((a) => a.id !== id)
      return { status: 200, body: siteView() }
    }
    if (apiOne && method === 'PUT') {
      const id = decodeURIComponent(apiOne[1])
      const old = site.apis.find((a) => a.id === id)
      const bad = (m: string) => new HttpError(400, 'invalid', m)
      if (body.id !== undefined && body.id !== id) throw bad('body 里的 id 和地址里的不一样')
      const provider = PROVIDERS.find((p) => p.id === id)
      if (provider) {
        // What is fixed must be as it is; the rest is what a provider takes.
        const fixed: [string, unknown][] = [
          ['name', provider.name],
          ['lang', provider.languages[0]],
          ['url', provider.url],
          ['text', provider.text],
          ['source', provider.source],
          ['author', provider.author],
          ['link', provider.link],
        ]
        for (const [f, want] of fixed)
          if (body[f] !== undefined && body[f] !== '' && body[f] !== want)
            throw new HttpError(400, 'builtin', `内置接口 ${id} 的 ${f} 不能改`, { field: f })
        if (Array.isArray(body.headers) && body.headers.length > 0)
          throw new HttpError(400, 'builtin', '内置接口不能有请求头', { field: 'headers' })
        if (isRecord(body.langMap) && Object.keys(body.langMap).length > 0)
          throw new HttpError(400, 'builtin', '内置接口的语言对照不能改', { field: 'langMap' })
        putProvider(
          provider,
          {
            enabled: body.enabled,
            refresh: body.refresh,
            minutes: body.refreshMinutes,
            rps: body.rps,
            skipNew: body.skipNew,
          },
          true,
        )
        return { status: 200, body: siteView() }
      }
      if (!/^[a-z0-9_-]{1,32}$/.test(id)) throw bad('接口 id 是 1 到 32 个小写字母、数字、- 或 _')
      const str = (k: string) => (typeof body[k] === 'string' ? (body[k] as string).trim() : '')
      const name = str('name')
      if (name === '' || Array.from(name).length > 100) throw bad('接口要有名字')
      const lang = str('lang') === '' ? '' : tagOf(str('lang'))
      const langMap: Record<string, string> = {}
      if (body.langMap !== undefined && body.langMap !== null) {
        if (!isRecord(body.langMap)) throw bad('langMap 是 {语言: 代码}')
        for (const [k, v] of Object.entries(body.langMap)) {
          const tag = tagOf(k)
          if (typeof v !== 'string' || !/^[A-Za-z0-9_-]{1,35}$/.test(v))
            throw bad(`langMap 里 ${tag} 的代码是 1 到 35 个字母、数字、- 或 _`)
          if (tag in langMap) throw bad(`langMap 里 ${tag} 写了两次`)
          langMap[tag] = v
        }
      }
      const url = str('url')
      let u: URL
      try {
        u = new URL(url.replaceAll('{lang}', 'en').replaceAll('{date}', '2026-01-01'))
      } catch {
        throw bad('这不是网址')
      }
      if (u.protocol !== 'https:' || u.username || u.password || u.hash || /\s/.test(url))
        throw bad('网址要是 https，不带用户名')
      const host = u.hostname.replace(/^\[|\]$/g, '')
      if (
        host === 'localhost' ||
        host.endsWith('.localhost') ||
        /^(127\.|10\.|192\.168\.|169\.254\.|0\.0\.0\.0)/.test(host) ||
        host === '::1'
      )
        throw bad('服务器不会连内网或本机地址')
      if (str('text') === '') throw bad('text 是正文的 JSON 路径，必填')
      for (const f of ['text', 'source', 'author'] as const) {
        const v = str(f)
        if (/secret/i.test(v))
          throw new HttpError(400, 'path_reveals_secret', `${f} 的路径会露出保密的内容`, { field: f })
        if (v !== '' && !/^[\w.[\]-]+$/.test(v))
          throw new HttpError(400, 'path_error', `${f} 不是合法的路径`, { field: f })
      }
      if (/\{header[.:_]/i.test(str('link'))) throw new HttpError(400, 'link_uses_header', 'link 不能用请求头')
      const refresh = str('refresh') || 'daily'
      if (!['daily', 'hourly', 'minutes'].includes(refresh)) throw bad('refresh 是 daily、hourly 或 minutes')
      const refreshMinutes = refresh === 'minutes' ? Number(body.refreshMinutes) : 0
      if (refresh === 'minutes' && !(Number.isInteger(refreshMinutes) && refreshMinutes >= 1 && refreshMinutes <= 1440))
        throw bad('refreshMinutes 是 1 到 1440')
      const rps = body.rps === undefined || body.rps === 0 ? 1 : Number(body.rps)
      if (!(rps >= 0.01 && rps <= 100)) throw bad('rps 是 0.01 到 100')
      if (body.headers !== undefined && !Array.isArray(body.headers)) throw bad('headers 是列表')
      const headers: { name: string; value: string }[] = []
      for (const h of (body.headers ?? []) as Record<string, unknown>[]) {
        const hn = typeof h.name === 'string' ? h.name : ''
        if (
          !/^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,100}$/.test(hn) ||
          ['host', 'content-length', 'transfer-encoding', 'connection', 'content-type'].includes(hn.toLowerCase())
        )
          throw bad(`${hn} 不能作请求头`)
        if (headers.some((x) => x.name.toLowerCase() === hn.toLowerCase())) throw bad(`请求头 ${hn} 写了两次`)
        let value = typeof h.value === 'string' ? h.value : undefined
        // The saved values go with the saved address: with another one they are given again.
        const kept = old?.headers.find((x) => x.name.toLowerCase() === hn.toLowerCase())
        if (value === undefined && kept && old?.url !== url)
          throw new HttpError(400, 'header_reenter', `the address changed; give the value of the header ${hn} again`, {
            header: hn,
          })
        if (value === undefined) value = kept?.value
        if (value === undefined)
          throw new HttpError(400, 'header_missing', `the header ${hn} has no value yet`, { header: hn })
        headers.push({ name: hn, value })
      }
      if (!old && site.apis.length >= 50) throw bad('最多 50 个接口')
      const made: SiteApi = {
        id,
        name,
        lang,
        url,
        headers,
        text: str('text'),
        source: str('source'),
        author: str('author'),
        link: str('link'),
        langMap,
        refresh,
        refreshMinutes,
        skipNew: body.skipNew === true,
        rps,
        enabled: typeof body.enabled === 'boolean' ? body.enabled : (old?.enabled ?? true),
      }
      site.apis = old ? site.apis.map((a) => (a.id === id ? made : a)) : [...site.apis, made]
      return { status: 200, body: siteView() }
    }
    if (path === '/admin/daily/try' && method === 'POST') {
      // The mock asks nobody: the address says what would have come back ("down": an error; a text path of
      // "missing" or "null": the path finds nothing, which is an error for the text only).
      const a = (isRecord(body.api) ? body.api : {}) as Record<string, unknown>
      const url = String(a.url ?? '')
      if (typeof body.lang === 'string' && body.lang !== '') tagOf(body.lang)
      // Like the server: a refusal with a code is a 400, the trial's output in `result`.
      const refused = (
        error: string,
        message: string,
        extra: Record<string, unknown> = {},
        result: Record<string, unknown> = {},
      ) => ({ status: 400, body: { error, message, ...extra, result: { ok: false, url, message, ...result } } })
      if (url.includes('html')) return refused('not_json', 'the answer is not JSON')
      if (url.includes('huge')) return refused('too_large', 'the answer is too large')
      if (url.includes('down')) return { status: 200, body: { ok: false, message: 'status 503', status: 503, url } }
      const raw = '{"hitokoto": "山色空蒙雨亦奇", "from": "饮湖上初晴后雨", "from_who": "苏轼"}'
      const pathErrors: { field: string; code: string; message: string }[] = []
      // Only the text is required: a source or an author whose path finds nothing (or a null, like "from_who": null)
      // is no value, not an error.
      const pick = (k: string, v: string) => {
        if (a[k] === 'missing' || a[k] === 'null') {
          if (k === 'text') pathErrors.push({ field: k, code: 'path_error', message: `no text or number at ${v}` })
          return undefined
        }
        return a[k] ? v : undefined
      }
      const quote = {
        text: pick('text', '山色空蒙雨亦奇'),
        source: pick('source', '饮湖上初晴后雨'),
        author: pick('author', '苏轼'),
      }
      const clean = Object.fromEntries(Object.entries(quote).filter(([, v]) => v !== undefined))
      if (pathErrors.length)
        return refused(
          'path_error',
          pathErrors[0].message,
          { field: pathErrors[0].field },
          { status: 200, raw, rawTruncated: false, quote: clean, pathErrors },
        )
      return { status: 200, body: { ok: true, status: 200, url, raw, rawTruncated: false, quote: clean } }
    }
    // The lines of an upload or a paste, with the author and source cut off (as the server does).
    const parseLines = (text: string, format: string): { text: string; author: string; source: string }[] => {
      const clean = text.replace(/\r\n/g, '\n')
      if (clean.includes('\0')) throw new HttpError(400, 'invalid', '有空字符')
      if (format === 'csv') {
        const rows = clean
          .split('\n')
          .filter((l) => l.trim() !== '')
          .map((l) => {
            const cells: string[] = []
            let cur = ''
            let quoted = false
            for (const ch of l) {
              if (ch === '"') quoted = !quoted
              else if (ch === ',' && !quoted) {
                cells.push(cur.trim())
                cur = ''
              } else cur += ch
            }
            cells.push(cur.trim())
            return cells
          })
        const body =
          rows.length > 0 && ['text', '文案', '正文', '内容'].includes(rows[0][0].toLowerCase()) ? rows.slice(1) : rows
        return body.filter((r) => r[0] !== '').map((r) => ({ text: r[0], author: r[1] ?? '', source: r[2] ?? '' }))
      }
      if (format !== 'lines' && format !== 'markdown')
        throw new HttpError(400, 'invalid', 'format 是 lines、markdown 或 csv')
      let parts: string[]
      if (format === 'lines') parts = clean.split('\n').filter((l) => l.trim() !== '')
      else {
        const hasRule = clean.split('\n').some((l) => l.trim() === '---')
        parts = (hasRule ? clean.split(/^\s*---\s*$/m) : clean.split(/\n\s*\n/))
          .map((x) => x.trim())
          .filter((x) => x !== '')
      }
      return parts.map((p) => {
        const [b, ...rest] = p.split('\t')
        const a = parseAttribution(b.trim())
        const tabSource = rest.join('\t').trim()
        return { text: a.text, author: a.author, source: tabSource || a.source }
      })
    }
    if (path === '/admin/daily/lines/preview' && method === 'POST') {
      const lines = parseLines(typeof body.text === 'string' ? body.text : '', String(body.format ?? 'lines'))
      return {
        status: 200,
        body: {
          lines: lines.map(({ text, author, source }) => ({
            text,
            ...(author ? { author } : {}),
            ...(source ? { source } : {}),
          })),
        },
      }
    }
    if (path === '/admin/daily/lines' && method === 'GET') {
      const raw = url.searchParams.get('lang') ?? ''
      const list = site.lines.get(raw === '' ? '' : tagOf(raw)) ?? []
      return {
        status: 200,
        body: {
          lines: list.map((l) => ({
            text: l.body,
            ...(l.author ? { author: l.author } : {}),
            ...(l.source ? { source: l.source } : {}),
          })),
        },
      }
    }
    if (path === '/admin/daily/lines' && method === 'PUT') {
      const lang = body.lang === undefined || body.lang === '' ? '' : tagOf(body.lang)
      const given = Array.isArray(body.lines)
        ? (body.lines as Record<string, unknown>[]).map((l) => ({
            text: String(l.text ?? '').trim(),
            author: String(l.author ?? '').trim(),
            source: String(l.source ?? '').trim(),
          }))
        : parseLines(typeof body.text === 'string' ? body.text : '', String(body.format ?? 'lines'))
      const parts = given.filter((l) => l.text !== '')
      if (parts.length === 0) throw new HttpError(400, 'invalid', '里面没有文字；要去掉这些行，请删除')
      const total = [...site.lines].filter(([l]) => l !== lang).reduce((n, [, v]) => n + v.length, 0) + parts.length
      if (total > 5000) throw new HttpError(400, 'invalid', '服务器一共最多保留 5000 行')
      site.lines.set(
        lang,
        parts.map((p) => ({ body: p.text, author: p.author, source: p.source })),
      )
      return { status: 200, body: siteView() }
    }
    if (path === '/admin/daily/lines' && method === 'DELETE') {
      const raw = url.searchParams.get('lang') ?? ''
      site.lines.delete(raw === '' ? '' : tagOf(raw))
      return { status: 200, body: siteView() }
    }
    throw new HttpError(404, 'not_found', '没有这个接口')
  }

  // --- members ---
  const memberOf = (s: SpaceRec, userId: string, you: string): Member => {
    const u = users.get(userId)!
    return { userId, username: u.username, name: u.name, role: s.members.get(userId)!.role, you: userId === you }
  }
  const checkRole = (raw: unknown): SpaceRole => {
    if (typeof raw !== 'string' || !ROLES.includes(raw))
      throw new HttpError(400, 'invalid', `a role is one of ${ROLES.join(', ')}`)
    return raw as SpaceRole
  }
  // A space always keeps one owner who can sign in: a disabled user cannot be the
  // one that remains (as the real server counts).
  const keepOneOwner = (s: SpaceRec, userId: string, leaving: SpaceRole) => {
    const others = [...s.members].filter(
      ([id, m]) => m.role === 'owner' && id !== userId && !users.get(id)?.disabled,
    ).length
    if (leaving === 'owner' && others < 1)
      throw new HttpError(409, 'last_owner', 'a space needs at least one owner; make someone else an owner first')
  }
  const routeMembers = (
    me: UserRec,
    s: SpaceRec,
    method: string,
    rest: string,
    body: Record<string, unknown>,
    query = '',
  ): MockResponse => {
    const j = (b: unknown, status = 200): MockResponse => ({ status, body: b })
    if (rest === '/members' && method === 'GET') {
      const list = [...s.members.keys()].map((id) => memberOf(s, id, me.id))
      list.sort((a, b) => Number(b.role === 'owner') - Number(a.role === 'owner') || compareCodePoints(a.name, b.name))
      return j(groupBook.membersView(s, me.id, list))
    }
    if (rest === '/members' && method === 'POST') {
      const role = checkRole(body.role)
      if (s.personal)
        throw new HttpError(409, 'personal_space', 'a personal space belongs to one person and cannot be shared')
      const name = typeof body.username === 'string' ? body.username.trim().toLowerCase() : ''
      const u = [...users.values()].find((x) => x.username === name)
      if (!u || u.disabled) throw new HttpError(404, 'user_not_found', 'there is no such user')
      if (s.members.has(u.id)) throw new HttpError(409, 'exists', 'that user is a member of this space already')
      s.members.set(u.id, { role, joined: ++counter })
      return j(memberOf(s, u.id, me.id), 201)
    }
    if (rest === '/members/effective' && method === 'GET')
      return groupBook.effective(s, me.id, new URL(`http://mock${rest}${query}`))
    const one = /^\/members\/([^/]+)$/.exec(rest)
    if (one) {
      const userId = decodeURIComponent(one[1])
      if (method === 'PATCH') {
        const role = checkRole(body.role)
        const cur = s.members.get(userId)
        if (!cur) throw new HttpError(404, 'not_found', 'that user is not a member of this space')
        if (cur.role !== role) {
          keepOneOwner(s, userId, cur.role)
          cur.role = role
        }
        return j(memberOf(s, userId, me.id))
      }
      if (method === 'DELETE') {
        const cur = s.members.get(userId)
        if (!cur && userId === me.id) {
          const via = groupBook.viaGroupsOnly(s, userId)
          if (via.length > 0)
            throw new HttpError(
              409,
              'member_via_group',
              `you are in this space through ${via.join(', ')}; ask a space owner to remove the group, or its administrator to remove you from it`,
            )
        }
        if (!cur) throw new HttpError(404, 'not_found', 'that user is not a member of this space')
        keepOneOwner(s, userId, cur.role)
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
      storage: {
        cache: cacheOf(s),
        pinned: s.pinned,
        exempt: s.exempt,
        lastUsed: rfc3339(s.lastUsed),
        cloneBytes: s.remote && s.cache === 'evicted' ? 0 : (s.size?.bytes ?? 0),
        indexBytes: s.size ? Math.round(s.size.bytes / 8) : 0,
        assetCacheBytes: s.assets?.cacheBytes ?? 0,
        assets: s.assets ? 'repository' : 'space',
        remote: noticeOf(s),
      },
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
        if (!Number.isInteger(n) || n < 1 || n > 200)
          throw new HttpError(400, 'invalid', 'limit is a number from 1 to 200')
        limit = n
      }
      const needle = (url.searchParams.get('q') ?? '').toLowerCase()
      const after = url.searchParams.get('cursor') ?? ''
      // In order of creation, as the real server's id order is.
      const all = [...spaces.values()]
      const from = after ? all.findIndex((s) => s.id === after) + 1 : 0
      const cache = url.searchParams.get('cache') ?? ''
      const overdue = url.searchParams.get('overdue')
      if (overdue !== null && overdue !== '0' && overdue !== '1')
        throw new HttpError(400, 'invalid', 'overdue is 1 or 0')
      const matching = all
        .slice(from)
        .filter((s) => needle === '' || s.name.toLowerCase().includes(needle))
        .filter((s) => cache === '' || cacheOf(s) === cache)
        .filter((s) => overdue !== '1' || noticeOf(s)?.overdue === true)
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
        storage: storageStatus(),
      }
      return j(status)
    }
    if (path === '/admin/storage' && method === 'GET') return j(storageBody())
    if (path === '/admin/storage' && method === 'PUT') {
      putStorage(body)
      return j(storageBody())
    }
    const one = /^\/admin\/spaces\/([^/]+)(?:\/(size|transfer|disable|enable|storage|evict))?$/.exec(path)
    if (one) {
      const id = decodeURIComponent(one[1])
      const s = adminSpaceOf(id)
      switch (one[2]) {
        case 'size':
          if (method !== 'POST') break
          s.size = { bytes: sizeOf(s), at: now(), pages: s.store.ids().length }
          quotaBook.evaluate()
          return j({ sizeBytes: s.size.bytes, sizeAt: rfc3339(s.size.at) })
        case 'transfer': {
          if (method !== 'POST') break
          if (s.personal)
            throw new HttpError(
              409,
              'personal_space',
              'a personal space belongs to one person and cannot be transferred or deleted',
            )
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
        case 'storage':
          if (method !== 'PUT') break
          if (body.pin !== undefined && typeof body.pin !== 'boolean')
            throw new HttpError(400, 'invalid', 'pin 要是布尔值')
          if (body.exempt !== undefined && typeof body.exempt !== 'boolean')
            throw new HttpError(400, 'invalid', 'exempt 要是布尔值')
          if (typeof body.pin === 'boolean') {
            s.pinned = body.pin
            if (body.pin && s.remote) s.cache = 'full'
          }
          if (typeof body.exempt === 'boolean') s.exempt = body.exempt
          return j(adminItem(s))
        case 'evict': {
          if (method !== 'POST') break
          if (!s.remote)
            throw new HttpError(409, 'conflict', '这个空间不跟随远端仓库，它是自己页面的唯一一份，不能清掉。')
          if (s.pinned) throw new HttpError(409, 'conflict', '这个空间被设为常驻。')
          if (s.cache === 'evicted') throw new HttpError(409, 'conflict', '这个空间已经清掉了。')
          if (!s.reachable)
            throw new HttpError(503, 'unavailable', '远端仓库连不上；没有确认远端有全部内容之前不清掉空间。')
          if (s.remote.state !== 'ok')
            throw new HttpError(
              409,
              'busy',
              `空间没有清掉：它的远端仓库是 ${s.remote.state}，没法核对空间里的东西是否都在远端。`,
            )
          if (s.store.draftCount() > 0) throw new HttpError(409, 'busy', '空间没有清掉：它还有没提交的草稿。')
          s.cache = 'evicted'
          return { status: 204 }
        }
        case undefined:
          if (method === 'GET') return j(adminItem(s))
          if (method !== 'DELETE') break
          if (s.personal)
            throw new HttpError(
              409,
              'personal_space',
              'a personal space belongs to one person and cannot be transferred or deleted',
            )
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

  const makeKey = (space: SpaceRec): RemoteKey => ({
    publicKey: `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI${b64(43)} petalbook-${space.id}`,
    fingerprint: `SHA256:${b64(43)}`,
  })

  const remoteInfo = (s: SpaceRec): Remote => {
    const r = s.remote!
    const c = r.credential
    let credential: Remote['credential']
    if (c.type === 'ssh') credential = { type: 'ssh', publicKey: s.key!.publicKey, fingerprint: s.key!.fingerprint }
    else if (c.type === 'token') credential = { type: 'token', username: c.username }
    else {
      const account = /^https:\/\/github\.com\/([^/]+)\//.exec(r.url)?.[1] ?? ''
      credential =
        c.via === 'app'
          ? {
              type: 'connection',
              provider: 'github',
              via: 'app',
              installationId: c.installationId,
              account,
              repoId: c.repoId,
            }
          : {
              type: 'connection',
              provider: 'github',
              via: 'user',
              user: { id: c.userId, username: users.get(c.userId)?.username ?? '' },
              login: github.connectedLogin(c.userId) ?? '',
              account,
              repoId: c.repoId,
            }
    }
    return {
      url: r.url,
      branch: r.branch,
      provider: c.type === 'github' ? 'github' : 'git',
      credential,
      state: r.state,
      ...(r.lastSync !== undefined ? { lastSync: rfc3339(r.lastSync) } : {}),
      error: r.error,
      errorCode: r.state === 'error' ? (r.errorCode ?? 'auth') : '',
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
    space.remote ??= {
      url: 'git@github.com:mock/notes.git',
      branch: 'main',
      credential: { type: 'ssh' },
      state: 'ok',
      lastSync: now(),
      error: '',
      hostKey: hostKeyOf('github.com'),
      rewritten: null,
    }
    const r = space.remote
    r.state = state
    space.reachable = state !== 'offline'
    r.error =
      patch.error ??
      (state === 'offline' ? '连接超时' : state === 'error' ? '远端拒绝了这个凭据，请检查令牌或部署密钥' : '')
    r.rewritten = state === 'rewritten' ? { lastGood: fakeCommit(), remote: fakeCommit() } : null
    if (state === 'ok') r.lastSync = now()
  }

  // `https://host/path` or an SSH address, with no password in it.
  const parseRemoteUrl = (raw: unknown): { kind: 'https' | 'ssh'; host: string } => {
    const bad = () =>
      new HttpError(400, 'invalid', '只接受 https://主机/路径 或 SSH 地址（git@主机:路径），地址里不能带密码')
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

  // `https://github.com/owner/name(.git)` or `git@github.com:owner/name.git`.
  const githubRepoOf = (raw: unknown): { owner: string; name: string } => {
    const m =
      typeof raw === 'string'
        ? /^(?:https:\/\/github\.com\/|git@github\.com:|ssh:\/\/git@github\.com\/)([A-Za-z0-9-]+)\/([A-Za-z0-9._-]+?)(?:\.git)?\/?$/.exec(
            raw.trim(),
          )
        : null
    if (!m)
      throw new HttpError(400, 'invalid', 'github 凭据要配 GitHub 上的仓库地址（https://github.com/所有者/仓库.git）')
    return { owner: m[1], name: m[2] }
  }

  const connectGitHub = (space: SpaceRec, userId: string, input: Partial<ConnectRemoteInput>): Remote => {
    const via = (input.credential as { via?: unknown } | undefined)?.via
    if (via !== 'app' && via !== 'user') throw new HttpError(400, 'invalid', 'credential.via 是 app 或 user')
    const { owner, name } = githubRepoOf(input.url)
    const found = github.existing(userId, owner, name, via)
    const branch = input.branch === undefined || input.branch === '' ? 'main' : input.branch
    if (typeof branch !== 'string' || /\s/.test(branch)) throw new HttpError(400, 'invalid', '分支名不合法')
    space.remote = {
      url: `https://github.com/${found.repo.owner}/${found.repo.name}.git`,
      branch,
      credential:
        found.via === 'app'
          ? { type: 'github', via: 'app', installationId: found.installationId, repoId: found.repo.id }
          : { type: 'github', via: 'user', userId, repoId: found.repo.id },
      state: 'ok',
      lastSync: now(),
      error: '',
      rewritten: null,
    }
    space.reachable = true
    github.repoConnected(found.repo.id) // only once everything is valid
    return remoteInfo(space)
  }

  const connectRemote = (space: SpaceRec, userId: string, body: Record<string, unknown>): Remote => {
    const input = body as unknown as Partial<ConnectRemoteInput>
    if ((input.credential as { type?: unknown } | undefined)?.type === 'connection')
      return connectGitHub(space, userId, input)
    const { kind, host } = parseRemoteUrl(input.url)
    if (!hostAllowed(host))
      throw new HttpError(403, 'forbidden', `管理员没有允许连接 ${host}。需要在服务器启动时用 --remote-host 加上它。`)
    const branch = input.branch === undefined || input.branch === '' ? 'main' : input.branch
    if (typeof branch !== 'string' || /\s/.test(branch)) throw new HttpError(400, 'invalid', '分支名不合法')
    const cred = input.credential
    if (!cred || (cred.type !== 'ssh' && cred.type !== 'token')) throw new HttpError(400, 'invalid', '缺少 credential')
    if ((cred.type === 'ssh') !== (kind === 'ssh'))
      throw new HttpError(400, 'invalid', cred.type === 'ssh' ? 'SSH 密钥要配 SSH 地址' : '令牌要配 https 地址')
    let credential: RemoteRec['credential']
    if (cred.type === 'ssh') {
      space.key ??= makeKey(space)
      credential = { type: 'ssh' }
    } else {
      const kept = space.remote?.credential.type === 'token' ? space.remote.credential : null
      const token = typeof cred.token === 'string' && cred.token !== '' ? cred.token : kept?.token
      if (!token) throw new HttpError(400, 'invalid', '缺少令牌')
      const username =
        typeof cred.username === 'string' && cred.username.trim() !== ''
          ? cred.username.trim()
          : (kept?.username ?? 'x-access-token')
      credential = { type: 'token', username, token }
    }

    // What the pretend remote is like.
    const word = /unreachable|refused|diverged|behind|ahead|empty/.exec(String(input.url))?.[0]
    const found =
      word === 'empty'
        ? 'no-branch'
        : ((word as NonNullable<MockRemoteOptions['find']> | undefined) ?? options.remote?.find ?? 'no-branch')
    if (found === 'unreachable') throw new HttpError(503, 'unavailable', `连不上 ${host}：连接超时。什么也没有改。`)
    if (found === 'refused')
      throw new HttpError(503, 'unavailable', `${host} 拒绝了这个凭据（认证失败）。什么也没有改。`)
    if (found !== 'no-branch') {
      if (space.store.pristine()) {
        // A space nothing was ever made in takes the remote's content and history.
        space.store.adopt([
          { folder: '', title: '来自远端的页面', body: '这个页面原本就在远端仓库里。\n', tags: ['远端'] },
          { folder: '笔记', title: '远端的笔记', body: '远端仓库里的第二个页面。\n' },
        ])
      } else if (found === 'diverged') {
        throw new HttpError(
          409,
          'conflict',
          '这个空间和远端仓库各自都有内容，两边的历史互不包含。PetalBook 不会合并两份历史，请为这个仓库新建一个空间。',
        )
      } else if (found === 'ahead') {
        space.store.adopt([{ folder: '', title: '远端新增的页面', body: '这个页面是在远端新增的。\n' }])
      }
    }
    space.remote = {
      url: String(input.url).trim(),
      branch,
      credential,
      state: 'ok',
      lastSync: now(),
      error: '',
      hostKey: kind === 'ssh' ? hostKeyOf(host) : undefined,
      rewritten: null,
    }
    space.reachable = true
    return remoteInfo(space)
  }

  const assetsView = (s: SpaceRec): AssetsRemote => {
    const a = s.assets!
    return {
      url: a.url,
      branch: a.branch,
      provider: a.credential.type === 'github' ? 'github' : 'git',
      credential: remoteInfo({ ...s, remote: { ...s.remote!, credential: a.credential, url: a.url } }).credential,
      state: 'ok',
      lastSync: rfc3339(now()),
      error: '',
      errorCode: '',
      rewritten: null,
      move: a.move && {
        state: a.move.state,
        files: a.move.files,
        filesDone: a.move.filesDone,
        bytes: a.move.bytes,
        bytesDone: a.move.bytesDone,
        error: a.move.error,
      },
    }
  }
  // What the mock plays for the attachment repository: connecting works for any address on an allowed host,
  // a move shows its progress one step at each look, and a repository that was used cannot be taken away.
  const routeAssets = (space: SpaceRec, method: string, rest: string, body: Record<string, unknown>): MockResponse => {
    if (!space.remote && method === 'PUT')
      throw new HttpError(409, 'remote_required', '空间要先跟随远端仓库，才能有附件仓库。', {
        require: storage.remotes.require ?? 'any',
      })
    const has = () => {
      if (!space.assets) throw new HttpError(404, 'not_found', '这个空间没有附件仓库')
      return space.assets
    }
    if (rest === '/remote/assets/move') {
      const a = has()
      if (a.move && (a.move.state === 'copying' || a.move.state === 'switching'))
        throw new HttpError(409, 'conflict', '附件正在搬')
      const files = space.files.count()
      if (files === 0) return { status: 200, body: assetsView(space) }
      const bytes = files * 48_000
      a.move = { state: 'copying', files, filesDone: 0, bytes, bytesDone: 0, error: '', step: 0 }
      a.used = true
      return { status: 202, body: assetsView(space) }
    }
    if (method === 'GET') {
      const a = has()
      const m = a.move
      if (m && (m.state === 'copying' || m.state === 'switching')) {
        m.step++
        if (m.step === 1) {
          m.filesDone = Math.floor(m.files / 2)
          m.bytesDone = Math.floor(m.bytes / 2)
        } else if (m.step === 2) m.state = 'switching'
        else {
          m.state = 'done'
          m.filesDone = m.files
          m.bytesDone = m.bytes
          a.cacheBytes = m.bytes
        }
      }
      return { status: 200, body: assetsView(space) }
    }
    if (method === 'PUT') {
      const gh = isRecord(body.sync) ? body.sync : null
      if (gh && gh.provider !== 'github')
        throw new HttpError(400, 'invalid', 'sync.provider 不是这台服务器能建仓库的提供方')
      const url = gh ? `https://github.com/${String(gh.owner)}/${String(gh.name)}.git` : String(body.url ?? '').trim()
      if (url === '') throw new HttpError(400, 'invalid', '缺少仓库地址')
      if (space.assets?.move && (space.assets.move.state === 'copying' || space.assets.move.state === 'switching'))
        throw new HttpError(409, 'conflict', '附件正在搬')
      if (space.assets && (space.assets.used || space.assets.move?.state === 'done') && space.assets.url !== url)
        throw new HttpError(409, 'conflict', '附件已经进了附件仓库，不能换成另一个')
      const credential = space.remote!.credential
      space.assets = {
        url,
        branch: 'main',
        credential,
        used: space.assets?.used ?? false,
        move: space.assets?.move ?? null,
        cacheBytes: space.assets?.cacheBytes ?? 0,
      }
      return { status: 200, body: assetsView(space) }
    }
    if (method === 'DELETE') {
      const a = has()
      if (a.used || a.move?.state === 'done')
        throw new HttpError(409, 'conflict', '附件已经进了附件仓库，不能再搬回空间仓库')
      space.assets = null
      return { status: 204 }
    }
    throw new HttpError(404, 'not_found', '没有这个接口')
  }

  const routeRemote = (
    space: SpaceRec,
    userId: string,
    method: string,
    rest: string,
    body: Record<string, unknown>,
  ): MockResponse => {
    // Accounts mode only, like the other space-management endpoints.
    accountsOnly()
    const j = (b: unknown, status = 200): MockResponse => ({ status, body: b })
    const following = () => {
      if (!space.remote) throw new HttpError(404, 'not_found', '这个空间没有连接远端仓库')
      return space.remote
    }
    // Whether a person may still manage this space as a member, directly or through a group (as the server checks
    // before using their GitHub connection; being an administrator does not count).
    const canManage = (uid: string) => {
      const u = users.get(uid)
      const { role } = groupBook.reach(space, uid)
      return !!u && !u.disabled && !!role && ROLE_PERMISSIONS[role].includes('manage')
    }
    if (rest === '/remote') {
      if (method === 'GET') {
        following()
        return j(remoteInfo(space))
      }
      if (method === 'PUT') {
        const cred = isRecord(body.credential) ? body.credential.type : undefined
        if (
          storage.remotes.require === 'github' &&
          cred !== 'github' &&
          !space.exempt &&
          !ownerExempt(space) &&
          storage.remotes.since !== null
        ) {
          throw new HttpError(409, 'remote_required', '服务器要求空间用 GitHub 仓库，请换成 GitHub 凭据。', {
            require: 'github',
          })
        }
        const info = connectRemote(space, userId, body)
        space.cache = 'partial'
        return j(info)
      }
      if (method === 'DELETE') {
        following()
        if (
          storage.remotes.require &&
          !space.exempt &&
          !ownerExempt(space) &&
          noticeOf({ ...space, remote: null }) !== null
        ) {
          throw new HttpError(409, 'remote_required', '服务器要求空间跟随远端仓库，不能移除。', {
            require: storage.remotes.require,
          })
        }
        space.remote = null
        space.reachable = true
        return { status: 204 }
      }
    }
    if (rest === '/remote/assets' || rest === '/remote/assets/move') return routeAssets(space, method, rest, body)
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
      } else if (r.credential.type === 'github' && r.credential.via === 'user' && !canManage(r.credential.userId)) {
        // Its owner may no longer manage the space (directly or through a group): their connection is not used.
        r.state = 'error'
        r.errorCode = 'connection_missing'
        r.error = '这个空间同步用的 GitHub 连接属于一个已不能管理它的人，请换一个连接'
      } else if (r.credential.type === 'github' && r.credential.via === 'user' && !github.userOf(r.credential.userId)) {
        // The connection this space syncs with has not been renewed: nothing changes until it is connected again.
        r.state = 'error'
        r.errorCode = github.connectedLogin(r.credential.userId) ? 'connection_expired' : 'connection_missing'
        r.error =
          r.errorCode === 'connection_expired'
            ? '这个空间用的 GitHub 授权已过期或被撤销，要由连接它的人重新连接'
            : '连接这个空间的人已经断开了 GitHub'
      } else {
        r.lastSync = now()
        if (r.state === 'offline' || (r.state === 'error' && isConnectionCode(r.errorCode))) {
          r.state = 'ok'
          r.error = ''
          r.errorCode = ''
        }
      }
      return j(remoteInfo(space))
    }
    if (rest === '/remote/accept' && method === 'POST') {
      const r = following()
      if (typeof body.commit !== 'string' || body.commit === '') throw new HttpError(400, 'invalid', '缺少 commit')
      if (r.state !== 'rewritten' || !r.rewritten || r.rewritten.remote !== body.commit)
        throw new HttpError(409, 'conflict', '远端已经不在这个提交上了，请刷新后再看')
      r.state = 'ok'
      r.rewritten = null
      r.error = ''
      r.lastSync = now()
      return j(remoteInfo(space))
    }
    throw new HttpError(404, 'not_found', '没有这个接口')
  }

  const routeSpace = (
    u: UserRec,
    space: SpaceRec,
    method: string,
    rest: string,
    url: URL,
    body: Record<string, unknown>,
    raw?: Uint8Array,
  ): MockResponse => {
    const st = space.store
    if (rest === '/pages') {
      if (method === 'GET') return { status: 200, body: st.listPages(url.searchParams) }
      if (method === 'POST') return { status: 201, body: st.create(body) }
    }
    if (rest === '/pages/groups' && method === 'GET') return { status: 200, body: st.groups(url.searchParams) }
    if (rest === '/views' && method === 'GET') {
      const v = url.searchParams
      return {
        status: 200,
        body: st.views(v.has('folder') ? (v.get('folder') ?? '') : undefined, v.get('tag') ?? undefined),
      }
    }
    if (rest === '/views' && method === 'PUT') return { status: 200, body: st.putViews(body) }
    if (rest === '/daily' && method === 'GET') {
      const body = dailyText(space, url.searchParams)
      // How long a browser may keep it: until the day ends where the asker is (tz, minutes east of UTC).
      const tzRaw = url.searchParams.get('tz')
      let age = 300
      if (tzRaw !== null && tzRaw !== '') {
        const tz = Number(tzRaw)
        if (!/^-?\d+$/.test(tzRaw) || tz < -840 || tz > 840)
          throw new HttpError(400, 'invalid', 'tz 是东偏 UTC 的分钟数，-840 到 840')
        const date = url.searchParams.get('date') ?? new Date(now()).toISOString().slice(0, 10)
        age = Math.max(
          60,
          Math.min(86400, Math.floor((Date.parse(date + 'T00:00:00Z') + 86400_000 - tz * 60_000 - now()) / 1000)),
        )
      }
      if (body.page === null && body.quote === undefined) age = 60
      return {
        status: 200,
        body,
        headers: {
          'Cache-Control': `private, max-age=${age}`,
          ETag: `"${gitBlobId(JSON.stringify(body)).slice(0, 24)}"`,
        },
      }
    }
    if (rest === '/daily/settings' && method === 'GET')
      return { status: 200, body: { daily: st.home.config(), inEffect: !accounts || site.allow.own } }
    if (rest === '/daily/settings' && method === 'PUT')
      return { status: 200, body: { ...st.home.setConfig(body), inEffect: !accounts || site.allow.own } }
    if (rest === '/daily/prefs') {
      accountsOnly()
      return routeDailyPrefs(space, method, body)
    }
    if (rest === '/favorites' && method === 'GET') return { status: 200, body: st.home.favorites(u.id) }
    if (rest === '/favorites' && method === 'POST') return { status: 200, body: st.home.addFavorite(u.id, body) }
    if (rest === '/favorites' && method === 'DELETE')
      return {
        status: 200,
        body: st.home.removeFavorite(u.id, url.searchParams.get('kind'), url.searchParams.get('target')),
      }
    if (rest === '/favorites/order' && method === 'PUT')
      return { status: 200, body: st.home.orderFavorites(u.id, body) }
    if (rest === '/pages/batch' && method === 'POST') return { status: 200, body: st.batch(body) }
    const hist = /^\/pages\/([^/]+)\/history$/.exec(rest)
    if (hist && method === 'GET')
      return { status: 200, body: st.history(decodeURIComponent(hist[1]), url.searchParams) }
    const bl = /^\/pages\/([^/]+)\/backlinks$/.exec(rest)
    if (bl && method === 'GET') return { status: 200, body: st.backlinks(decodeURIComponent(bl[1]), url.searchParams) }
    const ln = /^\/pages\/([^/]+)\/links$/.exec(rest)
    if (ln && method === 'GET') return { status: 200, body: st.links(decodeURIComponent(ln[1])) }
    const ver = /^\/pages\/([^/]+)\/versions\/([^/]+)$/.exec(rest)
    if (ver && method === 'GET')
      return { status: 200, body: st.version(decodeURIComponent(ver[1]), decodeURIComponent(ver[2])) }
    if (rest === '/remote' || rest.startsWith('/remote/')) return routeRemote(space, u.id, method, rest, body)
    const props = /^\/pages\/([^/]+)\/props$/.exec(rest)
    if (props && method === 'PATCH')
      return { status: 200, body: st.patchProps(u.id, decodeURIComponent(props[1]), body) }
    if (rest === '/types' && method === 'GET') {
      // Only for those who manage the space; for anyone else the key is absent.
      const notices = settingsNotices.get(space.id) ?? []
      return {
        status: 200,
        body:
          notices.length > 0 && spaceInfo(space, u).permissions.includes('manage')
            ? { ...st.types(), settingsNotices: notices }
            : st.types(),
      }
    }
    if (rest === '/checks' && method === 'GET')
      return { status: 200, body: st.checks(url.searchParams.get('kind'), url.searchParams.get('cursor')) }
    if (rest === '/checks/fix' && method === 'POST') return { status: 200, body: st.fixCheck(u.id, body) }
    if (rest === '/templates' && method === 'GET') return { status: 200, body: st.templates() }
    if (rest === '/templates/defaults' && method === 'PUT') return { status: 200, body: st.setTemplateDefault(body) }
    const tplOne = /^\/templates\/(.+)$/.exec(rest)
    if (tplOne && (method === 'GET' || method === 'PUT' || method === 'DELETE')) {
      let name: string
      try {
        name = decodeURIComponent(tplOne[1])
      } catch {
        throw new HttpError(400, 'invalid', '模板名不合法')
      }
      if (method === 'GET') return { status: 200, body: st.template(name) }
      if (method === 'DELETE') {
        st.deleteTemplate(name, url.searchParams.get('baseRev') ?? '')
        return { status: 204 }
      }
      return { status: 200, body: st.putTemplate(name, body) }
    }
    const typeOne = /^\/types\/(.+)$/.exec(rest)
    if (typeOne && (method === 'PUT' || method === 'DELETE')) {
      let name: string
      try {
        name = decodeURIComponent(typeOne[1])
      } catch {
        throw new HttpError(400, 'invalid', '类型名不合法')
      }
      if (method === 'DELETE') {
        st.deleteType(name, url.searchParams.get('baseRev') ?? '')
        return { status: 204 }
      }
      return { status: 200, body: st.putType(name, body as never) }
    }
    if (rest === '/folders/type' && method === 'PUT') {
      if (typeof body.folder !== 'string' || (body.type !== undefined && typeof body.type !== 'string'))
        throw new HttpError(400, 'invalid', '缺少 folder 或 type')
      return { status: 200, body: st.bindFolder(body.folder, (body.type as string | undefined) ?? '') }
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
    // Files: uploading one for a page, and reading any file of the space.
    const asset = /^\/pages\/([^/]+)\/assets$/.exec(rest)
    if (asset && method === 'POST') {
      const id = decodeURIComponent(asset[1])
      if (!st.peek(id)) throw new HttpError(404, 'not_found', '找不到这个页面')
      const why = blockedReason(space)
      if (why) throw new HttpError(503, 'unavailable', why)
      const name = url.searchParams.get('name') ?? ''
      if (name.trim() === '' || !(name.split(/[\\/]/).pop() ?? '').trim())
        throw new HttpError(400, 'invalid', '缺少文件名')
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
    if ((rest === '/sync/catalog' || rest === '/changes' || rest === '/sync/pages') && method === 'GET')
      offlineAllowed()
    if (rest === '/sync/catalog' && method === 'GET') return { status: 200, body: st.syncCatalog(url.searchParams) }
    if (rest === '/changes' && method === 'GET') return { status: 200, body: st.syncChanges(url.searchParams) }
    if (rest === '/sync/pages' && method === 'GET')
      return { status: 200, body: st.syncPages(u.id, url.searchParams.get('ids')) }
    throw new HttpError(404, 'not_found', '没有这个接口')
  }

  // --- GitHub (accounts mode) ---
  const routeGitHub = (
    ctx: Ctx,
    method: string,
    path: string,
    url: URL,
    body: Record<string, unknown>,
  ): MockResponse => {
    const j = (b: unknown, status = 200): MockResponse => ({ status, body: b })
    if (method === 'GET' && path === '/connections/github/authorization/callback') {
      const r = github.callback(ctx.user?.id ?? null, ctx.cookies[GITHUB_COOKIE], url.searchParams)
      return {
        status: 303,
        headers: { Location: r.to, ...(r.clear ? { 'Set-Cookie': setCookie(GITHUB_COOKIE, '', 0) } : {}) },
      }
    }
    if (method === 'GET' && path === '/connections/github/manifest/callback') {
      const r = github.manifestCallback(
        ctx.user?.id ?? null,
        isSiteAdmin(ctx.user ?? undefined),
        ctx.cookies[GITHUB_MANIFEST_COOKIE],
        url.searchParams,
      )
      return {
        status: 303,
        headers: { Location: r.to, ...(r.clear ? { 'Set-Cookie': setCookie(GITHUB_MANIFEST_COOKIE, '', 0) } : {}) },
      }
    }
    const me = requireUser(ctx)
    if (method === 'GET' && path === '/connections')
      return j({ providers: [github.info(me.id), ...forge.info(me.id), GIT_PROVIDER] })
    if (method === 'POST' && path === '/connections/github/authorization/start') {
      const r = github.start(me.id, body)
      return { status: 200, body: { url: r.url }, headers: { 'Set-Cookie': setCookie(GITHUB_COOKIE, r.state, 600) } }
    }
    if (method === 'DELETE' && path === '/connections/github/authorization') {
      github.disconnect(me.id)
      return { status: 204 }
    }
    const left = /^\/connections\/github\/leftovers\/([^/]+)$/.exec(path)
    if (left && method === 'DELETE') {
      if (!/^\d+$/.test(left[1]) || Number(left[1]) <= 0) throw new HttpError(404, 'not_found', '没有这个仓库')
      github.dismiss(me.id, Number(left[1]))
      return { status: 204 }
    }
    if (method === 'GET' && path === '/connections/github/owners')
      return j(github.owners(me.id, url.searchParams.get('cursor')))
    if (method === 'GET' && path === '/connections/github/repos') {
      const inst = url.searchParams.get('installation')
      const mine = url.searchParams.get('mine') === 'true'
      if ((inst === null) === !mine) throw new HttpError(400, 'invalid', '给 installation 或 mine=true，二选一')
      if (inst !== null && !/^\d+$/.test(inst)) throw new HttpError(400, 'invalid', 'installation 是数字')
      return j(
        github.reposOf(
          me.id,
          inst !== null ? { installation: Number(inst) } : { mine: true },
          url.searchParams.get('cursor'),
        ),
      )
    }
    throw new HttpError(404, 'not_found', '没有这个接口')
  }

  // The hosts with an OAuth application (accounts mode).
  const routeForge = (
    ctx: Ctx,
    id: string,
    method: string,
    path: string,
    url: URL,
    body: Record<string, unknown>,
  ): MockResponse => {
    const cookie = FORGE_COOKIE + id
    if (method === 'GET' && path === `/connections/${id}/authorization/callback`) {
      const r = forge.callback(id, ctx.user?.id ?? null, ctx.cookies[cookie], url.searchParams)
      return {
        status: 303,
        headers: { Location: r.to, ...(r.clear ? { 'Set-Cookie': setCookie(cookie, '', 0) } : {}) },
      }
    }
    const me = requireUser(ctx)
    if (method === 'POST' && path === `/connections/${id}/authorization/start`) {
      const r = forge.start(id, me.id, body)
      return { status: 200, body: { url: r.url }, headers: { 'Set-Cookie': setCookie(cookie, r.state, 600) } }
    }
    if (method === 'DELETE' && path === `/connections/${id}/authorization`) {
      forge.disconnect(id, me.id)
      return { status: 204 }
    }
    const left = new RegExp(`^/connections/${id}/leftovers/([^/]+)$`).exec(path)
    if (left && method === 'DELETE') {
      if (!/^\d+$/.test(left[1]) || Number(left[1]) <= 0) throw new HttpError(404, 'not_found', '没有这个仓库')
      forge.dismiss(id, me.id, Number(left[1]))
      return { status: 204 }
    }
    if (method === 'GET' && path === `/connections/${id}/owners`)
      return {
        status: 200,
        body: forge.owners(id, me.id, url.searchParams.get('cursor'), url.searchParams.get('q') ?? ''),
      }
    if (method === 'GET' && path === `/connections/${id}/repos`) {
      if (url.searchParams.get('installation') !== null)
        throw new HttpError(400, 'invalid', 'this provider has no installations', { provider: id })
      return {
        status: 200,
        body: forge.repos(id, me.id, url.searchParams.get('cursor'), url.searchParams.get('q') ?? ''),
      }
    }
    throw new HttpError(404, 'not_found', '没有这个接口')
  }

  const routeAdminForge = (id: string, method: string, path: string, body: Record<string, unknown>): MockResponse => {
    const j = (b: unknown, status = 200): MockResponse => ({ status, body: b })
    if (path === '/admin/connections' && method === 'GET') {
      const gh = github.admin()
      const providers = [
        {
          id: 'github',
          type: 'github',
          name: 'GitHub',
          host: 'github.com',
          configured: gh.configured,
          partialSync: 'yes' as const,
          connections: gh.configured ? gh.authorizations : 0,
          spaces: gh.configured ? gh.spaces : 0,
        },
        ...forge.list(),
        {
          id: 'git',
          type: 'git',
          name: 'Git',
          host: '',
          configured: true,
          partialSync: 'unknown' as const,
          connections: 0,
          spaces: 0,
        },
      ]
      return j({ providers })
    }
    if (path === '/admin/connections' && method === 'POST') return j(forge.create(body), 201)
    if (path === `/admin/connections/${id}/check` && method === 'POST') return j(forge.check(id))
    if (path === `/admin/connections/${id}` && method === 'GET') return j(forge.admin(id))
    if (path === `/admin/connections/${id}` && (method === 'PATCH' || method === 'PUT')) return j(forge.patch(id, body))
    if (path === `/admin/connections/${id}` && method === 'DELETE') {
      forge.remove(id)
      return { status: 204 }
    }
    throw new HttpError(404, 'not_found', '没有这个接口')
  }

  const routeAdminGitHub = (
    me: UserRec,
    method: string,
    path: string,
    url: URL,
    body: Record<string, unknown>,
  ): MockResponse => {
    const j = (b: unknown, status = 200): MockResponse => ({ status, body: b })
    // No App is waiting to be saved in the mock: the retry has nothing to finish.
    if (path === '/admin/connections/github/manifest/retry' && method === 'POST')
      throw new HttpError(404, 'not_found', 'no App is waiting to be saved')
    if (path === '/admin/connections/github' && method === 'GET') return j(github.admin())
    if (path === '/admin/connections/github' && method === 'PUT') {
      const str = (v: unknown, name: string) => {
        if (v !== undefined && typeof v !== 'string') throw new HttpError(400, 'invalid', `${name} 要是字符串`)
        return v as string | undefined
      }
      return j(
        github.putAdmin({
          appId: str(body.appId, 'appId'),
          privateKey: str(body.privateKey, 'privateKey'),
          clientId: str(body.clientId, 'clientId'),
          clientSecret: str(body.clientSecret, 'clientSecret'),
        }),
      )
    }
    if (path === '/admin/connections/github' && method === 'DELETE') {
      github.deleteAdmin()
      return { status: 204 }
    }
    if (path === '/admin/connections/github/check' && method === 'POST') return j(github.check())
    if (path === '/admin/connections/github/manifest' && method === 'POST') {
      const r = github.manifestStart(me.id, body)
      return { status: 200, body: r.start, headers: { 'Set-Cookie': setCookie(GITHUB_MANIFEST_COOKIE, r.state, 600) } }
    }
    if (path === '/admin/connections/github/installations' && method === 'GET')
      return j(github.installations(url.searchParams.get('cursor')))
    const inst = /^\/admin\/connections\/github\/installations\/(\d+)$/.exec(path)
    if (inst && method === 'PUT') {
      if (body.tenants === undefined)
        throw new HttpError(400, 'invalid', '给 tenants：要共享给的租户 id（不共享就是 []）')
      return j(github.share(Number(inst[1]), body.tenants))
    }
    throw new HttpError(404, 'not_found', '没有这个接口')
  }

  const route = (method: string, url: URL, body: Record<string, unknown>, ctx: Ctx, raw?: Uint8Array): MockResponse => {
    const path = url.pathname.replace(/^\/api/, '')
    const j = (b: unknown, status = 200): MockResponse => ({ status, body: b })

    // A session held until a passkey is registered may do only what leads to registering one, and sign out.
    if (accounts && ctx.user && ctx.token && held.has(ctx.token)) {
      const allowed =
        (method === 'GET' &&
          ['/session', '/meta', '/auth/providers', '/account', '/account/passkeys'].includes(path)) ||
        path.startsWith('/account/passkeys/') ||
        (method === 'POST' && path === '/logout')
      if (!allowed) throw new HttpError(403, 'passkey_required', '请先设置一个 passkey')
    }

    if (method === 'GET' && path === '/meta') {
      const u = ctx.user
      return j({ version: 'mock', maxUpload, maxImport, spaces: u ? callerSpaces(u).map((s) => spaceInfo(s, u)) : [] })
    }
    if (method === 'GET' && path === '/session') {
      const base = {
        mode,
        user: ctx.user ? publicUser(ctx.user) : null,
        setup: accounts && users.size === 0,
        hold: accounts && ctx.token && ctx.user && held.has(ctx.token) ? ('passkey' as const) : null,
        token: null,
      }
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
      const target = [...spaces.values()].find(
        (s) =>
          s.id === (url.searchParams.get('space') ?? '') || (!url.searchParams.get('space') && s.store.peek(touch[1])),
      )
      if (!target) throw new HttpError(404, 'not_found', '没有这个页面')
      return j(target.store.touch(touch[1], body))
    }

    // Mock only: a person is connected to a host without the round trip, or their connection ran out, or the host goes down.
    if (method === 'POST' && path === '/_mock/forge') {
      const uid = typeof body.user === 'string' ? body.user : (ctx.user?.id ?? '')
      const id = typeof body.provider === 'string' ? body.provider : ''
      if (typeof body.connect === 'string') forge.connect(id, uid, body.connect, body.admin === true)
      if (body.expire === true) forge.expire(id, uid)
      if (typeof body.down === 'boolean') forge.setDown(id, body.down)
      return j({})
    }

    // Mock only: a person's GitHub connection ran out (their spaces that sync through it go to error), or a trap for the next creation.
    if (method === 'POST' && path === '/_mock/github') {
      const uid = typeof body.user === 'string' ? body.user : (ctx.user?.id ?? '')
      if (typeof body.connect === 'string') github.connect(uid, body.connect)
      if (body.expire === true) {
        github.expire(uid)
        for (const sp of spaces.values()) {
          const c = sp.remote?.credential
          if (sp.remote && c?.type === 'github' && c.via === 'user' && c.userId === uid) {
            sp.remote.state = 'error'
            sp.remote.errorCode = 'connection_expired'
            sp.remote.error = '这个空间用的 GitHub 授权已过期或被撤销，要由连接它的人重新连接'
          }
        }
      }
      if (
        typeof body.trap === 'string' &&
        (body.trap === 'noPermission' ||
          body.trap === 'notInInstallation' ||
          body.trap === 'pushFails' ||
          body.trap === 'missingPermission' ||
          body.trap === 'personalNeedsInstall')
      )
        github.setTrap(body.trap, body.on !== false)
      // What GitHub says about the App now: the administration sees it after a re-check.
      if (isRecord(body.side))
        github.setSide({
          permissions: isRecord(body.side.permissions) ? (body.side.permissions as Record<string, string>) : undefined,
          gaps: Array.isArray(body.side.gaps) ? (body.side.gaps as GitHubGap[]) : undefined,
          anyAccount:
            typeof body.side.anyAccount === 'boolean' || body.side.anyAccount === null
              ? (body.side.anyAccount as boolean | null)
              : undefined,
        })
      return j({})
    }

    // Mock only: an account that single sign-on made has no password; this takes one away.
    if (method === 'POST' && path === '/_mock/drop-password') {
      const target = [...users.values()].find((u) => u.username === body.username)
      if (!target) throw new HttpError(404, 'not_found', '没有这个用户')
      target.passwordHash = undefined
      return j({})
    }

    // Mock only: put a space's remote in a state, to see how the app shows it.
    if (method === 'POST' && path === '/_mock/remote') {
      const target = spaces.get(url.searchParams.get('space') ?? '') ?? [...spaces.values()][0]
      if (!target) throw new HttpError(404, 'not_found', '没有这个空间')
      const state = body.state
      if (state !== 'local' && state !== 'ok' && state !== 'offline' && state !== 'rewritten' && state !== 'error')
        throw new HttpError(400, 'invalid', 'state 不对')
      setRemoteState(target, state, { error: typeof body.error === 'string' ? body.error : undefined })
      return j(spaceInfo(target, ownerOf(target) ?? [...users.values()][0]))
    }

    // Mock only: what an MCP client's link to /oauth/authorize does, for looking at the consent page by hand.
    if (path === '/_mock/oauth' && method === 'GET') {
      accountsOnly()
      requireOAuth()
      return { status: 303, headers: { Location: `/oauth/consent?req=${startPending(requireUser(ctx), {})}` } }
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
    if (path === '/auth/providers' && method === 'GET') {
      accountsOnly()
      const mode = book.effectiveMode()
      const list = book.publicProviders()
      return {
        status: 200,
        headers: { 'Cache-Control': 'no-store' },
        body: {
          providers: list,
          loginMode: mode,
          local: mode !== 'sso_redirect',
          sso: mode !== 'local_only' && list.length > 0,
          passkey: book.passkeyOn(),
          register: mailBook.open(),
          forgot: mailBook.ready(),
        },
      }
    }
    if (path === '/captcha' && method === 'GET') {
      accountsOnly()
      return captchaFor(url.searchParams)
    }
    if (path === '/auth/passkey/begin' && method === 'POST') {
      accountsOnly()
      return passkeyBegin(body)
    }
    if (path === '/auth/passkey/finish' && method === 'POST') {
      accountsOnly()
      return passkeyFinish(url.searchParams, body)
    }
    // The endpoints from before 2.8 serve the provider marked `legacyCallback`.
    const legacy = [...book.providers.values()].find((p) => p.view.oidc?.legacyCallback)
    if (path === '/auth/oidc/start' && method === 'POST') {
      accountsOnly()
      if (!legacy || !legacy.view.enabled) throw new HttpError(404, 'not_found', '没有配置单点登录')
      return ssoStart(ctx, legacy, body, true)
    }
    if (path === '/auth/oidc/callback' && method === 'GET') {
      accountsOnly()
      return ssoCallback(ctx, legacy?.view.slug ?? '', url.searchParams)
    }
    const sso = /^\/auth\/(oidc|saml|github)\/([^/]+)\/(start|callback|acs|metadata)$/.exec(path)
    if (sso) {
      accountsOnly()
      const rec = book.providers.get(decodeURIComponent(sso[2]))
      const known = rec && rec.view.kind === sso[1] ? rec : undefined
      if (sso[3] === 'start' && method === 'GET') return ssoNavigate(ctx, known, url.searchParams)
      if (sso[3] === 'start' && method === 'POST') {
        if (!known || !known.view.enabled) throw new HttpError(404, 'not_found', '没有这个登录方式')
        return ssoStart(ctx, known, body, false)
      }
      if ((sso[3] === 'callback' && method === 'GET') || (sso[3] === 'acs' && method === 'POST'))
        return ssoCallback(ctx, known?.view.slug ?? '', url.searchParams)
      if (sso[3] === 'metadata' && method === 'GET' && known?.view.saml)
        return { status: 200, body: { entityId: known.view.saml.entityId } }
      throw new HttpError(404, 'not_found', '没有这个接口')
    }
    if (path === '/invites/check' && method === 'POST') {
      accountsOnly()
      checkCaptcha('invite', 'ip', body.captcha)
      const inv = checkInviteToken(body.token)
      return j({
        role: inv.role,
        expires: rfc3339(inv.expires),
        username: inv.fill?.username ?? '',
        name: inv.fill?.name ?? '',
        email: inv.fill?.email ?? '',
      })
    }
    if (path === '/invites/accept' && method === 'POST') {
      accountsOnly()
      return acceptInvite(body)
    }

    // --- mail, registration, email verification and password reset (3.3) ---
    if (path === '/admin/mail' || path === '/admin/mail/test') {
      accountsOnly()
      const me = requireAdmin(ctx)
      if (!isSiteAdmin(me)) throw new HttpError(403, 'forbidden', '只有全站管理员能改发信设置')
      if (path === '/admin/mail' && method === 'GET') return j(mailBook.view())
      if (path === '/admin/mail' && method === 'PUT') {
        const r = mailBook.put(body)
        book.record('mail_changed', {
          detail: { keys: Object.keys(body).map((k) => (k === 'password' ? 'password' : k)) },
        })
        return j(r)
      }
      if (method === 'POST') return mailBook.test(body)
    }
    if (path === '/register' && method === 'GET') {
      accountsOnly()
      return { status: 200, headers: { 'Cache-Control': 'no-store' }, body: mailBook.registerInfo() }
    }
    if (path === '/register' && method === 'POST') {
      accountsOnly()
      return mailBook.register(body)
    }
    if (path === '/register/confirm' && method === 'POST') {
      accountsOnly()
      return mailBook.registerConfirm(body)
    }
    if (path === '/account/email/verify' && method === 'POST') {
      accountsOnly()
      return mailBook.requestVerify(requireUser(ctx))
    }
    if (path === '/email/confirm' && method === 'POST') {
      accountsOnly()
      return mailBook.confirmEmail(body, (id) => {
        const u = users.get(id)
        if (u) u.emailVerified = true
      })
    }
    if (path === '/password/forgot' && method === 'POST') {
      accountsOnly()
      return mailBook.forgot(body)
    }
    if (path === '/password/reset/check' && method === 'POST') {
      accountsOnly()
      return mailBook.resetCheck(body)
    }
    if (path === '/password/reset' && method === 'POST') {
      accountsOnly()
      return mailBook.reset(body)
    }
    const adminReset = /^\/users\/([^/]+)\/password-reset$/.exec(path)
    if (adminReset && method === 'POST') {
      accountsOnly()
      return mailBook.adminReset(requireAdmin(ctx), decodeURIComponent(adminReset[1]))
    }

    // --- account ---
    if (
      path === '/account' ||
      path === '/account/password' ||
      path.startsWith('/account/identities/') ||
      path.startsWith('/account/passkeys') ||
      path === '/account/tokens' ||
      path.startsWith('/account/tokens/')
    ) {
      accountsOnly()
      const me = requireUser(ctx)
      if (path === '/account/tokens' && method === 'GET')
        return j({
          tokens: [...apiTokens.values()]
            .filter((t) => t.userId === me.id)
            .sort(newestFirst)
            .map(publicToken),
        })
      if (path === '/account/tokens' && method === 'POST') return createToken(me, body)
      const tok = /^\/account\/tokens\/([^/]+)$/.exec(path)
      if (tok && method === 'DELETE') {
        revokeToken(decodeURIComponent(tok[1]), me.id, me.id)
        return { status: 204 }
      }
      if (path === '/account' && method === 'GET') return j(accountInfo(me))
      if (path === '/account' && method === 'PATCH') return j(patchAccount(me, body))
      if (path === '/account/password' && method === 'POST') {
        changePassword(ctx, me, body)
        return { status: 204 }
      }
      if (path === '/account/passkeys' && method === 'GET') return j({ passkeys: me.passkeys.map(passkeyView) })
      if (path === '/account/passkeys/begin' && method === 'POST') return registerBegin(ctx, me, body)
      if (path === '/account/passkeys/finish' && method === 'POST')
        return registerFinish(ctx, me, url.searchParams, body)
      const pk = /^\/account\/passkeys\/([^/]+)$/.exec(path)
      if (pk && method === 'PATCH') {
        const rec = me.passkeys.find((x) => x.id === decodeURIComponent(pk[1]))
        if (!rec) throw new HttpError(404, 'not_found', '没有这个 passkey')
        if (
          typeof body.name !== 'string' ||
          Array.from(body.name.trim()).length < 1 ||
          Array.from(body.name).length > 64
        )
          throw new HttpError(400, 'invalid', 'name 要 1 到 64 个字符')
        rec.name = body.name.trim()
        return j(passkeyView(rec))
      }
      if (pk && method === 'DELETE') {
        const at = me.passkeys.findIndex((x) => x.id === decodeURIComponent(pk[1]))
        if (at < 0) throw new HttpError(404, 'not_found', '没有这个 passkey')
        if (lastWay(me, 'passkey')) throw new HttpError(409, 'conflict', '这是你唯一的登录方式')
        book.record('passkey_removed', {
          actor: me.id,
          user: me.id,
          detail: { passkey: me.passkeys[at].id, by: 'user' },
        })
        me.passkeys.splice(at, 1)
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
      if (path === '/users' && method === 'GET') {
        let list = [...users.values()].sort((a, b) => a.created.localeCompare(b.created))
        // Without parameters, everyone; `q` (username or name contains it, ignoring case), `enabled=1` and `limit` narrow it.
        const q = url.searchParams.get('q')
        const enabled = url.searchParams.get('enabled')
        if (enabled !== null && enabled !== '1' && enabled !== '0')
          throw new HttpError(400, 'invalid', 'enabled is 1 or 0')
        if (enabled === '1') list = list.filter((u) => !u.disabled)
        if (q !== null && q.trim() !== '') {
          const needle = q.trim().toLowerCase()
          list = list.filter((u) => u.username.toLowerCase().includes(needle) || u.name.toLowerCase().includes(needle))
        }
        if (url.searchParams.has('limit')) {
          const raw = url.searchParams.get('limit') ?? ''
          const n = /^\d+$/.test(raw) ? Number(raw) : NaN
          if (!(n >= 1 && n <= 200)) throw new HttpError(400, 'invalid', 'limit is a number from 1 to 200')
          list = list.slice(0, n)
        }
        return j({ users: list.map(listedUser) })
      }
      const user = /^\/users\/([^/]+)$/.exec(path)
      if (user && method === 'PATCH') return j(patchUser(me, decodeURIComponent(user[1]), body))
      const userKeys = /^\/users\/([^/]+)\/passkeys(?:\/([^/]+))?$/.exec(path)
      if (userKeys) {
        const target = users.get(decodeURIComponent(userKeys[1]))
        if (!target) throw new HttpError(404, 'not_found', '没有这个用户')
        if (!userKeys[2] && method === 'GET') return j({ passkeys: target.passkeys.map(passkeyView) })
        if (userKeys[2] && method === 'DELETE') {
          const at = target.passkeys.findIndex((x) => x.id === decodeURIComponent(userKeys[2]))
          if (at < 0) throw new HttpError(404, 'not_found', '没有这个 passkey')
          book.record('passkey_removed', {
            actor: me.id,
            user: target.id,
            detail: { passkey: target.passkeys[at].id, by: 'admin' },
          })
          target.passkeys.splice(at, 1)
          return { status: 204 }
        }
      }
      if (path === '/invites' && method === 'POST') return createInvite(me, body)
      if (path === '/invites' && method === 'GET') return j({ invites: openInvites() })
      const inv = /^\/invites\/([^/]+)$/.exec(path)
      if (inv && method === 'DELETE') {
        if (!invites.delete(decodeURIComponent(inv[1]))) throw new HttpError(404, 'not_found', '没有这个邀请')
        return { status: 204 }
      }
    }

    if (path === '/account/grants' || path.startsWith('/account/grants/')) {
      accountsOnly()
      const me = requireUser(ctx)
      requireGrants()
      if (path === '/account/grants' && method === 'GET') return j({ grants: grantsOf(me.id).map(publicGrant) })
      const gr = /^\/account\/grants\/([^/]+)$/.exec(path)
      if (gr && method === 'DELETE') {
        const g = grants.get(decodeURIComponent(gr[1]))
        if (!g || g.userId !== me.id) throw new HttpError(404, 'not_found', 'no such grant')
        grants.delete(g.id)
        recordGrantRevoked(g, me.id, 'user')
        return { status: 204 }
      }
    }
    if (path === '/oauth/consent') {
      accountsOnly()
      const me = requireUser(ctx)
      requireOAuth()
      if (method === 'GET') return j(consentInfo(me, takePending(me, url.searchParams.get('req'), true)))
      if (method === 'POST') return decideConsent(me, body)
    }

    // --- the hosts with an OAuth application ---
    const forgeId = /^\/connections\/([^/]+)\//.exec(path)?.[1] ?? ''
    if (forgeId !== '' && forge.has(forgeId)) {
      accountsOnly()
      return routeForge(ctx, forgeId, method, path, url, body)
    }
    if (path === '/admin/connections' || /^\/admin\/connections\/([^/]+)(\/check)?$/.test(path)) {
      const id = /^\/admin\/connections\/([^/]+)/.exec(path)?.[1] ?? ''
      if (id === '' || forge.has(id) || !(id === 'github' || id === 'git')) {
        accountsOnly()
        const me = requireAdmin(ctx)
        if (!isSiteAdmin(me))
          throw new HttpError(403, 'forbidden', 'only an administrator of the whole server may do that')
        return routeAdminForge(id, method, path, body)
      }
    }
    // --- GitHub ---
    if (path === '/connections' || path === '/connections/github' || path.startsWith('/connections/github/')) {
      accountsOnly()
      return routeGitHub(ctx, method, path, url, body)
    }
    if (path === '/admin/connections/github' || path.startsWith('/admin/connections/github/')) {
      accountsOnly()
      // The server's GitHub App is the whole server's: a tenant's administrator is refused, as on the server.
      const me = requireAdmin(ctx)
      if (!isSiteAdmin(me))
        throw new HttpError(403, 'forbidden', 'only an administrator of the whole server may do that')
      return routeAdminGitHub(me, method, path, url, body)
    }
    // --- usage (3.3): one's own, and everyone's for the administrators ---
    if (path === '/account/usage' && method === 'GET') {
      accountsOnly()
      return j(quotaBook.account(requireUser(ctx).id))
    }
    if (path === '/admin/usage' && method === 'GET') {
      accountsOnly()
      const me = requireAdmin(ctx)
      return j(quotaBook.list(url, isSiteAdmin(me) ? '' : DEFAULT_TENANT))
    }

    // --- administration (administrators only; accounts mode) ---
    if (
      path === '/admin/status' ||
      path === '/admin/storage' ||
      path === '/admin/spaces' ||
      path.startsWith('/admin/spaces/')
    ) {
      accountsOnly()
      requireAdmin(ctx)
      return routeAdmin(method, path, url, body)
    }
    if (path === '/settings' && method === 'GET') {
      requireUser(ctx)
      return { status: 200, body: { settings: tenantBook.mine(DEFAULT_TENANT) } }
    }
    if (path.startsWith('/admin/tenan') || path.startsWith('/admin/settings')) {
      accountsOnly()
      const me = requireAdmin(ctx)
      const r = tenantBook.route(me.id, method, path, url, body)
      if (r) return r
    }
    if (path === '/groups' && method === 'GET') {
      accountsOnly()
      requireUser(ctx)
      return groupBook.find(url)
    }
    if (path === '/admin/groups' || path.startsWith('/admin/groups/')) {
      accountsOnly()
      const me = requireAdmin(ctx)
      return groupBook.routeAdmin(me, method, path, url, body)
    }
    if (
      path === '/admin/security' ||
      path === '/admin/audit' ||
      path === '/admin/auth/providers' ||
      path.startsWith('/admin/auth/providers/')
    ) {
      accountsOnly()
      const me = requireAdmin(ctx)
      if (path === '/admin/security' && method === 'GET') return j(book.view())
      if (path === '/admin/security' && method === 'PUT') return j(book.putSecurity(body, me.id))
      if (path === '/admin/audit' && method === 'GET') {
        const raw = url.searchParams.get('limit')
        const limit = raw === null ? 50 : /^\d+$/.test(raw) ? Number(raw) : NaN
        if (!(limit >= 1 && limit <= 200)) throw new HttpError(400, 'invalid', 'limit 要是 1 到 200')
        return j(
          book.auditList(
            url.searchParams.get('kind') || null,
            url.searchParams.get('user') || null,
            url.searchParams.get('cursor') || null,
            limit,
          ),
        )
      }
      if (path === '/admin/auth/providers' && method === 'GET')
        return j({
          providers: [...book.providers.values()].sort((a, b) => a.view.order - b.view.order).map(book.withDerived),
        })
      if (path === '/admin/auth/providers' && method === 'POST') return j(book.createProvider(body, me.id), 201)
      const one = /^\/admin\/auth\/providers\/([^/]+)(\/refresh)?$/.exec(path)
      if (one) {
        const slug = decodeURIComponent(one[1])
        const rec = book.providers.get(slug)
        if (!rec) throw new HttpError(404, 'not_found', '没有这个提供方')
        if (!one[2] && method === 'GET') return j(book.withDerived(rec))
        if (!one[2] && method === 'PATCH') return j(book.patchProvider(slug, body, me.id))
        if (!one[2] && method === 'DELETE') {
          book.deleteProvider(slug, url.searchParams.get('force') === '1', me.id)
          return { status: 204 }
        }
        if (one[2] && method === 'POST') return j(book.refreshProvider(slug))
      }
    }
    if (path === '/admin/grants' || path.startsWith('/admin/grants/')) {
      accountsOnly()
      const me = requireAdmin(ctx)
      requireGrants()
      if (path === '/admin/grants' && method === 'GET') {
        let limit = 50
        if (url.searchParams.has('limit')) {
          const raw = url.searchParams.get('limit') ?? ''
          const n = /^\d+$/.test(raw) ? Number(raw) : NaN
          if (!(n >= 1 && n <= 200)) throw new HttpError(400, 'invalid', 'limit 要在 1 到 200 之间')
          limit = n
        }
        const after = url.searchParams.get('cursor') ?? ''
        const all = grantsOf(url.searchParams.get('user') || null)
        const rest = all.slice(after ? all.findIndex((g) => g.id === after) + 1 : 0)
        const page = rest.slice(0, limit)
        const items: AdminGrant[] = page.map((g) => {
          const o = users.get(g.userId)
          return { ...publicGrant(g), owner: { id: g.userId, username: o?.username ?? '', name: o?.name ?? '' } }
        })
        return j({ items, next: rest.length > limit ? page[page.length - 1].id : '' })
      }
      const one = /^\/admin\/grants\/([^/]+)$/.exec(path)
      if (one && method === 'DELETE') {
        const g = grants.get(decodeURIComponent(one[1]))
        if (!g) throw new HttpError(404, 'not_found', 'no such grant')
        grants.delete(g.id)
        recordGrantRevoked(g, me.id, 'admin')
        return { status: 204 }
      }
    }
    if (path === '/admin/access' || path === '/admin/tokens' || path.startsWith('/admin/tokens/')) {
      accountsOnly()
      const me = requireAdmin(ctx)
      if (path === '/admin/access') {
        if (method === 'PUT') {
          for (const k of ['tokens', 'mcp'] as const) {
            const v = body[k]
            if (v !== undefined && typeof v !== 'boolean') throw new HttpError(400, 'invalid', `${k} 要是布尔值`)
            if (typeof v === 'boolean') machine[k] = v
          }
        }
        return j({ tokens: machine.tokens, mcp: machine.mcp, oauth: oauthHere() && machine.mcp })
      }
      if (path === '/admin/tokens' && method === 'GET') return adminTokens(url)
      const one = /^\/admin\/tokens\/([^/]+)$/.exec(path)
      if (one && method === 'DELETE') {
        revokeToken(decodeURIComponent(one[1]), null, me.id)
        return { status: 204 }
      }
    }
    if (path === '/admin/daily' || path.startsWith('/admin/daily/')) {
      accountsOnly()
      requireAdmin(ctx)
      return routeSiteDaily(method, path, url, body)
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
        quotaBook.checkSpace(me.id)
        const inbox = body.inbox === undefined ? DEFAULT_INBOX : checkInbox(body.inbox)
        const sync = isRecord(body.storage) ? body.storage.sync : undefined
        if (body.remote !== undefined && sync !== undefined)
          throw new HttpError(400, 'invalid', 'remote 和 storage.sync 不能一起给')
        const wanted = storage.remotes.require
        const meets =
          wanted === 'any'
            ? sync !== undefined || body.remote !== undefined
            : isRecord(sync) && sync.provider === wanted
        if (wanted && !me.remoteExempt && !meets) {
          throw new HttpError(
            409,
            'remote_required',
            wanted === 'any' ? '服务器要求新空间跟随远端仓库。' : `服务器要求新空间同步到 ${wanted}。`,
            { require: wanted },
          )
        }
        if (body.remote !== undefined) {
          const r = body.remote
          if (!isRecord(r) || typeof r.url !== 'string' || !isRecord(r.credential) || r.credential.type !== 'token')
            throw new HttpError(400, 'invalid', '新空间只能用令牌连接远端')
          const space = addSpace(
            newId((x) => spaces.has(x)),
            name,
            me.id,
            false,
            false,
            inbox,
          )
          connectRemote(space, me.id, r)
          return { status: 201, body: spaceInfo(space, me), headers: { Location: `/api/spaces/${space.id}` } }
        }
        if (sync !== undefined) {
          const g = sync
          if (!isRecord(g) || g.provider !== 'github' || typeof g.owner !== 'string' || typeof g.name !== 'string')
            throw new HttpError(400, 'invalid', 'storage.sync 要有 provider（github）、owner 和 name')
          if (g.description !== undefined && (typeof g.description !== 'string' || g.description.length > 350))
            throw new HttpError(400, 'invalid', 'description 最多 350 个字符')
          const found =
            g.create === false
              ? github.existing(me.id, g.owner, g.name, '')
              : github.create(me.id, { owner: g.owner, name: g.name, private: g.private !== false, spaceName: name })
          const space = addSpace(
            newId((x) => spaces.has(x)),
            name,
            me.id,
            false,
            false,
            inbox,
          )
          github.repoConnected(found.repo.id)
          space.remote = {
            url: `https://github.com/${found.repo.owner}/${found.repo.name}.git`,
            branch: 'main',
            credential:
              found.via === 'app'
                ? { type: 'github', via: 'app', installationId: found.installationId, repoId: found.repo.id }
                : { type: 'github', via: 'user', userId: me.id, repoId: found.repo.id },
            state: 'ok',
            lastSync: now(),
            error: '',
            rewritten: null,
          }
          // An attachment repository next to it: asked for, or the administrator's default when the request is silent.
          const ask = g.assets
          if (
            ask === true ||
            (isRecord(ask) && typeof ask.name === 'string') ||
            (ask === undefined && storage.assets.newSpaces)
          ) {
            const aname = isRecord(ask) && typeof ask.name === 'string' ? ask.name : `${found.repo.name}-assets`
            space.assets = {
              url: `https://github.com/${found.repo.owner}/${aname}.git`,
              branch: 'main',
              credential: space.remote.credential,
              used: false,
              move: null,
              cacheBytes: 0,
            }
          } else if (ask !== undefined && ask !== false)
            throw new HttpError(400, 'invalid', 'github.assets 是 true、false 或 {"name": "…"}')
          return { status: 201, body: spaceInfo(space, me), headers: { Location: `/api/spaces/${space.id}` } }
        }
        const plain = addSpace(
          newId((x) => spaces.has(x)),
          name,
          me.id,
          false,
          false,
          inbox,
        )
        return { status: 201, body: spaceInfo(plain, me), headers: { Location: `/api/spaces/${plain.id}` } }
      }
    }
    // The change cursor of each of up to 100 spaces, from memory: no space is opened.
    if (path === '/sync/heads' && method === 'GET') {
      const me = requireUser(ctx)
      offlineAllowed()
      const ids = url.searchParams.getAll('space')
      if (ids.length < 1 || ids.length > 100) throw new HttpError(400, 'invalid', 'give 1 to 100 space parameters')
      if (new Set(ids).size !== ids.length) throw new HttpError(400, 'invalid', 'a space given twice')
      const heads: Record<string, string> = {}
      const missing: string[] = []
      for (const id of ids) {
        const s = spaces.get(id)
        if (s && !s.disabled && groupBook.has(s, me.id) && spaceInfo(s, me).permissions.includes('read'))
          heads[id] = s.store.feedHead()
        else missing.push(id)
      }
      return j({ heads, ...(missing.length ? { missing } : {}) })
    }
    const m = /^\/spaces\/([^/]+)(\/.*)?$/.exec(path)
    if (m) {
      const me = requireUser(ctx)
      const id = decodeURIComponent(m[1])
      const rest = m[2] ?? ''
      // These do not exist in single-user mode, whoever asks.
      if (
        (rest === '' && method === 'PATCH') ||
        rest === '/remote' ||
        rest.startsWith('/remote/') ||
        rest === '/members' ||
        rest.startsWith('/members/') ||
        rest === '/groups' ||
        rest.startsWith('/groups/')
      )
        accountsOnly()
      // Leaving touches only the membership: it works in a space that is switched off.
      const leaving =
        method === 'DELETE' &&
        rest.startsWith('/members/') &&
        decodeURIComponent(rest.slice('/members/'.length)) === me.id
      const own = leaving ? spaces.get(id) : undefined
      const s = own && groupBook.has(own, me.id) ? own : openSpace(me, id, needOf(me, method, rest))
      // Any request that opens the space is a use; one that needs files restores an evicted space (the lists are
      // answered by the index, which an evicted space keeps).
      s.lastUsed = now()
      if (
        s.cache === 'evicted' &&
        !(
          method === 'GET' &&
          /^(\/pages|\/tree|\/tags|\/checks|\/pages\/[^/]+\/(backlinks|links))$/.test(rest) &&
          url.searchParams.get('body') === null
        )
      )
        s.cache = 'partial'
      if (rest === '' && method === 'PATCH') {
        if (body.name === undefined && body.inbox === undefined)
          throw new HttpError(400, 'invalid', '至少要给出 name 或 inbox')
        // Check both before changing either, so a bad one leaves the space as it was.
        const name = body.name === undefined ? s.name : checkName(body.name)
        const inbox = body.inbox === undefined ? s.inbox : checkInbox(body.inbox)
        s.name = name
        s.inbox = inbox
        return j(spaceInfo(s, me))
      }
      if (rest === '/members' || rest.startsWith('/members/')) {
        // Leaving needs no more than being a member: the permission check above is waived for it.
        return routeMembers(me, s, method, rest, body, url.search)
      }
      if (rest === '/groups' || rest.startsWith('/groups/')) {
        accountsOnly()
        const r = groupBook.routeSpace(me, s, method, rest, body)
        if (r) return r
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
    // The mail the mock "sent" (it sends nothing): tests and the dev page read the links from here.
    mail: mailBook,
    // `target` is a path with query, as sent by the client ("/api/...").
    // `cookie` is the request's Cookie header.
    // `raw` is the request body as bytes, for the one request that is not JSON (uploading a file).
    handle(
      method: string,
      target: string,
      bodyText?: string,
      contentType?: string | null,
      cookie?: string | null,
      raw?: Uint8Array,
    ): MockResponse {
      counter++
      try {
        sweepAll()
        const upload =
          method.toUpperCase() === 'POST' && /^\/api\/spaces\/[^/?]+\/(pages\/[^/?]+\/assets|import)(\?|$)/.test(target)
        // Writes must say they are JSON, like the real server insists; a file says it is bytes.
        if (upload) {
          if (!/^application\/octet-stream\s*(;|$)/i.test(contentType ?? ''))
            throw new HttpError(415, 'invalid', '上传文件和导入需要 Content-Type: application/octet-stream')
          return route(
            'POST',
            new URL(target, 'http://mock.local'),
            {},
            contextOf(cookie),
            raw ?? new TextEncoder().encode(bodyText ?? ''),
          )
        }
        if (method.toUpperCase() !== 'GET' && !/^application\/json\s*(;|$)/i.test(contentType ?? ''))
          throw new HttpError(415, 'invalid', '请求需要 Content-Type: application/json')
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
    peek: (id: string, space?: string): Page | undefined =>
      (space ? spaces.get(space) : [...spaces.values()].find((s) => s.store.peek(id)))?.store.peek(id),
    // Mock only: the file of a type was damaged by hand.
    damageType: (space: string, name: string, message: string) => spaces.get(space)!.store.damageType(name, message),
    // A file that is not a page, for the check page: the mock has no files of its own.
    addOrphan: (space: string, o: Parameters<SpaceStore['addOrphan']>[0]) => spaces.get(space)!.store.addOrphan(o),
    // Mock only: a field of a kind the app does not know.
    forceKind: (space: string, name: string, key: string, kind: FieldKind) =>
      spaces.get(space)!.store.forceKind(name, key, kind),
    ids: (space = 'default') => spaces.get(space)?.store.ids() ?? [],
    spaceIds: () => [...spaces.keys()],
    peekDraft: (userId: string, id: string, space = 'default') => spaces.get(space)?.store.peekDraft(userId, id),
    userId: (username: string) => [...users.values()].find((u) => u.username === username)?.id,
    // Views of a folder as written by hand in space.yml: served as "@n" until a write names them.
    // What GET .../types says to managers about settings files that could not be used.
    // Mock only: an MCP client sent this person to the authorization page; the id is what the consent page's address carries.
    startAuthorization: (username: string, o: StartOptions = {}): string => {
      const u = [...users.values()].find((x) => x.username === username)
      if (!u) throw new Error(`no user ${username}`)
      return startPending(u, o)
    },
    seedSettingsNotices: (notices: SettingsNotice[], space = 'default') => void settingsNotices.set(space, notices),
    // What GET /admin/daily says the server changed by itself in the daily text settings on an upgrade.
    seedDailyNotices: (notices: DailyNotice[]) => void (site.notices = notices.map((n) => ({ ...n }))),
    seedHandWrittenViews: (folder: string, views: Record<string, unknown>[], space = 'default') =>
      spaces.get(space)!.store.seedHandWrittenViews(folder, views as never),
    // Let the server's own draft commits run now (they also run on every request).
    sweep: sweepAll,
    // Put a space's remote in a state, as the dev route does.
    setRemote: (space: string, state: RemoteState | 'local', patch: { error?: string } = {}) =>
      setRemoteState(spaces.get(space)!, state, patch),
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
