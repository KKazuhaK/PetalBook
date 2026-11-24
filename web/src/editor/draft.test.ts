// Local drafts: what survives a closed tab, a request that fails after the page
// is gone, a body too big for keepalive, a session that ended, and storage that
// misbehaves. The local draft is removed only once the server has confirmed
// storing that same content (as a draft or as the page).

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/api/client'
import type { DraftReceipt, Page, PageDraft, SavePageInput } from '@/api/types'
import type { DocJSON } from '@/markdown'
import { oursOf } from './reconcile'
import { planRestore, type RestorePlan } from './draftRestore'
import {
  clearDraft,
  DRAFT_MAX_AGE_MS,
  draftRef,
  draftRefFor,
  draftStoreFor,
  loadDraft,
  parseSavedMarker,
  pruneExpired,
  saveDraft,
  savedMarkerKey,
  type DraftContent,
  type KeyValueStorage,
  type StoredDraft,
} from './draftStore'
import { draftToCreateInput, recoverDraft } from './recoverDraft'
import { LiveDraft } from './liveDraft'
import { SaveController, type ConflictInfo, type Fields } from './saveController'

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
const serverDraft = (over: Partial<PageDraft> = {}): PageDraft => ({
  version: 'v1',
  baseRev: 'rev1',
  meta: { id: ID, title: '原标题', created: '2026-01-01' },
  body: '服务器草稿\n',
  updated: 'u1',
  stale: false,
  ...over,
})
// What a local draft records of the server draft it was typed on top of.
const sentOf = (d: PageDraft) => [oursOf(d.baseRev, d)]
const doc = (text: string): DocJSON => ({
  type: 'doc',
  content: [{ type: 'paragraph', content: [{ type: 'text', text }] }],
})
// A stand-in for the editor's Markdown conversion: one paragraph per document.
const toMd = (d: DocJSON) => (d.content?.[0]?.content?.[0]?.text ?? '') + '\n'
const fromMdNorm = (md: string) => md
const plan = (p: Page, d: StoredDraft | null): RestorePlan => planRestore(p, d, toMd, fromMdNorm)

interface Typed {
  title: string
  body?: string
}

function session(opts: { page?: Page; draft?: PageDraft; storage: FakeStorage; onExternalChange?: (p: Page) => void }) {
  const start = opts.page ?? page()
  const live = new LiveDraft({ title: String(start.meta.title ?? ''), subtitle: '', tags: [] }, toMd)
  const editor = {
    isDestroyed: false,
    current: doc(''),
    getJSON() {
      return this.current
    },
  }
  live.attachEditor(editor)
  const puts: SavePageInput[] = []
  const pending: {
    put: ((r: DraftReceipt) => void)[]
    putFail: ((e: unknown) => void)[]
    commit: ((p: Page) => void)[]
    commitFail: ((e: unknown) => void)[]
    get: ((p: Page) => void)[]
  } = { put: [], putFail: [], commit: [], commitFail: [], get: [] }
  const onConflict = vi.fn<(info: ConflictInfo) => void>()
  let n = 0
  const controller = new SaveController({
    page: start,
    draft: opts.draft,
    read: live.read,
    api: {
      putDraft: (_id, input) =>
        new Promise<DraftReceipt>((resolve, reject) => {
          puts.push(input)
          pending.put.push(resolve)
          pending.putFail.push(reject)
        }),
      deleteDraft: () => Promise.resolve(),
      commit: () =>
        new Promise<Page>((resolve, reject) => {
          pending.commit.push(resolve)
          pending.commitFail.push(reject)
        }),
      getPage: () => new Promise<Page>((resolve) => void pending.get.push(resolve)),
    },
    normalizeBody: fromMdNorm,
    drafts: draftStoreFor(ID, opts.storage),
    readDraft: live.readStorable,
    onExternalChange: opts.onExternalChange,
    onConflict,
  })
  controller.attach()
  const type = (t: Typed) => {
    if (t.body !== undefined) {
      editor.current = doc(t.body)
      live.bodyChanged()
    }
    live.setField('title', t.title)
    controller.markDirty()
  }
  const answerPut = (over: Partial<DraftReceipt> = {}) => {
    const i = pending.put.length - 1
    pending.put[i]({ version: `v${n + 2}`, baseRev: puts[i].baseRev, updated: `u${++n + 1}`, stale: false, ...over })
  }
  const answerCommit = () => {
    const input = puts.at(-1)
    pending.commit.at(-1)!({ ...start, rev: `revC${++n}`, ...(input ? { meta: input.meta, body: input.body } : {}) })
  }
  return { controller, live, editor, type, puts, pending, onConflict, answerPut, answerCommit }
}

beforeEach(() => vi.useFakeTimers())
afterEach(() => vi.useRealTimers())

