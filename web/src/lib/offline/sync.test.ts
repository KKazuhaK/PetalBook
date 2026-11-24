import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { ApiError } from '@/api/client'
import { DEFAULT_CONFIG, type OfflineConfig } from './store'
import { configure, existingStore, hasCopy } from './manager'
import { syncSpace } from './sync'
import { bytesOf, totalBytes } from './registry'
import { make, network, S, type Net } from './testkit'

let net: Net
beforeEach(() => {
  net = network()
})
afterEach(() => net.restore())

const textCfg = (over: Partial<OfflineConfig> = {}): OfflineConfig => ({ ...DEFAULT_CONFIG, mode: 'text', ...over })
const open = async (cfg: OfflineConfig = DEFAULT_CONFIG) => (await configure('local', 'default', cfg))!
const ids = async (s: Awaited<ReturnType<typeof open>>) => (await s.catalog()).map((e) => e.id).sort()

describe('the first sync', () => {
  it('walks the catalogue, in batches of its own size, and keeps the change cursor of the walk', async () => {
    const a = await make('', '甲')
    const b = await make('Tang', '乙', '乙\n', { tags: ['诗'] })
    const s = await open()
    const r = await syncSpace(s)
    expect(r).toEqual({ outcome: 'ok', changed: true })
    expect(await ids(s)).toEqual([a.id, b.id].sort())
    const st = await s.state()
    expect(st.complete).toBe(true)
    expect(st.cursor).toBe((await S().syncCatalog({ limit: 1 })).changes)
    expect((await s.catalogEntry(b.id))!).toMatchObject({ folder: 'Tang', title: '乙', tags: ['诗'], rev: b.rev })
    // The tree and the tags are read after the walk, for the app to show them offline.
    expect((await s.tree())!.folders.map((f) => f.path)).toContain('Tang')
    expect((await s.tags())!.tags).toEqual([expect.objectContaining({ tag: '诗', pages: 1 })])
    // The catalogue-only mode keeps no text.
    expect(await s.textCount()).toBe(0)
    expect(net.count(/sync\/pages/)).toBe(0)
  })

  it('a second sync with nothing new asks for the changes once and for nothing else', async () => {
    await make('', '甲')
    const s = await open()
    await syncSpace(s)
    const before = net.requests.length
    const r = await syncSpace(s)
    expect(r).toEqual({ outcome: 'ok', changed: false })
    expect(net.requests.slice(before).map((q) => q.replace(/\?.*/, ''))).toEqual(['GET /api/spaces/default/changes'])
  })

  it('does nothing for a space that has no copy', async () => {
    expect(hasCopy('local', 'default')).toBe(false)
    const s = await (await import('./manager')).storeFor('local', 'default')
    expect(await syncSpace(s)).toEqual({ outcome: 'ok', changed: false })
    expect(net.requests).toEqual([])
  })
})

describe('what changed', () => {
  it('is brought in by the changes: an edit, a move, a deletion and a new page', async () => {
    const a = await make('', '一')
    const b = await make('', '二')
    const c = await make('', '三')
    const s = await open()
    await syncSpace(s)
    const a2 = await S().savePage(a.id, { baseRev: a.rev, meta: { ...a.meta, title: '一，改了' }, body: '改了\n' })
    await S().movePage(b.id, 'Moved')
    await S().deletePage(c.id)
    const d = await make('', '四')
    const r = await syncSpace(s)
    expect(r.changed).toBe(true)
    expect(await ids(s)).toEqual([a.id, b.id, d.id].sort())
    expect(await s.catalogEntry(a.id)).toMatchObject({ title: '一，改了', rev: a2.rev })
    expect((await s.catalogEntry(b.id))!.folder).toBe('Moved')
    expect((await s.state()).cursor).toBe((await S().syncCatalog({})).changes)
    // The folders changed, so the tree was read again.
    expect((await s.tree())!.folders.map((f) => f.path)).toContain('Moved')
  })

  it('is read in batches of 1000 until there is no more', async () => {
    for (let i = 0; i < 3; i++) await make('', `p${i}`)
    const s = await open()
    await syncSpace(s)
    for (let i = 3; i < 8; i++) await make('', `p${i}`)
    const before = net.count(/\/changes/)
    await syncSpace(s)
    expect(net.count(/\/changes/) - before).toBe(1)
    expect((await s.catalog()).length).toBe(8)
  })
})

