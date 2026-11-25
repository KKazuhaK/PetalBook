import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/api/client'
import type { DraftInput, DraftReceipt, Page, PageDraft } from '@/api/types'
import {
  applyFields,
  cleanFields,
  fieldsOf,
  flushAllSaves,
  SaveController,
  sameContent,
  waitForSaves,
  type ConflictInfo,
  type Draft,
} from './saveController'

const ID = '01JD3K8Q7Z'
const page = (over: Partial<Page> = {}): Page => ({
  id: ID,
  path: 'a.md',
  folder: '',
  rev: 'rev1',
  updated: '2026-11-21T08:30:00Z',
  meta: { id: ID, title: '原标题', series: '冬季', created: '2026-01-01', tags: ['甲'] },
  body: '* 一\n* 二\n',
  ...over,
})

type Call =
  | {
      kind: 'put'
      input: DraftInput
      keepalive: boolean
      resolve: (r: DraftReceipt) => void
      reject: (e: unknown) => void
    }
  | { kind: 'delete'; version: string; resolve: () => void; reject: (e: unknown) => void }
  | { kind: 'commit'; version: string; resolve: (p: Page) => void; reject: (e: unknown) => void }
  | { kind: 'get'; resolve: (p: Page) => void; reject: (e: unknown) => void }

export function setup(
  opts: { page?: Page; draft?: PageDraft; draftFields?: Partial<Draft>; normalizeBody?: (s: string) => string } = {},
) {
  const start = opts.page ?? page()
  let current: Draft = { title: '原标题', subtitle: '', source: [], tags: ['甲'], ...opts.draftFields }
  const calls: Call[] = []
  let clock = 0
  const api = {
    putDraft: vi.fn(
      (_id: string, input: DraftInput, o: { keepalive: boolean }) =>
        new Promise<DraftReceipt>(
          (resolve, reject) => void calls.push({ kind: 'put', input, keepalive: o.keepalive, resolve, reject }),
        ),
    ),
    deleteDraft: vi.fn(
      (...[, version]: [id: string, version: string, rev: string]) =>
        new Promise<void>((resolve, reject) => void calls.push({ kind: 'delete', version, resolve, reject })),
    ),
    commit: vi.fn(
      (_id: string, version: string) =>
        new Promise<Page>((resolve, reject) => void calls.push({ kind: 'commit', version, resolve, reject })),
    ),
    getPage: vi.fn(() => new Promise<Page>((resolve, reject) => void calls.push({ kind: 'get', resolve, reject }))),
  }
  const onConflict = vi.fn<(info: ConflictInfo) => void>()
  const onCommitted = vi.fn<(p: Page) => void>()
  const onExternalChange = vi.fn<(p: Page) => void>()
  const onStored = vi.fn<(id: string) => void>()
  const read = vi.fn(() => current)
  const c = new SaveController({
    page: start,
    draft: opts.draft,
    read,
    api,
    scope: 'sp',
    onConflict,
    onCommitted,
    onStored,
    onExternalChange,
    normalizeBody: opts.normalizeBody,
  })
  c.attach()
  const edit = (d: Partial<Draft>) => {
    current = { ...current, ...d }
    c.markDirty()
  }
  const puts = () => calls.filter((x): x is Extract<Call, { kind: 'put' }> => x.kind === 'put')
  const last = <K extends Call['kind']>(kind: K) =>
    calls.filter((x) => x.kind === kind).at(-1) as Extract<Call, { kind: K }>
  const settle = () => vi.advanceTimersByTimeAsync(0)
  // Answer the newest draft store as the server would.
  const answerPut = async (over: Partial<DraftReceipt> = {}) => {
    const p = last('put')
    p.resolve({ version: `ver${clock + 1}`, baseRev: p.input.baseRev, updated: `u${++clock}`, stale: false, ...over })
    await settle()
  }
  // Answer the newest commit: the file takes the stored draft.
  const answerCommit = async (over: Partial<Page> = {}) => {
    const stored = puts().at(-1)?.input
    last('commit').resolve(
      page({ rev: `rev${++clock + 1}`, ...(stored ? { meta: stored.meta, body: stored.body } : {}), ...over }),
    )
    await settle()
  }
  return {
    c,
    api,
    calls,
    edit,
    puts,
    last,
    settle,
    answerPut,
    answerCommit,
    onConflict,
    onCommitted,
    onStored,
    onExternalChange,
    read,
    setContent: (d: Partial<Draft>) => (current = { ...current, ...d }),
  }
}

// What the server's GET shows after our store: the file, and the draft we just stored.
const afterStore = (put: { input: DraftInput }, version: string, file: Page, stale = true): Page => ({
  ...file,
  draft: { version, baseRev: put.input.baseRev, meta: put.input.meta, body: put.input.body, updated: 'u', stale },
})

const serverDraft = (over: Partial<PageDraft> = {}): PageDraft => ({
  version: 'ver0',
  baseRev: 'rev1',
  meta: { id: ID, title: '草稿标题', series: '冬季', created: '2026-01-01', tags: ['甲'] },
  body: '草稿正文\n',
  updated: 'u0',
  stale: false,
  ...over,
})

beforeEach(() => vi.useFakeTimers())
afterEach(() => vi.useRealTimers())