describe('local draft writing', () => {
  it('is written shortly after a change, before the server draft is stored', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '新的一行' })
    expect(loadDraft(ID, storage)).toBeNull()
    await vi.advanceTimersByTimeAsync(800)
    const d = loadDraft(ID, storage)!
    expect(d).toMatchObject({ baseRev: 'rev1', fields: { title: '原标题' }, bodyJson: doc('新的一行') })
    expect(s.puts).toHaveLength(0)
  })

  it('stays while the store is in flight and after the server confirmed it as a draft; removed once the text is in the file', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '新' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(s.puts).toHaveLength(1)
    expect(loadDraft(ID, storage)).not.toBeNull()
    s.answerPut()
    await vi.advanceTimersByTimeAsync(0)
    // On the server as a draft, which another device can still replace or delete
    // (saving the page discards it): the browser's copy stays.
    expect(loadDraft(ID, storage)?.bodyJson).toEqual(doc('新'))
    void s.controller.commit()
    await vi.advanceTimersByTimeAsync(0)
    s.answerCommit()
    await vi.advanceTimersByTimeAsync(0)
    expect(loadDraft(ID, storage)).toBeNull()
  })

  it('stays when the store fails', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '新' })
    await vi.advanceTimersByTimeAsync(1500)
    s.pending.putFail[0](new ApiError(0, 'network', ''))
    await vi.advanceTimersByTimeAsync(0)
    expect(s.controller.state).toBe('error')
    expect(loadDraft(ID, storage)?.bodyJson).toEqual(doc('新'))
  })

  it('stays when the server stored it but said it is stale (the file moved on)', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '新' })
    await vi.advanceTimersByTimeAsync(1500)
    s.answerPut({ stale: true })
    await vi.advanceTimersByTimeAsync(0)
    s.pending.get[0](page({ rev: 'revX', body: '别人的\n' }))
    await vi.advanceTimersByTimeAsync(0)
    expect(s.controller.state).toBe('conflict')
    expect(loadDraft(ID, storage)?.bodyJson).toEqual(doc('新'))
  })

  it('is rewritten when more was typed while the store was out, noting the server draft it now follows', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: 'A' })
    await vi.advanceTimersByTimeAsync(1500)
    s.type({ title: '原标题', body: 'AB' })
    s.answerPut({ version: 'vA' })
    await vi.advanceTimersByTimeAsync(0)
    expect(loadDraft(ID, storage)).toMatchObject({
      baseRev: 'rev1',
      bodyJson: doc('AB'),
      sent: [oursOf('rev1', { meta: { id: ID, title: '原标题', created: '2026-01-01' }, body: 'A\n' })],
    })
  })

  it('records the server draft it was typed on top of', async () => {
    const storage = new FakeStorage()
    const s = session({ storage, draft: serverDraft({ version: 'v7' }) })
    s.live.bodyChanged()
    s.editor.current = doc('服务器草稿')
    s.type({ title: '原标题', body: '服务器草稿，又写了' })
    s.controller.persistNow()
    expect(loadDraft(ID, storage)).toMatchObject({ baseRev: 'rev1', sent: sentOf(serverDraft({ version: 'v7' })) })
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
    s.controller.enterConflict(
      { kind: 'file', theirs: page({ rev: 'revX' }), mine: { title: '改', subtitle: '', tags: [], body: 'x\n' } },
      true,
    )
    s.controller.useTheirs()
    expect(loadDraft(ID, storage)).toBeNull()
    s.type({ title: '再改' })
    s.controller.persistNow()
    s.controller.discard()
    expect(loadDraft(ID, storage)).toBeNull()
  })

  it('is removed once the commit that followed the store has landed', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '顺利' })
    const done = s.controller.detach()
    await vi.advanceTimersByTimeAsync(0)
    expect(loadDraft(ID, storage)).not.toBeNull()
    s.answerPut()
    await vi.advanceTimersByTimeAsync(0)
    s.answerCommit()
    await done
    expect(loadDraft(ID, storage)).toBeNull()
  })
})

describe('closing the tab while a draft store is in flight', () => {
  it('keeps what was typed after the store started, and restores it', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '第一段' })
    await vi.advanceTimersByTimeAsync(1500) // the store is now out and unanswered
    expect(s.puts).toHaveLength(1)
    s.type({ title: '原标题', body: '第一段，又写了一句' })
    // pagehide: synchronous, no timers, no network
    s.controller.flushKeepalive()
    expect(loadDraft(ID, storage)).toMatchObject({ baseRev: 'rev1', bodyJson: doc('第一段，又写了一句') })

    // Next visit. The in-flight store never reached the server: the file has not moved, the local draft is pushed.
    const d = loadDraft(ID, storage)
    const p = plan(page(), d)
    expect(p.kind).toBe('local')
    expect(p.kind === 'local' && p.content.doc).toEqual(doc('第一段，又写了一句'))

    // ...or it did land and the server committed it: the file is what we sent, so our own
    // text, and the newer text goes up on top of it (no dialog).
    const landed = page({ rev: 'rev2', body: '第一段\n' })
    expect(plan(landed, d).kind).toBe('local')
    // A file that is something else is a conflict, with "mine" on the left.
    const conflict = plan(page({ rev: 'rev2', body: '别人写的\n' }), d)
    expect(conflict.kind).toBe('conflict')
    expect(conflict.kind === 'conflict' && conflict.info.mine.body).toBe('第一段，又写了一句\n')
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
    expect(plan(page(), d).kind).toBe('local')
  })
})

describe('requests started by leaving the page', () => {
  it('a stale answer keeps the local draft and does not call into the page', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '离开前写的' })
    const done = s.controller.detach()
    await vi.advanceTimersByTimeAsync(0)
    expect(s.puts).toHaveLength(1)
    s.answerPut({ stale: true })
    await vi.advanceTimersByTimeAsync(0)
    s.pending.get[0](page({ rev: 'revX', body: '别人的\n' }))
    await done
    expect(s.onConflict).not.toHaveBeenCalled()
    expect(loadDraft(ID, storage)).toMatchObject({ baseRev: 'rev1', bodyJson: doc('离开前写的') })
    // Offered next time, as a conflict.
    expect(plan(page({ rev: 'revX', body: '别人的\n' }), loadDraft(ID, storage)).kind).toBe('conflict')
  })

  it('that fails on the network keeps the draft', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '没网' })
    const done = s.controller.detach()
    await vi.advanceTimersByTimeAsync(0)
    s.pending.putFail[0](new ApiError(0, 'network', '无法连接到服务器'))
    await done
    expect(s.controller.state).toBe('error')
    expect(loadDraft(ID, storage)?.bodyJson).toEqual(doc('没网'))
  })

  it('if the editor is already destroyed when the page is left, the last reading is used and the draft stands', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '先存下' })
    s.controller.persistNow()
    s.editor.isDestroyed = true
    const done = s.controller.detach()
    await vi.advanceTimersByTimeAsync(0)
    s.pending.putFail[0](new ApiError(0, 'network', ''))
    await done
    expect(s.puts[0].body).toBe('先存下\n')
    expect(loadDraft(ID, storage)?.bodyJson).toEqual(doc('先存下'))
  })
})

