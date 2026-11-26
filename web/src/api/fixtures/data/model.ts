// What a fixture server holds: plain typed records, declared here and filled by `createData`. Nothing in this file
// decides anything; the routes read and write these arrays.

import type {
  CaptchaProvider,
  ConsentInfo,
  EndedReason,
  FolderBinding,
  Favorite,
  PageMeta,
  PageType,
  PageTemplate,
  Problem,
  Permission,
  SpaceRole,
  SavedView,
  SpaceInfo,
  TemplateDefaults,
} from '../../types.ts'
import { createAdminData, type AdminData } from './admin.ts'
import { DEMO_EMPTY_FOLDERS, DEMO_NAME, DEMO_PAGES, DEMO_TAG_COLORS } from './demo.ts'
import { DEMO_SINGLE_USER, DEMO_ADMIN, SETUP_TOKEN, type UserRec } from './people.ts'

export interface StoredPage {
  id: string
  folder: string
  // The file's name in its folder, made from the title when the page is written.
  file: string
  meta: PageMeta
  body: string
  // Milliseconds; the answer says it as RFC 3339.
  updated: number
}

export interface DraftRec {
  version: string
  baseRev: string
  meta: PageMeta
  body: string
  updated: number
  // When the draft was first stored on this base.
  first: number
}

export interface SpaceData {
  info: SpaceInfo
  pages: StoredPage[]
  folders: string[]
  types: PageType[]
  bindings: FolderBinding[]
  templates: PageTemplate[]
  defaults: TemplateDefaults
  // The saved views of a folder or a tag, by scope (`folder=Work`).
  views: Record<string, { rev: string; views: SavedView[] }>
  favorites: Favorite[]
  tagColors: Record<string, string>
  folderStyles: Record<string, { icon: string; color: string }>
  sourcePresets: string[] | null
  // One signed-in person, so one draft per page.
  drafts: Record<string, DraftRec>
  // What the server found wrong in the space (the check page lists them).
  checks: Problem[]
  // The change feed: the epoch changes with a reindex; each write numbers the page it touched.
  feed: {
    epoch: string
    seq: number
    floor: number
    seen: Record<string, { fp: string; seq: number }>
    gone: { id: string; seq: number }[]
  }
}

export interface ProviderRec {
  slug: string
  kind: 'oidc' | 'saml' | 'github'
  name: string
  icon?: string
  order?: number
  [extra: string]: unknown
}

export interface AuthData {
  mode: 'single' | 'accounts'
  // No account exists: the first-run page.
  setup: boolean
  setupToken: string
  users: UserRec[]
  // Who is signed in (a user id), or nobody.
  signedIn: string | null
  hold: 'passkey' | 'password' | null
  ended?: EndedReason
  providers: ProviderRec[]
  loginMode: 'dual' | 'sso_first' | 'sso_redirect' | 'local_only'
  passkeys: boolean
  register: boolean
  forgot: boolean
  orgCreation: boolean
  // The page the registration form links to for the terms.
  termsUrl?: string
  // The invite that exists from the start.
  invite: { token: string; role: 'member' | 'admin'; fill?: { username: string; name: string; email: string } } | null
  // The challenge a captcha request answers with, or none.
  captcha: { provider?: CaptchaProvider; siteKey?: string; id?: string; image?: string } | null
  // OAuth authorization requests waiting for the person's consent, by `req`.
  consents: Record<string, ConsentInfo>
}

// The site's feature switches, as GET /api/settings says them.
export const DEFAULT_FEATURES: Record<string, boolean> = {
  'accounts.multipleEmails': false,
  'features.adminView': false,
  'features.auditDownload': false,
  'features.domains': false,
  'features.idpAdmins': false,
  'features.managedAccounts': false,
  'features.mcp': false,
  'features.offline': true,
  'features.orgCreation': false,
  'features.ownCaptcha': false,
  'features.ownConnections': false,
  'features.ownDaily': false,
  'features.ownSso': false,
  'features.passkeys': true,
  'features.registration': false,
  'features.tenancy': false,
  'features.tokens': true,
  'mail.ownSender': false,
}

export interface FixtureData {
  auth: AuthData
  features: Record<string, boolean>
  // What the administration console reads (data/admin.ts).
  admin: AdminData
  spaces: SpaceData[]
  // Counters for ids and versions, so a run is repeatable.
  counter: number
  // What a route appended: mail a person would be sent, and the like.
  sent: { mail: { to: string; kind: string; token?: string }[] }
}

