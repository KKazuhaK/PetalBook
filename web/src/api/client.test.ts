// The API client against the in-memory server, which implements docs/api.md.

import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { api as accountApi, ApiError, forbiddenMessage, isSignedOut, listParamsToQuery, markSignedIn, onForbidden, onSignedOut, spaceApi } from './client'
import { mockFetch } from './mock/fetch'
import { createMockServer, MOCK_ADMIN, type MockServer } from './mock/server'

// Everything of a space, for the default one, next to the endpoints that are not in a space.
const api = { ...accountApi, ...spaceApi('default') }
let server: MockServer
let calls: { method: string; url: string; body?: string; init?: RequestInit }[]
const realFetch = globalThis.fetch
const NOW = Date.parse('2026-11-21T08:30:00Z')

beforeEach(() => {
  server = createMockServer({ now: () => NOW })
  calls = []
  globalThis.fetch = mockFetch(server, { onRequest: (method, url, body, init) => calls.push({ method, url, body, init }) })
})
afterEach(() => {
  globalThis.fetch = realFetch
})

describe('listing query', () => {
  it('builds the query string', () => {
    expect(listParamsToQuery({})).toBe('')
    expect(listParamsToQuery({ folder: '', recursive: true, tag: ['a', 'b c'], q: '栀子', sort: '-title', limit: 20, cursor: 'x' })).toBe(
      '?folder=&recursive=true&tag=a&tag=b+c&q=%E6%A0%80%E5%AD%90&sort=-title&limit=20&cursor=x'.replace('%E6%A0%80%E5%AD%90', encodeURIComponent('栀子')),
    )
  })
})

describe('transport', () => {
  it('sends a JSON content type on every write, none on reads', async () => {
    await api.tags()
    await api.createFolder('x/y')
    await api.reindex()
    const headers = (i: number) => calls[i].init?.headers as Record<string, string>
    expect(headers(0)['Content-Type']).toBeUndefined()
    // Every write says it is JSON, even one with nothing to send.
    expect(headers(1)['Content-Type']).toBe('application/json')
    expect(headers(2)['Content-Type']).toBe('application/json')
    expect(calls[2].body).toBe('{}')
    const [{ id }] = (await api.listPages({ limit: 1 })).items
    await api.deletePage(id)
    expect((calls.at(-1)!.init?.headers as Record<string, string>)['Content-Type']).toBe('application/json')
  })

  it('asks for keepalive only when the body is small enough for the browser to allow', async () => {
    const [{ id }] = (await api.listPages({ limit: 1 })).items
    const p = await api.getPage(id)
    await api.savePage(id, { baseRev: p.rev, meta: p.meta, body: '小\n' }, { keepalive: true })
    expect(calls.at(-1)!.init?.keepalive).toBe(true)
    const big = await api.getPage(id)
    await api.savePage(id, { baseRev: big.rev, meta: big.meta, body: '字'.repeat(30_000) }, { keepalive: true })
    expect(calls.at(-1)!.init?.keepalive).toBeUndefined()
  })

  it('turns an unreachable server into a network error', async () => {
    globalThis.fetch = (() => Promise.reject(new TypeError('Failed to fetch'))) as typeof fetch
    await expect(api.meta()).rejects.toMatchObject({ code: 'network', status: 0 })
  })

  it('turns an HTML answer (proxy with no server) into a network error', async () => {
    globalThis.fetch = (() => Promise.resolve(new Response('<html>bad gateway</html>', { status: 502 }))) as typeof fetch
    await expect(api.meta()).rejects.toMatchObject({ code: 'network' })
  })
})

describe('403 forbidden', () => {
  const refuse = () =>
    (globalThis.fetch = (() =>
      Promise.resolve(new Response(JSON.stringify({ error: 'forbidden', message: 'host not allowed' }), { status: 403, headers: { 'Content-Type': 'application/json' } }))) as typeof fetch)

  it('is mapped to a clear Chinese message that names this page\'s origin', async () => {
    refuse()
    const err = await api.createPage({}).catch((e) => e)
    expect(err.code).toBe('forbidden')
    expect(err.status).toBe(403)
    expect(err.message).toBe(forbiddenMessage(location.origin))
    expect(err.message).toContain(location.origin)
    expect(err.message).toContain('--allow-origin')
    expect(err.message).not.toContain('host not allowed')
  })

  it('reaches every listener, from any request (a read or a save alike)', async () => {
    refuse()
    const seen: string[] = []
    const off = onForbidden((m) => seen.push(m))
    await api.tags().catch(() => undefined)
    await api.savePage('01JD3K8Q7Z', { baseRev: 'r', meta: {}, body: '' }).catch(() => undefined)
    off()
    await api.meta().catch(() => undefined)
    expect(seen).toHaveLength(2)
    expect(seen[0]).toContain(location.origin)
  })

  it('other errors keep the server message', async () => {
    globalThis.fetch = (() => Promise.resolve(new Response(JSON.stringify({ error: 'invalid', message: '不对' }), { status: 400 }))) as typeof fetch
    const err = await api.createPage({}).catch((e) => e)
    expect(err.message).toBe('不对')
  })
})

