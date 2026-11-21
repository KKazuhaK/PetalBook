// Local drafts: what survives a closed tab, a save that fails after the page is
// gone, a body too big for keepalive, and storage that misbehaves.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/api/client'
import type { Page, SavePageInput } from '@/api/types'
import type { DocJSON } from '@/markdown'
import { planRestore } from './draftRestore'
import { clearDraft, DRAFT_MAX_AGE_MS, draftStoreFor, loadDraft, parseSavedMarker, pruneExpired, saveDraft, savedMarkerKey, type DraftContent, type KeyValueStorage } from './draftStore'
import { draftToCreateInput, recoverDraft } from './recoverDraft'
import { LiveDraft } from './liveDraft'
import { SaveController, type Fields } from './saveController'

class FakeStorage implements KeyValueStorage {
  data = new Map<string, string>()
  quota = Infinity // total characters allowed
  failGet = false
  failSet = false
  failRemove = false
  get length() {
    return this.data.size
  }
  key(i: number) {
    return [...this.data.keys()][i] ?? null
  }
  getItem(k: string) {
    if (this.failGet) throw new DOMException('denied', 'SecurityError')
    return this.data.get(k) ?? null
  }
  setItem(k: string, v: string) {
    if (this.failSet) throw new DOMException('denied', 'SecurityError')
    const used = [...this.data].filter(([key]) => key !== k).reduce((n, [, val]) => n + val.length, 0)
    if (used + v.length > this.quota) throw new DOMException('full', 'QuotaExceededError')
    this.data.set(k, v)
  }
  removeItem(k: string) {
    if (this.failRemove) throw new DOMException('denied', 'SecurityError')
    this.data.delete(k)
  }
}

const ID = '01JD3K8Q7Z'
const page = (over: Partial<Page> = {}): Page => ({
  id: ID,
  path: 'a.md',
  folder: '',
  rev: 'rev1',
  updated: '2026-11-21T08:30:00Z',
  meta: { id: ID, title: '原标题', created: '2026-01-01' },
  body: '原文\n',
  ...over,
})
const doc = (text: string): DocJSON => ({ type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text }] }] })
// A stand-in for the editor's Markdown conversion: one paragraph per document.
const toMd = (d: DocJSON) => (d.content?.[0]?.content?.[0]?.text ?? '') + '\n'
const fromMdNorm = (md: string) => md

interface Typed {
  title: string
  body?: string
}

function session(opts: { page?: Page; storage: FakeStorage; save?: (id: string, input: SavePageInput) => Promise<Page>; onExternalSave?: (rev: string) => void }) {
  const start = opts.page ?? page()
  const live = new LiveDraft({ title: String(start.meta.title ?? ''), subtitle: '', tags: [] }, toMd)
  const editor = { isDestroyed: false, current: doc(''), getJSON() { return this.current } }
  live.attachEditor(editor)
  const sent: SavePageInput[] = []
  const pendings: { resolve: (p: Page) => void; reject: (e: unknown) => void }[] = []
  const onConflict = vi.fn()
  const save =
    opts.save ??
    ((_id: string, input: SavePageInput) =>
      new Promise<Page>((resolve, reject) => {
        sent.push(input)
        pendings.push({ resolve, reject })
      }))
  const controller = new SaveController({
    page: start,
    read: live.read,
    save: (id, input) => save(id, input),
    normalizeBody: fromMdNorm,
    drafts: draftStoreFor(ID, opts.storage),
    readDraft: live.readStorable,
    onExternalSave: opts.onExternalSave,
    onConflict,
  })
  const type = (t: Typed) => {
    if (t.body !== undefined) {
      editor.current = doc(t.body)
      live.bodyChanged()
    }
    live.setField('title', t.title)
    controller.markDirty()
  }
  const saved = (input: SavePageInput, rev: string): Page => ({ ...start, rev, meta: input.meta, body: input.body })
  return { controller, live, editor, type, sent, pendings, onConflict, saved }
}

beforeEach(() => vi.useFakeTimers())
afterEach(() => vi.useRealTimers())

