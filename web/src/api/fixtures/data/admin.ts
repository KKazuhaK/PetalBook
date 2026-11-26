// What the administration console reads: tenants, plans, groups, the site's settings, secrets, mail, sign-in
// providers, the audit log, the recycle bin, tokens, the daily text and what the server says of itself. Plain typed
// records, declared here; routes/admin.ts lists, reads and writes them and checks nothing.

import registries from '../../generated/registries.json' with { type: 'json' }
import type {
  AdminDailyBody,
  AdminGrant,
  AdminSpace,
  AdminToken,
  AuditItem,
  AuthProvider,
  ProviderInput,
  SecurityNotice,
  BulkJob,
  ExportRec,
  Group,
  GroupSpaceRef,
  Invite,
  MailSettings,
  Passkey,
  PersonIdentity,
  PersonUsage,
  PersonSession,
  Plan,
  RecycleItem,
  SecretMeta,
  SiteSetting,
  Tenant,
  TenantSetting,
} from '../../types.ts'
import type { SpaceData } from './model.ts'
import { DAILY_LANGUAGES, DAILY_PROVIDERS } from './dailyRegistry.ts'
import { SETTING_DEFS } from './settingDefs.ts'

export const DEFAULT_TENANT = '0000000000'

export interface GroupRec extends Group {
  // The people in it (user ids) and the spaces it is a member of.
  members: string[]
  spaceRefs: GroupSpaceRef[]
}

// What the console shows of one person besides the account: sign-ins, linked identities, passkeys, memberships.
export interface PersonRec {
  sessions: PersonSession[]
  identities: PersonIdentity[]
  passkeys: Passkey[]
  lastActive?: string
  // What the person holds, as measured, and the totals they are over.
  usage?: Partial<Pick<PersonUsage, 'spaces' | 'bytes' | 'pages' | 'measuredAt' | 'over'>>
  quota?: { spaces: number | null; bytes: number | null; pages: number | null }
}

export interface AdminData {
  // The site's settings with the site's value of each; what a tenant sets of its own is in `tenantRules`.
  settings: SiteSetting[]
  tenants: Tenant[]
  // A tenant's own value and rule per setting key: tenant id, then key.
  tenantRules: Record<string, Record<string, NonNullable<TenantSetting['tenant']>>>
  plans: Plan[]
  groups: GroupRec[]
  secrets: { site: Record<string, SecretMeta>; tenants: Record<string, Record<string, SecretMeta>> }
  mail: MailSettings
  captcha: { siteKey: string; hasSecret: boolean }
  // What the sign-in security page is told besides the settings: whether passkeys can work here, and its notices.
  security: { passkeysAvailable: boolean; notices: SecurityNotice[] }
  providers: AuthProvider[]
  audit: AuditItem[]
  recycle: RecycleItem[]
  exports: ExportRec[]
  jobs: BulkJob[]
  tokens: AdminToken[]
  grants: AdminGrant[]
  daily: AdminDailyBody
  // The administrators' own lines of daily text, by language.
  dailyLines: Record<string, { text: string; author: string; source: string }[]>
  // Spaces the signed-in person is not a member of: the console lists them, their own list of spaces does not.
  otherSpaces: SpaceData[]
  invites: (Invite & { tenant: string })[]
  // What a space has for the console beyond what a person's list of spaces says (its owner, its size, its storage).
  spaceExtra: Record<string, Partial<AdminSpace>>
  people: Record<string, PersonRec>
  // Whose view of a space or a person this session has open.
  views: Record<string, { since: string; until: string }>
  // The plan a confirmation was asked for, by token.
  confirms: Record<string, { action: string; spaces: string[]; users: string[]; items: string[] }>
}

const stamp = (now: number) => new Date(now).toISOString().replace(/\.\d{3}Z$/, 'Z')

// The audit kinds the server has, for the filters: the registry's, each category on or off by its default.
export const AUDIT_CATEGORIES = registries.auditCategories.map((c) => ({
  id: c.id,
  core: c.core,
  setting: c.setting,
  on: c.default,
  kinds: c.kinds,
}))