describe('spaces in URLs', () => {
  it('every space call carries its own space id, encoded', async () => {
    const a = spaceApi('01JD3K8Q7Z')
    await a.tags().catch(() => undefined)
    await a.putDraft('01AAAAAAAA', { baseRev: 'r', meta: {}, body: '', prev: '' }).catch(() => undefined)
    await a.commitPage('01AAAAAAAA', 'v1').catch(() => undefined)
    await a.deleteDraft('01AAAAAAAA').catch(() => undefined)
    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      'GET /api/spaces/01JD3K8Q7Z/tags',
      'PUT /api/spaces/01JD3K8Q7Z/pages/01AAAAAAAA/draft',
      'POST /api/spaces/01JD3K8Q7Z/pages/01AAAAAAAA/commit',
      'DELETE /api/spaces/01JD3K8Q7Z/pages/01AAAAAAAA/draft',
    ])
    await spaceApi('a/b c').tags().catch(() => undefined)
    expect(calls.at(-1)!.url).toBe('/api/spaces/a%2Fb%20c/tags')
  })

  it('a commit and a draft delete carry the JSON content type even with nothing to say', async () => {
    const [{ id }] = (await api.listPages({ limit: 1 })).items
    await spaceApi('default').commitPage(id, 'nope').catch(() => undefined)
    await spaceApi('default').deleteDraft(id)
    for (const c of calls.slice(-2)) expect((c.init?.headers as Record<string, string>)['Content-Type']).toBe('application/json')
  })

  it('a draft store uses keepalive when small, like a save', async () => {
    const [{ id }] = (await api.listPages({ limit: 1 })).items
    const p = await api.getPage(id)
    await api.putDraft(id, { baseRev: p.rev, meta: p.meta, body: '小\n', prev: '' }, { keepalive: true })
    expect(calls.at(-1)!.init?.keepalive).toBe(true)
    expect((await api.getPage(id)).draft?.body).toBe('小\n')
  })

  it('a commit sends its version in the body, a delete with a version puts it in the query, and a 409 carries the draft and the page', async () => {
    const [{ id }] = (await api.listPages({ limit: 1 })).items
    const p = await api.getPage(id)
    const r = await api.putDraft(id, { baseRev: p.rev, meta: p.meta, body: '甲\n', prev: '' })
    expect(JSON.parse(calls.at(-1)!.body!).prev).toBe('')
    const e = await api.commitPage(id, 'wrong').catch((x) => x)
    expect(JSON.parse(calls.at(-1)!.body!)).toEqual({ version: 'wrong' })
    expect(e).toMatchObject({ status: 409, code: 'conflict' })
    expect(e.draft.version).toBe(r.version)
    expect(e.current.id).toBe(id)
    await api.deleteDraft(id, r.version)
    expect(calls.at(-1)!.url).toBe(`/api/spaces/default/pages/${id}/draft?version=${encodeURIComponent(r.version)}`)
    const gone = await api.commitPage(id, r.version).catch((x) => x)
    expect(gone.draft).toBeNull()
  })

  it('sends the session cookie as same-origin credentials', async () => {
    await api.tags()
    expect(calls[0].init?.credentials).toBe('same-origin')
  })
})

