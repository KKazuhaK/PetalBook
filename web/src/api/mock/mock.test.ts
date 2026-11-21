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
