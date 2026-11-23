// The mock's side of milestone 3.3 (docs/api.md, "Tenants, registration and mail"): the switch, the tenants, and the
// layered settings with their locks. It does not keep people or spaces apart by tenant (the mock has one world);
// what it keeps true is what the web app reads: the contract of these endpoints.

import type { SettingLower, SiteSetting, Tenant, TenantSetting } from '../types.ts'
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
  layers: ('site' | 'tenant' | 'space')[]
  def: unknown
  lower: SettingLower
  min?: number
  max?: number
}
// The settings of 3.3, as the server has them (quota.* and 3.1's come later).
const DEFS: Def[] = [
  { key: 'registration.open', kind: 'boolean', layers: ['site', 'tenant'], def: false, lower: { mode: 'lock' } },
  { key: 'registration.domains', kind: 'list', layers: ['site', 'tenant'], def: [], lower: { mode: 'allow' } },
  { key: 'registration.perHour', kind: 'limit', layers: ['site', 'tenant'], def: 50, lower: { mode: 'restrict', max: 50 }, min: 1, max: 10000 },
  { key: 'legal.termsUrl', kind: 'text', layers: ['site', 'tenant'], def: '', lower: { mode: 'allow' } },
  { key: 'legal.privacyUrl', kind: 'text', layers: ['site', 'tenant'], def: '', lower: { mode: 'allow' } },
  { key: 'daily.own', kind: 'boolean', layers: ['site', 'tenant', 'space'], def: true, lower: { mode: 'allow' } },
  { key: 'daily.api', kind: 'boolean', layers: ['site', 'tenant', 'space'], def: true, lower: { mode: 'allow' } },
  { key: 'daily.default', kind: 'doc', layers: ['site', 'tenant'], def: null, lower: { mode: 'allow' } },
]