describe('draft writing', () => {
  it('is written shortly after a change, before the server save goes out', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '新的一行' })
    expect(loadDraft(ID, storage)).toBeNull()
    await vi.advanceTimersByTimeAsync(800)
    const d = loadDraft(ID, storage)!
    expect(d).toMatchObject({ baseRev: 'rev1', fields: { title: '原标题' }, bodyJson: doc('新的一行') })
    expect(s.sent).toHaveLength(0)
  })

  it('is removed when a save of exactly that state succeeds', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '新' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(loadDraft(ID, storage)).not.toBeNull()
    s.pendings[0].resolve(s.saved(s.sent[0], 'rev2'))
    await vi.advanceTimersByTimeAsync(0)
    expect(loadDraft(ID, storage)).toBeNull()
  })

  it('is rewritten against the new rev when more was typed while the save was out', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: 'A' })
    await vi.advanceTimersByTimeAsync(1500)
    s.type({ title: '原标题', body: 'AB' })
    s.pendings[0].resolve(s.saved(s.sent[0], 'rev2'))
    await vi.advanceTimersByTimeAsync(0)
    expect(loadDraft(ID, storage)).toMatchObject({ baseRev: 'rev2', bodyJson: doc('AB') })
  })

  it('opening and leaving writes nothing', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.controller.persistNow()
    await s.controller.detach()
    expect(storage.data.size).toBe(0)
  })

  it('is removed when the person takes the other side or the page is deleted', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '改', body: 'x' })
    s.controller.persistNow()
    s.controller.useTheirs(page({ rev: 'revX' }))
    expect(loadDraft(ID, storage)).toBeNull()
    s.type({ title: '再改' })
    s.controller.persistNow()
    s.controller.discard()
    expect(loadDraft(ID, storage)).toBeNull()
  })
})

describe('closing the tab while a save is in flight', () => {
  it('keeps what was typed after the save started, and restores it', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '第一段' })
    await vi.advanceTimersByTimeAsync(1500) // the save is now out and unanswered
    expect(s.sent).toHaveLength(1)
    s.type({ title: '原标题', body: '第一段，又写了一句' })
    // pagehide: synchronous, no timers, no network
    s.controller.flushKeepalive()
    expect(loadDraft(ID, storage)).toMatchObject({ baseRev: 'rev1', bodyJson: doc('第一段，又写了一句') })

    // Next visit. The in-flight save never reached the server: same rev, so the draft is applied.
    const d = loadDraft(ID, storage)
    const plan = planRestore(page(), d, toMd, fromMdNorm)
    expect(plan.kind).toBe('apply')
    expect(plan.kind === 'apply' && plan.doc).toEqual(doc('第一段，又写了一句'))

    // ...or it did land, so the server moved on: shown as a conflict with the draft as "mine".
    const landed = page({ rev: 'rev2', body: '第一段\n' })
    const conflict = planRestore(landed, d, toMd, fromMdNorm)
    expect(conflict.kind).toBe('conflict')
    expect(conflict.kind === 'conflict' && conflict.mine.body).toBe('第一段，又写了一句\n')
  })

  it('beforeunload and visibility changes take the draft the same way (persistNow is synchronous)', () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: 'x' })
    s.controller.persistNow()
    expect(loadDraft(ID, storage)?.bodyJson).toEqual(doc('x'))
  })

  it('a body too large for a keepalive request is still kept whole', () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    const big = '字'.repeat(200_000)
    s.type({ title: '原标题', body: big })
    s.controller.flushKeepalive()
    const d = loadDraft(ID, storage)!
    expect(d.bodyJson).toEqual(doc(big))
    expect(planRestore(page(), d, toMd, fromMdNorm).kind).toBe('apply')
  })
})

