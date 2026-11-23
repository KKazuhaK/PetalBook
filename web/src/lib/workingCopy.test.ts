import { describe, expect, it } from 'vitest'
import type { SavedView } from '../api/types.ts'
import { stateOfView } from './savedViews.ts'
import { IMPLICIT, initialWorkingCopy, prune, reduceWorkingCopy, type Outcome, type Patch, type WorkingCopy, type WorkingEvent } from './workingCopy.ts'

// The one function that decides what becomes of the working copy, for every event, every kind of view (a real id, a
// view written by hand that is still "@n") and every kind of change (the sort only, something else, both).

const mk = (id: string, name: string, extra: Partial<SavedView> = {}): SavedView => ({ id, name, layout: 'list', sort: '', group: '', filter: [], hidden: [], ...extra })

type Kind = 'real' | 'hand'
const KINDS: Kind[] = ['real', 'hand']
const PATCHES: Record<string, Patch> = { 'sort only': { sort: 'title' }, other: { layout: 'cards' }, both: { sort: 'title', layout: 'cards' } }

// The ids a kind of view has before and after someone's write names the views ("@n" become real ids).
const before = (k: Kind) => (k === 'real' ? ['r0', 'r1', 'r2'] : ['@0', '@1', '@2'])
const after = (k: Kind) => (k === 'real' ? ['r0', 'r1', 'r2'] : ['n0', 'n1', 'n2'])
const three = (ids: string[], names = ['甲', '乙', '丙']) => names.map((n, i) => mk(ids[i], n))

const run = (wc: WorkingCopy, ...events: WorkingEvent[]): Outcome => {
  let out: Outcome = { wc, adopt: true, lost: null, remember: null }
  for (const e of events) out = reduceWorkingCopy(out.wc, e)
  return out
}

// The second view (乙) shown and changed.
function start(k: Kind, patch: Patch): { views: SavedView[]; wc: WorkingCopy } {
  const views = three(before(k))
  const first = run(initialWorkingCopy(before(k)[1], { view: null, sort: null }), { kind: 'list', views, pending: false }).wc
  return { views, wc: run(first, { kind: 'edit', patch, base: stateOfView(views[1]) }).wc }
}

const list = (views: SavedView[], pending = false): WorkingEvent => ({ kind: 'list', views, pending })
const written = (op: 'save' | 'create' | 'duplicate' | 'rename' | 'reorder' | 'delete', on: string, sent: SavedView[] | { name: string; id?: string }[], got: SavedView[]): WorkingEvent => ({ kind: 'written', op, on, sent, got })

