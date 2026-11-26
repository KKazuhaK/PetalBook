// The mock's side of the limits sheet (docs/api.md, "The limits sheet", "Capabilities", "Plans"): the registry of keys,
// each tenant's manual values with their windows, plans and the site's defaults, and the endpoints that read and write
// them. The effective value is the first present of manual, billing (none in the mock), plan, site.

import type { LimitEntry, LimitKind, LimitSource, LimitValue, Tenant } from '../types.ts'
import { HttpError } from './store.ts'
import type { MockResponse } from './server.ts'

const bad = (message: string): never => {
  throw new HttpError(400, 'invalid', message)
}
const SLUG = /^[a-z0-9][a-z0-9-]{1,31}$/
const MODES = ['server', 'own', 'tenant']

interface Def {
  key: string
  kind: LimitKind
  def: LimitValue
  capability: boolean
  description: string
  noNull?: boolean
}
const cap = (key: string, def: boolean, description: string): Def => ({
  key,
  kind: 'boolean',
  def,
  capability: true,
  description,
})
const lim = (key: string, description: string, def: number | null = null, noNull = false): Def => ({
  key,
  kind: 'limit',
  def,
  capability: false,
  description,
  noNull,
})

// The server's registry (internal/limits/registry.go), in its order.
export const LIMIT_DEFS: Def[] = [
  cap('signin.sso', true, "The tenant's own SSO providers, joining through them, the tenant layer of auth.ssoOnly"),
  cap('signin.domains', true, 'Email domain claims, the SSO entry by domain, joining by domain, restrictOrgs'),
  cap('signin.managedAccounts', true, 'Managed accounts; they also need signin.sso and signin.domains'),
  cap('signin.socialChoice', true, 'The tenant layer of auth.social'),
  cap('signin.idpAdmins', false, "groups.admins on the tenant's SSO providers"),
  cap('data.adminView', true, "Tenant administrators viewing and exporting the content of the organisation's spaces"),
  cap('data.auditDownload', true, 'Downloading the audit log'),
  cap('integrations.smtp', true, "The tenant's own SMTP"),
  cap('integrations.captcha', true, "The tenant's own captcha keys"),
  cap('integrations.connections', true, "The tenant's own providers, also as a storage target"),
  cap('integrations.daily', true, "The tenant's own daily-text interfaces and lines"),
  cap('integrations.tokens', true, "API tokens of the tenant's people, and the tenant layer of access.tokens"),
  cap('integrations.mcp', true, "MCP and OAuth grants of the tenant's people, and the tenant layer of access.mcp"),
  cap('other.offline', true, "Offline copies of the tenant's spaces, and the tenant layer of offline.copies"),
  cap('other.registration', true, 'The tenant layer of registration.*'),
  cap(
    'sync.assetsRepository',
    true,
    "Whether the tenant's spaces may use an attachment repository (off: storage.assets is space)",
  ),
  {
    key: 'storage.modes',
    kind: 'list',
    def: MODES,
    capability: true,
    description: 'The storage modes the tenant may use for new spaces',
  },
  lim('storage.maxFileBytes', "The ceiling of the tenant's files.maxBytes, in bytes; null leaves --max-upload"),
  lim('storage.pinnedBytes', "What the tenant's administrators may pin, in bytes", 0, true),
  lim('storage.quota.spaces', 'Spaces in the tenant in total'),
  lim('storage.quota.bytes', 'Storage of the tenant in total, in bytes; also its allocation for storage pressure'),
  lim('storage.quota.pages', 'Pages in the tenant in total'),
  lim('storage.perPerson.spaces', "The ceiling of the tenant's spacesPerPerson"),
  lim('storage.perPerson.bytes', "The ceiling of the tenant's bytesPerPerson"),
  lim('storage.perPerson.pages', "The ceiling of the tenant's pagesPerPerson"),
  lim('other.people', 'People in the tenant (counted and shown, not refused)'),
  lim('usage.apiPerMinute', 'API-token requests per minute over the tenant'),
  lim('usage.mcpPerMinute', 'MCP requests per minute over the tenant'),
  lim('usage.mailPerDay', 'Mail sent for the tenant per day'),
  lim(
    'usage.outboundPerMinute',
    "The tenant's outgoing calls to external interfaces per minute; null is the site's outbound.tenantPerMinute",
  ),
]
const DEF = new Map(LIMIT_DEFS.map((d) => [d.key, d]))

