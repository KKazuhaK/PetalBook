// The API contract (docs/api.md), asserted through the real client and real
// `fetch`. The same suite runs against either implementation:
//
//   npm run test:contract                                  the in-memory mock (empty space)
//   PETALBOOK_API=http://127.0.0.1:18080 npm run test:contract   a real server, started on an EMPTY space
//
// It creates all its own data (under names made unique per run) and never
// assumes anything else is in the space. Where the mock and the server
// differ, the mock is wrong.

import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { api, ApiError } from './client'
import { mockFetch } from './mock/fetch'
import { createMockServer } from './mock/server'
import type { Page, PageSummary } from './types'

declare const process: { env: Record<string, string | undefined> }
const REAL = process.env.PETALBOOK_API?.replace(/\/+$/, '')
const realFetch = globalThis.fetch

beforeAll(() => {
  globalThis.fetch = REAL ? ((input, init) => realFetch(REAL + String(input), init)) : mockFetch(createMockServer({ empty: true }))
})
afterAll(() => {
  globalThis.fetch = realFetch
})

// Everything this run creates lives under this folder / carries this suffix.
const run = Math.random().toString(36).slice(2, 8)
const root = `c${run}`
const JSON_HEADERS = { 'Content-Type': 'application/json' }

const failure = async (p: Promise<unknown>): Promise<ApiError> => {
  try {
    await p
  } catch (e) {
    expect(e).toBeInstanceOf(ApiError)
    return e as ApiError
  }
  throw new Error('expected the request to fail')
}

const mk = (folder: string, meta: Record<string, unknown> = {}, body = '') => api.createPage({ folder, meta, body })
const walk = async (params: Parameters<typeof api.listPages>[0], limit = 3) => {
  const items: PageSummary[] = []
  let cursor = ''
  for (let guard = 0; guard < 200; guard++) {
    const r = await api.listPages({ ...params, limit, cursor: cursor || undefined })
    items.push(...r.items)
    cursor = r.next
    if (!cursor) return items
  }
  throw new Error('cursor never ended')
}
const ids = (items: PageSummary[]) => items.map((i) => i.id)

describe('meta', () => {
  it('reports a version and the default space', async () => {
    const m = await api.meta()
    expect(typeof m.version).toBe('string')
    expect(m.spaces.some((s) => s.id === 'default' && typeof s.name === 'string')).toBe(true)
  })
})

