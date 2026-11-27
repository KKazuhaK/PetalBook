// What only an administrator's pages use: the calls and the hooks over them. A
// module of its own so the administration, which loads on demand, carries them and
// the main bundle does not. (One space an administrator opens without being a member
// of it is `api.adminSpace`: the space list needs it.)

import { useMemo } from 'react'
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
  type UseQueryResult,
} from '@tanstack/react-query'
import { API_BASE, ApiError, enc, request } from './client'
import { keys } from './hooks'
import { SCOPE_PARAM } from '@/lib/adminScope'
import {
  dailyOf,
  dailyWrites,
  K,
  machineOf,
  securityOf,
  storageOf,
  storageWrites,
  type SettingStep,
} from './settingsFold'
import type {
  AdminForge,
  AdminProviderRow,
  CreateAdminForgeInput,
  PatchAdminForgeInput,
  AdminStorage,
  AdminStorageBody,
  AdminSpace,
  PutAdminStorage,
  MailPatch,
  MailSettings,
  MailTestResult,
  CreatedTenant,
  AdminUsageList,
  OwnQuota,
  PermissionInfo,
  RuleWrite,
  SettingLower,
  SiteSetting,
  SecretMeta,
  Tenancy,
  Tenant,
  TenantList,
  TenantOverview,
  TenantOrigin,
  TenantSetting,
  TenantSettingWritten,
  Group,
  GroupList,
  GroupPersonList,
  GroupSource,
  GroupSpaceList,
  AdminDaily,
  AdminDailyBody,
  AdminSpaceList,
  AuditFilters,
  AuditKinds,
  AuditList,
  AuditPurgeInput,
  AuditPurgeCount,
  AuditPurgeStarted,
  AuthProvider,
  Passkey,
  ProviderInput,
  CaptchaCredentials,
  SecurityBody,
  SecuritySettings,
  DailyAllow,
  AdminSpacesParams,
  CreatedInvite,
  DailyApi,
  DailyDefault,
  DailyProvider,
  DailyTryResult,
  RecycleItem,
  Invite,
  DailyLine,
  LinesFormat,
  Role,
  ServerStatus,
  SpaceSize,
  User,
  UsersPage,
  UsersParams,
  Link,
  ProviderTestResult,
  PersonMemberships,
  PersonSession,
  AdminGrantList,
  AdminTokenList,
  AdminGitHub,
  ClearedRule,
  CursorPage,
  Plan,
  PlanRef,
  PlanWrite,
  GitHubInstallation,
  GitHubManifestStart,
  PutAdminGitHubInput,
} from './types'

