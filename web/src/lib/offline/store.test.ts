import { beforeEach, describe, expect, it } from 'vitest'
import type { CatalogEntry, Page } from '@/api/types'
import { freshDevice } from './testkit'
import { clearAccount, configure, existingStore, hasCopy, storeFor } from './manager'
import { bytesOf, entryOf, spacesOf, totalBytes, usersOf } from './registry'
import { DEFAULT_CONFIG, OfflineStore, RECENT_KEPT, pageBytes, utf8Length } from './store'
import { wantedText } from './scope'

beforeEach(freshDevice)

const entry = (id: string, over: Partial<CatalogEntry> = {}): CatalogEntry => ({
  id,
  path: `${id}.md`,
  folder: '',
  label: id,
  untitled: false,
  tags: [],
  type: '',
  updated: '2026-11-23T08:00:00Z',
  rev: `rev-${id}`,
  ...over,
})
const page = (id: string, body = '正文\n', rev = `rev-${id}`): Page => ({
  id,
  path: `${id}.md`,
  folder: '',
  rev,
  updated: '2026-11-23T08:00:00Z',
  meta: { id, title: id },
  header: `id: ${id}\n`,
  body,
})

describe('the store of one space', () => {
  it('keeps the choice and the sync state, with defaults for what was not written', async () => {
    const s = await storeFor('u1', 'sp')
    expect(await s.config()).toBeUndefined()
    await s.setConfig({ ...DEFAULT_CONFIG, mode: 'text', folders: ['a'] })
    expect(await s.config()).toEqual({ ...DEFAULT_CONFIG, mode: 'text', folders: ['a'] })
    expect((await s.state()).cursor).toBe('')
    await s.setState({ cursor: 'c-1', complete: true })
    await s.setState({ tree: 't' })
    expect(await s.state()).toMatchObject({
      cursor: 'c-1',
      complete: true,
      tree: 't',
      favourites: { pages: [], folders: [] },
    })
  })

  it('writes the catalogue with its cursor in one step, and a removed page takes its text with it', async () => {
    const s = await storeFor('u1', 'sp')
    await s.applyCatalog([entry('A'), entry('B')], [], { cursor: 'c-1' })
    await s.putPages([page('A'), page('B')])
    expect((await s.catalog()).map((e) => e.id).sort()).toEqual(['A', 'B'])
    expect((await s.state()).cursor).toBe('c-1')
    const before = await s.bytes()
    expect(before).toBe(pageBytes(page('A')) + pageBytes(page('B')))
    await s.touchOpened('A', 5)
    await s.applyCatalog([entry('B', { rev: 'rev-2' })], ['A'], { cursor: 'c-2' })
    expect((await s.catalog()).map((e) => e.id)).toEqual(['B'])
    expect(await s.page('A')).toBeUndefined()
    expect(await s.bytes()).toBe(pageBytes(page('B')))
    expect((await s.state()).cursor).toBe('c-2')
    expect(await s.catalogCount()).toBe(1)
    expect(await s.textCount()).toBe(1)
  })

  it('keeps the mark of a page opened when it is removed, so a page that comes back (a folder moved under a poll) is still recent', async () => {
    const s = await storeFor('u1', 'sp')
    await s.applyCatalog([entry('A'), entry('B')], [])
    await s.touchOpened('A', 5)
    await s.applyCatalog([], ['A'])
    expect((await s.opened()).map((o) => o.id)).toEqual(['A'])
    await s.applyCatalog([entry('A', { folder: 'Moved' })], [])
    const w = wantedText(
      await s.catalog(),
      { ...DEFAULT_CONFIG, mode: 'text', inbox: false, favourites: false, recent: true },
      '',
      { pages: [], folders: [] },
      await s.opened(),
    )
    expect(w.recent).toEqual(['A'])
  })

  it('counts the bytes of text exactly through replacing and deleting', async () => {
    const s = await storeFor('u1', 'sp')
    await s.putPages([page('A', 'x'.repeat(100))])
    await s.putPages([page('A', 'x'.repeat(300), 'r2')])
    expect(await s.bytes()).toBe(pageBytes(page('A', 'x'.repeat(300))))
    await s.deletePages(['A', 'nothing'])
    expect(await s.bytes()).toBe(0)
    expect(utf8Length('a')).toBe(1)
    expect(utf8Length('é')).toBe(2)
    expect(utf8Length('页')).toBe(3)
    expect(utf8Length('😀')).toBe(4)
  })

  it('keeps the draft that came with a page', async () => {
    const s = await storeFor('u1', 'sp')
    const p = {
      ...page('A'),
      draft: {
        version: 'v1',
        baseRev: 'rev-A',
        meta: {},
        body: '写到一半',
        updated: '2026-11-23T08:00:00Z',
        stale: false,
      },
    }
    await s.putPages([p])
    expect((await s.page('A'))?.page.draft?.body).toBe('写到一半')
    expect(await s.bytes()).toBe(pageBytes(p))
  })

  it('remembers the last 200 pages opened, newest first, and forgets the oldest', async () => {
    const s = await storeFor('u1', 'sp')
    for (let i = 0; i < RECENT_KEPT + 5; i++) await s.touchOpened(`P${i}`, i + 1)
    const o = await s.opened()
    expect(o).toHaveLength(RECENT_KEPT)
    expect(o[0].id).toBe(`P${RECENT_KEPT + 4}`)
    expect(o.at(-1)!.id).toBe('P5')
    await s.touchOpened('P5', 10_000)
    expect((await s.opened())[0].id).toBe('P5')
    expect(await s.opened()).toHaveLength(RECENT_KEPT)
  })

  it('answers the whole catalogue from memory until something is written', async () => {
    const s = await storeFor('u1', 'sp')
    await s.applyCatalog([entry('A')], [])
    const a = await s.catalog()
    expect(await s.catalog()).toBe(a)
    await s.applyCatalog([entry('B')], [])
    expect(await s.catalog()).not.toBe(a)
    expect(await s.catalog()).toHaveLength(2)
  })

  it('drops what a catalogue walk that started again did not see', async () => {
    const s = await storeFor('u1', 'sp')
    await s.applyCatalog([entry('A'), entry('B'), entry('C')], [])
    await s.putPages([page('B')])
    expect(await s.dropCatalogExcept(new Set(['A', 'C']))).toBe(1)
    expect((await s.catalogIds()).sort()).toEqual(['A', 'C'])
    expect(await s.textCount()).toBe(0)
  })
})