describe('create and read', () => {
  it('creates a titled page: assigned id and created, file named from the title, missing folders made', async () => {
    const p = await mk(`${root}/titled/deep`, { id: 'CLIENTSENT', created: '1999-01-01', title: `暮雪${run}`, subtitle: '副', tags: [`tg${run}`] }, '正文\n')
    expect(p.id).toMatch(/^[0-9A-HJKMNP-TV-Z]{10}$/)
    expect(p.id).not.toBe('CLIENTSENT')
    expect(p.rev).toMatch(/^[0-9a-f]{40}$/)
    expect(p.meta.id).toBe(p.id)
    expect(p.meta.created).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    expect(p.meta.created).not.toBe('1999-01-01')
    expect(p.meta.title).toBe(`暮雪${run}`)
    expect(p.folder).toBe(`${root}/titled/deep`)
    expect(p.path).toBe(`${root}/titled/deep/暮雪${run}.md`)
    expect(p.body).toBe('正文\n')
    const { folders } = await api.tree()
    expect(folders.map((f) => f.path)).toEqual(expect.arrayContaining([`${root}`, `${root}/titled`, `${root}/titled/deep`]))
  })

  it('creates an untitled page: no title key, file named from date and id', async () => {
    const p = await mk(`${root}/untitled`, {}, '没有标题的一句话\n')
    expect('title' in p.meta).toBe(false)
    expect(p.path).toMatch(/\/\d{4}-\d{2}-\d{2}-[^/]+\.md$/)
    const s = (await walk({ folder: `${root}/untitled` })).find((i) => i.id === p.id)!
    expect(s.untitled).toBe(true)
    expect(s.title).toBeUndefined()
    expect(s.label).toBe('没有标题的一句话')
  })

  it('creates with no fields at all, in the root folder', async () => {
    const p = await api.createPage()
    expect(p.folder).toBe('')
    expect(p.body).toBe('')
    expect(p.id).toMatch(/^[0-9A-HJKMNP-TV-Z]{10}$/)
    await api.deletePage(p.id)
  })

  it('an untitled page with an empty body has the empty string as its label', async () => {
    const p = await mk(`${root}/emptylabel`)
    const s = (await walk({ folder: `${root}/emptylabel` }))[0]
    expect(s.id).toBe(p.id)
    expect(s.untitled).toBe(true)
    expect(s.label).toBe('')
  })

  it('cuts an untitled label to about 30 characters', async () => {
    const p = await mk(`${root}/longlabel`, {}, '字'.repeat(80) + '\n')
    const s = (await walk({ folder: `${root}/longlabel` })).find((i) => i.id === p.id)!
    expect(Array.from(s.label).length).toBeGreaterThan(0)
    expect(Array.from(s.label).length).toBeLessThanOrEqual(40)
  })

  it('gets a page, and 404s for one that does not exist', async () => {
    const p = await mk(`${root}/get`, { title: `读取${run}` }, 'x\n')
    expect(await api.getPage(p.id)).toEqual(p)
    const e = await failure(api.getPage('01ZZZZZZZZ'))
    expect(e.status).toBe(404)
    expect(e.code).toBe('not_found')
  })
})