export const adminApi = {
  // --- users and invites ---
  // `q` keeps the users whose username or name contains it; `limit` caps the count (1 to 200);
  // `enabled` leaves out the disabled accounts before the limit counts.
  users: (params: UsersParams & { enabled?: boolean } = {}, signal?: AbortSignal) => {
    const q = new URLSearchParams()
    if (params.q) q.set('q', params.q)
    if (params.limit) q.set('limit', String(params.limit))
    if (params.cursor) q.set('cursor', params.cursor)
    if (params.sort) q.set('sort', params.sort)
    if (params.order) q.set('order', params.order)
    if (params.role) q.set('role', params.role)
    if (params.state) q.set('state', params.state)
    if (params.inactiveDays !== undefined) q.set('inactiveDays', String(params.inactiveDays))
    if (params.activeDays !== undefined) q.set('activeDays', String(params.activeDays))
    if (params.tenant) q.set('tenant', params.tenant)
    if (params.enabled) q.set('enabled', '1')
    const query = q.toString()
    return request<UsersPage>(`/users${query ? `?${query}` : ''}`, { signal })
  },
  // One person, as the list shows them.
  user: (id: string, signal?: AbortSignal) => request<{ user: User }>(`/users/${enc(id)}`, { signal }),
  // The spaces they are a member of and their groups.
  userMemberships: (id: string, signal?: AbortSignal) =>
    request<PersonMemberships>(`/users/${enc(id)}/memberships`, { signal }),
  // Site administrators only (a tenant's administrator is answered 404): the sessions and the connected accounts.
  userSessions: (id: string, signal?: AbortSignal) =>
    request<{ sessions: PersonSession[] }>(`/users/${enc(id)}/sessions`, { signal }),
  endUserSession: (id: string, session: string) =>
    request<void>(`/users/${enc(id)}/sessions/${enc(session)}`, { method: 'DELETE' }),
  endUserSessions: (id: string) => request<{ ended: number }>(`/users/${enc(id)}/sessions`, { method: 'DELETE' }),
  userLinks: (id: string, signal?: AbortSignal) => request<{ links: Link[] }>(`/users/${enc(id)}/links`, { signal }),
  disconnectUserLink: (id: string, link: string) =>
    request<void>(`/users/${enc(id)}/links/${enc(link)}`, { method: 'DELETE' }),
  // `quota` sets the person's own limits: a number each, or null to follow the tenant; the keys left out stay.
  // `remoteExempt` (site administrators) leaves the spaces the person owns out of the requirement of a remote.
  updateUser: (
    id: string,
    input: {
      name?: string
      username?: string
      role?: Role
      disabled?: boolean
      passkeyRequired?: boolean
      email?: string
      // What to do with a new `email`: send the person a link that proves it (the default) or say it is verified.
      emailMode?: 'verify' | 'mark'
      emailVerified?: boolean
      siteAdmin?: boolean
      quota?: Partial<OwnQuota>
      remoteExempt?: boolean
    },
  ) =>
    request<{ user: User; verification?: 'sent' | 'not_sent' }>(`/users/${enc(id)}`, { method: 'PATCH', body: input }),
  // The passkeys of one person, for an administrator who removes a lost one.
  userPasskeys: (id: string, signal?: AbortSignal) =>
    request<{ passkeys: Passkey[] }>(`/users/${enc(id)}/passkeys`, { signal }),
  deleteUserPasskey: (id: string, passkey: string) =>
    request<void>(`/users/${enc(id)}/passkeys/${enc(passkey)}`, { method: 'DELETE' }),
  createInvite: (input: { role?: Role; days?: number } = {}) =>
    request<CreatedInvite>('/invites', { method: 'POST', body: input }),
  invites: (signal?: AbortSignal) => request<{ invites: Invite[] }>('/invites', { signal }),
  revokeInvite: (id: string) => request<void>(`/invites/${enc(id)}`, { method: 'DELETE' }),

  // --- spaces ---
  adminSpaces: (params: AdminSpacesParams = {}, signal?: AbortSignal) => {
    const q = new URLSearchParams()
    if (params.q) q.set('q', params.q)
    if (params.cursor) q.set('cursor', params.cursor)
    if (params.limit) q.set('limit', String(params.limit))
    if (params.cache) q.set('cache', params.cache)
    if (params.overdue) q.set('overdue', '1')
    if (params.sort) q.set('sort', params.sort)
    if (params.order) q.set('order', params.order)
    if (params.tenant) q.set('tenant', params.tenant)
    if (params.owner) q.set('owner', params.owner)
    if (params.remote) q.set('remote', params.remote)
    if (params.deadline) q.set('deadline', params.deadline)
    if (params.pinned) q.set('pinned', '1')
    if (params.exempt) q.set('exempt', '1')
    if (params.idleDays) q.set('idleDays', String(params.idleDays))
    const qs = q.toString()
    return request<AdminSpaceList>(`/admin/spaces${qs ? `?${qs}` : ''}`, { signal })
  },
  // The site's storage policy (docs/api.md, "Server storage").
  adminStorage: (signal?: AbortSignal) => request<AdminStorageBody>('/admin/storage', { signal }),
  // Keeps a space's files on the server (pin), or leaves it out of the requirement of a remote (exempt).
  putSpaceStorage: (id: string, input: { pin?: boolean; exempt?: boolean }) =>
    request<AdminSpace>(`/admin/spaces/${enc(id)}/storage`, { method: 'PUT', body: input }),
  // Evicts the space now, if it may be.
  evictSpace: (id: string) => request<void>(`/admin/spaces/${enc(id)}/evict`, { method: 'POST' }),
  // Measures the space's directory now.
  adminResizeSpace: (id: string) => request<SpaceSize>(`/admin/spaces/${enc(id)}/size`, { method: 'POST' }),
  adminTransferSpace: (id: string, userId: string) =>
    request<void>(`/admin/spaces/${enc(id)}/transfer`, { method: 'POST', body: { userId } }),
  // Makes the caller the owner of a space they manage without being a member; audited, and the owners are told.
  adminTakeOverSpace: (id: string, reason: string) =>
    request<void>(`/admin/spaces/${enc(id)}/takeover`, { method: 'POST', body: { reason } }),
  // The registry of permission keys.
  adminPermissions: (signal?: AbortSignal) =>
    request<{ permissions: PermissionInfo[] }>('/admin/permissions', { signal }),
  adminDisableSpace: (id: string) => request<void>(`/admin/spaces/${enc(id)}/disable`, { method: 'POST' }),
  adminEnableSpace: (id: string) => request<void>(`/admin/spaces/${enc(id)}/enable`, { method: 'POST' }),
  // Deleting recycles the space (3.5): the answer is the recycle item. `reason` is for the audit log.
  adminDeleteSpace: (id: string, reason = '') =>
    request<RecycleItem>(`/admin/spaces/${enc(id)}`, { method: 'DELETE', body: reason ? { reason } : {} }),
  adminStatus: (signal?: AbortSignal) => request<ServerStatus>('/admin/status', { signal }),

  // --- usage (3.3): people by bytes, in the caller's scope; a site administrator may narrow to one tenant ---
  usage: (params: { tenant?: string; over?: boolean; cursor?: string; limit?: number } = {}, signal?: AbortSignal) => {
    const q = new URLSearchParams()
    if (params.tenant) q.set('tenant', params.tenant)
    if (params.over) q.set('over', '1')
    if (params.cursor) q.set('cursor', params.cursor)
    if (params.limit) q.set('limit', String(params.limit))
    const qs = q.toString()
    return request<AdminUsageList>(`/admin/usage${qs ? `?${qs}` : ''}`, { signal })
  },

  // --- mail and password reset (3.3) ---
  mail: (signal?: AbortSignal) => request<MailSettings>('/admin/mail', { signal }),
  putMail: (input: MailPatch) => request<MailSettings>('/admin/mail', { method: 'PUT', body: input }),
  testMail: (to: string, lang?: string) =>
    request<MailTestResult>('/admin/mail/test', { method: 'POST', body: { to, ...(lang ? { lang } : {}) } }),
  // A password the administrator typed: the person must replace it at their next sign-in, and their sessions end.
  setTemporaryPassword: (id: string, password: string) =>
    request<void>(`/users/${enc(id)}/password`, { method: 'POST', body: { password } }),
  sendPasswordReset: (id: string, lang?: string) =>
    request<{ sent: boolean }>(`/users/${enc(id)}/password-reset`, { method: 'POST', body: lang ? { lang } : {} }),

  // --- tenants (3.3) ---
  tenants: (
    params: { q?: string; origin?: TenantOrigin; cursor?: string; limit?: number } = {},
    signal?: AbortSignal,
  ) => {
    const q = new URLSearchParams()
    if (params.q) q.set('q', params.q)
    if (params.origin) q.set('origin', params.origin)
    if (params.cursor) q.set('cursor', params.cursor)
    if (params.limit) q.set('limit', String(params.limit))
    const qs = q.toString()
    return request<TenantList>(`/admin/tenants${qs ? `?${qs}` : ''}`, { signal })
  },
  tenant: (id: string, signal?: AbortSignal) => request<Tenant>(`/admin/tenants/${enc(id)}`, { signal }),
  tenantOverview: (id: string, signal?: AbortSignal) =>
    request<TenantOverview>(`/admin/tenants/${enc(id)}/overview`, { signal }),
  createTenant: (input: { slug: string; name: string; admin?: { username: string; name?: string; email?: string } }) =>
    request<CreatedTenant>('/admin/tenants', { method: 'POST', body: input }),
  updateTenant: (id: string, input: { name?: string; slug?: string; disabled?: boolean }) =>
    request<Tenant>(`/admin/tenants/${enc(id)}`, { method: 'PATCH', body: input }),
  deleteTenant: (id: string) => request<void>(`/admin/tenants/${enc(id)}`, { method: 'DELETE' }),
  // --- secrets: write-only; a tenant's is under its own path ---
  secrets: (tenant: string | undefined, signal?: AbortSignal) =>
    request<{ secrets: SecretMeta[] }>(tenant ? `/admin/tenants/${enc(tenant)}/secrets` : '/admin/secrets', { signal }),
  putSecret: (tenant: string | undefined, name: string, value: string) =>
    request<{ secrets: SecretMeta[] }>(`${tenant ? `/admin/tenants/${enc(tenant)}` : '/admin'}/secrets/${enc(name)}`, {
      method: 'PUT',
      body: { value },
    }),
  deleteSecret: (tenant: string | undefined, name: string) =>
    request<{ secrets: SecretMeta[] }>(`${tenant ? `/admin/tenants/${enc(tenant)}` : '/admin'}/secrets/${enc(name)}`, {
      method: 'DELETE',
    }),
  // --- layered settings (3.3) ---
  plans: (signal?: AbortSignal) => request<{ plans: Plan[] }>('/admin/plans', { signal }),
  createPlan: (input: PlanWrite & { slug: string; name: string }) =>
    request<Plan>('/admin/plans', { method: 'POST', body: input }),
  updatePlan: (slug: string, input: PlanWrite) =>
    request<Plan & { affected: string[] }>(`/admin/plans/${enc(slug)}`, { method: 'PATCH', body: input }),
  deletePlan: (slug: string) => request<void>(`/admin/plans/${enc(slug)}`, { method: 'DELETE' }),
  // Gives a tenant a plan (null: none). Every manual rule of the tenant is cleared, and the answer lists them.
  putTenantPlan: (tenant: string, plan: string | null) =>
    request<{ plan: PlanRef | null; cleared: ClearedRule[] }>(`/admin/tenants/${enc(tenant)}/plan`, {
      method: 'PUT',
      body: { plan },
    }),
  siteSettings: (signal?: AbortSignal) => request<{ settings: SiteSetting[] }>('/admin/settings', { signal }),
  putSiteSetting: (key: string, input: { value?: unknown; lower?: SettingLower | null }) =>
    request<SiteSetting>(`/admin/settings/${enc(key)}`, { method: 'PUT', body: input }),
  tenantSettings: (tenant: string, signal?: AbortSignal) =>
    request<{ settings: TenantSetting[] }>(`/admin/tenants/${enc(tenant)}/settings`, { signal }),
  putTenantSetting: (
    tenant: string,
    key: string,
    input: { value?: unknown; lower?: SettingLower | null; siteLock?: boolean; siteRule?: RuleWrite | null },
  ) =>
    request<TenantSettingWritten>(`/admin/tenants/${enc(tenant)}/settings/${enc(key)}`, { method: 'PUT', body: input }),

  // --- groups ---
  adminGroups: (
    params: { q?: string; source?: GroupSource | ''; user?: string; cursor?: string; limit?: number } = {},
    signal?: AbortSignal,
  ) => {
    const q = new URLSearchParams()
    if (params.q) q.set('q', params.q)
    if (params.source) q.set('source', params.source)
    if (params.user) q.set('user', params.user)
    if (params.cursor) q.set('cursor', params.cursor)
    if (params.limit) q.set('limit', String(params.limit))
    const qs = q.toString()
    return request<GroupList>(`/admin/groups${qs ? `?${qs}` : ''}`, { signal })
  },
  createGroup: (input: { name: string; description?: string; source: GroupSource }) =>
    request<Group>('/admin/groups', { method: 'POST', body: input }),
  updateGroup: (id: string, input: { name?: string; description?: string }) =>
    request<Group>(`/admin/groups/${enc(id)}`, { method: 'PATCH', body: input }),
  deleteGroup: (id: string, force = false) =>
    request<void>(`/admin/groups/${enc(id)}${force ? '?force=1' : ''}`, { method: 'DELETE' }),
  groupPeople: (id: string, params: { q?: string; cursor?: string; limit?: number } = {}, signal?: AbortSignal) => {
    const q = new URLSearchParams()
    if (params.q) q.set('q', params.q)
    if (params.cursor) q.set('cursor', params.cursor)
    if (params.limit) q.set('limit', String(params.limit))
    const qs = q.toString()
    return request<GroupPersonList>(`/admin/groups/${enc(id)}/members${qs ? `?${qs}` : ''}`, { signal })
  },
  // 1 to 500 users, all or nothing.
  addGroupPeople: (id: string, userIds: string[]) =>
    request<{ added: number; already: number }>(`/admin/groups/${enc(id)}/members`, {
      method: 'POST',
      body: { userIds },
    }),
  removeGroupPerson: (id: string, userId: string) =>
    request<void>(`/admin/groups/${enc(id)}/members/${enc(userId)}`, { method: 'DELETE' }),
  groupSpaces: (id: string, params: { cursor?: string; limit?: number } = {}, signal?: AbortSignal) => {
    const q = new URLSearchParams()
    if (params.cursor) q.set('cursor', params.cursor)
    if (params.limit) q.set('limit', String(params.limit))
    const qs = q.toString()
    return request<GroupSpaceList>(`/admin/groups/${enc(id)}/spaces${qs ? `?${qs}` : ''}`, { signal })
  },

  // --- security: settings, sign-in providers and the audit log ---
  security: (signal?: AbortSignal) => request<SecurityBody>('/admin/security', { signal }),
  // The captcha's credentials, the one thing this endpoint writes; `secret` is write-only ("" clears it).
  putSecurity: (captcha: CaptchaCredentials) =>
    request<SecurityBody>('/admin/security', { method: 'PUT', body: { captcha } }),
  authProviders: (signal?: AbortSignal) => request<{ providers: AuthProvider[] }>('/admin/providers', { signal }),
  createAuthProvider: (input: ProviderInput) =>
    request<AuthProvider>('/admin/providers', { method: 'POST', body: input }),
  patchAuthProvider: (id: string, input: ProviderInput) =>
    request<AuthProvider>(`/admin/providers/${enc(id)}`, { method: 'PATCH', body: input }),
  deleteAuthProvider: (id: string, force = false) =>
    request<void>(`/admin/providers/${enc(id)}${force ? '?force=1' : ''}`, { method: 'DELETE' }),
  checkAuthProvider: (id: string) => request<AuthProvider>(`/admin/providers/${enc(id)}/check`, { method: 'POST' }),
  // A test sign-in: the address to send the browser to, even while the provider is off. It comes back to
  // /admin/security/providers?provider_test=<id>, where the result is read once.
  testAuthProvider: (id: string) => request<{ url: string }>(`/admin/providers/${enc(id)}/test`, { method: 'POST' }),
  authProviderTestResult: (id: string, signal?: AbortSignal) =>
    request<{ result: ProviderTestResult | null }>(`/admin/providers/${enc(id)}/test`, { signal }),
  audit: (params: Partial<AuditFilters> & { cursor?: string; limit?: number } = {}, signal?: AbortSignal) => {
    const q = auditQuery(params)
    if (params.cursor) q.set('cursor', params.cursor)
    if (params.limit) q.set('limit', String(params.limit))
    const qs = q.toString()
    return request<AuditList>(`/admin/audit${qs ? `?${qs}` : ''}`, { signal })
  },
  // The registry of kinds with their categories, and which optional categories are on in the caller's scope.
  auditKinds: (signal?: AbortSignal) => request<AuditKinds>('/admin/audit/kinds', { signal }),
  // Counts what a purge by hand would delete (site administrators), at most a million; deletes nothing.
  countAuditPurge: (input: AuditPurgeInput, signal?: AbortSignal) =>
    request<AuditPurgeCount>('/admin/audit/purge?dry_run=true', { method: 'POST', body: input, signal }),
  // Starts the purge: a job (202) that deletes in the background; follow it with useJob.
  purgeAudit: (input: AuditPurgeInput, signal?: AbortSignal) =>
    request<AuditPurgeStarted>('/admin/audit/purge', { method: 'POST', body: input, signal }),
  // The address of the CSV of a filter (the browser downloads it; the server streams it). A link cannot carry the scope
  // header, so in a tenant's scope the address names it (?scope=, which the server reads as the header).
  auditDownloadUrl: (filters: Partial<AuditFilters>, scope: string | null = null) => {
    const q = auditQuery(filters)
    if (scope) q.set(SCOPE_PARAM, `tenant:${scope}`)
    const qs = q.toString()
    return `${API_BASE}/admin/audit/download${qs ? `?${qs}` : ''}`
  },

  // --- machine access: API tokens ---
  adminTokens: (params: { cursor?: string; limit?: number } = {}, signal?: AbortSignal) => {
    const q = new URLSearchParams()
    if (params.cursor) q.set('cursor', params.cursor)
    if (params.limit) q.set('limit', String(params.limit))
    const qs = q.toString()
    return request<AdminTokenList>(`/admin/tokens${qs ? `?${qs}` : ''}`, { signal })
  },
  adminRevokeToken: (id: string) => request<void>(`/admin/tokens/${enc(id)}`, { method: 'DELETE' }),
  adminGrants: (params: { cursor?: string; limit?: number } = {}, signal?: AbortSignal) => {
    const q = new URLSearchParams()
    if (params.cursor) q.set('cursor', params.cursor)
    if (params.limit) q.set('limit', String(params.limit))
    const qs = q.toString()
    return request<AdminGrantList>(`/admin/grants${qs ? `?${qs}` : ''}`, { signal })
  },
  adminRevokeGrant: (id: string) => request<void>(`/admin/grants/${enc(id)}`, { method: 'DELETE' }),

  // --- the daily text of the whole server ---
  daily: (signal?: AbortSignal) => request<AdminDailyBody>('/admin/daily', { signal }),
  // A header with a `value` sets it; one with only a `name` keeps what is saved; a header left out is removed.
  putDailyApi: (api: Omit<DailyApi, 'note'>) =>
    request<AdminDailyBody>(`/admin/daily/apis/${enc(api.id)}`, { method: 'PUT', body: api }),
  putDailyProvider: (
    id: string,
    input: { enabled: boolean; refresh?: DailyProvider['refresh']; minutes?: number; rps?: number; skipNew?: boolean },
  ) => request<AdminDailyBody>(`/admin/daily/providers/${enc(id)}`, { method: 'PUT', body: input }),
  deleteDailyApi: (id: string) => request<AdminDailyBody>(`/admin/daily/apis/${enc(id)}`, { method: 'DELETE' }),
  // A refused trial is a 400 that carries what the trial showed in `result`; it reads as a trial with errors.
  tryDailyApi: async (api: Omit<DailyApi, 'note'>, lang: string, date: string): Promise<DailyTryResult> => {
    try {
      return await request<DailyTryResult>('/admin/daily/try', { method: 'POST', body: { api, lang, date } })
    } catch (e) {
      const b =
        e instanceof ApiError && e.status === 400
          ? (e.body as { result?: unknown; error?: string; field?: string; header?: string } | undefined)
          : undefined
      if (b && typeof b.result === 'object' && b.result !== null)
        return { ...(b.result as DailyTryResult), error: b.error, field: b.field, header: b.header }
      throw e
    }
  },
  // --- the hosts of GitLab and Gitea an administrator sets up ---
  adminConnectionProviders: (signal?: AbortSignal) =>
    request<{ providers: AdminProviderRow[] }>('/admin/connections', { signal }),
  createAdminForge: (input: CreateAdminForgeInput) =>
    request<AdminForge>('/admin/connections', { method: 'POST', body: input }),
  adminForge: (id: string, signal?: AbortSignal) => request<AdminForge>(`/admin/connections/${enc(id)}`, { signal }),
  patchAdminForge: (id: string, input: PatchAdminForgeInput) =>
    request<AdminForge>(`/admin/connections/${enc(id)}`, { method: 'PATCH', body: input }),
  checkAdminForge: (id: string) =>
    request<AdminForge>(`/admin/connections/${enc(id)}/check`, { method: 'POST', body: {} }),
  deleteAdminForge: (id: string) => request<void>(`/admin/connections/${enc(id)}`, { method: 'DELETE' }),
  // --- the GitHub App of the whole server ---
  adminGitHub: (signal?: AbortSignal) => request<AdminGitHub>('/admin/connections/github', { signal }),
  putAdminGitHub: (input: PutAdminGitHubInput) =>
    request<AdminGitHub>('/admin/connections/github', { method: 'PUT', body: input }),
  deleteAdminGitHub: () => request<void>('/admin/connections/github', { method: 'DELETE' }),
  // Reads the App and its installations from GitHub again.
  checkAdminGitHub: () => request<AdminGitHub>('/admin/connections/github/check', { method: 'POST', body: {} }),
  // The manifest and the address the browser posts it to, to make the App on GitHub in one click.
  startGitHubManifest: (input: { organization?: string; name?: string }) =>
    request<GitHubManifestStart>('/admin/connections/github/manifest', { method: 'POST', body: input }),
  // Saves the App GitHub made when saving it failed at the time (the redirect said `pending=1`).
  retryGitHubManifest: () =>
    request<AdminGitHub>('/admin/connections/github/manifest/retry', { method: 'POST', body: {} }),
  adminGitHubInstallations: (cursor?: string, signal?: AbortSignal) =>
    request<CursorPage<GitHubInstallation>>(
      `/admin/connections/github/installations${cursor ? `?cursor=${enc(cursor)}` : ''}`,
      { signal },
    ),
  // Shares the installation with exactly these tenants; none stops sharing.
  putAdminGitHubInstallation: (id: number, tenants: string[]) =>
    request<GitHubInstallation>(`/admin/connections/github/installations/${id}`, { method: 'PUT', body: { tenants } }),
  putDailyLines: (input: { lang: string } & ({ text: string; format: LinesFormat } | { lines: DailyLine[] })) =>
    request<AdminDailyBody>('/admin/daily/lines', { method: 'PUT', body: input }),
  // How a text would be split into lines, author and source; nothing is kept.
  previewDailyLines: (input: { text: string; format: LinesFormat }) =>
    request<{ lines: DailyLine[] }>('/admin/daily/lines/preview', { method: 'POST', body: input }),
  // The lines kept for a language.
  dailyLines: (lang: string) => request<{ lines: DailyLine[] }>(`/admin/daily/lines?lang=${enc(lang)}`),
  deleteDailyLines: (lang: string) =>
    request<AdminDailyBody>(`/admin/daily/lines?lang=${enc(lang)}`, { method: 'DELETE' }),
}

