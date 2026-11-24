// The body typed as Markdown in the source view, and header keys written
// there: through the live draft, the local copy, and back.

import { describe, expect, it, vi } from 'vitest'
import type { Page } from '@/api/types'
import type { DocJSON } from '@/markdown'
import { planRestore } from './draftRestore'
import { loadDraft, saveDraft, type KeyValueStorage, type StoredDraft } from './draftStore'
import { LiveDraft } from './liveDraft'
import { draftToCreateInput } from './recoverDraft'
import { buildSave, fieldsOf } from './saveController'

const ID = '01JD3K8Q7Z'
const page = (over: Partial<Page> = {}): Page => ({
  id: ID,
  path: 'a.md',
  folder: '',
  rev: 'rev1',
  updated: 'u',
  meta: { id: ID, title: '题', created: '2026-01-01', series: '冬' },
  body: '* 原文\n',
  ...over,
})
const doc = (text: string): DocJSON => ({
  type: 'doc',
  content: [{ type: 'paragraph', content: [{ type: 'text', text }] }],
})
const toMd = (d: DocJSON) => (d.content?.[0]?.content?.[0]?.text ?? '') + '\n'

function fakeStorage(): KeyValueStorage {
  const m = new Map<string, string>()
  return {
    getItem: (k) => m.get(k) ?? null,
    setItem: (k, v) => void m.set(k, v),
    removeItem: (k) => void m.delete(k),
    get length() {
      return m.size
    },
    key: (i) => [...m.keys()][i] ?? null,
  }
}

describe('LiveDraft with a typed body', () => {
  const fields = { title: '题', subtitle: '', tags: [] }
  it('sends the typed text as it is, without asking the editor', () => {
    const conv = vi.fn(() => 'from the document\n')
    const d = new LiveDraft(fields, conv)
    d.attachEditor({ isDestroyed: false, getJSON: () => doc('x') })
    d.setBodyText('* 星号列表\n\n不规整   的写法\n')
    expect(d.bodyKind()).toBe('text')
    expect(d.read().body).toBe('* 星号列表\n\n不规整   的写法\n')
    expect(d.readStorable()).toEqual({ fields, bodyText: '* 星号列表\n\n不规整   的写法\n' })
    expect(conv).not.toHaveBeenCalled()
  })
  it('an edit in the editor afterwards makes the document the body again', () => {
    const d = new LiveDraft(fields, () => 'from the document\n')
    d.attachEditor({ isDestroyed: false, getJSON: () => doc('x') })
    d.setBodyText('typed\n')
    d.bodyChanged()
    expect(d.bodyKind()).toBe('doc')
    expect(d.read().body).toBe('from the document\n')
    expect(d.readStorable().bodyText).toBeUndefined()
    expect(d.readStorable().bodyJson).toEqual(doc('x'))
  })
  it('typing in the source view again takes over again, and reset forgets it', () => {
    const d = new LiveDraft(fields, () => 'doc\n')
    d.attachEditor({ isDestroyed: false, getJSON: () => doc('x') })
    d.bodyChanged()
    d.setBodyText('second\n')
    expect(d.read().body).toBe('second\n')
    d.reset(fields)
    expect(d.bodyKind()).toBe('none')
    expect(d.read().body).toBeUndefined()
  })
  it('works with no editor at all (the source view alone)', () => {
    const d = new LiveDraft(fields, () => 'doc\n')
    d.setBodyText('only text\n')
    expect(d.read().body).toBe('only text\n')
    expect(() => d.readStorable()).not.toThrow()
  })
})

