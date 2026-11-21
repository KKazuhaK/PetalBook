// The API client against the in-memory server, which implements docs/api.md.

import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { api, forbiddenMessage, listParamsToQuery, onForbidden } from './client'
import { mockFetch } from './mock/fetch'
import { createMockServer, type MockServer } from './mock/server'

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
