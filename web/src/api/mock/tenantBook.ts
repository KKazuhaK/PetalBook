// The mock's side of milestone 3.3 (docs/api.md, "Tenants, registration and mail"): the switch, the tenants, and the
// layered settings with their locks. It does not keep people or spaces apart by tenant (the mock has one world);
// what it keeps true is what the web app reads: the contract of these endpoints.

import type {
  ClearedRule,
  CreatedInvite,
  Plan,
  PlanRule,
  SettingLower,
  SettingRule,
  SiteSetting,
  Tenant,
  TenantSetting,
  TenantUsage,
} from '../types.ts'
import { HttpError } from './store.ts'
import type { MockResponse } from './server.ts'

export const DEFAULT_TENANT = '0000000000'
const SLUG = /^[a-z0-9][a-z0-9-]{1,31}$/
const bad = (message: string): never => {
  throw new HttpError(400, 'invalid', message)
}
const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

type Kind = SiteSetting['kind']
interface Def {
  key: string
  kind: Kind
  layers: ('site' | 'tenant' | 'space' | 'person')[]
  def: unknown
  lower: SettingLower
  min?: number
  max?: number
  choices?: string[]
  // A site-only setting a site administrator may set for one tenant (its rule for that tenant).
  forTenant?: boolean
  // The value is kept by another part of the server (the sign-in policy, the storage policy, the daily text): the
  // book reads and writes it through `bound`, and that part checks what looks at more than one value.
  bound?: boolean
  // A limit that may also be null (no limit) although its default is a number.
  nullable?: boolean
  // Why a site-only setting has no tenant layer.
  why?: string
}
// The settings of 3.3, as the server has them (3.1's come with 3.1).
const DEFS: Def[] = [
  { key: 'features.registration', kind: 'boolean', layers: ['site', 'tenant'], def: false, lower: { mode: 'lock' } },
  { key: 'registration.domains', kind: 'list', layers: ['site', 'tenant'], def: [], lower: { mode: 'allow' } },
  {
    key: 'registration.perHour',
    kind: 'limit',
    layers: ['site', 'tenant'],
    def: 50,
    lower: { mode: 'restrict', max: 50 },
    min: 1,
    max: 10000,
  },
  { key: 'legal.termsUrl', kind: 'text', layers: ['site', 'tenant'], def: '', lower: { mode: 'allow' } },
  { key: 'legal.privacyUrl', kind: 'text', layers: ['site', 'tenant'], def: '', lower: { mode: 'allow' } },
  {
    key: 'daily.own',
    kind: 'boolean',
    layers: ['site', 'tenant', 'space'],
    def: true,
    lower: { mode: 'allow' },
    bound: true,
  },
  {
    key: 'daily.api',
    kind: 'boolean',
    layers: ['site', 'tenant', 'space'],
    def: true,
    lower: { mode: 'allow' },
    bound: true,
  },
  // Whether people may keep offline copies on a device (4.1); the sync endpoints answer 403 offline_off when it is off.
  { key: 'features.offline', kind: 'boolean', layers: ['site', 'tenant'], def: true, lower: { mode: 'allow' } },
  { key: 'daily.default', kind: 'doc', layers: ['site', 'tenant'], def: null, lower: { mode: 'allow' }, bound: true },
  // What one person may use: only the number of spaces is refused (0 is none); bytes and pages warn (null is none).
  {
    key: 'quota.spacesPerPerson',
    kind: 'limit',
    layers: ['site', 'tenant', 'person'],
    def: 20,
    lower: { mode: 'allow' },
    min: 0,
    max: 10000,
  },
  {
    key: 'quota.bytesPerPerson',
    kind: 'limit',
    layers: ['site', 'tenant', 'person'],
    def: null,
    lower: { mode: 'allow' },
    min: 1,
    max: 2 ** 50,
  },
  {
    key: 'quota.pagesPerPerson',
    kind: 'limit',
    layers: ['site', 'tenant', 'person'],
    def: null,
    lower: { mode: 'allow' },
    min: 1,
    max: 2 ** 40,
  },
  // The largest file one write may create or replace (3.5). The site's value is --max-upload and cannot be written;
  // a tenant sets any value up to it.
  {
    key: 'files.maxBytes',
    kind: 'limit',
    layers: ['site', 'tenant'],
    def: 10 * 1024 * 1024,
    lower: { mode: 'restrict', max: 10 * 1024 * 1024 },
    min: 1,
    max: 256 * 1024 * 1024,
  },
  // Where the attachments of a new space on a provider go: separate by default. A site administrator may also lock the
  // value for one tenant (siteLock). The switch is the site's alone: whether a tenant on its own storage is exempt.
  {
    key: 'storage.assets',
    kind: 'choice',
    layers: ['site', 'tenant'],
    def: 'repository',
    lower: { mode: 'allow' },
    choices: ['space', 'repository'],
  },
  {
    key: 'storage.assets.ownStorageExempt',
    kind: 'boolean',
    layers: ['site'],
    def: true,
    lower: { mode: 'allow' },
    forTenant: true,
  },
  // How many days a deleted space or person stays in the recycle bin (3.5); the site's alone.
  { key: 'recycle.days', kind: 'limit', layers: ['site'], def: 30, lower: { mode: 'allow' }, min: 1, max: 365 },
  // Whether addresses outside ASCII may be set (3.6); the site's alone.
  { key: 'users.emailNonAscii', kind: 'boolean', layers: ['site'], def: false, lower: { mode: 'allow' } },
  // The features (3.1): on unless a rule for one tenant turns them off (mail.ownSender: off unless).
  ...(
    [
      ['features.ownSso', true],
      ['features.domains', true],
      ['features.managedAccounts', true],
      ['features.idpAdmins', false],
      ['features.adminView', true],
      ['features.auditDownload', true],
      ['mail.ownSender', false],
      ['features.ownCaptcha', true],
      ['features.ownConnections', true],
      ['features.ownDaily', true],
    ] as [string, boolean][]
  ).map(([key, def]): Def => ({
    key,
    kind: 'boolean',
    layers: ['site'],
    def,
    lower: { mode: 'allow' },
    forTenant: true,
  })),
  {
    key: 'storage.mode',
    kind: 'choice',
    layers: ['site', 'tenant'],
    def: 'server',
    lower: { mode: 'allow' },
    choices: ['server', 'own', 'tenant'],
  },
  {
    key: 'storage.pinnedBytes',
    kind: 'limit',
    layers: ['site'],
    def: 0,
    lower: { mode: 'allow' },
    min: 0,
    max: 2 ** 50,
    forTenant: true,
  },
  // What a tenant may use in all (null is no limit), and what it may ask of the server.
  ...(
    [
      ['quota.people', 2 ** 40],
      ['quota.spaces', 2 ** 40],
      ['quota.bytes', 2 ** 50],
      ['quota.pages', 2 ** 40],
    ] as [string, number][]
  ).map(([key, max]): Def => ({
    key,
    kind: 'limit',
    // No tenant layer: a tenant's own value would rank above the site's rule for it.
    layers: ['site'],
    def: null,
    lower: { mode: 'allow' },
    min: 0,
    max,
    forTenant: true,
  })),
  ...['rate.apiPerMinute', 'rate.mcpPerMinute', 'rate.mailPerDay', 'rate.outboundPerMinute'].map((key): Def => ({
    key,
    kind: 'limit',
    layers: ['site'],
    def: null,
    lower: { mode: 'allow' },
    min: 0,
    max: 2 ** 40,
    forTenant: true,
  })),
]
const GIB = 1024 ** 3
const MIB = 1024 ** 2
const WHY = {
  signin: 'the sign-in page comes before any tenant is known',
  sessions: "sessions are the server's until organisations get their own lifetimes",
  storage: 'the requirement of a remote is worked out over every space of the server',
  cache: "the cache is on the server's own disk",
}
const site = (def: Partial<Def> & Pick<Def, 'key' | 'kind' | 'def'>): Def => ({
  layers: ['site'],
  lower: { mode: 'allow' },
  ...def,
})
// What moved into the settings with the fold of the limits sheet: machine access, tenancy, the sign-in policy, the
// storage policy and the daily text. The sign-in, storage and daily ones are kept by the parts they belong to.
const FOLDED: Def[] = [
  { key: 'features.tokens', kind: 'boolean', layers: ['site', 'tenant'], def: true, lower: { mode: 'allow' } },
  { key: 'features.mcp', kind: 'boolean', layers: ['site', 'tenant'], def: true, lower: { mode: 'allow' } },
  site({ key: 'features.tenancy', kind: 'boolean', def: false, why: 'whether tenants besides the default one exist' }),
  site({ key: 'features.passkeys', kind: 'boolean', def: true, bound: true, why: WHY.signin }),
  site({
    key: 'signin.mode',
    kind: 'choice',
    def: 'dual',
    choices: ['dual', 'sso_first', 'sso_redirect', 'local_only'],
    bound: true,
    why: WHY.signin,
  }),
  site({ key: 'signin.ssoOnly', kind: 'boolean', def: false, bound: true, why: WHY.signin }),
  site({ key: 'signin.passkeyRoles', kind: 'list', def: [], bound: true, why: WHY.signin }),
  site({ key: 'signin.lockout.enabled', kind: 'boolean', def: false, bound: true, why: WHY.signin }),
  site({ key: 'signin.lockout.minutes', kind: 'limit', def: 15, min: 1, max: 10080, bound: true, why: WHY.signin }),
  site({
    key: 'signin.lockout.scope',
    kind: 'choice',
    def: 'ip_account',
    choices: ['ip_account', 'ip', 'account'],
    bound: true,
    why: WHY.signin,
  }),
  site({ key: 'signin.perMinute', kind: 'limit', def: 10, min: 1, max: 100000, bound: true, why: WHY.signin }),
  site({ key: 'signin.maxFailures', kind: 'limit', def: 10, min: 1, max: 100000, bound: true, why: WHY.signin }),
  site({ key: 'signin.windowMinutes', kind: 'limit', def: 15, min: 1, max: 10080, bound: true, why: WHY.signin }),
  site({ key: 'sessions.idleDays', kind: 'limit', def: 30, min: 1, max: 365, bound: true, why: WHY.sessions }),
  site({ key: 'sessions.maxDays', kind: 'limit', def: 90, min: 1, max: 3650, bound: true, why: WHY.sessions }),
  site({
    key: 'captcha.provider',
    kind: 'choice',
    def: 'image',
    choices: ['image', 'turnstile', 'recaptcha', 'hcaptcha'],
    bound: true,
    why: WHY.signin,
  }),
  site({ key: 'captcha.contexts', kind: 'doc', def: null, bound: true, why: WHY.signin }),
  site({ key: 'storage.remotes.require', kind: 'text', def: '', bound: true, why: WHY.storage }),
  site({
    key: 'storage.remotes.scope',
    kind: 'choice',
    def: 'new',
    choices: ['new', 'all'],
    bound: true,
    why: WHY.storage,
  }),
  site({ key: 'storage.remotes.graceDays', kind: 'limit', def: 30, min: 0, max: 365, bound: true, why: WHY.storage }),
  site({
    key: 'storage.localSpaces.maxBytes',
    kind: 'limit',
    def: null,
    min: MIB,
    max: 2 ** 50,
    nullable: true,
    bound: true,
    why: WHY.storage,
  }),
  site({
    key: 'storage.hosts',
    kind: 'list',
    def: [],
    bound: true,
    why: 'the hosts a remote may be on are the network policy',
  }),
  site({ key: 'cache.partial', kind: 'boolean', def: true, bound: true, why: WHY.cache }),
  site({
    key: 'cache.evictAfterDays',
    kind: 'limit',
    def: 14,
    min: 1,
    max: 365,
    nullable: true,
    bound: true,
    why: WHY.cache,
  }),
  site({ key: 'cache.minIdleHours', kind: 'limit', def: 24, min: 1, max: 720, bound: true, why: WHY.cache }),
  site({
    key: 'cache.clonesBytes',
    kind: 'limit',
    def: null,
    min: GIB,
    max: 2 ** 50,
    nullable: true,
    bound: true,
    why: WHY.cache,
  }),
  site({
    key: 'cache.minFreeBytes',
    kind: 'limit',
    def: 10 * GIB,
    min: 0,
    max: 2 ** 50,
    nullable: true,
    bound: true,
    why: WHY.cache,
  }),
  site({
    key: 'cache.assetsBytes',
    kind: 'limit',
    def: 5 * GIB,
    min: 64 * MIB,
    max: 2 ** 50,
    bound: true,
    why: WHY.cache,
  }),
  site({
    key: 'daily.languages',
    kind: 'list',
    def: ['zh-Hans', 'en'],
    bound: true,
    why: "the pages of the daily text's settings are the server's",
  }),
]
DEFS.push(...FOLDED)
// A secret a tenant may hold, and the feature it belongs to.
const TENANT_SECRETS: { name: string; feature: string }[] = [{ name: 'mail.password', feature: 'mail.ownSender' }]
const TOTALS: Record<'people' | 'spaces' | 'bytes' | 'pages', string> = {
  people: 'quota.people',
  spaces: 'quota.spaces',
  bytes: 'quota.bytes',
  pages: 'quota.pages',
}
const FILES_KEY = 'files.maxBytes'