// --- users and invites ----------------------------------------------------------

// Every person, read by walking the pages of 200: for the one place that must know them all (the usernames pasted to
// add to a group). The lists people read are paged by the server (`useUsersPage`).
export function useUsers(enabled = true) {
  return useQuery({
    queryKey: keys.users,
    queryFn: async ({ signal }) => {
      const users: User[] = []
      let cursor = ''
      for (let page = 0; page < 100; page++) {
        const r = await adminApi.users({ limit: 200, cursor: cursor || undefined }, signal)
        users.push(...r.users)
        if (!r.next) break
        cursor = r.next
      }
      return { users }
    },
    staleTime: 30_000,
    enabled,
  })
}

// One page of the people list, as the server cuts it for this search, filter, sort and page size.
export function useUsersPage(params: UsersParams) {
  return useQuery({
    queryKey: [...keys.users, 'page', JSON.stringify(params)],
    queryFn: ({ signal }) => adminApi.users(params, signal),
    staleTime: 10_000,
    placeholderData: (prev) => prev,
  })
}

export function useUserRow(id: string, enabled = true) {
  return useQuery({
    queryKey: [...keys.users, 'one', id],
    queryFn: ({ signal }) => adminApi.user(id, signal),
    staleTime: 10_000,
    enabled,
  })
}

