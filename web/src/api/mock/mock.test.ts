// What only the mock promises: its seed data and its test hooks. The contract
// itself is tested in ../contract.test.ts.
import { describe, expect, it } from 'vitest'
import { createMockServer } from './server'

const call = (s: ReturnType<typeof createMockServer>, method: string, url: string, body?: unknown) =>
  s.handle(method, url, body === undefined ? undefined : JSON.stringify(body), method === 'GET' ? null : 'application/json')

describe('seed', () => {
  it('has about thirty pages in several folders, some untitled, some with subtitles and tags', () => {
    const s = createMockServer()
    const { items } = call(s, 'GET', '/api/spaces/default/pages?limit=200').body as { items: { untitled: boolean; subtitle?: string; tags?: string[]; folder: string }[] }
    expect(items.length).toBeGreaterThanOrEqual(30)
    expect(items.some((i) => i.untitled)).toBe(true)
    expect(items.some((i) => i.subtitle)).toBe(true)
    expect(items.some((i) => i.tags?.length)).toBe(true)
    expect(new Set(items.map((i) => i.folder)).size).toBeGreaterThanOrEqual(5)
  })

  it('starts empty on request', () => {
    const s = createMockServer({ empty: true })
    expect((call(s, 'GET', '/api/spaces/default/pages').body as { items: unknown[] }).items).toEqual([])
  })
})

describe('header merge rule', () => {
  it('existing keys keep their place, new keys are appended, absent keys go, id and created stay', () => {
    const s = createMockServer({ empty: true })
    const p = call(s, 'POST', '/api/spaces/default/pages', { meta: { title: 'T', a: 1, b: 2 } }).body as { id: string; rev: string; meta: Record<string, unknown> }
    const keys = Object.keys(p.meta)
    const put = call(s, 'PUT', `/api/spaces/default/pages/${p.id}`, { baseRev: p.rev, meta: { z: 9, b: 20, title: 'T2' }, body: '' }).body as { meta: Record<string, unknown> }
    // Survivors in the file's order, then the new key.
    expect(Object.keys(put.meta)).toEqual([...keys.filter((k) => ['id', 'created', 'title', 'b'].includes(k)), 'z'])
    expect(put.meta.b).toBe(20)
    expect(put.meta.created).toBe(p.meta.created)
  })

  it('takes header values as they come, with no type checks', () => {
    const s = createMockServer({ empty: true })
    const p = call(s, 'POST', '/api/spaces/default/pages', {}).body as { id: string; rev: string }
    const r = call(s, 'PUT', `/api/spaces/default/pages/${p.id}`, { baseRev: p.rev, meta: { title: 5, tags: 'not-a-list', x: null }, body: '' })
    expect(r.status).toBe(200)
    expect((r.body as { meta: Record<string, unknown> }).meta).toMatchObject({ title: 5, tags: 'not-a-list', x: null })
  })
})

describe('writes', () => {
  it('need a JSON content type', () => {
    const s = createMockServer({ empty: true })
    for (const ct of [null, 'text/plain', 'application/x-www-form-urlencoded']) {
      const r = s.handle('POST', '/api/spaces/default/pages', '{}', ct)
      expect(r.status).toBe(415)
      expect((r.body as { error: string }).error).toBe('invalid')
    }
    expect(s.handle('POST', '/api/spaces/default/pages', '{}', 'application/json; charset=utf-8').status).toBe(201)
  })
})

