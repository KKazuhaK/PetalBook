// Shapes of docs/api.md.

export interface SpaceInfo {
  id: string
  name: string
  // The caller's role in the space; always `owner` in this milestone.
  role: string
  // Made together with the account.
  personal: boolean
  // `local` (the repository lives on the server only) or the state of the
  // remote it follows. A server that does not say is taken to be local.
  sync?: SyncState
}
export interface MetaResponse {
  version: string
  spaces: SpaceInfo[]
}

export interface PageSummary {
  id: string
  path: string
  folder: string
  title?: string
  subtitle?: string
  // Where the text comes from; free text.
  source?: string
  label: string
  untitled: boolean
  tags?: string[]
  type?: string
  created?: string
  updated: string
  excerpt?: string
}

// The page header as the server serves it. Fields the app does not know about
// are sent back exactly as received; the server decides the key order.
export type PageMeta = Record<string, unknown>

// The caller's draft of a page (docs/api.md, "Drafts"). `stale` means the file
// moved on since `baseRev`, so the draft cannot be committed as it is.
export interface PageDraft {
  // Opaque; new each time the draft is stored. What `prev` and commit's
  // `version` refer to, since `updated` only has one-second resolution.
  version: string
  baseRev: string
  meta: PageMeta
  body: string
  updated: string
  stale: boolean
}

export interface Page {
  id: string
  path: string
  folder: string
  rev: string
  updated: string
  meta: PageMeta
  body: string
  // Only `GET .../pages/{id}` carries it, and only when the caller has one.
  draft?: PageDraft
}

export interface DraftReceipt {
  version: string
  baseRev: string
  updated: string
  stale: boolean
}

export type SortKey = 'updated' | 'created' | 'title' | '-updated' | '-created' | '-title'

export interface ListParams {
  folder?: string
  recursive?: boolean
  tag?: string[]
  q?: string
  sort?: SortKey
  limit?: number
  cursor?: string
}
export interface PageList {
  items: PageSummary[]
  next: string
}

export interface CreatePageInput {
  folder?: string
  meta?: PageMeta
  body?: string
}
export interface SavePageInput {
  baseRev: string
  meta: PageMeta
  body: string
}
// Storing a draft says which version it replaces ("" when none is known).
export interface DraftInput extends SavePageInput {
  prev: string
}

export interface TreeResponse {
  folders: { path: string; pages: number }[]
}
export interface TagsResponse {
  tags: { tag: string; pages: number }[]
}

// --- history (docs/api.md, "History") -------------------------------------------

export type VersionChange = 'add' | 'edit' | 'move' | 'delete'
// One commit that changed the page's file, newest first in a listing.
export interface HistoryItem {
  commit: string
  time: string
  author: string
  path: string
  // Absent for the version in which the page was deleted.
  rev?: string
  change: VersionChange
}
export interface HistoryList {
  items: HistoryItem[]
  next: string
}
// The page as it was in one version.
export interface PageVersion {
  commit: string
  time: string
  author: string
  path: string
  rev?: string
  meta: PageMeta
  body: string
}

// --- remote repository (docs/api.md, "Remote repository") -------------------------

export type RemoteState = 'ok' | 'offline' | 'rewritten' | 'error'
export type SyncState = 'local' | RemoteState

export type RemoteCredential = { type: 'ssh'; publicKey: string; fingerprint: string } | { type: 'token'; username: string }

export interface Remote {
  url: string
  branch: string
  credential: RemoteCredential
  state: RemoteState
  // Absent before the first time the server saw the remote.
  lastSync?: string
  // In words fit to show; empty when all is well.
  error: string
  // SSH only: the host key trusted on first connection.
  hostKey?: string
  // Set when `state` is `rewritten`.
  rewritten: { lastGood: string; remote: string } | null
}

export interface RemoteKey {
  publicKey: string
  fingerprint: string
}

export interface ConnectRemoteInput {
  url: string
  branch?: string
  // A token left out while the space already has one keeps it.
  credential: { type: 'ssh' } | { type: 'token'; username?: string; token?: string }
}

export type ErrorCode =
  | 'not_found'
  | 'invalid'
  | 'conflict'
  | 'exists'
  | 'unauthenticated'
  | 'forbidden'
  | 'rate_limited'
  | 'unavailable'
  | 'internal'
  | 'network'
  | 'unknown'

// --- accounts -----------------------------------------------------------------

export type Role = 'admin' | 'member'

export interface User {
  id: string
  username: string
  name: string
  email: string
  role: Role
  disabled: boolean
  created: string
}

export interface SessionInfo {
  mode: 'single' | 'accounts'
  user: User | null
  // True while there is no user at all: the app shows the first-run page.
  setup: boolean
  // Absent in single-user mode.
  methods?: { password: boolean; oidc: { name: string } | null }
}

export interface Identity {
  id: string
  provider: string
  label: string
  created: string
}
export interface AccountInfo {
  user: User
  hasPassword: boolean
  identities: Identity[]
}

export interface Invite {
  id: string
  role: Role
  created: string
  expires: string
  createdBy: string
}
// The token is in the response of the creating request only.
export interface CreatedInvite {
  id: string
  token: string
  role: Role
  created: string
  expires: string
}
export interface InviteCheck {
  role: Role
  expires: string
}

export type OidcIntent = 'login' | 'setup' | 'invite' | 'link'