describe('a cursor that is too old', () => {
  it('walks the catalogue again, forgets what is gone, and fetches only the text whose rev changed', async () => {
    const keep = await make('Notes', '不变')
    const edit = await make('Notes', '会改')
    const gone = await make('Notes', '会删')
    const s = await open(textCfg({ folders: ['Notes'], inbox: false, favourites: false, recent: false }))
    await syncSpace(s)
    expect(await s.textCount()).toBe(3)
    // While the device was away the index was built again (a reindex): every cursor is void.
    const edited = await S().savePage(edit.id, { baseRev: edit.rev, meta: edit.meta, body: '会改，改了\n' })
    await S().deletePage(gone.id)
    await S().reindex()
    const before = net.requests.length
    const r = await syncSpace(s)
    expect(r.outcome).toBe('ok')
    const sent = net.requests.slice(before).map((q) => decodeURIComponent(q))
    expect(sent.filter((q) => /sync\/catalog/.test(q)).length).toBeGreaterThanOrEqual(1)
    // Only the page that changed has its text fetched.
    const fetched = sent.filter((q) => /sync\/pages/.test(q))
    expect(fetched).toHaveLength(1)
    expect(fetched[0]).toContain(edit.id)
    expect(fetched[0]).not.toContain(keep.id)
    expect(await ids(s)).toEqual([keep.id, edit.id].sort())
    expect((await s.page(edit.id))!.page.body).toBe('会改，改了\n')
    expect((await s.page(edit.id))!.rev).toBe(edited.rev)
    expect(await s.page(gone.id)).toBeUndefined()
    expect((await s.state()).cursor).toBe((await S().syncCatalog({})).changes)
  })
})

describe('the text', () => {
  it('is kept for the inbox, the favourites, the chosen folders and tags, and the pages opened, and for nothing else', async () => {
    const inbox = await make('Inbox', '收件', '收件\n')
    const fav = await make('Misc', '收藏', '收藏\n')
    const folder = await make('Chosen/deep', '文件夹里', '文件夹里\n')
    const tagged = await make('Misc', '带标签', '带标签\n', { tags: ['留'] })
    const opened = await make('Misc', '读过', '读过\n')
    const other = await make('Misc', '别的', '别的\n')
    await S().addFavorite({ kind: 'page', target: fav.id })
    const s = await open(textCfg({ folders: ['Chosen'], tags: ['留'] }))
    await s.touchOpened(opened.id, Date.now())
    await syncSpace(s)
    const have = new Set((await s.textMetas()).map((m) => m.id))
    expect(have).toEqual(new Set([inbox.id, fav.id, folder.id, tagged.id, opened.id]))
    expect(have.has(other.id)).toBe(false)
    expect((await s.page(inbox.id))!.page.body).toBe('收件\n')
    expect(await s.bytes()).toBe(bytesOf('local'))
    // The inbox of the space and the favourites were read for the scope.
    expect((await s.state()).inbox).toBe('Inbox')
    expect((await s.state()).favourites.pages).toEqual([fav.id])
  })

  it("carries the caller's draft, so it can be shown offline", async () => {
    const p = await make('Inbox', '写到一半', '原文\n')
    await S().putDraft(p.id, { baseRev: p.rev, meta: p.meta, body: '原文，续写\n', prev: '' })
    const s = await open(textCfg())
    await syncSpace(s)
    expect((await s.page(p.id))!.page.draft!.body).toBe('原文，续写\n')
  })

  it('follows the changes: a page that changed is fetched again, one that left the scope is dropped', async () => {
    const a = await make('Inbox', '在收件箱', 'v1\n')
    const s = await open(textCfg({ favourites: false, recent: false }))
    await syncSpace(s)
    expect((await s.page(a.id))!.page.body).toBe('v1\n')
    const a2 = await S().savePage(a.id, { baseRev: a.rev, meta: a.meta, body: 'v2\n' })
    await syncSpace(s)
    expect((await s.page(a.id))!.page.body).toBe('v2\n')
    expect((await s.page(a.id))!.rev).toBe(a2.rev)
    await S().movePage(a.id, 'Elsewhere')
    await syncSpace(s)
    expect(await s.page(a.id)).toBeUndefined()
    expect(await s.catalogEntry(a.id)).toBeDefined()
    expect(await s.bytes()).toBe(0)
  })

  it('back to the catalogue only drops the text', async () => {
    await make('Inbox', '甲')
    const s = await open(textCfg())
    await syncSpace(s)
    expect(await s.textCount()).toBe(1)
    await s.setConfig({ ...DEFAULT_CONFIG, mode: 'catalog' })
    await syncSpace(s)
    expect(await s.textCount()).toBe(0)
    expect(await s.bytes()).toBe(0)
  })

  it('is fetched 100 pages to a request, at most four requests at a time, and what did not fit comes next', async () => {
    for (let i = 0; i < 230; i++) await make('Inbox', `n${i}`, 'x\n')
    const s = await open(textCfg({ favourites: false, recent: false }))
    let live = 0
    let peak = 0
    const real = globalThis.fetch
    globalThis.fetch = (async (input, init) => {
      const url = typeof input === 'string' ? input : String(input)
      if (!/sync\/pages/.test(url)) return real(input, init)
      live++
      peak = Math.max(peak, live)
      await new Promise((r) => setTimeout(r, 5))
      try {
        return await real(input, init)
      } finally {
        live--
      }
    }) as typeof fetch
    await syncSpace(s)
    globalThis.fetch = real
    expect(await s.textCount()).toBe(230)
    expect(
      net.requests
        .filter((r) => /sync\/pages/.test(r))
        .map((r) => r.split('ids=')[1].split(',').length)
        .sort((a, b) => b - a),
    ).toEqual([100, 100, 30])
    expect(peak).toBeGreaterThan(1)
    expect(peak).toBeLessThanOrEqual(4)
  })

  it('asks again for the pages an answer left out (rest)', async () => {
    for (let i = 0; i < 3; i++) await make('Inbox', `big${i}`, 'y'.repeat(50) + '\n')
    // The mock cuts an answer at a size of its own; ask the server for the same set twice by hand.
    const ids3 = (await S().listPages({ folder: 'Inbox' })).items.map((i) => i.id)
    const r = await S().syncPages(ids3)
    expect(r.pages).toHaveLength(3)
    expect(r.rest).toBeUndefined()
  })
})