describe('autosave stores a draft, never the file', () => {
  it('waits 1.5 s after the last change and then stores once', async () => {
    const t = setup()
    t.edit({ title: '新' })
    expect(t.c.state).toBe('dirty')
    await vi.advanceTimersByTimeAsync(1000)
    t.edit({ title: '新标题' })
    await vi.advanceTimersByTimeAsync(1400)
    expect(t.api.putDraft).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(200)
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
    expect(t.c.state).toBe('storing')
    expect(t.puts()[0].input.baseRev).toBe('rev1')
    expect(t.puts()[0].input.meta.title).toBe('新标题')
    await t.answerPut()
    expect(t.c.state).toBe('drafted')
  })

  it('says when a draft was stored, so that closing the tab afterwards still counts as writing', async () => {
    const t = setup()
    t.edit({ title: '新' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.onStored).not.toHaveBeenCalled()
    await t.answerPut()
    expect(t.onStored).toHaveBeenCalledTimes(1)
    expect(t.onStored).toHaveBeenCalledWith(t.c.page.id)
  })

  it('never commits by itself: the file is for commit to write', async () => {
    const t = setup()
    t.edit({ title: '新' })
    await vi.advanceTimersByTimeAsync(1500)
    await t.answerPut()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(t.api.commit).not.toHaveBeenCalled()
    expect(t.c.page.rev).toBe('rev1')
  })

  it('does not read (serialise) the editor on every change', async () => {
    const t = setup()
    for (let i = 0; i < 50; i++) t.edit({ body: `正文 ${i}` })
    expect(t.read).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1600)
    expect(t.read).toHaveBeenCalledTimes(1)
  })

  it('later stores keep the file rev as baseRev: a draft does not move the file', async () => {
    const t = setup()
    t.edit({ title: 'A' })
    await vi.advanceTimersByTimeAsync(1500)
    await t.answerPut()
    t.edit({ title: 'AB' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.puts()[1].input.baseRev).toBe('rev1')
  })
})

describe('no overlapping requests', () => {
  it('stores an edit made while a store is out only after it, and only when the person pauses', async () => {
    const t = setup()
    t.edit({ title: 'A' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)

    t.edit({ title: 'AB' })
    await vi.advanceTimersByTimeAsync(1500) // the debounce fires while the first request is still out
    t.edit({ title: 'ABC' })
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
    expect(t.c.state).toBe('storing')

    await t.answerPut() // first answer: still typing (timer pending), so no request yet
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
    expect(t.c.state).toBe('dirty')
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.api.putDraft).toHaveBeenCalledTimes(2)
    expect(t.puts()[1].input.meta.title).toBe('ABC')
    await t.answerPut()
    expect(t.c.state).toBe('drafted')
  })

  it('an edit whose pause has already passed is stored right behind the request that was out', async () => {
    const t = setup()
    t.edit({ title: 'A' })
    await vi.advanceTimersByTimeAsync(1500)
    t.edit({ title: 'AB' })
    await vi.advanceTimersByTimeAsync(1500)
    await t.answerPut()
    expect(t.api.putDraft).toHaveBeenCalledTimes(2)
    expect(t.puts()[1].input.meta.title).toBe('AB')
  })

  it('waitForSaves resolves once the queue is empty', async () => {
    const t = setup()
    t.edit({ title: 'A' })
    await vi.advanceTimersByTimeAsync(1500)
    let done = false
    void waitForSaves('sp', ID).then(() => (done = true))
    await t.settle()
    expect(done).toBe(false)
    await t.answerPut()
    expect(done).toBe(true)
  })
})

describe('commit ("save version")', () => {
  it('stores the typed text, then writes it to the file', async () => {
    const t = setup()
    t.edit({ title: '新标题' })
    const done = t.c.commit()
    await t.settle()
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
    expect(t.api.commit).not.toHaveBeenCalled()
    await t.answerPut()
    expect(t.c.state).toBe('committing')
    expect(t.api.commit).toHaveBeenCalledTimes(1)
    await t.answerCommit()
    await done
    expect(t.c.state).toBe('saved')
    expect(t.c.page.rev).not.toBe('rev1')
    expect(t.c.page.meta.title).toBe('新标题')
    expect(t.onCommitted).toHaveBeenCalledTimes(1)
  })

  it('with the draft already stored, only commits', async () => {
    const t = setup()
    t.edit({ title: 'X' })
    await vi.advanceTimersByTimeAsync(1500)
    await t.answerPut()
    void t.c.commit()
    await t.settle()
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
    expect(t.api.commit).toHaveBeenCalledTimes(1)
  })

  it('while a draft store is in flight, waits for it and then commits: one store, one commit', async () => {
    const t = setup()
    t.edit({ title: 'X' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
    const done = t.c.commit()
    await t.settle()
    expect(t.api.commit).not.toHaveBeenCalled()
    await t.answerPut()
    expect(t.api.commit).toHaveBeenCalledTimes(1)
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
    await t.answerCommit()
    await done
    expect(t.c.state).toBe('saved')
  })

  it('while a store is in flight and more was typed, stores the newer text before committing', async () => {
    const t = setup()
    t.edit({ title: 'A' })
    await vi.advanceTimersByTimeAsync(1500)
    t.edit({ title: 'AB' })
    void t.c.commit()
    await t.answerPut()
    expect(t.api.commit).not.toHaveBeenCalled()
    expect(t.puts()).toHaveLength(2)
    expect(t.puts()[1].input.meta.title).toBe('AB')
    await t.answerPut()
    expect(t.api.commit).toHaveBeenCalledTimes(1)
  })

  it('does nothing when there is nothing to commit', async () => {
    const t = setup()
    await t.c.commit()
    expect(t.calls).toHaveLength(0)
    expect(t.c.state).toBe('saved')
  })

  it('commits a server draft the page was opened on, without storing again', async () => {
    const t = setup({ draft: serverDraft() })
    expect(t.c.state).toBe('drafted')
    void t.c.commit()
    await t.settle()
    expect(t.api.putDraft).not.toHaveBeenCalled()
    expect(t.api.commit).toHaveBeenCalledTimes(1)
  })

  it('typing during a commit is stored on the new rev afterwards', async () => {
    const t = setup()
    t.edit({ title: 'A' })
    void t.c.commit()
    await t.settle()
    await t.answerPut()
    expect(t.c.state).toBe('committing')
    t.edit({ title: 'AB' }) // while the commit is out
    expect(t.c.state).toBe('committing')
    await t.answerCommit({ rev: 'revC' })
    // The pause has not passed yet, so the new text waits for it.
    expect(t.c.state).toBe('dirty')
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.puts()[1].input.baseRev).toBe('revC')
    expect(t.puts()[1].input.meta.title).toBe('AB')
    await t.answerPut()
    expect(t.c.state).toBe('drafted')
    // That was autosave: it does not commit again.
    expect(t.api.commit).toHaveBeenCalledTimes(1)
  })

  it('typing during a commit and undoing it again leaves the committed file as it is', async () => {
    const t = setup()
    t.edit({ title: 'A' })
    void t.c.commit()
    await t.settle()
    await t.answerPut()
    t.edit({ title: 'AB' })
    t.setContent({ title: 'A' })
    t.c.markDirty()
    await t.answerCommit()
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
    expect(t.api.deleteDraft).not.toHaveBeenCalled()
    expect(t.c.state).toBe('saved')
  })
})

describe('unchanged content is never sent', () => {
  it('opening a page and leaving sends nothing', async () => {
    const t = setup()
    await t.c.detach()
    expect(t.calls).toHaveLength(0)
    expect(t.read).not.toHaveBeenCalled()
  })

  it('opening a page that has a server draft and leaving without typing sends nothing either', async () => {
    const t = setup({ draft: serverDraft() })
    await t.c.detach()
    expect(t.calls).toHaveLength(0)
  })

  it('a change that is undone before the store sends nothing', async () => {
    const t = setup()
    t.edit({ title: '改了' })
    t.edit({ title: '原标题' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.calls).toHaveLength(0)
    expect(t.c.state).toBe('saved')
  })

  it('a change stored and then undone deletes the draft, so the server cannot commit the undone text', async () => {
    const t = setup()
    t.edit({ title: '改了' })
    await vi.advanceTimersByTimeAsync(1500)
    await t.answerPut()
    t.edit({ title: '原标题' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.api.deleteDraft).toHaveBeenCalledTimes(1)
    t.last('delete').resolve()
    await t.settle()
    expect(t.c.state).toBe('saved')
    // Leaving now has nothing to commit.
    await t.c.detach()
    expect(t.api.commit).not.toHaveBeenCalled()
  })

  it('a body edited and restored to the normalised form of the original sends nothing', async () => {
    const t = setup({ normalizeBody: (s) => s.replace(/^\* /gm, '- ') })
    t.edit({ body: '- 一\n- 二\n' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.calls).toHaveLength(0)
    expect(t.c.state).toBe('saved')
  })

  it('whitespace around title and tags is not a change', async () => {
    const t = setup()
    t.edit({ title: '  原标题 ', tags: [' 甲', '甲'] })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.calls).toHaveLength(0)
  })

  it('a title-only edit sends the original body string unchanged', async () => {
    const t = setup()
    t.edit({ title: '新标题' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.puts()[0].input.body).toBe('* 一\n* 二\n')
  })

  it('text equal to the server draft it was opened on sends nothing', async () => {
    const t = setup({ draft: serverDraft(), draftFields: { title: '草稿标题' } })
    t.setContent({ body: '草稿正文\n' })
    t.c.markDirty()
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.calls).toHaveLength(0)
    expect(t.c.state).toBe('drafted')
  })
})

describe('a page opened on its server draft', () => {
  it('builds the next draft on the draft, keeping its header fields', async () => {
    const draft = serverDraft({ meta: { id: ID, title: '草稿标题', zeta: { n: 1 }, created: '2026-01-01' } })
    const t = setup({ draft, draftFields: { title: '草稿标题', tags: [] } })
    t.edit({ body: '又改了\n' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.puts()[0].input).toEqual({ prev: 'ver0', baseRev: 'rev1', meta: draft.meta, body: '又改了\n' })
  })

  it('text brought back to the file while the server holds another draft deletes that draft', async () => {
    const t = setup({ draft: serverDraft(), draftFields: { title: '草稿标题' } })
    t.setContent({ title: '原标题', tags: ['甲'], body: '* 一\n* 二\n' })
    t.c.markDirty()
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.api.deleteDraft).toHaveBeenCalledTimes(1)
    expect(t.api.putDraft).not.toHaveBeenCalled()
  })
})

describe('stale: the file moved on', () => {
  const theirs = () =>
    page({ rev: 'revX', meta: { id: ID, title: '别人的标题', created: '2026-01-01' }, body: '别人写的\n' })

  async function stale() {
    const t = setup()
    t.edit({ title: '我的标题', body: '我写的\n' })
    await vi.advanceTimersByTimeAsync(1500)
    await t.answerPut({ stale: true, version: 'verS' })
    expect(t.api.getPage).toHaveBeenCalledTimes(1)
    t.last('get').resolve(afterStore(t.puts()[0], 'verS', theirs()))
    await t.settle()
    return t
  }

  it('a stale store fetches the file and raises a conflict with both versions; the text is kept', async () => {
    const t = await stale()
    expect(t.c.state).toBe('conflict')
    expect(t.onConflict).toHaveBeenCalledTimes(1)
    const info = t.onConflict.mock.calls[0][0]
    expect(info.kind).toBe('file')
    expect(info.theirs.body).toBe('别人写的\n')
    expect(info.mine).toMatchObject({ title: '我的标题', body: '我写的\n' })
    // Typing on does not talk to the server.
    t.edit({ body: '我又写了一点\n' })
    await vi.advanceTimersByTimeAsync(5000)
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
    expect(t.c.state).toBe('conflict')
  })

  it('keep mine stores the draft again on the file rev; then it can be committed', async () => {
    const t = await stale()
    void t.c.keepMine()
    await t.settle()
    expect(t.api.putDraft).toHaveBeenCalledTimes(2)
    expect(t.puts()[1].input.baseRev).toBe('revX')
    expect(t.puts()[1].input.body).toBe('我写的\n')
    expect(t.puts()[1].input.meta.title).toBe('我的标题')
    await t.answerPut()
    expect(t.c.state).toBe('drafted')
    void t.c.commit()
    await t.settle()
    expect(t.api.commit).toHaveBeenCalledTimes(1)
  })

  it('keep mine sends what the dialog showed as mine: an untouched body stays the one on screen', async () => {
    const t = setup()
    t.edit({ title: '我的标题' })
    await vi.advanceTimersByTimeAsync(1500)
    await t.answerPut({ stale: true, version: 'verS' })
    t.last('get').resolve(afterStore(t.puts()[0], 'verS', theirs()))
    await t.settle()
    void t.c.keepMine()
    await t.settle()
    // The editor still shows the body the person had; "mine" is not half theirs.
    expect(t.puts()[1].input.body).toBe(t.puts()[0].input.body)
    expect(t.puts()[1].input.body).not.toBe('别人写的\n')
    expect(t.puts()[1].input.baseRev).toBe('revX')
  })

  it('take theirs deletes the draft and shows the file', async () => {
    const t = await stale()
    const shown = t.c.useTheirs()
    expect(shown.rev).toBe('revX')
    expect(t.c.page.rev).toBe('revX')
    expect(t.c.hasUnsaved()).toBe(false)
    await t.settle()
    expect(t.api.deleteDraft).toHaveBeenCalledTimes(1)
    // The delete names the file rev the controller holds, so the server can refuse a stale belief.
    expect(t.api.deleteDraft.mock.calls[0][2]).toBe('revX')
    t.last('delete').resolve()
    await t.settle()
    expect(t.c.state).toBe('saved')
    // Later edits are stored against the new rev.
    t.setContent({ title: '别人的标题', tags: [] })
    t.edit({ title: '别人的标题，我补一句' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.puts()[1].input.baseRev).toBe('revX')
  })

  it('a 409 from commit is the same conflict', async () => {
    const t = setup({ draft: serverDraft() })
    // The editor shows the draft it was opened on.
    t.setContent({ title: '草稿标题', body: '草稿正文\n' })
    void t.c.commit()
    await t.settle()
    t.last('commit').reject(
      new ApiError(409, 'conflict', '页面已被修改', theirs(), undefined, serverDraft({ stale: true })),
    )
    await t.settle()
    expect(t.c.state).toBe('conflict')
    const info = t.onConflict.mock.calls[0][0]
    expect(info.kind).toBe('file')
    expect(info.theirs.rev).toBe('revX')
    expect(info.mine).toMatchObject({ title: '草稿标题', body: '草稿正文\n' })
  })

  it('a conflict found after the page was left waits for the next visit and does not call into the page', async () => {
    const t = setup()
    t.edit({ title: '离开前写的' })
    const done = t.c.detach()
    await t.settle()
    await t.answerPut({ stale: true })
    t.last('get').resolve(theirs())
    await done
    expect(t.c.state).toBe('conflict')
    expect(t.onConflict).not.toHaveBeenCalled()
    expect(t.api.commit).not.toHaveBeenCalled()
  })
})

describe('every request names the draft it means', () => {
  const other = (over: Partial<PageDraft> = {}) =>
    serverDraft({
      version: 'verOther',
      meta: { id: ID, title: '别处的标题', created: '2026-01-01' },
      body: '别处的正文\n',
      ...over,
    })
  const refused = (draft: PageDraft | null, current: Page) =>
    new ApiError(409, 'conflict', '草稿不是这一份', current, undefined, draft)

  it('a store carries "" while there is no draft, then the version it got back', async () => {
    const t = setup()
    t.edit({ title: 'A' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.puts()[0].input.prev).toBe('')
    await t.answerPut({ version: 'verA' })
    t.edit({ title: 'AB' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.puts()[1].input.prev).toBe('verA')
  })

  it('a commit carries the version of the draft it means', async () => {
    const t = setup()
    t.edit({ title: 'A' })
    void t.c.commit()
    await t.settle()
    await t.answerPut({ version: 'verA' })
    expect(t.last('commit').version).toBe('verA')
  })

  it('a delete carries the version it removes', async () => {
    const t = setup()
    t.edit({ title: '改了' })
    await vi.advanceTimersByTimeAsync(1500)
    await t.answerPut({ version: 'verA' })
    t.edit({ title: '原标题' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.last('delete').version).toBe('verA')
  })

  // The sequence that used to end in a wiped file: our draft was committed by
  // the server, another device then committed X, and "save version" was pressed.
  it('commit refused, draft gone, the file is something else: a conflict, never "saved"', async () => {
    const t = setup()
    t.edit({ title: '我的', body: '我的字\n' })
    await vi.advanceTimersByTimeAsync(1500)
    await t.answerPut({ version: 'verA' })
    void t.c.commit()
    await t.settle()
    const x = page({ rev: 'revX', meta: { id: ID, title: '另一台设备', created: '2026-01-01' }, body: 'X 的字\n' })
    t.last('commit').reject(refused(null, x))
    await t.settle()
    expect(t.c.state).toBe('conflict')
    expect(t.onCommitted).not.toHaveBeenCalled()
    const info = t.onConflict.mock.calls[0][0]
    expect(info.kind).toBe('file')
    expect(info.theirs.body).toBe('X 的字\n')
    expect(info.mine).toMatchObject({ title: '我的', body: '我的字\n' })
    // Keeping mine stores on X's rev, replacing nothing blindly: prev is "" (no draft there).
    void t.c.keepMine()
    await t.settle()
    expect(t.puts()[1].input).toMatchObject({ prev: '', baseRev: 'revX', body: '我的字\n' })
  })

  it('commit refused, draft gone, the file is exactly our draft: it was committed already, saved', async () => {
    const t = setup()
    t.edit({ title: '我的', body: '我的字\n' })
    await vi.advanceTimersByTimeAsync(1500)
    const stored = t.puts()[0].input
    await t.answerPut({ version: 'verA' })
    void t.c.commit()
    await t.settle()
    t.last('commit').reject(refused(null, page({ rev: 'revS', meta: stored.meta, body: stored.body })))
    await t.settle()
    expect(t.c.state).toBe('saved')
    expect(t.c.page.rev).toBe('revS')
    expect(t.onConflict).not.toHaveBeenCalled()
    expect(t.onCommitted).toHaveBeenCalledTimes(1)
  })

  it('commit refused because another draft is there: a conflict between my text and that draft', async () => {
    const t = setup()
    t.edit({ title: '我的', body: '我的字\n' })
    await vi.advanceTimersByTimeAsync(1500)
    await t.answerPut({ version: 'verA' })
    void t.c.commit()
    await t.settle()
    t.last('commit').reject(refused(other(), page()))
    await t.settle()
    const info = t.onConflict.mock.calls[0][0]
    expect(info.kind).toBe('draft')
    expect(info.mine.title).toBe('我的')
    expect(info.theirs.meta.title).toBe('别处的标题')
    // Keep mine replaces that draft: the store names it.
    void t.c.keepMine()
    await t.settle()
    expect(t.puts()[1].input.prev).toBe('verOther')
  })

  it('a store refused because another device stored a draft: nothing is stored, the text stays, a conflict with that draft', async () => {
    const t = setup()
    t.edit({ title: '这边的字' })
    await vi.advanceTimersByTimeAsync(1500)
    t.last('put').reject(refused(other(), page()))
    await t.settle()
    expect(t.c.state).toBe('conflict')
    expect(t.c.hasUnsaved()).toBe(true)
    const info = t.onConflict.mock.calls[0][0]
    expect(info.kind).toBe('draft')
    expect(info.mine.title).toBe('这边的字')
    expect(info.draft?.version).toBe('verOther')
    // Leaving now commits nothing: the other draft is not ours to commit.
    await t.c.detach()
    expect(t.api.commit).not.toHaveBeenCalled()
  })

  it('a store refused with no draft there, the file being our last draft (past the maximum draft age): re-based and stored again, no dialog', async () => {
    const t = setup()
    t.edit({ title: '第一版' })
    await vi.advanceTimersByTimeAsync(1500)
    const first = t.puts()[0].input
    await t.answerPut({ version: 'verA' })
    t.edit({ title: '第二版' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.puts()[1].input.prev).toBe('verA')
    t.last('put').reject(refused(null, page({ rev: 'revS', meta: first.meta, body: first.body })))
    await t.settle()
    expect(t.onConflict).not.toHaveBeenCalled()
    expect(t.onCommitted).toHaveBeenCalledTimes(1)
    expect(t.puts()).toHaveLength(3)
    expect(t.puts()[2].input).toMatchObject({ prev: '', baseRev: 'revS' })
    expect(t.puts()[2].input.meta.title).toBe('第二版')
    await t.answerPut()
    expect(t.c.state).toBe('drafted')
  })

  it('a store refused with no draft there, the file being something else: a conflict, the text kept', async () => {
    const t = setup()
    t.edit({ title: '第一版' })
    await vi.advanceTimersByTimeAsync(1500)
    await t.answerPut({ version: 'verA' })
    t.edit({ title: '第二版' })
    await vi.advanceTimersByTimeAsync(1500)
    t.last('put').reject(
      refused(null, page({ rev: 'revX', meta: { id: ID, title: '别人', created: '2026-01-01' }, body: '别人的字\n' })),
    )
    await t.settle()
    expect(t.c.state).toBe('conflict')
    expect(t.onCommitted).not.toHaveBeenCalled()
    expect(t.onConflict.mock.calls[0][0]).toMatchObject({ kind: 'file', mine: { title: '第二版' } })
    expect(t.c.hasUnsaved()).toBe(true)
  })

  it('a store refused with no draft there and the file unchanged (the draft was deleted elsewhere): stored again', async () => {
    const t = setup({ draft: serverDraft() })
    t.setContent({ title: '草稿标题', body: '草稿正文\n' })
    t.edit({ title: '又改了' })
    await vi.advanceTimersByTimeAsync(1500)
    t.last('put').reject(refused(null, page()))
    await t.settle()
    expect(t.onConflict).not.toHaveBeenCalled()
    expect(t.puts()).toHaveLength(2)
    expect(t.puts()[1].input.prev).toBe('')
  })

  it('a notice that another tab stored a draft, arriving while a request is out, is acted on afterwards', async () => {
    const t = setup()
    t.edit({ title: '这边' })
    await vi.advanceTimersByTimeAsync(1500)
    t.c.noticeExternalDraft('verOther')
    await t.settle()
    expect(t.api.getPage).not.toHaveBeenCalled()
    await t.answerPut({ version: 'verA' })
    await t.settle()
    expect(t.api.getPage).toHaveBeenCalledTimes(1)
    // Our text was confirmed as the server's draft and nothing is unsaved. The server
    // replaces a draft only for a request that names its version, so the other tab
    // had ours in front of it: what is there now succeeds it, and is shown.
    const fresh = page({ draft: other() })
    t.last('get').resolve(fresh)
    await t.settle()
    expect(t.onExternalChange).toHaveBeenCalledWith(fresh)
    expect(t.onConflict).not.toHaveBeenCalled()
    expect(t.c.state).toBe('drafted')
  })

  it('a notice that another tab committed, arriving while a request is out, is acted on afterwards', async () => {
    const t = setup({ draft: serverDraft() })
    void t.c.commit()
    await t.settle()
    t.c.noticeExternalSave('revElse')
    await t.settle()
    expect(t.api.getPage).not.toHaveBeenCalled()
    await t.answerCommit({ rev: 'revC' })
    await t.settle()
    expect(t.api.getPage).toHaveBeenCalledTimes(1)
  })
})

describe('a store whose answer was lost', () => {
  const lostFirst = async () => {
    const t = setup()
    t.edit({ title: '第一版', body: '第一版的字\n' })
    await vi.advanceTimersByTimeAsync(1500)
    const sent = t.puts()[0].input
    t.last('put').reject(new ApiError(0, 'network', '')) // it landed, but we never heard
    await t.settle()
    return { t, sent }
  }

  it('retrying meets a draft that is our own text: adopt its version, no conflict', async () => {
    const { t, sent } = await lostFirst()
    void t.c.retry()
    await t.settle()
    // Nothing new is sent first: the server is asked what it holds.
    expect(t.puts()).toHaveLength(1)
    t.last('get').resolve(
      page({ draft: serverDraft({ version: 'verLanded', baseRev: sent.baseRev, meta: sent.meta, body: sent.body }) }),
    )
    await t.settle()
    expect(t.onConflict).not.toHaveBeenCalled()
    expect(t.c.state).toBe('drafted')
    // The next store replaces that version.
    t.edit({ title: '第二版' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.puts().at(-1)!.input.prev).toBe('verLanded')
  })

  it("...but a draft that differs in the header, with the same body, is another person's and a conflict", async () => {
    const { t, sent } = await lostFirst()
    void t.c.retry()
    await t.settle()
    t.last('get').resolve(
      page({
        draft: serverDraft({
          version: 'verOther',
          baseRev: sent.baseRev,
          meta: { ...sent.meta, title: '别处的标题' },
          body: sent.body,
        }),
      }),
    )
    await t.settle()
    expect(t.c.state).toBe('conflict')
    expect(t.onConflict.mock.calls[0][0].kind).toBe('draft')
  })

  // After a store whose answer was lost, nothing new is sent before the server is asked what
  // it holds: here it committed that store (it is the file now), and the newer text goes on top.
  it('more typing, the server committed the lost draft: asked first, the file is our own text, re-based, no dialog', async () => {
    const { t, sent } = await lostFirst()
    t.edit({ title: '第二版', body: '第二版的字\n' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.puts()).toHaveLength(1)
    t.last('get').resolve(page({ rev: 'revS', meta: sent.meta, body: sent.body }))
    await t.settle()
    expect(t.onConflict).not.toHaveBeenCalled()
    expect(t.c.state).not.toBe('conflict')
    expect(t.puts()).toHaveLength(2)
    expect(t.puts()[1].input).toMatchObject({ prev: '', baseRev: 'revS', body: '第二版的字\n' })
    expect(t.onCommitted).toHaveBeenCalledTimes(1)
    await t.answerPut()
    expect(t.c.state).toBe('drafted')
    expect(t.c.hasUnsaved()).toBe(false)
  })

  it('...but a file that is not what was sent is a conflict, even with the same body', async () => {
    const { t, sent } = await lostFirst()
    t.edit({ title: '第二版', body: '第二版的字\n' })
    await vi.advanceTimersByTimeAsync(1500)
    t.last('get').resolve(page({ rev: 'revX', meta: { ...sent.meta, title: '别人改的标题' }, body: sent.body }))
    await t.settle()
    expect(t.c.state).toBe('conflict')
    expect(t.onConflict.mock.calls[0][0].kind).toBe('file')
  })

  // A 200 `stale: true` still happens (the file changed from outside after we looked); then the file is
  // looked at, and it is a conflict unless it is our own text.
  it('a store answered stale (the file changed from outside) is a conflict with the text kept', async () => {
    const t = setup()
    t.edit({ title: '我的' })
    await vi.advanceTimersByTimeAsync(1500)
    await t.answerPut({ version: 'verS', stale: true })
    t.last('get').resolve(afterStore(t.puts()[0], 'verS', page({ rev: 'revX', body: '别人改的\n' })))
    await t.settle()
    expect(t.c.state).toBe('conflict')
  })

  it('with a draft known, a lost store that was then committed: the file the server shows is ours', async () => {
    const t = setup()
    t.edit({ title: '一', body: '一的字\n' })
    await vi.advanceTimersByTimeAsync(1500)
    await t.answerPut({ version: 'verA' })
    t.edit({ title: '二', body: '二的字\n' })
    await vi.advanceTimersByTimeAsync(1500)
    const second = t.puts()[1].input
    expect(second.prev).toBe('verA')
    t.last('put').reject(new ApiError(0, 'network', '')) // landed, unheard
    await t.settle()
    t.edit({ title: '三', body: '三的字\n' })
    await vi.advanceTimersByTimeAsync(1500)
    // Asked first; the sweeper committed the second text meanwhile.
    expect(t.puts()).toHaveLength(2)
    t.last('get').resolve(page({ rev: 'revS', meta: second.meta, body: second.body }))
    await t.settle()
    expect(t.onConflict).not.toHaveBeenCalled()
    expect(t.puts()[2].input).toMatchObject({ prev: '', baseRev: 'revS', body: '三的字\n' })
  })

  it('undoing back to the file while a store is unconfirmed asks the server, and deletes the draft of ours that is there, with its version', async () => {
    const { t, sent } = await lostFirst()
    t.setContent({ title: '原标题', body: '* 一\n* 二\n' })
    t.c.markDirty()
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.api.getPage).toHaveBeenCalledTimes(1)
    t.last('get').resolve(
      page({ draft: serverDraft({ version: 'verLanded', baseRev: sent.baseRev, meta: sent.meta, body: sent.body }) }),
    )
    await t.settle()
    expect(t.api.deleteDraft).toHaveBeenCalledTimes(1)
    expect(t.last('delete').version).toBe('verLanded')
    t.last('delete').resolve()
    await t.settle()
    expect(t.c.state).toBe('saved')
    expect(t.onConflict).not.toHaveBeenCalled()
  })

  it('undoing back while a store is unconfirmed, and the server has already committed it: the file is re-based and the undo is stored', async () => {
    const { t, sent } = await lostFirst()
    t.setContent({ title: '原标题', body: '* 一\n* 二\n' })
    t.c.markDirty()
    await vi.advanceTimersByTimeAsync(1500)
    t.last('get').resolve(page({ rev: 'revS', meta: sent.meta, body: sent.body }))
    await t.settle()
    expect(t.puts()).toHaveLength(2)
    expect(t.puts()[1].input).toMatchObject({ prev: '', baseRev: 'revS', body: '* 一\n* 二\n' })
  })

  it('with nothing unconfirmed, undoing back to the file sends nothing, as before', async () => {
    const t = setup()
    t.edit({ title: '改了' })
    t.edit({ title: '原标题' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.calls).toHaveLength(0)
  })
})

describe('a slow answer to "what does the server have?"', () => {
  it('is dropped when the person typed and a store succeeded since: it never overwrites newer state', async () => {
    const t = setup({ draft: serverDraft() })
    t.setContent({ title: '草稿标题', body: '草稿正文\n' })
    t.c.noticeExternalDraft(null) // another tab deleted the draft: the question goes out
    await t.settle()
    expect(t.api.getPage).toHaveBeenCalledTimes(1)
    t.edit({ title: '我又写了' })
    await vi.advanceTimersByTimeAsync(1500)
    await t.answerPut({ version: 'verMine' })
    expect(t.c.state).toBe('drafted')
    // The old answer: no draft, the file.
    t.last('get').resolve(page())
    await t.settle()
    expect(t.onExternalChange).not.toHaveBeenCalled()
    expect(t.c.state).toBe('drafted')
  })

  it('is dropped when the person typed meanwhile, even before anything was sent', async () => {
    const t = setup()
    t.c.noticeExternalSave('revElse')
    await t.settle()
    t.edit({ title: '正在写' })
    t.last('get').resolve(page({ rev: 'revElse', body: '别处的\n' }))
    await t.settle()
    expect(t.onExternalChange).not.toHaveBeenCalled()
    expect(t.c.page.rev).toBe('rev1')
  })

  it('still applies when nothing happened since it was asked', async () => {
    const t = setup()
    t.c.noticeExternalSave('revElse')
    await t.settle()
    t.last('get').resolve(page({ rev: 'revElse' }))
    await t.settle()
    expect(t.onExternalChange).toHaveBeenCalledTimes(1)
  })
})

describe('a versioned delete the server refuses because the draft is gone', () => {
  const refused = (current: Page) => new ApiError(409, 'conflict', '草稿已经没有了', current, undefined, null)
  const X = { title: '原标题', body: '* 一\n* 二\n' }
  const yFile = (sent: { meta: PageDraft['meta']; body: string }) =>
    page({ rev: 'revY', meta: sent.meta, body: sent.body })

  // Type Y, stored as draft V1, the server commits V1 on its own (we are not
  // told), then the text is undone back to X: the file keeps Y.
  async function sweptThenUndone() {
    const t = setup()
    t.edit({ title: 'Y', body: 'Y 的字\n' })
    await vi.advanceTimersByTimeAsync(1500)
    const sent = t.puts()[0].input
    await t.answerPut({ version: 'verV1' })
    t.setContent(X)
    t.c.markDirty()
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.last('delete').version).toBe('verV1')
    return { t, sent }
  }

  it('is never "saved" on its own: the file holds our older text, so the undone text is stored on top of it', async () => {
    const { t, sent } = await sweptThenUndone()
    t.last('delete').reject(refused(yFile(sent)))
    await t.settle()
    expect(t.c.state).not.toBe('saved')
    expect(t.onConflict).not.toHaveBeenCalled()
    expect(t.c.page.rev).toBe('revY')
    expect(t.puts()).toHaveLength(2)
    expect(t.puts()[1].input).toMatchObject({ prev: '', baseRev: 'revY', body: '* 一\n* 二\n' })
    expect(t.puts()[1].input.meta.title).toBe('原标题')
    await t.answerPut()
    expect(t.c.state).toBe('drafted')
    expect(t.onCommitted).toHaveBeenCalledTimes(1)
  })

  it('is "saved" when the file has not moved and is exactly the editor\'s text', async () => {
    const { t } = await sweptThenUndone()
    t.last('delete').reject(refused(page()))
    await t.settle()
    expect(t.c.state).toBe('saved')
    expect(t.c.page.rev).toBe('rev1')
    expect(t.puts()).toHaveLength(1)
  })

  it('is a file conflict when the file is something else', async () => {
    const { t } = await sweptThenUndone()
    t.last('delete').reject(
      refused(page({ rev: 'revZ', body: '别人写的\n', meta: { id: ID, title: '别人', created: '2026-01-01' } })),
    )
    await t.settle()
    expect(t.c.state).toBe('conflict')
    expect(t.onConflict.mock.calls[0][0]).toMatchObject({ kind: 'file', mine: { title: '原标题' } })
  })

  it('variant: a store was lost, the text is undone; the draft that landed is ours and is committed before the delete: stored on top, not saved', async () => {
    const t = setup()
    t.edit({ title: 'Y', body: 'Y 的字\n' })
    await vi.advanceTimersByTimeAsync(1500)
    const sent = t.puts()[0].input
    t.last('put').reject(new ApiError(0, 'network', ''))
    await t.settle()
    t.setContent(X)
    t.c.markDirty()
    await vi.advanceTimersByTimeAsync(1500)
    // The page is asked what it holds: our draft (version known now).
    t.last('get').resolve(
      page({ draft: serverDraft({ version: 'verLanded', baseRev: sent.baseRev, meta: sent.meta, body: sent.body }) }),
    )
    await t.settle()
    expect(t.last('delete').version).toBe('verLanded')
    // The sweep lands between that answer and the delete.
    t.last('delete').reject(refused(yFile(sent)))
    await t.settle()
    expect(t.c.state).not.toBe('saved')
    expect(t.onConflict).not.toHaveBeenCalled()
    expect(t.puts()[1].input).toMatchObject({ prev: '', baseRev: 'revY', body: '* 一\n* 二\n' })
  })

  it('variant: the draft was confirmed, the sweep landed, then a store was lost and the text undone: still not "saved"', async () => {
    const t = setup()
    t.edit({ title: 'Y', body: 'Y 的字\n' })
    await vi.advanceTimersByTimeAsync(1500)
    const first = t.puts()[0].input
    await t.answerPut({ version: 'verA' })
    t.edit({ title: 'Z', body: 'Z 的字\n' })
    await vi.advanceTimersByTimeAsync(1500)
    t.last('put').reject(new ApiError(0, 'network', ''))
    await t.settle()
    t.setContent(X)
    t.c.markDirty()
    await vi.advanceTimersByTimeAsync(1500)
    // The lost store makes the server be asked first: its draft is gone, the file is the first text.
    t.last('get').resolve(yFile(first))
    await t.settle()
    expect(t.c.state).not.toBe('saved')
    expect(t.onConflict).not.toHaveBeenCalled()
    expect(t.puts().at(-1)!.input).toMatchObject({ prev: '', baseRev: 'revY', body: '* 一\n* 二\n' })
  })

  it('after "take theirs" what was discarded is no longer ours: a delete refused with that text in the file is a file conflict', async () => {
    const t = setup({ draft: serverDraft({ baseRev: 'revOld', stale: true }) })
    t.c.enterConflict(
      {
        kind: 'file',
        theirs: page({ rev: 'revT' }),
        mine: { title: '草稿标题', subtitle: '', source: [], tags: [], body: '草稿正文\n' },
      },
      false,
    )
    t.c.useTheirs()
    await t.settle()
    expect(t.last('delete').version).toBe('ver0')
    t.last('delete').reject(refused(page({ rev: 'revC', meta: serverDraft().meta, body: '草稿正文\n' })))
    await t.settle()
    expect(t.c.state).toBe('conflict')
    expect(t.puts()).toHaveLength(0)
  })
})

describe('failures at each step, and recovery', () => {
  it('a failed draft store keeps the text, reports it, and retries on demand', async () => {
    const t = setup()
    t.edit({ title: '新' })
    await vi.advanceTimersByTimeAsync(1500)
    t.last('put').reject(new ApiError(0, 'network', '无法连接到服务器'))
    await t.settle()
    expect(t.c.state).toBe('error')
    expect(t.c.hasUnsaved()).toBe(true)
    void t.c.retry()
    await t.settle()
    // The failed store may have landed: the server is asked before anything is sent again.
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
    t.last('get').resolve(page())
    await t.settle()
    expect(t.api.putDraft).toHaveBeenCalledTimes(2)
    expect(t.c.state).toBe('storing')
    expect(t.puts()[1].input.baseRev).toBe('rev1')
    await t.answerPut()
    expect(t.c.state).toBe('drafted')
  })

  it('typing again after a failure retries by itself at the next pause', async () => {
    const t = setup()
    t.edit({ title: '新' })
    await vi.advanceTimersByTimeAsync(1500)
    t.last('put').reject(new ApiError(0, 'network', ''))
    await t.settle()
    t.edit({ title: '新的' })
    expect(t.c.state).toBe('dirty')
    await vi.advanceTimersByTimeAsync(1500)
    // The attempt at the next pause is the question about what the server holds.
    expect(t.api.getPage).toHaveBeenCalledTimes(1)
    t.last('get').resolve(page())
    await t.settle()
    expect(t.api.putDraft).toHaveBeenCalledTimes(2)
  })

  it('coming back online retries a network failure, and only that', async () => {
    const t = setup()
    t.edit({ title: '新' })
    await vi.advanceTimersByTimeAsync(1500)
    t.last('put').reject(new ApiError(500, 'internal', '坏了'))
    await t.settle()
    t.c.noticeOnline()
    await t.settle()
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
    t.edit({ title: '新的' })
    await vi.advanceTimersByTimeAsync(1500)
    // A 500 may have landed too: the next step is the question, then the store.
    t.last('get').resolve(page())
    await t.settle()
    t.last('put').reject(new ApiError(0, 'network', ''))
    await t.settle()
    t.c.noticeOnline()
    await t.settle()
    // Online again: the question first, then the store.
    expect(t.api.getPage).toHaveBeenCalledTimes(2)
    t.last('get').resolve(page())
    await t.settle()
    expect(t.api.putDraft).toHaveBeenCalledTimes(3)
  })

  it('a failed commit leaves the draft on the server and retries the commit', async () => {
    const t = setup()
    t.edit({ title: '新' })
    void t.c.commit()
    await t.settle()
    await t.answerPut()
    t.last('commit').reject(new ApiError(0, 'network', ''))
    await t.settle()
    expect(t.c.state).toBe('error')
    expect(t.c.hasUnsaved()).toBe(false)
    void t.c.retry()
    await t.settle()
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
    expect(t.api.commit).toHaveBeenCalledTimes(2)
    await t.answerCommit()
    expect(t.c.state).toBe('saved')
  })

  it('a "save version" whose draft store failed is repeated whole by retry: the store, then the commit', async () => {
    const t = setup()
    t.edit({ title: '新' })
    void t.c.commit()
    await t.settle()
    t.last('put').reject(new ApiError(0, 'network', ''))
    await t.settle()
    expect(t.c.state).toBe('error')
    expect(t.api.commit).not.toHaveBeenCalled()
    void t.c.retry()
    await t.settle()
    t.last('get').resolve(page())
    await t.settle()
    await t.answerPut()
    expect(t.api.commit).toHaveBeenCalledTimes(1)
    await t.answerCommit()
    expect(t.c.state).toBe('saved')
  })

  it('typing after a failed commit does not turn the next autosave into a commit', async () => {
    const t = setup()
    t.edit({ title: '新' })
    void t.c.commit()
    await t.settle()
    await t.answerPut()
    t.last('commit').reject(new ApiError(0, 'network', ''))
    await t.settle()
    t.edit({ title: '新的' })
    await vi.advanceTimersByTimeAsync(1500)
    await t.answerPut()
    expect(t.api.commit).toHaveBeenCalledTimes(1)
    expect(t.c.state).toBe('drafted')
  })

  it('a failed delete of an undone draft is retried before anything else', async () => {
    const t = setup()
    t.edit({ title: '改了' })
    await vi.advanceTimersByTimeAsync(1500)
    await t.answerPut()
    t.edit({ title: '原标题' })
    await vi.advanceTimersByTimeAsync(1500)
    t.last('delete').reject(new ApiError(0, 'network', ''))
    await t.settle()
    expect(t.c.state).toBe('error')
    void t.c.retry()
    await t.settle()
    expect(t.api.deleteDraft).toHaveBeenCalledTimes(2)
    t.last('delete').resolve()
    await t.settle()
    expect(t.c.state).toBe('saved')
  })

  it('a failure fetching the file after a stale answer is retried from the store', async () => {
    const t = setup()
    t.edit({ title: '我的' })
    await vi.advanceTimersByTimeAsync(1500)
    await t.answerPut({ stale: true })
    t.last('get').reject(new ApiError(0, 'network', ''))
    await t.settle()
    expect(t.c.state).toBe('error')
    void t.c.retry()
    await t.settle()
    expect(t.api.putDraft).toHaveBeenCalledTimes(2)
  })

  it('an editor that cannot be serialised ends in the error state instead of throwing', async () => {
    const t = setup()
    t.read.mockImplementation(() => {
      throw new Error('no Markdown form for block node "x"')
    })
    t.edit({ title: '新' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.c.state).toBe('error')
    expect(t.calls).toHaveLength(0)
  })
})

describe('signed out in the middle of editing', () => {
  it('a 401 is an error with the text kept; leaving afterwards sends nothing more', async () => {
    const t = setup()
    t.edit({ title: '写到一半' })
    await vi.advanceTimersByTimeAsync(1500)
    t.last('put').reject(new ApiError(401, 'unauthenticated', '登录已失效'))
    await t.settle()
    expect(t.c.state).toBe('error')
    expect((t.c.getSnapshot().error as ApiError).status).toBe(401)
    expect(t.c.hasUnsaved()).toBe(true)
    // The app sends the person to the sign-in page, which unmounts the editor.
    await t.c.detach()
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
    expect(t.api.commit).not.toHaveBeenCalled()
  })

  it('a 401 on commit is an error too, and the stored draft stays on the server', async () => {
    const t = setup({ draft: serverDraft() })
    void t.c.commit()
    await t.settle()
    t.last('commit').reject(new ApiError(401, 'unauthenticated', ''))
    await t.settle()
    expect(t.c.state).toBe('error')
    expect(t.api.deleteDraft).not.toHaveBeenCalled()
  })
})

describe('leaving the page', () => {
  it('commits what was typed: the store, then the commit, from the content captured at once', async () => {
    const t = setup()
    t.edit({ title: '离开前改的' })
    const done = t.c.detach()
    // The editor is gone from now on; the controller must not read it again.
    t.read.mockImplementation(() => {
      throw new Error('editor destroyed')
    })
    await t.settle()
    expect(t.puts()[0].input.meta.title).toBe('离开前改的')
    await t.answerPut()
    expect(t.api.commit).toHaveBeenCalledTimes(1)
    await t.answerCommit()
    await done
    expect(t.c.state).toBe('saved')
    expect(t.onCommitted).toHaveBeenCalledTimes(1)
  })

  it('with the draft already stored, only commits', async () => {
    const t = setup()
    t.edit({ title: '改' })
    await vi.advanceTimersByTimeAsync(1500)
    await t.answerPut()
    const done = t.c.detach()
    await t.settle()
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
    await t.answerCommit()
    await done
  })

  it('while a store is out with more typed behind it, stores the rest from the captured text and commits', async () => {
    const t = setup()
    t.edit({ title: '一' })
    await vi.advanceTimersByTimeAsync(1500)
    t.edit({ title: '二' })
    const done = t.c.detach()
    t.read.mockImplementation(() => {
      throw new Error('editor destroyed')
    })
    await t.answerPut()
    expect(t.puts()[1].input.meta.title).toBe('二')
    await t.answerPut()
    expect(t.api.commit).toHaveBeenCalledTimes(1)
    await t.answerCommit()
    await done
  })

  it('a commit that fails after the page is gone leaves the draft on the server for its own commit', async () => {
    const t = setup()
    t.edit({ title: '改' })
    const done = t.c.detach()
    await t.settle()
    await t.answerPut()
    t.last('commit').reject(new ApiError(0, 'network', ''))
    await done
    expect(t.c.state).toBe('error')
    expect(t.onConflict).not.toHaveBeenCalled()
  })

  it('typed and undone: nothing is committed', async () => {
    const t = setup()
    t.edit({ title: '改了' })
    t.edit({ title: '原标题' })
    await t.c.detach()
    expect(t.calls).toHaveLength(0)
  })

  it('discard drops unsaved changes for good, even through a later detach', async () => {
    const t = setup()
    t.edit({ title: '要删掉的页面' })
    t.c.discard()
    await t.c.detach()
    await vi.advanceTimersByTimeAsync(5000)
    expect(t.calls).toHaveLength(0)
  })

  it('suspend then attach resumes autosave (a failed delete keeps the page alive)', async () => {
    const t = setup()
    t.edit({ title: '删除失败的页面' })
    t.c.suspend()
    await vi.advanceTimersByTimeAsync(5000)
    expect(t.calls).toHaveLength(0)
    t.c.attach()
    t.c.markDirty()
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
  })
})

describe('the tab is hidden or closed', () => {
  it('stores unsaved content with keepalive, once, and does not commit', async () => {
    const t = setup()
    t.edit({ title: '切走了' })
    t.c.flushKeepalive()
    t.c.flushKeepalive()
    await t.settle()
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
    expect(t.puts()[0].keepalive).toBe(true)
    await t.answerPut()
    await vi.advanceTimersByTimeAsync(3000)
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
    expect(t.api.commit).not.toHaveBeenCalled()
  })

  it('with nothing unsaved sends nothing', () => {
    const t = setup()
    t.c.flushKeepalive()
    expect(t.calls).toHaveLength(0)
  })

  it('while a store is out, the next one goes with keepalive', async () => {
    const t = setup()
    t.edit({ title: '一' })
    await vi.advanceTimersByTimeAsync(1500)
    t.edit({ title: '二' })
    t.c.flushKeepalive()
    await t.answerPut()
    expect(t.puts()[1].keepalive).toBe(true)
  })
})

describe('two tabs on one page', () => {
  it('another tab committed and this one has nothing unsaved: it reloads what the server has', async () => {
    const t = setup()
    t.c.noticeExternalSave('rev1') // its own rev: nothing new
    await t.settle()
    expect(t.api.getPage).not.toHaveBeenCalled()
    t.c.noticeExternalSave('rev2')
    await t.settle()
    const fresh = page({ rev: 'rev2', body: '另一个标签页写的\n' })
    t.last('get').resolve(fresh)
    await t.settle()
    expect(t.onExternalChange).toHaveBeenCalledWith(fresh)
    expect(t.c.page.rev).toBe('rev2')
    expect(t.c.state).toBe('saved')
  })

  it('another tab committed and this one has unsaved text: it stores now and meets the conflict', async () => {
    const t = setup()
    t.edit({ title: '这边的字' })
    t.c.noticeExternalSave('rev2')
    await t.settle()
    expect(t.onExternalChange).not.toHaveBeenCalled()
    expect(t.puts()).toHaveLength(1)
    expect(t.puts()[0].input.baseRev).toBe('rev1')
    await t.answerPut({ stale: true })
    t.last('get').resolve(
      page({ rev: 'rev2', body: '那边的字\n', meta: { id: ID, title: '那边的标题', created: '2026-01-01' } }),
    )
    await t.settle()
    expect(t.c.state).toBe('conflict')
    expect(t.onConflict).toHaveBeenCalledTimes(1)
  })

  it('the other tab stored a draft and this one has nothing unsaved: it shows that draft', async () => {
    const t = setup()
    t.c.noticeExternalDraft('v9')
    await t.settle()
    const fresh = page({ draft: serverDraft({ version: 'v9', updated: 'u9' }) })
    t.last('get').resolve(fresh)
    await t.settle()
    expect(t.onExternalChange).toHaveBeenCalledWith(fresh)
    expect(t.c.state).toBe('drafted')
  })

  it("the other tab stored a draft and this one has unsaved text: both are the person's, so they choose", async () => {
    const t = setup()
    t.edit({ title: '这边的字' })
    t.c.noticeExternalDraft('v9')
    await t.settle()
    t.last('get').resolve(
      page({
        draft: serverDraft({
          version: 'v9',
          updated: 'u9',
          meta: { id: ID, title: '那边的字', created: '2026-01-01' },
          body: '那边\n',
        }),
      }),
    )
    await t.settle()
    expect(t.c.state).toBe('conflict')
    const info = t.onConflict.mock.calls[0][0]
    expect(info.kind).toBe('draft')
    expect(info.mine.title).toBe('这边的字')
    expect(info.theirs.meta.title).toBe('那边的字')
  })

  it("keeping mine replaces the other tab's draft; taking theirs adopts it", async () => {
    const mk = async () => {
      const t = setup()
      t.edit({ title: '这边的字' })
      t.c.noticeExternalDraft('v9')
      await t.settle()
      t.last('get').resolve(
        page({
          draft: serverDraft({
            version: 'v9',
            updated: 'u9',
            meta: { id: ID, title: '那边的字', created: '2026-01-01' },
            body: '那边\n',
          }),
        }),
      )
      await t.settle()
      return t
    }
    const a = await mk()
    void a.c.keepMine()
    await a.settle()
    expect(a.puts()[0].input.meta.title).toBe('这边的字')
    await a.answerPut()
    expect(a.c.state).toBe('drafted')

    const b = await mk()
    const shown = b.c.useTheirs()
    expect(shown.meta.title).toBe('那边的字')
    expect(b.c.state).toBe('drafted')
    expect(b.c.hasUnsaved()).toBe(false)
    expect(b.api.putDraft).not.toHaveBeenCalled()
  })

  it('a draft announcement for the draft it already knows is ignored', async () => {
    const t = setup({ draft: serverDraft({ version: 'ver0', updated: 'u0' }) })
    t.c.noticeExternalDraft('ver0')
    await t.settle()
    expect(t.api.getPage).not.toHaveBeenCalled()
  })
})

describe('opening in a conflict', () => {
  const theirs = () => page({ rev: 'revX', body: '别人改的\n' })

  it('enterConflict puts the controller in the conflict state and keeping mine stores on the file rev', async () => {
    const t = setup()
    const mine = { title: '原标题', subtitle: '', source: [], tags: [] as string[], body: '我的\n' }
    t.c.enterConflict({ kind: 'file', theirs: theirs(), mine }, true)
    expect(t.c.state).toBe('conflict')
    expect(t.onConflict).toHaveBeenCalledWith({ kind: 'file', theirs: theirs(), mine })
    t.setContent({ body: '我的\n' })
    void t.c.keepMine()
    await t.settle()
    expect(t.puts()[0].input).toMatchObject({ baseRev: 'revX', body: '我的\n' })
  })

  it("a server draft that is stale is the person's text; taking the file deletes it", async () => {
    const t = setup({ draft: serverDraft({ baseRev: 'revOld', stale: true }) })
    t.c.enterConflict(
      {
        kind: 'file',
        theirs: theirs(),
        mine: { title: '草稿标题', subtitle: '', source: [], tags: [], body: '草稿正文\n' },
      },
      false,
    )
    t.c.useTheirs()
    await t.settle()
    expect(t.api.deleteDraft).toHaveBeenCalledTimes(1)
  })

  it("two unrelated drafts, the server's stale: after the choice the file is weighed as well", async () => {
    const t = setup({ draft: serverDraft({ baseRev: 'revOld', stale: true }) })
    const info: ConflictInfo = {
      kind: 'draft',
      theirs: page({ meta: { id: ID, title: '草稿标题' }, body: '草稿正文\n' }),
      mine: { title: '本地', subtitle: '', source: [], tags: [], body: '本地正文\n' },
      draft: {
        version: 'ver0',
        baseRev: 'revOld',
        meta: { id: ID, title: '草稿标题' },
        body: '草稿正文\n',
        updated: 'u0',
      },
      after: theirs(),
    }
    t.c.enterConflict(info, true)
    expect(t.onConflict).toHaveBeenCalledTimes(1)
    void t.c.keepMine()
    // Not stored yet: the file still has a say.
    expect(t.api.putDraft).not.toHaveBeenCalled()
    expect(t.c.state).toBe('conflict')
    expect(t.onConflict).toHaveBeenCalledTimes(2)
    const second = t.onConflict.mock.calls[1][0]
    expect(second.kind).toBe('file')
    expect(second.theirs.rev).toBe('revX')
    expect(second.mine.body).toBe('本地正文\n')
  })
})

describe('flushAllSaves', () => {
  it('stores the unsaved text of every open editor', async () => {
    const t = setup()
    t.edit({ title: '退出登录前' })
    void flushAllSaves()
    await t.settle()
    expect(t.api.putDraft).toHaveBeenCalledTimes(1)
  })
})

describe('tags and scalars as the server serves them', () => {
  it('shows a tag written as a single string, and a numeric or boolean title as text', () => {
    expect(fieldsOf({ id: 'X', tags: '栀子', title: 2026, subtitle: true })).toEqual({
      title: '2026',
      subtitle: 'true',
      source: [],
      tags: ['栀子'],
    })
    expect(fieldsOf({ id: 'X', tags: ['a', null, 7, 'b'] })).toEqual({
      title: '',
      subtitle: '',
      source: [],
      tags: ['a', 'b'],
    })
    expect(fieldsOf({ id: 'X', tags: '' }).tags).toEqual([])
    expect(fieldsOf({ id: 'X', title: null, subtitle: { a: 1 } })).toMatchObject({ title: '', subtitle: '' })
  })

  it('a string tag is not lost when another tag is added', async () => {
    const p = page({ meta: { id: ID, title: '标', tags: '栀子' } })
    const t = setup({ page: p, draftFields: { title: '标', tags: ['栀子'] } })
    t.edit({ tags: ['栀子', 'x'] })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.puts()[0].input.meta.tags).toEqual(['栀子', 'x'])
  })

  it('a string tag untouched is sent back as the string', async () => {
    const p = page({ meta: { id: ID, title: '标', tags: '栀子' } })
    const t = setup({ page: p, draftFields: { title: '标', tags: ['栀子'] } })
    t.edit({ title: '新标' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.puts()[0].input.meta.tags).toBe('栀子')
  })

  it('non-text entries of a tag list survive an edit of the tags, after the edited ones', () => {
    const meta = { id: 'X', tags: ['a', null, 7, 'b'] }
    const out = applyFields(
      meta,
      { title: '', subtitle: '', source: [], tags: ['b', 'c'] },
      { title: false, subtitle: false, source: false, tags: true },
    )
    expect(out.tags).toEqual(['b', 'c', null, 7])
    // Removing every text tag leaves the rest rather than deleting the key.
    expect(
      applyFields(
        meta,
        { title: '', subtitle: '', source: [], tags: [] },
        { title: false, subtitle: false, source: false, tags: true },
      ).tags,
    ).toEqual([null, 7])
    expect(
      'tags' in
        applyFields(
          { id: 'X', tags: ['a'] },
          { title: '', subtitle: '', source: [], tags: [] },
          { title: false, subtitle: false, source: false, tags: true },
        ),
    ).toBe(false)
  })

  it('a numeric title is rewritten only if the person changes it', async () => {
    const p = page({ meta: { id: ID, title: 2026, created: 'c' } })
    const t = setup({ page: p, draftFields: { title: '2026', tags: [] } })
    t.edit({ body: '改正文\n' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.puts()[0].input.meta.title).toBe(2026)
    await t.answerPut()
    t.edit({ title: '二〇二六' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.puts()[1].input.meta.title).toBe('二〇二六')
  })
})

describe('header handling', () => {
  it('sends unknown fields back exactly as received (the server decides their order)', async () => {
    const p = page({
      meta: { id: ID, title: '标', zeta: { a: 1 }, series: '冬季', created: '2026-01-01', alpha: [1, 2] },
    })
    const t = setup({ page: p, draftFields: { title: '标', tags: [] } })
    t.edit({ title: '改标题' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.puts()[0].input.meta).toEqual({
      id: ID,
      title: '改标题',
      zeta: { a: 1 },
      series: '冬季',
      created: '2026-01-01',
      alpha: [1, 2],
    })
  })

  it('an untitled page stays untitled: no title key, never an empty string', async () => {
    const p = page({ meta: { id: ID, created: '2026-11-21' }, body: '一句话\n' })
    const t = setup({ page: p, draftFields: { title: '', tags: [] } })
    t.edit({ body: '一句话，加一点\n' })
    await vi.advanceTimersByTimeAsync(1500)
    expect('title' in t.puts()[0].input.meta).toBe(false)
    expect('subtitle' in t.puts()[0].input.meta).toBe(false)
    expect(t.puts()[0].input.meta).toEqual({ id: ID, created: '2026-11-21' })
  })

  it('clearing a title removes the key', () => {
    const out = applyFields(
      { id: 'X', title: '甲', subtitle: '乙', created: 'c' },
      { title: '', subtitle: '乙', source: [], tags: [] },
      { title: true, subtitle: false, source: false, tags: false },
    )
    expect(out).toEqual({ id: 'X', subtitle: '乙', created: 'c' })
  })

  it('a field that was absent is added when the person fills it in', () => {
    const base = { id: 'X', created: 'c', series: 's' }
    const a = applyFields(
      base,
      { title: 'T', subtitle: 'S', source: [], tags: ['t'] },
      { title: true, subtitle: true, source: false, tags: true },
    )
    expect(a).toEqual({ id: 'X', created: 'c', series: 's', title: 'T', subtitle: 'S', tags: ['t'] })
  })

  it('never touches header values the person did not edit, whatever their type', async () => {
    const odd = page({
      meta: { id: ID, title: 5, subtitle: ['x'], tags: ['a', 'a', 7], weird: { n: null } },
      body: 'b\n',
    })
    // Shown as: title "5", no subtitle, tags ["a","a"].
    const t = setup({ page: odd, draftFields: { title: '5', subtitle: '', tags: ['a', 'a'] } })
    t.edit({ title: '5' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.calls).toHaveLength(0)
    t.edit({ body: '改了\n' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.puts()[0].input.meta).toEqual(odd.meta)
  })

  it('writes only the field that changed: others keep their served values', async () => {
    const odd = page({ meta: { id: ID, title: 5, tags: ['a', 'a'] } })
    const t = setup({ page: odd, draftFields: { title: '5', subtitle: '', tags: ['a', 'a'] } })
    t.edit({ subtitle: '新副标题' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.puts()[0].input.meta).toEqual({ id: ID, title: 5, tags: ['a', 'a'], subtitle: '新副标题' })
  })

  it('cleanFields trims, flattens newlines and de-duplicates tags', () => {
    expect(cleanFields({ title: ' 甲\n乙 ', subtitle: '', source: [' 书\n名 '], tags: [' a ', 'a', '', 'b'] })).toEqual(
      {
        title: '甲 乙',
        subtitle: '',
        source: ['书 名'],
        tags: ['a', 'b'],
      },
    )
  })

  it('sameContent ignores key order and an id or created one side leaves out, nothing else', () => {
    const a = { meta: { id: 'X', title: 't', tags: ['a'], created: 'c' }, body: 'b' }
    expect(sameContent(a, { meta: { tags: ['a'], title: 't' }, body: 'b' })).toBe(true)
    expect(sameContent(a, { meta: { id: 'X', title: 't', tags: ['b'], created: 'c' }, body: 'b' })).toBe(false)
    expect(sameContent(a, { meta: a.meta, body: 'b\n' })).toBe(false)
  })
})

describe('a rev from an answer is adopted only if the content is what the controller believes', () => {
  it('a move that changes nothing is only a new path: no request, the rev stays', async () => {
    const t = setup()
    t.c.adopt(page({ path: 'x/a.md', folder: 'x' }))
    await t.settle()
    expect(t.calls).toHaveLength(0)
    expect(t.c.page.folder).toBe('x')
    expect(t.c.page.rev).toBe('rev1')
  })

  it("a move whose answer has another rev (the page was changed meanwhile) is not adopted: with nothing typed the server's page is fetched and shown", async () => {
    const t = setup()
    t.c.adopt(page({ rev: 'revB', body: '另一台设备写的\n', path: 'x/a.md', folder: 'x' }))
    await t.settle()
    expect(t.c.page.rev).toBe('rev1')
    expect(t.api.getPage).toHaveBeenCalledTimes(1)
    const fresh = page({ rev: 'revB', body: '另一台设备写的\n' })
    t.last('get').resolve(fresh)
    await t.settle()
    expect(t.onExternalChange).toHaveBeenCalledWith(fresh)
    expect(t.c.page.rev).toBe('revB')
  })

  it('...and with text typed it goes through the one decision: a file conflict, not a quiet re-base', async () => {
    const t = setup()
    t.edit({ title: '我在写' })
    t.c.adopt(page({ rev: 'revB', body: '另一台设备写的\n' }))
    await t.settle()
    // The typed text is stored at once, on the rev it has: the server says stale, and the file is looked at.
    await t.answerPut({ stale: true, version: 'verS' })
    t.last('get').resolve(afterStore(t.puts()[0], 'verS', page({ rev: 'revB', body: '另一台设备写的\n' })))
    await t.settle()
    expect(t.c.state).toBe('conflict')
    expect(t.onConflict.mock.calls[0][0].kind).toBe('file')
  })
})

describe('what counts as ours', () => {
  const refused = (draft: PageDraft | null, current: Page) =>
    new ApiError(409, 'conflict', '', current, undefined, draft)

  it('a long offline stretch sends nothing new and ours does not grow: one store is outstanding, then the question, which fails too', async () => {
    const t = setup()
    t.edit({ title: '第一版', body: '第一版的字\n' })
    await vi.advanceTimersByTimeAsync(1500)
    const first = t.puts()[0].input
    t.last('put').reject(new ApiError(0, 'network', '')) // landed, unheard
    await t.settle()
    for (let i = 0; i < 300; i++) {
      t.edit({ title: `第${i}次`, body: `${i}\n` })
      await vi.advanceTimersByTimeAsync(31_000)
      t.last('get').reject(new ApiError(0, 'network', ''))
      await t.settle()
    }
    expect(t.puts()).toHaveLength(1)
    expect((t.c as unknown as { ours: unknown[] }).ours.length).toBeLessThan(3)
    // Back online: the server is asked, shows the first store as its draft (ours), the newest text goes up.
    t.c.noticeOnline()
    await t.settle()
    t.last('get').resolve(
      page({
        draft: serverDraft({ version: 'verLanded', baseRev: first.baseRev, meta: first.meta, body: first.body }),
      }),
    )
    await t.settle()
    expect(t.onConflict).not.toHaveBeenCalled()
    expect(t.puts()[1].input.prev).toBe('verLanded')
  })

  it('retries back off while offline (1 s, 2 s, 4 s … up to 30 s) and going online resumes at once', async () => {
    const t = setup()
    t.edit({ title: '一' })
    await vi.advanceTimersByTimeAsync(1500)
    t.last('put').reject(new ApiError(0, 'network', ''))
    await t.settle()
    const asks = () => t.api.getPage.mock.calls.length
    // Second failure: the next attempt waits 2 s, then 4 s, however often the person types.
    t.edit({ title: '二' })
    await vi.advanceTimersByTimeAsync(1500)
    t.last('get').reject(new ApiError(0, 'network', ''))
    await t.settle()
    const before = asks()
    t.edit({ title: '三' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(asks()).toBe(before)
    await vi.advanceTimersByTimeAsync(1000)
    expect(asks()).toBe(before + 1)
    t.last('get').reject(new ApiError(0, 'network', ''))
    await t.settle()
    // Coming back online does not wait for the backoff.
    t.c.noticeOnline()
    await t.settle()
    expect(asks()).toBe(before + 2)
  })

  it('if the list ever reached its cap it is not used: the answer is a conflict dialog, never a silent adoption', async () => {
    const t = setup()
    t.edit({ title: '第一版', body: '第一版的字\n' })
    await vi.advanceTimersByTimeAsync(1500)
    const first = t.puts()[0].input
    // The cap is unreachable in use; fill the list to see what the guard does.
    const inner = t.c as unknown as { ours: { baseRev: string; digest: string }[]; oursFull: boolean }
    for (let i = 0; i < 520; i++) inner.ours.push({ baseRev: 'rev1', digest: `x${i}` })
    inner.oursFull = true
    t.last('put').reject(new ApiError(0, 'network', ''))
    await t.settle()
    t.edit({ title: '最后', body: '最后\n' })
    await vi.advanceTimersByTimeAsync(1500)
    t.last('get').resolve(
      page({
        draft: serverDraft({ version: 'verLanded', baseRev: first.baseRev, meta: first.meta, body: first.body }),
      }),
    )
    await t.settle()
    expect(t.c.state).toBe('conflict')
    expect(t.onConflict.mock.calls[0][0].kind).toBe('draft')
  })

  it('taking another draft makes it ours: the server committing it is then our own text, not a file conflict', async () => {
    const t = setup()
    t.edit({ title: '我的', body: '我的字\n' })
    await vi.advanceTimersByTimeAsync(1500)
    const other = serverDraft({
      version: 'verOther',
      meta: { id: ID, title: '别处', created: '2026-01-01' },
      body: '别处的字\n',
    })
    t.last('put').reject(refused(other, page()))
    await t.settle()
    expect(t.onConflict.mock.calls[0][0].kind).toBe('draft')
    t.c.useTheirs()
    await t.settle()
    // The server commits that draft; the next store is refused with the file being it.
    t.setContent({ title: '别处', body: '别处的字，我接着写\n' })
    t.c.markDirty()
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.puts().at(-1)!.input.prev).toBe('verOther')
    t.last('put').reject(refused(null, page({ rev: 'revC', meta: other.meta, body: other.body })))
    await t.settle()
    expect(t.onConflict).toHaveBeenCalledTimes(1)
    expect(t.c.state).not.toBe('conflict')
    expect(t.puts().at(-1)!.input).toMatchObject({ prev: '', baseRev: 'revC' })
  })

  it('what was given up is not ours: after "take theirs" a file that holds that text is a conflict', async () => {
    const t = setup()
    t.edit({ title: '我的', body: '我的字\n' })
    await vi.advanceTimersByTimeAsync(1500)
    const mine = t.puts()[0].input
    t.last('put').reject(
      refused(null, page({ rev: 'revB', body: '别人的\n', meta: { id: ID, title: '别人', created: '2026-01-01' } })),
    )
    await t.settle()
    expect(t.c.state).toBe('conflict')
    t.c.useTheirs()
    await t.settle()
    t.setContent({ title: '别人', body: '别人的\n' })
    t.c.markDirty()
    t.edit({ body: '又写了\n' })
    await vi.advanceTimersByTimeAsync(1500)
    // The other device now writes the text that was given up.
    t.last('put').reject(refused(null, page({ rev: 'revC', meta: mine.meta, body: mine.body })))
    await t.settle()
    expect(t.c.state).toBe('conflict')
  })
})