interface Expect {
  shown: string
  // The change is still there (on the view shown), or it is gone.
  patch: 'kept' | 'gone'
  lost: boolean
  remember?: string | null
  adopt?: boolean
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
  { name: 'refetch, nothing changed', events: (_k, views) => [list(three(views.map((v) => v.id)))], expect: { shown: '乙', patch: 'kept', lost: false, remember: null } },
  {
    name: 'refetch, someone else named the views and moved this one to the front',
    events: (k) => [list([mk(after(k)[1], '乙'), mk(after(k)[0], '甲'), mk(after(k)[2], '丙')])],
    expect: (k) => ({ shown: '乙', patch: 'kept', lost: false, remember: k === 'hand' ? 'n1' : null }),
  },
  { name: 'refetch, someone else inserted a view before it', events: (k) => [list([mk('x', '新'), ...three(after(k))])], expect: { shown: '乙', patch: 'kept', lost: false } },
  { name: 'refetch, someone else deleted it', events: (k) => [list([mk(after(k)[0], '甲'), mk(after(k)[2], '丙')])], expect: { shown: '甲', patch: 'gone', lost: true, remember: '' } },
  {
    name: 'refetch, someone else renamed it',
    events: (k) => [list(three(after(k), ['甲', '乙乙', '丙']))],
    // A real id is the same view under another name; an "@n" can only be told by its name and settings.
    expect: (k) => (k === 'real' ? { shown: '乙乙', patch: 'kept', lost: false } : { shown: '甲', patch: 'gone', lost: true, remember: '' }),
  },
  {
    name: 'refetch, the file was edited by hand: the view at its place has another name',
    events: (k) => [list(three(before(k), ['甲', '乙乙', '丙']))],
    expect: (k) => (k === 'real' ? { shown: '乙乙', patch: 'kept', lost: false } : { shown: '甲', patch: 'gone', lost: true }),
  },
  {
    name: 'refetch, someone else changed its settings',
    events: (k) => [list([mk(after(k)[0], '甲'), mk(after(k)[1], '乙', { group: 'status' }), mk(after(k)[2], '丙')])],
    expect: (k) => (k === 'real' ? { shown: '乙', patch: 'kept', lost: false } : { shown: '甲', patch: 'gone', lost: true }),
  },
  { name: 'refetch, two views now look like it', events: (k) => [list(three(after(k), ['丙', '乙', '乙']))], expect: (k) => (k === 'real' ? { shown: '乙', patch: 'kept', lost: false } : { shown: '丙', patch: 'gone', lost: true }) },
  { name: 'refetch, every view deleted', events: () => [list([])], expect: { shown: '(implicit)', patch: 'gone', lost: true, remember: '' } },
  {
    name: 'refetch while a write of the person is on its way, that cannot place the view: waits',
    events: (k) => [list([mk(after(k)[0], '甲'), mk(after(k)[2], '丙')], true)],
    expect: { shown: '乙', patch: 'kept', lost: false, adopt: false },
  },
  {
    name: 'refused write (409): the views as they are, the write settles, the view was deleted',
    events: (k) => [list([mk(after(k)[0], '甲'), mk(after(k)[2], '丙')], true), list([mk(after(k)[0], '甲'), mk(after(k)[2], '丙')], false)],
    expect: { shown: '甲', patch: 'gone', lost: true, remember: '' },
  },
  {
    name: 'refused write (409): someone else had named the views',
    events: (k) => [list(three(after(k)), true), list(three(after(k)), false)],
    expect: { shown: '乙', patch: 'kept', lost: false },
  },
  {
    name: 'own save',
    events: (k, views) => [written('save', views[1].id, views, three(after(k)).map((v, i) => (i === 1 ? { ...v, layout: 'cards', sort: 'title' } : v)))],
    expect: (k) => ({ shown: '乙', patch: 'gone', lost: false, remember: after(k)[1] }),
  },
  {
    name: 'own save, a refetch with what it wrote landed first',
    events: (k, views) => {
      const got = three(after(k)).map((v, i) => (i === 1 ? { ...v, layout: 'cards' as const, sort: 'title' } : v))
      return [list(got, true), written('save', views[1].id, views, got), list(got, false)]
    },
    expect: { shown: '乙', patch: 'gone', lost: false },
  },
  { name: 'own new view', events: (k, views) => [written('create', views[1].id, [...views, { name: '新' }], [...three(after(k)), mk('x', '新')])], expect: { shown: '新', patch: 'gone', lost: false, remember: 'x' } },
  {
    name: 'own copy of the view shown',
    events: (k, views) => [written('duplicate', views[1].id, [...views, { name: '乙 copy' }], [...three(after(k)), mk('x', '乙 copy', { layout: 'cards' })])],
    expect: { shown: '乙 copy', patch: 'gone', lost: false, remember: 'x' },
  },
  {
    name: 'own rename of the view shown',
    events: (k, views) => [written('rename', views[1].id, views.map((v, i) => (i === 1 ? { ...v, name: '乙二' } : v)), three(after(k), ['甲', '乙二', '丙']))],
    expect: { shown: '乙二', patch: 'kept', lost: false },
  },
  {
    name: 'own move of the view shown, next to a view with the same name',
    events: (k, views) => {
      const sent = [{ ...views[1] }, { ...views[0], name: '乙' }, views[2]]
      return [written('reorder', views[1].id, sent, three(after(k), ['乙', '乙', '丙']).map((v, i) => ({ ...v, id: `${after(k)[i]}m` })))]
    },
    expect: (k) => ({ shown: '乙', patch: 'kept', lost: false, remember: `${after(k)[0]}m` }),
  },
  {
    name: 'own move of another view',
    events: (k, views) => [written('reorder', views[2].id, [views[2], views[0], views[1]], [mk(after(k)[2], '丙'), mk(after(k)[0], '甲'), mk(after(k)[1], '乙')])],
    expect: { shown: '乙', patch: 'kept', lost: false },
  },
  { name: 'own delete of the view shown', events: (k, views) => [written('delete', views[1].id, [views[0], views[2]], [mk(after(k)[0], '甲'), mk(after(k)[2], '丙')])], expect: { shown: '甲', patch: 'gone', lost: false, remember: '' } },
  { name: 'own delete of another view', events: (k, views) => [written('delete', views[0].id, [views[1], views[2]], [mk(after(k)[1], '乙'), mk(after(k)[2], '丙')])], expect: { shown: '乙', patch: 'kept', lost: false } },
  { name: 'pick another view', events: (_k, views) => [{ kind: 'pick', id: views[2].id, views }], expect: { shown: '丙', patch: 'gone', lost: false } },
  { name: 'pick the view shown again', events: (_k, views) => [{ kind: 'pick', id: views[1].id, views }], expect: { shown: '乙', patch: 'kept', lost: false, remember: null } },
  { name: 'revert', events: () => [{ kind: 'revert' }], expect: { shown: '乙', patch: 'gone', lost: false } },
  {
    name: 'the address names another view with a sort: not this view\'s',
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
          expect(out.adopt).toBe(want.adopt ?? true)
        })
      }
})