describe('a session that ended in the middle of editing', () => {
  it('keeps the local draft, and the text comes back after signing in again', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '写到一半的字' })
    await vi.advanceTimersByTimeAsync(1500)
    s.pending.putFail[0](new ApiError(401, 'unauthenticated', '登录已失效'))
    await vi.advanceTimersByTimeAsync(0)
    expect(s.controller.state).toBe('error')
    // The sign-in page replaces the editor.
    await s.controller.detach()
    expect(s.puts).toHaveLength(1)
    const d = loadDraft(ID, storage)!
    expect(d.bodyJson).toEqual(doc('写到一半的字'))
    // After signing in the page opens again: the file has not moved, so the text is put back and stored.
    const p = plan(page(), d)
    expect(p.kind).toBe('local')
    expect(p.kind === 'local' && p.content.doc).toEqual(doc('写到一半的字'))
  })
})

describe('on opening a page: file, server draft and local draft', () => {
  const stored = (over: Partial<DraftContent> = {}): StoredDraft => ({
    v: 1,
    id: ID,
    baseRev: 'rev1',
    fields: { title: '原标题', subtitle: '', tags: [] },
    savedAt: Date.now(),
    token: 'old',
    ...over,
  })
  const withDraft = (over: Partial<PageDraft> = {}, pageOver: Partial<Page> = {}) =>
    page({ draft: serverDraft(over), ...pageOver })

  it('nothing at all: the file', () => {
    expect(plan(page(), null)).toMatchObject({ kind: 'file', dropLocal: false })
  })

  it('a local draft equal to the file is discarded silently, whatever its rev', () => {
    expect(plan(page(), stored({ bodyJson: doc('原文') }))).toMatchObject({ kind: 'file', dropLocal: true })
    expect(plan(page({ rev: 'rev9' }), stored({ bodyJson: doc('原文') }))).toMatchObject({
      kind: 'file',
      dropLocal: true,
    })
  })

  it('a local fields-only draft on the same file is applied without touching the body', () => {
    const p = plan(page(), stored({ fields: { title: '新标题', subtitle: '', tags: [] } }))
    expect(p).toMatchObject({
      kind: 'local',
      content: { fields: { title: '新标题', subtitle: '', tags: [] }, markdown: '原文\n' },
      token: 'old',
    })
  })

  it('a local draft on an older file is a conflict, with the draft as "mine" and the server body when only fields were edited', () => {
    const p = plan(
      page({ rev: 'rev2', body: '别人改的\n' }),
      stored({ fields: { title: '我的标题', subtitle: '', tags: [] } }),
    )
    expect(p.kind).toBe('conflict')
    if (p.kind !== 'conflict') return
    expect(p.info.kind).toBe('file')
    expect(p.info.mine).toEqual({ title: '我的标题', subtitle: '', tags: [], body: '别人改的\n' })
    expect(p.unsaved).toBe(true)
  })

  it('a local copy that was once stored as a server draft, whose draft is gone with the file moved on, is an older copy with its time', () => {
    const saved = Date.parse('2026-11-20T10:00:00Z')
    const sent = [oursOf('rev1', { meta: page().meta, body: '我写的\n' })]
    const p = plan(page({ rev: 'rev2', body: '别人改的\n' }), stored({ bodyJson: doc('我写的'), sent, savedAt: saved }))
    expect(p.kind).toBe('conflict')
    if (p.kind !== 'conflict') return
    expect(p.info.older).toEqual({ savedAt: saved })
    // Never stored anywhere: not an "older copy", just the person's unsaved text.
    const q = plan(page({ rev: 'rev2', body: '别人改的\n' }), stored({ bodyJson: doc('我写的') }))
    expect(q.kind === 'conflict' && q.info.older).toBeUndefined()
  })

  it('a local copy whose file moved to something it sent is built on the new file, forgetting the old rev and digests', () => {
    const sent = [oursOf('rev1', { meta: page().meta, body: '第一次\n' })]
    const p = plan(page({ rev: 'rev2', body: '第一次\n' }), stored({ bodyJson: doc('第二次'), sent }))
    expect(p).toMatchObject({ kind: 'local', seed: { lineage: 'rev2', ours: [] } })
    // Not something it sent: a conflict, and nothing is carried over.
    const q = plan(page({ rev: 'rev2', body: '别人的\n' }), stored({ bodyJson: doc('第二次'), sent }))
    expect(q).toMatchObject({ kind: 'conflict', seed: { lineage: 'rev1', ours: [] } })
  })

  it('a server draft alone is shown, already stored', () => {
    const p = plan(withDraft(), null)
    expect(p).toMatchObject({
      kind: 'server',
      content: { fields: { title: '原标题', subtitle: '', tags: [] }, markdown: '服务器草稿\n' },
      dropLocal: false,
    })
  })

  it('a stale server draft alone is a conflict between the draft and the file', () => {
    const file = withDraft({ baseRev: 'revOld', stale: true }, { rev: 'rev2', body: '文件现在的\n' })
    const p = plan(file, null)
    expect(p.kind).toBe('conflict')
    if (p.kind !== 'conflict') return
    expect(p.info.kind).toBe('file')
    expect(p.info.mine.body).toBe('服务器草稿\n')
    expect(p.info.theirs.body).toBe('文件现在的\n')
    expect(p.unsaved).toBe(false)
  })

  it('a local draft equal to the server draft is dropped', () => {
    const p = plan(withDraft(), stored({ bodyJson: doc('服务器草稿') }))
    expect(p).toMatchObject({ kind: 'server', dropLocal: false, token: 'old' })
  })

  it('a local draft written on top of the server draft it knows is newer, and is pushed', () => {
    const p = plan(
      withDraft({ version: 'v5' }),
      stored({ bodyJson: doc('服务器草稿，后来又写的'), sent: sentOf(serverDraft()) }),
    )
    expect(p).toMatchObject({ kind: 'local', token: 'old' })
    expect(p.kind === 'local' && p.content.doc).toEqual(doc('服务器草稿，后来又写的'))
  })

  it('a local fields-only draft following the server draft keeps the server draft body', () => {
    const p = plan(
      withDraft({ version: 'v5' }),
      stored({ fields: { title: '只改标题', subtitle: '', tags: [] }, sent: sentOf(serverDraft()) }),
    )
    expect(p).toMatchObject({ kind: 'local', content: { fields: { title: '只改标题' }, markdown: '服务器草稿\n' } })
  })

  it('a local draft that does not follow the server draft and differs from it is shown next to it, both kept', () => {
    const p = plan(
      withDraft({ version: 'v5' }),
      stored({ bodyJson: doc('另一台机器上的字'), sent: sentOf(serverDraft({ body: '更早的服务器草稿\n' })) }),
    )
    expect(p.kind).toBe('conflict')
    if (p.kind !== 'conflict') return
    expect(p.info.kind).toBe('draft')
    expect(p.info.mine.body).toBe('另一台机器上的字\n')
    expect(p.info.theirs.body).toBe('服务器草稿\n')
    expect(p.info.draft?.version).toBe('v5')
    expect(p.info.after).toBeUndefined()
    // And with no `followed` at all (written before the server had a draft).
    expect(plan(withDraft({ version: 'v5' }), stored({ bodyJson: doc('另一台机器上的字') })).kind).toBe('conflict')
  })

  it('the same when the server draft is stale: after the choice the file is weighed as well', () => {
    const file = withDraft({ baseRev: 'revOld', stale: true, version: 'v5' }, { rev: 'rev2', body: '文件现在的\n' })
    const p = plan(file, stored({ baseRev: 'revOld', bodyJson: doc('本地的字') }))
    expect(p.kind === 'conflict' && p.info.kind).toBe('draft')
    expect(p.kind === 'conflict' && p.info.after?.rev).toBe('rev2')
  })

  it('a local draft following a stale server draft is "mine" against the file', () => {
    const file = withDraft({ baseRev: 'revOld', stale: true, version: 'v5' }, { rev: 'rev2', body: '文件现在的\n' })
    const p = plan(
      file,
      stored({ baseRev: 'revOld', bodyJson: doc('本地继续写的'), sent: sentOf(serverDraft({ baseRev: 'revOld' })) }),
    )
    expect(p.kind).toBe('conflict')
    if (p.kind !== 'conflict') return
    expect(p.info.kind).toBe('file')
    expect(p.info.mine.body).toBe('本地继续写的\n')
    expect(p.info.theirs.body).toBe('文件现在的\n')
    expect(p.unsaved).toBe(true)
  })

  it('no branch drops a text the server does not hold: every kind of plan carries the local content or says dropLocal for a copy the server has', () => {
    const cases: [Page, StoredDraft][] = [
      [page(), stored({ bodyJson: doc('甲') })],
      [page({ rev: 'rev2' }), stored({ bodyJson: doc('乙') })],
      [withDraft(), stored({ bodyJson: doc('丙') })],
      [withDraft({ version: 'v5' }), stored({ bodyJson: doc('丁'), sent: sentOf(serverDraft()) })],
      [withDraft({ baseRev: 'x', stale: true }, { rev: 'rev2' }), stored({ bodyJson: doc('戊') })],
    ]
    for (const [p, d] of cases) {
      const r = plan(p, d)
      expect(r.kind === 'file' || r.kind === 'server' ? r.dropLocal === true : true).toBe(
        r.kind === 'file' || r.kind === 'server' ? true : true,
      )
      if (r.kind === 'conflict')
        expect(JSON.stringify(r.info.mine)).toContain(String(d.bodyJson?.content?.[0]?.content?.[0]?.text))
      if (r.kind === 'local') expect(r.content.doc).toEqual(d.bodyJson)
    }
  })

  it('a restored local draft is then stored normally, on the revision it was typed on', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    const p = plan(page(), stored({ bodyJson: doc('恢复的内容') }))
    expect(p.kind).toBe('local')
    s.live.bodyChanged()
    s.editor.current = doc('恢复的内容')
    s.controller.markDirty()
    await vi.advanceTimersByTimeAsync(1500)
    expect(s.puts[0]).toMatchObject({ baseRev: 'rev1', body: '恢复的内容\n' })
  })

  it('a conflict on open puts the controller in the conflict state, ready for keep-mine', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    const theirs = page({ rev: 'rev2', body: '别人改的\n' })
    const mine = { title: '原标题', subtitle: '', tags: [] as string[], body: '我的\n' }
    s.live.bodyChanged()
    s.editor.current = doc('我的')
    s.controller.enterConflict({ kind: 'file', theirs, mine }, true)
    expect(s.controller.state).toBe('conflict')
    expect(s.onConflict).toHaveBeenCalledWith({ kind: 'file', theirs, mine })
    void s.controller.keepMine()
    await vi.advanceTimersByTimeAsync(0)
    expect(s.puts[0]).toMatchObject({ baseRev: 'rev2', body: '我的\n' })
  })
})

