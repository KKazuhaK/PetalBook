import { describe, expect, it } from 'vitest'
import { createMockServer, MOCK_ADMIN } from './server.ts'

// The mock's quotas and usage: the contract of docs/api.md ("Quotas"), which the web app reads. The numbers are what
// the spaces say when they are measured, as the real server's background measurement writes them.

function world(options: { maxSpaces?: number } = {}) {
  let clock = Date.parse('2026-11-23T08:00:00Z')
  const server = createMockServer({ empty: true, mode: 'accounts', now: () => clock, oidc: null, ...options })
  const req = (method: string, path: string, body?: unknown, cookie = '') => {
    const r = server.handle(
      method,
      `/api${path}`,
      body === undefined ? undefined : JSON.stringify(body),
      'application/json',
      cookie,
    )
    const set = r.headers?.['Set-Cookie']
    return { status: r.status, body: (r.body ?? {}) as any, cookie: set ? set.split(';')[0] : '' }
  }
  const login = (username: string, password: string) => req('POST', '/login', { username, password }).cookie
  const root = login(MOCK_ADMIN.username, MOCK_ADMIN.password)
  const person = (username: string, role = 'member') => {
    const inv = req('POST', '/invites', { role }, root).body
    const r = req('POST', '/invites/accept', { token: inv.token, username, password: 'a-long-password' })
    return { id: r.body.user.id as string, cookie: r.cookie }
  }
  const spaceIdOf = (cookie: string) => req('GET', '/spaces', undefined, cookie).body.spaces[0].id as string
  return { server, req, root, person, spaceIdOf, advance: (ms: number) => (clock += ms) }
}

describe('the mock account usage', () => {
  it('is the personal space, nothing measured, no limit but the default number of spaces; measuring fills the bytes and pages', () => {
    const w = world()
    const kai = w.person('kai')
    const u = w.req('GET', '/account/usage', undefined, kai.cookie).body
    expect(u).toMatchObject({
      spaces: { used: 1, limit: 20 },
      bytes: { used: 0, limit: null },
      pages: { used: 0, limit: null },
      measuredAt: null,
      notices: [],
    })
    const sp = w.spaceIdOf(kai.cookie)
    w.req('POST', `/spaces/${sp}/pages`, { folder: '', meta: { title: 'one' }, body: 'text' }, kai.cookie)
    w.req('POST', `/admin/spaces/${sp}/size`, undefined, w.root)
    const after = w.req('GET', '/account/usage', undefined, kai.cookie).body
    expect(after.bytes.used).toBeGreaterThan(0)
    expect(after.pages.used).toBeGreaterThan(0)
    expect(after.measuredAt).toBe('2026-11-23T08:00:00Z')
    expect(w.req('GET', '/account/usage').status).toBe(401)
  })

  it('is the layered setting for the limit on spaces, and the number of spaces is the one refusal, with what ran out', () => {
    const w = world()
    const kai = w.person('kai')
    expect(w.req('PUT', '/admin/settings/quota.spacesPerPerson', { value: 2 }, w.root).status).toBe(200)
    expect(w.req('POST', '/spaces', { name: 'B' }, kai.cookie).status).toBe(201)
    const refused = w.req('POST', '/spaces', { name: 'C' }, kai.cookie)
    expect([refused.status, refused.body.error, refused.body.quota, refused.body.limit, refused.body.used]).toEqual([
      403,
      'quota_exceeded',
      'spaces',
      2,
      2,
    ])
    // The person's own limit goes over the setting; 0 is none; null follows again.
    expect(w.req('PATCH', `/users/${kai.id}`, { quota: { spaces: 5 } }, w.root).body.user.quota).toEqual({
      spaces: 5,
      bytes: null,
      pages: null,
    })
    expect(w.req('POST', '/spaces', { name: 'C' }, kai.cookie).status).toBe(201)
    w.req('PATCH', `/users/${kai.id}`, { quota: { spaces: 0 } }, w.root)
    expect(w.req('GET', '/account/usage', undefined, kai.cookie).body.spaces.limit).toBeNull()
    w.req('PATCH', `/users/${kai.id}`, { quota: { spaces: null } }, w.root)
    expect(w.req('GET', '/account/usage', undefined, kai.cookie).body.spaces.limit).toBe(2)
    // The tenant's total of spaces is refused too.
    w.req('PUT', '/admin/settings/quota.spacesPerPerson', { value: 0 }, w.root)
    w.req('PUT', '/admin/tenants/0000000000/limits', { values: { 'storage.quota.spaces': { value: 4 } } }, w.root)
    const total = w.req('POST', '/spaces', { name: 'D' }, kai.cookie)
    expect([total.status, total.body.error, total.body.limit]).toEqual([403, 'quota_exceeded', 4])
  })

  it("--max-spaces is the site's value of the setting, read only", () => {
    const w = world({ maxSpaces: 1 })
    const site = w
      .req('GET', '/admin/settings', undefined, w.root)
      .body.settings.find((s: any) => s.key === 'quota.spacesPerPerson')
    expect(site.site).toMatchObject({ value: 1, fromFlag: true })
    expect(w.req('PUT', '/admin/settings/quota.spacesPerPerson', { value: 5 }, w.root).body.error).toBe('conflict')
    const kai = w.person('kai')
    expect(w.req('POST', '/spaces', { name: 'B' }, kai.cookie).body.error).toBe('quota_exceeded')
  })

  it('warns once per crossing: a notice for the person, over for the administrators, one audit row each', () => {
    const w = world()
    const kai = w.person('kai')
    const sp = w.spaceIdOf(kai.cookie)
    for (const title of ['one', 'two'])
      w.req('POST', `/spaces/${sp}/pages`, { folder: '', meta: { title }, body: 'text' }, kai.cookie)
    w.req('PUT', '/admin/settings/quota.pagesPerPerson', { value: 1 }, w.root)
    w.req('POST', `/admin/spaces/${sp}/size`, undefined, w.root)
    const u = w.req('GET', '/account/usage', undefined, kai.cookie).body
    expect(u.notices.map((n: any) => [n.quota, n.scope, n.limit])).toEqual([['pages', 'person', 1]])
    const over = w.req('GET', '/admin/usage?over=1', undefined, w.root).body.items
    expect(over.map((i: any) => [i.user.id, i.over])).toEqual([[kai.id, ['pages']]])
    w.req('POST', `/admin/spaces/${sp}/size`, undefined, w.root)
    const rows = (w.req('GET', '/admin/audit?kind=quota_exceeded', undefined, w.root).body.items as any[]) ?? []
    expect(rows).toHaveLength(1)
    expect(rows[0].detail).toMatchObject({ quota: 'pages', limit: 1, scope: 'person' })
    // Nothing is refused for going over.
    expect(w.req('POST', '/spaces', { name: 'B' }, kai.cookie).status).toBe(201)
  })
})