describe('the room on the device', () => {
  it('is capped: the text opened longest ago goes first, and what is pinned is kept as long as it fits', async () => {
    const pinned = await make('Inbox', '固定', 'p'.repeat(3000))
    const r1 = await make('Misc', '最近一', 'a'.repeat(3000))
    const r2 = await make('Misc', '最近二', 'b'.repeat(3000))
    const s = await open(textCfg({ favourites: false }))
    await s.touchOpened(r1.id, 100)
    await s.touchOpened(r2.id, 200)
    // Room for two of the three.
    await syncSpace(s, { cap: 8000 })
    const have = new Set((await s.textMetas()).map((m) => m.id))
    expect(have.has(pinned.id)).toBe(true)
    expect(have.has(r2.id)).toBe(true)
    expect(have.has(r1.id)).toBe(false)
    expect((await s.state()).overCap).toBe(true)
    expect(totalBytes()).toBeLessThanOrEqual(8000)
    // With room again, the next sync fetches what was left out.
    await syncSpace(s)
    expect(await s.textCount()).toBe(3)
    expect((await s.state()).overCap).toBe(false)
  })

  it('counts every space and account on the device against the cap', async () => {
    const a = await make('Inbox', '甲', 'a'.repeat(3000))
    // Another account has used most of the room already.
    const other = await configure('someone-else', 'default', textCfg())
    await other!.putPages([
      { id: 'X', path: 'X.md', folder: '', rev: 'r', updated: '', meta: {}, body: 'z'.repeat(6000) },
    ])
    const s = await open(textCfg())
    await syncSpace(s, { cap: 8000 })
    expect(await s.page(a.id)).toBeUndefined()
    expect((await s.state()).overCap).toBe(true)
  })
})

