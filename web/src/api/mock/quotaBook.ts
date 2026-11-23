// The mock's side of quotas and usage (docs/api.md, "Quotas"): what each person and the tenant use, the limits that
// apply (the person's own over the tenant's setting over the site's), the one refusal (the number of spaces) and the
// warnings (a notice, one audit row per crossing). The numbers are what the mock's own spaces say when they are
// "measured" (POST /admin/spaces/{id}/size), as the real server's background measurement writes them: nothing is
// counted when it is asked for.

import type { AccountUsage, AdminUsageList, OwnQuota, PersonQuotaKey, PersonUsage, TenantQuotaKey, TenantUsage, UsageNotice } from '../types.ts'
import { HttpError } from './store.ts'

const bad = (message: string): never => {
  throw new HttpError(400, 'invalid', message)
}

const KEYS: PersonQuotaKey[] = ['spaces', 'bytes', 'pages']
const RANGE: Record<PersonQuotaKey, [number, number]> = { spaces: [0, 10000], bytes: [1, 2 ** 50], pages: [1, 2 ** 40] }
const SETTING: Record<PersonQuotaKey, string> = { spaces: 'quota.spacesPerPerson', bytes: 'quota.bytesPerPerson', pages: 'quota.pagesPerPerson' }

// A space as the book sees it: who it is counted for, and what its last measurement found.
export interface UsageSpace {
  id: string
  ownerId: string | null
  bytes: number
  pages: number
  measuredAt: number | null
}

export interface QuotaBookOptions {
  now: () => number
  rfc3339: (ms: number) => string
  tenant: string
  people: () => { id: string; username: string; name: string }[]
  spaces: () => UsageSpace[]
  // The effective value of a setting for the tenant (quota.* of the site, the tenant and the flag).
  setting: (key: string) => number | null
  // What a layer above lets the layer below do with a setting (site administrators answer to the site's rule only).
  lowerOf: (key: string, site: boolean) => { mode: 'allow' | 'restrict' | 'lock'; max?: number }
  tenantQuota: () => Record<TenantQuotaKey, number | null>
  record: (kind: string, o: { user?: string; tenant?: string; detail: Record<string, unknown> }) => void
}