export interface FixtureOptions {
  now?: () => number
  mode?: 'single' | 'accounts'
  // No demo pages.
  empty?: boolean
  // The folder quick notes go to.
  inbox?: string
  // Accounts mode: nobody is signed in until a login.
  signedIn?: boolean
  // Accounts mode with no account: the first-run page.
  setup?: boolean
  providers?: ProviderRec[]
  loginMode?: AuthData['loginMode']
  passkeys?: boolean
  invite?: AuthData['invite']
  // Change what was built, before the server starts.
  data?: (d: FixtureData) => void
}

const ago = (now: number, minutes: number) => now - minutes * 60_000

export function emptySpace(info: Partial<SpaceInfo> & Pick<SpaceInfo, 'id' | 'name'>): SpaceData {
  return {
    info: {
      role: 'owner',
      permissions: ['read', 'write', 'members', 'manage'],
      personal: false,
      sync: 'local',
      inbox: 'Inbox',
      directRole: 'owner',
      viaGroups: false,
      disabled: false,
      ...info,
    },
    pages: [],
    folders: [''],
    types: [],
    bindings: [],
    templates: [],
    defaults: { folders: {}, quickNote: '' },
    views: {},
    favorites: [],
    tagColors: {},
    folderStyles: {},
    sourcePresets: null,
    drafts: {},
    checks: [],
    feed: { epoch: '00000000', seq: 0, floor: 0, seen: {}, gone: [] },
  }
}

const ROLE_PERMISSIONS: Record<SpaceRole, Permission[]> = {
  owner: ['read', 'write', 'members', 'manage'],
  editor: ['read', 'write'],
  viewer: ['read'],
}

// A space somebody else owns, which the signed-in person is a member of in `role`.
export function sharedSpace(data: FixtureData, role: SpaceRole, name = '团队'): SpaceData {
  const s = emptySpace({
    id: `01FSHARE${data.spaces.length}`,
    name,
    role,
    directRole: role,
    permissions: ROLE_PERMISSIONS[role],
  })
  data.spaces.push(s)
  return s
}

export function createData(options: FixtureOptions = {}): FixtureData {
  const now = (options.now ?? Date.now)()
  const accounts = options.mode === 'accounts'
  const user: UserRec = accounts ? DEMO_ADMIN : DEMO_SINGLE_USER
  const space = emptySpace({
    id: 'default',
    name: DEMO_NAME,
    personal: accounts,
    inbox: options.inbox ?? 'Inbox',
  })
  if (!options.empty) {
    space.tagColors = { ...DEMO_TAG_COLORS }
    const taken = new Set<string>()
    for (const f of DEMO_EMPTY_FOLDERS) space.folders.push(f)
    DEMO_PAGES.forEach((p, i) => {
      const id = `01FX${(i + 1).toString(32).toUpperCase().padStart(6, '0')}`.replace(/[ILOU]/g, 'X')
      const meta: PageMeta = { id }
      if (p.title) meta.title = p.title
      if (p.subtitle) meta.subtitle = p.subtitle
      if (p.tags) meta.tags = [...p.tags]
      meta.created = p.created
      Object.assign(meta, p.extra)
      let file = `${p.title ?? `${p.created}-${id.slice(-4).toLowerCase()}`}.md`
      for (let n = 2; taken.has(`${p.folder}/${file}`); n++) file = `${p.title} ${n}.md`
      taken.add(`${p.folder}/${file}`)
      space.pages.push({ id, folder: p.folder, file, meta, body: p.body, updated: ago(now, p.ago) })
      for (let f = p.folder; f; f = f.includes('/') ? f.slice(0, f.lastIndexOf('/')) : '')
        if (!space.folders.includes(f)) space.folders.push(f)
    })
  }
  const data: FixtureData = {
    auth: {
      mode: options.mode ?? 'single',
      setup: options.setup ?? false,
      setupToken: SETUP_TOKEN,
      users: options.setup ? [] : [structuredClone(user)],
      signedIn: accounts ? (options.signedIn ? user.id : null) : user.id,
      hold: null,
      providers: options.providers ?? [],
      loginMode: options.loginMode ?? 'dual',
      passkeys: options.passkeys ?? true,
      register: false,
      forgot: false,
      orgCreation: false,
      invite: options.invite ?? null,
      captcha: null,
      consents: {},
    },
    features: { ...DEFAULT_FEATURES },
    admin: createAdminData(now, user),
    spaces: [space],
    counter: 0,
    sent: { mail: [] },
  }
  options.data?.(data)
  return data
}
