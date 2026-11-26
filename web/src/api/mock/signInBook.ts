// The mock's side of milestone 2.8 (docs/api.md, "Signing in" and "Administration"): the security settings,
// the sign-in providers, the captcha and the audit log. The flows that need a session (signing in, the
// passkeys) are in server.ts; this holds the state and the checks of what an administrator may set.

import type {
  AuditItem,
  AuthProvider,
  CaptchaContext,
  CaptchaContextSettings,
  CaptchaProvider,
  LoginMode,
  ProviderInput,
  ProviderKind,
  PublicProvider,
  Role,
  SecurityNotice,
  SecurityBody,
  SecuritySettings,
} from '../types.ts'
import { AUDIT_CATEGORIES, categoryOfKind, isSiteKind } from './auditKinds.ts'
import { HttpError, isRecord } from './store.ts'

export interface PasskeyRec {
  id: string
  // The credential id the browser reported; what a sign-in finds the passkey by.
  credId: string
  name: string
  created: string
  lastUsed: string | null
  synced: boolean
}

// What the book needs to know of a user.
export interface BookUser {
  id: string
  username: string
  role: Role
  disabled: boolean
  passkeyRequired: boolean
  passwordHash?: string
  passkeys: PasskeyRec[]
  identities: { id: string; providerSlug: string }[]
}

export interface ProviderRec {
  view: AuthProvider
  // Mock only: the subject the pretend identity provider signs people in as.
  sub: string
  secret: boolean
}

// Settings that differ from the defaults, as a test or the dev server sets them.
export interface MockSecurity {
  login?: Partial<{ mode: LoginMode; ssoOnly: boolean }>
  passkeys?: Partial<{ enabled: boolean; requireRoles: Role[] }>
  captcha?: Partial<{
    provider: CaptchaProvider
    siteKey: string
    hasSecret: boolean
    contexts: Partial<Record<CaptchaContext, Partial<CaptchaContextSettings>>>
  }>
  lockout?: Partial<SecuritySettings['lockout']>
  limits?: Partial<SecuritySettings['limits']>
}

export interface SignInBookOptions {
  now: () => number
  // The server's public URL; null means it has none (`--public-url` not given).
  publicUrl: string | null
  // Whether passkeys can work (a suitable public URL).
  passkeysAvailable: boolean
  users: () => Iterable<BookUser>
  newAuditId: () => string
  // Groups (3.2): a group's source, undefined for a group that does not exist; and
  // what to do when a provider goes (its memberships go with it).
  groupSource?: (id: string) => string | undefined
  onProviderDeleted?: (slug: string) => void
  onMappingRemoved?: (slug: string, value: string, keep: string) => void
  // 3.3: whether any tenant's registration is open (ssoOnly cannot be switched on then).
  registrationOpen?: () => boolean
  // Whether an optional audit category is switched on (its setting, and the default).
  categoryOn?: (setting: string, def: boolean) => boolean
  security?: MockSecurity
  providers?: (ProviderInput & {
    slug: string
    kind: ProviderKind
    name: string
    sub?: string
    status?: AuthProvider['status']
  })[]
  notices?: SecurityNotice[]
}

const CONTEXTS: CaptchaContext[] = ['login', 'invite', 'setup', 'forgot', 'register']
export const SLUG = /^[a-z0-9][a-z0-9-]{0,31}$/
export const MAX_PROVIDERS = 50

function bad(message: string, code = 'invalid'): never {
  throw new HttpError(400, code, message)
}
const int = (v: unknown, lo: number, hi: number, field: string): number => {
  if (typeof v !== 'number' || !Number.isInteger(v) || v < lo || v > hi)
    return bad(`${field} 要是 ${lo} 到 ${hi} 的整数`)
  return v
}
const bool = (v: unknown, field: string): boolean => (typeof v === 'boolean' ? v : bad(`${field} 要是布尔值`))
const oneOf = <T extends string>(v: unknown, list: readonly T[], field: string): T =>
  typeof v === 'string' && (list as readonly string[]).includes(v) ? (v as T) : bad(`${field} 不对`)

