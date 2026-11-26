// The server's policy is the layered settings (GET /api/admin/settings, PUT /api/admin/settings/{key}); the pages that
// were once made of their own endpoints (security, storage, the daily text, machine access, tenancy) read and write
// those keys. This module is the one place that knows which key is which: it puts the settings and what is derived
// into the shape a page shows, and turns what a page changes into the writes, one key each.

import type {
  AdminDaily,
  AdminDailyBody,
  AdminStorage,
  AdminStorageBody,
  CaptchaContext,
  CaptchaContextSettings,
  DailyDefault,
  PutAdminStorage,
  Role,
  SecurityBody,
  SecuritySettings,
  SiteSetting,
} from './types'

export const K = {
  tokens: 'features.tokens',
  mcp: 'features.mcp',
  tenancy: 'features.tenancy',
  passkeys: 'features.passkeys',
  mode: 'signin.mode',
  ssoOnly: 'signin.ssoOnly',
  passkeyRoles: 'signin.passkeyRoles',
  lockoutEnabled: 'signin.lockout.enabled',
  lockoutMinutes: 'signin.lockout.minutes',
  lockoutScope: 'signin.lockout.scope',
  perMinute: 'signin.perMinute',
  maxFailures: 'signin.maxFailures',
  windowMinutes: 'signin.windowMinutes',
  idleDays: 'sessions.idleDays',
  maxDays: 'sessions.maxDays',
  captchaProvider: 'captcha.provider',
  captchaContexts: 'captcha.contexts',
  remotesRequire: 'storage.remotes.require',
  remotesScope: 'storage.remotes.scope',
  remotesGraceDays: 'storage.remotes.graceDays',
  localMaxBytes: 'storage.localSpaces.maxBytes',
  hosts: 'storage.hosts',
  cachePartial: 'cache.partial',
  cacheEvictAfterDays: 'cache.evictAfterDays',
  cacheMinIdleHours: 'cache.minIdleHours',
  cacheClonesBytes: 'cache.clonesBytes',
  cacheMinFreeBytes: 'cache.minFreeBytes',
  cacheAssetsBytes: 'cache.assetsBytes',
  dailyDefault: 'daily.default',
  dailyLanguages: 'daily.languages',
  dailyOwn: 'daily.own',
  dailyApi: 'daily.api',
} as const

// One write: the key and its new value.
export interface SettingStep {
  key: string
  value: unknown
}

export function lookup(list: readonly SiteSetting[]) {
  const by = new Map(list.map((s) => [s.key, s]))
  return {
    value: (key: string): unknown => by.get(key)?.site.value,
    flag: (key: string): boolean => by.get(key)?.site.fromFlag === true,
  }
}
type Lookup = ReturnType<typeof lookup>

const asNumber = (v: unknown, or: number): number => (typeof v === 'number' ? v : or)
const asNullable = (v: unknown): number | null => (typeof v === 'number' ? v : null)

// --- machine access and tenancy ---

export const machineOf = (list: readonly SiteSetting[]) => {
  const l = lookup(list)
  return { tokens: l.value(K.tokens) !== false, mcp: l.value(K.mcp) !== false }
}

// --- the security page ---

const CONTEXTS: readonly CaptchaContext[] = ['login', 'invite', 'setup', 'forgot', 'register']
const ONE: CaptchaContextSettings = { on: false, when: 'after_failures', failures: 3 }

export function securityOf(list: readonly SiteSetting[], b: SecurityBody): SecuritySettings {
  const l: Lookup = lookup(list)
  const doc = (l.value(K.captchaContexts) ?? {}) as Partial<Record<CaptchaContext, Partial<CaptchaContextSettings>>>
  const contexts = Object.fromEntries(CONTEXTS.map((c) => [c, { ...ONE, ...doc[c] }])) as Record<
    CaptchaContext,
    CaptchaContextSettings
  >
  return {
    login: {
      mode: l.value(K.mode) as SecuritySettings['login']['mode'],
      effectiveMode: b.login.effectiveMode,
      ssoOnly: l.value(K.ssoOnly) === true,
      ssoOnlyInEffect: b.login.ssoOnlyInEffect,
      ssoAvailable: b.login.ssoAvailable,
    },
    passkeys: {
      enabled: l.value(K.passkeys) !== false,
      available: b.passkeys.available,
      requireRoles: (Array.isArray(l.value(K.passkeyRoles)) ? l.value(K.passkeyRoles) : []) as Role[],
      required: b.passkeys.required,
      requiredWithout: b.passkeys.requiredWithout,
    },
    captcha: {
      provider: l.value(K.captchaProvider) as SecuritySettings['captcha']['provider'],
      siteKey: b.captcha.siteKey,
      hasSecret: b.captcha.hasSecret,
      contexts,
    },
    lockout: {
      enabled: l.value(K.lockoutEnabled) === true,
      minutes: asNumber(l.value(K.lockoutMinutes), 15),
      scope: l.value(K.lockoutScope) as SecuritySettings['lockout']['scope'],
    },
    limits: {
      perMinute: asNumber(l.value(K.perMinute), 10),
      maxFailures: asNumber(l.value(K.maxFailures), 10),
      windowMinutes: asNumber(l.value(K.windowMinutes), 15),
      sessionIdleDays: asNumber(l.value(K.idleDays), 30),
      sessionMaxDays: asNumber(l.value(K.maxDays), 90),
    },
    passwordAdmins: b.passwordAdmins,
    notices: b.notices,
  }
}