function checkValue(d: Def, v: unknown): unknown {
  switch (d.kind) {
    case 'boolean':
      return typeof v === 'boolean' ? v : bad(`${d.key} is true or false`)
    case 'limit':
      if (v === null && (d.def === null || d.nullable)) return null
      return typeof v === 'number' && Number.isInteger(v) && v >= (d.min ?? 0) && v <= (d.max ?? 1e9)
        ? v
        : bad(`${d.key} is a number from ${d.min} to ${d.max}`)
    case 'choice':
      return typeof v === 'string' && d.choices?.includes(v) ? v : bad(`${d.key} is one of ${d.choices?.join(', ')}`)
    case 'list': {
      if (!Array.isArray(v) || v.some((x) => typeof x !== 'string')) return bad(`${d.key} is a list of text`)
      if (d.bound) return v
      const out: string[] = []
      for (const raw of v as string[]) {
        const x = raw.trim().replace(/^@/, '').toLowerCase()
        if (!x.includes('.') || /[\s@/:]/.test(x)) bad(`"${raw}" is not a domain`)
        if (!out.includes(x)) out.push(x)
      }
      return out
    }
    case 'text': {
      if (typeof v !== 'string') return bad(`${d.key} is text`)
      if (d.bound) return v.trim()
      const s = v.trim()
      if (s !== '' && !/^https:\/\/[^/\s@]+/.test(s)) bad('an https address, or ""')
      return s
    }
    default:
      return v
  }
}
function checkLower(d: Def, l: unknown): SettingLower {
  const o = l as SettingLower | null
  if (!o || typeof o !== 'object' || !['allow', 'restrict', 'lock'].includes(o.mode))
    return bad('lower.mode is allow, restrict or lock')
  if (o.mode !== 'restrict') return { mode: o.mode }
  if (d.kind === 'boolean')
    return o.options?.length === 1 && o.options[0] === false
      ? { mode: 'restrict', options: [false] }
      : bad('a boolean setting can only be restricted to [false]')
  if (d.kind === 'choice') {
    const opts = (o.options ?? []).filter((x): x is string => typeof x === 'string' && !!d.choices?.includes(x))
    return opts.length > 0 ? { mode: 'restrict', options: [...new Set(opts)] } : bad('restrict needs options')
  }
  if (d.kind === 'limit')
    return typeof o.max === 'number' && o.max >= (d.min ?? 0) && o.max <= (d.max ?? 1e9)
      ? { mode: 'restrict', max: o.max }
      : bad(`${d.key}: max is out of range`)
  return bad(`${d.key} cannot be restricted, only allowed or locked`)
}
const aboveMax = () =>
  new HttpError(400, 'setting_above_max', 'The value is above the maximum; raise the maximum first.')