// The totals a tenant shows (its `quota`), by the key they are held under.
const TOTALS: Record<string, 'people' | 'spaces' | 'bytes' | 'pages'> = {
  'other.people': 'people',
  'storage.quota.spaces': 'spaces',
  'storage.quota.bytes': 'bytes',
  'storage.quota.pages': 'pages',
}

interface Row {
  value: LimitValue
  from?: string
  until?: string
}
interface Plan {
  slug: string
  name: string
  description: string
  values: Record<string, LimitValue>
}

// Checks a value against its key and returns it as it is kept.
export function checkValue(key: string, v: unknown): LimitValue {
  const d = DEF.get(key)
  if (!d) return bad(`${key} is not a key of the limits sheet`)
  if (d.kind === 'boolean') return typeof v === 'boolean' ? v : bad(`${key} is true or false`)
  if (d.kind === 'limit') {
    if (v === null) return d.noNull ? bad(`${key} is a number, not null`) : null
    return typeof v === 'number' && Number.isInteger(v) && v >= 0
      ? v
      : bad(`${key} is a whole number of at least 0, or null`)
  }
  if (!Array.isArray(v)) return bad(`${key} is a list`)
  const out: string[] = []
  for (const x of v) {
    if (typeof x !== 'string' || !MODES.includes(x)) bad(`${key}: ${String(x)} is not one of ${MODES.join(', ')}`)
    if (out.includes(x as string)) bad(`${key}: ${String(x)} twice`)
    out.push(x as string)
  }
  return out.sort()
}

// One entry of "values" in a write: null removes, an object is {value, from, until}, anything else is the value.
function entryIn(key: string, raw: unknown): Row | null {
  if (raw === null) return null
  if (typeof raw !== 'object' || Array.isArray(raw)) return { value: checkValue(key, raw) }
  const e = raw as Record<string, unknown>
  for (const k of Object.keys(e))
    if (!['value', 'from', 'until'].includes(k)) bad('an object is {"value": …, "from": …, "until": …}')
  if (!('value' in e)) bad('an object is {"value": …, "from": …, "until": …}')
  const row: Row = { value: checkValue(key, e.value) }
  for (const k of ['from', 'until'] as const) {
    if (e[k] === undefined) continue
    if (typeof e[k] !== 'string' || Number.isNaN(Date.parse(e[k] as string))) bad('from and until are RFC 3339 times')
    row[k] = new Date(e[k] as string).toISOString().replace('.000Z', 'Z')
  }
  if (row.from && row.until && Date.parse(row.until) <= Date.parse(row.from)) bad(`${key}: until is not after from`)
  return row
}

export interface LimitsBookOptions {
  now: () => string
  // The tenant, or 404 when the caller may not see it.
  visible: (me: string, id: string) => Tenant
  // A tenant's totals changed: it shows them as its quota, and the quotas are looked at again.
  onTotals: (id: string, totals: Record<'people' | 'spaces' | 'bytes' | 'pages', number | null>) => void
  tenantIds: () => string[]
  // What a tenant uses, for the entries that are counted.
  usageOf: (id: string) => { people: number; spaces: number; bytes: number; pages: number }
}