// --- the storage page ---

export function storageOf(list: readonly SiteSetting[], b: AdminStorageBody): AdminStorage {
  const l = lookup(list)
  const require = l.value(K.remotesRequire)
  return {
    remotes: {
      require: typeof require === 'string' && require !== '' ? require : null,
      scope: (l.value(K.remotesScope) as 'new' | 'all') ?? 'new',
      graceDays: asNumber(l.value(K.remotesGraceDays), 30),
      since: b.remotes.since,
    },
    hosts: {
      allowed: Array.isArray(l.value(K.hosts)) ? (l.value(K.hosts) as string[]) : [],
      ...(l.flag(K.hosts) ? { fromFlag: true } : {}),
    },
    localSpaces: { maxBytes: asNullable(l.value(K.localMaxBytes)) },
    cache: {
      partial: l.value(K.cachePartial) !== false,
      evictAfterDays: asNullable(l.value(K.cacheEvictAfterDays)),
      minIdleHours: asNumber(l.value(K.cacheMinIdleHours), 24),
      clonesBytes: asNullable(l.value(K.cacheClonesBytes)),
      minFreeBytes: asNullable(l.value(K.cacheMinFreeBytes)),
      assetsBytes: asNumber(l.value(K.cacheAssetsBytes), 0),
    },
    git: b.git,
    affected: b.affected,
  }
}

// The writes a change of the storage policy makes, in the order the server should see them: the requirement before
// its scope and grace period.
export function storageWrites(p: PutAdminStorage): SettingStep[] {
  const out: SettingStep[] = []
  const put = (key: string, value: unknown) => out.push({ key, value })
  if (p.remotes) {
    if ('require' in p.remotes) put(K.remotesRequire, p.remotes.require ?? '')
    if ('scope' in p.remotes) put(K.remotesScope, p.remotes.scope)
    if ('graceDays' in p.remotes) put(K.remotesGraceDays, p.remotes.graceDays)
  }
  if (p.hosts && 'allowed' in p.hosts) put(K.hosts, p.hosts.allowed)
  if (p.localSpaces && 'maxBytes' in p.localSpaces) put(K.localMaxBytes, p.localSpaces.maxBytes)
  const c = p.cache
  if (c) {
    if ('partial' in c) put(K.cachePartial, c.partial)
    if ('evictAfterDays' in c) put(K.cacheEvictAfterDays, c.evictAfterDays)
    if ('minIdleHours' in c) put(K.cacheMinIdleHours, c.minIdleHours)
    if ('clonesBytes' in c) put(K.cacheClonesBytes, c.clonesBytes)
    if ('minFreeBytes' in c) put(K.cacheMinFreeBytes, c.minFreeBytes)
    if ('assetsBytes' in c) put(K.cacheAssetsBytes, c.assetsBytes)
  }
  return out
}

// --- the daily text's page ---

export function dailyOf(list: readonly SiteSetting[], b: AdminDailyBody): AdminDaily {
  const l = lookup(list)
  const languages = l.value(K.dailyLanguages)
  return {
    ...b,
    default: (l.value(K.dailyDefault) ?? null) as DailyDefault,
    languages: Array.isArray(languages) ? (languages as string[]) : [],
    allow: { own: l.value(K.dailyOwn) !== false, api: l.value(K.dailyApi) !== false },
  }
}

export function dailyWrites(p: {
  default?: DailyDefault
  languages?: string[]
  allow?: { own?: boolean; api?: boolean }
}): SettingStep[] {
  const out: SettingStep[] = []
  if ('default' in p) out.push({ key: K.dailyDefault, value: p.default ?? null })
  if ('languages' in p) out.push({ key: K.dailyLanguages, value: p.languages })
  if (p.allow && 'own' in p.allow) out.push({ key: K.dailyOwn, value: p.allow.own })
  if (p.allow && 'api' in p.allow) out.push({ key: K.dailyApi, value: p.allow.api })
  return out
}
