import { describe, expect, it } from 'vitest'
import type { SavedView } from '../api/types.ts'
import { stateOfView } from './savedViews.ts'
import {
  catchUp,
  IMPLICIT,
  initialWorkingCopy,
  prune,
  reduceWorkingCopy,
  type Outcome,
  type Patch,
  type Tabs,
  type WorkingCopy,
  type WorkingEvent,
} from './workingCopy.ts'

// The one function that decides what becomes of the working copy, for every event, every kind of view (a real id, a
// view written by hand that is still "@n") and every kind of change (the sort only, something else, both).

const mk = (id: string, name: string, extra: Partial<SavedView> = {}): SavedView => ({
  id,
  name,
  layout: 'list',
  sort: '',
  group: '',
  filter: [],
  hidden: [],
  ...extra,
})

type Kind = 'real' | 'hand'
const KINDS: Kind[] = ['real', 'hand']
const PATCHES: Record<string, Patch> = {
  'sort only': { sort: 'title' },
  other: { layout: 'cards' },
  both: { sort: 'title', layout: 'cards' },
}

// The ids a kind of view has before and after someone's write names the views ("@n" become real ids).
const before = (k: Kind) => (k === 'real' ? ['r0', 'r1', 'r2'] : ['@0', '@1', '@2'])
const after = (k: Kind) => (k === 'real' ? ['r0', 'r1', 'r2'] : ['n0', 'n1', 'n2'])
const three = (ids: string[], names = ['甲', '乙', '丙']) => names.map((n, i) => mk(ids[i], n))

const run = (wc: WorkingCopy, ...events: WorkingEvent[]): Outcome => {
  let out: Outcome = { wc, lost: null, remember: null }
  for (const e of events) out = reduceWorkingCopy(out.wc, e)
  return out
}

// The second view (乙) shown and changed.
function start(k: Kind, patch: Patch): { views: SavedView[]; wc: WorkingCopy } {
  const views = three(before(k))
  const first = run(initialWorkingCopy(before(k)[1], { view: null, sort: null }), { kind: 'list', views }).wc
  return { views, wc: run(first, { kind: 'edit', patch, base: stateOfView(views[1]) }).wc }
}

const list = (views: SavedView[]): WorkingEvent => ({ kind: 'list', views })
const written = (
  op: 'save' | 'create' | 'duplicate' | 'rename' | 'reorder' | 'delete',
  on: string,
  sent: SavedView[] | { name: string; id?: string }[],
  got: SavedView[],
): WorkingEvent => ({ kind: 'written', op, on, sent, got })

interface Expect {
  shown: string
  // The change is still there (on the view shown), or it is gone.
  patch: 'kept' | 'gone'
  lost: boolean
  remember?: string | null
}

interface Case {
  name: string
  events: (k: Kind, views: SavedView[], wc: WorkingCopy) => WorkingEvent[]
  expect: Expect | ((k: Kind) => Expect)
}

const shownName = (o: Outcome, events: WorkingEvent[], views: SavedView[]) => {
  const all = [views, ...events.flatMap((e) => (e.kind === 'list' ? [e.views] : e.kind === 'written' ? [e.got] : []))]
  for (const l of all.reverse()) {
    const v = l.find((x) => x.id === o.wc.viewKey)
    if (v) return v.name
  }
  return o.wc.viewKey === '' ? '(implicit)' : `?${o.wc.viewKey}`
}