describe('being signed out', () => {
  let accounts: MockServer
  beforeEach(() => {
    accounts = createMockServer({ mode: 'accounts', empty: true })
    globalThis.fetch = mockFetch(accounts, { onRequest: (method, url, body, init) => calls.push({ method, url, body, init }) })
    markSignedIn()
  })
  afterEach(() => markSignedIn())

  it('the first 401 tells the app once; several failing requests do not make a storm', async () => {
    let told = 0
    const off = onSignedOut(() => told++)
    const results = await Promise.allSettled([api.tags(), api.tree(), api.listPages(), api.getPage('01AAAAAAAA'), accountApi.account()])
    off()
    expect(results.every((r) => r.status === 'rejected' && r.reason instanceof ApiError && r.reason.status === 401)).toBe(true)
    expect(told).toBe(1)
    expect(isSignedOut()).toBe(true)
  })

  it('until someone signs in, requests that need a session fail on the spot, without reaching the server', async () => {
    await api.tags().catch(() => undefined)
    const before = calls.length
    const e = await api.savePage('01AAAAAAAA', { baseRev: 'r', meta: {}, body: '' }).catch((x) => x)
    expect(e).toMatchObject({ status: 401, code: 'unauthenticated' })
    expect(calls.length).toBe(before)
    // The sign-in calls still go out.
    await accountApi.session()
    await accountApi.login({ username: 'nobody', password: 'x' }).catch(() => undefined)
    expect(calls.length).toBe(before + 2)
  })

  it('a wrong password is an answer to the login, not a lost session', async () => {
    let told = 0
    const off = onSignedOut(() => told++)
    const e = await accountApi.login({ username: 'nobody', password: 'wrong-one' }).catch((x) => x)
    off()
    expect(e).toMatchObject({ status: 401, code: 'unauthenticated' })
    expect(told).toBe(0)
    expect(isSignedOut()).toBe(false)
  })

  it('signing in re-opens the door, and a session that shows a user does too', async () => {
    const withAdmin = createMockServer({ mode: 'accounts', empty: true, admin: MOCK_ADMIN })
    globalThis.fetch = mockFetch(withAdmin)
    await api.tags().catch(() => undefined)
    expect(isSignedOut()).toBe(true)
    await accountApi.login({ username: MOCK_ADMIN.username, password: MOCK_ADMIN.password })
    expect(isSignedOut()).toBe(false)
    const spaceId = (await accountApi.spaces()).spaces[0].id
    expect(await spaceApi(spaceId).tags()).toEqual({ tags: [] })
    await accountApi.logout()
    await spaceApi(spaceId).tags().catch(() => undefined)
    expect(isSignedOut()).toBe(true)
    markSignedIn()
    await accountApi.login({ username: MOCK_ADMIN.username, password: MOCK_ADMIN.password })
    await spaceApi(spaceId).tags().catch(() => undefined)
    expect((await accountApi.session()).user?.username).toBe(MOCK_ADMIN.username)
    expect(isSignedOut()).toBe(false)
  })
})

describe('429 and 403 that are not about the origin', () => {
  it('a 429 carries how long to wait, from Retry-After', async () => {
    globalThis.fetch = (() => Promise.resolve(new Response(JSON.stringify({ error: 'rate_limited', message: '慢一点' }), { status: 429, headers: { 'Retry-After': '42' } }))) as typeof fetch
    const e = await accountApi.login({ username: 'a', password: 'b' }).catch((x) => x)
    expect(e).toMatchObject({ status: 429, code: 'rate_limited', retryAfter: 42, message: '慢一点' })
  })

  it('a 403 on account, user, invite and setup requests keeps the server message and does not raise the origin notice', async () => {
    globalThis.fetch = (() => Promise.resolve(new Response(JSON.stringify({ error: 'forbidden', message: '当前密码不对' }), { status: 403 }))) as typeof fetch
    const seen: string[] = []
    const off = onForbidden((m) => seen.push(m))
    const results = await Promise.all([
      accountApi.changePassword({ current: 'x', new: 'a-long-password' }).catch((x) => x),
      accountApi.users().catch((x) => x),
      accountApi.createInvite().catch((x) => x),
      accountApi.setup({ token: 't', username: 'ab', password: 'a-long-password' }).catch((x) => x),
      // The space limit: the server's message goes to the new-space dialog.
      accountApi.createSpace('第十一个').catch((x) => x),
    ])
    off()
    for (const e of results) expect(e).toMatchObject({ status: 403, code: 'forbidden', message: '当前密码不对' })
    expect(seen).toEqual([])
  })

  it('a 403 on a page request is still the origin notice', async () => {
    globalThis.fetch = (() => Promise.resolve(new Response(JSON.stringify({ error: 'forbidden', message: 'x' }), { status: 403 }))) as typeof fetch
    const seen: string[] = []
    const off = onForbidden((m) => seen.push(m))
    await spaceApi('default').tags().catch(() => undefined)
    off()
    expect(seen).toHaveLength(1)
  })
})
