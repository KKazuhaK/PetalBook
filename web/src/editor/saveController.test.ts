import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/api/client'
import type { Page, SavePageInput } from '@/api/types'
import { applyFields, cleanFields, fieldsOf, SaveController, waitForSaves, type Draft } from './saveController'

const page = (over: Partial<Page> = {}): Page => ({
  id: '01JD3K8Q7Z',
  path: 'a.md',
  folder: '',
  rev: 'rev1',
  updated: '2026-11-21T08:30:00Z',
  meta: { id: '01JD3K8Q7Z', title: '原标题', series: '冬季', created: '2026-01-01', tags: ['甲'] },
  body: '* 一\n* 二\n',
  ...over,
})

function setup(opts: { page?: Page; draft?: Partial<Draft>; normalizeBody?: (s: string) => string } = {}) {
  const start = opts.page ?? page()
  let current: Draft = { title: '原标题', subtitle: '', tags: ['甲'], ...opts.draft }
  const sent: { input: SavePageInput; keepalive: boolean }[] = []
  const resolvers: ((p: Page) => void)[] = []
  const rejecters: ((e: unknown) => void)[] = []
  let n = 1
  const save = vi.fn(
    (_id: string, input: SavePageInput, o: { keepalive: boolean }) =>
      new Promise<Page>((resolve, reject) => {
        sent.push({ input, keepalive: o.keepalive })
        resolvers.push((p) => resolve(p))
        rejecters.push(reject)
      }),
  )
  const onConflict = vi.fn()
  const onSaved = vi.fn()
  const read = vi.fn(() => current)
  const c = new SaveController({ page: start, read, save, onConflict, onSaved, normalizeBody: opts.normalizeBody })
  const edit = (d: Partial<Draft>) => {
    current = { ...current, ...d }
    c.markDirty()
  }
  // Answer the newest request as the server would: new rev, content as sent.
  const respond = async (over: Partial<Page> = {}) => {
    const idx = sent.length - 1
    const req = sent[idx].input
    resolvers[idx](page({ rev: `rev${++n}`, meta: req.meta, body: req.body, ...over }))
    await vi.advanceTimersByTimeAsync(0)
  }
  return { c, save, sent, edit, respond, rejecters, onConflict, onSaved, read, setDraft: (d: Partial<Draft>) => (current = { ...current, ...d }) }
}

beforeEach(() => vi.useFakeTimers())
afterEach(() => vi.useRealTimers())

describe('debounce', () => {
  it('waits 1.5 s after the last change and then saves once', async () => {
    const t = setup()
    t.edit({ title: '新' })
    expect(t.c.state).toBe('dirty')
    await vi.advanceTimersByTimeAsync(1000)
    t.edit({ title: '新标题' })
    await vi.advanceTimersByTimeAsync(1400)
    expect(t.save).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(200)
    expect(t.save).toHaveBeenCalledTimes(1)
    expect(t.c.state).toBe('saving')
    expect(t.sent[0].input.baseRev).toBe('rev1')
    expect(t.sent[0].input.meta.title).toBe('新标题')
    await t.respond()
    expect(t.c.state).toBe('saved')
  })

  it('does not read (serialise) the editor on every change', async () => {
    const t = setup()
    for (let i = 0; i < 50; i++) t.edit({ body: `正文 ${i}` })
    expect(t.read).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1600)
    expect(t.read).toHaveBeenCalledTimes(1)
  })
})

