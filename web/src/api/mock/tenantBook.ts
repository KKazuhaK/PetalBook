// The mock's side of milestone 3.3 (docs/api.md, "Tenants, registration and mail"): the switch, the tenants, and the
// layered settings with their locks. It does not keep people or spaces apart by tenant (the mock has one world);
// what it keeps true is what the web app reads: the contract of these endpoints.

import type { CreatedInvite, SettingLower, SiteSetting, Tenant, TenantSetting, TenantUsage } from '../types.ts'
import { HttpError } from './store.ts'
import { createLimitsBook } from './limitsBook.ts'
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
}
// The settings of 3.3, as the server has them (3.1's come with 3.1).
const DEFS: Def[] = [
  { key: 'registration.open', kind: 'boolean', layers: ['site', 'tenant'], def: false, lower: { mode: 'lock' } },
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
  { key: 'daily.own', kind: 'boolean', layers: ['site', 'tenant', 'space'], def: true, lower: { mode: 'allow' } },
  { key: 'daily.api', kind: 'boolean', layers: ['site', 'tenant', 'space'], def: true, lower: { mode: 'allow' } },
  // Whether people may keep offline copies on a device (4.1); the sync endpoints answer 403 offline_off when it is off.
  { key: 'offline.copies', kind: 'boolean', layers: ['site', 'tenant'], def: true, lower: { mode: 'allow' } },
  { key: 'daily.default', kind: 'doc', layers: ['site', 'tenant'], def: null, lower: { mode: 'allow' } },
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
  { key: 'storage.assets.ownStorageExempt', kind: 'boolean', layers: ['site'], def: true, lower: { mode: 'allow' } },
  // How many days a deleted space or person stays in the recycle bin (3.5); the site's alone.
  { key: 'recycle.days', kind: 'limit', layers: ['site'], def: 30, lower: { mode: 'allow' }, min: 1, max: 365 },
  // Whether addresses outside ASCII may be set (3.6); the site's alone.
  { key: 'users.emailNonAscii', kind: 'boolean', layers: ['site'], def: false, lower: { mode: 'allow' } },
]
const FILES_KEY = 'files.maxBytes'