// A few users by (part of) a username or name, for a picker: the server does the
// filtering and the limit (disabled accounts never count), so the whole list of
// accounts is never loaded for it.
export const USER_SEARCH_LIMIT = 20
export function useUserSearch(q: string, enabled = true) {
  return useQuery({
    queryKey: keys.userSearch(q),
    queryFn: ({ signal }) => adminApi.users({ q, limit: USER_SEARCH_LIMIT, enabled: true }, signal),
    staleTime: 15_000,
    enabled,
  })
}

export function useInvites(enabled = true) {
  return useQuery({
    queryKey: keys.invites,
    queryFn: ({ signal }) => adminApi.invites(signal),
    staleTime: 30_000,
    enabled,
  })
}

// --- administration ----------------------------------------------------------------

export const ADMIN_PAGE_SIZE = 50

// Every space on the server, a cursor page at a time; `q` is the name filter.
export type AdminSpacesFilter = Omit<AdminSpacesParams, 'q' | 'cursor' | 'limit'>
export function useAdminSpaces(q: string, enabled = true, filter: AdminSpacesFilter = {}) {
  return useInfiniteQuery({
    queryKey: keys.adminSpaceList(q, JSON.stringify(filter)),
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) =>
      adminApi.adminSpaces(
        { ...filter, q: q || undefined, cursor: pageParam || undefined, limit: ADMIN_PAGE_SIZE },
        signal,
      ),
    getNextPageParam: (last) => last.next || undefined,
    enabled,
    staleTime: 15_000,
  })
}

// One page of the spaces' list, as the server cuts it for this search, filter, sort and page size.
export function useAdminSpacesPage(params: AdminSpacesParams) {
  return useQuery({
    queryKey: [...keys.adminSpaceList(params.q ?? '', JSON.stringify({ ...params, q: undefined })), 'page'],
    queryFn: ({ signal }) => adminApi.adminSpaces(params, signal),
    staleTime: 15_000,
    placeholderData: (prev) => prev,
  })
}

