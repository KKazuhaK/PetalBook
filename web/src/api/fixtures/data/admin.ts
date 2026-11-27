// What the administration console reads: tenants, plans, groups, the site's settings, secrets, mail, sign-in
// providers, the audit log, the recycle bin, tokens, the daily text and what the server says of itself. Plain typed
// records, declared here; routes/admin.ts lists, reads and writes them and checks nothing.

import registries from '../../generated/registries.json' with { type: 'json' }
import type {
  PermissionKey,
  AdminDailyBody,
  AdminGrant,
  AdminSpace,
  AdminToken,
  AuditItem,
  AuthProvider,
  ProviderInput,
  ProviderTemplate,
  SecurityNotice,
  ExportRec,
  Job,
  Group,
  GroupSpaceRef,
  Invite,
  MailSettings,
  Passkey,
  Link,
  ProviderTestResult,
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

// What the console shows of one person besides the account: sign-ins, linked sign-ins, passkeys, memberships.
export interface PersonRec {
  sessions: PersonSession[]
  links: Link[]
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
  // The tenants the signed-in person administers by a role of their own besides their home one (the console's scope
  // selector lists them).
  scopeTenants: string[]
  // The keys a tenant's role gives the signed-in person there, where it is narrower than a tenant administrator's.
  scopeKeys: Record<string, PermissionKey[]>
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
  jobs: Job[]
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
  // A test sign-in's result, kept until it is read once.
  providerTests: Record<string, ProviderTestResult>
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
    scopeTenants: [],
    scopeKeys: {},
    tenantRules: {},
    plans: [],
    groups: [],
    secrets: {
      site: Object.fromEntries(
        [
          'github.appKey',
          'github.clientSecret',
          'github.webhookSecret',
          'mail.password',
          'captcha.secret',
          'signin.appleKey',
        ].map((n) => [n, { name: n, set: false }]),
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
      // Older rows of other categories, for the purge to count.
      ...(
        [
          [40, 'login_failed', 'security'],
          [75, 'setting_changed', 'admin'],
          [120, 'space_deleted', 'data'],
          [200, 'login_failed', 'security'],
        ] as const
      ).map(([days, kind, category], i) => ({
        id: `A0000000${i + 2}`,
        at: stamp(now - days * 86_400_000),
        kind,
        category,
        capacity: null,
        actor: null,
        user: null,
        username: '',
        ip: '203.0.113.7',
        detail: {},
      })),
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
    providerTests: {},
    confirms: {},
  }
}

const SITE = 'https://notes.example.com'

// The presets the server ships (internal/auth/templates.go), as GET /api/admin/provider-templates lists them.
export const PROVIDER_TEMPLATES: ProviderTemplate[] = [
  {
    id: 'google',
    name: 'Google',
    kind: 'oidc',
    class: 'social',
    icon: 'preset:google',
    issuer: 'https://accounts.google.com',
    authorizationEndpoint: 'https://accounts.google.com/o/oauth2/v2/auth',
    tokenEndpoint: 'https://oauth2.googleapis.com/token',
    jwksUri: 'https://www.googleapis.com/oauth2/v3/certs',
    scopes: ['openid', 'email', 'profile'],
    secret: 'client',
    common: true,
    region: 'global',
  },
  {
    id: 'microsoft',
    name: 'Microsoft',
    kind: 'oidc',
    class: 'social',
    icon: 'preset:microsoft',
    issuer: 'https://login.microsoftonline.com/{tenantid}/v2.0',
    authorizationEndpoint: 'https://login.microsoftonline.com/common/oauth2/v2.0/authorize',
    tokenEndpoint: 'https://login.microsoftonline.com/common/oauth2/v2.0/token',
    jwksUri: 'https://login.microsoftonline.com/common/discovery/v2.0/keys',
    scopes: ['openid', 'email', 'profile'],
    secret: 'client',
    common: true,
    region: 'global',
  },
  {
    id: 'apple',
    name: 'Apple',
    kind: 'oidc',
    class: 'social',
    icon: 'preset:apple',
    issuer: 'https://appleid.apple.com',
    authorizationEndpoint: 'https://appleid.apple.com/auth/authorize',
    tokenEndpoint: 'https://appleid.apple.com/auth/token',
    jwksUri: 'https://appleid.apple.com/auth/keys',
    scopes: ['openid', 'email', 'name'],
    responseMode: 'form_post',
    secret: 'apple',
    common: true,
    region: 'global',
  },
  {
    id: 'github',
    name: 'GitHub',
    kind: 'github',
    class: 'social',
    icon: 'preset:github',
    issuer: 'https://github.com',
    scopes: ['read:user', 'user:email'],
    secret: 'client',
    common: true,
    region: 'global',
  },
  {
    id: 'gitlab-com',
    name: 'GitLab.com',
    kind: 'oidc',
    class: 'social',
    icon: 'preset:gitlab',
    issuer: 'https://gitlab.com',
    authorizationEndpoint: 'https://gitlab.com/oauth/authorize',
    tokenEndpoint: 'https://gitlab.com/oauth/token',
    jwksUri: 'https://gitlab.com/oauth/discovery/keys',
    scopes: ['openid', 'email', 'profile'],
    secret: 'client',
    common: false,
    region: 'global',
  },
]

// A create from a template as the create it stands for, as the server makes it.
function fromTemplate(input: ProviderInput & { slug?: string }): ProviderInput & { slug: string } {
  const tm = PROVIDER_TEMPLATES.find((x) => x.id === input.template)
  if (!tm) return { ...input, slug: input.slug ?? '' }
  const { clientId, clientSecret, ...rest } = input
  const out: ProviderInput & { slug: string } = {
    ...rest,
    slug: input.slug ?? tm.id,
    kind: tm.kind,
    class: 'social',
    name: input.name ?? tm.name,
    icon: input.icon ?? tm.icon,
  }
  if (tm.kind === 'github') out.github = { baseUrl: tm.issuer, clientId, clientSecret, ...input.github }
  else out.oidc = { issuer: tm.issuer, scopes: tm.scopes, clientId, clientSecret, ...input.oidc }
  return out
}

// A sign-in provider as the console lists it, made from what a person typed: the secrets only as "set", and the
// addresses the identity provider needs worked out from the slug.
export function makeProvider(given: ProviderInput & { slug?: string }, at: string, order: number): AuthProvider {
  const input = fromTemplate(given)
  const kind = input.kind ?? 'oidc'
  const url = `${SITE}/api/auth/${kind}/${input.slug}`
  const p: AuthProvider = {
    id: input.slug,
    owner: input.owner ?? 'site',
    slug: input.slug,
    kind,
    class: input.class ?? (kind === 'github' ? 'social' : 'sso'),
    template: given.template && kind === 'oidc' ? given.template : '',
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
      ...rest,
      hasSecret: !!clientSecret || !!input.apple?.privateKey,
      redirectUrl: `${url}/callback`,
    }
    if (given.template === 'apple')
      p.apple = {
        teamId: input.apple?.teamId ?? '',
        keyId: input.apple?.keyId ?? '',
        hasKey: !!input.apple?.privateKey,
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
  const {
    oidc,
    saml,
    github,
    groups,
    apple,
    kind: _kind,
    slug: _slug,
    class: _class,
    template: _template,
    clientId: _clientId,
    clientSecret: _clientSecret,
    ...top
  } = input
  void [_kind, _slug, _class, _template, _clientId, _clientSecret]
  Object.assign(p, top, { updated: at })
  if (apple && p.apple) {
    if (apple.teamId !== undefined) p.apple.teamId = apple.teamId
    if (apple.keyId !== undefined) p.apple.keyId = apple.keyId
    if (apple.privateKey) p.apple.hasKey = true
  }
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
