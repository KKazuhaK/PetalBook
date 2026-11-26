import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { ApiError } from '@/api/client'
import cases from '../../../../internal/page/testdata/excerpts.json'
import type { Page } from '@/api/types'
import { t } from '@/i18n'
import { DEFAULT_CONFIG } from './store'
import { configure } from './manager'
import { noteOpened } from './index'
import { excerptOf } from './reader'
import { isOffline } from './status'
import { make, network, S, type Net } from './testkit'
import { syncSpace } from './sync'

let net: Net
beforeEach(() => {
  net = network()
})
afterEach(() => net.restore())

// A device with a copy of the space, then no network.
async function offlineWith(
  mode: 'catalog' | 'text',
  build: () => Promise<void>,
  over: Partial<typeof DEFAULT_CONFIG> = {},
) {
  await build()
  const s = (await configure('local', 'default', { ...DEFAULT_CONFIG, mode, ...over }))!
  await syncSpace(s)
  net.down()
  return s
}

describe('reading without the server', () => {
  it('lists the catalogue by folder, tag and sort, with the title and the text of the page when it is kept', async () => {
    const ids: Record<string, Page> = {}
    await offlineWith(
      'text',
      async () => {
        ids.a = await make('Tang', '暮雪', '千山鸟飞绝\n万径人踪灭\n', { tags: ['诗'] })
        ids.b = await make('Tang/Li', '静夜思', '床前明月光\n', { tags: ['诗', '月'] })
        ids.c = await make('Song', '无题', '')
      },
      { folders: ['Tang'] },
    )
    const sp = S()
    const all = await sp.listPages({ folder: '', recursive: true, sort: 'title' })
    expect(all.items.map((i) => i.title)).toEqual(
      ['暮雪', '无题', '静夜思'].sort((x, y) => x.codePointAt(0)! - y.codePointAt(0)!),
    )
    expect(isOffline()).toBe(true)
    const tang = await sp.listPages({ folder: 'Tang', recursive: true })
    expect(tang.items.map((i) => i.id).sort()).toEqual([ids.a.id, ids.b.id].sort())
    expect(tang.items.find((i) => i.id === ids.a.id)!.excerpt).toBe('千山鸟飞绝\n万径人踪灭')
    // A folder without the pages below it, a tag, a date, a sort by creation, a page at a time.
    expect((await sp.listPages({ folder: 'Tang' })).items.map((i) => i.id)).toEqual([ids.a.id])
    expect((await sp.listPages({ tag: ['月'] })).items.map((i) => i.id)).toEqual([ids.b.id])
    expect((await sp.listPages({ tag: ['诗', '月'] })).items).toHaveLength(1)
    // The server owns the creation date (today): a range that holds it, and one that does not.
    const today = new Date().toISOString().slice(0, 10)
    expect((await sp.listPages({ createdFrom: today })).items).toHaveLength(3)
    expect((await sp.listPages({ createdTo: '2000-01-01' })).items).toHaveLength(0)
    expect((await sp.listPages({ updatedFrom: '2999-01-01' })).items).toHaveLength(0)
    // The same creation date: the order is by id, and a page at a time follows it.
    const byId = [ids.a.id, ids.b.id, ids.c.id].sort()
    expect((await sp.listPages({ sort: 'created' })).items.map((i) => i.id)).toEqual(byId)
    const first = await sp.listPages({ sort: 'created', limit: 2 })
    expect(first.items.map((i) => i.id)).toEqual(byId.slice(0, 2))
    const second = await sp.listPages({ sort: 'created', limit: 2, cursor: first.next })
    expect(second.items.map((i) => i.id)).toEqual(byId.slice(2))
    expect(second.next).toBe('')
    expect(net.count(/GET .*\/pages/)).toBeGreaterThan(5) // each was tried on the network first
  })

  it('searches the titles of the whole catalogue and the text of the pages that have it, and only those', async () => {
    const ids: Record<string, Page> = {}
    await offlineWith(
      'text',
      async () => {
        ids.kept = await make('Inbox', '收件箱里', '月光洒在窗前\n')
        ids.bare = await make('Misc', '没有正文', '月光也在这里\n')
        ids.title = await make('Misc', '明月几时有', '别的\n')
      },
      { favourites: false, recent: false },
    )
    const sp = S()
    const hit = async (q: string, extra: { in?: 'title' } = {}) =>
      (await sp.listPages({ q, recursive: true, folder: '', ...extra })).items.map((i) => i.id).sort()
    // The text of a page that is kept is searched; the one that is not is found by its title only.
    expect(await hit('月光')).toEqual([ids.kept.id])
    expect(await hit('明月')).toEqual([ids.title.id])
    expect(await hit('月光', { in: 'title' })).toEqual([])
    expect(await hit('没有')).toEqual([ids.bare.id])
    // Every word has to be in it; case does not matter.
    expect(await hit('窗前 月光')).toEqual([ids.kept.id])
    expect(await hit('窗前 明月')).toEqual([])
    expect((await sp.listPages({ q: 'Inbox' })).items).toEqual([])
  })

  it('reads a page whose text was kept, with its draft; one that was not is said not to be downloaded', async () => {
    let kept!: Page
    let bare!: Page
    await offlineWith(
      'text',
      async () => {
        kept = await make('Inbox', '读得到', '正文在这里\n')
        bare = await make('Misc', '读不到', '正文不在\n')
        await S().putDraft(kept.id, { baseRev: kept.rev, meta: kept.meta, body: '正文在这里，续\n', prev: '' })
      },
      { favourites: false, recent: false },
    )
    const sp = S()
    const p = await sp.getPage(kept.id)
    expect(p.body).toBe('正文在这里\n')
    expect(p.rev).toBe(kept.rev)
    expect(p.draft!.body).toBe('正文在这里，续\n')
    const e = await sp.getPage(bare.id).catch((x: unknown) => x)
    expect(e).toBeInstanceOf(ApiError)
    expect(e).toMatchObject({ status: 0, code: 'not_offline', message: t.offline.notDownloaded })
    const gone = await sp.getPage('01ZZZZZZZZ').catch((x: unknown) => x)
    expect(gone).toMatchObject({ status: 404, code: 'not_found', message: t.offline.notHere })
  })

  it('gives the tree and the tags as the server last gave them, and counts them itself when it has none', async () => {
    const s = await offlineWith('catalog', async () => {
      await make('Tang/Li', 'a', 'x', { tags: ['甲', '乙'] })
      await make('Tang', 'b', 'x', { tags: ['甲'] })
      await S().createFolder('Empty')
    })
    const when = expect.stringMatching(/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$/)
    // Each folder's updated is the newest page in it or below it; the empty folder has none.
    expect((await S().tree()).folders).toEqual([
      { path: '', pages: 0, updated: when },
      { path: 'Empty', pages: 0 },
      { path: 'Tang', pages: 1, updated: when },
      { path: 'Tang/Li', pages: 1, updated: when },
    ])
    expect((await S().tags()).tags).toEqual([
      { tag: '甲', pages: 2 },
      { tag: '乙', pages: 1 },
    ])
    // Without what the server gave (an older copy), the catalogue is counted.
    await s.setTree(undefined as never)
    expect((await S().tree()).folders.map((f) => f.path)).toEqual(['', 'Tang', 'Tang/Li'])
  })

  it("moves a folder's updated offline when a page is edited, although the saved tree is not read again", async () => {
    net.restore()
    let clock = Date.UTC(2026, 10, 1, 8)
    net = network({ now: () => clock })
    const page = await make('Tang/Li/Deep', 'a', 'x\n')
    await make('Other', 'b', 'x\n')
    const s = await offlineWith('catalog', async () => {})
    const at = async () => Object.fromEntries((await S().tree()).folders.map((f) => [f.path, f.updated ?? '']))
    const before = await at()
    expect(before['Tang']).toBe('2026-11-01T08:00:00Z')
    net.up()
    clock += 3_600_000
    await S().savePage(page.id, { baseRev: page.rev, meta: page.meta, body: 'y\n' })
    await syncSpace(s) // the page changed; the tree's stamp did not
    net.down()
    const after = await at()
    for (const f of ['', 'Tang', 'Tang/Li', 'Tang/Li/Deep']) expect(after[f]).toBe('2026-11-01T09:00:00Z')
    expect(after['Other']).toBe('2026-11-01T08:00:00Z')
  })

  it('does not answer what needs more than the catalogue and the text: properties, bodies, boards, history', async () => {
    await offlineWith('text', async () => void (await make('Inbox', 'a')))
    const sp = S()
    for (const call of [
      () => sp.listPages({ props: true }),
      () => sp.listPages({ body: true }),
      () => sp.listPages({ filter: [{ key: 'status', op: 'in', values: ['x'] }] }),
      () => sp.listPages({ sort: 'prop:status' }),
      () => sp.history('01ZZZZZZZZ'),
      () => sp.types(),
      () => sp.templates(),
    ]) {
      const e = await call().catch((x: unknown) => x)
      expect(e).toMatchObject({ status: 0, code: 'network' })
    }
  })

  it('answers nothing for a space that has no copy, and never writes', async () => {
    await make('', '甲')
    net.down()
    expect(
      await S()
        .listPages({})
        .catch((x: unknown) => x),
    ).toMatchObject({ code: 'network' })
    const s = await configure('local', 'default', { ...DEFAULT_CONFIG })
    net.up()
    await syncSpace(s!)
    net.down()
    expect((await S().listPages({})).items).toHaveLength(1)
    expect(
      await S()
        .createPage({ meta: { title: 'x' } })
        .catch((x: unknown) => x),
    ).toMatchObject({ code: 'network' })
    expect(
      await S()
        .savePage('01ZZZZZZZZ', { baseRev: 'x', meta: {}, body: '' })
        .catch((x: unknown) => x),
    ).toMatchObject({ code: 'network' })
  })

  it('does not answer until the first walk of the catalogue has finished', async () => {
    await make('', '甲')
    await configure('local', 'default', { ...DEFAULT_CONFIG })
    net.down()
    expect(
      await S()
        .listPages({})
        .catch((x: unknown) => x),
    ).toMatchObject({ code: 'network' })
  })

  it('is online again as soon as a request gets through', async () => {
    await offlineWith('catalog', async () => void (await make('', '甲')))
    await S().listPages({})
    expect(isOffline()).toBe(true)
    net.up()
    await S().listPages({})
    expect(isOffline()).toBe(false)
  })
})