function checkValue(d: Def, v: unknown): unknown {
  switch (d.kind) {
    case 'boolean':
      return typeof v === 'boolean' ? v : bad(`${d.key} is true or false`)
    case 'limit':
      return typeof v === 'number' && Number.isInteger(v) && v >= (d.min ?? 0) && v <= (d.max ?? 1e9) ? v : bad(`${d.key} is a number from ${d.min} to ${d.max}`)
    case 'list': {
      if (!Array.isArray(v) || v.some((x) => typeof x !== 'string')) return bad(`${d.key} is a list of text`)
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
  if (!o || typeof o !== 'object' || !['allow', 'restrict', 'lock'].includes(o.mode)) return bad('lower.mode is allow, restrict or lock')
  if (o.mode !== 'restrict') return { mode: o.mode }
  if (d.kind === 'boolean') return o.options?.length === 1 && o.options[0] === false ? { mode: 'restrict', options: [false] } : bad('a boolean setting can only be restricted to [false]')
  if (d.kind === 'limit') return typeof o.max === 'number' && o.max >= (d.min ?? 0) && o.max <= (d.max ?? 1e9) ? { mode: 'restrict', max: o.max } : bad(`${d.key}: max is out of range`)
  return bad(`${d.key} cannot be restricted, only allowed or locked`)
}
const allows = (l: SettingLower, d: Def, v: unknown): boolean => (l.mode === 'lock' ? false : l.mode === 'allow' ? true : d.kind === 'boolean' ? v === false : d.kind === 'limit' ? typeof v === 'number' && l.max !== undefined && v <= l.max : false)
const tightens = (l: SettingLower, d: Def, above: SettingLower): boolean => {
  if (above.mode === 'allow') return true
  if (above.mode === 'lock') return l.mode === 'lock'
  if (l.mode === 'lock') return true
  if (l.mode !== 'restrict') return false
  return d.kind === 'limit' ? (l.max ?? Infinity) <= (above.max ?? Infinity) : true
}

export interface TenantBookOptions {
  now: () => string
  // The switches the daily text keeps in its own record (the site's value of daily.own and daily.api).
  daily: { get: () => { own: boolean; api: boolean }; set: (patch: { own?: boolean; api?: boolean }) => void }
  isSiteAdmin: (id: string) => boolean
  // Mail is enabled (registration needs it), ssoOnly is stored on, and a change of registration.open happened.
  mailReady: () => boolean
  ssoOnlyStored: () => boolean
  onRegistrationChanged: () => void
}

interface TenantRow {
  value?: unknown // undefined: follows the site
  lower?: SettingLower
}

export function createTenantBook(o: TenantBookOptions) {
  let enabled = false
  const tenants = new Map<string, Tenant>()
  const stamp = o.now()
  tenants.set(DEFAULT_TENANT, { id: DEFAULT_TENANT, slug: 'default', name: 'Default', default: true, disabled: false, registration: { open: false }, quota: { people: null, spaces: null, bytes: null, pages: null }, created: stamp, updated: stamp })
  const siteRows = new Map<string, { value?: unknown; lower?: SettingLower }>()
  const tenantRows = new Map<string, Map<string, TenantRow>>()
  let seq = 1

  const siteValue = (d: Def): unknown => (d.key === 'daily.own' ? o.daily.get().own : d.key === 'daily.api' ? o.daily.get().api : siteRows.has(d.key) && siteRows.get(d.key)!.value !== undefined ? siteRows.get(d.key)!.value : d.def)
  const siteLower = (d: Def): SettingLower => siteRows.get(d.key)?.lower ?? d.lower
  const siteView = (d: Def): SiteSetting => ({ key: d.key, kind: d.kind, layers: d.layers, site: { value: siteValue(d), lower: siteLower(d) } })
  const tenantView = (tenant: string, d: Def): TenantSetting => {
    const row = tenantRows.get(tenant)?.get(d.key)
    const sl = siteLower(d)
    const own = row?.value
    const inEffect = own === undefined || own === null || allows(sl, d, own)
    return {
      key: d.key,
      kind: d.kind,
      layers: d.layers,
      site: { value: siteValue(d), lower: sl },
      tenant: { value: own ?? null, lower: row?.lower ?? null, inEffect },
      effective: own !== undefined && own !== null && inEffect ? own : siteValue(d),
    }
  }
  const effectiveOf = (tenant: string, d: Def) => tenantView(tenant, d)

  const tenantOut = (t: Tenant): Tenant => ({ ...t, registration: { open: o.mailReady() && effectiveOf(t.id, DEFS[0]).effective === true } })
  const checkOpening = () => {
    if (!o.mailReady()) throw new HttpError(409, 'mail_not_configured', 'registration verifies addresses by mail, and mail is not enabled')
    if (o.ssoOnlyStored()) throw new HttpError(409, 'sso_only_with_registration', 'password sign-in is refused (ssoOnly), so nobody could finish registering')
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
  const putSite = (key: string, body: Record<string, unknown>): SiteSetting & { affected?: number } => {
    const d = defOf(key, 'site')
    for (const k of Object.keys(body)) if (k !== 'value' && k !== 'lower') bad(`unknown field ${k}`)
    if (!('value' in body) && !('lower' in body)) bad('give value, lower or both')
    const row = siteRows.get(key) ?? {}
    if ('value' in body) {
      const v = d.kind === 'doc' ? body.value : checkValue(d, body.value)
      if (key === 'registration.open' && v === true) checkOpening()
      if (key === 'daily.own') o.daily.set({ own: v as boolean })
      else if (key === 'daily.api') o.daily.set({ api: v as boolean })
      else row.value = v
    }
    let affected: number | undefined
    if ('lower' in body) {
      row.lower = body.lower === null ? undefined : checkLower(d, body.lower)
      const l = row.lower ?? d.lower
      if (key === 'registration.open' && l.mode !== 'lock' && [...tenantRows.values()].some((r) => r.get(key)?.value === true)) checkOpening()
      affected = 0
      for (const rows of tenantRows.values()) {
        const t = rows.get(key)
        if (t && ((t.value !== undefined && t.value !== null && !allows(l, d, t.value)) || (t.lower && !tightens(t.lower, d, l)))) affected++
      }
    }
    siteRows.set(key, row)
    if (key === 'registration.open') o.onRegistrationChanged()
    return { ...siteView(d), ...(affected === undefined ? {} : { affected }) }
  }
  const putTenant = (tenant: string, key: string, body: Record<string, unknown>): TenantSetting => {
    const d = defOf(key, 'tenant')
    for (const k of Object.keys(body)) if (k !== 'value' && k !== 'lower') bad(`unknown field ${k}`)
    if (!('value' in body) && !('lower' in body)) bad('give value, lower or both')
    const sl = siteLower(d)
    const rows = tenantRows.get(tenant) ?? new Map<string, TenantRow>()
    const row = rows.get(key) ?? {}
    if ('value' in body && body.value !== null) {
      if (sl.mode === 'lock') throw new HttpError(409, 'setting_locked', `${key} is locked by the site's administrators`)
      const v = d.kind === 'doc' ? body.value : checkValue(d, body.value)
      if (!allows(sl, d, v)) throw new HttpError(409, 'setting_restricted', `${key}: outside the site's rule`)
      if (key === 'registration.open' && v === true) checkOpening()
      row.value = v
    } else if ('value' in body) row.value = undefined
    if ('lower' in body) {
      if (body.lower === null) row.lower = undefined
      else {
        const l = checkLower(d, body.lower)
        if (sl.mode === 'lock' && l.mode !== 'lock') throw new HttpError(409, 'setting_locked', `${key} is locked by the site's administrators`)
        if (!tightens(l, d, sl)) throw new HttpError(409, 'setting_restricted', `${key}: the tenant's rule must be at least as strict as the site's`)
        row.lower = l
      }
    }
    rows.set(key, row)
    tenantRows.set(tenant, rows)
    if (key === 'registration.open') o.onRegistrationChanged()
    return effectiveOf(tenant, d)
  }

  const page = (all: Tenant[], cursor: string, limit: number) => {
    const from = cursor ? all.findIndex((x) => x.id === cursor) + 1 : 0
    const items = all.slice(from, from + limit)
    return { items: items.map(tenantOut), next: from + limit < all.length ? items[items.length - 1].id : '' }
  }

  // The settings of one person's layers (GET /api/settings).
  const mine = (tenant: string) =>
    DEFS.filter((d) => d.layers.includes('space')).map((d) => {
      const e = effectiveOf(tenant, d)
      const on = e.effective === true
      const lockedBy = siteLower(d).mode === 'lock' ? 'site' : e.tenant?.lower?.mode === 'lock' ? 'tenant' : undefined
      const source = lockedBy ?? (!on ? (e.tenant && e.tenant.value !== null && e.tenant.inEffect ? 'tenant' : 'site') : undefined)
      return { key: d.key, effective: e.effective, space: { canChange: on && !lockedBy, inEffect: on, ...(source ? { source } : {}) } }
    })

  return {
    mine,
    registrationStored: anyOpen,
    // Whether daily.own / daily.api are in effect for a person's tenant (the mock has one world: the default).
    effective: (key: string): unknown => effectiveOf(DEFAULT_TENANT, DEFS.find((d) => d.key === key)!).effective,
    // Returns undefined for a path that is not this book's.
    route(me: string, method: string, path: string, url: URL, body: Record<string, unknown>): MockResponse | undefined {
      const j = (b: unknown, status = 200): MockResponse => ({ status, body: b })
      const site = o.isSiteAdmin(me)
      const needSite = () => {
        if (!site) throw new HttpError(403, 'forbidden', 'only an administrator of the whole server may do that')
      }
      if (path === '/admin/tenancy') {
        needSite()
        if (method === 'GET') return j({ enabled, tenants: tenants.size })
        if (method === 'PUT') {
          if (typeof body.enabled !== 'boolean') bad('give enabled: true or false')
          enabled = body.enabled as boolean
          return j({ enabled, tenants: tenants.size })
        }
      }
      if (path === '/admin/tenants') {
        needSite()
        if (method === 'GET') {
          const q = (url.searchParams.get('q') ?? '').toLowerCase()
          const limit = Number(url.searchParams.get('limit') ?? 50)
          if (!(limit >= 1 && limit <= 200)) bad('limit is a number from 1 to 200')
          const all = [...tenants.values()].filter((t) => !q || t.name.toLowerCase().includes(q) || t.slug.includes(q)).sort((a, b) => (a.default ? -1 : b.default ? 1 : a.name.toLowerCase() < b.name.toLowerCase() ? -1 : 1))
          return j(page(all, url.searchParams.get('cursor') ?? '', limit))
        }
        if (method === 'POST') {
          if (!enabled) throw new HttpError(409, 'tenants_off', 'more than the default tenant is switched off')
          const name = typeof body.name === 'string' ? body.name.trim() : ''
          if (!name || name.length > 80 || typeof body.slug !== 'string' || !SLUG.test(body.slug)) bad('slug is 2 to 32 characters of a-z, 0-9 and -; name is 1 to 80 characters')
          if ([...tenants.values()].some((t) => t.slug === body.slug)) throw new HttpError(409, 'exists', 'that slug is taken')
          if (tenants.size >= 1000) throw new HttpError(409, 'tenant_limit', 'a server has at most 1000 tenants')
          const id = `T${String(seq++).padStart(9, '0')}`
          const stampNow = o.now()
          const t: Tenant = { id, slug: body.slug as string, name, default: false, disabled: false, registration: { open: false }, quota: { people: null, spaces: null, bytes: null, pages: null }, created: stampNow, updated: stampNow }
          tenants.set(id, t)
          const out: Record<string, unknown> = { ...tenantOut(t) }
          if (body.admin && typeof body.admin === 'object') {
            const a = body.admin as { username?: unknown }
            if (typeof a.username !== 'string' || !/^[a-z0-9][a-z0-9._-]{1,31}$/.test(a.username)) bad('admin.username is not a username')
            out.invite = { id: `INV${id}`, token: `invite-${id}`, role: 'admin', created: stampNow, expires: stampNow, tenant: id, siteAdmin: false }
          }
          return j(out, 201)
        }
      }
      const one = /^\/admin\/tenants\/([^/]+)$/.exec(path)
      if (one) {
        const id = decodeURIComponent(one[1])
        if (method === 'GET') return j(tenantOut(visible(me, id)))
        if (method === 'PATCH') {
          const t = visible(me, id)
          for (const k of Object.keys(body)) {
            if (!['name', 'slug', 'disabled', 'quota'].includes(k)) bad(`unknown field ${k}`)
            if (k !== 'name' && !site) throw new HttpError(403, 'forbidden', `only an administrator of the whole server may change ${k}`)
          }
          if ('name' in body) {
            const n = typeof body.name === 'string' ? body.name.trim() : ''
            if (!n || n.length > 80) bad('name is 1 to 80 characters')
            t.name = n
          }
          if ('slug' in body) {
            if (typeof body.slug !== 'string' || !SLUG.test(body.slug)) bad('slug is 2 to 32 characters of a-z, 0-9 and -')
            if (t.default && body.slug !== 'default') throw new HttpError(409, 'default_tenant', "the default tenant's slug is default")
            if ([...tenants.values()].some((x) => x.id !== id && x.slug === body.slug)) throw new HttpError(409, 'exists', 'that slug is taken')
            t.slug = body.slug as string
          }
          if ('disabled' in body) {
            if (typeof body.disabled !== 'boolean') bad('disabled is true or false')
            if (body.disabled && t.default) throw new HttpError(409, 'default_tenant', 'the default tenant cannot be disabled')
            t.disabled = body.disabled as boolean
          }
          if ('quota' in body) {
            const q = body.quota as Record<string, unknown>
            if (!q || typeof q !== 'object') bad('quota is an object')
            for (const [k, v] of Object.entries(q)) {
              if (!['people', 'spaces', 'bytes', 'pages'].includes(k)) bad('quota has people, spaces, bytes and pages')
              if (v !== null && !(typeof v === 'number' && v >= 0)) bad(`quota.${k} is a number from 0, or null`)
              ;(t.quota as Record<string, number | null>)[k] = v as number | null
            }
          }
          t.updated = o.now()
          return j(tenantOut(t))
        }
        if (method === 'DELETE') {
          needSite()
          const t = visible(me, id)
          if (t.default) throw new HttpError(409, 'default_tenant', 'the default tenant cannot be deleted')
          tenants.delete(id)
          tenantRows.delete(id)
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
        return j(putSite(decodeURIComponent(sk[1]), body))
      }
      const ts = /^\/admin\/tenants\/([^/]+)\/settings$/.exec(path)
      if (ts && method === 'GET') {
        const t = visible(me, decodeURIComponent(ts[1]))
        return j({ settings: DEFS.filter((d) => d.layers.includes('tenant')).map((d) => effectiveOf(t.id, d)) })
      }
      const tk = /^\/admin\/tenants\/([^/]+)\/settings\/([^/]+)$/.exec(path)
      if (tk && method === 'PUT') {
        const t = visible(me, decodeURIComponent(tk[1]))
        return j(putTenant(t.id, decodeURIComponent(tk[2]), body))
      }
      return undefined
    },
    same,
  }
}
export type TenantBook = ReturnType<typeof createTenantBook>