export function createQuotaBook(o: QuotaBookOptions) {
  const own = new Map<string, OwnQuota>()
  // What a row was last found to be over, so a crossing is reported once.
  const was = new Map<string, string>()

  const ownOf = (id: string): OwnQuota => ({ spaces: null, bytes: null, pages: null, ...own.get(id) })
  const limitsOf = (id: string): OwnQuota => {
    const mine = ownOf(id)
    const out = {} as OwnQuota
    for (const k of KEYS) {
      const v = mine[k] ?? o.setting(SETTING[k])
      out[k] = k === 'spaces' && v === 0 ? null : v
    }
    return out
  }
  const personRow = (id: string) => {
    const mine = o.spaces().filter((s) => s.ownerId === id)
    const measured = mine.map((s) => s.measuredAt ?? 0)
    return {
      spaces: mine.length,
      bytes: mine.reduce((n, s) => n + s.bytes, 0),
      pages: mine.reduce((n, s) => n + s.pages, 0),
      measuredAt: measured.length && Math.max(...measured) > 0 ? Math.max(...measured) : null,
    }
  }
  const over = (row: { spaces: number; bytes: number; pages: number }, l: OwnQuota): PersonQuotaKey[] => KEYS.filter((k) => l[k] !== null && row[k] > (l[k] as number))

  const tenantRow = (): TenantUsage => {
    const all = o.spaces()
    const people = o.people().length
    const q = o.tenantQuota()
    const row = {
      people,
      spaces: all.length,
      bytes: all.reduce((n, s) => n + s.bytes, 0),
      pages: all.reduce((n, s) => n + s.pages, 0),
    }
    const at = Math.max(0, ...all.map((s) => s.measuredAt ?? 0))
    const tenantOver = (['people', 'spaces', 'bytes', 'pages'] as TenantQuotaKey[]).filter((k) => q[k] !== null && row[k] > (q[k] as number))
    return { ...row, measuredAt: at > 0 ? o.rfc3339(at) : null, over: tenantOver }
  }

  // One audit row per crossing: what is over now and was not before.
  const evaluate = () => {
    for (const p of o.people()) {
      const l = limitsOf(p.id)
      const row = personRow(p.id)
      const now = over(row, l)
      const before = (was.get(p.id) ?? '').split(',').filter(Boolean)
      for (const k of now) if (!before.includes(k)) o.record('quota_exceeded', { user: p.id, tenant: o.tenant, detail: { quota: k, limit: l[k], used: row[k], scope: 'person' } })
      was.set(p.id, now.join(','))
    }
    const t = tenantRow()
    const q = o.tenantQuota()
    const before = (was.get('tenant') ?? '').split(',').filter(Boolean)
    for (const k of t.over) if (!before.includes(k)) o.record('quota_exceeded', { tenant: o.tenant, detail: { quota: k, limit: q[k], used: t[k], scope: 'tenant' } })
    was.set('tenant', t.over.join(','))
  }

  return {
    evaluate,
    ownOf,
    tenantUsage: tenantRow,
    // Creating a space: the one quota that is refused.
    checkSpace(userId: string) {
      const l = limitsOf(userId)
      const used = personRow(userId).spaces
      if (l.spaces !== null && used >= l.spaces) throw new HttpError(403, 'quota_exceeded', `you already own ${used} spaces, the most you may have`, { quota: 'spaces', limit: l.spaces, used })
      const q = o.tenantQuota().spaces
      const all = o.spaces().length
      if (q !== null && q > 0 && all >= q) throw new HttpError(403, 'quota_exceeded', `this tenant already has ${all} spaces, the most it may have`, { quota: 'spaces', limit: q, used: all })
    },
    account(userId: string): AccountUsage {
      const l = limitsOf(userId)
      const row = personRow(userId)
      const t = tenantRow()
      const tq = o.tenantQuota()
      const notices: UsageNotice[] = [
        ...over(row, l).map((k): UsageNotice => ({ quota: k, scope: 'person', limit: l[k] as number, used: row[k] })),
        ...t.over.map((k): UsageNotice => ({ quota: k, scope: 'tenant', limit: tq[k] as number, used: t[k] })),
      ]
      return {
        spaces: { used: row.spaces, limit: l.spaces },
        bytes: { used: row.bytes, limit: l.bytes },
        pages: { used: row.pages, limit: l.pages },
        measuredAt: row.measuredAt === null ? null : o.rfc3339(row.measuredAt),
        notices,
      }
    },
    list(url: URL, scopeTenant: string): AdminUsageList {
      const q = url.searchParams
      const limit = q.has('limit') ? Number(q.get('limit')) : 50
      if (!(Number.isInteger(limit) && limit >= 1 && limit <= 200)) bad('limit is a number from 1 to 200')
      const overParam = q.get('over')
      if (overParam !== null && !['1', '0', ''].includes(overParam)) bad('over is 1 or 0')
      const t = q.get('tenant')
      if (t && t !== o.tenant && t !== scopeTenant) throw new HttpError(404, 'not_found', 'no such tenant')
      let rows: PersonUsage[] = o.people().map((p) => {
        const row = personRow(p.id)
        const l = limitsOf(p.id)
        return {
          user: { id: p.id, username: p.username, name: p.name },
          tenant: o.tenant,
          spaces: row.spaces,
          bytes: row.bytes,
          pages: row.pages,
          limits: l,
          own: ownOf(p.id),
          // The mock has no locks or restrictions on a person's limits: an own value is always in force.
          ownInEffect: { spaces: true, bytes: true, pages: true },
          over: over(row, l),
          measuredAt: row.measuredAt === null ? null : o.rfc3339(row.measuredAt),
        }
      })
      if (overParam === '1') rows = rows.filter((r) => r.over.length > 0)
      rows.sort((a, b) => b.bytes - a.bytes || (a.user.id < b.user.id ? -1 : 1))
      const cursor = q.get('cursor') ?? ''
      let from = 0
      if (cursor) {
        const [b, id] = [cursor.slice(0, cursor.indexOf(':')), cursor.slice(cursor.indexOf(':') + 1)]
        if (!/^\d+$/.test(b) || !id) bad('that cursor is not one this list gave')
        from = rows.findIndex((r) => r.bytes < Number(b) || (r.bytes === Number(b) && r.user.id > id))
        if (from < 0) from = rows.length
      }
      const page = rows.slice(from, from + limit)
      const last = page[page.length - 1]
      return { items: page, next: from + limit < rows.length && last ? `${last.bytes}:${last.user.id}` : '' }
    },
    // quota on PATCH /api/users/{id}: validated before anything is changed, applied by the returned function.
    patch(userId: string, raw: unknown, site: boolean): () => void {
      if (!raw || typeof raw !== 'object' || Array.isArray(raw)) bad('quota is an object with spaces, bytes and pages')
      const next = ownOf(userId)
      for (const [k, v] of Object.entries(raw as Record<string, unknown>)) {
        if (!(KEYS as string[]).includes(k)) bad('quota has spaces, bytes and pages')
        const key = k as PersonQuotaKey
        if (v === null) {
          next[key] = null
          continue
        }
        if (typeof v !== 'number' || !Number.isInteger(v) || v < RANGE[key][0] || v > RANGE[key][1]) bad(`quota.${k} is a number from ${RANGE[key][0]} to ${RANGE[key][1]}, or null to follow the tenant`)
        const lower = o.lowerOf(SETTING[key], site)
        if (lower.mode === 'lock') throw new HttpError(409, 'setting_locked', `${SETTING[key]} is locked: nobody below it may change it`)
        if (lower.mode === 'restrict' && (lower.max === undefined || (v as number) > lower.max)) throw new HttpError(409, 'setting_restricted', `${SETTING[key]}: above what the layer above allows`)
        if (!site) {
          // Above the tenant's value only a site administrator may go; no limit is the highest.
          const cap = o.setting(SETTING[key])
          const rank = (n: number | null) => (n === null || (key === 'spaces' && n === 0) ? Infinity : n)
          if (rank(v as number) > rank(cap)) throw new HttpError(403, 'forbidden', "only an administrator of the whole server may give a person more than their tenant allows")
        }
        next[key] = v as number
      }
      return () => {
        own.set(userId, next)
        evaluate()
      }
    },
  }
}
export type QuotaBook = ReturnType<typeof createQuotaBook>