describe('a page that is opened', () => {
  it('is among the last opened, and its text is kept at once, when the space keeps those', async () => {
    const p = await make('Misc', '读过的', '读过的正文\n')
    const s = (await configure('local', 'default', {
      ...DEFAULT_CONFIG,
      mode: 'text',
      favourites: false,
      inbox: false,
    }))!
    await syncSpace(s)
    expect(await s.page(p.id)).toBeUndefined()
    await noteOpened('default', await S().getPage(p.id))
    expect((await s.opened()).map((o) => o.id)).toEqual([p.id])
    expect((await s.page(p.id))!.page.body).toBe('读过的正文\n')
    net.down()
    expect((await S().getPage(p.id)).body).toBe('读过的正文\n')
  })

  it('is nothing for the catalogue only, for a space with no copy, or when the recent ones are turned off', async () => {
    const p = await make('Misc', 'x')
    await noteOpened('default', p)
    expect(await (await import('./manager')).existingStore('local', 'default')).toBeUndefined()
    const s = (await configure('local', 'default', { ...DEFAULT_CONFIG, mode: 'catalog' }))!
    await noteOpened('default', p)
    expect(await s.opened()).toEqual([])
    await s.setConfig({ ...DEFAULT_CONFIG, mode: 'text', recent: false })
    await noteOpened('default', p)
    expect(await s.opened()).toEqual([])
  })
})

describe('excerptOf', () => {
  // The cases the server's excerpt builder is tested with (internal/page/testdata/excerpts.json).
  it.each(cases)('matches the server: $name', ({ body, excerpt }) => {
    expect(excerptOf(body)).toBe(excerpt)
  })

  it('counts the marker inside the 400 characters', () => {
    const ex = excerptOf('- [x] ' + '花'.repeat(600))
    expect(Array.from(ex)).toHaveLength(400)
    expect(ex.startsWith('[x] 花')).toBe(true)
  })
})