describe('a save started by leaving the page', () => {
  it('that fails with a conflict keeps the draft and does not call into the page', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '离开前写的' })
    const done = s.controller.detach()
    await vi.advanceTimersByTimeAsync(0)
    expect(s.sent).toHaveLength(1)
    s.pendings[0].reject(new ApiError(409, 'conflict', '', page({ rev: 'revX', body: '别人的\n' })))
    await done
    expect(s.onConflict).not.toHaveBeenCalled()
    expect(loadDraft(ID, storage)).toMatchObject({ baseRev: 'rev1', bodyJson: doc('离开前写的') })
    // Offered next time, as a conflict.
    expect(planRestore(page({ rev: 'revX', body: '别人的\n' }), loadDraft(ID, storage), toMd, fromMdNorm).kind).toBe('conflict')
  })

  it('that fails on the network keeps the draft', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '没网' })
    const done = s.controller.detach()
    await vi.advanceTimersByTimeAsync(0)
    s.pendings[0].reject(new ApiError(0, 'network', '无法连接到服务器'))
    await done
    expect(s.controller.state).toBe('error')
    expect(loadDraft(ID, storage)?.bodyJson).toEqual(doc('没网'))
  })

  it('that succeeds removes the draft', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '顺利' })
    const done = s.controller.detach()
    await vi.advanceTimersByTimeAsync(0)
    expect(loadDraft(ID, storage)).not.toBeNull()
    s.pendings[0].resolve(s.saved(s.sent[0], 'rev2'))
    await done
    expect(loadDraft(ID, storage)).toBeNull()
  })

  it('if the editor is already destroyed when the page is left, the last reading is used and the draft stands', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '先存下' })
    s.controller.persistNow()
    s.editor.isDestroyed = true
    const done = s.controller.detach()
    await vi.advanceTimersByTimeAsync(0)
    s.pendings[0].reject(new ApiError(0, 'network', ''))
    await done
    expect(s.sent[0].body).toBe('先存下\n')
    expect(loadDraft(ID, storage)?.bodyJson).toEqual(doc('先存下'))
  })
})

describe('on opening a page', () => {
  const stored = (over: Partial<DraftContent> = {}): DraftContent => ({ baseRev: 'rev1', fields: { title: '原标题', subtitle: '', tags: [] }, savedAt: Date.now(), token: 'old', ...over })

  it('a draft equal to the server version is discarded silently', () => {
    const storage = new FakeStorage()
    saveDraft(ID, stored({ bodyJson: doc('原文') }), storage)
    expect(planRestore(page(), loadDraft(ID, storage), toMd, fromMdNorm)).toEqual({ kind: 'none', stale: true })
    // Same also when the revision differs but the content is what the server has.
    expect(planRestore(page({ rev: 'rev9' }), loadDraft(ID, storage), toMd, fromMdNorm)).toEqual({ kind: 'none', stale: true })
  })

  it('no draft means nothing to do', () => {
    expect(planRestore(page(), null, toMd, fromMdNorm)).toEqual({ kind: 'none', stale: false })
  })

  it('fields-only drafts apply without touching the body', () => {
    const plan = planRestore(page(), { v: 1, id: ID, ...stored({ fields: { title: '新标题', subtitle: '', tags: [] } }) }, toMd, fromMdNorm)
    expect(plan).toEqual({ kind: 'apply', fields: { title: '新标题', subtitle: '', tags: [] }, doc: undefined, token: 'old' })
  })

  it('a conflict plan carries the draft as "mine", with the server body when only fields were edited', () => {
    const plan = planRestore(page({ rev: 'rev2', body: '别人改的\n' }), { v: 1, id: ID, ...stored({ fields: { title: '我的标题', subtitle: '', tags: [] } }) }, toMd, fromMdNorm)
    expect(plan.kind).toBe('conflict')
    expect(plan.kind === 'conflict' && plan.mine).toEqual({ title: '我的标题', subtitle: '', tags: [], body: '别人改的\n' })
  })

  it('a restored draft is then saved normally, on the revision it was typed on', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    const plan = planRestore(page(), { v: 1, id: ID, ...stored({ bodyJson: doc('恢复的内容') }) }, toMd, fromMdNorm)
    expect(plan.kind).toBe('apply')
    s.live.bodyChanged()
    s.editor.current = doc('恢复的内容')
    s.controller.markDirty()
    await vi.advanceTimersByTimeAsync(1500)
    expect(s.sent[0]).toMatchObject({ baseRev: 'rev1', body: '恢复的内容\n' })
  })

  it('a conflict on open puts the controller in the conflict state, ready for keep-mine', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    const theirs = page({ rev: 'rev2', body: '别人改的\n' })
    const mine = { title: '原标题', subtitle: '', tags: [] as string[], body: '我的\n' }
    s.live.bodyChanged()
    s.editor.current = doc('我的')
    s.controller.enterConflict(theirs, mine)
    expect(s.controller.state).toBe('conflict')
    expect(s.onConflict).toHaveBeenCalledWith(theirs, mine)
    void s.controller.keepMine(theirs)
    await vi.advanceTimersByTimeAsync(0)
    expect(s.sent[0]).toMatchObject({ baseRev: 'rev2', body: '我的\n' })
  })
})