describe('the sort in the address', () => {
  const views = three(['r0', 'r1', 'r2'])
  const shown = run(initialWorkingCopy('r1', { view: null, sort: null }), list(views)).wc
  it('changes the sort of the view it names, and only the sort', () => {
    const wc = run(shown, { kind: 'edit', patch: { layout: 'cards', sort: 'title' }, base: stateOfView(views[1]) }).wc
    expect(run(wc, { kind: 'address', view: 'r1', sort: '-title', base: stateOfView(views[1]) }).wc.patch).toEqual({ layout: 'cards', sort: '-title' })
    expect(run(wc, { kind: 'address', view: 'r1', sort: null, base: stateOfView(views[1]) }).wc.patch).toEqual({ layout: 'cards' })
    expect(run(wc, { kind: 'address', view: 'r1', sort: 'title', base: stateOfView(views[1]) }).wc).toBe(wc)
  })
  it('starts a listing with the sort of the view it names, else ignores it unless it is the implicit view', () => {
    expect(initialWorkingCopy('r0', { view: 'r1', sort: 'title' })).toEqual({ viewKey: 'r1', snapshot: null, patch: { sort: 'title' }, fromAddress: true })
    expect(initialWorkingCopy('r0', { view: null, sort: 'title' })).toEqual({ viewKey: 'r0', snapshot: null, patch: {} })
    expect(initialWorkingCopy('', { view: null, sort: 'title' })).toEqual({ viewKey: '', snapshot: null, patch: { sort: 'title' }, fromAddress: true })
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
    const wc = run(initialWorkingCopy('', { view: null, sort: null }), list([]), { kind: 'edit', patch: { sort: 'title' }, base: stateOfView(mk('', '')) }).wc
    const out = run(wc, list([mk('a', '甲')]))
    expect([out.wc.viewKey, out.wc.patch, out.lost]).toEqual(['a', {}, { name: '' }])
  })
  it('the person made the first view from the implicit one: shown, and nothing is said', () => {
    const wc = run(initialWorkingCopy('', { view: null, sort: null }), list([]), { kind: 'edit', patch: { sort: 'title' }, base: stateOfView(mk('', '')) }).wc
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

describe('edits made while the person\'s own write is on its way', () => {
  const edit = (patch: Patch, views: SavedView[]): WorkingEvent => ({ kind: 'edit', patch, base: stateOfView(views[1]) })
  const open = (k: Kind) => {
    const views = three(before(k))
    const wc = run(initialWorkingCopy(before(k)[1], { view: null, sort: null }), list(views)).wc
    return { views, wc }
  }
  const working = (views: SavedView[], p: Patch) => ({ ...stateOfView(views[1]), ...p })

  for (const k of KINDS) {
    it(`save (${k}): what was saved is gone from the change, what was changed since stays`, () => {
      const { views, wc } = open(k)
      const sentPatch: Patch = { layout: 'cards' }
      const w = run(wc, edit(sentPatch, views))
      const later = run(w.wc, edit({ sort: 'title' }, views))
      const got = three(after(k)).map((v, i) => (i === 1 ? { ...v, layout: 'cards' as const } : v))
      const out = run(later.wc, { kind: 'written', op: 'save', on: views[1].id, sent: views, got, working: working(views, sentPatch) })
      expect([out.wc.viewKey, out.wc.patch, out.lost]).toEqual([after(k)[1], { sort: 'title' }, null])
    })
    it(`save (${k}): a value changed again since is kept, one changed back to what was saved is not`, () => {
      const { views, wc } = open(k)
      const w = run(wc, edit({ layout: 'cards', sort: 'title' }, views), edit({ layout: 'board' as never }, views))
      const got = three(after(k)).map((v, i) => (i === 1 ? { ...v, layout: 'cards' as const, sort: 'title' } : v))
      const out = run(w.wc, { kind: 'written', op: 'save', on: views[1].id, sent: views, got, working: working(views, { layout: 'cards', sort: 'title' }) })
      expect(out.wc.patch).toEqual({ layout: 'board' })
    })
    for (const op of ['create', 'duplicate'] as const) {
      it(`${op} (${k}): the change made since is carried onto the view now shown`, () => {
        const { views, wc } = open(k)
        const w = run(wc, edit({ layout: 'cards' }, views))
        const later = run(w.wc, edit({ sort: 'title' }, views))
        const made = mk('x', '新', { layout: 'cards' })
        const out = run(later.wc, { kind: 'written', op, on: views[1].id, sent: [...views, { name: '新' }], got: [...three(after(k)), made], working: working(views, { layout: 'cards' }) })
        expect([out.wc.viewKey, out.wc.patch, out.lost, out.remember]).toEqual(['x', { sort: 'title' }, null, 'x'])
      })
    }
  }
  it('nothing changed since: nothing is carried', () => {
    const { views, wc } = open('real')
    const w = run(wc, edit({ layout: 'cards' }, views))
    const out = run(w.wc, { kind: 'written', op: 'create', on: views[1].id, sent: [...views, { name: '新' }], got: [...three(after('real')), mk('x', '新', { layout: 'cards' })], working: working(views, { layout: 'cards' }) })
    expect(out.wc.patch).toEqual({})
  })
})

describe('round 9: the person\'s own write and what is changed meanwhile', () => {
  const views = three(['a', 'b', 'c'])
  const B = stateOfView(views[1])
  const onB = run(initialWorkingCopy('b', { view: null, sort: null }), list(views)).wc
  const edit = (patch: Patch, base = B): WorkingEvent => ({ kind: 'edit', patch, base })
  const savedB = (sort: string) => views.map((v) => (v.id === 'b' ? { ...v, sort } : v))
  const shownOf = (o: Outcome, got: SavedView[]) => ({ ...stateOfView(got.find((v) => v.id === o.wc.viewKey)!), ...o.wc.patch })

  it('deleting the view shown: a change made while the delete is on its way is not put on the view shown next', () => {
    const w = run(onB, edit({ sort: 'title' }))
    const got = [views[0], views[2]]
    const out = run(w.wc, { kind: 'written', op: 'delete', on: 'b', sent: got, got, working: { ...B }, base: B })
    expect([out.wc.viewKey, out.wc.patch, out.lost]).toEqual(['a', {}, null])
  })

  for (const back of ['an edit back to the old sort', 'a revert'] as const)
    it(`saving: ${back} made while the save is on its way is kept as a change of the saved view`, () => {
      const w = run(onB, edit({ sort: 'title' })).wc
      const working = { ...B, sort: 'title' }
      const later = run(w, back === 'a revert' ? { kind: 'revert' } : edit({ sort: B.sort })).wc
      const got = savedB('title')
      const out = run(later, { kind: 'written', op: 'save', on: 'b', sent: got, got, working, base: B })
      expect([out.wc.viewKey, out.wc.patch, out.lost]).toEqual(['b', { sort: '' }, null])
      expect(shownOf(out, got).sort).toBe('')
      // A list with the save in it, read before the answer came, waits for the answer instead of re-basing the change.
      const early = reduceWorkingCopy(later, list(got, true))
      expect([early.wc, early.adopt]).toEqual([later, false])
      expect(run(early.wc, { kind: 'written', op: 'save', on: 'b', sent: got, got, working, base: B }).wc.patch).toEqual({ sort: '' })
    })

  it('saving: the view picked again from a list with the save in it keeps the change made on it as it is', () => {
    const w = run(onB, edit({ sort: 'title' })).wc
    const got = savedB('title')
    const repicked = run(w, { kind: 'pick', id: 'a', views }, list(got, true), { kind: 'pick', id: 'b', views: got }, edit({ layout: 'cards' }, stateOfView(got[1]))).wc
    const out = run(repicked, { kind: 'written', op: 'save', on: 'b', sent: got, got, working: { ...B, sort: 'title' }, base: B })
    expect(out.wc.patch).toEqual({ layout: 'cards' })
  })
})

describe('round 9: a change dropped at the first reading', () => {
  const views = three(['a', 'b', 'c'])
  it('the sort of an address whose view is gone (a reload, a link): dropped with a word that the link\'s view is gone', () => {
    const out = run(initialWorkingCopy('a', { view: 'gone', sort: 'title' }), list(views))
    expect([out.wc.viewKey, out.wc.patch, out.lost]).toEqual(['a', {}, { name: '', link: true }])
  })
  it('a change made in this visit before the views were read is not dropped silently', () => {
    const wc = run(initialWorkingCopy('', { view: null, sort: null }), { kind: 'edit', patch: { sort: 'title' }, base: stateOfView(mk('', '')) }).wc
    const out = run(wc, list(views))
    expect([out.wc.viewKey, out.wc.patch, out.lost]).toEqual(['a', {}, { name: '' }])
  })
  it('the sort of an address changed in this visit before the views were read: dropped with the usual word', () => {
    const wc = run(initialWorkingCopy('', { view: null, sort: 'title' }), { kind: 'edit', patch: { sort: '-title' }, base: stateOfView(mk('', '')) }).wc
    expect(run(wc, list(views)).lost).toEqual({ name: '' })
  })
  it('the sort of an address whose view is there stays, and a later loss is said with the view\'s name', () => {
    const kept = run(initialWorkingCopy('a', { view: 'b', sort: 'title' }), list(views))
    expect([kept.wc.viewKey, kept.wc.patch, kept.lost]).toEqual(['b', { sort: 'title' }, null])
    expect(run(kept.wc, list([views[0], views[2]])).lost).toEqual({ name: '乙' })
  })
})