describe('save', () => {
  it('saves with the current rev: new rev, same id, body replaced', async () => {
    const p = await mk(`${root}/save`, { title: `保存${run}` }, '旧\n')
    const saved = await api.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: '新\n' })
    expect(saved.id).toBe(p.id)
    expect(saved.rev).not.toBe(p.rev)
    expect(saved.body).toBe('新\n')
    expect((await api.getPage(p.id)).rev).toBe(saved.rev)
  })

  it('renames the file when the title changes; the id stays', async () => {
    const p = await mk(`${root}/rename`, { title: `旧名${run}` }, 'x\n')
    const saved = await api.savePage(p.id, { baseRev: p.rev, meta: { ...p.meta, title: `新名${run}` }, body: p.body })
    expect(saved.id).toBe(p.id)
    expect(saved.path).toBe(`${root}/rename/新名${run}.md`)
    expect((await api.getPage(p.id)).path).toBe(saved.path)
  })

  it('a stale baseRev is 409 conflict carrying the current page, and nothing is overwritten', async () => {
    const p = await mk(`${root}/conflict`, { title: `冲突${run}` }, '起点\n')
    const first = await api.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: '先到\n' })
    const e = await failure(api.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: '后到\n' }))
    expect(e.status).toBe(409)
    expect(e.code).toBe('conflict')
    expect(e.current?.rev).toBe(first.rev)
    expect(e.current?.body).toBe('先到\n')
    expect((await api.getPage(p.id)).body).toBe('先到\n')
  })

  it('a request without meta, body or baseRev is 400 invalid; an id mismatch too', async () => {
    const p = await mk(`${root}/partial`, { title: `残缺${run}` }, 'x\n')
    const url = `/api/spaces/default/pages/${p.id}`
    const put = (payload: unknown) => fetch(url, { method: 'PUT', headers: JSON_HEADERS, body: JSON.stringify(payload) })
    for (const payload of [{ baseRev: p.rev, meta: p.meta }, { baseRev: p.rev, body: 'y\n' }, { meta: p.meta, body: 'y\n' }, {}]) {
      const r = await put(payload)
      expect(r.status).toBe(400)
      expect(((await r.json()) as { error: string }).error).toBe('invalid')
    }
    const e = await failure(api.savePage(p.id, { baseRev: p.rev, meta: { ...p.meta, id: '01AAAAAAAA' }, body: 'y\n' }))
    expect(e.status).toBe(400)
    expect(e.code).toBe('invalid')
    expect((await api.getPage(p.id)).body).toBe('x\n')
  })

  it('refuses a write that does not say it is JSON', async () => {
    const p = await mk(`${root}/ctype`, { title: `类型${run}` })
    const r = await fetch(`/api/spaces/default/pages/${p.id}`, { method: 'PUT', headers: { 'Content-Type': 'text/plain' }, body: JSON.stringify({ baseRev: p.rev, meta: p.meta, body: 'z\n' }) })
    expect([400, 415]).toContain(r.status)
    expect(((await r.json()) as { error: string }).error).toBe('invalid')
  })

  it('keeps created and id when left out, removes other absent keys, and returns unknown fields exactly', async () => {
    const p = await mk(`${root}/header`, { title: `头部${run}` }, 'x\n')
    const created = p.meta.created
    const weird = { nested: { a: [1, 2, { b: null }] }, 数字: 42, flag: true, 空: '' }
    const a = await api.savePage(p.id, { baseRev: p.rev, meta: { title: `头部${run}`, series: '冬季', ...weird }, body: 'x\n' })
    expect(a.meta.id).toBe(p.id)
    expect(a.meta.created).toBe(created)
    expect(a.meta.series).toBe('冬季')
    for (const [k, v] of Object.entries(weird)) expect(a.meta[k]).toEqual(v)
    // Send back exactly what was served: nothing changes.
    const b = await api.savePage(a.id, { baseRev: a.rev, meta: a.meta, body: a.body })
    expect(b.meta).toEqual(a.meta)
    // A key left out is removed; id and created stay.
    const { series: _series, ...without } = a.meta
    void _series
    const c = await api.savePage(b.id, { baseRev: b.rev, meta: without, body: b.body })
    expect('series' in c.meta).toBe(false)
    expect(c.meta.created).toBe(created)
    expect(c.meta.id).toBe(p.id)
    expect(c.meta.nested).toEqual(weird.nested)
  })

  it('a title added later and removed again leaves the page untitled', async () => {
    const p = await mk(`${root}/retitle`, {}, '一句话\n')
    const a = await api.savePage(p.id, { baseRev: p.rev, meta: { ...p.meta, title: `加了${run}` }, body: p.body })
    expect(a.meta.title).toBe(`加了${run}`)
    const { title: _t, ...rest } = a.meta
    void _t
    const b = await api.savePage(a.id, { baseRev: a.rev, meta: rest, body: a.body })
    expect('title' in b.meta).toBe(false)
    const s = (await walk({ folder: `${root}/retitle` })).find((i) => i.id === p.id)!
    expect(s.untitled).toBe(true)
  })
})

describe('move and delete', () => {
  it('moves a page, creating the folder; id and content stay', async () => {
    const p = await mk(`${root}/from`, { title: `搬家${run}` }, '内容\n')
    const moved = await api.movePage(p.id, `${root}/to/new`)
    expect(moved.id).toBe(p.id)
    expect(moved.folder).toBe(`${root}/to/new`)
    expect(moved.path).toBe(`${root}/to/new/搬家${run}.md`)
    expect(moved.body).toBe('内容\n')
    expect((await api.getPage(p.id)).path).toBe(moved.path)
    const tree = Object.fromEntries((await api.tree()).folders.map((f) => [f.path, f.pages]))
    expect(tree[`${root}/to/new`]).toBe(1)
    expect(tree[`${root}/from`]).toBe(0)
  })

  it('moves to the root folder', async () => {
    const p = await mk(`${root}/toroot`, { title: `回根${run}` })
    const moved = await api.movePage(p.id, '')
    expect(moved.folder).toBe('')
    await api.deletePage(p.id)
  })

  it('refuses a bad destination with 400 invalid', async () => {
    const p = await mk(`${root}/badmove`, { title: `坏路径${run}` })
    for (const folder of ['.hidden', 'a/..', '/abs', 'trail/', 'assets']) {
      const e = await failure(api.movePage(p.id, folder))
      expect(e.status, folder).toBe(400)
      expect(e.code).toBe('invalid')
    }
    expect((await api.getPage(p.id)).folder).toBe(`${root}/badmove`)
  })

  it('deletes with 204, then the page is gone', async () => {
    const p = await mk(`${root}/del`, { title: `删除${run}` })
    expect(await api.deletePage(p.id)).toBeUndefined()
    expect((await failure(api.getPage(p.id))).code).toBe('not_found')
    expect((await failure(api.deletePage(p.id))).status).toBe(404)
    expect((await walk({ folder: `${root}/del` })).length).toBe(0)
  })
})