describe("the mock usage list and a person's own limits", () => {
  it('lists people by bytes with a cursor, narrows to the ones over a limit, and checks its parameters', () => {
    const w = world()
    const people = ['amy', 'bob', 'cat'].map((n) => w.person(n))
    // b is the largest: it gets a page of text.
    const big = w.spaceIdOf(people[1].cookie)
    w.req(
      'POST',
      `/spaces/${big}/pages`,
      { folder: '', meta: { title: 'x' }, body: 'y'.repeat(5000) },
      people[1].cookie,
    )
    for (const p of people) w.req('POST', `/admin/spaces/${w.spaceIdOf(p.cookie)}/size`, undefined, w.root)
    const first = w.req('GET', '/admin/usage?limit=2', undefined, w.root).body
    expect(first.items[0].user.username).toBe('bob')
    expect(first.items).toHaveLength(2)
    expect(first.next).not.toBe('')
    const second = w.req('GET', `/admin/usage?limit=2&cursor=${encodeURIComponent(first.next)}`, undefined, w.root).body
    const all = [...first.items, ...second.items].map((i: any) => i.user.id)
    expect(new Set(all).size).toBe(all.length)
    expect(second.items.length).toBeGreaterThan(0)
    expect(w.req('GET', '/admin/usage?limit=0', undefined, w.root).status).toBe(400)
    expect(w.req('GET', '/admin/usage?over=yes', undefined, w.root).status).toBe(400)
    expect(w.req('GET', '/admin/usage?cursor=junk', undefined, w.root).status).toBe(400)
    expect(w.req('GET', '/admin/usage?tenant=nope', undefined, w.root).status).toBe(404)
    expect(w.req('GET', '/admin/usage', undefined, people[0].cookie).status).toBe(403)
    expect(first.items[0].own).toEqual({ spaces: null, bytes: null, pages: null })
  })

  it('refuses a bad quota before anything else is changed, and a tenant administrator may not go above the tenant', () => {
    const w = world()
    const kai = w.person('kai')
    const boss = w.person('boss', 'admin')
    w.req('PATCH', `/users/${boss.id}`, { siteAdmin: false }, w.root)
    w.req('PUT', '/admin/settings/quota.bytesPerPerson', { value: 1000 }, w.root)
    for (const bad of [
      'x',
      { disk: 1 },
      { spaces: -1 },
      { spaces: 10001 },
      { bytes: 0 },
      { pages: 1.5 },
      { bytes: '1' },
    ])
      expect(w.req('PATCH', `/users/${kai.id}`, { quota: bad }, w.root).body.error, JSON.stringify(bad)).toBe('invalid')
    const refused = w.req('PATCH', `/users/${kai.id}`, { disabled: true, quota: { bytes: 5000 } }, boss.cookie)
    expect([refused.status, refused.body.error]).toEqual([403, 'forbidden'])
    expect(w.req('GET', '/users', undefined, w.root).body.users.find((u: any) => u.id === kai.id).disabled).toBe(false)
    expect(w.req('PATCH', `/users/${kai.id}`, { quota: { bytes: 500 } }, boss.cookie).body.user.quota.bytes).toBe(500)
    expect(w.req('PATCH', `/users/${kai.id}`, { quota: { bytes: 5000 } }, w.root).body.user.quota.bytes).toBe(5000)
    // Locked above: nobody below changes it.
    w.req('PUT', '/admin/settings/quota.pagesPerPerson', { lower: { mode: 'lock' } }, w.root)
    expect(w.req('PATCH', `/users/${kai.id}`, { quota: { pages: 5 } }, w.root).body.error).toBe('setting_locked')
  })
})