const CASES: Case[] = [
  {
    name: 'refetch, nothing changed',
    events: (_k, views) => [list(three(views.map((v) => v.id)))],
    expect: { shown: '乙', patch: 'kept', lost: false, remember: null },
  },
  {
    name: 'refetch, someone else named the views and moved this one to the front',
    events: (k) => [list([mk(after(k)[1], '乙'), mk(after(k)[0], '甲'), mk(after(k)[2], '丙')])],
    expect: (k) => ({ shown: '乙', patch: 'kept', lost: false, remember: k === 'hand' ? 'n1' : null }),
  },
  {
    name: 'refetch, someone else inserted a view before it',
    events: (k) => [list([mk('x', '新'), ...three(after(k))])],
    expect: { shown: '乙', patch: 'kept', lost: false },
  },
  {
    name: 'refetch, someone else deleted it',
    events: (k) => [list([mk(after(k)[0], '甲'), mk(after(k)[2], '丙')])],
    expect: { shown: '甲', patch: 'gone', lost: true, remember: '' },
  },
  {
    name: 'refetch, someone else renamed it',
    events: (k) => [list(three(after(k), ['甲', '乙乙', '丙']))],
    // A real id is the same view under another name; an "@n" can only be told by its name and settings.
    expect: (k) =>
      k === 'real'
        ? { shown: '乙乙', patch: 'kept', lost: false }
        : { shown: '甲', patch: 'gone', lost: true, remember: '' },
  },
  {
    name: 'refetch, the file was edited by hand: the view at its place has another name',
    events: (k) => [list(three(before(k), ['甲', '乙乙', '丙']))],
    expect: (k) =>
      k === 'real' ? { shown: '乙乙', patch: 'kept', lost: false } : { shown: '甲', patch: 'gone', lost: true },
  },
  {
    name: 'refetch, someone else changed its settings',
    events: (k) => [list([mk(after(k)[0], '甲'), mk(after(k)[1], '乙', { group: 'status' }), mk(after(k)[2], '丙')])],
    expect: (k) =>
      k === 'real' ? { shown: '乙', patch: 'kept', lost: false } : { shown: '甲', patch: 'gone', lost: true },
  },
  {
    name: 'refetch, two views now look like it',
    events: (k) => [list(three(after(k), ['丙', '乙', '乙']))],
    expect: (k) =>
      k === 'real' ? { shown: '乙', patch: 'kept', lost: false } : { shown: '丙', patch: 'gone', lost: true },
  },
  {
    name: 'refetch, every view deleted',
    events: () => [list([])],
    expect: { shown: '(implicit)', patch: 'gone', lost: true, remember: '' },
  },
  {
    name: 'refused write (409), the views read again: the view was deleted',
    events: (k) => [list([mk(after(k)[0], '甲'), mk(after(k)[2], '丙')])],
    expect: { shown: '甲', patch: 'gone', lost: true, remember: '' },
  },
  {
    name: 'refused write (409), the views read again: someone else had named the views',
    events: (k) => [list(three(after(k)))],
    expect: { shown: '乙', patch: 'kept', lost: false },
  },
  {
    name: 'own save',
    events: (k, views) => [
      written(
        'save',
        views[1].id,
        views,
        three(after(k)).map((v, i) => (i === 1 ? { ...v, layout: 'cards', sort: 'title' } : v)),
      ),
    ],
    // The id changes only for a view written by hand.
    expect: (k) => ({ shown: '乙', patch: 'gone', lost: false, remember: k === 'hand' ? after(k)[1] : null }),
  },
  {
    name: 'own save, then the views read after it',
    events: (k, views) => {
      const got = three(after(k)).map((v, i) => (i === 1 ? { ...v, layout: 'cards' as const, sort: 'title' } : v))
      return [written('save', views[1].id, views, got), list(got)]
    },
    expect: { shown: '乙', patch: 'gone', lost: false },
  },
  {
    name: 'own save, then the views read after it: someone else deleted the view meanwhile',
    events: (k, views) => {
      const got = three(after(k)).map((v, i) => (i === 1 ? { ...v, layout: 'cards' as const, sort: 'title' } : v))
      return [written('save', views[1].id, views, got), list([got[0], got[2]])]
    },
    // Nothing unsaved was there to lose.
    expect: { shown: '甲', patch: 'gone', lost: false, remember: '' },
  },
  // A new view or a copy is made from what is shown, changes included.
  {
    name: 'own new view',
    events: (k, views, wc) => [
      written('create', views[1].id, [...views, { name: '新' }], [...three(after(k)), mk('x', '新', wc.patch)]),
    ],
    expect: { shown: '新', patch: 'gone', lost: false, remember: 'x' },
  },
  {
    name: 'own copy of the view shown',
    events: (k, views, wc) => [
      written(
        'duplicate',
        views[1].id,
        [...views, { name: '乙 copy' }],
        [...three(after(k)), mk('x', '乙 copy', wc.patch)],
      ),
    ],
    expect: { shown: '乙 copy', patch: 'gone', lost: false, remember: 'x' },
  },
  {
    name: 'own rename of the view shown',
    events: (k, views) => [
      written(
        'rename',
        views[1].id,
        views.map((v, i) => (i === 1 ? { ...v, name: '乙二' } : v)),
        three(after(k), ['甲', '乙二', '丙']),
      ),
    ],
    expect: { shown: '乙二', patch: 'kept', lost: false },
  },
  {
    name: 'own move of the view shown, next to a view with the same name',
    events: (k, views) => {
      const sent = [{ ...views[1] }, { ...views[0], name: '乙' }, views[2]]
      return [
        written(
          'reorder',
          views[1].id,
          sent,
          three(after(k), ['乙', '乙', '丙']).map((v, i) => ({ ...v, id: `${after(k)[i]}m` })),
        ),
      ]
    },
    expect: (k) => ({ shown: '乙', patch: 'kept', lost: false, remember: `${after(k)[0]}m` }),
  },
  {
    name: 'own move of another view',
    events: (k, views) => [
      written(
        'reorder',
        views[2].id,
        [views[2], views[0], views[1]],
        [mk(after(k)[2], '丙'), mk(after(k)[0], '甲'), mk(after(k)[1], '乙')],
      ),
    ],
    expect: { shown: '乙', patch: 'kept', lost: false },
  },
  {
    name: 'own delete of the view shown, then the views read after it',
    events: (k, views) => [
      written('delete', views[1].id, [views[0], views[2]], [mk(after(k)[0], '甲'), mk(after(k)[2], '丙')]),
      list([mk(after(k)[0], '甲'), mk(after(k)[2], '丙')]),
    ],
    expect: { shown: '甲', patch: 'gone', lost: false, remember: '' },
  },
  {
    name: 'own delete of another view',
    events: (k, views) => [
      written('delete', views[0].id, [views[1], views[2]], [mk(after(k)[1], '乙'), mk(after(k)[2], '丙')]),
    ],
    expect: { shown: '乙', patch: 'kept', lost: false },
  },
  {
    name: 'pick another view',
    events: (_k, views) => [{ kind: 'pick', id: views[2].id, views }],
    expect: { shown: '丙', patch: 'gone', lost: false },
  },
  {
    name: 'pick the view shown again',
    events: (_k, views) => [{ kind: 'pick', id: views[1].id, views }],
    expect: { shown: '乙', patch: 'kept', lost: false, remember: null },
  },
  { name: 'revert', events: () => [{ kind: 'revert' }], expect: { shown: '乙', patch: 'gone', lost: false } },
  {
    name: "the address names another view with a sort: not this view's",
    events: (_k, views) => [{ kind: 'address', view: views[0].id, sort: '-title', base: stateOfView(views[1]) }],
    expect: { shown: '乙', patch: 'kept', lost: false },
  },
]