describe('the local copy of a typed body', () => {
  const stored = (over: Partial<StoredDraft>): StoredDraft => ({
    v: 1,
    id: 'ref',
    baseRev: 'rev1',
    token: 't',
    savedAt: 1,
    fields: { title: '题', subtitle: '', tags: [] },
    ...over,
  })
  it('is kept and read back as text', () => {
    const st = fakeStorage()
    saveDraft(
      'ref',
      {
        baseRev: 'rev1',
        token: 't',
        savedAt: Date.now(),
        fields: { title: '题', subtitle: '', tags: [], others: { series: '夏' } },
        bodyText: '* a\n',
      },
      st,
    )
    const d = loadDraft('ref', st)
    expect(d?.bodyText).toBe('* a\n')
    expect(d?.fields.others).toEqual({ series: '夏' })
  })
  it('a copy with a body text of the wrong type is discarded, not trusted', () => {
    const st = fakeStorage()
    st.setItem('pb.draft.ref', JSON.stringify({ ...stored({}), savedAt: Date.now(), bodyText: 5 }))
    expect(loadDraft('ref', st)).toBeNull()
    st.setItem(
      'pb.draft.ref',
      JSON.stringify({
        ...stored({}),
        savedAt: Date.now(),
        fields: { title: '', subtitle: '', tags: [], others: [1] },
      }),
    )
    expect(loadDraft('ref', st)).toBeNull()
  })
  it('is restored as typed: the plan carries the text and says it is not to be rewritten', () => {
    const plan = planRestore(page(), stored({ bodyText: '* 改过\n' }), toMd, (md) => md)
    expect(plan.kind).toBe('local')
    if (plan.kind === 'local') expect(plan.content).toMatchObject({ markdown: '* 改过\n', typed: true })
  })
  it('equal to the file means nothing to restore', () => {
    expect(planRestore(page(), stored({ bodyText: '* 原文\n' }), toMd, (md) => md)).toMatchObject({
      kind: 'file',
      dropLocal: true,
    })
  })
  it('a typed body on a file that moved on is a conflict showing that text', () => {
    const plan = planRestore(
      page({ rev: 'rev2', body: '别人改的\n' }),
      stored({ bodyText: '我写的\n' }),
      toMd,
      (md) => md,
    )
    expect(plan.kind).toBe('conflict')
    if (plan.kind === 'conflict') {
      expect(plan.info.mine.body).toBe('我写的\n')
      expect(plan.content).toMatchObject({ markdown: '我写的\n', typed: true })
    }
  })
  it('header keys alone (no body edit) are something to restore', () => {
    const plan = planRestore(
      page(),
      stored({ fields: { title: '题', subtitle: '', tags: [], others: { created: '2026-01-01', series: '夏' } } }),
      toMd,
      (md) => md,
    )
    expect(plan.kind).toBe('local')
  })
  it('recovering a deleted page keeps the typed body and the other keys', () => {
    const input = draftToCreateInput(
      stored({
        bodyText: '* a\n',
        fields: { title: '题', subtitle: '', tags: [], others: { series: '夏', created: '2020-01-01', id: 'X' } },
      }),
      toMd,
    )
    expect(input).toEqual({ folder: '', meta: { title: '题', series: '夏' }, body: '* a\n' })
  })
})

describe('other header keys in a save', () => {
  const base = page()
  const none = () => base.body
  it('absent means untouched', () => {
    expect(buildSave(base, { title: '题', subtitle: '', tags: [] }, none)).toBeNull()
  })
  it('the same keys in another order are no change', () => {
    expect(
      buildSave(base, { title: '题', subtitle: '', tags: [], others: { series: '冬', created: '2026-01-01' } }, none),
    ).toBeNull()
  })
  it('replaces the other keys and leaves id and the named fields alone', () => {
    const s = buildSave(
      base,
      {
        title: '题',
        subtitle: '',
        tags: [],
        others: { created: '2026-01-01', status: '定稿', id: 'ignored', title: 'ignored' },
      },
      none,
    )
    expect(s?.meta).toEqual({ id: ID, title: '题', created: '2026-01-01', status: '定稿' })
    expect(s?.body).toBe(base.body)
  })
  it('nested values are compared as data', () => {
    const b = page({ meta: { id: ID, extra: { a: [1, { b: 2 }] } } })
    expect(
      buildSave(b, { title: '', subtitle: '', tags: [], others: { extra: { a: [1, { b: 2 }] } } }, () => b.body),
    ).toBeNull()
    expect(
      buildSave(b, { title: '', subtitle: '', tags: [], others: { extra: { a: [1, { b: 3 }] } } }, () => b.body)?.meta,
    ).toEqual({ id: ID, extra: { a: [1, { b: 3 }] } })
  })
})