describe('no overlapping saves', () => {
  it('queues an edit made while a save is out and sends the latest after it', async () => {
    const t = setup()
    t.edit({ title: 'A' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.save).toHaveBeenCalledTimes(1)

    t.edit({ title: 'AB' })
    await vi.advanceTimersByTimeAsync(1500) // debounce fires while the first request is still out
    t.edit({ title: 'ABC' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.save).toHaveBeenCalledTimes(1)
    expect(t.c.state).toBe('saving')

    await t.respond() // first answer arrives
    expect(t.save).toHaveBeenCalledTimes(2)
    expect(t.sent[1].input.meta.title).toBe('ABC')
    expect(t.sent[1].input.baseRev).toBe('rev2') // the rev from the first response
    await t.respond()
    expect(t.c.state).toBe('saved')
  })

  it('shows unsaved changes again when an edit lands during the request and the wait has not elapsed', async () => {
    const t = setup()
    t.edit({ title: 'A' })
    await vi.advanceTimersByTimeAsync(1500)
    t.edit({ title: 'AB' })
    await t.respond()
    expect(t.c.state).toBe('dirty')
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.save).toHaveBeenCalledTimes(2)
  })

  it('waitForSaves resolves once the queue is empty', async () => {
    const t = setup()
    t.edit({ title: 'A' })
    await vi.advanceTimersByTimeAsync(1500)
    let done = false
    void waitForSaves('01JD3K8Q7Z').then(() => (done = true))
    await vi.advanceTimersByTimeAsync(0)
    expect(done).toBe(false)
    await t.respond()
    expect(done).toBe(true)
  })
})

describe('conflict', () => {
  const theirs = () => page({ rev: 'revX', meta: { id: '01JD3K8Q7Z', title: '别人的标题', created: '2026-01-01' }, body: '别人写的\n' })

  it('stops autosaving and keeps the text, reporting both versions', async () => {
    const t = setup()
    t.edit({ title: '我的标题', body: '我写的\n' })
    await vi.advanceTimersByTimeAsync(1500)
    t.rejecters[0](new ApiError(409, 'conflict', '页面已被修改', theirs()))
    await vi.advanceTimersByTimeAsync(0)
    expect(t.c.state).toBe('conflict')
    expect(t.onConflict).toHaveBeenCalledTimes(1)
    const [current, mine] = t.onConflict.mock.calls[0]
    expect(current.body).toBe('别人写的\n')
    expect(mine).toMatchObject({ title: '我的标题', body: '我写的\n' })

    t.edit({ body: '我又写了一点\n' })
    await vi.advanceTimersByTimeAsync(5000)
    expect(t.save).toHaveBeenCalledTimes(1)
    expect(t.c.state).toBe('conflict')
  })

  it('keep mine saves again on top of the current rev', async () => {
    const t = setup()
    t.edit({ title: '我的标题', body: '我写的\n' })
    await vi.advanceTimersByTimeAsync(1500)
    t.rejecters[0](new ApiError(409, 'conflict', '', theirs()))
    await vi.advanceTimersByTimeAsync(0)
    void t.c.keepMine(theirs())
    await vi.advanceTimersByTimeAsync(0)
    expect(t.save).toHaveBeenCalledTimes(2)
    expect(t.sent[1].input.baseRev).toBe('revX')
    expect(t.sent[1].input.body).toBe('我写的\n')
    expect(t.sent[1].input.meta.title).toBe('我的标题')
    await t.respond()
    expect(t.c.state).toBe('saved')
  })

  it('keep mine with an untouched body keeps the other side body', async () => {
    const t = setup()
    t.edit({ title: '我的标题' })
    await vi.advanceTimersByTimeAsync(1500)
    t.rejecters[0](new ApiError(409, 'conflict', '', theirs()))
    await vi.advanceTimersByTimeAsync(0)
    void t.c.keepMine(theirs())
    await vi.advanceTimersByTimeAsync(0)
    expect(t.sent[1].input.body).toBe('别人写的\n')
  })

  it('use theirs adopts the current page and goes quiet', async () => {
    const t = setup()
    t.edit({ title: '我的标题' })
    await vi.advanceTimersByTimeAsync(1500)
    t.rejecters[0](new ApiError(409, 'conflict', '', theirs()))
    await vi.advanceTimersByTimeAsync(0)
    t.c.useTheirs(theirs())
    expect(t.c.state).toBe('saved')
    expect(t.c.page.rev).toBe('revX')
    expect(t.c.hasUnsaved()).toBe(false)
    // After the editor reloads, later edits save against the new rev.
    t.setDraft({ title: '别人的标题' })
    t.edit({ title: '别人的标题，我补一句' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.sent[1].input.baseRev).toBe('revX')
  })
})

describe('errors', () => {
  it('reports a failed save, keeps the draft, and retries on demand', async () => {
    const t = setup()
    t.edit({ title: '新' })
    await vi.advanceTimersByTimeAsync(1500)
    t.rejecters[0](new ApiError(0, 'network', '无法连接到服务器'))
    await vi.advanceTimersByTimeAsync(0)
    expect(t.c.state).toBe('error')
    void t.c.retry()
    await vi.advanceTimersByTimeAsync(0)
    expect(t.save).toHaveBeenCalledTimes(2)
    expect(t.c.state).toBe('saving')
    expect(t.sent[1].input.baseRev).toBe('rev1')
  })
})

describe('unchanged content is never sent', () => {
  it('opening a page and leaving sends nothing', async () => {
    const t = setup()
    await t.c.detach()
    expect(t.save).not.toHaveBeenCalled()
    expect(t.read).not.toHaveBeenCalled()
  })

  it('a change that is undone before the save sends nothing', async () => {
    const t = setup()
    t.edit({ title: '改了' })
    t.edit({ title: '原标题' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.save).not.toHaveBeenCalled()
    expect(t.c.state).toBe('saved')
  })

  it('a body edited and restored to the normalised form of the original sends nothing', async () => {
    const t = setup({ normalizeBody: (s) => s.replace(/^\* /gm, '- ') })
    t.edit({ body: '- 一\n- 二\n' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.save).not.toHaveBeenCalled()
    expect(t.c.state).toBe('saved')
  })

  it('whitespace around title and tags is not a change', async () => {
    const t = setup()
    t.edit({ title: '  原标题 ', tags: [' 甲', '甲'] })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.save).not.toHaveBeenCalled()
  })

  it('a title-only edit sends the original body string unchanged', async () => {
    const t = setup()
    t.edit({ title: '新标题' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.sent[0].input.body).toBe('* 一\n* 二\n')
  })
})

describe('tags and scalars as the server serves them', () => {
  it('shows a tag written as a single string, and a numeric or boolean title as text', () => {
    expect(fieldsOf({ id: 'X', tags: '栀子', title: 2026, subtitle: true })).toEqual({ title: '2026', subtitle: 'true', tags: ['栀子'] })
    expect(fieldsOf({ id: 'X', tags: ['a', null, 7, 'b'] })).toEqual({ title: '', subtitle: '', tags: ['a', 'b'] })
    expect(fieldsOf({ id: 'X', tags: '' }).tags).toEqual([])
    expect(fieldsOf({ id: 'X', title: null, subtitle: { a: 1 } })).toMatchObject({ title: '', subtitle: '' })
  })

  it('a string tag is not lost when another tag is added', async () => {
    const p = page({ meta: { id: '01JD3K8Q7Z', title: '标', tags: '栀子' } })
    const t = setup({ page: p, draft: { title: '标', tags: ['栀子'] } })
    t.edit({ tags: ['栀子', 'x'] })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.sent[0].input.meta.tags).toEqual(['栀子', 'x'])
  })

  it('a string tag untouched is sent back as the string', async () => {
    const p = page({ meta: { id: '01JD3K8Q7Z', title: '标', tags: '栀子' } })
    const t = setup({ page: p, draft: { title: '标', tags: ['栀子'] } })
    t.edit({ title: '新标' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.sent[0].input.meta.tags).toBe('栀子')
  })

  it('non-text entries of a tag list survive an edit of the tags, after the edited ones', () => {
    const meta = { id: 'X', tags: ['a', null, 7, 'b'] }
    const out = applyFields(meta, { title: '', subtitle: '', tags: ['b', 'c'] }, { title: false, subtitle: false, tags: true })
    expect(out.tags).toEqual(['b', 'c', null, 7])
    // Removing every text tag leaves the rest rather than deleting the key.
    expect(applyFields(meta, { title: '', subtitle: '', tags: [] }, { title: false, subtitle: false, tags: true }).tags).toEqual([null, 7])
    expect('tags' in applyFields({ id: 'X', tags: ['a'] }, { title: '', subtitle: '', tags: [] }, { title: false, subtitle: false, tags: true })).toBe(false)
  })

  it('a numeric title is rewritten only if the person changes it', async () => {
    const p = page({ meta: { id: '01JD3K8Q7Z', title: 2026, created: 'c' } })
    const t = setup({ page: p, draft: { title: '2026', tags: [] } })
    t.edit({ body: '改正文\n' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.sent[0].input.meta.title).toBe(2026)
    await t.respond()
    t.edit({ title: '二〇二六' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.sent[1].input.meta.title).toBe('二〇二六')
  })
})

describe('header handling', () => {
  it('sends unknown fields back exactly as received (the server decides their order)', async () => {
    const p = page({ meta: { id: '01JD3K8Q7Z', title: '标', zeta: { a: 1 }, series: '冬季', created: '2026-01-01', alpha: [1, 2] } })
    const t = setup({ page: p, draft: { title: '标', tags: [] } })
    t.edit({ title: '改标题' })
    await vi.advanceTimersByTimeAsync(1500)
    const meta = t.sent[0].input.meta
    expect(meta).toEqual({ id: '01JD3K8Q7Z', title: '改标题', zeta: { a: 1 }, series: '冬季', created: '2026-01-01', alpha: [1, 2] })
  })

  it('an untitled page stays untitled: no title key, never an empty string', async () => {
    const p = page({ meta: { id: '01JD3K8Q7Z', created: '2026-11-21' }, body: '一句话\n' })
    const t = setup({ page: p, draft: { title: '', tags: [] } })
    t.edit({ body: '一句话，加一点\n' })
    await vi.advanceTimersByTimeAsync(1500)
    expect('title' in t.sent[0].input.meta).toBe(false)
    expect('subtitle' in t.sent[0].input.meta).toBe(false)
    expect(t.sent[0].input.meta).toEqual({ id: '01JD3K8Q7Z', created: '2026-11-21' })
  })

  it('clearing a title removes the key', () => {
    const out = applyFields({ id: 'X', title: '甲', subtitle: '乙', created: 'c' }, { title: '', subtitle: '乙', tags: [] }, { title: true, subtitle: false, tags: false })
    expect(out).toEqual({ id: 'X', subtitle: '乙', created: 'c' })
  })

  it('a field that was absent is added when the person fills it in', () => {
    const base = { id: 'X', created: 'c', series: 's' }
    const a = applyFields(base, { title: 'T', subtitle: 'S', tags: ['t'] }, { title: true, subtitle: true, tags: true })
    expect(a).toEqual({ id: 'X', created: 'c', series: 's', title: 'T', subtitle: 'S', tags: ['t'] })
  })

  it('never touches header values the person did not edit, whatever their type', async () => {
    const odd = page({ meta: { id: '01JD3K8Q7Z', title: 5, subtitle: ['x'], tags: ['a', 'a', 7], weird: { n: null } }, body: 'b\n' })
    // Shown as: title "5", no subtitle, tags ["a","a"].
    const t = setup({ page: odd, draft: { title: '5', subtitle: '', tags: ['a', 'a'] } })
    t.edit({ title: '5' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.save).not.toHaveBeenCalled()
    t.edit({ body: '改了\n' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.sent[0].input.meta).toEqual(odd.meta)
  })

  it('writes only the field that changed: others keep their served values', async () => {
    const odd = page({ meta: { id: '01JD3K8Q7Z', title: 5, tags: ['a', 'a'] } })
    const t = setup({ page: odd, draft: { title: '5', subtitle: '', tags: ['a', 'a'] } })
    t.edit({ subtitle: '新副标题' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.sent[0].input.meta).toEqual({ id: '01JD3K8Q7Z', title: 5, tags: ['a', 'a'], subtitle: '新副标题' })
  })

  it('cleanFields trims, flattens newlines and de-duplicates tags', () => {
    expect(cleanFields({ title: ' 甲\n乙 ', subtitle: '', tags: [' a ', 'a', '', 'b'] })).toEqual({ title: '甲 乙', subtitle: '', tags: ['a', 'b'] })
  })
})

describe('leaving the page', () => {
  it('detach captures the content immediately, before the editor goes away', async () => {
    const t = setup()
    t.edit({ title: '离开前改的' })
    const done = t.c.detach()
    // The editor is gone from now on; the controller must not read it again.
    t.read.mockImplementation(() => {
      throw new Error('editor destroyed')
    })
    await vi.advanceTimersByTimeAsync(0)
    expect(t.sent[0].input.meta.title).toBe('离开前改的')
    await t.respond()
    await done
  })

  it('detach while a save is out sends the queued edit afterwards, using the frozen content', async () => {
    const t = setup()
    t.edit({ title: '一' })
    await vi.advanceTimersByTimeAsync(1500)
    t.edit({ title: '二' })
    const done = t.c.detach()
    t.read.mockImplementation(() => {
      throw new Error('editor destroyed')
    })
    await t.respond()
    expect(t.sent[1].input.meta.title).toBe('二')
    await t.respond()
    await done
  })

  it('flushKeepalive sends unsaved content with keepalive, once', async () => {
    const t = setup()
    t.edit({ title: '切走了' })
    t.c.flushKeepalive()
    t.c.flushKeepalive()
    await vi.advanceTimersByTimeAsync(0)
    expect(t.save).toHaveBeenCalledTimes(1)
    expect(t.sent[0].keepalive).toBe(true)
    // The pending debounce timer was cancelled.
    await t.respond()
    await vi.advanceTimersByTimeAsync(3000)
    expect(t.save).toHaveBeenCalledTimes(1)
  })

  it('flushKeepalive with nothing unsaved sends nothing', () => {
    const t = setup()
    t.c.flushKeepalive()
    expect(t.save).not.toHaveBeenCalled()
  })

  it('discard drops unsaved changes for good, even through a later detach', async () => {
    const t = setup()
    t.edit({ title: '要删掉的页面' })
    t.c.discard()
    await t.c.detach()
    await vi.advanceTimersByTimeAsync(5000)
    expect(t.save).not.toHaveBeenCalled()
  })

  it('suspend then attach resumes autosave (a failed delete keeps the page alive)', async () => {
    const t = setup()
    t.edit({ title: '删除失败的页面' })
    t.c.suspend()
    await vi.advanceTimersByTimeAsync(5000)
    expect(t.save).not.toHaveBeenCalled()
    t.c.attach()
    t.c.markDirty()
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.save).toHaveBeenCalledTimes(1)
  })

  it('an editor that cannot be serialised ends in the error state instead of throwing', async () => {
    const t = setup()
    t.read.mockImplementation(() => {
      throw new Error('no Markdown form for block node "x"')
    })
    t.edit({ title: '新' })
    await vi.advanceTimersByTimeAsync(1500)
    expect(t.c.state).toBe('error')
    expect(t.save).not.toHaveBeenCalled()
  })
})