const allows = (l: SettingLower, d: Def, v: unknown): boolean =>
  l.mode === 'lock'
    ? false
    : l.mode === 'allow'
      ? true
      : d.kind === 'boolean'
        ? v === false
        : d.kind === 'choice'
          ? (l.options ?? []).includes(v as string)
          : d.kind === 'limit'
            ? typeof v === 'number' && l.max !== undefined && v <= l.max
            : false
// The server's Lower.Nearest: a value in effect that a restriction forbids becomes the nearest one it allows (a limit
// its max, a boolean false, a choice the next stricter allowed one, else the strictest allowed; choices are loosest
// first).
const nearest = (l: SettingLower, d: Def, v: unknown): unknown => {
  if (l.mode !== 'restrict' || allows(l, d, v)) return v
  if (d.kind === 'limit') return l.max ?? v
  if (d.kind === 'boolean') return false
  if (d.kind === 'choice') {
    const cs = d.choices ?? []
    const opts = l.options ?? []
    const at = cs.indexOf(v as string)
    const stricter = cs.slice(at + 1).find((c) => opts.includes(c))
    if (stricter !== undefined) return stricter
    const looser = cs
      .slice(0, Math.max(at, 0))
      .reverse()
      .find((c) => opts.includes(c))
    if (looser !== undefined) return looser
  }
  return v
}
const tightens = (l: SettingLower, d: Def, above: SettingLower): boolean => {
  if (above.mode === 'allow') return true
  if (above.mode === 'lock') return l.mode === 'lock'
  if (l.mode === 'lock') return true
  if (l.mode !== 'restrict') return false
  return d.kind === 'limit' ? (l.max ?? Infinity) <= (above.max ?? Infinity) : true
}

export interface TenantBookOptions {
  now: () => string
  // The settings kept by the part of the server they belong to: the value, and a write that checks what looks at more
  // than one value (and throws the refusal).
  bound: Record<string, { get: () => unknown; set: (value: unknown, actor: string) => void }>
  // The secrets of the site (the mail password, the captcha's secret, the GitHub App's): listed, and changed.
  siteSecrets: {
    list: () => { name: string; set: boolean; updatedAt?: string }[]
    change: (name: string, value: string | null) => void
  }
  // What the requirement of a remote now asks of spaces (answered with a write of storage.remotes.*).
  remotesAffected: () => { InGrace: number; Overdue: number; Exempt: number }
  isSiteAdmin: (id: string) => boolean
  // Mail is enabled (registration needs it), ssoOnly is stored on, and a change of features.registration happened.
  mailReady: () => boolean
  ssoOnlyStored: () => boolean
  onRegistrationChanged: () => void
  // What a tenant uses (the default tenant's, from the mock's spaces and people).
  usageOf: (tenant: string) => TenantUsage
  // A limit changed: look at everyone's numbers again.
  onQuotaChanged: () => void
  // --max-spaces, when it was given: the site's quota.spacesPerPerson, read only.
  maxSpacesFlag?: number
  // --max-upload: the site's files.maxBytes, read only, and the ceiling of every layer.
  maxUpload: number
  // A real invite of the server for the first administrator of a new tenant (nothing is mailed).
  onTenantDeleted?: (id: string) => void
  makeInvite: (
    by: string,
    tenant: string,
    fill: { username: string; name: string; email: string },
  ) => CreatedInvite & { tenant: string; siteAdmin: boolean }
}

interface TenantRow {
  value?: unknown // undefined: follows the site
  lower?: SettingLower
}

// The site's manual rule for one tenant: a value in place of the site's, the rule the tenant's layer is held to, and
// the window it counts in (RFC 3339; none is always).
interface SiteRule {
  value?: unknown
  lower?: SettingLower
  from?: string
  until?: string
}

const ZERO: TenantUsage = { people: 0, spaces: 0, bytes: 0, pages: 0, measuredAt: null, over: [] }