describe('a browser copy of text the server had confirmed as its draft decides nothing on opening', () => {
  const confirmedCopy = (version: string): StoredDraft => ({
    v: 1,
    id: ID,
    baseRev: 'rev1',
    fields: { title: '原标题', subtitle: '', tags: [] },
    bodyJson: doc('昨天写的'),
    savedAt: 1,
    token: 'tok',
    sent: [],
    confirmed: version,
  })

  it('the draft was committed and the page edited elsewhere since: the file is shown, no question, the copy goes', () => {
    const p = plan(page({ rev: 'rev3', body: '后来在手机上改的\n' }), confirmedCopy('v1'))
    expect(p).toMatchObject({ kind: 'file', dropLocal: true })
  })

  it("that draft is still the server's: it is shown, and the copy stays with it", () => {
    const p = plan(page({ draft: serverDraft({ version: 'v1', body: '昨天写的\n' }) }), confirmedCopy('v1'))
    expect(p).toMatchObject({ kind: 'server', dropLocal: false, token: 'tok' })
  })

  it('another draft replaced it (typed on top of it elsewhere): that one is shown, the copy goes', () => {
    const p = plan(
      page({ draft: serverDraft({ version: 'v9', body: '昨天写的，手机上又补了\n' }) }),
      confirmedCopy('v1'),
    )
    expect(p).toMatchObject({ kind: 'server', dropLocal: true })
    expect('token' in p && p.token).toBeFalsy()
  })

  it('a controller marks the copy confirmed once the store is answered, and unmarks it when more is typed', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '我的字' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(loadDraft(ID, storage)?.confirmed).toBeUndefined()
    s.answerPut({ version: 'vA' })
    await vi.advanceTimersByTimeAsync(0)
    expect(loadDraft(ID, storage)).toMatchObject({ confirmed: 'vA' })
    s.type({ title: '原标题', body: '我的字，继续' })
    await vi.advanceTimersByTimeAsync(700)
    expect(loadDraft(ID, storage)?.confirmed).toBeUndefined()
    expect(loadDraft(ID, storage)?.bodyJson).toEqual(doc('我的字，继续'))
  })
})

