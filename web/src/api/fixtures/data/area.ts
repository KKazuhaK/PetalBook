// What a fixture server holds for the person's own account and what is around it: passkeys, linked sign-ins, addresses,
// tokens and connected apps. Plain records; the routes (routes/account.ts) read and write them.

import type {
  AccountEmail,
  ApiToken,
  DailyOptionChoice,
  DailyPrefs,
  DailySettings,
  DailySpaceSource,
  Grant,
  ConnectionOwner,
  ConnectionRepo,
  Link,
  NotificationItem,
  Passkey,
  Provider,
  SpaceRole,
  AdminForge,
  AssetsRemote,
  Remote,
  AdminGitHub,
  GitHubInstallation,
  GitHubPendingClaim,
} from '../../types.ts'

export interface AccountData {
  passkeys: Passkey[]
  // The name the next passkey is given: what the begin call was asked for.
  passkeyName: string
  links: Link[]
  // The addresses beyond the one on the account (the account's own address is always listed first).
  emails: AccountEmail[]
  tokens: ApiToken[]
  // null: the server has no authorization server, and the grants answer 404.
  grants: Grant[] | null
}

export interface DailyData {
  // A space's own source of the day's text, by space id.
  settings: Record<string, DailySettings | null>
  // What a space chose of the interfaces (accounts mode), by space id.
  prefs: Record<
    string,
    { inherit: boolean; apis: string[]; options?: DailyOptionChoice; sources?: Record<string, DailySpaceSource[]> }
  >
  // What the administrators offer: the interfaces, the languages with a tab, the server's default, what they allow.
  site: {
    available: DailyPrefs['available']
    languages: string[]
    default: DailyPrefs['default']
    allowed?: DailyPrefs['allowed']
    unavailable: NonNullable<DailyPrefs['unavailable']>
  }
}

// The platforms the server syncs spaces with (GitHub, GitLab, Gitea...), and what a person sees of them.
export interface ConnectionsData {
  providers: Provider[]
  // The accounts a repository can be made under, by provider id.
  owners: Record<string, ConnectionOwner[]>
  // The repositories a connection can reach, by `<provider>:<installation id | mine>`.
  repos: Record<string, ConnectionRepo[]>
  // The administrators' side: the hosts with an OAuth application, the GitHub App (null: none yet) and the
  // installations of the App.
  forges: AdminForge[]
  github: Extract<AdminGitHub, { configured: true }> | null
  installations: GitHubInstallation[]
  // The claims of the organisation's installations that wait for GitHub's event (the person's never do).
  pendingClaims: GitHubPendingClaim[]
  // The spaces that sync through each installation, by its id.
  installationSpaces: Record<number, { id: string; name: string }[]>
  // What GitHub says of the App the next time the server asks (a re-check): these fields replace the stored ones.
  githubSide: Partial<NonNullable<ConnectionsData['github']>> | null
}

export interface AreaData {
  connections: ConnectionsData
  // The remote repository a space follows and its attachment repository, by space id.
  remotes: Record<string, { remote: Remote | null; assets: AssetsRemote | null }>
  account: AccountData
  daily: DailyData
  // The notifications of the signed-in person, and the preferences they keep.
  notifications: NotificationItem[]
  prefs: Record<string, unknown>
  // The people of a space and the groups in it, by space id. A space with no entry has the signed-in person alone, in
  // the role the space's info says.
  members: Record<string, { userId: string; role: SpaceRole }[]>
  spaceGroups: Record<string, { groupId: string; role: SpaceRole }[]>
}

export function createArea(): AreaData {
  return {
    account: { passkeys: [], passkeyName: '', links: [], emails: [], tokens: [], grants: null },
    daily: {
      settings: {},
      prefs: {},
      site: { available: [], languages: ['zh-Hans', 'en'], default: null, unavailable: {} },
    },
    remotes: {},
    connections: {
      providers: [],
      owners: {},
      repos: {},
      forges: [],
      github: null,
      installations: [],
      pendingClaims: [],
      installationSpaces: {},
      githubSide: null,
    },
    notifications: [],
    prefs: {},
    members: {},
    spaceGroups: {},
  }
}