describe('folders and tree', () => {
  const bad = ['.hidden', 'a/.b', '..', 'a/..', 'name.', 'name ', 'a/name.', 'assets', 'assets/inside', '长'.repeat(86), 'a//b', '/lead', 'trail/']

  it('lists empty folders and counts only pages directly inside', async () => {
    await api.createFolder(`${root}/tree/empty`)
    await mk(`${root}/tree`, { title: `树一${run}` })
    await mk(`${root}/tree/sub`, { title: `树二${run}` })
    await mk(`${root}/tree/sub`, { title: `树三${run}` })
    const tree = Object.fromEntries((await api.tree()).folders.map((f) => [f.path, f.pages]))
    expect(tree['']).toBeGreaterThanOrEqual(0)
    expect(tree[`${root}/tree/empty`]).toBe(0)
    expect(tree[`${root}/tree`]).toBe(1)
    expect(tree[`${root}/tree/sub`]).toBe(2)
  })

  it('creates a folder, 409 exists the second time, and 409 exists for the root', async () => {
    expect(await api.createFolder(`${root}/mkdir/a`)).toEqual({ path: `${root}/mkdir/a` })
    const again = await failure(api.createFolder(`${root}/mkdir/a`))
    expect([again.status, again.code]).toEqual([409, 'exists'])
    const rootErr = await failure(api.createFolder(''))
    expect([rootErr.status, rootErr.code]).toEqual([409, 'exists'])
  })

  it('refuses folder names the server does not allow, with 400 invalid', async () => {
    for (const name of bad) {
      const path = name.startsWith('/') || name.startsWith('.') || ['assets', 'assets/inside'].includes(name) || name === '..' || name === 'a/..' || name === 'a//b' ? name : `${root}/${name}`
      const e = await failure(api.createFolder(path))
      expect(e.status, name).toBe(400)
      expect(e.code, name).toBe('invalid')
    }
    const e = await failure(api.createPage({ folder: '.hidden', body: '' }))
    expect([e.status, e.code]).toEqual([400, 'invalid'])
  })

  it('accepts a name that merely contains a dot or the word assets below the root', async () => {
    expect(await api.createFolder(`${root}/v1.2`)).toEqual({ path: `${root}/v1.2` })
    expect(await api.createFolder(`${root}/assets`)).toEqual({ path: `${root}/assets` })
  })
})

describe('tags', () => {
  it('counts pages per tag, ordered by count descending', async () => {
    const a = `a${run}`
    const b = `b${run}`
    await mk(`${root}/tags`, { title: `标签一${run}`, tags: [a, b] })
    await mk(`${root}/tags`, { title: `标签二${run}`, tags: [a] })
    await mk(`${root}/tags`, { title: `标签三${run}`, tags: [a] })
    const { tags } = await api.tags()
    const count = (t: string) => tags.find((x) => x.tag === t)?.pages
    expect(count(a)).toBe(3)
    expect(count(b)).toBe(1)
    for (let i = 1; i < tags.length; i++) expect(tags[i - 1].pages).toBeGreaterThanOrEqual(tags[i].pages)
  })

  it('a tag change on save moves the counts', async () => {
    const t = `m${run}`
    const p = await mk(`${root}/tagmove`, { title: `改标签${run}`, tags: [t] })
    expect((await api.tags()).tags.find((x) => x.tag === t)?.pages).toBe(1)
    await api.savePage(p.id, { baseRev: p.rev, meta: { ...p.meta, tags: [] }, body: p.body })
    expect((await api.tags()).tags.find((x) => x.tag === t)).toBeUndefined()
  })
})