describe('coming back to the tab', () => {
  it('with nothing waiting, asks the server once and shows what changed elsewhere', async () => {
    const storage = new FakeStorage()
    const changed = vi.fn()
    const s = session({ storage, onExternalChange: changed })
    s.controller.noticeVisible()
    await vi.advanceTimersByTimeAsync(0)
    expect(s.pending.get.length).toBe(1)
    const fresh = page({ rev: 'rev2', body: '手机上改的\n' })
    s.pending.get[0](fresh)
    await vi.advanceTimersByTimeAsync(0)
    expect(changed).toHaveBeenCalledWith(fresh)
  })

  it('with text still to store, asks nothing: the store finds out by itself', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '我的字' })
    s.controller.noticeVisible()
    await vi.advanceTimersByTimeAsync(0)
    expect(s.pending.get.length).toBe(0)
  })

  it('nothing changed elsewhere: nothing happens', async () => {
    const storage = new FakeStorage()
    const changed = vi.fn()
    const s = session({ storage, onExternalChange: changed })
    s.controller.noticeVisible()
    await vi.advanceTimersByTimeAsync(0)
    s.pending.get[0](page())
    await vi.advanceTimersByTimeAsync(0)
    expect(changed).not.toHaveBeenCalled()
    expect(s.controller.state).toBe('saved')
  })
})

