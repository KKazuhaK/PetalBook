// The mock's side of milestone 2.8 (docs/api.md, "Signing in" and "Administration"): the security settings,
// the sign-in providers, the captcha and the audit log. The flows that need a session (signing in, the
// passkeys) are in server.ts; this holds the state and the checks of what an administrator may set.

import type { AuditItem, AuthProvider, CaptchaContext, CaptchaContextSettings, CaptchaProvider, LoginMode, ProviderInput, ProviderKind, PublicProvider, Role, SecurityNotice, SecurityPatch, SecuritySettings } from '../types.ts'
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
  captcha?: Partial<{ provider: CaptchaProvider; siteKey: string; hasSecret: boolean; contexts: Partial<Record<CaptchaContext, Partial<CaptchaContextSettings>>> }>
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
  security?: MockSecurity
  providers?: (ProviderInput & { slug: string; kind: ProviderKind; name: string; sub?: string; status?: AuthProvider['status'] })[]
  notices?: SecurityNotice[]
}

const MODES: LoginMode[] = ['dual', 'sso_first', 'sso_redirect', 'local_only']
const SCOPES = ['ip_account', 'ip', 'account']
const CAPTCHAS: CaptchaProvider[] = ['image', 'turnstile', 'recaptcha', 'hcaptcha']
const CONTEXTS: CaptchaContext[] = ['login', 'invite', 'setup']
export const SLUG = /^[a-z0-9][a-z0-9-]{0,31}$/
export const MAX_PROVIDERS = 50

function bad(message: string, code = 'invalid'): never {
  throw new HttpError(400, code, message)
}
const int = (v: unknown, lo: number, hi: number, field: string): number => {
  if (typeof v !== 'number' || !Number.isInteger(v) || v < lo || v > hi) return bad(`${field} 要是 ${lo} 到 ${hi} 的整数`)
  return v
}
const bool = (v: unknown, field: string): boolean => (typeof v === 'boolean' ? v : bad(`${field} 要是布尔值`))
const oneOf = <T extends string>(v: unknown, list: readonly T[], field: string): T => (typeof v === 'string' && (list as readonly string[]).includes(v) ? (v as T) : bad(`${field} 不对`))