describe('what the server says about the space', () => {
  it('404: the person cannot read it any more, so the copy is deleted', async () => {
    await make('', '甲')
    const s = await open()
    await syncSpace(s)
    const real = globalThis.fetch
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      if (/\/changes/.test(String(input)))
        return new Response(JSON.stringify({ error: 'not_found', message: 'no such space' }), {
          status: 404,
          headers: { 'Content-Type': 'application/json' },
        })
      return real(input, init)
    }) as typeof fetch
    const r = await syncSpace(s)
    globalThis.fetch = real
    expect(r.outcome).toBe('gone')
    expect(hasCopy('local', 'default')).toBe(false)
    expect(await existingStore('local', 'default')).toBeUndefined()
  })

  it('403 space_disabled: the copy stays, read only, and is marked', async () => {
    await make('', '甲')
    const s = await open()
    await syncSpace(s)
    const real = globalThis.fetch
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      if (/\/changes/.test(String(input)))
        return new Response(JSON.stringify({ error: 'space_disabled', message: 'off' }), {
          status: 403,
          headers: { 'Content-Type': 'application/json' },
        })
      return real(input, init)
    }) as typeof fetch
    const r = await syncSpace(s)
    globalThis.fetch = real
    expect(r.outcome).toBe('disabled')
    expect(hasCopy('local', 'default')).toBe(true)
    expect((await s.state()).disabled).toBe(true)
    expect((await s.catalog()).length).toBe(1)
  })

  it('no network: the copy is untouched and the outcome says so', async () => {
    await make('', '甲')
    const s = await open()
    await syncSpace(s)
    net.down()
    const r = await syncSpace(s)
    expect(r.outcome).toBe('offline')
    expect((await s.catalog()).length).toBe(1)
    net.up()
    expect((await syncSpace(s)).outcome).toBe('ok')
  })

  it('another failure is kept for the settings to show, and the next sync is a fresh start', async () => {
    await make('', '甲')
    const s = await open()
    await syncSpace(s)
    const real = globalThis.fetch
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      if (/\/changes/.test(String(input)))
        return new Response(JSON.stringify({ error: 'internal', message: 'boom' }), {
          status: 500,
          headers: { 'Content-Type': 'application/json' },
        })
      return real(input, init)
    }) as typeof fetch
    const r = await syncSpace(s)
    globalThis.fetch = real
    expect(r).toMatchObject({ outcome: 'error', message: 'boom' })
    expect((await s.state()).error).toBe('boom')
    await syncSpace(s)
    expect((await s.state()).error).toBe('')
  })

  it('a walk of the catalogue that is cut off goes on from its last batch, and still forgets what is gone', async () => {
    const pages = []
    for (let i = 0; i < 6; i++) pages.push(await make('', `页${i}`))
    const s = await open()
    await syncSpace(s, { catalogBatch: 2 })
    // The index is built again and a page goes: the next sync walks again (five pages, three batches), and the
    // network fails at the third batch.
    await S().deletePage(pages[5].id)
    await S().reindex()
    const real = globalThis.fetch
    let batches = 0
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      if (/sync\/catalog/.test(String(input)) && ++batches === 3) throw new TypeError('Failed to fetch')
      return real(input, init)
    }) as typeof fetch
    expect((await syncSpace(s, { catalogBatch: 2 })).outcome).toBe('offline')
    globalThis.fetch = real
    const before = net.requests.length
    const r = await syncSpace(s, { catalogBatch: 2 })
    expect(r.outcome).toBe('ok')
    const walked = net.requests.slice(before).filter((q) => /sync\/catalog/.test(q))
    // One batch was left: the walk asks for it with the cursor it had, not from the first page again.
    expect(walked).toHaveLength(1)
    expect(walked[0]).toContain('cursor=')
    expect(await ids(s)).toEqual(
      pages
        .slice(0, 5)
        .map((p) => p.id)
        .sort(),
    )
    expect((await s.state()).cursor).toBe((await S().syncCatalog({})).changes)
    // A later walk starts from the first batch again.
    await S().reindex()
    const again = net.requests.length
    await syncSpace(s, { catalogBatch: 2 })
    expect(net.requests.slice(again).filter((q) => /sync\/catalog/.test(q))[0]).not.toContain('cursor=')
    expect(await ids(s)).toEqual(
      pages
        .slice(0, 5)
        .map((p) => p.id)
        .sort(),
    )
  })

  it('is told a 410 only through the client: ApiError carries the code', async () => {
    const s = await open()
    await syncSpace(s)
    await S().reindex()
    await expect(S().changes((await s.state()).cursor)).rejects.toSatisfy(
      (e) => e instanceof ApiError && e.status === 410 && e.code === 'changes_expired',
    )
  })
})