describe('the mock tenant usage and the prefilled invite', () => {
  it('says what a tenant uses and which of its totals it is over', () => {
    const w = world()
    w.person('kai')
    w.req('PUT', '/admin/tenants/0000000000/limits', { values: { 'other.people': { value: 1 } } }, w.root)
    const t = w.req('GET', '/admin/tenants/0000000000', undefined, w.root).body
    expect(t.usage).toMatchObject({ people: 2, spaces: 2, over: ['people'] })
    expect(w.req('GET', '/admin/tenants', undefined, w.root).body.items[0].usage.people).toBe(2)
    expect(w.req('GET', '/admin/audit?kind=quota_exceeded', undefined, w.root).body.items).toHaveLength(1)
  })

  it("makes a real invite for a new tenant's first administrator, which the form that accepts it starts with", () => {
    const w = world()
    w.req('PUT', '/admin/tenancy', { enabled: true }, w.root)
    const made = w.req(
      'POST',
      '/admin/tenants',
      { slug: 'acme', name: 'Acme', admin: { username: 'wu', name: 'Wu Wei', email: 'wu@acme.example' } },
      w.root,
    )
    expect(made.status).toBe(201)
    const inv = made.body.invite
    expect(inv).toMatchObject({
      role: 'admin',
      username: 'wu',
      name: 'Wu Wei',
      email: 'wu@acme.example',
      siteAdmin: false,
    })
    const check = w.req('POST', '/invites/check', { token: inv.token })
    expect(check.body).toMatchObject({ role: 'admin', username: 'wu', name: 'Wu Wei', email: 'wu@acme.example' })
    const accepted = w.req('POST', '/invites/accept', { token: inv.token, password: 'a-long-password' })
    expect(accepted.status).toBe(201)
    expect(accepted.body.user).toMatchObject({
      username: 'wu',
      name: 'Wu Wei',
      email: 'wu@acme.example',
      emailVerified: false,
      role: 'admin',
    })
    expect(
      w.req('POST', '/admin/tenants', { slug: 'bad', name: 'Bad', admin: { username: 'x', email: 'nope' } }, w.root)
        .status,
    ).toBe(400)
    // An ordinary invite fills in nothing.
    const plain = w.req('POST', '/invites', { role: 'member' }, w.root).body
    expect(w.req('POST', '/invites/check', { token: plain.token }).body).toMatchObject({
      username: '',
      name: '',
      email: '',
    })
  })
})
