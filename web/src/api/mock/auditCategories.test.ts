import { describe, expect, it } from 'vitest'
import { createMockServer, MOCK_ADMIN } from './server.ts'
import { AUDIT_CATEGORIES, categoryOfKind, isSiteKind } from './auditKinds.ts'

// The mock's audit log with categories, the registry of kinds, the filters and the CSV: the contract of docs/api.md
// ("Audit log"). The real server's registry is tested in Go (internal/auditlog).

function world() {
  let clock = Date.parse('2026-11-23T08:00:00Z')
  const server = createMockServer({ empty: true, mode: 'accounts', now: () => clock, oidc: null })
  const req = (method: string, path: string, body?: unknown, cookie = '') => {
    const r = server.handle(
      method,
      `/api${path}`,
      body === undefined ? undefined : JSON.stringify(body),
      'application/json',
      cookie,
    )
    const set = r.headers?.['Set-Cookie']
    return {
      status: r.status,
      body: (r.body ?? {}) as any,
      bytes: r.bytes,
      headers: r.headers,
      cookie: set ? set.split(';')[0] : '',
    }
  }
  const root = req('POST', '/login', { username: MOCK_ADMIN.username, password: MOCK_ADMIN.password }).cookie
  return { req, root, advance: (ms: number) => (clock += ms) }
}

const kindsOf = (r: { body: any }) => (r.body.items as { kind: string }[]).map((i) => i.kind)

describe('the mock audit log', () => {
  it('files every row under its kind’s category, and the registry says which kinds only a site administrator sees', () => {
    expect(categoryOfKind('login')).toBe('signins')
    expect(categoryOfKind('login_failed')).toBe('security')
    expect(categoryOfKind('space_deleted')).toBe('data')
    expect(categoryOfKind('made-up')).toBe('admin')
    expect(isSiteKind('provider_created')).toBe(true)
    expect(isSiteKind('tenant_changed')).toBe(false)
    expect(AUDIT_CATEGORIES.filter((c) => c.core).map((c) => c.id)).toEqual(['security', 'admin', 'data', 'system'])
    expect(AUDIT_CATEGORIES.filter((c) => !c.core).every((c) => c.setting === `audit.${c.id}`)).toBe(true)
    const w = world()
    const rows = w.req('GET', '/admin/audit', undefined, w.root).body.items
    expect(rows.length).toBeGreaterThan(0)
    expect(rows.every((r: any) => typeof r.category === 'string' && r.capacity === null)).toBe(true)
  })

  it('filters by several categories, kind, actor and the day, and refuses what is not a filter', () => {
    const w = world()
    w.req('POST', '/login', { username: 'nobody', password: 'wrong-password' })
    w.advance(86_400_000)
    w.req('PUT', '/admin/settings/signin.perMinute', { value: 25 }, w.root)
    const list = (q: string) => w.req('GET', `/admin/audit?${q}`, undefined, w.root)
    expect(kindsOf(list('category=security'))).toEqual(['login_failed'])
    expect(new Set(kindsOf(list('category=security,signins')))).toEqual(new Set(['login_failed', 'login']))
    expect(new Set(kindsOf(list('category=security&category=admin')))).toContain('setting_changed')
    expect(kindsOf(list('kind=setting_changed&limit=1'))).toEqual(['setting_changed'])
    // The first day has the refused sign-in, the second the setting.
    expect(kindsOf(list('from=2026-11-23&to=2026-11-23&category=security'))).toEqual(['login_failed'])
    expect(kindsOf(list('from=2026-11-24&category=security'))).toEqual([])
    expect(kindsOf(list('from=2026-11-24'))).toContain('setting_changed')
    for (const bad of ['category=nope', 'capacity=root', 'from=yesterday', 'to=2026-13-40'])
      expect(list(bad).status).toBe(400)
  })

  it('lists the registry with whether each optional category is on, and writes nothing for one that is off', () => {
    const w = world()
    const on = () =>
      Object.fromEntries(
        (w.req('GET', '/admin/audit/kinds', undefined, w.root).body.categories as any[]).map((c) => [c.id, c.on]),
      )
    expect(on()).toEqual({
      security: true,
      admin: true,
      data: true,
      system: true,
      signins: true,
      reads: false,
      downloads: false,
      api: false,
    })
    const logins = () => kindsOf(w.req('GET', '/admin/audit?kind=login&limit=200', undefined, w.root)).length
    const before = logins()
    w.req('PUT', '/admin/settings/audit.signins', { value: false }, w.root)
    expect(on().signins).toBe(false)
    w.req('POST', '/login', { username: MOCK_ADMIN.username, password: MOCK_ADMIN.password })
    expect(logins()).toBe(before)
    w.req('PUT', '/admin/settings/audit.signins', { value: true }, w.root)
    w.req('POST', '/login', { username: MOCK_ADMIN.username, password: MOCK_ADMIN.password })
    expect(logins()).toBe(before + 1)
  })

  it('downloads the filter as CSV with a quote before a cell that starts like a formula, and audits the download', () => {
    const w = world()
    w.req('POST', '/login', { username: '=cmd|calc', password: 'wrong-password' })
    const r = w.req('GET', '/admin/audit/download?category=security', undefined, w.root)
    expect(r.status).toBe(200)
    expect(r.headers?.['Content-Type']).toBe('text/csv; charset=utf-8')
    expect(r.headers?.['Content-Disposition']).toMatch(/^attachment/)
    const text = new TextDecoder().decode(r.bytes)
    const lines = text.trimEnd().split('\n')
    expect(lines[0]).toBe('id,time,category,kind,capacity,tenant,actor,user,username,ip,detail')
    expect(lines[1]).toContain("'=cmd|calc")
    expect(lines.length).toBe(2)
    expect(kindsOf(w.req('GET', '/admin/audit?kind=audit_downloaded', undefined, w.root))).toEqual(['audit_downloaded'])
  })
})