// People by usage, the most bytes first, a cursor page at a time.
export function useAdminUsage(params: { tenant: string; over: boolean }, enabled = true) {
  return useInfiniteQuery({
    queryKey: keys.adminUsage(params.tenant, params.over),
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) =>
      adminApi.usage(
        {
          tenant: params.tenant || undefined,
          over: params.over,
          cursor: pageParam || undefined,
          limit: ADMIN_PAGE_SIZE,
        },
        signal,
      ),
    getNextPageParam: (last) => last.next || undefined,
    enabled,
    staleTime: 10_000,
  })
}

// --- mail (3.3) ---

export function useMail(enabled = true) {
  return useQuery({
    queryKey: keys.adminMail,
    queryFn: ({ signal }) => adminApi.mail(signal),
    enabled,
    staleTime: 5_000,
  })
}
export function usePutMail() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: MailPatch) => adminApi.putMail(input),
    // Mail on or off changes what the sign-in page links to.
    onSuccess: (data) => {
      qc.setQueryData(keys.adminMail, data)
      void qc.invalidateQueries({ queryKey: keys.providers })
      void qc.invalidateQueries({ queryKey: keys.tenants })
    },
  })
}

// --- tenants and layered settings (3.3) ---

// Whether tenants besides the default one may exist (features.tenancy), and how many there are (`count` of the list).
export function useTenancy(enabled = true): UseQueryResult<Tenancy> {
  const on = useQuery({
    queryKey: keys.siteSettings,
    queryFn: ({ signal }) => adminApi.siteSettings(signal),
    enabled,
    staleTime: 10_000,
    select: (d) => d.settings.find((x) => x.key === K.tenancy)?.site.value === true,
  })
  const count = useQuery({
    queryKey: keys.tenantCount,
    queryFn: ({ signal }) => adminApi.tenants({ limit: 1 }, signal).then((l) => l.count),
    enabled,
    staleTime: 15_000,
  })
  return useJoined(on, count, (enabledNow, tenants) => ({ enabled: enabledNow, tenants }))
}
export function useSetTenancy() {
  const write = useWriteSettings()
  return {
    ...write,
    mutate: (on: boolean, o?: Parameters<typeof write.mutate>[1]) => write.mutate([{ key: K.tenancy, value: on }], o),
  }
}
// Every tenant, the default one first; there are at most 1,000, so the pages are followed to the end.
// `fresh` asks again on mounting even when the list is cached: a dialog that offers tenants to pick must not miss a new one.
// `origin` asks the server for one kind only (?origin=), so the pages are the server's, not a filter of what was loaded.
export function useTenants(enabled = true, fresh = false, origin?: TenantOrigin) {
  return useQuery({
    queryKey: origin ? [...keys.tenants, origin] : keys.tenants,
    enabled,
    staleTime: 15_000,
    refetchOnMount: fresh ? 'always' : true,
    queryFn: async ({ signal }) => {
      const out: Tenant[] = []
      let cursor = ''
      do {
        const page = await adminApi.tenants({ cursor, limit: 200, origin }, signal)
        out.push(...page.items)
        cursor = page.next
      } while (cursor)
      return out
    },
  })
}
// One tenant (its name, for a banner or a heading); under keys.tenants, so a rename reads it again.
export function useTenant(id: string | null) {
  return useQuery({
    queryKey: [...keys.tenants, 'one', id],
    queryFn: ({ signal }) => adminApi.tenant(id!, signal),
    enabled: id !== null,
    staleTime: 15_000,
  })
}

// A tenant's page: the tenant and its numbers in one call. Anything that changes the tenant reads it again (it sits
// under keys.tenants).
export function useTenantOverview(id: string) {
  return useQuery({
    queryKey: [...keys.tenants, 'overview', id],
    queryFn: ({ signal }) => adminApi.tenantOverview(id, signal),
    staleTime: 10_000,
  })
}
export function useCreateTenant() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: Parameters<typeof adminApi.createTenant>[0]) => adminApi.createTenant(input),
    onSuccess: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: keys.tenants }),
        qc.invalidateQueries({ queryKey: keys.tenantCount }),
      ]),
  })
}
export function useUpdateTenant() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, ...input }: { id: string } & Parameters<typeof adminApi.updateTenant>[1]) =>
      adminApi.updateTenant(id, input),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.tenants }),
  })
}
export function useDeleteTenant() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => adminApi.deleteTenant(id),
    onSuccess: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: keys.tenants }),
        qc.invalidateQueries({ queryKey: keys.tenantCount }),
      ]),
  })
}
// --- plans ---

export function usePlans(enabled = true) {
  return useQuery({
    queryKey: keys.plans,
    queryFn: ({ signal }) => adminApi.plans(signal).then((r) => r.plans),
    enabled,
    staleTime: 10_000,
  })
}
// A plan's rules are the lowest source of every tenant that has it: the tenants' rules and the plan list are read again.
function plansChanged(qc: QueryClient) {
  return Promise.all([
    qc.invalidateQueries({ queryKey: keys.plans }),
    qc.invalidateQueries({ queryKey: ['admin', 'settings'] }),
    qc.invalidateQueries({ queryKey: keys.tenants }),
  ])
}
export function useCreatePlan() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: Parameters<typeof adminApi.createPlan>[0]) => adminApi.createPlan(input),
    onSuccess: () => plansChanged(qc),
  })
}
export function useUpdatePlan() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ slug, ...input }: { slug: string } & PlanWrite) => adminApi.updatePlan(slug, input),
    onSuccess: () => plansChanged(qc),
  })
}
export function useDeletePlan() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (slug: string) => adminApi.deletePlan(slug),
    onSuccess: () => plansChanged(qc),
  })
}
export function usePutTenantPlan() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ tenant, plan }: { tenant: string; plan: string | null }) => adminApi.putTenantPlan(tenant, plan),
    onSuccess: () => plansChanged(qc),
  })
}
export function useSiteSettings(enabled = true) {
  return useQuery({
    queryKey: keys.siteSettings,
    queryFn: ({ signal }) => adminApi.siteSettings(signal),
    enabled,
    staleTime: 10_000,
  })
}
export function useTenantSettings(tenant: string, enabled = true) {
  return useQuery({
    queryKey: keys.tenantSettings(tenant),
    queryFn: ({ signal }) => adminApi.tenantSettings(tenant, signal),
    enabled: enabled && tenant !== '',
    staleTime: 10_000,
  })
}
// A change at one layer can change what every other layer shows (a lock, a restriction), so all of them are read again.
export function usePutSetting() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({
      tenant,
      key,
      ...input
    }: {
      tenant?: string
      key: string
      value?: unknown
      lower?: SettingLower | null
      siteLock?: boolean
      siteRule?: RuleWrite | null
    }): Promise<{ affected?: number; warning?: { code: string; keys: string[]; message: string } }> =>
      tenant ? adminApi.putTenantSetting(tenant, key, input) : adminApi.putSiteSetting(key, input),
    onSuccess: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: ['admin', 'settings'] }),
        // A quota rule moves the totals the tenant shows, and what it is over.
        qc.invalidateQueries({ queryKey: keys.tenants }),
        qc.invalidateQueries({ queryKey: ['settings'] }),
        qc.invalidateQueries({ queryKey: keys.adminDaily }),
        qc.invalidateQueries({ queryKey: keys.tenantCount }),
        qc.invalidateQueries({ queryKey: keys.adminSecurity }),
        qc.invalidateQueries({ queryKey: keys.adminStorage }),
      ]),
  })
}

// --- secrets ----------------------------------------------------------------------------------