export function createLimitsBook(o: LimitsBookOptions) {
  const site = new Map<string, LimitValue>()
  const plans = new Map<string, Plan>()
  const planOf = new Map<string, string>()
  const manual = new Map<string, Map<string, Row>>()
  const active = (r: Row) => {
    const now = Date.parse(o.now())
    return (!r.from || now >= Date.parse(r.from)) && (!r.until || now < Date.parse(r.until))
  }
  const siteValue = (d: Def): LimitValue => (site.has(d.key) ? (site.get(d.key) as LimitValue) : d.def)

  const entry = (id: string, d: Def): LimitEntry => {
    const chain: { v: LimitValue; src: LimitSource; row?: Row; on: boolean }[] = []
    const m = manual.get(id)?.get(d.key)
    if (m) chain.push({ v: m.value, src: 'manual', row: m, on: active(m) })
    const p = planOf.has(id) ? plans.get(planOf.get(id)!) : undefined
    if (p && d.key in p.values) chain.push({ v: p.values[d.key], src: 'plan', on: true })
    chain.push({ v: siteValue(d), src: 'site', on: true })
    const first = chain.findIndex((c) => c.on)
    const c = chain[first]
    const e: LimitEntry = { key: d.key, kind: d.kind, description: d.description, value: c.v, source: c.src }
    if (c.row?.from) e.from = c.row.from
    if (c.row?.until) e.until = c.row.until
    if (c.src === 'manual') {
      const under = chain.slice(first + 1).find((x) => x.on)
      if (under) e.underlying = { value: under.v, source: under.src }
    }
    if (m && !active(m)) e.manual = { value: m.value, from: m.from ?? null, until: m.until ?? null }
    return e
  }
  const sheet = (id: string, capsOnly: boolean): LimitEntry[] => {
    const use = o.usageOf(id)
    const used: Record<string, number> = {
      'other.people': use.people,
      'storage.quota.spaces': use.spaces,
      'storage.quota.bytes': use.bytes,
      'storage.quota.pages': use.pages,
    }
    return LIMIT_DEFS.filter((d) => !capsOnly || d.capability).map((d) => {
      const e = entry(id, d)
      if (!capsOnly && d.key in used) e.used = used[d.key]
      return e
    })
  }
  const planRef = (id: string) => {
    const p = planOf.has(id) ? plans.get(planOf.get(id)!) : undefined
    return p ? { slug: p.slug, name: p.name } : null
  }
  const syncTotals = (id: string) => {
    const t = { people: null, spaces: null, bytes: null, pages: null } as Record<
      'people' | 'spaces' | 'bytes' | 'pages',
      number | null
    >
    for (const [key, name] of Object.entries(TOTALS)) t[name] = entry(id, DEF.get(key)!).value as number | null
    o.onTotals(id, t)
  }
  const syncAll = () => o.tenantIds().forEach(syncTotals)
  const planOut = (p: Plan) => ({
    slug: p.slug,
    name: p.name,
    description: p.description,
    values: p.values,
    tenants: [...planOf.values()].filter((s) => s === p.slug).length,
  })
  const siteView = () =>
    LIMIT_DEFS.map((d) => ({
      key: d.key,
      kind: d.kind,
      group: d.key.split('.')[0],
      description: d.description,
      default: d.def,
      value: siteValue(d),
      capability: d.capability,
    }))
  const values = (body: Record<string, unknown>) => {
    const v = body.values
    if (!v || typeof v !== 'object' || Array.isArray(v) || Object.keys(v).length === 0)
      bad('values is an object of the keys to change.')
    return v as Record<string, unknown>
  }
  const changedBy = (fn: () => void) => {
    const before = new Map(o.tenantIds().map((id) => [id, JSON.stringify(sheet(id, false))]))
    fn()
    syncAll()
    return o.tenantIds().filter((id) => before.get(id) !== JSON.stringify(sheet(id, false)))
  }

  return {
    // The effective value a tenant has for a key (the quotas and the attachment policy read it).
    value: (id: string, key: string): LimitValue => entry(id, DEF.get(key)!).value,
    forget: (id: string) => {
      manual.delete(id)
      planOf.delete(id)
    },
    // Returns undefined for a path that is not this book's.
    route(
      me: string,
      isSite: boolean,
      method: string,
      path: string,
      body: Record<string, unknown>,
    ): MockResponse | undefined {
      const j = (b: unknown, status = 200): MockResponse => ({ status, body: b })
      const needSite = () => {
        if (!isSite) throw new HttpError(403, 'forbidden', 'only an administrator of the whole server may do that')
      }
      if (path === '/admin/capabilities') {
        needSite()
        if (method === 'GET') return j({ capabilities: siteView() })
        if (method === 'PUT') {
          const given = values(body)
          const changes = new Map<string, LimitValue | undefined>()
          for (const [k, raw] of Object.entries(given)) {
            const row = entryIn(k, raw)
            if (row && (row.from || row.until)) bad(`${k}: the site's default has no dates`)
            changes.set(k, row ? row.value : undefined)
          }
          const affected = changedBy(() => {
            for (const [k, v] of changes)
              if (v === undefined) site.delete(k)
              else site.set(k, v)
          })
          return j({ affected, capabilities: siteView() })
        }
      }
      const sheetPath = /^\/admin\/tenants\/([^/]+)\/(limits|capabilities)$/.exec(path)
      if (sheetPath) {
        const t = o.visible(me, decodeURIComponent(sheetPath[1]))
        const capsOnly = sheetPath[2] === 'capabilities'
        const answer = (warning?: unknown) =>
          j({
            plan: planRef(t.id),
            [capsOnly ? 'capabilities' : 'limits']: sheet(t.id, capsOnly),
            ...(warning ? { warning } : {}),
          })
        if (method === 'GET') return answer()
        if (method === 'PUT') {
          needSite()
          const given = values(body)
          const writes = new Map<string, Row | null>()
          for (const [k, raw] of Object.entries(given)) {
            const d = DEF.get(k)
            if (!d || (capsOnly && !d.capability)) bad(`${k} is not a key of this sheet.`)
            writes.set(k, entryIn(k, raw))
          }
          const replaced: string[] = []
          const rows = manual.get(t.id) ?? new Map<string, Row>()
          for (const [k, row] of writes) {
            const old = rows.get(k)
            if (row && (row.from || row.until) && old && !old.from && !old.until) replaced.push(k)
            if (row) rows.set(k, row)
            else rows.delete(k)
          }
          manual.set(t.id, rows)
          syncTotals(t.id)
          return answer(
            replaced.length
              ? {
                  code: 'undated_value_replaced',
                  keys: replaced.sort(),
                  message:
                    'There is one manual value for each key. A dated value replaced the undated one: when its window ends, the value is not the undated one but what is below it (billing, the plan, the site).',
                }
              : undefined,
          )
        }
      }
      const tp = /^\/admin\/tenants\/([^/]+)\/plan$/.exec(path)
      if (tp && method === 'PUT') {
        needSite()
        const t = o.visible(me, decodeURIComponent(tp[1]))
        if (body.plan !== null && typeof body.plan !== 'string') bad('plan is a slug, or null for none')
        if (typeof body.plan === 'string' && !plans.has(body.plan)) bad(`There is no plan ${body.plan}.`)
        const rows = manual.get(t.id)
        const cleared = [...(rows ?? [])]
          .sort(([a], [b]) => (a < b ? -1 : 1))
          .map(([key, r]) => ({ key, value: r.value, from: r.from ?? null, until: r.until ?? null }))
        manual.delete(t.id)
        if (body.plan === null) planOf.delete(t.id)
        else planOf.set(t.id, body.plan as string)
        syncTotals(t.id)
        return j({ plan: planRef(t.id), cleared })
      }
      if (path === '/admin/plans') {
        needSite()
        if (method === 'GET')
          return j({ plans: [...plans.values()].sort((a, b) => (a.slug < b.slug ? -1 : 1)).map(planOut) })
        if (method === 'POST') {
          const name = typeof body.name === 'string' ? body.name.trim() : ''
          if (typeof body.slug !== 'string' || !SLUG.test(body.slug) || !name || name.length > 80)
            bad('A plan has a slug (2 to 32 characters of a-z, 0-9 and -) and a name (1 to 80 characters).')
          const desc = typeof body.description === 'string' ? body.description.trim() : ''
          if (desc.length > 500) bad('A plan description is at most 500 characters.')
          const vals: Record<string, LimitValue> = {}
          for (const [k, v] of Object.entries((body.values as Record<string, unknown>) ?? {}))
            vals[k] = checkValue(k, v)
          if (plans.size >= 100) throw new HttpError(409, 'plan_limit', 'There are at most 100 plans.')
          if (plans.has(body.slug as string)) throw new HttpError(409, 'exists', 'That slug is taken.')
          const p = { slug: body.slug as string, name, description: desc, values: vals }
          plans.set(p.slug, p)
          return j(planOut(p), 201)
        }
      }
      const one = /^\/admin\/plans\/([^/]+)$/.exec(path)
      if (one) {
        needSite()
        const slug = decodeURIComponent(one[1])
        const p = plans.get(slug)
        if (!p) throw new HttpError(404, 'not_found', 'No such plan.')
        if (method === 'GET') return j(planOut(p))
        if (method === 'PATCH') {
          for (const k of Object.keys(body))
            if (!['name', 'description', 'values'].includes(k)) bad('A plan changes by name, description and values.')
          if ('name' in body) {
            const n = typeof body.name === 'string' ? body.name.trim() : ''
            if (!n || n.length > 80) bad('A plan name is 1 to 80 characters.')
            p.name = n
          }
          if ('description' in body) {
            const d = typeof body.description === 'string' ? body.description.trim() : ''
            if (d.length > 500) bad('A plan description is at most 500 characters.')
            p.description = d
          }
          const affected = changedBy(() => {
            for (const [k, v] of Object.entries((body.values as Record<string, unknown>) ?? {})) {
              if (v === null) delete p.values[k]
              else p.values[k] = checkValue(k, v)
            }
          })
          return j({ ...planOut(p), affected })
        }
        if (method === 'DELETE') {
          const n = [...planOf.values()].filter((s) => s === slug).length
          if (n > 0)
            throw new HttpError(409, 'plan_in_use', 'Tenants use this plan; give them another first.', { tenants: n })
          plans.delete(slug)
          return { status: 204 }
        }
      }
      return undefined
    },
  }
}
export type LimitsBook = ReturnType<typeof createLimitsBook>