describe('a refresh replaces the editor text only when nothing is unsaved', () => {
  it('with a confirmed draft, a file changed under it comes back with the draft stale: both go to the caller, who raises the conflict', async () => {
    const storage = new FakeStorage()
    const changed = vi.fn()
    const s = session({ storage, onExternalChange: changed })
    s.type({ title: '原标题', body: '我的字' })
    await vi.advanceTimersByTimeAsync(1500)
    s.answerPut()
    await vi.advanceTimersByTimeAsync(0)
    expect(s.controller.state).toBe('drafted')
    s.controller.noticeExternalSave('rev2')
    await vi.advanceTimersByTimeAsync(0)
    // The server keeps a draft when the page is saved directly: it is stale now.
    const mine = s.puts[0]
    const fresh = page({
      rev: 'rev2',
      body: '别人保存的\n',
      draft: { version: 'v2', baseRev: 'rev1', meta: mine.meta, body: mine.body, updated: 'u2', stale: true },
    })
    s.pending.get[0](fresh)
    await vi.advanceTimersByTimeAsync(0)
    expect(changed).toHaveBeenCalledWith(fresh)
    // What the caller does with it: the person's draft against the file.
    const p = plan(fresh, null)
    expect(p.kind).toBe('conflict')
    if (p.kind === 'conflict') {
      expect(p.info.kind).toBe('file')
      expect(p.info.mine.body).toBe('我的字\n')
      expect(p.info.theirs.body).toBe('别人保存的\n')
    }
  })

  it('typed text is never replaced: with something unsaved the answer goes through the one decision', async () => {
    const storage = new FakeStorage()
    const changed = vi.fn()
    const s = session({ storage, onExternalChange: changed })
    s.type({ title: '原标题', body: '我的字' })
    await vi.advanceTimersByTimeAsync(1500)
    s.answerPut()
    await vi.advanceTimersByTimeAsync(0)
    s.type({ title: '原标题', body: '我的字，又写了' })
    s.controller.noticeExternalSave('rev2')
    await vi.advanceTimersByTimeAsync(0)
    // Something is unsaved: no question is asked, the text is stored and the server's answer decides.
    expect(s.pending.get.length).toBe(0)
    expect(changed).not.toHaveBeenCalled()
    expect(s.puts.length).toBe(2)
  })

  it("with a confirmed draft gone from the server (committed, or replaced or deleted by a request naming it) the server's page is shown", async () => {
    const storage = new FakeStorage()
    const changed = vi.fn()
    const s = session({ storage, onExternalChange: changed })
    s.type({ title: '原标题', body: '我的字' })
    await vi.advanceTimersByTimeAsync(1500)
    s.answerPut()
    await vi.advanceTimersByTimeAsync(0)
    s.controller.noticeExternalDraft(null)
    await vi.advanceTimersByTimeAsync(0)
    const fresh = page()
    s.pending.get[0](fresh)
    await vi.advanceTimersByTimeAsync(0)
    expect(changed).toHaveBeenCalledWith(fresh)
    expect(s.onConflict).not.toHaveBeenCalled()
  })

  it('with nothing typed, nothing held and nothing sent, the page is shown and the browser copy is cleared', async () => {
    const storage = new FakeStorage()
    const changed = vi.fn()
    const s = session({ storage, onExternalChange: changed })
    s.controller.noticeExternalSave('rev2')
    await vi.advanceTimersByTimeAsync(0)
    const fresh = page({ rev: 'rev2', body: '别处改的\n' })
    s.pending.get[0](fresh)
    await vi.advanceTimersByTimeAsync(0)
    expect(changed).toHaveBeenCalledWith(fresh)
    expect(s.onConflict).not.toHaveBeenCalled()
  })

  it('an observation that shows the text is in the file clears the browser copy', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '我的字' })
    await vi.advanceTimersByTimeAsync(1500)
    // The answer is lost; the server had stored it and then committed it (the sweeper).
    s.pending.putFail[0](new ApiError(0, 'network', ''))
    await vi.advanceTimersByTimeAsync(0)
    expect(loadDraft(ID, storage)).not.toBeNull()
    void s.controller.retry()
    await vi.advanceTimersByTimeAsync(0)
    const input = s.puts[0]
    s.pending.get[0](page({ rev: 'rev2', meta: input.meta, body: input.body }))
    await vi.advanceTimersByTimeAsync(0)
    expect(s.onConflict).not.toHaveBeenCalled()
    expect(loadDraft(ID, storage)).toBeNull()
  })
})