// The secrets the scope may hold, each as set or not and when it changed; never a value. The site's, or one tenant's.
export function useSecrets(tenant?: string, enabled = true) {
  return useQuery({
    queryKey: keys.secrets(tenant ?? ''),
    queryFn: ({ signal }) => adminApi.secrets(tenant, signal).then((r) => r.secrets),
    enabled,
    staleTime: 10_000,
  })
}
// Replaces a secret, or clears it (no value). The answer is the list.
export function useChangeSecret(tenant?: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ name, value }: { name: string; value: string | null }) =>
      value === null ? adminApi.deleteSecret(tenant, name) : adminApi.putSecret(tenant, name, value),
    onSuccess: (r) => {
      qc.setQueryData(keys.secrets(tenant ?? ''), r.secrets)
      // The mail password and the captcha's secret also show on their own pages.
      void qc.invalidateQueries({ queryKey: keys.adminMail })
      void qc.invalidateQueries({ queryKey: keys.adminSecurity })
    },
  })
}

// What a change of a setting can change on the pages: the settings themselves, what is derived from them, the tenants'
// totals, and what the spaces show.
function settingsChanged(qc: QueryClient) {
  return Promise.all([
    qc.invalidateQueries({ queryKey: ['admin', 'settings'] }),
    qc.invalidateQueries({ queryKey: keys.adminSecurity }),
    qc.invalidateQueries({ queryKey: keys.adminStorage }),
    qc.invalidateQueries({ queryKey: keys.adminDaily }),
    qc.invalidateQueries({ queryKey: keys.tenants }),
    qc.invalidateQueries({ queryKey: keys.tenantCount }),
    qc.invalidateQueries({ queryKey: keys.adminSpaces }),
    qc.invalidateQueries({ queryKey: keys.spaces }),
    qc.invalidateQueries({ queryKey: keys.adminStatus }),
    qc.invalidateQueries({ queryKey: ['settings'] }),
    qc.invalidateQueries({ queryKey: ['admin', 'audit'] }),
    qc.invalidateQueries({
      predicate: (q) => q.queryKey[0] === 'space' && String(q.queryKey[2]).startsWith('daily'),
    }),
  ])
}

// Which key a refusal was about, so a page can show it where the person is looking.
const failedAt = new WeakMap<object, string>()
export const CREDENTIALS = 'captcha.credentials'
export const failedKey = (e: unknown): string | undefined =>
  typeof e === 'object' && e !== null ? failedAt.get(e) : undefined

// One step of a write: a setting, or (on the security page) the captcha's credentials.
export type SecurityStep = SettingStep | { credentials: CaptchaCredentials }
const isCredentials = (s: SecurityStep): s is { credentials: CaptchaCredentials } => 'credentials' in s

// The steps, one after the other: the first one the server refuses stops the rest, and its error is the answer. What
// was written before it stays written. Whatever happens, the pages read again.
export function useWriteSettings() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (steps: SecurityStep[]): Promise<void> => {
      for (const step of steps) {
        try {
          if (isCredentials(step)) await adminApi.putSecurity(step.credentials)
          else await adminApi.putSiteSetting(step.key, { value: step.value })
        } catch (e) {
          if (typeof e === 'object' && e !== null) failedAt.set(e, isCredentials(step) ? CREDENTIALS : step.key)
          throw e
        }
      }
    },
    onSettled: () => settingsChanged(qc),
  })
}

// Two queries as one: ready when both are, failing when either fails.
function useJoined<A, B, R>(a: UseQueryResult<A>, b: UseQueryResult<B>, make: (a: A, b: B) => R): UseQueryResult<R> {
  const data = useMemo(
    () => (a.data !== undefined && b.data !== undefined ? make(a.data, b.data) : undefined),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `make` is a function of the two answers only
    [a.data, b.data],
  )
  const lead = a.isError ? a : b.isError ? b : a.isPending ? a : b.isPending ? b : a
  return {
    ...lead,
    data,
    refetch: () => Promise.all([a.refetch(), b.refetch()]).then(() => lead),
  } as unknown as UseQueryResult<R>
}

export function useAdminStatus(enabled = true, staleTime = 5_000) {
  return useQuery({
    queryKey: keys.adminStatus,
    queryFn: ({ signal }) => adminApi.adminStatus(signal),
    enabled,
    staleTime,
  })
}

// What an administrator does to a space changes the administration's list, and
// what the members see of it (the space list: a marker, a new owner, gone).
function useAdminSpaceChanged() {
  const qc = useQueryClient()
  return (space: string) =>
    Promise.all([
      qc.invalidateQueries({ queryKey: keys.adminSpaces }),
      qc.invalidateQueries({ queryKey: keys.spaces }),
      qc.invalidateQueries({ queryKey: keys.members(space) }),
      qc.invalidateQueries({ queryKey: keys.adminStatus }),
    ])
}

// Measuring answers with the one space's numbers: they are put into the cached
// pages, so the table does not reload to show them.
export function useResizeSpace() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => adminApi.adminResizeSpace(id),
    onSuccess: (size, id) => {
      const patch = (list: AdminSpaceList) => ({
        ...list,
        items: list.items.map((s) => (s.id === id ? { ...s, ...size } : s)),
      })
      // The lists are read as infinite queries (the daily text's picker) or as one page (the table).
      qc.setQueriesData<{ pages: AdminSpaceList[]; pageParams: unknown[] } | AdminSpaceList>(
        { queryKey: keys.adminSpaces },
        (prev) => {
          if (!prev || typeof prev !== 'object') return prev
          if ('pages' in prev) return { ...prev, pages: prev.pages.map(patch) }
          return 'items' in prev && Array.isArray(prev.items) ? patch(prev) : prev
        },
      )
    },
  })
}
export function useTransferSpace() {
  const changed = useAdminSpaceChanged()
  return useMutation({
    mutationFn: ({ id, userId }: { id: string; userId: string }) => adminApi.adminTransferSpace(id, userId),
    onSuccess: (_r, { id }) => changed(id),
  })
}
export function useSetSpaceDisabled() {
  const changed = useAdminSpaceChanged()
  return useMutation({
    mutationFn: ({ id, disabled }: { id: string; disabled: boolean }) =>
      disabled ? adminApi.adminDisableSpace(id) : adminApi.adminEnableSpace(id),
    onSuccess: (_r, { id }) => changed(id),
  })
}
export function useDeleteSpace() {
  const changed = useAdminSpaceChanged()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, reason }: { id: string; reason?: string }) => adminApi.adminDeleteSpace(id, reason),
    onSuccess: (_r, { id }) => {
      qc.removeQueries({ queryKey: keys.space(id) })
      // The space is in the recycle bin now, and the storage numbers moved with it.
      void qc.invalidateQueries({ queryKey: ['admin', 'recycle'] })
      void qc.invalidateQueries({ queryKey: keys.adminStorage })
      return changed(id)
    },
  })
}

// --- groups --------------------------------------------------------------------------

// Every group, a cursor page at a time; `q` is the name filter, `source` local or synced.
export function useAdminGroups(q: string, source: GroupSource | '', enabled = true) {
  return useInfiniteQuery({
    queryKey: keys.adminGroupList(q, source),
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) =>
      adminApi.adminGroups(
        { q: q || undefined, source, cursor: pageParam || undefined, limit: ADMIN_PAGE_SIZE },
        signal,
      ),
    getNextPageParam: (last) => last.next || undefined,
    enabled,
    staleTime: 15_000,
  })
}

// The synced groups whose names contain `q`, for the mapping of a sign-in provider: the first 200 by name (the
// tenant has at most 10,000 groups, so the picker searches rather than loading them all).
export function useSyncedGroups(q = '', enabled = true) {
  return useQuery({
    queryKey: keys.adminGroupList(q, 'synced-all'),
    queryFn: ({ signal }) => adminApi.adminGroups({ q: q || undefined, source: 'synced', limit: 200 }, signal),
    enabled,
    staleTime: 15_000,
    placeholderData: (prev) => prev,
  })
}