describe('the working copy', () => {
  for (const k of KINDS)
    for (const [pk, patch] of Object.entries(PATCHES))
      for (const c of CASES) {
        it(`${c.name} — ${k === 'real' ? 'a real id' : 'a view written by hand ("@n")'}, change: ${pk}`, () => {
          const { views, wc } = start(k, patch)
          expect(wc.patch).toEqual(patch)
          const events = c.events(k, views, wc)
          const out = run(wc, ...events)
          const want = typeof c.expect === 'function' ? c.expect(k) : c.expect
          expect(shownName(out, events, views)).toBe(want.shown)
          expect(out.wc.patch).toEqual(want.patch === 'kept' ? patch : {})
          expect(out.lost !== null).toBe(want.lost)
          if (want.lost) expect(out.lost!.name).toBe('乙')
          if (want.remember !== undefined) expect(out.remember).toBe(want.remember)
        })
      }
})

describe('the sort in the address', () => {
  const views = three(['r0', 'r1', 'r2'])
  const shown = run(initialWorkingCopy('r1', { view: null, sort: null }), list(views)).wc
  it('changes the sort of the view it names, and only the sort', () => {
    const wc = run(shown, { kind: 'edit', patch: { layout: 'cards', sort: 'title' }, base: stateOfView(views[1]) }).wc
    expect(run(wc, { kind: 'address', view: 'r1', sort: '-title', base: stateOfView(views[1]) }).wc.patch).toEqual({
      layout: 'cards',
      sort: '-title',
    })
    expect(run(wc, { kind: 'address', view: 'r1', sort: null, base: stateOfView(views[1]) }).wc.patch).toEqual({
      layout: 'cards',
    })
    expect(run(wc, { kind: 'address', view: 'r1', sort: 'title', base: stateOfView(views[1]) }).wc).toBe(wc)
  })
  it('starts a listing with the sort of the view it names, else ignores it unless it is the implicit view', () => {
    expect(initialWorkingCopy('r0', { view: 'r1', sort: 'title' })).toEqual({
      viewKey: 'r1',
      snapshot: null,
      patch: { sort: 'title' },
      fromAddress: true,
    })
    expect(initialWorkingCopy('r0', { view: null, sort: 'title' })).toEqual({
      viewKey: 'r0',
      snapshot: null,
      patch: {},
    })
    expect(initialWorkingCopy('', { view: null, sort: 'title' })).toEqual({
      viewKey: '',
      snapshot: null,
      patch: { sort: 'title' },
      fromAddress: true,
    })
  })
  it('at the first reading: kept on the view it names, dropped with a word when that view is gone', () => {
    const kept = run(initialWorkingCopy('r0', { view: 'r1', sort: 'title' }), list(views))
    expect([kept.wc.viewKey, kept.wc.patch, kept.lost]).toEqual(['r1', { sort: 'title' }, null])
    const gone = run(initialWorkingCopy('r0', { view: 'zz', sort: 'title' }), list(views))
    // Only the address brought it (an old link, a reload): the sort is dropped, and the person is told the link's view is gone.
    expect([gone.wc.viewKey, gone.wc.patch, gone.lost]).toEqual(['r0', {}, { name: '', link: true }])
  })
})

