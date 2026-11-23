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
  // From 3.2 `role` and `permissions` are the effective ones (the person's own
  // role and their groups' together). The person's own role, "" when they reach
  // the space only through groups, and whether a group of theirs is a member.
  directRole?: SpaceRole | ''
  viaGroups?: boolean
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
  // The type the page has: its header's, else its folder's (see "Page types").
  effectiveType?: string
  created?: string
  updated: string
  excerpt?: string
  // Only with `props: true` in the listing (the table view): the header's keys
  // that are not built in, as plain values, and the rev a change of them needs.
  props?: Record<string, unknown>
  rev?: string
  // With `props`: the keys whose value there is not the whole of what the header
  // holds (cut text, a list that lost elements, an object). Absent when none. A
  // cell must not write such a copy back.
  partialProps?: string[]
  // Only with `body: true` in the listing (continuous reading): the Markdown.
  body?: string
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
  // The type the page has: its header's `type`, else its folder's, else "".
  effectiveType?: string
  // The keys of `meta` that a listing holds only in part (the summary's list, judged by the server from
  // the file); absent when none. A cell must not write such a key back.
  partialProps?: string[]
}

export interface DraftReceipt {
  version: string
  baseRev: string
  updated: string
  stale: boolean
}

export type SortKey = 'updated' | 'created' | 'title' | '-updated' | '-created' | '-title'
// Sorting by a property of the header: `prop:status`, `-prop:status`.
export type PropSort = `prop:${string}` | `-prop:${string}`

// One condition of a table's filter (docs/api.md, "Filtering by properties").
export type FilterOp = 'in' | 'range' | 'empty' | 'notEmpty' | 'contains'
export interface PropFilter {
  key: string
  // `notIn` is the server's too (has a value and none of the given ones); the table's own form never makes it,
  // a board's "other values" column and a saved view do.
  op: FilterOp | 'notIn'
  values?: (string | number | boolean | null)[]
}

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
  sort?: SortKey | PropSort
  // Conditions on properties; all must hold.
  filter?: PropFilter[]
  // Put `props` and `rev` in the summaries (the table view does).
  props?: boolean
  // Put the Markdown `body` in the summaries; the limit is then at most 20.
  body?: boolean
  limit?: number
  cursor?: string
}
export interface PageList {
  items: PageSummary[]
  next: string
}

// A page that links to this one (`GET …/pages/{id}/backlinks`).
export interface Backlink {
  id: string
  path: string
  title: string
  untitled: boolean
  // The paragraph the link is in, as plain text; "" when the file no longer has the link.
  excerpt: string
}
export interface BacklinkList {
  items: Backlink[]
  next: string
  total: number
}
// One link of a page, in the order of the text (`GET …/pages/{id}/links`).
export interface PageLink {
  text: string
  // The file the link names, from the root of the space.
  path: string
  id?: string
  anchor?: string
  broken: boolean
  target?: { id: string; path: string; title: string; untitled: boolean }
}
export interface PageLinkList {
  items: PageLink[]
}