describe('listing', () => {
  const folder = `${root}/list`
  let created: Page[] = []

  beforeAll(async () => {
    created = []
    for (let i = 0; i < 7; i++) created.push(await mk(folder, { title: `列${String(i).padStart(2, '0')}${run}`, tags: i % 2 ? [`odd${run}`, `all${run}`] : [`all${run}`] }, `正文${i}\n`))
    await mk(`${folder}/inner`, { title: `内层${run}`, tags: [`all${run}`] })
  })

  it('walks every page with the cursor, to an empty next, without repeats', async () => {
    const items = await walk({ folder }, 3)
    expect(items).toHaveLength(7)
    expect(new Set(ids(items)).size).toBe(7)
    expect(ids(items).sort()).toEqual(created.map((p) => p.id).sort())
    const last = await api.listPages({ folder, limit: 200 })
    expect(last.next).toBe('')
  })

  it('a page of exactly the limit may still have more; the walk ends cleanly', async () => {
    const first = await api.listPages({ folder, limit: 7 })
    expect(first.items).toHaveLength(7)
    if (first.next) expect((await api.listPages({ folder, limit: 7, cursor: first.next })).items).toHaveLength(0)
  })

  it('folder is direct children only unless recursive', async () => {
    expect((await walk({ folder })).every((p) => p.folder === folder)).toBe(true)
    const deep = await walk({ folder, recursive: true })
    expect(deep).toHaveLength(8)
    expect(deep.every((p) => p.folder === folder || p.folder.startsWith(folder + '/'))).toBe(true)
  })

  it('the root folder is listed on its own, and recursive from the root includes everything', async () => {
    const rootPage = await mk('', { title: `根页${run}` })
    expect((await walk({ folder: '' })).some((p) => p.id === rootPage.id)).toBe(true)
    expect((await walk({ folder: '' })).every((p) => p.folder === '')).toBe(true)
    expect((await walk({ folder: '', recursive: true })).some((p) => p.id === created[0].id)).toBe(true)
    await api.deletePage(rootPage.id)
  })

  it('tag filters combine with AND', async () => {
    expect((await walk({ tag: [`all${run}`] }))).toHaveLength(8)
    const both = await walk({ tag: [`all${run}`, `odd${run}`] })
    expect(both).toHaveLength(3)
    expect(both.every((p) => p.tags?.includes(`odd${run}`))).toBe(true)
    expect(await walk({ tag: [`all${run}`, `nope${run}`] })).toEqual([])
  })

  it('sorts by title in both directions, by code point and ignoring case', async () => {
    const f = `${root}/titlesort`
    for (const title of [`beta${run}`, `Alpha${run}`, `alpine${run}`, `中文${run}`]) await mk(f, { title })
    await mk(f, {}, '无标题甲\n')
    await mk(f, {}, '无标题乙\n')
    const asc = await walk({ folder: f, sort: 'title' })
    expect(asc.map((p) => p.untitled)).toEqual([false, false, false, false, true, true])
    expect(asc.slice(0, 4).map((p) => p.title)).toEqual([`Alpha${run}`, `alpine${run}`, `beta${run}`, `中文${run}`])
    const desc = await walk({ folder: f, sort: '-title' })
    expect(desc.map((p) => p.untitled)).toEqual([false, false, false, false, true, true])
    expect(desc.slice(0, 4).map((p) => p.title)).toEqual([`中文${run}`, `beta${run}`, `alpine${run}`, `Alpha${run}`])
  })

  it('sorts by updated and created, newest first by default', async () => {
    const items = await walk({ folder })
    for (let i = 1; i < items.length; i++) expect(items[i - 1].updated >= items[i].updated).toBe(true)
    const asc = await walk({ folder, sort: 'updated' })
    for (let i = 1; i < asc.length; i++) expect(asc[i - 1].updated <= asc[i].updated).toBe(true)
    const byCreated = await walk({ folder, sort: '-created' })
    for (let i = 1; i < byCreated.length; i++) expect((byCreated[i - 1].created ?? '') >= (byCreated[i].created ?? '')).toBe(true)
    expect(ids(await walk({ folder, sort: 'created' })).sort()).toEqual(ids(items).sort())
  })

  it('summaries carry the documented fields', async () => {
    const s = (await walk({ folder })).find((p) => p.id === created[1].id)!
    expect(s).toMatchObject({ id: created[1].id, folder, untitled: false, title: `列01${run}`, label: `列01${run}`, tags: [`odd${run}`, `all${run}`] })
    expect(s.path).toBe(`${folder}/列01${run}.md`)
    expect(s.updated).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/)
    expect(s.created).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    expect(s.excerpt).toContain('正文1')
  })
})