describe('the first reading and the implicit view', () => {
  it('a remembered "@n" that a write has named since is the view at its place, without any change', () => {
    const out = run({ viewKey: '@1', snapshot: null, patch: { sort: 'title' } }, list(three(['a', 'b', 'c'])))
    expect([out.wc.viewKey, out.wc.patch, out.remember]).toEqual(['b', {}, 'b'])
  })
  it('a remembered view that is gone: the first, and it is forgotten', () => {
    const out = run(initialWorkingCopy('gone', { view: null, sort: null }), list(three(['a', 'b', 'c'])))
    expect([out.wc.viewKey, out.lost, out.remember]).toEqual(['a', null, ''])
  })
  it('no views: the implicit view keeps its changes', () => {
    const wc = run(initialWorkingCopy('', { view: null, sort: 'title' }), list([])).wc
    expect(wc).toEqual({ viewKey: '', snapshot: IMPLICIT, patch: { sort: 'title' } })
    expect(run(wc, list([])).wc).toBe(wc)
  })
  it('someone else made the first view: the changes of the implicit view are not put on it, and the person is told', () => {
    const wc = run(initialWorkingCopy('', { view: null, sort: null }), list([]), {
      kind: 'edit',
      patch: { sort: 'title' },
      base: stateOfView(mk('', '')),
    }).wc
    const out = run(wc, list([mk('a', '甲')]))
    expect([out.wc.viewKey, out.wc.patch, out.lost]).toEqual(['a', {}, { name: '' }])
  })
  it('the person made the first view from the implicit one: shown, and nothing is said', () => {
    const wc = run(initialWorkingCopy('', { view: null, sort: null }), list([]), {
      kind: 'edit',
      patch: { sort: 'title' },
      base: stateOfView(mk('', '')),
    }).wc
    const out = run(wc, written('create', '', [{ name: '甲' }], [mk('a', '甲', { sort: 'title' })]))
    expect([out.wc.viewKey, out.wc.patch, out.lost, out.remember]).toEqual(['a', {}, null, 'a'])
  })
})

describe('a change', () => {
  it('keeps only what differs from the view (the default sort is no change)', () => {
    const base = stateOfView(mk('a', '甲'))
    expect(prune({ sort: '-updated', layout: 'list', group: 'x' }, base)).toEqual({ group: 'x' })
  })
})

describe("the answer of the person's own write", () => {
  const views = three(['a', 'b', 'c'])
  const onB = run(initialWorkingCopy('b', { view: null, sort: null }), list(views)).wc
  const edit = (patch: Patch): WorkingEvent => ({ kind: 'edit', patch, base: stateOfView(views[1]) })

  it('deleting the view shown: its change goes with it, silently, and the views read next say what is shown', () => {
    const w = run(onB, edit({ sort: 'title' })).wc
    const got = [views[0], views[2]]
    const answered = run(w, written('delete', 'b', got, got))
    expect([answered.wc.viewKey, answered.wc.patch, answered.lost, answered.remember]).toEqual(['', {}, null, ''])
    const out = run(answered.wc, list(got))
    expect([out.wc.viewKey, out.wc.patch, out.lost]).toEqual(['a', {}, null])
  })
  it('an answer that cannot place the view changes nothing: the views read next decide', () => {
    const w = run(onB, edit({ sort: 'title' })).wc
    const out = reduceWorkingCopy(w, written('rename', 'a', [{ id: 'a', name: '甲二' }], [mk('a', '甲二')]))
    expect(out.wc).toBe(w)
  })
})