const SETTLED = '2026-11-01T00:00:00Z'
type Scene = { spaces: { info: { id: string; name: string } }[]; area: AreaData }

// The server has an authorization server for MCP clients: connected apps are listed (none yet), and MCP is on.
export function withAuthorizationServer(d: { features: Record<string, boolean>; area: AreaData }) {
  d.area.account.grants ??= []
  d.features['features.mcp'] = true
}

// A connected app the person approved earlier, on the first space (the server then has an authorization server).
export function approveApp(d: Scene, name: string, access: 'read' | 'write' = 'write') {
  const grants = (d.area.account.grants ??= [])
  const space = d.spaces[0].info
  grants.push({
    id: `grant${grants.length + 1}`,
    client: { name, id: `pbc_mock${grants.length + 1}` },
    access,
    spaces: [{ id: space.id, name: space.name }],
    created: SETTLED,
    lastUsed: SETTLED,
  })
}

// A token the person made earlier, on the first space.
export function addToken(d: Scene, name: string) {
  const tokens = d.area.account.tokens
  const space = d.spaces[0].info
  tokens.push({
    id: `token${tokens.length + 1}`,
    name,
    prefix: `pbk_${name}`.padEnd(12, 'x').slice(0, 12),
    access: 'read',
    spaces: [{ id: space.id, name: space.name }],
    created: SETTLED,
    expires: '2099-01-01T00:00:00Z',
    lastUsed: null,
    revoked: null,
    state: 'active',
  })
}

// An interface the administrators made, for a language ('' takes any).
export function addApi(d: { area: AreaData }, id: string, lang: string) {
  d.area.daily.site.available.push({ id, name: `接口${id}`, lang })
}

// The built-in interface of the Chinese one-liners, with the categories a person may choose.
export function addHitokoto(d: { area: AreaData }) {
  d.area.daily.site.available.push({
    id: 'hitokoto',
    name: '一言',
    lang: 'zh-Hans',
    languages: ['zh-Hans', 'zh-Hant'],
    options: [
      {
        key: 'categories',
        param: 'c',
        kind: 'multi',
        values: [
          ['a', '动画', 'Anime'],
          ['i', '诗词', 'Poetry'],
          ['k', '哲学', 'Philosophy'],
        ].map(([value, zh, en]) => ({ value, label: { zh, en } })),
      },
    ],
  })
}

// The built-in interface of the poems, whose one option is a path.
export function addJinrishici(d: { area: AreaData }) {
  d.area.daily.site.available.push({
    id: 'jinrishici',
    name: '今日诗词',
    lang: 'zh-Hans',
    languages: ['zh-Hans', 'zh-Hant'],
    options: [
      {
        key: 'category',
        param: '',
        kind: 'path',
        default: 'all',
        values: [
          ['all', '全部', 'All'],
          ['shuqing', '抒情', 'Lyrical'],
          ['shuqing/libie', '抒情-离别', '抒情-离别'],
        ].map(([value, zh, en]) => ({ value, label: { zh, en } })),
      },
    ],
  })
}

// A notification for the signed-in person. Ids sort by time, like the real server's.
export function addNotification(
  d: { counter: number; area: AreaData },
  kind: string,
  ref: string,
  payload: Record<string, unknown>,
  at = '2026-11-25T10:00:00Z',
) {
  const n = ++d.counter
  d.area.notifications.push({
    id: `${Date.parse(at).toString(16).padStart(12, '0')}${n.toString(16).padStart(6, '0')}`,
    kind,
    ref,
    payload,
    at,
    read: false,
  })
}

// A space with problems, as the notification centre lists it (a state that holds now, not an event).
export function addProblems(
  d: { area: AreaData },
  space: { id: string; name: string },
  counts: Record<string, number>,
) {
  const total = Object.values(counts).reduce((a, b) => a + b, 0)
  d.area.notifications.push({
    id: `problems:${space.id}`,
    kind: 'problems',
    ref: space.id,
    payload: { space: space.name, total, counts, sig: JSON.stringify(counts) },
    at: null,
    read: false,
    derived: true,
  })
}
