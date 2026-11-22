// Shapes of docs/api.md.

// What a caller may do in a space (docs/api.md, "Space"). The app shows or hides
// controls by these and never by the name of a role.
export type Permission = 'read' | 'write' | 'members' | 'manage'
// The caller's role in a space. Empty for an administrator who is not a member.
export type SpaceRole = 'owner' | 'editor' | 'viewer'

export interface SpaceInfo {
  id: string
  name: string
  // For showing ("Owner", "Viewer"); never for deciding what is allowed.
  role: SpaceRole | ''
  // What the caller may do here.
  permissions: Permission[]
  // True while an administrator has switched the space off: it stays in the
  // list, but nothing in it can be opened.
  disabled?: boolean
  // Made together with the account.
  personal: boolean
  // `local` (the repository lives on the server only) or the state of the
  // remote it follows. A server that does not say is taken to be local.
  sync?: SyncState
  // The folder quick notes go to: a folder path, never empty. Only a name; the
  // folder exists once a page has been made in it.
  inbox: string
}
export interface MetaResponse {
  version: string
  // The largest file, in bytes, the server accepts as an upload.
  maxUpload: number
  // The largest body, in bytes, the server accepts for an import.
  maxImport: number
  spaces: SpaceInfo[]
}

// What an import did (docs/api.md, "Import"). `skipped` is at most 200 entries;
// `skippedMore` counts the ones beyond that and is absent when there are none.
export interface ImportReport {
  pages: number
  files: number
  skipped: { path: string; reason: string }[]
  skippedMore?: number
}

// The answer to storing a file for a page: `path` is what goes into the page.
export interface UploadedAsset {
  path: string
  name: string
  size: number
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
  // The header exactly as the file has it (the text between the `---` lines),
  // for showing the file as written. What a save writes is `meta` alone.
  header?: string
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
  // With `q`: `title` searches titles only.
  in?: 'title'
  // Days (`2026-11-01`, UTC), both ends included. `updated*` is when the file
  // was last written; `created*` is the header's own date.
  updatedFrom?: string
  updatedTo?: string
  createdFrom?: string
  createdTo?: string
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
// The ten names the server accepts for a tag's colour; how each looks is the
// client's business (src/lib/tagColors.ts).
export type TagColorName = 'gray' | 'red' | 'orange' | 'amber' | 'green' | 'teal' | 'blue' | 'purple' | 'pink' | 'rose'
// A name, or a custom `#rrggbb` (the server stores it in lower case).
export type TagColor = TagColorName | `#${string}`
export interface TagsResponse {
  // `color` is absent when the tag has none.
  tags: { tag: string; pages: number; color?: TagColor }[]
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
  | 'no_permission'
  | 'space_disabled'
  | 'user_not_found'
  | 'user_disabled'
  | 'last_owner'
  | 'personal_space'
  | 'busy'
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

export type BatchPagesInput = { op: 'move'; ids: string[]; folder: string } | { op: 'delete'; ids: string[] }

// --- members (docs/api.md, "Members") -----------------------------------------------

export interface Member {
  userId: string
  username: string
  name: string
  role: SpaceRole
  // True for the caller.
  you: boolean
}

// --- administration (docs/api.md, "Administration") -----------------------------------

export interface AdminSpace {
  id: string
  name: string
  personal: boolean
  created: string
  disabled: boolean
  // The first owner; null when there is none.
  owner: { id: string; username: string; name: string } | null
  members: number
  // The last stored measurement; null until the space has been measured.
  sizeBytes: number | null
  sizeAt: string | null
  sync: SyncState
}
export interface AdminSpaceList {
  items: AdminSpace[]
  // The cursor of the next page, '' after the last.
  next: string
}
export interface AdminSpacesParams {
  q?: string
  cursor?: string
  limit?: number
}
export interface SpaceSize {
  sizeBytes: number
  sizeAt: string
}
export interface DeletedSpace {
  // Where the repository went, relative to the data directory; '' if it could not be moved.
  trashed: string
  cacheTrashed: string
  warnings: string[]
}
export interface ServerStatus {
  version: string
  startedAt: string
  uptimeSeconds: number
  database: 'sqlite' | 'postgres'
  spaces: number
  users: number
  openSpaces: number
  // Absent where the platform cannot say.
  diskFreeBytes?: number
  goVersion: string
}