describe('catching up after a write has settled', () => {
  const views = three(['@0', '@1', '@2'])
  const named = three(['n0', 'n1', 'n2'], ['甲', '乙二', '丙'])
  const L = (v: SavedView[]) => ({ views: v })
  const start = (): Tabs<{ views: SavedView[] }> => {
    const first = L(views)
    const wc = run(initialWorkingCopy('@1', { view: null, sort: null }), list(views), {
      kind: 'edit',
      patch: { layout: 'cards' },
      base: stateOfView(views[1]),
    }).wc
    return { list: first, seen: first, wc, answer: null }
  }
  it('takes the answer only to place the view, then the views read after it; the answer is never the list shown', () => {
    const t = {
      ...start(),
      answer: {
        op: 'rename' as const,
        on: '@1',
        sent: views.map((v, i) => (i === 1 ? { ...v, name: '乙二' } : v)),
        got: named,
      },
    }
    const fresh = L([...named, mk('x', '别人的')])
    const out = catchUp(t, fresh)
    expect(out.tabs.list).toBe(fresh)
    expect([out.tabs.wc.viewKey, out.tabs.wc.patch, out.lost, out.remember, out.tabs.answer]).toEqual([
      'n1',
      { layout: 'cards' },
      null,
      'n1',
      null,
    ])
  })
  it('with no views read since (the read failed), the tabs stay as they were and nothing is dropped', () => {
    const t0 = start()
    const t = {
      ...t0,
      answer: {
        op: 'rename' as const,
        on: '@1',
        sent: views.map((v, i) => (i === 1 ? { ...v, name: '乙二' } : v)),
        got: named,
      },
    }
    const out = catchUp(t, t0.seen)
    expect(out.tabs.list).toBe(t0.list)
    expect([out.tabs.wc.viewKey, out.tabs.wc.patch, out.lost]).toEqual(['n1', { layout: 'cards' }, null])
  })
  it('nothing new: the same tabs', () => {
    const t = start()
    expect(catchUp(t, t.seen).tabs).toBe(t)
  })
})

describe('round 9: a change dropped at the first reading', () => {
  const views = three(['a', 'b', 'c'])
  it("the sort of an address whose view is gone (a reload, a link): dropped with a word that the link's view is gone", () => {
    const out = run(initialWorkingCopy('a', { view: 'gone', sort: 'title' }), list(views))
    expect([out.wc.viewKey, out.wc.patch, out.lost]).toEqual(['a', {}, { name: '', link: true }])
  })
  it('a change made in this visit before the views were read is not dropped silently', () => {
    const wc = run(initialWorkingCopy('', { view: null, sort: null }), {
      kind: 'edit',
      patch: { sort: 'title' },
      base: stateOfView(mk('', '')),
    }).wc
    const out = run(wc, list(views))
    expect([out.wc.viewKey, out.wc.patch, out.lost]).toEqual(['a', {}, { name: '' }])
  })
  it('the sort of an address changed in this visit before the views were read: dropped with the usual word', () => {
    const wc = run(initialWorkingCopy('', { view: null, sort: 'title' }), {
      kind: 'edit',
      patch: { sort: '-title' },
      base: stateOfView(mk('', '')),
    }).wc
    expect(run(wc, list(views)).lost).toEqual({ name: '' })
  })
  it("the sort of an address whose view is there stays, and a later loss is said with the view's name", () => {
    const kept = run(initialWorkingCopy('a', { view: 'b', sort: 'title' }), list(views))
    expect([kept.wc.viewKey, kept.wc.patch, kept.lost]).toEqual(['b', { sort: 'title' }, null])
    expect(run(kept.wc, list([views[0], views[2]])).lost).toEqual({ name: '乙' })
  })
})

describe('the address before the views are read', () => {
  const views = three(['a', 'b', 'c'])
  it('keeps "sort=" as given (no comparison with a view not read yet): the first list decides', () => {
    const wc = run(initialWorkingCopy('', { view: null, sort: null }), {
      kind: 'address',
      view: '',
      sort: '',
      base: stateOfView(mk('', '')),
    }).wc
    expect(wc).toEqual({ viewKey: '', snapshot: null, patch: { sort: '' }, fromAddress: true })
    // The implicit view is not there any more: the link's sort is dropped with the word for a link.
    expect(run(wc, list(views)).lost).toEqual({ name: '', link: true })
    // Still no view: the sort stays.
    expect(run(wc, list([])).wc.patch).toEqual({ sort: '' })
  })
  it('a sort for the view it names, given before the views are read, is kept on it', () => {
    const wc = run(initialWorkingCopy('b', { view: null, sort: null }), {
      kind: 'address',
      view: 'b',
      sort: 'created',
      base: stateOfView(mk('', '')),
    }).wc
    const out = run(wc, list(views))
    expect([out.wc.viewKey, out.wc.patch, out.lost]).toEqual(['b', { sort: 'created' }, null])
  })
})