export function createSignInBook(o: SignInBookOptions) {
  const iso = () => new Date(o.now()).toISOString().replace(/\.\d{3}Z$/, 'Z')
  const base = (o.publicUrl ?? '').replace(/\/+$/, '')

  // --- audit ---
  const audit: AuditItem[] = []
  const record = (
    kind: string,
    row: {
      actor?: string | null
      user?: string | null
      username?: string | null
      ip?: string
      detail?: Record<string, unknown>
    } = {},
  ) => {
    const category = categoryOfKind(kind)
    // An optional category that is switched off writes nothing, as on the server.
    const info = AUDIT_CATEGORIES.find((c) => c.id === category)
    if (info && !info.core && o.categoryOn && !o.categoryOn(info.setting ?? '', info.def)) return
    audit.push({
      id: o.newAuditId(),
      at: iso(),
      kind,
      category,
      capacity: null,
      actor: row.actor ?? null,
      user: row.user ?? null,
      username: row.username ?? null,
      ip: row.ip ?? '203.0.113.7',
      detail: row.detail ?? {},
    })
  }
  // The rows a request's filters name, newest first. hideSite leaves out the kinds only a site administrator sees (a
  // tenant administrator's view); a tenant's own rows are the whole mock, which has one tenant.
  const auditRows = (q: URLSearchParams, hideSite: boolean): AuditItem[] => {
    const categories = q
      .getAll('category')
      .flatMap((v) => v.split(','))
      .map((v) => v.trim())
      .filter(Boolean)
    for (const c of categories) if (!AUDIT_CATEGORIES.some((x) => x.id === c)) bad(`未知的类别 ${c}`)
    const capacity = (q.get('capacity') ?? '').trim()
    if (capacity && capacity !== 'site_admin' && capacity !== 'tenant_admin')
      bad('capacity 要是 site_admin 或 tenant_admin')
    // A day (YYYY-MM-DD) or an RFC 3339 time; a day in `to` includes the day.
    const when = (name: string): { t: number; day: boolean } | null => {
      const v = (q.get(name) ?? '').trim()
      if (!v) return null
      const day = /^\d{4}-\d{2}-\d{2}$/.test(v)
      const t = Date.parse(day ? `${v}T00:00:00Z` : v)
      if (Number.isNaN(t) || (!day && !/Z|[+-]\d{2}:\d{2}$/.test(v))) return bad(`${name} 要是时间或日期`)
      return { t, day }
    }
    const from = when('from')
    const to = when('to')
    const upto = to ? to.t + (to.day ? 86_400_000 : 0) : Infinity
    const kind = (q.get('kind') ?? '').trim()
    const user = (q.get('user') ?? '').trim()
    const actor = (q.get('actor') ?? '').trim()
    return audit
      .filter((a) => {
        const at = Date.parse(a.at)
        return (
          (categories.length === 0 || categories.includes(a.category)) &&
          (!kind || a.kind === kind) &&
          (!user || a.user === user) &&
          (!actor || a.actor === actor) &&
          (!capacity || a.capacity === capacity) &&
          (!from || at >= from.t) &&
          at < upto &&
          !(hideSite && isSiteKind(a.kind))
        )
      })
      .reverse()
  }
  const auditList = (
    q: URLSearchParams,
    hideSite: boolean,
    cursor: string | null,
    limit: number,
  ): { items: AuditItem[]; next: string } => {
    const all = auditRows(q, hideSite)
    const start = cursor ? all.findIndex((a) => a.id === cursor) : 0
    if (cursor && start < 0) throw new HttpError(400, 'invalid', 'cursor 不对')
    const items = all.slice(start, start + limit)
    return { items, next: all[start + limit]?.id ?? '' }
  }
  // GET /admin/audit/kinds: the registry, with whether each optional category is on.
  const auditKinds = (hideSite: boolean) => ({
    categories: AUDIT_CATEGORIES.map((c) => ({
      id: c.id,
      core: c.core,
      setting: c.setting,
      on: c.core || (o.categoryOn ? o.categoryOn(c.setting ?? '', c.def) : c.def),
      kinds: c.kinds.filter((k) => !(hideSite && k.site)),
    })),
  })
  // The CSV of GET /admin/audit/download: at most 100,000 rows, a cell that starts like a formula quoted as text.
  const auditCsv = (q: URLSearchParams, hideSite: boolean): string => {
    const cell = (v: string) => {
      const safe = /^[=+\-@\t\r]/.test(v) ? `'${v}` : v
      return /[",\r\n]/.test(safe) ? `"${safe.replace(/"/g, '""')}"` : safe
    }
    const lines = [
      ['id', 'time', 'category', 'kind', 'capacity', 'tenant', 'actor', 'user', 'username', 'ip', 'detail'],
    ]
    for (const a of auditRows(q, hideSite).slice(0, 100_000))
      lines.push([
        a.id,
        a.at,
        a.category,
        a.kind,
        a.capacity ?? '',
        '',
        a.actor ?? '',
        a.user ?? '',
        a.username ?? '',
        a.ip,
        JSON.stringify(a.detail),
      ])
    return lines.map((l) => l.map(cell).join(',')).join('\n') + '\n'
  }

  // --- security settings ---
  const security = {
    login: { mode: 'dual' as LoginMode, ssoOnly: false },
    passkeys: { enabled: true, requireRoles: [] as Role[] },
    captcha: {
      provider: 'image' as CaptchaProvider,
      siteKey: '',
      secretFor: '' as CaptchaProvider | '',
      contexts: {
        login: { on: false, when: 'after_failures', failures: 3 },
        invite: { on: false, when: 'after_failures', failures: 3 },
        setup: { on: false, when: 'after_failures', failures: 3 },
        forgot: { on: false, when: 'after_failures', failures: 3 },
        register: { on: false, when: 'after_failures', failures: 3 },
      } as SecuritySettings['captcha']['contexts'],
    },
    lockout: { enabled: false, minutes: 15, scope: 'ip_account' as SecuritySettings['lockout']['scope'] },
    limits: { perMinute: 10, maxFailures: 10, windowMinutes: 15, sessionIdleDays: 30, sessionMaxDays: 90 },
  }
  const seed = o.security
  if (seed?.login) Object.assign(security.login, seed.login)
  if (seed?.passkeys) Object.assign(security.passkeys, seed.passkeys)
  if (seed?.captcha) {
    const { contexts, ...rest } = seed.captcha
    Object.assign(security.captcha, rest)
    if (contexts) for (const c of CONTEXTS) Object.assign(security.captcha.contexts[c], contexts[c])
    if (rest.hasSecret) security.captcha.secretFor = security.captcha.provider
  }
  if (seed?.lockout) Object.assign(security.lockout, seed.lockout)
  if (seed?.limits) Object.assign(security.limits, seed.limits)

  // --- providers ---
  const providers = new Map<string, ProviderRec>()
  const callbackOf = (kind: ProviderKind, slug: string, legacy = false) =>
    `${base}/api/auth/${kind}/${legacy ? '' : `${slug}/`}callback`.replace(/\/\/callback$/, '/callback')

  const enabledProviders = () =>
    [...providers.values()]
      .filter((p) => p.view.enabled)
      .sort((a, b) => a.view.order - b.view.order || a.view.name.localeCompare(b.view.name))
  const publicProviders = (): PublicProvider[] =>
    enabledProviders().map((p) => ({ slug: p.view.slug, kind: p.view.kind, name: p.view.name, icon: p.view.icon }))
  const effectiveMode = (): LoginMode => (enabledProviders().length === 0 ? 'local_only' : security.login.mode)
  const ssoOnlyInEffect = () => security.login.ssoOnly && effectiveMode() !== 'local_only'
  const passkeyOn = () => security.passkeys.enabled && o.passkeysAvailable

  const people = (slug: string) => {
    let n = 0
    for (const u of o.users()) if (u.identities.some((i) => i.providerSlug === slug)) n++
    return n
  }
  const withDerived = (rec: ProviderRec): AuthProvider => {
    const v = rec.view
    return {
      ...v,
      people: people(v.slug),
      ...(v.oidc ? { oidc: { ...v.oidc, hasSecret: rec.secret } } : {}),
      ...(v.github ? { github: { ...v.github, hasSecret: rec.secret } } : {}),
    }
  }

  const fresh = (
    input: ProviderInput & { slug: string; kind: ProviderKind; name: string },
    sub?: string,
    legacy = false,
  ): ProviderRec => {
    const at = iso()
    const view: AuthProvider = {
      slug: input.slug,
      kind: input.kind,
      name: input.name,
      icon: input.icon ?? '',
      enabled: input.enabled ?? true,
      order: input.order ?? providers.size,
      autoCreate: input.autoCreate ?? false,
      mergeVerifiedEmail: input.mergeVerifiedEmail ?? false,
      groups: { enabled: false, claim: input.kind === 'oidc' ? 'groups' : '', missing: 'keep', map: [] },
      status: { ok: true, checked: at, message: '' },
      people: 0,
      created: at,
      updated: at,
    }
    if (input.kind === 'oidc') {
      view.oidc = {
        issuer: input.oidc?.issuer ?? '',
        clientId: input.oidc?.clientId ?? '',
        hasSecret: false,
        scopes: input.oidc?.scopes ?? ['profile', 'email'],
        legacyCallback: legacy || (input.oidc?.legacyCallback ?? false),
        redirectUrl: callbackOf('oidc', input.slug, legacy || input.oidc?.legacyCallback === true),
      }
    } else if (input.kind === 'github') {
      view.github = {
        baseUrl: input.github?.baseUrl ?? 'https://github.com',
        clientId: input.github?.clientId ?? '',
        hasSecret: false,
        organizations: input.github?.organizations ?? [],
        redirectUrl: callbackOf('github', input.slug),
      }
    } else {
      const s = input.saml
      view.saml = {
        metadataUrl: s?.metadataUrl ?? '',
        metadataXml: s?.metadataXml ?? '',
        idpEntityId: 'https://idp.example/metadata',
        entityId: s?.entityId || `${base}/api/auth/saml/${input.slug}/metadata`,
        acsUrl: `${base}/api/auth/saml/${input.slug}/acs`,
        metadataPath: `/api/auth/saml/${input.slug}/metadata`,
        attributes: s?.attributes ?? { email: 'mail', name: 'displayName', username: 'uid' },
        nameIdFormat: s?.nameIdFormat ?? '',
        signRequests: s?.signRequests ?? false,
        allowIdpInitiated: s?.allowIdpInitiated ?? false,
        certificate: '-----BEGIN CERTIFICATE-----\nMIIBmock\n-----END CERTIFICATE-----',
        certificateExpires: new Date(o.now() + 10 * 365 * 86_400_000).toISOString().replace(/\.\d{3}Z$/, 'Z'),
      }
    }
    return { view, sub: sub ?? `${input.slug}-sub-1`, secret: false }
  }
  for (const p of o.providers ?? []) {
    const rec = fresh(p, p.sub)
    Object.assign(rec.view, { ...(p.status ? { status: p.status } : {}) })
    providers.set(p.slug, rec)
  }
  // A provider made from the command line before 2.8 (the `oidc` option of the mock).
  const addLegacy = (name: string, autoCreate: boolean, sub: string) => {
    const rec = fresh(
      { slug: 'sso', kind: 'oidc', name, autoCreate, oidc: { issuer: 'https://sso.example', clientId: 'petalbook' } },
      sub,
      true,
    )
    rec.secret = true
    providers.set('sso', rec)
  }

  const issuerOf = (v: AuthProvider) => v.oidc?.issuer ?? v.saml?.idpEntityId ?? v.github?.baseUrl ?? ''
  const checkProvider = (input: ProviderInput, kind: ProviderKind, slug: string, existing?: AuthProvider) => {
    if (!o.publicUrl) bad('服务器没有公开地址（--public-url）', 'public_url_required')
    if (
      input.name !== undefined &&
      (typeof input.name !== 'string' || input.name.trim() === '' || Array.from(input.name).length > 60)
    )
      bad('名称需要 1 到 60 个字符')
    if (input.icon !== undefined && input.icon !== '' && !/^preset:[a-z]+$/.test(input.icon)) bad('icon 不对')
    if (input.order !== undefined) int(input.order, 0, 10_000, 'order')
    for (const f of ['enabled', 'autoCreate', 'mergeVerifiedEmail'] as const)
      if (input[f] !== undefined) bool(input[f], f)
    const merged = { ...(existing ?? {}), ...input } as ProviderInput
    if (kind === 'oidc') {
      const issuer = input.oidc?.issuer ?? existing?.oidc?.issuer ?? ''
      if (!issuer) bad('缺少 issuer')
      if (!/^https:\/\//.test(issuer)) bad('issuer 要是 https 地址', 'oidc_issuer_invalid')
      if (!(input.oidc?.clientId ?? existing?.oidc?.clientId)) bad('缺少 clientId')
      if (input.oidc?.legacyCallback) {
        for (const p of providers.values())
          if (p.view.slug !== slug && p.view.oidc?.legacyCallback)
            throw new HttpError(409, 'legacy_callback_in_use', '另一个提供方已经在用旧的回调地址')
      }
    } else if (kind === 'github') {
      if (!(input.github?.clientId ?? existing?.github?.clientId)) bad('缺少 clientId')
      const orgs = input.github?.organizations ?? existing?.github?.organizations ?? []
      if ((merged.autoCreate ?? existing?.autoCreate) && orgs.length === 0)
        bad('GitHub 自动建号必须指定组织', 'auto_create_needs_org')
    } else {
      if (!/^https:\/\//.test(o.publicUrl ?? '')) bad('SAML 需要 https 的公开地址', 'saml_needs_https')
      const url = input.saml?.metadataUrl ?? existing?.saml?.metadataUrl ?? ''
      const xml = input.saml?.metadataXml ?? existing?.saml?.metadataXml ?? ''
      if (!url && !xml) bad('缺少元数据（地址或 XML）')
      if (url && !/^https:\/\//.test(url)) bad('元数据地址要是 https')
      if ((xml && !xml.includes('EntityDescriptor')) || url.includes('bad-metadata'))
        bad('元数据解析不了', 'saml_metadata_invalid')
    }
    // The identity provider's own address may not be shared.
    const issuer =
      kind === 'oidc'
        ? (input.oidc?.issuer ?? existing?.oidc?.issuer)
        : kind === 'github'
          ? (input.github?.baseUrl ?? existing?.github?.baseUrl ?? 'https://github.com')
          : undefined
    if (issuer)
      for (const p of providers.values())
        if (p.view.slug !== slug && p.view.kind === kind && issuerOf(p.view) === issuer)
          throw new HttpError(409, 'provider_issuer_in_use', '另一个提供方已经用了这个签发方')
  }

  // The groups setting: what is refused, in the order the server refuses it.
  const checkGroups = (g: unknown, kind: ProviderKind) => {
    if (g === undefined) return
    if (!isRecord(g)) bad('groups 要是一个对象')
    if (g.enabled !== undefined) bool(g.enabled, 'groups.enabled')
    if (g.claim !== undefined) {
      if (typeof g.claim !== 'string' || new TextEncoder().encode(g.claim).length > 256)
        bad('groups.claim 最多 256 字节')
      if (kind === 'github' && g.claim.trim() !== '') bad('GitHub 的组就是组织，不能设置 groups.claim')
    }
    if (g.missing !== undefined && g.missing !== 'keep' && g.missing !== 'clear') bad('groups.missing 是 keep 或 clear')
    if (g.map !== undefined) {
      if (!Array.isArray(g.map) || g.map.length > 1000) bad('groups.map 最多 1000 项')
      const seen = new Set<string>()
      for (const e of g.map as unknown[]) {
        if (
          !isRecord(e) ||
          typeof e.value !== 'string' ||
          e.value === '' ||
          new TextEncoder().encode(e.value).length > 256
        )
          bad('groups.map 的值是 1 到 256 个字节')
        if (typeof e.groupId !== 'string' || o.groupSource?.(e.groupId) === undefined)
          throw new HttpError(404, 'group_not_found', '映射里有不存在的组')
      }
      for (const e of g.map as { value: string }[]) {
        if (seen.has(e.value)) throw new HttpError(409, 'exists', `值 ${e.value} 映射了两次`)
        seen.add(e.value)
      }
      for (const e of g.map as { groupId: string }[])
        if (o.groupSource?.(e.groupId) !== 'synced') throw new HttpError(409, 'group_not_synced', '只能映射到同步组')
    }
  }
  const applyGroups = (v: AuthProvider, g: ProviderInput['groups'], slug: string, changed: Record<string, unknown>) => {
    if (!g) return
    const cur = v.groups
    if (g.enabled !== undefined && g.enabled !== cur.enabled) changed['groups.enabled'] = cur.enabled = g.enabled
    if (g.claim !== undefined && g.claim.trim() !== cur.claim) changed['groups.claim'] = cur.claim = g.claim.trim()
    if (g.missing !== undefined && g.missing !== cur.missing) changed['groups.missing'] = cur.missing = g.missing
    if (g.map !== undefined) {
      const next = new Map(g.map.map((e) => [e.value, e.groupId]))
      for (const old of cur.map)
        if (next.get(old.value) !== old.groupId) o.onMappingRemoved?.(slug, old.value, next.get(old.value) ?? '')
      if (JSON.stringify(cur.map) !== JSON.stringify(g.map)) changed['groups.map'] = g.map.length
      cur.map = g.map.map((e) => ({ ...e }))
    }
    if (cur.enabled && v.kind === 'saml' && cur.claim === '') bad('SAML 要写明属性名（groups.claim）')
  }

  const createProvider = (body: Record<string, unknown>, actor: string): AuthProvider => {
    const input = body as ProviderInput
    const slug = typeof input.slug === 'string' ? input.slug : ''
    if (!SLUG.test(slug)) bad('slug 是 1 到 32 个字符：小写字母、数字和连字符，以字母或数字开头')
    const kind = oneOf(input.kind, ['oidc', 'saml', 'github'] as const, 'kind')
    if (typeof input.name !== 'string' || input.name.trim() === '') bad('缺少名称')
    if (providers.has(slug)) throw new HttpError(409, 'exists', '这个 slug 已经有了')
    if (providers.size >= MAX_PROVIDERS) throw new HttpError(409, 'provider_limit', '最多 50 个提供方')
    const kinds = ['oidc', 'saml', 'github'].filter((k) => (input as Record<string, unknown>)[k] !== undefined)
    if (kinds.length !== 1 || kinds[0] !== kind) bad(`需要 ${kind} 这一块，也只能有这一块`)
    checkProvider(input, kind, slug)
    checkGroups((input as Record<string, unknown>).groups, kind)
    const rec = fresh({ ...input, slug, kind, name: input.name!.trim() })
    applyGroups(rec.view, input.groups, slug, {})
    const secret = input.oidc?.clientSecret ?? input.github?.clientSecret
    rec.secret = typeof secret === 'string' && secret !== ''
    // A provider whose discovery document or metadata cannot be fetched is stored, switched off.
    const probe = `${input.oidc?.issuer ?? ''}${input.saml?.metadataUrl ?? ''}${input.github?.baseUrl ?? ''}`
    if (probe.includes('unreachable')) {
      rec.view.status = { ok: false, checked: iso(), message: '连不上这个地址' }
      if (input.enabled === undefined) rec.view.enabled = false
    }
    providers.set(slug, rec)
    record('provider_created', { actor, detail: { slug, kind } })
    return withDerived(rec)
  }

  const patchProvider = (slug: string, body: Record<string, unknown>, actor: string): AuthProvider => {
    const rec = providers.get(slug)
    if (!rec) throw new HttpError(404, 'not_found', '没有这个提供方')
    if (body.slug !== undefined && body.slug !== slug) bad('slug 不能改')
    if (body.kind !== undefined && body.kind !== rec.view.kind) bad('kind 不能改')
    const input = body as ProviderInput
    checkProvider(input, rec.view.kind, slug, rec.view)
    checkGroups((input as Record<string, unknown>).groups, rec.view.kind)
    const v = rec.view
    const changed: Record<string, unknown> = {}
    applyGroups(v, input.groups, slug, changed)
    for (const f of ['name', 'icon', 'enabled', 'order', 'autoCreate', 'mergeVerifiedEmail'] as const) {
      if (input[f] !== undefined && input[f] !== v[f]) {
        ;(v as unknown as Record<string, unknown>)[f] = input[f]
        changed[f] = input[f]
      }
    }
    for (const kind of ['oidc', 'saml', 'github'] as const) {
      const block = input[kind] as Record<string, unknown> | undefined
      if (!block || !isRecord(block)) continue
      const target = v[kind] as unknown as Record<string, unknown> | undefined
      if (!target) continue
      for (const [k, val] of Object.entries(block)) {
        if (k === 'clientSecret') {
          rec.secret = typeof val === 'string' && val !== ''
          changed.clientSecret = 'changed'
        } else if (k in target && JSON.stringify(target[k]) !== JSON.stringify(val)) {
          target[k] = val
          changed[`${kind}.${k}`] = val
        }
      }
      if (kind === 'oidc' && v.oidc) v.oidc.redirectUrl = callbackOf('oidc', slug, v.oidc.legacyCallback)
    }
    v.updated = iso()
    record('provider_changed', { actor, detail: { slug, kind: v.kind, ...changed } })
    return withDerived(rec)
  }

  // The accounts that could no longer sign in if this provider went.
  const strandedBy = (slug: string): number => {
    let n = 0
    for (const u of o.users()) {
      if (!u.identities.some((i) => i.providerSlug === slug)) continue
      if (u.passwordHash === undefined && u.passkeys.length === 0 && !u.identities.some((i) => i.providerSlug !== slug))
        n++
    }
    return n
  }

  const deleteProvider = (slug: string, force: boolean, actor: string) => {
    const rec = providers.get(slug)
    if (!rec) throw new HttpError(404, 'not_found', '没有这个提供方')
    const accounts = strandedBy(slug)
    if (accounts > 0 && !force) throw new HttpError(409, 'provider_in_use', '有账号只能用这个提供方登录', { accounts })
    providers.delete(slug)
    o.onProviderDeleted?.(slug)
    record('provider_deleted', { actor, detail: { slug, kind: rec.view.kind } })
  }

  const refreshProvider = (slug: string): AuthProvider => {
    const rec = providers.get(slug)
    if (!rec) throw new HttpError(404, 'not_found', '没有这个提供方')
    const probe = `${rec.view.oidc?.issuer ?? ''}${rec.view.saml?.metadataUrl ?? ''}${rec.view.github?.baseUrl ?? ''}`
    rec.view.status = probe.includes('unreachable')
      ? { ok: false, checked: iso(), message: '连不上这个地址' }
      : { ok: true, checked: iso(), message: '' }
    return withDerived(rec)
  }

  // --- the security page ---
  const requiredOf = (u: BookUser) => u.passkeyRequired || security.passkeys.requireRoles.includes(u.role)
  const passwordAdmins = () => {
    let n = 0
    for (const u of o.users()) if (u.role === 'admin' && !u.disabled && u.passwordHash !== undefined) n++
    return n
  }
  // What GET /api/admin/security says: only what is derived from the settings, and the captcha's keys.
  const view = (): SecurityBody => {
    let required = 0
    let without = 0
    for (const u of o.users()) {
      if (u.disabled || !requiredOf(u)) continue
      required++
      if (u.passkeys.length === 0) without++
    }
    return {
      login: {
        effectiveMode: effectiveMode(),
        ssoOnlyInEffect: ssoOnlyInEffect(),
        ssoAvailable: enabledProviders().length > 0,
      },
      passkeys: { available: o.passkeysAvailable, required, requiredWithout: without },
      captcha: {
        siteKey: security.captcha.siteKey,
        hasSecret: (security.captcha.secretFor as string) === security.captcha.provider,
      },
      passwordAdmins: passwordAdmins(),
      notices: [...(o.notices ?? [])],
    }
  }

  const requiredCount = (roles: Role[]) => {
    let n = 0
    for (const u of o.users()) if (!u.disabled && (u.passkeyRequired || roles.includes(u.role))) n++
    return n
  }
  // The server's checkPasskeys: a passkey setting that would leave someone locked out, or that cannot be served.
  const checkPasskeys = (enabled: boolean, roles: Role[], curEnabled: boolean) => {
    const required = requiredCount(roles)
    if ((roles.length > 0 && !o.passkeysAvailable) || (enabled && !curEnabled && required > 0 && !o.passkeysAvailable))
      bad('现在不能使用 passkey（需要合适的公开地址）', 'passkey_unavailable')
    if (!enabled && required > 0) bad('还有人被要求使用 passkey，请先取消要求', 'passkey_requirement_unsatisfiable')
  }
  const anyContextOn = (c: SecuritySettings['captcha']['contexts']) => CONTEXTS.some((x) => c[x].on)
  const needKeys = (siteKey: string, hasSecret: boolean) => {
    if (!siteKey) bad('第三方验证码需要站点密钥', 'captcha_site_key_required')
    if (!hasSecret) bad('第三方验证码需要密钥（换提供方后要给新的）', 'captcha_secret_required')
  }
  const needPasswordAdmin = () => {
    if (passwordAdmins() === 0) bad('没有带密码的启用管理员，不能开启「只许单点登录」', 'local_admin_required')
  }

  // The settings the sign-in policy keeps, by key. The kinds and ranges are checked by the settings' own list; what is
  // checked here is what looks at more than one value.
  const SETTINGS = [
    'features.passkeys',
    'signin.mode',
    'signin.ssoOnly',
    'signin.passkeyRoles',
    'signin.lockout.enabled',
    'signin.lockout.minutes',
    'signin.lockout.scope',
    'signin.perMinute',
    'signin.maxFailures',
    'signin.windowMinutes',
    'sessions.idleDays',
    'sessions.maxDays',
    'captcha.provider',
    'captcha.contexts',
  ]
  const settingGet = (key: string): unknown => {
    switch (key) {
      case 'features.passkeys':
        return security.passkeys.enabled
      case 'signin.mode':
        return security.login.mode
      case 'signin.ssoOnly':
        return security.login.ssoOnly
      case 'signin.passkeyRoles':
        return [...security.passkeys.requireRoles]
      case 'signin.lockout.enabled':
        return security.lockout.enabled
      case 'signin.lockout.minutes':
        return security.lockout.minutes
      case 'signin.lockout.scope':
        return security.lockout.scope
      case 'signin.perMinute':
        return security.limits.perMinute
      case 'signin.maxFailures':
        return security.limits.maxFailures
      case 'signin.windowMinutes':
        return security.limits.windowMinutes
      case 'sessions.idleDays':
        return security.limits.sessionIdleDays
      case 'sessions.maxDays':
        return security.limits.sessionMaxDays
      case 'captcha.provider':
        return security.captcha.provider
      case 'captcha.contexts':
        return structuredClone(security.captcha.contexts)
    }
    return undefined
  }
  // Checks and stores one value (already of its kind and within its range).
  const settingPut = (key: string, value: unknown, actor: string): void => {
    const before = settingGet(key)
    switch (key) {
      case 'features.passkeys':
        checkPasskeys(value === true, security.passkeys.requireRoles, security.passkeys.enabled)
        security.passkeys.enabled = value === true
        break
      case 'signin.mode':
        if (security.login.ssoOnly && security.login.mode === 'local_only' && value !== 'local_only')
          needPasswordAdmin()
        security.login.mode = value as LoginMode
        break
      case 'signin.ssoOnly':
        if (value === true && !security.login.ssoOnly) {
          if (o.registrationOpen?.())
            throw new HttpError(400, 'sso_only_with_registration', '有租户开着注册时不能只允许单点登录')
          needPasswordAdmin()
        }
        security.login.ssoOnly = value === true
        break
      case 'signin.passkeyRoles': {
        const list = value as string[]
        if (list.some((r) => r !== 'admin' && r !== 'member')) bad('signin.passkeyRoles 只有 admin 和 member')
        const roles = (['admin', 'member'] as Role[]).filter((r) => list.includes(r))
        checkPasskeys(security.passkeys.enabled, roles, security.passkeys.enabled)
        security.passkeys.requireRoles = roles
        break
      }
      case 'signin.lockout.enabled':
        security.lockout.enabled = value === true
        break
      case 'signin.lockout.minutes':
        security.lockout.minutes = value as number
        break
      case 'signin.lockout.scope':
        security.lockout.scope = value as SecuritySettings['lockout']['scope']
        break
      case 'signin.perMinute':
        security.limits.perMinute = value as number
        break
      case 'signin.maxFailures':
        security.limits.maxFailures = value as number
        break
      case 'signin.windowMinutes':
        security.limits.windowMinutes = value as number
        break
      case 'sessions.idleDays':
        if ((value as number) > security.limits.sessionMaxDays) bad('sessions.maxDays 不能小于 sessions.idleDays')
        security.limits.sessionIdleDays = value as number
        break
      case 'sessions.maxDays':
        if ((value as number) < security.limits.sessionIdleDays) bad('sessions.maxDays 不能小于 sessions.idleDays')
        security.limits.sessionMaxDays = value as number
        break
      case 'captcha.provider': {
        const next = value as CaptchaProvider
        if (next !== security.captcha.provider && next !== 'image') {
          if (!o.publicUrl) bad('第三方验证码需要公开地址（--public-url）', 'captcha_needs_public_url')
          // A secret belongs to the provider it was made for: a new provider has none.
          if (anyContextOn(security.captcha.contexts)) bad('第三方验证码需要站点密钥', 'captcha_site_key_required')
        }
        if (next !== security.captcha.provider) {
          security.captcha.siteKey = ''
          security.captcha.secretFor = ''
        }
        security.captcha.provider = next
        break
      }
      case 'captcha.contexts': {
        // A partial document is merged over the current one; the answer is the whole.
        if (!isRecord(value)) bad('captcha.contexts 要是对象')
        const doc = value as Record<string, unknown>
        const next = structuredClone(security.captcha.contexts)
        for (const [c, x] of Object.entries(doc)) {
          if (!(CONTEXTS as string[]).includes(c)) bad(`captcha.contexts.${c} 不认识`)
          if (!isRecord(x)) bad(`captcha.contexts.${c} 要是对象`)
          const one = x as Record<string, unknown>
          for (const k of Object.keys(one)) if (!['on', 'when', 'failures'].includes(k)) bad(`${c}.${k} 不认识`)
          const cur = next[c as CaptchaContext]
          if (one.on !== undefined) cur.on = bool(one.on, `${c}.on`)
          if (one.when !== undefined) cur.when = oneOf(one.when, ['always', 'after_failures'] as const, `${c}.when`)
          if (one.failures !== undefined) cur.failures = int(one.failures, 1, 100, `${c}.failures`)
        }
        if (security.captcha.provider !== 'image') {
          if (!o.publicUrl) bad('第三方验证码需要公开地址（--public-url）', 'captcha_needs_public_url')
          if (anyContextOn(next))
            needKeys(security.captcha.siteKey, security.captcha.secretFor === security.captcha.provider)
        }
        security.captcha.contexts = next
        break
      }
      default:
        throw new HttpError(404, 'not_found', 'no such setting')
    }
    if (JSON.stringify(before) !== JSON.stringify(settingGet(key)))
      record('setting_changed', {
        actor,
        detail: { key, layer: 'site', from: before, to: settingGet(key) },
      })
  }

  // PUT /api/admin/security: the captcha's credentials, the one part of the policy that is not a setting.
  const putCredentials = (body: Record<string, unknown>, actor: string): SecurityBody => {
    for (const k of Object.keys(body)) if (k !== 'captcha') bad(`不认识 ${k}`)
    if (!isRecord(body.captcha)) return bad('captcha 要是对象')
    const b = body.captcha as Record<string, unknown>
    for (const k of Object.keys(b)) if (k !== 'siteKey' && k !== 'secret') bad(`captcha.${k} 不认识`)
    if (b.siteKey === undefined && b.secret === undefined) return view()
    if (security.captcha.provider === 'image') bad('内置的图片验证码没有密钥，请先选一个提供方')
    let siteKey = security.captcha.siteKey
    if (b.siteKey !== undefined) {
      if (typeof b.siteKey !== 'string' || b.siteKey.trim().length > 512) bad('captcha.siteKey 最多 512 个字符')
      siteKey = (b.siteKey as string).trim()
    }
    let hasSecret = security.captcha.secretFor === security.captcha.provider
    if (b.secret !== undefined) {
      if (typeof b.secret !== 'string' || b.secret.length > 2048) bad('captcha.secret 最多 2048 字节')
      hasSecret = (b.secret as string).trim() !== ''
    }
    if (anyContextOn(security.captcha.contexts)) needKeys(siteKey, hasSecret)
    if (siteKey !== security.captcha.siteKey)
      record('setting_changed', {
        actor,
        detail: { key: 'captcha.siteKey', layer: 'site', from: security.captcha.siteKey, to: siteKey },
      })
    security.captcha.siteKey = siteKey
    if (b.secret !== undefined) {
      security.captcha.secretFor = hasSecret ? security.captcha.provider : ''
      record('setting_changed', { actor, detail: { key: 'captcha.secret', layer: 'site', to: 'changed' } })
    }
    return view()
  }

  // --- the captcha ---
  const challenges = new Map<string, { answer: string; at: number }>()
  const CHALLENGE_MS = 300_000
  const ANSWER = '4821'
  const issue = (): { id: string; image: string } => {
    const id = `c${challenges.size + 1}-${o.now().toString(36)}`
    challenges.set(id, { answer: ANSWER, at: o.now() })
    // The mock's image says its answer: nothing here is secret.
    const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="160" height="48"><rect width="160" height="48" fill="#fff"/><text x="12" y="34" font-size="28" font-family="monospace" fill="#333">${ANSWER}</text></svg>`
    return { id, image: `data:image/svg+xml;base64,${btoa(svg)}` }
  }
  // An image challenge is answered once, right or wrong.
  const takeChallenge = (id: unknown, answer: unknown): boolean => {
    if (typeof id !== 'string') return false
    const c = challenges.get(id)
    challenges.delete(id)
    return (
      !!c && o.now() - c.at <= CHALLENGE_MS && typeof answer === 'string' && answer.trim().toLowerCase() === c.answer
    )
  }

  return {
    security,
    providers,
    audit,
    record,
    auditList,
    auditKinds,
    auditCsv,
    view,
    putCredentials,
    settingGet,
    settingPut,
    SETTINGS,
    // The secret of the captcha, as the secrets list tells it.
    captchaSecretSet: () => (security.captcha.secretFor as string) === security.captcha.provider,
    setCaptchaSecret: (v: string | null) => {
      security.captcha.secretFor = v === null ? '' : security.captcha.provider
    },
    publicProviders,
    effectiveMode,
    ssoOnlyInEffect,
    passkeyOn,
    requiredOf,
    enabledProviders,
    addLegacy,
    createProvider,
    patchProvider,
    deleteProvider,
    refreshProvider,
    withDerived,
    // How many entries of the providers map to a group, and which; and taking them out.
    mappingsOf: (groupId: string) =>
      [...providers.values()].flatMap((p) =>
        p.view.groups.map.filter((e) => e.groupId === groupId).map((e) => ({ provider: p.view.slug, value: e.value })),
      ),
    unmap: (groupId: string) => {
      for (const p of providers.values()) p.view.groups.map = p.view.groups.map.filter((e) => e.groupId !== groupId)
    },
    issue,
    takeChallenge,
    passwordAdmins,
  }
}
export type SignInBook = ReturnType<typeof createSignInBook>