export function useGroupPeople(id: string, q: string, enabled = true) {
  return useInfiniteQuery({
    queryKey: keys.adminGroupPeople(id, q),
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) =>
      adminApi.groupPeople(id, { q: q || undefined, cursor: pageParam || undefined, limit: ADMIN_PAGE_SIZE }, signal),
    getNextPageParam: (last) => last.next || undefined,
    enabled,
    staleTime: 10_000,
  })
}

export function useGroupSpaces(id: string, enabled = true) {
  return useInfiniteQuery({
    queryKey: keys.adminGroupSpaces(id),
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) =>
      adminApi.groupSpaces(id, { cursor: pageParam || undefined, limit: ADMIN_PAGE_SIZE }, signal),
    getNextPageParam: (last) => last.next || undefined,
    enabled,
    staleTime: 10_000,
  })
}

// What changes a group changes the lists of groups, and what the members of
// spaces see of them, and a person's own groups.
function useGroupsChanged() {
  const qc = useQueryClient()
  return () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: keys.adminGroups }),
      qc.invalidateQueries({ queryKey: keys.account }),
      qc.invalidateQueries({ queryKey: keys.spaces }),
      qc.invalidateQueries({ predicate: (q) => q.queryKey[0] === 'space' && q.queryKey[2] === 'members' }),
    ])
}
export function useCreateGroup() {
  const changed = useGroupsChanged()
  return useMutation({
    mutationFn: (input: { name: string; description?: string; source: GroupSource }) => adminApi.createGroup(input),
    onSuccess: changed,
  })
}
export function useUpdateGroup() {
  const changed = useGroupsChanged()
  return useMutation({
    mutationFn: ({ id, ...input }: { id: string; name?: string; description?: string }) =>
      adminApi.updateGroup(id, input),
    onSuccess: changed,
  })
}
export function useDeleteGroup() {
  const changed = useGroupsChanged()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, force }: { id: string; force: boolean }) => adminApi.deleteGroup(id, force),
    // The providers' mappings lose their entries with the group.
    onSuccess: () => Promise.all([changed(), qc.invalidateQueries({ queryKey: keys.adminProviders })]),
  })
}
export function useAddGroupPeople() {
  const changed = useGroupsChanged()
  return useMutation({
    mutationFn: ({ id, userIds }: { id: string; userIds: string[] }) => adminApi.addGroupPeople(id, userIds),
    onSuccess: changed,
  })
}
export function useRemoveGroupPerson() {
  const changed = useGroupsChanged()
  return useMutation({
    mutationFn: ({ id, userId }: { id: string; userId: string }) => adminApi.removeGroupPerson(id, userId),
    onSuccess: changed,
  })
}

// --- the daily text of the whole server ----------------------------------------------

// The interfaces, providers and lines with the default, the languages and what spaces may do (daily.*).
export function useAdminDaily(enabled = true): UseQueryResult<AdminDaily> {
  const body = useQuery({
    queryKey: keys.adminDaily,
    queryFn: ({ signal }) => adminApi.daily(signal),
    enabled,
    staleTime: 15_000,
  })
  const settings = useSiteSettings(enabled)
  return useJoined(body, settings, (b, s) => dailyOf(s.settings, b))
}

// Every write answers with the settings as they are now, which replace the cached ones.
function useDailyWrite<V>(write: (v: V) => Promise<AdminDailyBody>) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: write,
    onSuccess: (data) => {
      qc.setQueryData(keys.adminDaily, data)
      // What spaces may choose from, and what they show, comes from these.
      void qc.invalidateQueries({
        predicate: (q) => q.queryKey[0] === 'space' && String(q.queryKey[2]).startsWith('daily'),
      })
    },
  })
}
// The default, the languages and what spaces may do are settings: each is one write.
export function useSaveDailySite() {
  const write = useWriteSettings()
  return {
    ...write,
    mutate: (
      input: { default?: DailyDefault; languages?: string[]; allow?: Partial<DailyAllow> },
      o?: Parameters<typeof write.mutate>[1],
    ) => write.mutate(dailyWrites(input), o),
  }
}
export const useSaveDailyApi = () => useDailyWrite((api: Omit<DailyApi, 'note'>) => adminApi.putDailyApi(api))
export const useSaveDailyProvider = () =>
  useDailyWrite(
    ({
      id,
      ...input
    }: {
      id: string
      enabled: boolean
      refresh?: DailyProvider['refresh']
      minutes?: number
      rps?: number
      skipNew?: boolean
    }) => adminApi.putDailyProvider(id, input),
  )
export const useDeleteDailyApi = () => useDailyWrite((id: string) => adminApi.deleteDailyApi(id))
export const useSaveDailyLines = () =>
  useDailyWrite((input: Parameters<typeof adminApi.putDailyLines>[0]) => adminApi.putDailyLines(input))
export const useDeleteDailyLines = () => useDailyWrite((lang: string) => adminApi.deleteDailyLines(lang))
export function useTryDailyApi() {
  return useMutation({
    mutationFn: ({ api, lang, date }: { api: Omit<DailyApi, 'note'>; lang: string; date: string }) =>
      adminApi.tryDailyApi(api, lang, date),
  })
}

// --- security ----------------------------------------------------------------------------

// The sign-in policy (its settings) with what the server derives from it.
export function useSecuritySettings(enabled = true): UseQueryResult<SecuritySettings> {
  const body = useQuery({
    queryKey: keys.adminSecurity,
    queryFn: ({ signal }) => adminApi.security(signal),
    enabled,
    staleTime: 15_000,
  })
  const settings = useSiteSettings(enabled)
  return useJoined(body, settings, (b, s) => securityOf(s.settings, b))
}

export function useAuthProviders() {
  return useQuery({
    queryKey: keys.adminProviders,
    queryFn: ({ signal }) => adminApi.authProviders(signal),
    staleTime: 15_000,
  })
}

export const AUDIT_PAGE_SIZE = 50
// The audit log, newest first, a cursor page at a time, narrowed by the filters.
export function useAuditLog(filters: AuditFilters) {
  return useInfiniteQuery({
    queryKey: keys.adminAudit(auditQuery(filters).toString()),
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) =>
      adminApi.audit({ ...filters, cursor: pageParam || undefined, limit: AUDIT_PAGE_SIZE }, signal),
    getNextPageParam: (last) => last.next || undefined,
    staleTime: 5_000,
  })
}

// The registry of the audit log's kinds and categories: what the filters offer.
export function useAuditKinds() {
  return useQuery({
    queryKey: keys.adminAuditKinds,
    queryFn: ({ signal }) => adminApi.auditKinds(signal),
    staleTime: 30_000,
  })
}

// How many rows a purge of this category and date would delete: asked again when either changes, never cached.
export function useAuditPurgeCount(input: AuditPurgeInput, enabled: boolean) {
  return useQuery({
    queryKey: ['admin', 'audit-purge-count', input.category ?? 'all', input.before ?? ''],
    queryFn: ({ signal }) => adminApi.countAuditPurge(input, signal),
    enabled,
    gcTime: 0,
    staleTime: 0,
  })
}

// Starts the purge, which runs as a job: the caller follows `job.id` with useJob and calls useAuditPurged when it ends.
export function usePurgeAudit() {
  return useMutation({ mutationFn: (input: AuditPurgeInput) => adminApi.purgeAudit(input) })
}

// Reads the log and its registry again once a purge has ended.
export function useAuditPurged() {
  const qc = useQueryClient()
  return () => qc.invalidateQueries({ queryKey: ['admin', 'audit'] })
}