export function createTenantBook(o: TenantBookOptions) {
  const enabledNow = () => siteRows.get('features.tenancy')?.value === true
  const tenantSecrets = new Map<string, Map<string, string>>()
  const tenants = new Map<string, Tenant>()
  const stamp = o.now()
  tenants.set(DEFAULT_TENANT, {
    id: DEFAULT_TENANT,
    slug: 'default',
    name: 'Default',
    default: true,
    disabled: false,
    registration: { open: false },
    quota: { people: null, spaces: null, bytes: null, pages: null },
    plan: null,
    usage: ZERO,
    created: stamp,
    updated: stamp,
  })
  const siteRows = new Map<string, { value?: unknown; lower?: SettingLower }>()
  const tenantRows = new Map<string, Map<string, TenantRow>>()
  const siteRules = new Map<string, Map<string, SiteRule>>()
  // The plans (a bundle of rules each) and the plan every tenant has.
  const plans = new Map<string, { slug: string; name: string; description: string; rules: Record<string, PlanRule> }>()
  const tenantPlan = new Map<string, string>()
  let seq = 1

  const siteValue = (d: Def): unknown =>
    d.key === FILES_KEY
      ? o.maxUpload
      : d.key === 'quota.spacesPerPerson' && o.maxSpacesFlag !== undefined
        ? o.maxSpacesFlag
        : d.bound && o.bound[d.key]
          ? o.bound[d.key].get()
          : siteRows.has(d.key) && siteRows.get(d.key)!.value !== undefined
            ? siteRows.get(d.key)!.value
            : d.def
  const siteLower = (d: Def): SettingLower =>
    siteRows.get(d.key)?.lower ?? (d.key === FILES_KEY ? { mode: 'restrict', max: o.maxUpload } : d.lower)
  const siteView = (d: Def): SiteSetting => ({
    key: d.key,
    kind: d.kind,
    layers: d.layers,
    ...(d.choices ? { choices: d.choices } : {}),
    ...(d.why ? { serverWide: d.why } : {}),
    site: {
      value: siteValue(d),
      lower: siteLower(d),
      ...((d.key === 'quota.spacesPerPerson' && o.maxSpacesFlag !== undefined) || d.key === FILES_KEY
        ? { fromFlag: true }
        : {}),
    },
  })
  const windowHolds = (r: SiteRule) => {
    const now = Date.parse(o.now())
    return (!r.from || now >= Date.parse(r.from)) && (!r.until || now < Date.parse(r.until))
  }
  const tenantView = (tenant: string, d: Def): TenantSetting => {
    const row = d.layers.includes('tenant') ? tenantRows.get(tenant)?.get(d.key) : undefined
    const manual = siteRules.get(tenant)?.get(d.key)
    const pr = plans.get(tenantPlan.get(tenant) ?? '')?.rules[d.key]
    const planRule: SiteRule | undefined = pr && { value: pr.value, lower: pr.lower ?? undefined }
    const sl = siteLower(d)
    // The rule in effect is the first whose window holds, manual then plan; the site's lock over everyone beats it.
    const rule = manual && windowHolds(manual) ? manual : planRule
    const active = !!rule
    const ruleOn = active && sl.mode !== 'lock'
    const base = ruleOn && rule.value !== undefined && rule.value !== null ? rule.value : siteValue(d)
    const above = ruleOn && rule.lower ? rule.lower : sl
    const own = row?.value
    const has = own !== undefined && own !== null
    const inEffect = !has || allows(above, d, own)
    // A restriction bounds the value in effect too, as on the server.
    const v = nearest(above, d, has && inEffect ? own : base)
    const rules: SettingRule[] = []
    if (manual)
      rules.push({
        source: 'manual',
        value: manual.value ?? null,
        lower: manual.lower ?? null,
        ...(manual.from ? { from: manual.from } : {}),
        ...(manual.until ? { until: manual.until } : {}),
        inEffect: ruleOn && rule === manual,
      })
    if (planRule)
      rules.push({
        source: 'plan',
        value: planRule.value ?? null,
        lower: planRule.lower ?? null,
        inEffect: ruleOn && rule === planRule,
        ...(rule === manual ? { underlying: true } : {}),
      })
    const now = Date.parse(o.now())
    const next = manual
      ? [manual.from, manual.until].filter((x): x is string => !!x && Date.parse(x) > now).sort()[0]
      : undefined
    return {
      key: d.key,
      kind: d.kind,
      layers: d.layers,
      ...(d.choices ? { choices: d.choices } : {}),
      site: { value: siteValue(d), lower: sl },
      tenant: {
        value: own ?? null,
        lower: row?.lower ?? null,
        inEffect,
        siteLocked: ruleOn && rule.lower?.mode === 'lock',
        ...(rules.length ? { rules } : {}),
        ...(next ? { next } : {}),
      },
      effective: d.key === FILES_KEY ? Math.min(o.maxUpload, v as number) : v,
    }
  }
  const effectiveOf = (tenant: string, d: Def) => tenantView(tenant, d)

  const planRef = (tenant: string): Tenant['plan'] => {
    const p = plans.get(tenantPlan.get(tenant) ?? '')
    return p ? { slug: p.slug, name: p.name } : null
  }
  // A tenant's totals are its quota.* settings in effect.
  const quotaOf = (id: string): Tenant['quota'] => {
    const out = { people: null, spaces: null, bytes: null, pages: null } as Tenant['quota']
    for (const [name, key] of Object.entries(TOTALS) as [keyof Tenant['quota'], string][]) {
      const v = effectiveOf(
        id,
        DEFS.find((d) => d.key === key)!,
      ).effective
      out[name] = typeof v === 'number' ? v : null
    }
    return out
  }
  const tenantOut = (t: Tenant): Tenant => ({
    ...t,
    quota: quotaOf(t.id),
    plan: planRef(t.id),
    usage: t.id === DEFAULT_TENANT ? o.usageOf(t.id) : ZERO,
    registration: { open: o.mailReady() && effectiveOf(t.id, DEFS[0]).effective === true },
  })
  const checkOpening = () => {
    if (!o.mailReady())
      throw new HttpError(
        409,
        'mail_not_configured',
        'registration verifies addresses by mail, and mail is not enabled',
      )
    if (o.ssoOnlyStored())
      throw new HttpError(
        409,
        'sso_only_with_registration',
        'password sign-in is refused (ssoOnly), so nobody could finish registering',
      )
  }
  const anyOpen = () => [...tenants.keys()].some((id) => effectiveOf(id, DEFS[0]).effective === true)
  const visible = (me: string, id: string): Tenant => {
    const t = tenants.get(id)
    if (!t || (!o.isSiteAdmin(me) && id !== DEFAULT_TENANT)) throw new HttpError(404, 'not_found', 'no such tenant')
    return t
  }
  const defOf = (key: string, layer: 'site' | 'tenant'): Def => {
    const d = DEFS.find((x) => x.key === key)
    if (!d || !d.layers.includes(layer)) throw new HttpError(404, 'not_found', 'no such setting')
    return d
  }
  const putSite = (
    key: string,
    body: Record<string, unknown>,
    actor: string,
  ): SiteSetting & { affected?: number; remotes?: { InGrace: number; Overdue: number; Exempt: number } } => {
    const d = defOf(key, 'site')
    for (const k of Object.keys(body)) if (k !== 'value' && k !== 'lower') bad(`unknown field ${k}`)
    if (!('value' in body) && !('lower' in body)) bad('give value, lower or both')
    const row = siteRows.get(key) ?? {}
    if ('value' in body) {
      if (key === FILES_KEY) throw new HttpError(409, 'conflict', `${key} is set by --max-upload; change the flag`)
      if (key === 'quota.spacesPerPerson' && o.maxSpacesFlag !== undefined)
        throw new HttpError(409, 'conflict', `${key} is set by a command-line flag; change the flag`)
      const v =
        d.kind === 'doc'
          ? body.value
          : body.value === null && d.kind === 'limit' && d.def !== null && !d.nullable
            ? d.def
            : checkValue(d, body.value)
      if (key === 'features.registration' && v === true) checkOpening()
      // The server's aboveMax: a site value over its own maximum is refused, not capped.
      const l = 'lower' in body ? (body.lower === null ? d.lower : checkLower(d, body.lower)) : (row.lower ?? d.lower)
      if (d.kind === 'limit' && l.mode === 'restrict' && !allows(l, d, v)) throw aboveMax()
      if (d.bound) o.bound[key].set(v, actor)
      else row.value = v
    }
    let affected: number | undefined
    if ('lower' in body) {
      row.lower = body.lower === null ? undefined : checkLower(d, body.lower)
      const l = row.lower ?? d.lower
      if (
        key === 'features.registration' &&
        l.mode !== 'lock' &&
        [...tenantRows.values()].some((r) => r.get(key)?.value === true)
      )
        checkOpening()
      affected = 0
      for (const rows of tenantRows.values()) {
        const t = rows.get(key)
        if (
          t &&
          ((t.value !== undefined && t.value !== null && !allows(l, d, t.value)) ||
            (t.lower && !tightens(t.lower, d, l)))
        )
          affected++
      }
    }
    siteRows.set(key, row)
    if (key === 'features.registration') o.onRegistrationChanged()
    if (key.startsWith('quota.')) o.onQuotaChanged()
    return {
      ...siteView(d),
      ...(affected === undefined ? {} : { affected }),
      ...(key.startsWith('storage.remotes.') ? { remotes: o.remotesAffected() } : {}),
    }
  }
  const putTenant = (
    tenant: string,
    key: string,
    body: Record<string, unknown>,
    bySite: boolean,
  ): TenantSetting & { warning?: { code: string; keys: string[]; message: string } } => {
    const d = DEFS.find((x) => x.key === key)
    if (!d || !(d.layers.includes('tenant') || d.forTenant)) throw new HttpError(404, 'not_found', 'no such setting')
    const fields = ['value', 'lower', 'siteLock', 'siteRule']
    for (const k of Object.keys(body)) if (!fields.includes(k)) bad(`unknown field ${k}`)
    if (!fields.some((k) => k in body)) bad('give value, lower, siteLock, siteRule or any')
    if (!bySite && ('siteLock' in body || 'siteRule' in body))
      throw new HttpError(409, 'setting_locked', `${key}: only the site's administrators set a rule for a tenant`)
    if ('siteLock' in body && 'siteRule' in body) bad(`${key}: give siteRule or siteLock, not both`)
    const siteOnly = !d.layers.includes('tenant')
    if (siteOnly && !bySite)
      throw new HttpError(409, 'setting_locked', `${key} is the site's: only its administrators set it for a tenant`)
    if (siteOnly && 'lower' in body) bad(`${key} has no tenant layer: give the rule for the tenant as siteRule`)
    const sl = siteLower(d)
    const rules = siteRules.get(tenant) ?? new Map<string, SiteRule>()
    const cur = rules.get(key)
    let hasValue = 'value' in body
    let value = body.value
    let hasRule = false
    let rule: SiteRule | undefined
    if ('siteLock' in body) {
      hasRule = true
      if (body.siteLock === true) {
        // Without a value, the value in effect now: locking never changes what the tenant sees.
        rule = { lower: { mode: 'lock' }, value: effectiveOf(tenant, d).effective }
        if (hasValue) {
          rule.value = value
          hasValue = false
        }
      }
    }
    if ('siteRule' in body) {
      hasRule = true
      const r = body.siteRule as { value?: unknown; lower?: unknown; from?: unknown; until?: unknown } | null
      rule = r ? { value: r.value, lower: r.lower ? checkLower(d, r.lower) : undefined } : undefined
      for (const k of ['from', 'until'] as const) {
        const t = r?.[k]
        if (t === undefined || t === null) continue
        if (typeof t !== 'string' || Number.isNaN(Date.parse(t))) bad('from and until are RFC 3339 times')
        if (rule) rule[k] = new Date(t as string).toISOString().replace('.000Z', 'Z')
      }
      if (rule?.from && rule.until && Date.parse(rule.until) <= Date.parse(rule.from))
        bad(`${key}: until is not after from`)
      if (rule?.until && Date.parse(rule.until) <= Date.parse(o.now()))
        throw new HttpError(
          400,
          'setting_until_past',
          `The rule for ${key} ends at ${rule.until}, which has already passed: give a later until, or none.`,
        )
    }
    if (siteOnly && hasValue) {
      rule = {
        value,
        lower: hasRule ? rule?.lower : cur?.lower,
        from: hasRule ? rule?.from : cur?.from,
        until: hasRule ? rule?.until : cur?.until,
      }
      hasRule = true
      hasValue = false
    }
    if (rule && rule.value !== undefined && rule.value !== null)
      rule.value = d.kind === 'doc' ? rule.value : checkValue(d, rule.value)
    if (!bySite && cur?.lower?.mode === 'lock' && sl.mode !== 'lock')
      throw new HttpError(409, 'setting_locked', `${key} is locked for this tenant by the site's administrators`)
    const next = hasRule ? rule : cur
    const above = next?.lower && sl.mode !== 'lock' ? next.lower : sl
    if (
      hasRule &&
      rule?.value != null &&
      d.kind === 'limit' &&
      above.mode === 'restrict' &&
      !allows(above, d, rule.value)
    )
      throw aboveMax()
    const rows = tenantRows.get(tenant) ?? new Map<string, TenantRow>()
    const row = rows.get(key) ?? {}
    if (hasValue && value !== null) {
      if (above.mode === 'lock')
        throw new HttpError(409, 'setting_locked', `${key} is locked by the site's administrators`)
      value = d.kind === 'doc' ? value : checkValue(d, value)
      if (!allows(above, d, value)) throw new HttpError(409, 'setting_restricted', `${key}: outside the site's rule`)
    }
    if (key === 'features.registration' && ((hasValue && value === true) || rule?.value === true)) checkOpening()
    let lower: SettingLower | undefined
    if ('lower' in body && body.lower !== null) {
      lower = checkLower(d, body.lower)
      if (above.mode === 'lock' && lower.mode !== 'lock')
        throw new HttpError(409, 'setting_locked', `${key} is locked by the site's administrators`)
      if (!tightens(lower, d, above))
        throw new HttpError(
          409,
          'setting_restricted',
          `${key}: the tenant's rule must be at least as strict as the site's`,
        )
    }
    if (hasValue) row.value = value === null ? undefined : value
    if ('lower' in body) row.lower = lower
    if (hasValue || 'lower' in body) {
      rows.set(key, row)
      tenantRows.set(tenant, rows)
    }
    let replaced = false
    if (hasRule) {
      replaced = !!rule && (!!rule.from || !!rule.until) && !!cur && !cur.from && !cur.until
      if (rule && (rule.value != null || rule.lower)) rules.set(key, rule)
      else rules.delete(key)
      siteRules.set(tenant, rules)
    }
    if (key === 'features.registration') o.onRegistrationChanged()
    if (key.startsWith('quota.')) o.onQuotaChanged()
    return {
      ...effectiveOf(tenant, d),
      ...(replaced
        ? {
            warning: {
              code: 'undated_value_replaced',
              keys: [key],
              message: 'A dated rule replaced the undated one.',
            },
          }
        : {}),
    }
  }

  // A plan's rule is checked as a rule for one tenant; a limit the tenant also sets must cap (restrict or lock).
  const checkPlanRule = (key: string, raw: unknown): PlanRule => {
    const d = DEFS.find((x) => x.key === key)
    if (!d || !(d.layers.includes('tenant') || d.forTenant)) return bad(`no setting ${key} has a rule for a tenant`)
    const r = raw as { value?: unknown; lower?: unknown } | null
    if (!r || typeof r !== 'object') return bad(`${key}: give a rule`)
    for (const k of Object.keys(r)) if (k !== 'value' && k !== 'lower') bad(`unknown field ${k}`)
    const out: PlanRule = {}
    if (r.lower !== undefined && r.lower !== null) out.lower = checkLower(d, r.lower)
    if (r.value !== undefined && r.value !== null) out.value = d.kind === 'doc' ? r.value : checkValue(d, r.value)
    if (out.value === undefined && out.lower === undefined) bad(`${key}: give a value, a lock or a restriction`)
    if (
      out.value !== undefined &&
      d.kind === 'limit' &&
      d.layers.includes('tenant') &&
      (!out.lower || out.lower.mode === 'allow')
    )
      throw new HttpError(
        400,
        'setting_rule_needs_max',
        `A plan or billing rule for ${key} must set a maximum or a lock.`,
      )
    if (
      out.value !== undefined &&
      out.lower?.mode === 'restrict' &&
      d.kind === 'limit' &&
      !allows(out.lower, d, out.value)
    )
      throw aboveMax()
    return out
  }
  const planOut = (p: { slug: string; name: string; description: string; rules: Record<string, PlanRule> }): Plan => ({
    slug: p.slug,
    name: p.name,
    description: p.description,
    rules: p.rules,
    tenants: [...tenantPlan.values()].filter((x) => x === p.slug).length,
  })
  const planFields = (body: Record<string, unknown>, creating: boolean) => {
    const out: { name?: string; description?: string } = {}
    if ('name' in body || creating) {
      const n = typeof body.name === 'string' ? body.name.trim() : ''
      if (!n || n.length > 80) bad('a plan has a name of 1 to 80 characters')
      out.name = n
    }
    if ('description' in body) {
      const d = typeof body.description === 'string' ? body.description.trim() : ''
      if (typeof body.description !== 'string' || [...d].length > 500) bad('a description is at most 500 characters')
      out.description = d
    }
    return out
  }
  const planRoute = (method: string, path: string, body: Record<string, unknown>): MockResponse | undefined => {
    const j = (b: unknown, status = 200): MockResponse => ({ status, body: b })
    if (path === '/admin/plans' && method === 'GET') return j({ plans: [...plans.values()].map(planOut) })
    if (path === '/admin/plans' && method === 'POST') {
      const f = planFields(body, true)
      const slug = typeof body.slug === 'string' ? body.slug : ''
      if (!SLUG.test(slug)) bad('a plan has a slug of 2 to 32 characters')
      if (plans.size >= 100) throw new HttpError(409, 'plan_limit', 'There are at most 100 plans.')
      if (plans.has(slug)) throw new HttpError(409, 'exists', 'That slug is taken.')
      const rules: Record<string, PlanRule> = {}
      for (const [k, v] of Object.entries((body.rules ?? {}) as Record<string, unknown>))
        if (v !== null) rules[k] = checkPlanRule(k, v)
      const p = { slug, name: f.name!, description: f.description ?? '', rules }
      plans.set(p.slug, p)
      return j(planOut(p), 201)
    }
    const one = /^\/admin\/plans\/([^/]+)$/.exec(path)
    if (!one) return undefined
    const p = plans.get(decodeURIComponent(one[1]))
    if (!p) throw new HttpError(404, 'not_found', 'No such plan.')
    if (method === 'GET') return j(planOut(p))
    if (method === 'PATCH') {
      const f = planFields(body, false)
      const next = { ...p.rules }
      for (const [k, v] of Object.entries((body.rules ?? {}) as Record<string, unknown>)) {
        if (v === null) delete next[k]
        else next[k] = checkPlanRule(k, v)
      }
      Object.assign(p, f, { rules: next })
      o.onQuotaChanged()
      const affected = [...tenantPlan.entries()].filter(([, s]) => s === p.slug).map(([id]) => id)
      return j({ ...planOut(p), affected })
    }
    if (method === 'DELETE') {
      const n = planOut(p).tenants
      if (n > 0)
        throw new HttpError(409, 'plan_in_use', 'Tenants use this plan; give them another first.', { tenants: n })
      plans.delete(p.slug)
      return { status: 204 }
    }
    return undefined
  }

  const page = (all: Tenant[], cursor: string, limit: number) => {
    const from = cursor ? all.findIndex((x) => x.id === cursor) + 1 : 0
    const items = all.slice(from, from + limit)
    return { items: items.map(tenantOut), next: from + limit < all.length ? items[items.length - 1].id : '' }
  }

  // The settings of one person's layers (GET /api/settings).
  const mine = (tenant: string) =>
    DEFS.filter((d) => d.layers.includes('space') || d.key === 'features.offline' || d.key === 'storage.assets').map(
      (d) => {
        const e = effectiveOf(tenant, d)
        if (d.key === 'storage.assets') {
          const r = assetsRule(tenant)
          return { key: d.key, effective: r.default, allowed: r.allowed, locked: r.locked }
        }
        // Not the person's to change: a client only reads whether it may keep offline copies.
        if (!d.layers.includes('space')) return { key: d.key, effective: e.effective }
        const on = e.effective === true
        const lockedBy = siteLower(d).mode === 'lock' ? 'site' : e.tenant?.lower?.mode === 'lock' ? 'tenant' : undefined
        const source =
          lockedBy ?? (!on ? (e.tenant && e.tenant.value !== null && e.tenant.inEffect ? 'tenant' : 'site') : undefined)
        return {
          key: d.key,
          effective: e.effective,
          space: { canChange: on && !lockedBy, inEffect: on, ...(source ? { source } : {}) },
        }
      },
    )

  // What the layer below the tenant may do with a setting: a site administrator answers to the site's rule only, anyone
  // else to the stricter of the site's and the tenant's.
  const lowerFor = (key: string, site: boolean): SettingLower => {
    const d = DEFS.find((x) => x.key === key)!
    const sl = siteLower(d)
    const tl = tenantRows.get(DEFAULT_TENANT)?.get(key)?.lower
    return site || !tl || !tightens(tl, d, sl) ? sl : tl
  }
  // Where the attachments of a new space of the default tenant go, and what its owner may ask for (the server's
  // AssetsRule): the site's lock, the site's lock for the tenant, the site's value, the tenant's value, in that order.
  const assetsRule = (tenant: string) => {
    const d = DEFS.find((x) => x.key === 'storage.assets')!
    const e = tenantView(tenant, d)
    const sl = siteLower(d)
    const below =
      sl.mode === 'lock' || (e.tenant?.siteLocked && e.tenant.inEffect)
        ? ({ mode: 'lock' } as SettingLower)
        : (e.tenant?.lower ?? ({ mode: 'allow' } as SettingLower))
    const merged = below.mode !== 'lock' && sl.mode === 'restrict' ? sl : below
    const choices = d.choices ?? []
    let value = e.effective as string
    let allowed = choices.filter((c) => merged.mode === 'allow' || (merged.mode === 'restrict' && allows(merged, d, c)))
    // A rule for the tenant that restricts the attachments leaves only what it names.
    const rl = e.tenant?.rules?.find((r) => r.inEffect)?.lower
    if (rl?.mode === 'restrict') {
      allowed = allowed.filter((c) => allows(rl, d, c))
      if (!allowed.includes(value)) value = allowed[0]
      return { default: value, allowed, locked: false }
    }
    if (merged.mode === 'lock') allowed = [value]
    else if (!allowed.includes(value)) value = allowed[0]
    return { default: value, allowed, locked: merged.mode === 'lock' }
  }
  return {
    mine,
    assetsRule,
    lowerFor,
    quotaOf,
    registrationStored: anyOpen,
    exists: (id: string) => tenants.has(id),
    // Whether daily.own / daily.api are in effect for a person's tenant (the mock has one world: the default).
    effective: (key: string): unknown =>
      effectiveOf(
        DEFAULT_TENANT,
        DEFS.find((d) => d.key === key)!,
      ).effective,
    // Returns undefined for a path that is not this book's.
    route(me: string, method: string, path: string, url: URL, body: Record<string, unknown>): MockResponse | undefined {
      const j = (b: unknown, status = 200): MockResponse => ({ status, body: b })
      const site = o.isSiteAdmin(me)
      const needSite = () => {
        if (!site) throw new HttpError(403, 'forbidden', 'only an administrator of the whole server may do that')
      }
      if (path === '/admin/tenants') {
        needSite()
        if (method === 'GET') {
          const q = (url.searchParams.get('q') ?? '').toLowerCase()
          const limit = Number(url.searchParams.get('limit') ?? 50)
          if (!(limit >= 1 && limit <= 200)) bad('limit is a number from 1 to 200')
          const all = [...tenants.values()]
            .filter((t) => !q || t.name.toLowerCase().includes(q) || t.slug.includes(q))
            .sort((a, b) => (a.default ? -1 : b.default ? 1 : a.name.toLowerCase() < b.name.toLowerCase() ? -1 : 1))
          return j({ ...page(all, url.searchParams.get('cursor') ?? '', limit), count: tenants.size })
        }
        if (method === 'POST') {
          if (!enabledNow()) throw new HttpError(409, 'tenants_off', 'more than the default tenant is switched off')
          const name = typeof body.name === 'string' ? body.name.trim() : ''
          if (!name || name.length > 80 || typeof body.slug !== 'string' || !SLUG.test(body.slug))
            bad('slug is 2 to 32 characters of a-z, 0-9 and -; name is 1 to 80 characters')
          if ([...tenants.values()].some((t) => t.slug === body.slug))
            throw new HttpError(409, 'exists', 'that slug is taken')
          if (tenants.size >= 1000) throw new HttpError(409, 'tenant_limit', 'a server has at most 1000 tenants')
          const id = `T${String(seq++).padStart(9, '0')}`
          const stampNow = o.now()
          const t: Tenant = {
            id,
            slug: body.slug as string,
            name,
            default: false,
            disabled: false,
            registration: { open: false },
            quota: { people: null, spaces: null, bytes: null, pages: null },
            plan: null,
            usage: ZERO,
            created: stampNow,
            updated: stampNow,
          }
          tenants.set(id, t)
          const out: Record<string, unknown> = { ...tenantOut(t) }
          if (body.admin && typeof body.admin === 'object') {
            const a = body.admin as { username?: unknown; name?: unknown; email?: unknown }
            const username = typeof a.username === 'string' ? a.username.trim().toLowerCase() : ''
            if (!/^[a-z0-9][a-z0-9._-]{1,31}$/.test(username)) bad('admin.username is not a username')
            if (a.name !== undefined && (typeof a.name !== 'string' || a.name.trim().length > 80))
              bad('admin.name is at most 80 characters')
            const email = typeof a.email === 'string' ? a.email.trim() : ''
            if (
              a.email !== undefined &&
              (typeof a.email !== 'string' || (email !== '' && (email.length > 254 || !email.includes('@'))))
            )
              bad('admin.email is not an address')
            out.invite = o.makeInvite(me, id, {
              username,
              name: typeof a.name === 'string' ? a.name.trim() : '',
              email,
            })
          }
          return j(out, 201)
        }
      }
      if (path === '/admin/plans' || path.startsWith('/admin/plans/')) {
        needSite()
        return planRoute(method, path, body)
      }
      const tplan = /^\/admin\/tenants\/([^/]+)\/plan$/.exec(path)
      if (tplan && method === 'PUT') {
        needSite()
        const t = visible(me, decodeURIComponent(tplan[1]))
        const slug = body.plan
        if (slug !== null && (typeof slug !== 'string' || !plans.has(slug))) bad(`there is no plan ${String(slug)}`)
        if (slug === null) tenantPlan.delete(t.id)
        else tenantPlan.set(t.id, slug as string)
        // Every manual rule of the tenant goes, dated or not.
        const cleared: ClearedRule[] = [...(siteRules.get(t.id) ?? new Map<string, SiteRule>()).entries()].map(
          ([key, r]) => ({
            key,
            value: r.value ?? null,
            lower: r.lower ?? null,
            ...(r.from ? { from: r.from } : {}),
            ...(r.until ? { until: r.until } : {}),
          }),
        )
        siteRules.delete(t.id)
        o.onQuotaChanged()
        return j({ plan: planRef(t.id), cleared })
      }
      const one = /^\/admin\/tenants\/([^/]+)$/.exec(path)
      if (one) {
        const id = decodeURIComponent(one[1])
        if (method === 'GET') return j(tenantOut(visible(me, id)))
        if (method === 'PATCH') {
          const t = visible(me, id)
          for (const k of Object.keys(body)) {
            if (k === 'quota') bad('the totals are the quota.* settings of the tenant')
            if (!['name', 'slug', 'disabled'].includes(k)) bad(`unknown field ${k}`)
            if (k !== 'name' && !site)
              throw new HttpError(403, 'forbidden', `only an administrator of the whole server may change ${k}`)
          }
          if ('name' in body) {
            const n = typeof body.name === 'string' ? body.name.trim() : ''
            if (!n || n.length > 80) bad('name is 1 to 80 characters')
            t.name = n
          }
          if ('slug' in body) {
            if (typeof body.slug !== 'string' || !SLUG.test(body.slug))
              bad('slug is 2 to 32 characters of a-z, 0-9 and -')
            if (t.default && body.slug !== 'default')
              throw new HttpError(409, 'default_tenant', "the default tenant's slug is default")
            if ([...tenants.values()].some((x) => x.id !== id && x.slug === body.slug))
              throw new HttpError(409, 'exists', 'that slug is taken')
            t.slug = body.slug as string
          }
          if ('disabled' in body) {
            if (typeof body.disabled !== 'boolean') bad('disabled is true or false')
            if (body.disabled && t.default)
              throw new HttpError(409, 'default_tenant', 'the default tenant cannot be disabled')
            t.disabled = body.disabled as boolean
          }
          t.updated = o.now()
          return j(tenantOut(t))
        }
        if (method === 'DELETE') {
          needSite()
          const t = visible(me, id)
          if (t.default) throw new HttpError(409, 'default_tenant', 'the default tenant cannot be deleted')
          tenants.delete(id)
          tenantSecrets.delete(id)
          tenantRows.delete(id)
          siteRules.delete(id)
          tenantPlan.delete(id)
          o.onTenantDeleted?.(id)
          return { status: 204 }
        }
      }
      if (path === '/admin/settings' && method === 'GET') {
        needSite()
        return j({ settings: DEFS.map(siteView) })
      }
      const sk = /^\/admin\/settings\/([^/]+)$/.exec(path)
      if (sk && method === 'PUT') {
        needSite()
        return j(putSite(decodeURIComponent(sk[1]), body, me))
      }
      // --- secrets: write-only, the answer is the list ---
      const secretsOf = (tenant: string) => {
        const have = tenantSecrets.get(tenant) ?? new Map<string, string>()
        return TENANT_SECRETS.map((x) => ({
          name: x.name,
          set: have.has(x.name),
          ...(have.has(x.name) ? { updatedAt: have.get(x.name) } : {}),
        }))
      }
      const readValue = (): string => {
        const v = body.value
        if (typeof v !== 'string') return bad('Give value, the secret. Clear it with DELETE.')
        if (v.length > 4096) bad('The secret is at most 4096 bytes.')
        return v
      }
      if (path === '/admin/secrets' && method === 'GET') {
        needSite()
        return j({ secrets: o.siteSecrets.list() })
      }
      const ss = /^\/admin\/secrets\/([^/]+)$/.exec(path)
      if (ss && (method === 'PUT' || method === 'DELETE')) {
        needSite()
        const name = decodeURIComponent(ss[1])
        if (!o.siteSecrets.list().some((x) => x.name === name)) throw new HttpError(404, 'not_found', 'No such secret.')
        o.siteSecrets.change(name, method === 'PUT' ? readValue() : null)
        return j({ secrets: o.siteSecrets.list() })
      }
      const ts0 = /^\/admin\/tenants\/([^/]+)\/secrets$/.exec(path)
      if (ts0 && method === 'GET') return j({ secrets: secretsOf(visible(me, decodeURIComponent(ts0[1])).id) })
      const ts1 = /^\/admin\/tenants\/([^/]+)\/secrets\/([^/]+)$/.exec(path)
      if (ts1 && (method === 'PUT' || method === 'DELETE')) {
        const t = visible(me, decodeURIComponent(ts1[1]))
        const name = decodeURIComponent(ts1[2])
        const spec = TENANT_SECRETS.find((x) => x.name === name)
        if (!spec) throw new HttpError(404, 'not_found', 'No such secret.')
        const have = tenantSecrets.get(t.id) ?? new Map<string, string>()
        if (method === 'PUT') {
          // A secret that belongs to a feature is refused while the tenant does not have it.
          const feature = DEFS.find((x) => x.key === spec.feature)!
          if (effectiveOf(t.id, feature).effective !== true)
            throw new HttpError(403, 'feature_off', `This organization does not have the feature ${spec.feature}.`, {
              feature: spec.feature,
            })
          readValue()
          have.set(name, o.now())
        } else have.delete(name)
        tenantSecrets.set(t.id, have)
        return j({ secrets: secretsOf(t.id) })
      }
      const ts = /^\/admin\/tenants\/([^/]+)\/settings$/.exec(path)
      if (ts && method === 'GET') {
        const t = visible(me, decodeURIComponent(ts[1]))
        return j({
          settings: DEFS.filter((d) => d.layers.includes('tenant') || d.forTenant).map((d) => effectiveOf(t.id, d)),
        })
      }
      const tk = /^\/admin\/tenants\/([^/]+)\/settings\/([^/]+)$/.exec(path)
      if (tk && method === 'PUT') {
        const t = visible(me, decodeURIComponent(tk[1]))
        return j(putTenant(t.id, decodeURIComponent(tk[2]), body, site))
      }
      return undefined
    },
    same,
  }
}
export type TenantBook = ReturnType<typeof createTenantBook>