export function createSignInBook(o: SignInBookOptions) {
  const iso = () => new Date(o.now()).toISOString().replace(/\.\d{3}Z$/, 'Z')
  const base = (o.publicUrl ?? '').replace(/\/+$/, '')

  // --- audit ---
  const audit: AuditItem[] = []
  const record = (kind: string, row: { actor?: string | null; user?: string | null; username?: string | null; ip?: string; detail?: Record<string, unknown> } = {}) => {
    audit.push({ id: o.newAuditId(), at: iso(), kind, actor: row.actor ?? null, user: row.user ?? null, username: row.username ?? null, ip: row.ip ?? '203.0.113.7', detail: row.detail ?? {} })
  }
  const auditList = (kind: string | null, user: string | null, cursor: string | null, limit: number): { items: AuditItem[]; next: string } => {
    const all = audit.filter((a) => (!kind || a.kind === kind) && (!user || a.user === user || a.actor === user)).reverse()
    const start = cursor ? all.findIndex((a) => a.id === cursor) : 0
    if (cursor && start < 0) throw new HttpError(400, 'invalid', 'cursor 不对')
    const items = all.slice(start, start + limit)
    return { items, next: all[start + limit]?.id ?? '' }
  }

  // --- security settings ---
  const security = {
    login: { mode: 'dual' as LoginMode, ssoOnly: false },
    passkeys: { enabled: true, requireRoles: [] as Role[] },
    captcha: {
      provider: 'image' as CaptchaProvider,
      siteKey: '',
      secretFor: '' as CaptchaProvider | '',
      contexts: { login: { on: false, when: 'after_failures', failures: 3 }, invite: { on: false, when: 'after_failures', failures: 3 }, setup: { on: false, when: 'after_failures', failures: 3 } } as SecuritySettings['captcha']['contexts'],
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
  const callbackOf = (kind: ProviderKind, slug: string, legacy = false) => `${base}/api/auth/${kind}/${legacy ? '' : `${slug}/`}callback`.replace(/\/\/callback$/, '/callback')

  const enabledProviders = () => [...providers.values()].filter((p) => p.view.enabled).sort((a, b) => a.view.order - b.view.order || a.view.name.localeCompare(b.view.name))
  const publicProviders = (): PublicProvider[] => enabledProviders().map((p) => ({ slug: p.view.slug, kind: p.view.kind, name: p.view.name, icon: p.view.icon }))
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
    return { ...v, people: people(v.slug), ...(v.oidc ? { oidc: { ...v.oidc, hasSecret: rec.secret } } : {}), ...(v.github ? { github: { ...v.github, hasSecret: rec.secret } } : {}) }
  }

  const fresh = (input: ProviderInput & { slug: string; kind: ProviderKind; name: string }, sub?: string, legacy = false): ProviderRec => {
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
      view.github = { baseUrl: input.github?.baseUrl ?? 'https://github.com', clientId: input.github?.clientId ?? '', hasSecret: false, organizations: input.github?.organizations ?? [], redirectUrl: callbackOf('github', input.slug) }
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
    const rec = fresh({ slug: 'sso', kind: 'oidc', name, autoCreate, oidc: { issuer: 'https://sso.example', clientId: 'petalbook' } }, sub, true)
    rec.secret = true
    providers.set('sso', rec)
  }

  const issuerOf = (v: AuthProvider) => v.oidc?.issuer ?? v.saml?.idpEntityId ?? v.github?.baseUrl ?? ''
  const checkProvider = (input: ProviderInput, kind: ProviderKind, slug: string, existing?: AuthProvider) => {
    if (!o.publicUrl) bad('服务器没有公开地址（--public-url）', 'public_url_required')
    if (input.name !== undefined && (typeof input.name !== 'string' || input.name.trim() === '' || Array.from(input.name).length > 60)) bad('名称需要 1 到 60 个字符')
    if (input.icon !== undefined && input.icon !== '' && !/^preset:[a-z]+$/.test(input.icon)) bad('icon 不对')
    if (input.order !== undefined) int(input.order, 0, 10_000, 'order')
    for (const f of ['enabled', 'autoCreate', 'mergeVerifiedEmail'] as const) if (input[f] !== undefined) bool(input[f], f)
    const merged = { ...(existing ?? {}), ...input } as ProviderInput
    if (kind === 'oidc') {
      const issuer = input.oidc?.issuer ?? existing?.oidc?.issuer ?? ''
      if (!issuer) bad('缺少 issuer')
      if (!/^https:\/\//.test(issuer)) bad('issuer 要是 https 地址', 'oidc_issuer_invalid')
      if (!(input.oidc?.clientId ?? existing?.oidc?.clientId)) bad('缺少 clientId')
      if (input.oidc?.legacyCallback) {
        for (const p of providers.values()) if (p.view.slug !== slug && p.view.oidc?.legacyCallback) throw new HttpError(409, 'legacy_callback_in_use', '另一个提供方已经在用旧的回调地址')
      }
    } else if (kind === 'github') {
      if (!(input.github?.clientId ?? existing?.github?.clientId)) bad('缺少 clientId')
      const orgs = input.github?.organizations ?? existing?.github?.organizations ?? []
      if ((merged.autoCreate ?? existing?.autoCreate) && orgs.length === 0) bad('GitHub 自动建号必须指定组织', 'github_auto_create_needs_org')
    } else {
      if (!/^https:\/\//.test(o.publicUrl ?? '')) bad('SAML 需要 https 的公开地址', 'saml_needs_https')
      const url = input.saml?.metadataUrl ?? existing?.saml?.metadataUrl ?? ''
      const xml = input.saml?.metadataXml ?? existing?.saml?.metadataXml ?? ''
      if (!url && !xml) bad('缺少元数据（地址或 XML）')
      if (url && !/^https:\/\//.test(url)) bad('元数据地址要是 https')
      if ((xml && !xml.includes('EntityDescriptor')) || url.includes('bad-metadata')) bad('元数据解析不了', 'saml_metadata_invalid')
    }
    // The identity provider's own address may not be shared.
    const issuer = kind === 'oidc' ? (input.oidc?.issuer ?? existing?.oidc?.issuer) : kind === 'github' ? (input.github?.baseUrl ?? existing?.github?.baseUrl ?? 'https://github.com') : undefined
    if (issuer) for (const p of providers.values()) if (p.view.slug !== slug && p.view.kind === kind && issuerOf(p.view) === issuer) throw new HttpError(409, 'provider_issuer_in_use', '另一个提供方已经用了这个签发方')
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
    const rec = fresh({ ...input, slug, kind, name: input.name!.trim() })
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
    const v = rec.view
    const changed: Record<string, unknown> = {}
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
      if (u.passwordHash === undefined && u.passkeys.length === 0 && !u.identities.some((i) => i.providerSlug !== slug)) n++
    }
    return n
  }

  const deleteProvider = (slug: string, force: boolean, actor: string) => {
    const rec = providers.get(slug)
    if (!rec) throw new HttpError(404, 'not_found', '没有这个提供方')
    const accounts = strandedBy(slug)
    if (accounts > 0 && !force) throw new HttpError(409, 'provider_in_use', '有账号只能用这个提供方登录', { accounts })
    providers.delete(slug)
    record('provider_deleted', { actor, detail: { slug, kind: rec.view.kind } })
  }

  const refreshProvider = (slug: string): AuthProvider => {
    const rec = providers.get(slug)
    if (!rec) throw new HttpError(404, 'not_found', '没有这个提供方')
    const probe = `${rec.view.oidc?.issuer ?? ''}${rec.view.saml?.metadataUrl ?? ''}${rec.view.github?.baseUrl ?? ''}`
    rec.view.status = probe.includes('unreachable') ? { ok: false, checked: iso(), message: '连不上这个地址' } : { ok: true, checked: iso(), message: '' }
    return withDerived(rec)
  }

  // --- the security page ---
  const requiredOf = (u: BookUser) => u.passkeyRequired || security.passkeys.requireRoles.includes(u.role)
  const passwordAdmins = () => {
    let n = 0
    for (const u of o.users()) if (u.role === 'admin' && !u.disabled && u.passwordHash !== undefined) n++
    return n
  }
  const view = (): SecuritySettings => {
    let required = 0
    let without = 0
    for (const u of o.users()) {
      if (u.disabled || !requiredOf(u)) continue
      required++
      if (u.passkeys.length === 0) without++
    }
    return {
      login: { mode: security.login.mode, effectiveMode: effectiveMode(), ssoOnly: security.login.ssoOnly, ssoOnlyInEffect: ssoOnlyInEffect(), ssoAvailable: enabledProviders().length > 0 },
      passkeys: { enabled: security.passkeys.enabled, available: o.passkeysAvailable, requireRoles: [...security.passkeys.requireRoles], required, requiredWithout: without },
      captcha: {
        provider: security.captcha.provider,
        siteKey: security.captcha.siteKey,
        hasSecret: (security.captcha.secretFor as string) === security.captcha.provider,
        contexts: { login: { ...security.captcha.contexts.login }, invite: { ...security.captcha.contexts.invite }, setup: { ...security.captcha.contexts.setup } },
      },
      lockout: { ...security.lockout },
      limits: { ...security.limits },
      passwordAdmins: passwordAdmins(),
      notices: [...(o.notices ?? [])],
    }
  }

  const putSecurity = (body: Record<string, unknown>, actor: string): SecuritySettings => {
    const patch = body as SecurityPatch
    const known = ['login', 'passkeys', 'captcha', 'lockout', 'limits']
    for (const k of Object.keys(body)) if (!known.includes(k)) bad(`不认识 ${k}`)
    // Everything is checked on a copy first, so a refusal changes nothing.
    const next = structuredClone(security)
    const from: Record<string, unknown> = {}
    const to: Record<string, unknown> = {}
    const set = <T,>(block: string, field: string, current: T, value: T) => {
      if (JSON.stringify(current) !== JSON.stringify(value)) {
        from[`${block}.${field}`] = current
        to[`${block}.${field}`] = value
      }
      return value
    }
    const only = (block: unknown, name: string, fields: string[]): Record<string, unknown> => {
      if (!isRecord(block)) return bad(`${name} 要是对象`)
      for (const k of Object.keys(block)) if (!fields.includes(k)) bad(`${name}.${k} 不认识`)
      return block
    }
    if (patch.login !== undefined) {
      const b = only(patch.login, 'login', ['mode', 'ssoOnly'])
      if (b.mode !== undefined) next.login.mode = set('login', 'mode', security.login.mode, oneOf(b.mode, MODES, 'login.mode'))
      if (b.ssoOnly !== undefined) next.login.ssoOnly = set('login', 'ssoOnly', security.login.ssoOnly, bool(b.ssoOnly, 'login.ssoOnly'))
    }
    if (patch.passkeys !== undefined) {
      const b = only(patch.passkeys, 'passkeys', ['enabled', 'requireRoles'])
      if (b.enabled !== undefined) next.passkeys.enabled = set('passkeys', 'enabled', security.passkeys.enabled, bool(b.enabled, 'passkeys.enabled'))
      if (b.requireRoles !== undefined) {
        // Only the sets the contract allows: none, the administrators, or everyone.
        const r = b.requireRoles
        const text = Array.isArray(r) ? JSON.stringify([...r].sort()) : ''
        if (!['[]', '["admin"]', '["admin","member"]'].includes(text)) bad('passkeys.requireRoles 不对')
        const roles = (r as Role[]).slice().sort((x) => (x === 'admin' ? -1 : 1))
        next.passkeys.requireRoles = set('passkeys', 'requireRoles', security.passkeys.requireRoles, roles)
      }
    }
    if (patch.lockout !== undefined) {
      const b = only(patch.lockout, 'lockout', ['enabled', 'minutes', 'scope'])
      if (b.enabled !== undefined) next.lockout.enabled = set('lockout', 'enabled', security.lockout.enabled, bool(b.enabled, 'lockout.enabled'))
      if (b.minutes !== undefined) next.lockout.minutes = set('lockout', 'minutes', security.lockout.minutes, int(b.minutes, 1, 10_080, 'lockout.minutes'))
      if (b.scope !== undefined) next.lockout.scope = set('lockout', 'scope', security.lockout.scope, oneOf(b.scope, SCOPES as SecuritySettings['lockout']['scope'][], 'lockout.scope'))
    }
    if (patch.limits !== undefined) {
      const b = only(patch.limits, 'limits', ['perMinute', 'maxFailures', 'windowMinutes', 'sessionIdleDays', 'sessionMaxDays'])
      const ranges: Record<string, [number, number]> = { perMinute: [1, 10_000], maxFailures: [1, 1000], windowMinutes: [1, 1440], sessionIdleDays: [1, 365], sessionMaxDays: [1, 3650] }
      for (const [k, [lo, hi]] of Object.entries(ranges)) {
        if (b[k] !== undefined) (next.limits as Record<string, number>)[k] = set('limits', k, (security.limits as Record<string, number>)[k], int(b[k], lo, hi, `limits.${k}`))
      }
      if (next.limits.sessionMaxDays < next.limits.sessionIdleDays) bad('limits.sessionMaxDays 不能小于 sessionIdleDays')
    }
    let secretGiven: string | undefined
    if (patch.captcha !== undefined) {
      const b = only(patch.captcha, 'captcha', ['provider', 'siteKey', 'secret', 'contexts'])
      if (b.provider !== undefined) next.captcha.provider = set('captcha', 'provider', security.captcha.provider, oneOf(b.provider, CAPTCHAS, 'captcha.provider'))
      if (b.siteKey !== undefined) {
        if (typeof b.siteKey !== 'string' || b.siteKey.length > 200) bad('captcha.siteKey 不对')
        next.captcha.siteKey = set('captcha', 'siteKey', security.captcha.siteKey, b.siteKey)
      }
      if (b.secret !== undefined) {
        if (typeof b.secret !== 'string' || b.secret.length > 500) bad('captcha.secret 不对')
        secretGiven = b.secret
        next.captcha.secretFor = b.secret === '' ? '' : next.captcha.provider
        from['captcha.secret'] = '…'
        to['captcha.secret'] = 'changed'
      }
      if (b.contexts !== undefined) {
        const cs = only(b.contexts, 'captcha.contexts', CONTEXTS)
        for (const c of CONTEXTS) {
          if (cs[c] === undefined) continue
          const x = only(cs[c], `captcha.contexts.${c}`, ['on', 'when', 'failures'])
          const cur = security.captcha.contexts[c]
          if (x.on !== undefined) next.captcha.contexts[c].on = set(`captcha.${c}`, 'on', cur.on, bool(x.on, `${c}.on`))
          if (x.when !== undefined) next.captcha.contexts[c].when = set(`captcha.${c}`, 'when', cur.when, oneOf(x.when, ['always', 'after_failures'] as const, `${c}.when`))
          if (x.failures !== undefined) next.captcha.contexts[c].failures = set(`captcha.${c}`, 'failures', cur.failures, int(x.failures, 1, 100, `${c}.failures`))
        }
      }
    }
    // What the stored secret is for changes with the provider: one for another provider does not count.
    if (secretGiven === undefined && next.captcha.provider !== security.captcha.provider && next.captcha.secretFor !== next.captcha.provider) next.captcha.secretFor = ''
    if (next.login.ssoOnly && !security.login.ssoOnly && passwordAdmins() === 0) bad('没有带密码的启用管理员，不能开启「只许单点登录」', 'local_admin_required')
    const third = next.captcha.provider !== 'image'
    if (third && CONTEXTS.some((c) => next.captcha.contexts[c].on)) {
      if (!next.captcha.siteKey) bad('第三方验证码需要站点密钥', 'captcha_site_key_required')
      if (next.captcha.secretFor !== next.captcha.provider) bad('第三方验证码需要密钥（换提供方要在同一次请求里给新的）', 'captcha_secret_required')
      if (!o.publicUrl) bad('第三方验证码需要公开地址（--public-url）', 'captcha_needs_public_url')
    }
    let required = 0
    for (const u of o.users()) if (!u.disabled && (u.passkeyRequired || next.passkeys.requireRoles.includes(u.role))) required++
    if (patch.passkeys !== undefined && !o.passkeysAvailable && (next.passkeys.requireRoles.length > 0 || (next.passkeys.enabled && required > 0))) {
      bad('现在不能使用 passkey（需要合适的公开地址）', 'passkey_unavailable')
    }
    if (!next.passkeys.enabled && required > 0) bad('还有人被要求使用 passkey，请先取消要求', 'passkey_requirement_unsatisfiable')
    Object.assign(security, next)
    record('security_changed', { actor, detail: { from, to } })
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
    return !!c && o.now() - c.at <= CHALLENGE_MS && typeof answer === 'string' && answer.trim().toLowerCase() === c.answer
  }

  return {
    security,
    providers,
    audit,
    record,
    auditList,
    view,
    putSecurity,
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
    issue,
    takeChallenge,
    passwordAdmins,
  }
}
export type SignInBook = ReturnType<typeof createSignInBook>