describe('storage that misbehaves', () => {
  const content: DraftContent = {
    baseRev: 'rev1',
    fields: { title: 't', subtitle: '', tags: [] },
    savedAt: Date.now(),
    token: 'tk',
    bodyJson: doc('x'),
  }
  const fields: Fields = { title: 't', subtitle: '', tags: [] }

  it('reading that throws is the same as no draft', () => {
    const storage = new FakeStorage()
    saveDraft(ID, content, storage)
    storage.failGet = true
    expect(loadDraft(ID, storage)).toBeNull()
  })

  it('writing that throws returns false and typing and storing carry on', async () => {
    const storage = new FakeStorage()
    storage.failSet = true
    expect(saveDraft(ID, content, storage)).toBe(false)
    const s = session({ storage })
    s.type({ title: '原标题', body: '照常' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(s.puts).toHaveLength(1)
    s.controller.flushKeepalive()
    s.controller.persistNow()
    expect(s.controller.state).toBe('storing')
    s.answerPut()
    await vi.advanceTimersByTimeAsync(0)
    expect(s.controller.state).toBe('drafted')
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
  const content = (savedAt: number): DraftContent => ({
    baseRev: 'r',
    fields: { title: 't', subtitle: '', tags: [] },
    savedAt,
    token: 'tk',
    bodyJson: doc('x'),
  })

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
  const draft = {
    v: 1 as const,
    id: ID,
    baseRev: 'r',
    fields: { title: '写了一半', subtitle: '副', tags: ['x'] },
    savedAt: Date.now(),
    token: 'tk',
    bodyJson: doc('没存上的话'),
  }

  it('the draft becomes the fields and body of a new page, in the root folder', () => {
    expect(draftToCreateInput(draft, toMd)).toEqual({
      folder: '',
      meta: { title: '写了一半', subtitle: '副', tags: ['x'] },
      body: '没存上的话\n',
    })
    expect(
      draftToCreateInput({ ...draft, fields: { title: '', subtitle: '', tags: [] }, bodyJson: undefined }, toMd),
    ).toEqual({ folder: '', meta: {}, body: '' })
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
    expect(create).toHaveBeenCalledWith({
      folder: '',
      meta: { title: '写了一半', subtitle: '副', tags: ['x'] },
      body: '没存上的话\n',
    })
    expect(loadDraft(ID, storage)).toBeNull()
  })
})

describe('two tabs on one page', () => {
  it("storing in one tab does not remove the other tab's newer local draft", async () => {
    const storage = new FakeStorage()
    const a = session({ storage })
    const b = session({ storage })
    a.type({ title: '原标题', body: 'A 的字' })
    await vi.advanceTimersByTimeAsync(800) // A's draft written
    b.type({ title: '原标题', body: 'B 的字' })
    await vi.advanceTimersByTimeAsync(800) // B's draft written over it; A's store (1500) is now out
    expect(a.puts).toHaveLength(1)
    expect(loadDraft(ID, storage)?.bodyJson).toEqual(doc('B 的字'))
    a.answerPut()
    await vi.advanceTimersByTimeAsync(0)
    expect(a.controller.state).toBe('drafted')
    expect(loadDraft(ID, storage)?.bodyJson).toEqual(doc('B 的字'))
  })

  it('a tab removes its own draft once the text is in the file', async () => {
    const storage = new FakeStorage()
    const a = session({ storage })
    a.type({ title: '原标题', body: 'A' })
    await vi.advanceTimersByTimeAsync(1500)
    a.answerPut()
    await vi.advanceTimersByTimeAsync(0)
    expect(loadDraft(ID, storage)).not.toBeNull()
    void a.controller.commit()
    await vi.advanceTimersByTimeAsync(0)
    a.answerCommit()
    await vi.advanceTimersByTimeAsync(0)
    expect(loadDraft(ID, storage)).toBeNull()
  })

  it('a store announces the file rev and the draft for other tabs; a commit announces the new rev and no draft', async () => {
    const storage = new FakeStorage()
    const a = session({ storage })
    a.type({ title: '原标题', body: 'A' })
    await vi.advanceTimersByTimeAsync(1500)
    a.answerPut({ version: 'vZ' })
    await vi.advanceTimersByTimeAsync(0)
    expect(parseSavedMarker(storage.getItem(savedMarkerKey(ID)))).toMatchObject({ rev: 'rev1', draft: 'vZ' })
    void a.controller.commit()
    await vi.advanceTimersByTimeAsync(0)
    a.answerCommit()
    await vi.advanceTimersByTimeAsync(0)
    const m = parseSavedMarker(storage.getItem(savedMarkerKey(ID)))
    expect(m?.rev).toMatch(/^revC/)
    expect(m?.draft).toBeNull()
    expect(parseSavedMarker('nonsense')).toBeNull()
    expect(parseSavedMarker(null)).toBeNull()
    // A marker from before drafts existed still reads.
    expect(parseSavedMarker(JSON.stringify({ rev: 'r', at: 1 }))).toEqual({ rev: 'r', draft: null, at: 1 })
  })

  it('the other tab, with nothing unsaved, is told to reload', async () => {
    const storage = new FakeStorage()
    const reload = vi.fn()
    const b = session({ storage, onExternalChange: reload })
    b.controller.noticeExternalSave('rev1') // its own rev: nothing new
    await vi.advanceTimersByTimeAsync(0)
    expect(b.pending.get).toHaveLength(0)
    b.controller.noticeExternalSave('rev2')
    await vi.advanceTimersByTimeAsync(0)
    b.pending.get[0](page({ rev: 'rev2' }))
    await vi.advanceTimersByTimeAsync(0)
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('the other tab, with unsaved changes, keeps its base and meets the stale conflict', async () => {
    const storage = new FakeStorage()
    const reload = vi.fn()
    const b = session({ storage, onExternalChange: reload })
    b.type({ title: '原标题', body: 'B 的字' })
    b.controller.noticeExternalSave('rev2')
    await vi.advanceTimersByTimeAsync(0)
    expect(reload).not.toHaveBeenCalled()
    // It stored at once, on the revision it still has.
    expect(b.puts).toHaveLength(1)
    expect(b.puts[0].baseRev).toBe('rev1')
    b.answerPut({ stale: true })
    await vi.advanceTimersByTimeAsync(0)
    b.pending.get[0](page({ rev: 'rev2', body: 'A 的字\n' }))
    await vi.advanceTimersByTimeAsync(0)
    expect(b.controller.state).toBe('conflict')
    expect(b.onConflict).toHaveBeenCalledTimes(1)
    // Nothing was lost: the server holds its text as a stale draft, which the next visit shows against the file.
    expect(b.puts[0].body).toBe('B 的字\n')
    b.type({ title: '原标题', body: 'B 的字，又写' })
    await vi.advanceTimersByTimeAsync(800)
    expect(loadDraft(ID, storage)?.bodyJson).toEqual(doc('B 的字，又写'))
  })
})

describe('after the page is left', () => {
  it('a store that succeeds with more queued points the stored local draft at what the server has now', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: 'A' })
    await vi.advanceTimersByTimeAsync(1500) // in flight
    s.type({ title: '原标题', body: 'AB' })
    const done = s.controller.detach() // the editor goes away; "AB" is queued
    await vi.advanceTimersByTimeAsync(0)
    expect(loadDraft(ID, storage)).toMatchObject({ baseRev: 'rev1' })
    s.answerPut({ version: 'vA' })
    await vi.advanceTimersByTimeAsync(0)
    // The queued store is out; the local draft follows the draft just stored.
    expect(s.puts[1].baseRev).toBe('rev1')
    expect(loadDraft(ID, storage)).toMatchObject({
      baseRev: 'rev1',
      bodyJson: doc('AB'),
      sent: expect.arrayContaining([
        oursOf('rev1', { meta: { id: ID, title: '原标题', created: '2026-01-01' }, body: 'A\n' }),
        oursOf('rev1', { meta: { id: ID, title: '原标题', created: '2026-01-01' }, body: 'AB\n' }),
      ]),
    })
    // It fails: next visit the local draft is pushed on top of the server draft, not mistaken for an unrelated one.
    s.pending.putFail[1](new ApiError(0, 'network', ''))
    await done
    const withDraft = page({ draft: serverDraft({ version: 'vA', body: 'A\n' }) })
    expect(plan(withDraft, loadDraft(ID, storage)).kind).toBe('local')
  })
})

describe('a store refused because another device replaced the draft', () => {
  it('stores nothing, keeps the local draft with the text, and raises the conflict; the other draft is not committed by leaving', async () => {
    const storage = new FakeStorage()
    const s = session({ storage })
    s.type({ title: '原标题', body: '这边的字' })
    await vi.advanceTimersByTimeAsync(1500)
    const other = serverDraft({ version: 'vOther', body: '那边的字\n' })
    s.pending.putFail[0](new ApiError(409, 'conflict', '', page({ draft: undefined }), undefined, other))
    await vi.advanceTimersByTimeAsync(0)
    expect(s.controller.state).toBe('conflict')
    expect(s.onConflict.mock.calls[0][0].kind).toBe('draft')
    expect(loadDraft(ID, storage)?.bodyJson).toEqual(doc('这边的字'))
    // Leave: no commit request goes out, and the next visit sees both.
    await s.controller.detach()
    expect(s.pending.commit).toHaveLength(0)
    const next = plan(page({ draft: other }), loadDraft(ID, storage))
    expect(next.kind).toBe('conflict')
    expect(next.kind === 'conflict' && next.info.kind).toBe('draft')
  })
})

describe('local drafts of a person, a space and a page', () => {
  const draft = (): DraftContent => ({
    baseRev: 'r',
    fields: { title: 't', subtitle: '', tags: [] },
    savedAt: Date.now(),
    token: 'tk',
    bodyJson: doc('x'),
  })

  it("a second user of the same browser is never offered the first one's text, nor another space's", () => {
    const storage = new FakeStorage()
    const mine = draftRef('userA', 'spaceA', ID)
    saveDraft(mine, draft(), storage)
    expect(loadDraft(mine, storage)).not.toBeNull()
    expect(loadDraft(draftRef('userB', 'spaceA', ID), storage)).toBeNull()
    expect(loadDraft(draftRef('userA', 'spaceB', ID), storage)).toBeNull()
  })

  it('in single-user mode a milestone 1.1 draft (keyed by the page id) moves to the new key and is not orphaned', () => {
    const storage = new FakeStorage()
    storage.setItem(`pb.draft.${ID}`, JSON.stringify({ v: 1, id: ID, ...draft() }))
    const ref = draftRefFor('local', 'default', ID, storage)
    expect(ref).toBe(draftRef('local', 'default', ID))
    expect(loadDraft(ref, storage)?.bodyJson).toEqual(doc('x'))
    expect(storage.getItem(`pb.draft.${ID}`)).toBeNull()
  })

  it('in accounts mode an old draft is not handed to whoever opens the page first', () => {
    const storage = new FakeStorage()
    storage.setItem(`pb.draft.${ID}`, JSON.stringify({ v: 1, id: ID, ...draft() }))
    const ref = draftRefFor('userB', 'spaceA', ID, storage)
    expect(loadDraft(ref, storage)).toBeNull()
    expect(storage.getItem(`pb.draft.${ID}`)).not.toBeNull()
  })

  it('a draft already under the new key is not overwritten by an old one', () => {
    const storage = new FakeStorage()
    const ref = draftRef('local', 'default', ID)
    saveDraft(ref, { ...draft(), bodyJson: doc('new') }, storage)
    storage.setItem(`pb.draft.${ID}`, JSON.stringify({ v: 1, id: ID, ...draft() }))
    draftRefFor('local', 'default', ID, storage)
    expect(loadDraft(ref, storage)?.bodyJson).toEqual(doc('new'))
  })

  it('a draft of the same content on another base, or with other content, is not ours even when everything else looks alike', () => {
    const d: StoredDraft = {
      v: 1,
      id: ID,
      baseRev: 'rev1',
      fields: { title: '原标题', subtitle: '', tags: [] },
      bodyJson: doc('我的'),
      savedAt: Date.now(),
      token: 'old',
      sent: sentOf(serverDraft({ body: '我发过的\n' })),
    }
    expect(plan(page({ draft: serverDraft({ body: '别人发的\n' }) }), d).kind).toBe('conflict')
    expect(plan(page({ draft: serverDraft({ body: '我发过的\n', baseRev: 'revElse' }) }), d).kind).toBe('conflict')
  })

  it('the file is the text this local draft followed, committed by the server: not a conflict, the text goes up on the new file', () => {
    const followed = serverDraft({ body: '我发过的\n' })
    const d: StoredDraft = {
      v: 1,
      id: ID,
      baseRev: 'rev1',
      fields: { title: '原标题', subtitle: '', tags: [] },
      bodyJson: doc('我后来又写的'),
      savedAt: Date.now(),
      token: 'old',
      sent: sentOf(followed),
    }
    const p = plan(page({ rev: 'revCommitted', meta: followed.meta, body: followed.body }), d)
    expect(p.kind).toBe('local')
  })

  it('the file moved and is not the text this local draft followed: a conflict', () => {
    const followed = serverDraft({ body: '我发过的\n' })
    const d: StoredDraft = {
      v: 1,
      id: ID,
      baseRev: 'rev1',
      fields: { title: '原标题', subtitle: '', tags: [] },
      bodyJson: doc('我后来又写的'),
      savedAt: Date.now(),
      token: 'old',
      sent: sentOf(followed),
    }
    expect(plan(page({ rev: 'revElse', body: '别人写的\n' }), d).kind).toBe('conflict')
  })
})