describe('a body typed in the source view is compared as typed', () => {
  const base = page({ body: '* a\n* b\n' })
  const fields = { title: '题', subtitle: '', tags: [] }
  const normalized = () => '- a\n- b\n'
  it("rewriting the file into the editor's own style by hand is a change", () => {
    expect(buildSave(base, { ...fields, body: '- a\n- b\n', bodyTyped: true }, normalized)?.body).toBe('- a\n- b\n')
  })
  it('the same text from the editor is not (it is only how the editor writes it)', () => {
    expect(buildSave(base, { ...fields, body: '- a\n- b\n' }, normalized)).toBeNull()
  })
  it("the file's exact text is no change either way", () => {
    expect(buildSave(base, { ...fields, body: '* a\n* b\n', bodyTyped: true }, normalized)).toBeNull()
  })
  it('LiveDraft marks a typed body', () => {
    const d = new LiveDraft(fields, () => 'x')
    d.setBodyText('- a\n- b\n')
    expect(d.read().bodyTyped).toBe(true)
    d.attachEditor({ isDestroyed: false, getJSON: () => doc('x') })
    d.bodyChanged()
    expect(d.read().bodyTyped).toBeUndefined()
  })
})

describe('header keys held in a server draft', () => {
  const draftMeta = { id: ID, title: '题', created: '2026-01-01', series: '冬', status: 'done' }
  it('stay when the page is opened on that draft and edited further', () => {
    const p = page({
      draft: { version: 'v1', baseRev: 'rev1', meta: draftMeta, body: '* 原文\n', updated: 'u', stale: false },
    })
    const plan = planRestore(p, null, toMd, (md) => md)
    expect(plan.kind).toBe('server')
    if (plan.kind !== 'server') return
    expect(plan.content.fields.others).toEqual({ created: '2026-01-01', series: '冬', status: 'done' })
    const next = buildSave(p, { ...plan.content.fields, title: '新题', body: undefined }, () => p.body)
    expect(next?.meta).toMatchObject({ title: '新题', status: 'done', series: '冬' })
  })
  it("a draft with the file's own keys carries none", () => {
    expect(fieldsOf(page().meta, page().meta).others).toBeUndefined()
    expect(fieldsOf(page().meta).others).toBeUndefined()
    expect(fieldsOf(draftMeta, page().meta).others).toEqual({ created: '2026-01-01', series: '冬', status: 'done' })
  })
})

describe("keys the person never changed are not sent over someone else's", () => {
  it('text typed on the file keeps whatever other keys the file has by then', () => {
    // Opened on rev1; another device added `status`; "keep mine" saves on that file.
    const mine = { ...fieldsOf(page().meta), title: '我的题', body: '我的\n' }
    const theirs = page({ rev: 'rev2', meta: { ...page().meta, status: 'done' } })
    expect(buildSave(theirs, mine, () => theirs.body)?.meta).toEqual({ ...theirs.meta, title: '我的题' })
  })
})

describe('other keys as the server will write them', () => {
  const base = page()
  const f = { title: '题', subtitle: '', tags: [] }
  it("a header typed without `created` is the file's header: the server keeps that key", () => {
    expect(buildSave(base, { ...f, others: { series: '冬' } }, () => base.body)).toBeNull()
  })
  it('an emptied type is no key, and a numeric one is text', () => {
    expect(
      buildSave(base, { ...f, others: { created: '2026-01-01', series: '冬', type: '' } }, () => base.body),
    ).toBeNull()
    const typed = page({ meta: { ...base.meta, type: '1984' } })
    expect(
      buildSave(typed, { ...f, others: { created: '2026-01-01', series: '冬', type: 1984 } }, () => typed.body),
    ).toBeNull()
  })
})