// The query of a set of filters, in a fixed order, without what is empty: the same string for the same view.
export function auditQuery(f: Partial<AuditFilters>): URLSearchParams {
  const q = new URLSearchParams()
  if (f.category?.length) q.set('category', f.category.join(','))
  for (const k of ['kind', 'user', 'actor', 'capacity', 'from', 'to', 'tenant'] as const) if (f[k]) q.set(k, f[k])
  return q
}

// --- machine access ------------------------------------------------------------------

// Whether API tokens and MCP are on for the site (features.tokens, features.mcp); a tenant may switch them off for itself.
export function useMachineAccess(enabled = true) {
  return useQuery({
    queryKey: keys.siteSettings,
    queryFn: ({ signal }) => adminApi.siteSettings(signal),
    enabled,
    staleTime: 10_000,
    select: (d) => machineOf(d.settings),
  })
}

export const ADMIN_TOKENS_PAGE = 50

// Every person's tokens, a cursor page at a time.
export function useAdminTokens(enabled = true) {
  return useInfiniteQuery({
    queryKey: keys.adminTokens,
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) =>
      adminApi.adminTokens({ cursor: pageParam || undefined, limit: ADMIN_TOKENS_PAGE }, signal),
    getNextPageParam: (last) => last.next || undefined,
    enabled,
    staleTime: 15_000,
  })
}

// Every person's OAuth grants, a cursor page at a time.
export function useAdminGrants(enabled = true) {
  return useInfiniteQuery({
    queryKey: keys.adminGrants,
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) =>
      adminApi.adminGrants({ cursor: pageParam || undefined, limit: ADMIN_TOKENS_PAGE }, signal),
    getNextPageParam: (last) => last.next || undefined,
    enabled,
    staleTime: 15_000,
  })
}

// --- the hosts of GitLab and Gitea ---------------------------------------------------

// Every provider the server has, with its counts; the cards of the hosts are made from it.
export function useAdminConnectionProviders(enabled = true) {
  return useQuery({
    queryKey: keys.adminConnectionList,
    queryFn: ({ signal }) => adminApi.adminConnectionProviders(signal),
    enabled,
    staleTime: 15_000,
  })
}

export function useAdminForge(id: string, enabled = true) {
  return useQuery({
    queryKey: keys.adminForge(id),
    queryFn: ({ signal }) => adminApi.adminForge(id, signal),
    enabled,
    staleTime: 15_000,
  })
}

// What an administrator changes about a host changes what everyone is offered.
function useForgeChanged() {
  const qc = useQueryClient()
  return () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: keys.adminConnectionList }),
      qc.invalidateQueries({ queryKey: ['admin', 'connections'] }),
      qc.invalidateQueries({ queryKey: keys.connections }),
    ])
}

export function useCreateAdminForge() {
  const changed = useForgeChanged()
  return useMutation({
    mutationFn: (input: CreateAdminForgeInput) => adminApi.createAdminForge(input),
    onSuccess: () => changed(),
  })
}

export function usePatchAdminForge(id: string) {
  const changed = useForgeChanged()
  return useMutation({
    mutationFn: (input: PatchAdminForgeInput) => adminApi.patchAdminForge(id, input),
    onSuccess: () => changed(),
  })
}

export function useCheckAdminForge(id: string) {
  const changed = useForgeChanged()
  return useMutation({ mutationFn: () => adminApi.checkAdminForge(id), onSuccess: () => changed() })
}

export function useDeleteAdminForge(id: string) {
  const changed = useForgeChanged()
  return useMutation({ mutationFn: () => adminApi.deleteAdminForge(id), onSuccess: () => changed() })
}

// --- the GitHub App ----------------------------------------------------------------

export function useAdminGitHub(enabled = true) {
  return useQuery({
    queryKey: keys.adminGitHub,
    queryFn: ({ signal }) => adminApi.adminGitHub(signal),
    enabled,
    staleTime: 15_000,
  })
}

// What an administrator changes about the App changes what everyone is offered.
function useGitHubChanged() {
  const qc = useQueryClient()
  return () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: keys.adminGitHub }),
      qc.invalidateQueries({ queryKey: keys.connections }),
    ])
}

export function useSaveAdminGitHub() {
  const changed = useGitHubChanged()
  return useMutation({
    mutationFn: (input: PutAdminGitHubInput) => adminApi.putAdminGitHub(input),
    onSuccess: () => changed(),
  })
}

export function useCheckAdminGitHub() {
  const changed = useGitHubChanged()
  return useMutation({ mutationFn: () => adminApi.checkAdminGitHub(), onSuccess: () => changed() })
}

export function useStartGitHubManifest() {
  return useMutation({
    mutationFn: (input: { organization?: string; name?: string }) => adminApi.startGitHubManifest(input),
  })
}

export function useRetryGitHubManifest() {
  const changed = useGitHubChanged()
  return useMutation({ mutationFn: () => adminApi.retryGitHubManifest(), onSuccess: () => changed() })
}

export function useDeleteAdminGitHub() {
  const changed = useGitHubChanged()
  return useMutation({ mutationFn: () => adminApi.deleteAdminGitHub(), onSuccess: () => changed() })
}

// The App's installations, a page (one call to GitHub) at a time.
export function useAdminGitHubInstallations(enabled = true) {
  return useInfiniteQuery({
    queryKey: keys.adminGitHubInstallations,
    initialPageParam: '',
    queryFn: ({ pageParam, signal }): Promise<CursorPage<GitHubInstallation>> =>
      adminApi.adminGitHubInstallations(pageParam || undefined, signal),
    getNextPageParam: (last) => last.next || undefined,
    enabled,
    staleTime: 15_000,
  })
}

export function useShareGitHubInstallation() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, tenants }: { id: number; tenants: string[] }) =>
      adminApi.putAdminGitHubInstallation(id, tenants),
    onSuccess: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: keys.adminGitHubInstallations }),
        qc.invalidateQueries({ queryKey: ['connections'] }),
      ]),
  })
}

// --- server storage (docs/api.md, "Server storage") ---

// What is measured and derived (the summary, what the requirement asks of spaces); a tenant administrator reads it too.
export function useAdminStorage(enabled = true) {
  return useQuery({
    queryKey: keys.adminStorage,
    queryFn: ({ signal }) => adminApi.adminStorage(signal),
    enabled,
    staleTime: 10_000,
  })
}
// The policy of the site (storage.*, cache.*) with what is derived: site administrators only.
export function useStoragePolicy(enabled = true): UseQueryResult<AdminStorage> {
  const body = useAdminStorage(enabled)
  const settings = useSiteSettings(enabled)
  return useJoined(body, settings, (b, s) => storageOf(s.settings, b))
}
// A change of the policy changes what the spaces show (notices, read only) and the counts of the status.
export function useSaveAdminStorage() {
  const write = useWriteSettings()
  return {
    ...write,
    mutate: (input: PutAdminStorage, o?: Parameters<typeof write.mutate>[1]) => write.mutate(storageWrites(input), o),
    mutateAsync: (input: PutAdminStorage) => write.mutateAsync(storageWrites(input)),
  }
}
export function usePutSpaceStorage() {
  const changed = useAdminSpaceChanged()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, ...input }: { id: string; pin?: boolean; exempt?: boolean }) =>
      adminApi.putSpaceStorage(id, input),
    onSuccess: (_r, { id }) => Promise.all([changed(id), qc.invalidateQueries({ queryKey: keys.adminStorage })]),
  })
}
export function useEvictSpace() {
  const changed = useAdminSpaceChanged()
  return useMutation({ mutationFn: (id: string) => adminApi.evictSpace(id), onSuccess: (_r, id) => changed(id) })
}