export function createAdminData(now: number, admin: { id: string; username: string; name: string }): AdminData {
  const at = stamp(now)
  const tenant: Tenant = {
    id: DEFAULT_TENANT,
    slug: 'default',
    name: 'Default',
    default: true,
    disabled: false,
    registration: { open: false },
    quota: { people: null, spaces: null, bytes: null, pages: null },
    plan: null,
    origin: 'default',
    createdBy: '',
    usage: { people: 1, spaces: 1, bytes: 0, pages: 0, measuredAt: null, over: [] },
    created: at,
    updated: at,
  }
  return {
    settings: structuredClone(SETTING_DEFS),
    tenants: [tenant],
    tenantRules: {},
    plans: [],
    groups: [],
    secrets: {
      site: Object.fromEntries(
        ['github.appKey', 'github.clientSecret', 'github.webhookSecret', 'mail.password', 'captcha.secret'].map((n) => [
          n,
          { name: n, set: false },
        ]),
      ),
      tenants: {},
    },
    mail: {
      enabled: false,
      host: '',
      port: 587,
      security: 'starttls',
      username: '',
      hasPassword: false,
      from: '',
      fromName: '',
      replyTo: '',
      status: { ok: false, checked: null, message: '' },
      queue: { waiting: 0, sentLastHour: 0, failedLastHour: 0 },
    },
    captcha: { siteKey: '', hasSecret: false },
    security: { passkeysAvailable: true, notices: [] },
    providers: [],
    audit: [
      {
        id: 'A00000001',
        at,
        kind: 'login',
        category: 'signins',
        capacity: null,
        actor: null,
        user: admin.id,
        username: admin.username,
        ip: '203.0.113.7',
        detail: { method: 'password' },
      },
    ],
    recycle: [],
    exports: [],
    jobs: [],
    tokens: [],
    grants: [],
    daily: {
      knownLanguages: DAILY_LANGUAGES,
      apis: [],
      providers: structuredClone(DAILY_PROVIDERS),
      lines: { total: 0, byLang: {}, max: 5000 },
    },
    dailyLines: {},
    otherSpaces: [],
    invites: [],
    spaceExtra: {},
    people: {},
    views: {},
    confirms: {},
  }
}

const SITE = 'https://notes.example.com'

// A sign-in provider as the console lists it, made from what a person typed: the secrets only as "set", and the
// addresses the identity provider needs worked out from the slug.
export function makeProvider(input: ProviderInput & { slug: string }, at: string, order: number): AuthProvider {
  const kind = input.kind ?? 'oidc'
  const url = `${SITE}/api/auth/${kind}/${input.slug}`
  const p: AuthProvider = {
    slug: input.slug,
    kind,
    name: input.name ?? input.slug,
    icon: input.icon ?? '',
    enabled: input.enabled ?? true,
    order: input.order ?? order,
    autoCreate: input.autoCreate ?? false,
    mergeVerifiedEmail: input.mergeVerifiedEmail ?? false,
    groups: { enabled: false, claim: '', missing: 'keep', map: [], ...input.groups },
    status: { ok: true, checked: at, message: '' },
    people: 0,
    created: at,
    updated: at,
  }
  if (kind === 'oidc') {
    const { clientSecret, ...rest } = input.oidc ?? {}
    p.oidc = {
      issuer: '',
      clientId: '',
      scopes: ['openid', 'email', 'profile'],
      legacyCallback: false,
      ...rest,
      hasSecret: !!clientSecret,
      redirectUrl: `${url}/callback`,
    }
  }
  if (kind === 'github') {
    const { clientSecret, ...rest } = input.github ?? {}
    p.github = {
      baseUrl: '',
      clientId: '',
      organizations: [],
      ...rest,
      hasSecret: !!clientSecret,
      redirectUrl: `${url}/callback`,
    }
  }
  if (kind === 'saml') {
    p.saml = {
      metadataUrl: '',
      metadataXml: '',
      nameIdFormat: '',
      signRequests: false,
      allowIdpInitiated: false,
      attributes: { email: 'email', name: 'name', username: 'username' },
      ...input.saml,
      idpEntityId: '',
      entityId: `${url}/metadata`,
      acsUrl: `${url}/acs`,
      metadataPath: `/api/auth/saml/${input.slug}/metadata`,
      certificate: '-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n',
      certificateExpires: '2036-11-20T08:00:00Z',
    }
  }
  return p
}

// A change to a provider: the fields sent, a secret kept as "set".
export function changeProvider(p: AuthProvider, input: ProviderInput, at: string): AuthProvider {
  const { oidc, saml, github, groups, kind: _kind, slug: _slug, ...top } = input
  void _kind
  void _slug
  Object.assign(p, top, { updated: at })
  if (groups) p.groups = { ...p.groups, ...groups }
  if (oidc && p.oidc) {
    const { clientSecret, ...rest } = oidc
    Object.assign(p.oidc, rest)
    if (clientSecret !== undefined) p.oidc.hasSecret = clientSecret !== ''
  }
  if (github && p.github) {
    const { clientSecret, ...rest } = github
    Object.assign(p.github, rest)
    if (clientSecret !== undefined) p.github.hasSecret = clientSecret !== ''
  }
  if (saml && p.saml) Object.assign(p.saml, saml)
  return p
}