function checkValue(d: Def, v: unknown): unknown {
  switch (d.kind) {
    case 'boolean':
      return typeof v === 'boolean' ? v : bad(`${d.key} is true or false`)
    case 'limit':
      if (v === null && d.def === null) return null
      return typeof v === 'number' && Number.isInteger(v) && v >= (d.min ?? 0) && v <= (d.max ?? 1e9)
        ? v
        : bad(`${d.key} is a number from ${d.min} to ${d.max}`)
    case 'choice':
      return typeof v === 'string' && d.choices?.includes(v) ? v : bad(`${d.key} is one of ${d.choices?.join(', ')}`)
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
  siteLocked?: boolean // a site administrator set and locked the value for this tenant
}

const ZERO: TenantUsage = { people: 0, spaces: 0, bytes: 0, pages: 0, measuredAt: null, over: [] }

export function createTenantBook(o: TenantBookOptions) {
  let enabled = false
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
    usage: ZERO,
    created: stamp,
    updated: stamp,
  })
  const siteRows = new Map<string, { value?: unknown; lower?: SettingLower }>()
  const tenantRows = new Map<string, Map<string, TenantRow>>()
  let seq = 1

  const siteValue = (d: Def): unknown =>
    d.key === FILES_KEY
      ? o.maxUpload
      : d.key === 'quota.spacesPerPerson' && o.maxSpacesFlag !== undefined
        ? o.maxSpacesFlag
        : d.key === 'daily.own'
          ? o.daily.get().own
          : d.key === 'daily.api'
            ? o.daily.get().api
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
    site: {
      value: siteValue(d),
      lower: siteLower(d),
      ...((d.key === 'quota.spacesPerPerson' && o.maxSpacesFlag !== undefined) || d.key === FILES_KEY
        ? { fromFlag: true }
        : {}),
    },
  })
  const tenantView = (tenant: string, d: Def): TenantSetting => {
    const row = tenantRows.get(tenant)?.get(d.key)
    const sl = siteLower(d)
    const own = row?.value
    const has = own !== undefined && own !== null
    // A value the site locked for this tenant is in effect unless the site's lock over everyone beats it.
    const siteLocked = !!row?.siteLocked && has
    const inEffect = !has || (siteLocked ? sl.mode !== 'lock' : allows(sl, d, own))
    return {
      key: d.key,
      kind: d.kind,
      layers: d.layers,
      ...(d.choices ? { choices: d.choices } : {}),
      site: { value: siteValue(d), lower: sl },
      tenant: { value: own ?? null, lower: row?.lower ?? null, inEffect, siteLocked },
      effective:
        d.key === FILES_KEY
          ? Math.min(o.maxUpload, (has && inEffect ? own : siteValue(d)) as number)
          : has && inEffect
            ? own
            : siteValue(d),
    }
  }
  const effectiveOf = (tenant: string, d: Def) => tenantView(tenant, d)

  const tenantOut = (t: Tenant): Tenant => ({
    ...t,
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
  // The limits sheet (3.6): a tenant's totals are its values of other.people and storage.quota.*.
  const sheets = createLimitsBook({
    now: o.now,
    visible,
    tenantIds: () => [...tenants.keys()],
    usageOf: (id) => o.usageOf(id),
    onTotals: (id, totals) => {
      const t = tenants.get(id)
      if (!t || same(t.quota, totals)) return
      t.quota = totals
      o.onQuotaChanged()
    },
  })
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
      if (key === FILES_KEY) throw new HttpError(409, 'conflict', `${key} is set by --max-upload; change the flag`)
      if (key === 'quota.spacesPerPerson' && o.maxSpacesFlag !== undefined)
        throw new HttpError(409, 'conflict', `${key} is set by a command-line flag; change the flag`)
      const v =
        d.kind === 'doc'
          ? body.value
          : body.value === null && d.kind === 'limit' && d.def !== null
            ? d.def
            : checkValue(d, body.value)
      if (key === 'registration.open' && v === true) checkOpening()
      if (key === 'daily.own') o.daily.set({ own: v as boolean })
      else if (key === 'daily.api') o.daily.set({ api: v as boolean })
      else row.value = v
    }
    let affected: number | undefined
    if ('lower' in body) {
      row.lower = body.lower === null ? undefined : checkLower(d, body.lower)
      const l = row.lower ?? d.lower
      if (
        key === 'registration.open' &&
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
    if (key === 'registration.open') o.onRegistrationChanged()
    if (key.startsWith('quota.')) o.onQuotaChanged()
    return { ...siteView(d), ...(affected === undefined ? {} : { affected }) }
  }
  const putTenant = (tenant: string, key: string, body: Record<string, unknown>, bySite: boolean): TenantSetting => {
    const d = defOf(key, 'tenant')
    for (const k of Object.keys(body)) if (k !== 'value' && k !== 'lower' && k !== 'siteLock') bad(`unknown field ${k}`)
    if (!('value' in body) && !('lower' in body) && !('siteLock' in body)) bad('give value, lower, siteLock or any')
    const sl = siteLower(d)
    const rows = tenantRows.get(tenant) ?? new Map<string, TenantRow>()
    const row = rows.get(key) ?? {}
    const locked = `${key} is locked for this tenant by the site's administrators`
    if (!bySite && 'siteLock' in body)
      throw new HttpError(409, 'setting_locked', `${key}: only the site's administrators lock a value for a tenant`)
    if (!bySite && row.siteLocked) throw new HttpError(409, 'setting_locked', locked)
    let locking = !!row.siteLocked
    if ('siteLock' in body) locking = body.siteLock === true
    const final = 'value' in body ? body.value : row.value
    if (final === undefined || final === null) {
      if (body.siteLock === true) bad(`${key}: a lock for the tenant needs a value to lock`)
      locking = false
    }
    const forced = bySite && locking
    if ('value' in body && body.value !== null) {
      if (sl.mode === 'lock' && !forced)
        throw new HttpError(409, 'setting_locked', `${key} is locked by the site's administrators`)
      const v = d.kind === 'doc' ? body.value : checkValue(d, body.value)
      if (!forced && !allows(sl, d, v))
        throw new HttpError(409, 'setting_restricted', `${key}: outside the site's rule`)
      if (key === 'registration.open' && v === true) checkOpening()
      row.value = v
    } else if ('value' in body) row.value = undefined
    if ('lower' in body) {
      if (body.lower === null) row.lower = undefined
      else {
        const l = checkLower(d, body.lower)
        if (!forced) {
          if (sl.mode === 'lock' && l.mode !== 'lock')
            throw new HttpError(409, 'setting_locked', `${key} is locked by the site's administrators`)
          if (!tightens(l, d, sl))
            throw new HttpError(
              409,
              'setting_restricted',
              `${key}: the tenant's rule must be at least as strict as the site's`,
            )
        }
        row.lower = l
      }
    }
    row.siteLocked = locking
    rows.set(key, row)
    tenantRows.set(tenant, rows)
    if (key === 'registration.open') o.onRegistrationChanged()
    if (key.startsWith('quota.')) o.onQuotaChanged()
    return effectiveOf(tenant, d)
  }

  const page = (all: Tenant[], cursor: string, limit: number) => {
    const from = cursor ? all.findIndex((x) => x.id === cursor) + 1 : 0
    const items = all.slice(from, from + limit)
    return { items: items.map(tenantOut), next: from + limit < all.length ? items[items.length - 1].id : '' }
  }

  // The settings of one person's layers (GET /api/settings).
  const mine = (tenant: string) =>
    DEFS.filter((d) => d.layers.includes('space') || d.key === 'offline.copies' || d.key === 'storage.assets').map(
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
  // The default tenant's own totals, which PATCH /admin/tenants/{id} sets.
  const quotaOf = (id: string) => tenants.get(id)?.quota ?? { people: null, spaces: null, bytes: null, pages: null }
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
    // The limits sheet turns the attachment repository off (sync.assetsRepository): only the space is possible.
    if (sheets.value(tenant, 'sync.assetsRepository') === false) {
      return { default: 'space', allowed: ['space'], locked: false }
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
          const all = [...tenants.values()]
            .filter((t) => !q || t.name.toLowerCase().includes(q) || t.slug.includes(q))
            .sort((a, b) => (a.default ? -1 : b.default ? 1 : a.name.toLowerCase() < b.name.toLowerCase() ? -1 : 1))
          return j(page(all, url.searchParams.get('cursor') ?? '', limit))
        }
        if (method === 'POST') {
          if (!enabled) throw new HttpError(409, 'tenants_off', 'more than the default tenant is switched off')
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
      const sheet = sheets.route(me, site, method, path, body)
      if (sheet) return sheet
      const one = /^\/admin\/tenants\/([^/]+)$/.exec(path)
      if (one) {
        const id = decodeURIComponent(one[1])
        if (method === 'GET') return j(tenantOut(visible(me, id)))
        if (method === 'PATCH') {
          const t = visible(me, id)
          for (const k of Object.keys(body)) {
            if (k === 'quota') bad('the totals are set through PUT /admin/tenants/{tenant}/limits')
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
          tenantRows.delete(id)
          sheets.forget(id)
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
        return j(putTenant(t.id, decodeURIComponent(tk[2]), body, site))
      }
      return undefined
    },
    same,
  }
}
export type TenantBook = ReturnType<typeof createTenantBook>