describe('search', () => {
  const f = `${root}/search`
  const latin = `zephyr${run}`
  let a: Page, b: Page, c: Page, d: Page

  beforeAll(async () => {
    a = await mk(f, { title: `夏夜${run}`, subtitle: '萤火' }, '蕨类植物在潮湿的墙角生长。\n')
    b = await mk(f, {}, `The ${latin} wind came in from the west.\n`)
    c = await mk(f, {}, '蕨类，植物\n')
    d = await mk(f, {}, '蕨类\n植物\n')
  })

  const hits = async (q: string) => ids(await walk({ q, folder: f, recursive: true }))

  it('finds a one-character Chinese term', async () => {
    const r = await hits('蕨')
    expect(r).toEqual(expect.arrayContaining([a.id, c.id, d.id]))
    expect(r).not.toContain(b.id)
  })

  it('finds a two-character Chinese term, only as adjacent characters', async () => {
    const r = await hits('类植')
    expect(r).toContain(a.id)
    // Not across punctuation or a line break.
    expect(r).not.toContain(c.id)
    expect(r).not.toContain(d.id)
    expect(await hits('蕨物')).toEqual([])
  })

  it('finds a Latin word, ignoring case', async () => {
    expect(await hits(latin)).toEqual([b.id])
    expect(await hits(latin.toUpperCase())).toEqual([b.id])
  })

  it('searches the title and the subtitle too', async () => {
    expect(await hits(`夏夜${run}`)).toEqual([a.id])
    expect(await hits('萤火')).toEqual([a.id])
  })

  it('terms separated by spaces must all match', async () => {
    expect(await hits(`蕨类 ${run}`)).toEqual([a.id])
    expect(await hits(`${latin} 蕨类`)).toEqual([])
  })

  it('finds nothing for a term that is not there', async () => {
    expect(await hits(`没有这个词${run}`)).toEqual([])
  })

  it('can be sorted explicitly and combined with a tag filter', async () => {
    const r = await walk({ q: '蕨', folder: f, recursive: true, sort: 'title' })
    expect(r[r.length - 1].untitled).toBe(true)
  })
})

describe('reindex', () => {
  it('rebuilds the index and answers with counts; nothing is lost', async () => {
    const p = await mk(`${root}/reindex`, { title: `重建${run}` }, '重建前\n')
    const r = await api.reindex()
    expect(r.pages).toBeGreaterThanOrEqual(1)
    expect(typeof r.ms).toBe('number')
    expect((await walk({ folder: `${root}/reindex` })).map((i) => i.id)).toEqual([p.id])
    expect((await api.getPage(p.id)).body).toBe('重建前\n')
  })
})
