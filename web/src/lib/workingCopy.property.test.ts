import { describe, expect, it } from 'vitest'
import type { SavedView, ViewLayout } from '../api/types.ts'
import { DEFAULT_STATE, moveView, sameState, stateOfView, viewAtMark, withState, type ViewState } from './savedViews.ts'
import { hasEdits, initialWorkingCopy, reduceWorkingCopy, type Patch, type ViewOp, type WorkingCopy, type WorkingEvent } from './workingCopy.ts'

// Random interleavings of everything that can happen to the views of a listing, driven through the reducer the way the
// page drives it (useWorkingCopy, PageListView, ViewBar), with the server played here. After every step:
//  (a) every value of the change is one the person set (or chose by a revert or a pick) on the view it is shown on;
//  (b) a change is never dropped without a notice, unless by the person's own revert, pick or delete of that view;
//  (c) the answer of the person's own write never changes what is shown of the view (no edit made meanwhile is lost).
// A view is told by an identity of its own here, so a view written by hand ("@n") that a write names is the same view.

const SEQUENCES = 20_000

function rng(seed: number) {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

const SORTS = ['', 'title', '-title', 'created']
const LAYOUTS: ViewLayout[] = ['list', 'cards', 'table']
const KEYS = ['sort', 'layout'] as const
const MAX = 6

interface ServerView {
  uid: number
  id: string
  name: string
  layout: ViewLayout
  sort: string
}
interface Listing {
  views: SavedView[]
  rev: number
}
type Draft = Omit<SavedView, 'id'> & { id?: string }
interface Inflight {
  op: ViewOp
  on: string
  onIdent: string
  sent: Draft[]
  baseRev: number
  working: ViewState
  base: ViewState
  result?: { ok: true; got: Listing } | { ok: false; now: Listing }
}

const toSaved = (v: ServerView): SavedView => ({ id: v.id, name: v.name, layout: v.layout, sort: v.sort, group: '', filter: [], hidden: [] })
const val = (v: unknown) => JSON.stringify(v ?? null)
function ok(cond: boolean, msg: () => string): asserts cond {
  if (!cond) expect.fail(msg())
}
const sameKey = (k: (typeof KEYS)[number], a: ViewState, b: ViewState) => sameState({ ...DEFAULT_STATE, [k]: a[k] }, { ...DEFAULT_STATE, [k]: b[k] })

class Sim {
  private r: () => number
  private server: ServerView[] = []
  private rev = 0
  private uid = 0
  private names = 0
  private idents = new Map<string, string>()
  private list: Listing | null = null
  private wc: WorkingCopy
  private address: string
  private inflight: Inflight | null = null
  private history = new Map<string, Map<string, Set<string>>>()
  readonly log: string[] = []

  constructor(seed: number) {
    this.r = rng(seed)
    const n = this.int(4)
    const hand = n > 0 && this.r() < 0.4
    for (let i = 0; i < n; i++) {
      const uid = this.uid++
      this.server.push({ uid, id: hand ? `@${i}` : `r${uid}`, name: `v${uid}`, layout: this.pick(LAYOUTS), sort: this.pick(SORTS) })
    }
    this.register()
    const ids = this.server.map((v) => v.id)
    const remembered = this.pick(['', ...ids, '@1', 'gone'])
    const address = { view: this.pick<string | null>([null, ...ids, 'zz']), sort: this.r() < 0.4 ? this.pick(SORTS) : null }
    this.wc = initialWorkingCopy(remembered, address)
    if (address.sort !== null && (address.view !== null || remembered === '')) this.note(address.view ?? '', 'sort', address.sort)
    this.address = JSON.stringify([address.view, address.sort])
    this.log.push(`start ${JSON.stringify({ server: this.server, remembered, address })}`)
  }

  private int(n: number) {
    return Math.floor(this.r() * n)
  }
  private pick<T>(xs: readonly T[]): T {
    return xs[this.int(xs.length)]
  }
  private register() {
    for (const v of this.server) this.idents.set(v.id, `u${v.uid}`)
  }
  private ident(id: string) {
    return id === '' ? 'implicit' : (this.idents.get(id) ?? `id:${id}`)
  }
  private note(id: string, k: string, v: unknown) {
    const who = this.ident(id)
    const h = this.history.get(who) ?? new Map<string, Set<string>>()
    this.history.set(who, h)
    const s = h.get(k) ?? new Set<string>()
    h.set(k, s)
    s.add(val(v))
  }
  private noteState(id: string, s: ViewState) {
    for (const k of KEYS) this.note(id, k, s[k])
  }
  private serverList(): Listing {
    return { views: this.server.map(toSaved), rev: this.rev }
  }
  // Any write names the views written by hand.
  private written() {
    this.rev++
    for (const v of this.server) if (v.id.startsWith('@')) v.id = `n${v.uid}`
    this.register()
  }
  private fresh(prefix: string) {
    return `${prefix}${this.names++}`
  }

  // What the page derives (PageListView).
  private ui() {
    const saved = this.list?.views ?? []
    const own = saved.find((v) => v.id === this.wc.viewKey) ?? null
    const active = own ?? viewAtMark(saved, this.wc.viewKey) ?? saved[0] ?? null
    const base = active ? stateOfView(active) : DEFAULT_STATE
    const mine = own !== null || (active === null && this.wc.viewKey === '')
    const state: ViewState = { ...base, ...(mine ? this.wc.patch : {}) }
    return { saved, active, base, mine, state, modified: active !== null && !sameState(state, base) }
  }
  private shown(views: readonly SavedView[] | undefined, wc: WorkingCopy): ViewState | null {
    if (wc.viewKey === '') return { ...DEFAULT_STATE, ...wc.patch }
    const v = views?.find((x) => x.id === wc.viewKey)
    return v ? { ...stateOfView(v), ...wc.patch } : null
  }

  // One event through the reducer, the way useWorkingCopy applies it, and the invariants checked.
  private apply(e: WorkingEvent, listing: Listing | null, how: { exempt?: boolean; owner?: string; mine?: boolean; stale?: boolean } = {}) {
    const before = this.wc
    const shownBefore = this.shown(this.list?.views, before)
    const out = reduceWorkingCopy(before, e)
    const where = () => `\n${this.log.join('\n')}\n→ ${JSON.stringify(out)}`
    if (!out.adopt) ok(out.wc === before, () => `held, yet changed${where()}`)
    if (out.lost) ok(hasEdits(before.patch), () => `a notice with nothing dropped${where()}`)
    this.wc = out.wc
    if (out.adopt && listing) this.list = listing
    // (a)
    const h = this.history.get(this.ident(out.wc.viewKey))
    for (const [k, v] of Object.entries(out.wc.patch)) ok(h?.get(k)?.has(val(v)) === true, () => `(a) ${k}=${val(v)} was never set on ${this.ident(out.wc.viewKey)}${where()}`)
    if (out.wc === before || out.lost) return
    const after = this.shown(this.list?.views, out.wc)
    // (b)
    if (!how.exempt && hasEdits(before.patch)) {
      ok(this.ident(out.wc.viewKey) === (how.owner ?? this.ident(before.viewKey)), () => `(b) the change left its view silently${where()}`)
      ok(after !== null, () => `(b) the view shown is not in the list${where()}`)
      const full = { ...DEFAULT_STATE, ...before.patch }
      for (const k of Object.keys(before.patch) as (typeof KEYS)[number][]) ok(sameKey(k, after!, full), () => `(b) ${k} dropped silently${where()}`)
    }
    // (c): the whole state shown, when the write was about the view shown. Not when the answer is older than a list
    // already shown (read after the write landed, before its answer came): it can show older settings for a moment, and
    // only (b) holds then (nothing the person changed is lost).
    if (how.mine && !how.stale && !how.exempt && shownBefore) ok(after !== null && sameState(after, shownBefore), () => `(c) the answer changed what is shown${where()}`)
  }

  // The address follows the working copy (PageListView), and the address step of useWorkingCopy sees it: never a change.
  private echo() {
    const sort = this.wc.patch.sort ?? null
    const view = sort !== null && this.wc.viewKey !== '' ? this.wc.viewKey : null
    const key = JSON.stringify([view, sort])
    if (key === this.address) return
    this.address = key
    const v = this.list?.views.find((x) => x.id === this.wc.viewKey)
    const before = this.wc
    const out = reduceWorkingCopy(before, { kind: 'address', view: view ?? '', sort, base: v ? stateOfView(v) : DEFAULT_STATE })
    ok(out.wc === before, () => `the address written from the working copy changed it\n${this.log.join('\n')}\n→ ${JSON.stringify(out.wc)}`)
  }

  step() {
    const u = this.ui()
    const f = this.inflight
    const choices: [number, () => void][] = [
      [u.mine ? 30 : 0, () => this.edit()],
      [u.mine && u.modified ? 5 : 0, () => this.revert()],
      [u.saved.length > 0 ? 8 : 0, () => this.pickView()],
      [this.list && !f ? 12 : 0, () => this.own()],
      [f && !f.result ? 10 : 0, () => this.commit()],
      [f?.result ? 10 : 0, () => this.answer()],
      [8, () => this.other()],
      [12, () => this.refetch()],
    ]
    const total = choices.reduce((s, [w]) => s + w, 0)
    let x = this.r() * total
    for (const [w, act] of choices) {
      if ((x -= w) < 0) {
        act()
        break
      }
    }
    this.echo()
  }

  finish() {
    if (this.inflight && !this.inflight.result) this.commit()
    if (this.inflight) this.answer()
    this.refetch()
    this.echo()
  }

  private edit() {
    const k = this.pick(KEYS)
    const v = k === 'sort' ? this.pick(SORTS) : this.pick(LAYOUTS)
    const patch: Patch = { [k]: v }
    this.log.push(`edit ${JSON.stringify(patch)}`)
    this.note(this.wc.viewKey, k, v)
    this.apply({ kind: 'edit', patch, base: this.ui().base }, null, { exempt: true })
  }
  private revert() {
    this.log.push('revert')
    this.noteState(this.wc.viewKey, this.ui().base)
    this.apply({ kind: 'revert' }, null, { exempt: true })
  }
  private pickView() {
    const v = this.pick(this.ui().saved)
    this.log.push(`pick ${v.id}`)
    this.noteState(v.id, stateOfView(v))
    this.apply({ kind: 'pick', id: v.id, views: this.list?.views ?? [] }, null, { exempt: true })
  }

  // A write of the person, as ViewBar makes it.
  private own() {
    const { saved: views, active, base, mine, state, modified } = this.ui()
    const ops: ViewOp[] = []
    if (active && mine && modified) ops.push('save')
    if (views.length < MAX) ops.push('create')
    if (active && views.length < MAX) ops.push('duplicate')
    if (active) ops.push('rename', 'delete')
    if (active && views.length > 1) ops.push('reorder')
    if (ops.length === 0) return
    const op = this.pick(ops)
    let on = active?.id ?? ''
    let sent: Draft[]
    if (op === 'save') sent = views.map((v) => (v.id === active!.id ? withState(v, state) : v))
    else if (op === 'create') sent = [...views, withState({ name: this.fresh('c') } as Draft, state)]
    else if (op === 'duplicate') {
      const { id, ...rest } = active!
      void id
      sent = [...views, withState({ ...rest, name: this.fresh('d') }, state)]
    } else if (op === 'rename') sent = views.map((v) => (v.id === active!.id ? { ...v, name: this.fresh('n') } : v))
    else if (op === 'reorder') {
      const target = views[this.int(views.length)]
      if (target.id === active!.id) return
      sent = moveView(views, active!.id, views.indexOf(target))
    } else sent = views.filter((v) => v.id !== active!.id)
    if (op === 'create') on = active?.id ?? ''
    this.inflight = { op, on, onIdent: this.ident(on), sent, baseRev: this.list!.rev, working: state, base }
    this.log.push(`own ${op} on ${on} (rev ${this.list!.rev}) working ${JSON.stringify(state)}`)
  }
  private commit() {
    const f = this.inflight!
    if (f.baseRev !== this.rev) {
      f.result = { ok: false, now: this.serverList() }
      this.log.push('commit: 409')
      return
    }
    this.server = f.sent.map((d): ServerView => {
      // What the app writes are views (withState keeps their fields as they are).
      const w = { name: d.name as string, layout: d.layout as ViewLayout, sort: d.sort as string }
      if (d.id !== undefined) {
        const s = this.server.find((x) => x.id === d.id)!
        return { ...s, ...w }
      }
      const uid = this.uid++
      return { uid, id: `r${uid}`, ...w }
    })
    this.written()
    f.result = { ok: true, got: this.serverList() }
    this.log.push(`commit: ${JSON.stringify(this.server)}`)
  }
  private answer() {
    const f = this.inflight!
    const res = f.result!
    if (res.ok) {
      const mine = this.ident(this.wc.viewKey) === f.onIdent
      const made = (f.op === 'create' || f.op === 'duplicate') && mine ? res.got.views.at(-1)! : null
      this.log.push(`answer ${f.op}`)
      if (made) {
        // What was changed on the view shown goes on, on the view made from it.
        const src = this.history.get(f.onIdent)
        const to = this.ident(made.id)
        const h = this.history.get(to) ?? new Map<string, Set<string>>()
        for (const [k, s] of src ?? []) h.set(k, new Set([...(h.get(k) ?? []), ...s]))
        this.history.set(to, h)
      }
      this.apply({ kind: 'written', op: f.op, on: f.on, sent: f.sent, got: res.got.views, working: f.working, base: f.base }, res.got, {
        mine,
        stale: this.list !== null && this.list.rev > res.got.rev,
        exempt: mine && f.op === 'delete',
        owner: made ? this.ident(made.id) : undefined,
      })
      this.inflight = null
      this.apply({ kind: 'list', views: res.got.views, pending: false }, res.got)
    } else {
      this.log.push(`answer 409`)
      this.apply({ kind: 'list', views: res.now.views, pending: true }, res.now)
      this.inflight = null
      this.apply({ kind: 'list', views: res.now.views, pending: false }, res.now)
    }
  }
  // Someone else writes the views.
  private other() {
    const s = this.server
    const kinds = ['add', ...(s.length ? ['delete', 'rename', 'state', 'reorder'] : [])]
    const k = this.pick(kinds)
    if (k === 'add' && s.length < MAX) {
      const uid = this.uid++
      s.splice(this.int(s.length + 1), 0, { uid, id: `r${uid}`, name: this.fresh('o'), layout: this.pick(LAYOUTS), sort: this.pick(SORTS) })
    } else if (k === 'delete') s.splice(this.int(s.length), 1)
    else if (k === 'rename') this.pick(s).name = this.fresh('o')
    else if (k === 'state') {
      const v = this.pick(s)
      if (this.r() < 0.5) v.sort = this.pick(SORTS)
      else v.layout = this.pick(LAYOUTS)
    } else if (k === 'reorder') {
      const [v] = s.splice(this.int(s.length), 1)
      s.splice(this.int(s.length + 1), 0, v)
    }
    this.written()
    this.log.push(`other ${k}: ${JSON.stringify(this.server)}`)
  }
  private refetch() {
    const l = this.serverList()
    this.log.push(`refetch (pending ${this.inflight !== null})`)
    this.apply({ kind: 'list', views: l.views, pending: this.inflight !== null }, l)
  }
}

describe('the working copy, under random interleavings', () => {
  it(`keeps (a), (b) and (c) over ${SEQUENCES} sequences`, () => {
    for (let seed = 1; seed <= SEQUENCES; seed++) {
      const sim = new Sim(seed)
      const r = rng(seed ^ 0x9e3779b9)
      const n = 5 + Math.floor(r() * 40)
      for (let i = 0; i < n; i++) sim.step()
      sim.finish()
    }
  }, 120_000)
})