describe('storage that misbehaves', () => {
  const content: DraftContent = { baseRev: 'rev1', fields: { title: 't', subtitle: '', tags: [] }, savedAt: Date.now(), token: 'tk', bodyJson: doc('x') }
  const fields: Fields = { title: 't', subtitle: '', tags: [] }

  it('reading that throws is the same as no draft', () => {
    const storage = new FakeStorage()
    saveDraft(ID, content, storage)
    storage.failGet = true
    expect(loadDraft(ID, storage)).toBeNull()
  })

  it('writing that throws returns false and typing and saving carry on', async () => {
    const storage = new FakeStorage()
    storage.failSet = true
    expect(saveDraft(ID, content, storage)).toBe(false)
    const s = session({ storage })
    s.type({ title: '原标题', body: '照常' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(s.sent).toHaveLength(1)
    s.controller.flushKeepalive()
    s.controller.persistNow()
    expect(s.controller.state).toBe('saving')
    s.pendings[0].resolve(s.saved(s.sent[0], 'rev2'))
    await vi.advanceTimersByTimeAsync(0)
    expect(s.controller.state).toBe('saved')
  })

  it('removing that throws does not propagate', () => {
    const storage = new FakeStorage()
    storage.failRemove = true
    expect(() => clearDraft(ID, storage)).not.toThrow()
  })

  it('no storage at all is fine', () => {
    expect(loadDraft(ID, null)).toBeNull()
    expect(saveDraft(ID, content, null)).toBe(false)
    expect(() => clearDraft(ID, null)).not.toThrow()
  })

  it('when storage is full, the oldest drafts of other pages make room', () => {
    const storage = new FakeStorage()
    saveDraft('OLD0000001', { ...content, savedAt: Date.now() - 2000, bodyJson: doc('a'.repeat(100)) }, storage)
    saveDraft('NEW0000002', { ...content, savedAt: Date.now() - 1000, bodyJson: doc('b'.repeat(100)) }, storage)
    storage.setItem('unrelated', 'z'.repeat(50))
    storage.quota = 600
    expect(saveDraft(ID, { ...content, bodyJson: doc('c'.repeat(300)) }, storage)).toBe(true)
    expect(loadDraft('OLD0000001', storage)).toBeNull()
    expect(loadDraft(ID, storage)).not.toBeNull()
    expect(storage.getItem('unrelated')).not.toBeNull()
  })

  it('when even that is not enough, it gives up quietly', () => {
    const storage = new FakeStorage()
    storage.quota = 10
    expect(saveDraft(ID, content, storage)).toBe(false)
  })

  it('an unreadable stored value is dropped', () => {
    const storage = new FakeStorage()
    storage.setItem(`pb.draft.${ID}`, '{not json')
    expect(loadDraft(ID, storage)).toBeNull()
    expect(storage.data.size).toBe(0)
    storage.setItem(`pb.draft.${ID}`, JSON.stringify({ v: 2, id: ID }))
    expect(loadDraft(ID, storage)).toBeNull()
    expect(fields.title).toBe('t')
  })
})

describe('LiveDraft.readStorable', () => {
  it('returns the document without converting to Markdown', () => {
    const conv = vi.fn(() => 'md')
    const d = new LiveDraft({ title: 'a', subtitle: '', tags: [] }, conv)
    const ed = { isDestroyed: false, getJSON: vi.fn(() => doc('x')) }
    d.attachEditor(ed)
    expect(d.readStorable().bodyJson).toBeUndefined()
    d.bodyChanged()
    expect(d.readStorable().bodyJson).toEqual(doc('x'))
    d.readStorable()
    expect(conv).not.toHaveBeenCalled()
    expect(ed.getJSON).toHaveBeenCalledTimes(1)
  })
})

describe('old drafts', () => {
  const content = (savedAt: number): DraftContent => ({ baseRev: 'r', fields: { title: 't', subtitle: '', tags: [] }, savedAt, token: 'tk', bodyJson: doc('x') })

  it('a draft older than 30 days is not offered and is removed', () => {
    const storage = new FakeStorage()
    const now = Date.now()
    saveDraft(ID, content(now - DRAFT_MAX_AGE_MS - 1000), storage)
    expect(loadDraft(ID, storage, now)).toBeNull()
    expect(storage.data.size).toBe(0)
    saveDraft(ID, content(now - DRAFT_MAX_AGE_MS + 60_000), storage)
    expect(loadDraft(ID, storage, now)).not.toBeNull()
  })

  it('opening the store drops expired drafts and saved markers of any page, and nothing else', () => {
    const storage = new FakeStorage()
    const now = Date.now()
    saveDraft('OLD0000001', content(now - DRAFT_MAX_AGE_MS - 5), storage)
    saveDraft('NEW0000002', content(now - 1000), storage)
    storage.setItem(savedMarkerKey('OLD0000001'), JSON.stringify({ rev: 'x', at: now - DRAFT_MAX_AGE_MS - 5 }))
    storage.setItem('theme', 'dark')
    draftStoreFor(ID, storage)
    expect([...storage.data.keys()].sort()).toEqual(['pb.draft.NEW0000002', 'theme'])
    expect(() => pruneExpired(null)).not.toThrow()
  })
})

describe('a page that is gone', () => {
  const draft = { v: 1 as const, id: ID, baseRev: 'r', fields: { title: '写了一半', subtitle: '副', tags: ['x'] }, savedAt: Date.now(), token: 'tk', bodyJson: doc('没存上的话') }

  it('the draft becomes the fields and body of a new page, in the root folder', () => {
    expect(draftToCreateInput(draft, toMd)).toEqual({ folder: '', meta: { title: '写了一半', subtitle: '副', tags: ['x'] }, body: '没存上的话\n' })
    expect(draftToCreateInput({ ...draft, fields: { title: '', subtitle: '', tags: [] }, bodyJson: undefined }, toMd)).toEqual({ folder: '', meta: {}, body: '' })
  })

  it('recovering creates the page first and removes the draft only afterwards', async () => {
    const storage = new FakeStorage()
    saveDraft(ID, draft, storage)
    const failing = vi.fn(() => Promise.reject(new Error('down')))
    await expect(recoverDraft(ID, draft, { create: failing, toMarkdown: toMd, storage })).rejects.toThrow('down')
    expect(loadDraft(ID, storage)).not.toBeNull()
    const create = vi.fn(async (input) => ({ ...page({ id: '01NEW00000' }), body: input.body }))
    const made = await recoverDraft(ID, draft, { create, toMarkdown: toMd, storage })
    expect(made.id).toBe('01NEW00000')
    expect(create).toHaveBeenCalledWith({ folder: '', meta: { title: '写了一半', subtitle: '副', tags: ['x'] }, body: '没存上的话\n' })
    expect(loadDraft(ID, storage)).toBeNull()
  })
})

describe('two tabs on one page', () => {
  it("a save in one tab does not remove the other tab's newer draft", async () => {
    const storage = new FakeStorage()
    const a = session({ storage })
    const b = session({ storage })
    a.type({ title: '原标题', body: 'A 的字' })
    await vi.advanceTimersByTimeAsync(800) // A's draft written
    b.type({ title: '原标题', body: 'B 的字' })
    await vi.advanceTimersByTimeAsync(800) // B's draft written over it; A's save (1500) is now out
    expect(a.sent).toHaveLength(1)
    expect(loadDraft(ID, storage)?.bodyJson).toEqual(doc('B 的字'))
    a.pendings[0].resolve(a.saved(a.sent[0], 'rev2'))
    await vi.advanceTimersByTimeAsync(0)
    expect(a.controller.state).toBe('saved')
    expect(loadDraft(ID, storage)?.bodyJson).toEqual(doc('B 的字'))
  })

  it('a tab removes its own draft as before', async () => {
    const storage = new FakeStorage()
    const a = session({ storage })
    a.type({ title: '原标题', body: 'A' })
    await vi.advanceTimersByTimeAsync(1500)
    a.pendings[0].resolve(a.saved(a.sent[0], 'rev2'))
    await vi.advanceTimersByTimeAsync(0)
    expect(loadDraft(ID, storage)).toBeNull()
  })

  it('a successful save announces the new rev for other tabs', async () => {
    const storage = new FakeStorage()
    const a = session({ storage })
    a.type({ title: '原标题', body: 'A' })
    await vi.advanceTimersByTimeAsync(1500)
    a.pendings[0].resolve(a.saved(a.sent[0], 'rev2'))
    await vi.advanceTimersByTimeAsync(0)
    expect(parseSavedMarker(storage.getItem(savedMarkerKey(ID)))?.rev).toBe('rev2')
    expect(parseSavedMarker('nonsense')).toBeNull()
    expect(parseSavedMarker(null)).toBeNull()
  })

  it('the other tab, with nothing unsaved, is told to reload', async () => {
    const storage = new FakeStorage()
    const reload = vi.fn()
    const b = session({ storage, onExternalSave: reload })
    b.controller.noticeExternalSave('rev1') // its own rev: nothing new
    expect(reload).not.toHaveBeenCalled()
    b.controller.noticeExternalSave('rev2')
    expect(reload).toHaveBeenCalledWith('rev2')
  })

  it('the other tab, with unsaved changes, keeps its base and meets the normal conflict', async () => {
    const storage = new FakeStorage()
    const reload = vi.fn()
    const b = session({ storage, onExternalSave: reload })
    b.type({ title: '原标题', body: 'B 的字' })
    b.controller.noticeExternalSave('rev2')
    await vi.advanceTimersByTimeAsync(0)
    expect(reload).not.toHaveBeenCalled()
    // It tried at once, on the revision it still has.
    expect(b.sent).toHaveLength(1)
    expect(b.sent[0].baseRev).toBe('rev1')
    b.pendings[0].reject(new ApiError(409, 'conflict', '', page({ rev: 'rev2', body: 'A 的字\n' })))
    await vi.advanceTimersByTimeAsync(0)
    expect(b.controller.state).toBe('conflict')
    expect(b.onConflict).toHaveBeenCalledTimes(1)
  })
})

describe('after the page is left', () => {
  it('a save that succeeds with more queued points the stored draft at the new rev', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: 'A' })
    await vi.advanceTimersByTimeAsync(1500) // in flight
    s.type({ title: '原标题', body: 'AB' })
    const done = s.controller.detach() // the editor goes away; "AB" is queued
    await vi.advanceTimersByTimeAsync(0)
    expect(loadDraft(ID, storage)?.baseRev).toBe('rev1')
    s.pendings[0].resolve(s.saved(s.sent[0], 'rev2'))
    await vi.advanceTimersByTimeAsync(0)
    // The queued save is out, on rev2; the draft agrees.
    expect(s.sent[1].baseRev).toBe('rev2')
    expect(loadDraft(ID, storage)).toMatchObject({ baseRev: 'rev2', bodyJson: doc('AB') })
    // It fails: next visit the draft is applied, not a conflict with our own save.
    s.pendings[1].reject(new ApiError(0, 'network', ''))
    await done
    const server = page({ rev: 'rev2', body: 'A\n' })
    expect(planRestore(server, loadDraft(ID, storage), toMd, fromMdNorm).kind).toBe('apply')
  })
})