export interface CreatePageInput {
  folder?: string
  meta?: PageMeta
  body?: string
  // The template to start from: left out for the folder's (or the quick-note) default, "" for none.
  template?: string
  // The browser's offset in minutes east of UTC, for {{date}}, {{time}} and {{datetime}} (UTC when left out).
  tz?: number
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

// --- page types (docs/api.md, "Page types") ----------------------------------------

export type FieldKind = 'text' | 'longtext' | 'select' | 'multiselect' | 'date' | 'number' | 'checkbox' | 'url' | 'other'
export interface TypeField {
  key: string
  // "" when the file gives none: show the key.
  title: string
  kind: FieldKind
  // For select and multiselect.
  options?: string[]
  // The field's schema as the file has it.
  schema?: unknown
}
export interface PageType {
  // The file name; what a header's `type` says. Never changes.
  name: string
  // "" when the file gives none: show the name.
  title: string
  // The Git blob id of the type's file: a change or a delete is made from it (`baseRev`).
  rev: string
  fields: TypeField[]
  // Set when the file cannot be used; the fields are then empty.
  error?: string
}
// A template page of .petalbook/templates (docs/api.md, "Templates"). The list leaves `body` out.
export interface PageTemplate {
  // The file name without .md; the template's id.
  name: string
  path: string
  rev: string
  meta: PageMeta
  body?: string
}
export interface TemplateDefaults {
  folders: Record<string, string>
  quickNote: string
}
export interface TemplatesResponse {
  templates: PageTemplate[]
  defaults: TemplateDefaults
}
export interface PutTemplateInput {
  // "" makes the template; otherwise the rev it was read at.
  baseRev: string
  meta: PageMeta
  body: string
}
// What the check page lists (docs/api.md, "Checks"), in this order.
export type ProblemKind = 'no_id' | 'duplicate_id' | 'bad_header' | 'type_mismatch' | 'not_in_type' | 'broken_link' | 'missing_asset' | 'duplicate_title'
export type ProblemFix = 'add_id' | 'remove_fields'
export interface Problem {
  kind: ProblemKind
  path: string
  // The page, when the file is one.
  id?: string
  message: string
  // The current rev of the file the problem is in: the baseRev of POST .../checks/fix
  // (a file without an id has no page to read it from).
  rev: string
  // bad_header: {line}; duplicate_id and duplicate_title: {paths}; type_mismatch and
  // not_in_type: {fields}; broken_link: {target, line}; missing_asset: {target}.
  detail?: Record<string, unknown>
  fix: ProblemFix | null
}
export interface ChecksParams {
  kind?: ProblemKind
  cursor?: string
}
export interface ChecksResponse {
  // Every kind, listed or not.
  counts: Record<ProblemKind, number>
  items: Problem[]
  next: string
}
export interface FixCheckInput {
  kind: 'no_id' | 'not_in_type'
  path: string
  // The rev of the file as it was listed.
  baseRev: string
}
export interface FolderBinding {
  folder: string
  type: string
}
export interface TypesResponse {
  types: PageType[]
  bindings: FolderBinding[]
  // What the server did about a settings file it could not use (docs/api.md, "Settings files that cannot be used").
  // Only for those who manage the space; absent for anyone else, and when there is nothing to tell.
  settingsNotices?: SettingsNotice[]
}
// A version of a settings file: a commit, or (without one) a change in the folder that is in no commit yet.
export interface SettingsVersion {
  commit?: string
  time: string
}
export interface SettingsNotice {
  // The settings file or type file, as a path in the space.
  file: string
  // One entry of it (`Tang`, `Tang: views`), or "not a folder" for a link or a file where a folder belongs.
  entry?: string
  // The file in the space is still the one that cannot be used; false once it was put right.
  unreadable: boolean
  since?: SettingsVersion
  restoredFrom?: SettingsVersion
  // Where the file as it was is kept.
  copy?: string
  repairedAt?: string
  // The entries that could not be read even one by one ("*": none of it).
  lost?: string[]
}
// What `PUT .../types/{name}` takes: the whole of what the type is to be.
export interface TypeInput {
  // The `rev` of the type this is made from; "" or absent to make one.
  baseRev?: string
  title: string
  fields: { key: string; title?: string; kind?: FieldKind; options?: string[] }[]
}
export interface PatchPropsInput {
  baseRev: string
  set?: Record<string, unknown>
  unset?: string[]
}

// --- saved views (docs/api.md, "Saved views") -------------------------------------------

export type ViewLayout = 'list' | 'cards' | 'table' | 'board' | 'read'
// One saved view. The server always answers with every key; keys it does not know come back as they were
// sent (the app keeps `cardFields` there: the fields a board's card shows).
export interface SavedView {
  id: string
  name: string
  layout: ViewLayout
  // "" is the listing's default order.
  sort: string
  // The field a board is grouped by.
  group: string
  filter: PropFilter[]
  // The keys of the columns a table hides.
  hidden: string[]
  [extra: string]: unknown
}
export interface ViewList {
  views: SavedView[]
  rev: string
}
// A view belongs to a folder ("" is the root) or to a tag.
export type ViewScope = { folder: string } | { tag: string }
export interface PutViewsInput {
  baseRev: string
  // New views have no `id`; the server makes one.
  views: (Omit<SavedView, 'id'> & { id?: string })[]
}

// The answer of `GET .../pages/groups`: how many pages hold each value of a property, for a board's headers.
export interface GroupsResponse {
  groups: { value: string; count: number }[]
  empty: number
  truncated: boolean
}
export interface GroupsParams {
  key: string
  folder?: string
  recursive?: boolean
  tag?: string[]
  filter?: PropFilter[]
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
  // How the change was made, when it did not come from the web app.
  via?: 'mcp' | 'token'
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

export type RemoteCredential =
  | { type: 'ssh'; publicKey: string; fingerprint: string }
  | { type: 'token'; username: string }
  // The server's GitHub App (docs/api.md, "GitHub"): no secret is stored for the space or returned.
  | { type: 'github'; via: 'app'; installationId: number; account: string; repoId: number }
  | { type: 'github'; via: 'user'; user: { id: string; username: string }; login: string; account?: string; repoId: number }

export interface Remote {
  url: string
  branch: string
  credential: RemoteCredential
  state: RemoteState
  // Absent before the first time the server saw the remote.
  lastSync?: string
  // In words fit to show; empty when all is well.
  error: string
  // What `error` state needs, for choosing what to offer: `auth`, `hostkey`, `clone`, `rescued` or a
  // `github_…` code (see GitHubErrorCode). A code this client does not know is treated like `auth`.
  errorCode: string
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
  credential: { type: 'ssh' } | { type: 'token'; username?: string; token?: string } | { type: 'github'; via: GitHubVia }
}

// --- GitHub (docs/api.md, "GitHub") ----------------------------------------------------

export type GitHubVia = 'app' | 'user'
export type GitHubErrorCode = 'github_authorization_expired' | 'github_authorization_missing' | 'github_installation_gone' | 'github_not_configured'

export interface GitHubAuthorization {
  login: string
  githubId: number
  state: 'ok' | 'expired'
  connected: string
  // Absent when the App does not expire user tokens.
  refreshExpires?: string
  // How many spaces sync through this connection.
  spaces: number
}

export interface GitHubInfo {
  app: { name: string; slug: string; htmlUrl: string; installUrl: string } | null
  // Whether people can connect their GitHub account.
  userAuthorization: boolean
  authorization: GitHubAuthorization | null
}

export interface GitHubOwner {
  login: string
  type: 'User' | 'Organization'
  // Whose token creates the repository: the caller's own, or the installation's.
  via: GitHubVia
  installationId: number | null
}

export interface GitHubRepo {
  id: number
  fullName: string
  private: boolean
  defaultBranch: string
  htmlUrl: string
  canPush: boolean
}

export interface GitHubCursorPage<T> {
  items: T[]
  // Opaque; empty after the last page.
  next: string
}

// A new space with its own repository on GitHub (`POST /api/spaces`, `github`).
export interface NewGitHubRepo {
  owner: string
  name: string
  private?: boolean
  description?: string
  // False connects the space to the repository that exists already.
  create?: boolean
}

export interface CreateSpaceInput {
  name: string
  inbox?: string
  github?: NewGitHubRepo
}

// The settings of the server's one GitHub App as an administrator sees them: never a secret.
export type AdminGitHub =
  | { configured: false; callbackUrl: string }
  | {
      configured: true
      appId: string
      clientId: string
      slug: string
      name: string
      owner: string
      htmlUrl: string
      installUrl: string
      privateKey: { set: boolean; fingerprint: string }
      clientSecret: { set: boolean }
      callbackUrl: string
      permissions: Record<string, string>
      // Of administration, contents and metadata: what the App lacks. A warning only.
      missing: string[]
      userAuthorization: boolean
      checked: string
      spaces: number
      authorizations: number
    }

export interface GitHubInstallation {
  id: number
  account: { login: string; type: 'User' | 'Organization'; id: number }
  repositorySelection: 'all' | 'selected'
  suspended: boolean
  permissions: Record<string, string>
  htmlUrl: string
  // The ids of the tenants it is shared with, sorted; none: not shared.
  tenants: string[]
  spaces: number
}

export interface PutAdminGitHubInput {
  appId?: string
  privateKey?: string
  // "" removes the client id and the secret.
  clientId?: string
  clientSecret?: string
}


// A refusal of the daily-text endpoints says why with a stable code in `error`, next to `message`, and the
// thing it is about: header_reenter / header_missing (header), not_json, too_large, path_error /
// path_reveals_secret (field: text|source|author|link), link_uses_header, skip_too_large, bad_language (a language
// tag that is not one), api_not_found (an interface of the default that is not set up), builtin (what a built-in
// interface fixes; field).
export interface ReasonCode {
  code: string
  header?: string
  field?: string
}

export type ErrorCode =
  | 'token_limit'
  | 'quota_exceeded'
  | 'tenants_off'
  | 'tenant_limit'
  | 'tenant_not_empty'
  | 'default_tenant'
  | 'setting_locked'
  | 'setting_restricted'
  | 'mail_not_configured'
  | 'mail_insecure'
  | 'mail_from_required'
  | 'mail_needs_public_url'
  | 'registration_closed'
  | 'password_reset_off'
  | 'email_domain_not_allowed'
  | 'terms_required'
  | 'email_verified'
  | 'email_unverified'
  | 'sso_only_with_registration'
  | 'bad_language'
  | 'api_not_found'
  | 'builtin'
  | 'header_reenter'
  | 'header_missing'
  | 'not_json'
  | 'too_large'
  | 'path_error'
  | 'path_reveals_secret'
  | 'link_uses_header'
  | 'skip_too_large'
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
  | 'github_not_configured'
  | 'github_not_authorized'
  | 'github_owner_not_allowed'
  | 'github_name_taken'
  | 'github_no_permission'
  | 'github_repo_not_in_installation'
  | 'github_push_failed'
  | 'github_key_invalid'
  | 'github_app_rejected'
  | 'github_client_rejected'
  | 'github_app_in_use'
  | 'rate_limited'
  | 'captcha_failed'
  | 'local_login_refused'
  | 'passkey_required'
  | 'passkey_failed'
  | 'recent_login_required'
  | 'too_many_passkeys'
  | 'passkey_unavailable'
  | 'provider_in_use'
  | 'group_not_found'
  | 'group_synced'
  | 'group_not_synced'
  | 'group_limit'
  | 'group_in_use'
  | 'member_via_group'
  | 'unavailable'
  | 'internal'
  | 'network'
  // No answer in the time given (the request was called off; what it did on the server is not known).
  | 'timeout'
  | 'unknown'

// --- accounts -----------------------------------------------------------------

export type Role = 'admin' | 'member'

export interface User {
  id: string
  username: string
  name: string
  email: string
  emailVerified: boolean
  role: Role
  disabled: boolean
  // Whether this person must sign in with a passkey.
  passkeyRequired: boolean
  // An administrator of the whole server (3.3); false for one of a tenant. Absent in single-user mode.
  siteAdmin?: boolean
  // The tenant the person belongs to; absent in single-user mode, which has none.
  tenant?: TenantRef
  // The person's own limits (null follows the tenant); only the administrators' list and PATCH answer have it.
  quota?: OwnQuota
  created: string
  // The users list only: how many passkeys and single sign-on identities the person has.
  passkeys?: number
  sso?: number
}

export interface SessionInfo {
  mode: 'single' | 'accounts'
  user: User | null
  // True while there is no user at all: the app shows the first-run page.
  setup: boolean
  // `passkey`: the signed-in person must register a passkey before anything else.
  hold?: 'passkey' | null
  // Absent in single-user mode.
  methods?: { password: boolean; oidc: { name: string } | null }
  // The API token the request was made with; the app's own requests have none.
  token?: { id: string; name: string; access: TokenAccess; spaces: string[] } | null
}

// An API token (docs/api.md, "API tokens"). The secret is never in one of these.
export type TokenAccess = 'read' | 'write'
export type TokenState = 'active' | 'expired' | 'revoked'
export interface ApiTokenSpace {
  id: string
  // "" for a space that is gone or that the owner has left.
  name: string
}
export interface ApiToken {
  id: string
  name: string
  prefix: string
  access: TokenAccess
  spaces: ApiTokenSpace[]
  created: string
  expires: string | null
  lastUsed: string | null
  revoked: string | null
  state: TokenState
}
// The secret is in the response of the creating request only.
export interface CreatedToken {
  token: ApiToken
  secret: string
}
export interface AdminToken extends ApiToken {
  owner: { id: string; username: string; name: string }
}
export interface AdminTokenList {
  items: AdminToken[]
  next: string
}
// An OAuth grant: one approval of one client (an MCP client, say) by one person
// (docs/api.md, "OAuth for MCP clients"). It works like a token of that access
// and those spaces.
export interface Grant {
  id: string
  client: { name: string; id: string }
  access: TokenAccess
  spaces: ApiTokenSpace[]
  created: string
  // The last refresh; the creation until the first.
  lastUsed: string | null
}
export interface AdminGrant extends Grant {
  owner: { id: string; username: string; name: string }
}
export interface AdminGrantList {
  items: AdminGrant[]
  next: string
}
// What the consent page shows of an authorization request.
export interface ConsentInfo {
  client: { name: string; id: string; host: string }
  redirectHost: string
  // The host may show as letters that look like others.
  punycode: boolean
  scopes: TokenAccess[]
  spaces: { id: string; name: string; permissions: Permission[]; personal: boolean }[]
  user: { id: string; name: string }
}
export interface ConsentDecision {
  req: string
  approve: boolean
  spaces?: string[]
  access?: TokenAccess
}
export interface MachineAccess {
  tokens: boolean
  mcp: boolean
  // Whether the authorization server is there; read only.
  oauth: boolean
}

export type ProviderKind = 'oidc' | 'saml' | 'github'

export interface Identity {
  id: string
  provider: string
  // `''` for an identity whose provider was deleted.
  providerSlug: string
  kind: ProviderKind | ''
  label: string
  created: string
}
export interface Passkey {
  id: string
  name: string
  created: string
  lastUsed?: string | null
  synced: boolean
}
export interface AccountInfo {
  user: User
  hasPassword: boolean
  identities: Identity[]
  passkeys: Passkey[]
  // The groups the person is in, by name.
  groups: GroupRef[]
}

// --- signing in (docs/api.md, "Signing in") -------------------------------------------

export type LoginMode = 'dual' | 'sso_first' | 'sso_redirect' | 'local_only'
export interface PublicProvider {
  slug: string
  kind: ProviderKind
  name: string
  // `''` or `preset:<name>`.
  icon: string
}
export interface AuthProviders {
  providers: PublicProvider[]
  loginMode: LoginMode
  local: boolean
  sso: boolean
  passkey: boolean
  // 3.3: whether the page links to registration and to password reset.
  register?: boolean
  forgot?: boolean
}

// --- mail, registration, email verification and password reset (3.3) ---
export type MailSecurity = 'starttls' | 'tls' | 'none'
export interface MailSettings {
  enabled: boolean
  host: string
  port: number
  security: MailSecurity
  username: string
  hasPassword: boolean
  from: string
  fromName: string
  replyTo: string
  status: { ok: boolean; checked: string | null; message: string }
  queue: { waiting: number; sentLastHour: number; failedLastHour: number }
}
export interface MailPatch {
  enabled?: boolean
  host?: string
  port?: number
  security?: MailSecurity
  username?: string
  // Write only: absent keeps the stored one, '' clears it.
  password?: string
  from?: string
  fromName?: string
  replyTo?: string
}
export interface MailTestResult {
  ok: boolean
  message?: string
}
export type RegisterInfo = { open: false } | { open: true; tenant: { slug: string; name: string }; domains: string[]; termsUrl: string; privacyUrl: string; password: { minLength: number } }
export interface RegisterInput {
  tenant?: string
  username: string
  name: string
  email: string
  password: string
  acceptTerms?: boolean
  captcha?: CaptchaAnswer
  lang?: string
}

export type CaptchaProvider = 'image' | 'turnstile' | 'recaptcha' | 'hcaptcha'
export type CaptchaContext = 'login' | 'invite' | 'setup' | 'forgot' | 'register'
export interface CaptchaChallenge {
  required: boolean
  provider?: CaptchaProvider
  siteKey?: string
  id?: string
  image?: string
}
// What a form sends back: the typed answer of an image, or a widget's token.
export type CaptchaAnswer = { id: string; answer: string } | { token: string }

export interface PasskeyCeremony {
  ceremony: string
  options: Record<string, unknown>
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
  // What a new tenant's first administrator's invite fills in (see InviteCheck).
  username?: string
  name?: string
  email?: string
}
export interface InviteCheck {
  role: Role
  expires: string
  // What the invite starts the form with ("" when it says nothing); the address becomes the account's, not verified.
  username?: string
  name?: string
  email?: string
}

export type OidcIntent = 'login' | 'setup' | 'invite' | 'link'
export type ProviderIntent = OidcIntent

export type BatchPagesInput = { op: 'move'; ids: string[]; folder: string } | { op: 'delete'; ids: string[] }

// --- members (docs/api.md, "Members") -----------------------------------------------

export interface Member {
  userId: string
  username: string
  name: string
  role: SpaceRole
  // True for the caller.
  you: boolean
  // A group of theirs is a member of the space too; the role the person's own
  // role and those groups give together.
  viaGroups?: boolean
  effectiveRole?: SpaceRole
}

// --- groups (docs/api.md, "Groups") --------------------------------------------------

export type GroupSource = 'local' | 'synced'
// What everyone may see of a group.
export interface GroupRef {
  id: string
  name: string
  source: GroupSource
}
// A group as an administrator sees it.
export interface Group extends GroupRef {
  description: string
  people: number
  spaces: number
  mappedFrom: { provider: string; value: string }[]
  created: string
  updated: string
}
export interface GroupList {
  items: Group[]
  next: string
}
export interface GroupPerson {
  userId: string
  username: string
  name: string
  disabled: boolean
  from: { provider: string; value: string }[]
  added: string
}
export interface GroupPersonList {
  items: GroupPerson[]
  next: string
}
export interface GroupSpaceRef {
  id: string
  name: string
  role: SpaceRole
}
export interface GroupSpaceList {
  items: GroupSpaceRef[]
  next: string
}
// A group that is a member of a space.
export interface GroupMember {
  groupId: string
  name: string
  source: GroupSource
  role: SpaceRole
  people: number
  // The caller is in the group.
  yours: boolean
}
export interface MembersResponse {
  members: Member[]
  groups: GroupMember[]
}
export interface EffectiveMember {
  userId: string
  username: string
  name: string
  role: SpaceRole | ''
  directRole: SpaceRole | ''
  groups: { groupId: string; name: string; role: SpaceRole }[]
  you: boolean
}
export interface EffectiveMemberList {
  items: EffectiveMember[]
  next: string
}
// What a provider does with its groups claim.
export interface GroupsMapping {
  enabled: boolean
  claim: string
  missing: 'keep' | 'clear'
  map: { value: string; groupId: string }[]
}

// --- administration (docs/api.md, "Administration") -----------------------------------

export interface AdminSpace {
  id: string
  name: string
  personal: boolean
  // The folder quick notes go to.
  inbox: string
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

// --- home: the daily text and favourites (docs/api.md, "Home") ---------------------

// Where a space takes its daily text from: a folder (and those below it), a tag or a type, with filters.
export interface DailySource {
  folder?: string
  tag?: string
  type?: string
  filter?: PropFilter[]
}
// A source for every language beside sources by language (the language codes are lower-case keys).
export type DailySettings = DailySource & { [lang: string]: DailySource | string | PropFilter[] | undefined }

export interface DailyQuote {
  text: string
  source?: string
  author?: string
  link?: string
  fetched?: string
  stale?: boolean
}
export type DailyReason = 'no_source' | 'no_match' | 'unavailable' | 'out_of_range'
// What `GET …/daily` says: a page of the space, a text from the administrators, or nothing to show.
export interface DailyAnswer {
  page: (PageSummary & { body: string; bodyTruncated?: boolean }) | null
  count?: number
  quote?: DailyQuote
  origin?: 'space' | 'external' | 'site'
  reason?: DailyReason
}

export interface DailyPrefs {
  inherit: boolean
  apis: string[]
  // A built-in interface that answers in more than one language lists them in `languages`.
  available: { id: string; name: string; lang: string; languages?: string[] }[]
  // The languages the settings pages have a tab for (besides "all languages").
  languages?: string[]
  default: { kind: string } | null
  // What the administrators let spaces do (absent: everything). A field that is `false` is set for the whole
  // server: the space's page shows it disabled and says so.
  allowed?: { own?: boolean; api?: boolean }
}
export interface DailyAllow {
  own: boolean
  api: boolean
}

export type FavoriteKind = 'page' | 'folder' | 'view'
export interface Favorite {
  kind: FavoriteKind
  target: string
  name: string
  path?: string
  // A view: the folder or tag it belongs to.
  of?: string
  ofKind?: 'folder' | 'tag'
  missing: boolean
  created: string
}
export interface FavoriteList {
  items: Favorite[]
  limit: number
}
export interface FavoriteRef {
  kind: FavoriteKind
  target: string
}

export interface DailyApiHeader {
  name: string
  set?: boolean
  value?: string
}
export interface DailyApi {
  id: string
  name: string
  lang: string
  url: string
  headers: DailyApiHeader[]
  text: string
  source: string
  author: string
  link: string
  refresh: 'daily' | 'hourly' | 'minutes'
  refreshMinutes: number
  skipNew: boolean
  rps: number
  enabled: boolean
  // How a language is written in {lang} for this service: {"zh-Hans": "zh"}.
  langMap?: Record<string, string>
  // A built-in interface: its address, paths, link and language mapping are fixed.
  builtin?: boolean
  // The languages a built-in interface answers in (lang is the first).
  languages?: string[]
}
// A service the server knows how to ask: switched on and off, no address to fill in.
export interface DailyProvider {
  id: string
  name: string
  languages: string[]
  site: string
  note: string
  enabled: boolean
  refresh: 'daily' | 'hourly' | 'minutes'
  minutes: number
  rps: number
  maxRps?: number
  skipNew: boolean
}
export interface KnownLanguage {
  tag: string
  name: string
  english: string
}
export type DailyDefault = null | { kind: 'list' } | { kind: 'space'; space: string; source: DailySettings } | { kind: 'api'; apis: Record<string, string> }
export interface DailyNotice {
  code: 'builtin_adopted' | 'builtin_renamed' | 'builtin_kept' | 'dropped_language' | 'unreadable' | (string & {})
  id?: string
  to?: string
  lang?: string
  field?: string
  message: string
}
export interface AdminDaily {
  default: DailyDefault
  languages: string[]
  // The common languages to pick from; any valid tag may still be typed.
  knownLanguages: KnownLanguage[]
  apis: DailyApi[]
  providers: DailyProvider[]
  lines: { total: number; byLang: Record<string, number>; max: number }
  // What the server changed in the settings by itself, reading what an earlier version saved; absent from older servers.
  notices?: DailyNotice[]
  // Whether spaces may use a source of their own and pick an external interface; absent until the server has them.
  allow?: DailyAllow
}
export interface DailyTryResult {
  ok: boolean
  status?: number
  url?: string
  message?: string
  // Why it did not work, as a code (see ReasonCode), with what it is about.
  error?: string
  header?: string
  field?: string
  raw?: string
  rawTruncated?: boolean
  quote?: DailyQuote
  pathErrors?: { field: string; code: string; message: string }[]
}
export type LinesFormat = 'lines' | 'markdown'

// --- security settings, providers and the audit log (docs/api.md, "Administration") ------------

export type LockoutScope = 'ip_account' | 'ip' | 'account'
export type CaptchaWhen = 'always' | 'after_failures'
export interface CaptchaContextSettings {
  on: boolean
  when: CaptchaWhen
  failures: number
}
export interface SecurityNotice {
  code: 'flags_ignored' | 'secret_unreadable' | 'passkey_host_changed' | (string & {})
  message: string
  slug?: string
  field?: string
}
export interface SecuritySettings {
  login: { mode: LoginMode; effectiveMode: LoginMode; ssoOnly: boolean; ssoOnlyInEffect: boolean; ssoAvailable: boolean }
  passkeys: { enabled: boolean; available: boolean; requireRoles: Role[]; required: number; requiredWithout: number }
  captcha: { provider: CaptchaProvider; siteKey: string; hasSecret: boolean; contexts: Record<CaptchaContext, CaptchaContextSettings> }
  lockout: { enabled: boolean; minutes: number; scope: LockoutScope }
  limits: { perMinute: number; maxFailures: number; windowMinutes: number; sessionIdleDays: number; sessionMaxDays: number }
  passwordAdmins: number
  notices: SecurityNotice[]
}
// What `PUT /api/admin/security` takes: any block, any field of it; `captcha.secret` is write-only.
export interface SecurityPatch {
  login?: Partial<{ mode: LoginMode; ssoOnly: boolean }>
  passkeys?: Partial<{ enabled: boolean; requireRoles: Role[] }>
  captcha?: Partial<{ provider: CaptchaProvider; siteKey: string; secret: string; contexts: Partial<Record<CaptchaContext, Partial<CaptchaContextSettings>>> }>
  lockout?: Partial<{ enabled: boolean; minutes: number; scope: LockoutScope }>
  limits?: Partial<SecuritySettings['limits']>
}

export interface OidcSettings {
  issuer: string
  clientId: string
  hasSecret: boolean
  scopes: string[]
  legacyCallback: boolean
  redirectUrl: string
}
export interface SamlSettings {
  metadataUrl: string
  metadataXml: string
  idpEntityId: string
  entityId: string
  acsUrl: string
  metadataPath: string
  attributes: { email: string; name: string; username: string }
  nameIdFormat: string
  signRequests: boolean
  allowIdpInitiated: boolean
  certificate: string
  certificateExpires: string
}
export interface GithubSettings {
  baseUrl: string
  clientId: string
  hasSecret: boolean
  organizations: string[]
  redirectUrl: string
}
export interface AuthProvider {
  slug: string
  kind: ProviderKind
  name: string
  icon: string
  enabled: boolean
  order: number
  autoCreate: boolean
  mergeVerifiedEmail: boolean
  groups: GroupsMapping
  oidc?: OidcSettings
  saml?: SamlSettings
  github?: GithubSettings
  status: { ok: boolean; checked: string; message: string }
  people: number
  created: string
  updated: string
}
// What a create or a patch sends: the common fields and the kind's own block, with `clientSecret` where there is one.
export interface ProviderInput {
  slug?: string
  kind?: ProviderKind
  name?: string
  icon?: string
  enabled?: boolean
  order?: number
  autoCreate?: boolean
  mergeVerifiedEmail?: boolean
  groups?: Partial<GroupsMapping>
  oidc?: Partial<Pick<OidcSettings, 'issuer' | 'clientId' | 'scopes' | 'legacyCallback'>> & { clientSecret?: string }
  saml?: Partial<Pick<SamlSettings, 'metadataUrl' | 'metadataXml' | 'entityId' | 'attributes' | 'nameIdFormat' | 'signRequests' | 'allowIdpInitiated'>>
  github?: Partial<Pick<GithubSettings, 'baseUrl' | 'clientId' | 'organizations'>> & { clientSecret?: string }
}

export type AuditKind =
  | 'login'
  | 'login_failed'
  | 'rate_limited'
  | 'lockout'
  | 'passkey_clone'
  | 'identity_linked'
  | 'identity_unlinked'
  | 'passkey_added'
  | 'passkey_removed'
  | 'security_changed'
  | 'provider_created'
  | 'provider_changed'
  | 'provider_deleted'
  | 'user_changed'
  | 'token_created'
  | 'token_revoked'
  | 'grant_revoked'
  | 'group_created'
  | 'group_changed'
  | 'group_deleted'
  | 'group_member_added'
  | 'group_member_removed'
  | 'groups_synced'
  | 'groups_claim_missing'
  | 'groups_claim_truncated'
  | 'space_group_added'
  | 'space_group_changed'
  | 'space_group_removed'
  | 'dropped'
export interface AuditItem {
  id: string
  at: string
  kind: AuditKind | (string & {})
  actor: string | null
  user: string | null
  username: string | null
  ip: string
  detail: Record<string, unknown>
}
export interface AuditList {
  items: AuditItem[]
  next: string
}

// --- tenants and layered settings (3.3, docs/api.md "Tenants, registration and mail") ---

export interface TenantRef {
  id: string
  slug: string
  name: string
}
export type TenantQuotaKey = 'people' | 'spaces' | 'bytes' | 'pages'
// What a tenant uses, as measured (docs/api.md, "Quotas"); `over` lists the totals it is over.
export interface TenantUsage {
  people: number
  spaces: number
  bytes: number
  pages: number
  measuredAt: string | null
  over: TenantQuotaKey[]
}
export interface Tenant extends TenantRef {
  default: boolean
  disabled: boolean
  registration: { open: boolean }
  quota: Record<TenantQuotaKey, number | null>
  usage: TenantUsage
  created: string
  updated: string
}
export interface TenantList {
  items: Tenant[]
  next: string
}
export interface Tenancy {
  enabled: boolean
  tenants: number
}
export type CreatedTenant = Tenant & { invite?: CreatedInvite }

// --- quotas and usage (3.3) ---

export type PersonQuotaKey = 'spaces' | 'bytes' | 'pages'
// A person's own limits: a number, or null to follow the tenant and the site.
export type OwnQuota = Record<PersonQuotaKey, number | null>
// A quota that is over: the person's own (scope person) or the tenant's total (scope tenant).
export interface UsageNotice {
  quota: TenantQuotaKey
  scope: 'person' | 'tenant'
  limit: number
  used: number
}
// GET /api/account/usage. limit null is none.
export interface AccountUsage {
  spaces: { used: number; limit: number | null }
  bytes: { used: number; limit: number | null }
  pages: { used: number; limit: number | null }
  measuredAt: string | null
  notices: UsageNotice[]
}
export interface PersonUsage {
  user: { id: string; username: string; name: string }
  tenant: string
  spaces: number
  bytes: number
  pages: number
  limits: Record<PersonQuotaKey, number | null>
  // The person's own limits (null follows the tenant), what the edit form starts with.
  own: OwnQuota
  // false for an own limit a lock or restriction put on the setting later forbids (kept, in force again when lifted).
  ownInEffect: Record<PersonQuotaKey, boolean>
  over: PersonQuotaKey[]
  measuredAt: string | null
}
export interface AdminUsageList {
  items: PersonUsage[]
  next: string
}

export type SettingLayer = 'site' | 'tenant' | 'space' | 'person'
export type SettingKind = 'boolean' | 'choice' | 'limit' | 'list' | 'text' | 'doc'
// What a layer lets the layers below it do.
export interface SettingLower {
  mode: 'allow' | 'restrict' | 'lock'
  options?: (string | boolean)[]
  max?: number
}
export interface SiteSetting {
  key: string
  kind: SettingKind
  layers: SettingLayer[]
  site: { value: unknown; lower: SettingLower; fromFlag?: boolean }
  // Set by PUT when the new rule makes this many tenants' values ineffective.
  affected?: number
}
export interface TenantSetting {
  key: string
  kind: SettingKind
  layers: SettingLayer[]
  site: { value: unknown; lower: SettingLower }
  tenant: { value: unknown; lower: SettingLower | null; inEffect: boolean } | null
  effective: unknown
}
// What GET /api/settings says of a setting that a space or a person can change.
export interface MySetting {
  key: string
  effective: unknown
  space?: { canChange: boolean; inEffect: boolean; source?: 'site' | 'tenant' }
  person?: { canChange: boolean; inEffect: boolean; source?: 'site' | 'tenant' }
}