describe('accounts mode', () => {
  const cookieOf = (r: { headers?: Record<string, string> }) => r.headers?.['Set-Cookie']?.split(';')[0]

  it('seeds the administrator, whose personal space holds the sample pages', () => {
    const s = createMockServer({ mode: 'accounts' })
    const login = call(s, 'POST', '/api/login', { username: 'admin', password: 'petalbook-admin' })
    expect(login.status).toBe(200)
    const cookie = cookieOf(login)!
    const handle = (method: string, url: string, body?: unknown) => s.handle(method, url, body === undefined ? undefined : JSON.stringify(body), method === 'GET' ? null : 'application/json', cookie)
    const { spaces } = handle('GET', '/api/spaces').body as { spaces: { id: string; personal: boolean }[] }
    expect(spaces).toHaveLength(1)
    const { items } = handle('GET', `/api/spaces/${spaces[0].id}/pages?limit=200`).body as { items: unknown[] }
    expect(items.length).toBeGreaterThanOrEqual(30)
  })

  it('without a user it is the first-run state, and the invite option makes a usable invite', () => {
    const s = createMockServer({ mode: 'accounts', admin: false, invite: { token: 'tok123' } })
    expect((call(s, 'GET', '/api/session').body as { setup: boolean }).setup).toBe(true)
    // An invite needs someone to have made it; with no user there is none to list it, but the token is good.
    expect(call(s, 'POST', '/api/invites/check', { token: 'tok123' }).body).toMatchObject({ role: 'member' })
    expect(call(s, 'POST', '/api/invites/check', { token: 'nope' }).status).toBe(404)
  })

  it('single-user mode is the default: no sign-in, the account endpoints are not there', () => {
    const s = createMockServer()
    expect((call(s, 'GET', '/api/session').body as { mode: string; user: { id: string } }).user.id).toBe('local')
    expect(call(s, 'GET', '/api/account').status).toBe(404)
  })

  it('the cookie stand-in is HttpOnly and SameSite=Lax', () => {
    const s = createMockServer({ mode: 'accounts' })
    const r = call(s, 'POST', '/api/login', { username: 'admin', password: 'petalbook-admin' })
    expect(r.headers?.['Set-Cookie']).toMatch(/HttpOnly; SameSite=Lax/)
  })
})

describe('space limit', () => {
  it('POST /api/spaces is 403 with a message once the person owns the maximum', () => {
    const s = createMockServer({ mode: 'accounts', empty: true, maxSpaces: 2 })
    const login = call(s, 'POST', '/api/login', { username: 'admin', password: 'petalbook-admin' })
    const cookie = login.headers!['Set-Cookie'].split(';')[0]
    const post = () => s.handle('POST', '/api/spaces', JSON.stringify({ name: 'x' }), 'application/json', cookie)
    expect(post().status).toBe(201)
    const r = post()
    expect(r.status).toBe(403)
    expect(r.body).toMatchObject({ error: 'forbidden' })
  })
})

describe('draft rules the real server shares', () => {
  const NOW = Date.parse('2026-11-21T08:30:00Z')
  const setup = () => {
    let clock = NOW
    const s = createMockServer({ now: () => clock })
    const id = (call(s, 'GET', '/api/spaces/default/pages?limit=1').body as { items: { id: string }[] }).items[0].id
    const page = call(s, 'GET', `/api/spaces/default/pages/${id}`).body as { rev: string; meta: object; body: string }
    const put = (baseRev: string, body: string, prev: string) => call(s, 'PUT', `/api/spaces/default/pages/${id}/draft`, { baseRev, meta: page.meta, body, prev }).body as { version: string }
    return { s, id, page, put, advance: (ms: number) => (clock += ms) }
  }

  it('storing on a new base restarts the max-age count (a draft the file has left starts afresh)', () => {
    const { s, id, page, put, advance } = setup()
    const body = () => (call(s, 'GET', `/api/spaces/default/pages/${id}`).body as { body: string }).body
    let v = put(page.rev, 'a\n', '').version // t = 0, on the base the file has
    advance(50_000)
    call(s, 'POST', `/api/_mock/touch/${id}`, { body: 'elsewhere\n' })
    const newBase = (call(s, 'GET', `/api/spaces/default/pages/${id}`).body as { rev: string }).rev
    advance(50_000)
    // t = 100 s: stored on the new base, then again every 100 s (never quiet for 2 minutes).
    for (let t = 100; t <= 500; t += 100) {
      v = put(newBase, `b${t}\n`, v).version
      advance(100_000)
    }
    // t = 600 s: 600 s after the first store, but only 500 s after the first on the new base.
    expect(body()).toBe('elsewhere\n')
  })
})