describe('the copies of an account', () => {
  it('are per account and per space: another account sees none of them', async () => {
    await configure('u1', 'sp', { ...DEFAULT_CONFIG })
    const s1 = await storeFor('u1', 'sp')
    await s1.applyCatalog([entry('A')], [])
    expect(hasCopy('u1', 'sp')).toBe(true)
    expect(hasCopy('u2', 'sp')).toBe(false)
    expect(hasCopy('u1', 'other')).toBe(false)
    expect(await existingStore('u2', 'sp')).toBeUndefined()
    // Another account on the same browser starts empty, whatever the space.
    await configure('u2', 'sp', { ...DEFAULT_CONFIG })
    expect(await (await storeFor('u2', 'sp')).catalog()).toEqual([])
    expect(spacesOf('u1')).toEqual(['sp'])
    expect(usersOf().sort()).toEqual(['u1', 'u2'])
  })

  it('are listed with their sizes, which add up for the whole device', async () => {
    await configure('u1', 'a', { ...DEFAULT_CONFIG, mode: 'text' })
    await configure('u1', 'b', { ...DEFAULT_CONFIG, mode: 'text' })
    await configure('u2', 'c', { ...DEFAULT_CONFIG, mode: 'text' })
    await (await storeFor('u1', 'a')).putPages([page('A', 'x'.repeat(1000))])
    await (await storeFor('u1', 'b')).putPages([page('B', 'y'.repeat(2000))])
    await (await storeFor('u2', 'c')).putPages([page('C', 'z'.repeat(4000))])
    expect(entryOf('u1', 'a')).toMatchObject({ texts: 1, catalog: 0 })
    expect(bytesOf('u1')).toBe(pageBytes(page('A', 'x'.repeat(1000))) + pageBytes(page('B', 'y'.repeat(2000))))
    expect(totalBytes()).toBe(bytesOf('u1') + bytesOf('u2'))
  })

  it("are deleted with the account: its databases, the ones the registry lost included, and nobody else's", async () => {
    await configure('u1', 'a', { ...DEFAULT_CONFIG })
    await configure('u1', 'b', { ...DEFAULT_CONFIG })
    await configure('u2', 'c', { ...DEFAULT_CONFIG })
    await (await storeFor('u1', 'a')).applyCatalog([entry('A')], [])
    await (await storeFor('u2', 'c')).applyCatalog([entry('C')], [])
    // The registry is lost (localStorage cleared) but the databases are there.
    const reg = localStorage.getItem('petalbook.offline')!
    localStorage.removeItem('petalbook.offline')
    await clearAccount('u1')
    localStorage.setItem('petalbook.offline', reg)
    const names = (await indexedDB.databases()).map((d) => d.name)
    expect(names.filter((n) => n?.includes(':u1:'))).toEqual([])
    expect(names.filter((n) => n?.includes(':u2:'))).toHaveLength(1)
    // An open store of the account is closed, and what is opened again is empty.
    await clearAccount('u1')
    expect(spacesOf('u1')).toEqual([])
    expect(await existingStore('u1', 'a')).toBeUndefined()
    expect(await (await storeFor('u1', 'a')).catalog()).toEqual([])
    expect(await (await storeFor('u2', 'c')).catalog()).toHaveLength(1)
  })

  it('configure(null) deletes the copy of a space', async () => {
    await configure('u1', 'a', { ...DEFAULT_CONFIG })
    await (await storeFor('u1', 'a')).applyCatalog([entry('A')], [])
    await configure('u1', 'a', null)
    expect(hasCopy('u1', 'a')).toBe(false)
    expect((await indexedDB.databases()).filter((d) => d.name?.includes(':u1:'))).toEqual([])
  })

  it('a store another tab deleted is opened again, empty', async () => {
    const s = await storeFor('u1', 'a')
    await s.setConfig(DEFAULT_CONFIG)
    await s.refreshRegistry()
    const gone = indexedDB.deleteDatabase((await indexedDB.databases())[0].name!)
    await new Promise((r) => (gone.onsuccess = r))
    expect(s.closed).toBe(true)
    const again = await storeFor('u1', 'a')
    expect(again).not.toBe(s)
    expect(await again.config()).toBeUndefined()
    expect(again).toBeInstanceOf(OfflineStore)
  })
})
