import { describe, expect, it } from 'vitest'
import type { SavedView, ViewLayout } from '../api/types.ts'
import {
  DEFAULT_STATE,
  isHandMark,
  moveView,
  sameState,
  snapshotOf,
  stateOfView,
  viewAtMark,
  withState,
  type ViewState,
} from './savedViews.ts'
import {
  catchUp,
  hasEdits,
  initialWorkingCopy,
  reduceWorkingCopy,
  type Lost,
  type Patch,
  type Tabs,
  type ViewOp,
  type WorkingCopy,
  type WorkingEvent,
  type Written,
} from './workingCopy.ts'

// Random interleavings of everything that can happen to the views of a listing, driven the way the page drives the
// reducer (useWorkingCopy, PageListView, ViewBar, useSaveViews), with the server and the query cache played here: edits,
// reverts, picks, the person's own writes (with 409s, network errors and answers lost after the commit), other
// people's writes, reads of the views that are served at one moment and arrive later, a read called off when a write
// starts and the read made after its answer, address changes (back, forward, a link), and leaving and coming back while
// a write is on its way. While a write is on its way the page has the tabs and the tools off: an edit, a revert or a
// pick, or an address change, tried then is refused by the guard modelled here, and nothing of the views read is taken in.
// The read made after a write may fail or run out of time (a slow read is one that times out): when the write was
// answered, the answer is put in the cache as the views (unless a read made after it arrived), and the views are read
// again in the background a bounded number of times. A write that ran out of time may still be made by the server later.
// After every step:
//  (a) every value of the change is one the person set on the view it is shown on;
//  (b) a change is never dropped without a notice, unless by the person's own revert, pick or delete of that view, and
//      the answer of the person's own write never changes what is shown;
//  (c) every value of the change is the last one the person chose for it on that view, so a later Save never writes
//      older settings;
//  (d) once a write has settled (written, refused, failed, or with the page left and opened again) nothing is off.
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
// A read of the views: served (the server's list at that moment) and then arrived, or failed. At most one at a time,
// as the query cache has it; a read asked for while one is on its way joins it.
interface Get {
  served: Listing | null
}
interface Mutation {
  op: ViewOp
  on: string
  sent: Draft[]
  baseRev: number
  // The page that started it (a page opened again is another).
  page: number
  result: { ok: true; got: Listing } | { ok: false; unknown?: boolean } | null
  // After the answer: the read made then, which the write waits for.
  refetch: Get | null
  // How many times the cache had taken a list when the answer came.
  cacheAtAnswer: number
}
// A write whose time ran out before the server made it: the server may still make it (if nothing was written since).
interface Box {
  tabs: Tabs<Listing>
  writing: number
  address: string
}

const toSaved = (v: ServerView): SavedView => ({
  id: v.id,
  name: v.name,
  layout: v.layout,
  sort: v.sort,
  group: '',
  filter: [],
  hidden: [],
})
const val = (v: unknown) => JSON.stringify(v ?? null)
function ok(cond: boolean, msg: () => string): asserts cond {
  if (!cond) expect.fail(msg())
}
const sameKey = (k: (typeof KEYS)[number], a: ViewState, b: ViewState) =>
  sameState({ ...DEFAULT_STATE, [k]: a[k] }, { ...DEFAULT_STATE, [k]: b[k] })

class Sim {
  private r: () => number
  private server: ServerView[] = []
  private rev = 0
  private uid = 0
  private names = 0
  private idents = new Map<string, string>()
  private cache: Listing | undefined
  // How many times the cache took a list (dataUpdateCount).
  private cacheN = 0
  // Background reads left after a read that did not come after a write.
  private retries = 0
  private late: { sent: Draft[]; baseRev: number } | null = null
  private get: Get | null = { served: null }
  private mutation: Mutation | null = null
  private page = 0
  private box: Box
  // What the browser remembers as the view of the listing.
  private stored: string
  private history = new Map<string, Map<string, Set<string>>>()
  private latest = new Map<string, Record<string, string>>()
  private revOf = new WeakMap<Written, number>()
  refused = 0
  readonly log: string[] = []

  constructor(seed: number) {
    this.r = rng(seed)
    const n = this.int(4)
    const hand = n > 0 && this.r() < 0.4
    for (let i = 0; i < n; i++) {
      const uid = this.uid++
      this.server.push({
        uid,
        id: hand ? `@${i}` : `r${uid}`,
        name: `v${uid}`,
        layout: this.pick(LAYOUTS),
        sort: this.pick(SORTS),
      })
    }
    this.register()
    const ids = this.server.map((v) => v.id)
    this.stored = this.pick(['', ...ids, '@1', 'gone'])
    const address = {
      view: this.pick<string | null>([null, ...ids, 'zz']),
      sort: this.r() < 0.4 ? this.pick(SORTS) : null,
    }
    this.log.push(`start ${JSON.stringify({ server: this.server, remembered: this.stored, address })}`)
    this.box = this.open(address)
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
  private where = () => `\n${this.log.join('\n')}\n→ ${JSON.stringify(this.box.tabs.wc)}`

  // What the person chose: every value ever set on a view (a), and the last one for each setting (c).
  private note(id: string, k: string, v: unknown) {
    const who = this.ident(id)
    const h = this.history.get(who) ?? new Map<string, Set<string>>()
    this.history.set(who, h)
    const s = h.get(k) ?? new Set<string>()
    h.set(k, s)
    s.add(val(v))
    this.latest.set(who, { ...this.latest.get(who), [k]: val(v) })
  }
  private forget(id: string, k?: string) {
    const who = this.ident(id)
    if (k === undefined) return this.latest.set(who, {})
    const l = { ...this.latest.get(who) }
    delete l[k]
    this.latest.set(who, l)
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

  // The listing opened (again): the working copy from the browser's memory and the address.
  private open(address: { view: string | null; sort: string | null }): Box {
    this.page++
    const wc = initialWorkingCopy(this.stored, address)
    if (wc.patch.sort !== undefined) {
      this.forget(wc.viewKey)
      this.note(wc.viewKey, 'sort', wc.patch.sort)
    }
    // A page that opens reads the views (the cache may have them already).
    this.get ??= { served: null }
    return {
      tabs: { list: null, wc, seen: undefined, answer: null },
      writing: 0,
      address: JSON.stringify([address.view, address.sort]),
    }
  }

  // The guard of the page: off while a write is on its way (this page's, or one from before it was opened again).
  private busy() {
    return this.mutation !== null || this.box.writing > 0
  }

  // What the page derives (PageListView).
  private ui() {
    const saved = this.box.tabs.list?.views ?? []
    const wc = this.box.tabs.wc
    const own = saved.find((v) => v.id === wc.viewKey) ?? null
    const active = own ?? viewAtMark(saved, wc.viewKey) ?? saved[0] ?? null
    const base = active ? stateOfView(active) : DEFAULT_STATE
    const mine = own !== null || (active === null && wc.viewKey === '')
    const state: ViewState = { ...base, ...(mine ? wc.patch : {}) }
    return { saved, active, base, mine, state, modified: active !== null && !sameState(state, base) }
  }
  private shown(views: readonly SavedView[] | undefined, wc: WorkingCopy): ViewState | null {
    if (wc.viewKey === '') return { ...DEFAULT_STATE, ...wc.patch }
    const v = views?.find((x) => x.id === wc.viewKey)
    return v ? { ...stateOfView(v), ...wc.patch } : null
  }

  // (a) and (c), for the working copy as it is now.
  private checkChange() {
    const wc = this.box.tabs.wc
    const who = this.ident(wc.viewKey)
    const h = this.history.get(who)
    const last = this.latest.get(who) ?? {}
    for (const [k, v] of Object.entries(wc.patch)) {
      ok(h?.get(k)?.has(val(v)) === true, () => `(a) ${k}=${val(v)} was never set on ${who}${this.where()}`)
      ok(last[k] === val(v), () => `(c) ${k}=${val(v)} is not the last choice on ${who} (${last[k]})${this.where()}`)
    }
  }

  // (b): what the person changed is still shown on the same view, unless a notice said it was dropped.
  private checkKept(
    before: WorkingCopy,
    after: WorkingCopy,
    afterList: readonly SavedView[] | undefined,
    lost: Lost | null,
  ) {
    if (lost) {
      ok(hasEdits(before.patch), () => `a notice with nothing dropped${this.where()}`)
      return ok(this.gone(before, afterList ?? []), () => `a notice for a view that is there${this.where()}`)
    }
    if (after === before || !hasEdits(before.patch)) return
    ok(
      this.ident(after.viewKey) === this.ident(before.viewKey),
      () => `(b) the change left its view silently${this.where()}`,
    )
    const shownAfter = this.shown(afterList, after)
    ok(shownAfter !== null, () => `(b) the view shown is not in the list${this.where()}`)
    const full = { ...DEFAULT_STATE, ...before.patch }
    for (const k of Object.keys(before.patch) as (typeof KEYS)[number][])
      ok(sameKey(k, shownAfter, full), () => `(b) ${k} dropped silently${this.where()}`)
  }

  // Whether the view of a working copy is really not there to follow in `views`: deleted, or, for a view written by
  // hand, no longer told by its name and settings (changed, or two alike).
  private gone(wc: WorkingCopy, views: readonly SavedView[]) {
    if (wc.viewKey === '') return views.length > 0
    if (wc.snapshot === null || !isHandMark(wc.viewKey)) return !views.some((v) => v.id === wc.viewKey)
    const who = this.ident(wc.viewKey)
    const same = (v: SavedView) => snapshotOf(v).name === wc.snapshot!.name && snapshotOf(v).sig === wc.snapshot!.sig
    const it = views.find((v) => this.ident(v.id) === who)
    return !it || !same(it) || views.filter(same).length !== 1
  }

  private outcome(r: { lost: Lost | null; remember: string | null }) {
    if (r.remember !== null) this.stored = r.remember
  }

  // The page draws: when nothing is on its way, it takes in the answer of the last write and the views read.
  private render() {
    if (this.mutation === null)
      ok(this.box.writing === 0, () => `(d) a write settled and the page still counts it${this.where()}`)
    if (!this.busy()) this.catchUp()
    this.echo()
  }

  private catchUp() {
    const t = this.box.tabs
    const fresh = this.cache !== undefined && this.cache !== t.seen
    // After an answer the cache always has views at least as new as it (read after it, or the answer itself): the tabs
    // never show the list from before the write with the working copy on what the write made.
    if (t.answer) ok(fresh, () => `(b) an answer taken in without the views after it${this.where()}`)
    if (!t.answer && !fresh) return
    const before = t.wc
    let mid = before
    let owner: string | undefined
    let exempt = false
    if (t.answer) {
      const a = t.answer
      const mine = before.viewKey === a.on
      // Through the reducer on its own, to check the answer alone: it never changes what is shown.
      mid = reduceWorkingCopy(before, { kind: 'written', ...a }).wc
      exempt = mine && a.op === 'delete'
      if (mine && (a.op === 'create' || a.op === 'duplicate')) {
        const made = a.got.at(-1)!
        owner = this.ident(made.id)
        const from = this.ident(a.on)
        const h = this.history.get(owner) ?? new Map<string, Set<string>>()
        for (const [k, s] of this.history.get(from) ?? []) h.set(k, new Set([...(h.get(k) ?? []), ...s]))
        this.history.set(owner, h)
        this.latest.set(owner, { ...this.latest.get(from) })
      }
      if (!exempt && mid !== before) {
        const was = this.shown(t.list?.views, before)
        const now = this.shown(a.got, mid)
        ok(
          was !== null && now !== null && sameState(was, now),
          () => `(b) the answer changed what is shown${this.where()}`,
        )
        ok(
          this.ident(mid.viewKey) === (owner ?? this.ident(before.viewKey)),
          () => `(b) the answer moved the change to another view${this.where()}`,
        )
      }
    }
    // The views taken in after an answer were read after it (the read before was called off).
    if (t.answer && fresh)
      ok(
        this.cache!.rev >= this.revOf.get(t.answer)!,
        () => `a list older than the answer taken in after it${this.where()}`,
      )
    const r = catchUp(t, this.cache)
    this.log.push(`catch up${t.answer ? ` ${t.answer.op}` : ''}${fresh ? ` + list rev ${this.cache!.rev}` : ''}`)
    this.box = { ...this.box, tabs: r.tabs }
    this.outcome(r)
    if (!exempt) this.checkKept(mid, r.tabs.wc, r.tabs.list?.views, r.lost)
    // What is shown is a view of the list shown (or the implicit view when there is none), and it is the person's own.
    const shownNow = this.shown(r.tabs.list?.views, r.tabs.wc)
    ok(
      shownNow !== null || (r.tabs.list?.views.length ?? 0) === 0,
      () => `(b) the view shown is not among the tabs${this.where()}`,
    )
    this.checkChange()
  }

  // One event of the person's through the reducer, the way useWorkingCopy applies it.
  private step(e: WorkingEvent) {
    const r = reduceWorkingCopy(this.box.tabs.wc, e)
    ok(r.lost === null, () => `a notice for the person's own doing${this.where()}`)
    this.box = { ...this.box, tabs: { ...this.box.tabs, wc: r.wc } }
    this.outcome(r)
    this.checkChange()
  }

  // The address follows the working copy (PageListView), and the address step of useWorkingCopy sees it: never a change.
  private echo() {
    const wc = this.box.tabs.wc
    const sort = wc.patch.sort ?? null
    const view = sort !== null && wc.viewKey !== '' ? wc.viewKey : null
    const key = JSON.stringify([view, sort])
    if (key === this.box.address) return
    this.box = { ...this.box, address: key }
    const out = reduceWorkingCopy(wc, { kind: 'address', view: view ?? '', sort, base: this.baseOf(wc) })
    ok(out.wc === wc, () => `the address written from the working copy changed it${this.where()}`)
  }
  private baseOf(wc: WorkingCopy) {
    const v = this.box.tabs.list?.views.find((x) => x.id === wc.viewKey)
    return v ? stateOfView(v) : DEFAULT_STATE
  }

  step1() {
    const u = this.ui()
    const m = this.mutation
    const choices: [number, () => void][] = [
      [u.mine ? 30 : 0, () => this.edit()],
      [u.mine && u.modified ? 5 : 0, () => this.revert()],
      [u.saved.length > 0 ? 8 : 0, () => this.pickView()],
      [this.box.tabs.list && !this.busy() ? 12 : 0, () => this.own()],
      [m && !m.result ? 10 : 0, () => this.commit()],
      [m?.result && !m.refetch ? 10 : 0, () => this.answer()],
      [6, () => this.other()],
      [8, () => this.startGet()],
      [this.get && !this.get.served ? 8 : 0, () => this.serve()],
      [this.get?.served ? 8 : 0, () => this.arrive()],
      [this.get ? 2 : 0, () => this.failGet()],
      [m?.refetch && this.get === m.refetch ? 4 : 0, () => this.failGet()],
      [this.retries > 0 && !m ? 5 : 0, () => this.readLater()],
      [this.late ? 3 : 0, () => this.lateWrite()],
      [4, () => this.address()],
      [2, () => this.reopen()],
    ]
    const total = choices.reduce((s, [w]) => s + w, 0)
    let x = this.r() * total
    for (const [w, act] of choices) {
      if ((x -= w) < 0) {
        act()
        break
      }
    }
    this.render()
  }

  // Everything on its way comes to an end, and the views are read once more.
  finish() {
    for (let i = 0; this.mutation && i < 10; i++) {
      const m = this.mutation
      if (!m.result) this.commit()
      else if (!m.refetch) this.answer()
      else {
        if (!this.get?.served) this.serve()
        this.arrive()
      }
      this.render()
    }
    ok(this.mutation === null, () => `the write never settled${this.where()}`)
    this.startGet()
    this.serve()
    this.arrive()
    this.render()
    ok(!this.busy(), () => `(d) still off after the write settled${this.where()}`)
    ok(this.box.tabs.answer === null && this.box.tabs.seen === this.cache, () => `(d) not caught up${this.where()}`)
  }

  private edit() {
    if (this.busy()) return this.guarded('edit')
    const k = this.pick(KEYS)
    const v = k === 'sort' ? this.pick(SORTS) : this.pick(LAYOUTS)
    const patch: Patch = { [k]: v }
    this.log.push(`edit ${JSON.stringify(patch)}`)
    this.note(this.box.tabs.wc.viewKey, k, v)
    this.step({ kind: 'edit', patch, base: this.ui().base })
  }
  private revert() {
    if (this.busy()) return this.guarded('revert')
    this.log.push('revert')
    this.forget(this.box.tabs.wc.viewKey)
    this.step({ kind: 'revert' })
  }
  private pickView() {
    if (this.busy()) return this.guarded('pick')
    const v = this.pick(this.ui().saved)
    this.log.push(`pick ${v.id}`)
    if (v.id !== this.box.tabs.wc.viewKey) this.forget(v.id)
    this.step({ kind: 'pick', id: v.id, views: this.box.tabs.list?.views ?? [] })
  }
  // The tabs and the tools are off: the attempt does nothing.
  private guarded(what: string) {
    this.refused++
    this.log.push(`${what} refused: a write is on its way`)
  }

  // A write of the person, as ViewBar makes it: the page counts it, the read on its way is called off.
  private own() {
    const { saved: views, active, mine, state, modified } = this.ui()
    const ops: ViewOp[] = []
    if (active && mine && modified) ops.push('save')
    if (views.length < MAX) ops.push('create')
    if (active && views.length < MAX) ops.push('duplicate')
    if (active) ops.push('rename', 'delete')
    if (active && views.length > 1) ops.push('reorder')
    if (ops.length === 0) return
    const op = this.pick(ops)
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
    const on = active?.id ?? ''
    this.box = { ...this.box, writing: this.box.writing + 1 }
    this.mutation = {
      op,
      on,
      sent,
      baseRev: this.box.tabs.list!.rev,
      page: this.page,
      result: null,
      refetch: null,
      cacheAtAnswer: 0,
    }
    this.get = null
    this.log.push(`own ${op} on ${on} (rev ${this.box.tabs.list!.rev}) state ${JSON.stringify(state)}`)
  }
  private commit() {
    const m = this.mutation!
    const fate = this.r()
    if (fate < 0.05) {
      m.result = { ok: false, unknown: true }
      this.log.push('put: network error, nothing written')
      return
    }
    if (fate < 0.1) {
      // Out of time before the server got to it: it may still write it.
      m.result = { ok: false, unknown: true }
      this.late = { sent: m.sent, baseRev: m.baseRev }
      this.log.push('put: timed out, not written yet')
      return
    }
    if (m.baseRev !== this.rev) {
      m.result = { ok: false }
      this.log.push('put: 409')
      return
    }
    this.apply(m.sent)
    // Written, but the answer may be lost on the way back (or come too late).
    m.result = fate < 0.2 ? { ok: false, unknown: true } : { ok: true, got: this.serverList() }
    this.log.push(`put: written${m.result.ok ? '' : ', answer lost'} ${JSON.stringify(this.server)}`)
  }
  private apply(sent: Draft[]) {
    this.server = sent.map((d): ServerView => {
      const w = { name: d.name as string, layout: d.layout as ViewLayout, sort: d.sort as string }
      if (d.id !== undefined) return { ...this.server.find((x) => x.id === d.id)!, ...w }
      const uid = this.uid++
      return { uid, id: `r${uid}`, ...w }
    })
    this.written()
  }
  // The server makes a write whose time ran out, if nothing was written since it was sent (else a 409 nobody sees).
  private lateWrite() {
    const l = this.late!
    this.late = null
    if (l.baseRev !== this.rev) return void this.log.push('late put: 409')
    this.apply(l.sent)
    this.log.push(`late put: written ${JSON.stringify(this.server)}`)
  }
  // The answer is there (or a refusal, or an error): the views are read again, a read on its way called off, and the
  // write settles when that read does (or fails, or runs out of time).
  private answer() {
    const m = this.mutation!
    this.log.push(`answer ${m.result!.ok ? m.op : 'error'}`)
    this.get = { served: null }
    m.refetch = this.get
    m.cacheAtAnswer = this.cacheN
  }
  // A read in the background after a read that did not come (useSaveViews).
  private readLater() {
    this.retries--
    this.log.push('read again in the background')
    this.startGet()
  }
  private settle(m: Mutation) {
    this.mutation = null
    this.log.push('settled')
    if (m.page === this.page) {
      const w: Written | null = m.result?.ok ? { op: m.op, on: m.on, sent: m.sent, got: m.result.got.views } : null
      if (w && m.result?.ok) this.revOf.set(w, m.result.got.rev)
      if (w) this.box = { ...this.box, tabs: { ...this.box.tabs, answer: w } }
      this.box = { ...this.box, writing: this.box.writing - 1 }
    }
  }

  private startGet() {
    if (this.get) return
    this.get = { served: null }
    this.log.push('read asked')
  }
  private serve() {
    if (!this.get || this.get.served) return
    this.get.served = this.serverList()
    this.log.push(`read served rev ${this.rev}`)
  }
  private arrive() {
    const g = this.get
    if (!g?.served) return
    this.get = null
    this.cache = g.served
    this.cacheN++
    this.retries = 0
    this.log.push(`read arrived rev ${g.served.rev}`)
    const m = this.mutation
    if (m?.refetch === g) {
      // No answer at all: the write may still be made, so the views are read again later too.
      if (m.result && !m.result.ok && m.result.unknown) this.retries = 5
      this.settle(m)
    }
  }
  private failGet() {
    const g = this.get!
    this.get = null
    this.log.push('read failed or ran out of time')
    const m = this.mutation
    if (m?.refetch === g) {
      // The answer is newer than the cache, unless a read made after it arrived; the views are read again later.
      if (m.result?.ok && this.cacheN === m.cacheAtAnswer) {
        this.cache = { ...m.result.got }
        this.cacheN++
        this.log.push(`the answer taken as the views, rev ${m.result.got.rev}`)
      }
      this.retries = 5
      this.settle(m)
    } else if (this.retries > 0) this.log.push(`${this.retries} background reads left`)
  }

  // Someone else writes the views.
  private other() {
    const s = this.server
    const kinds = ['add', ...(s.length ? ['delete', 'rename', 'state', 'reorder'] : [])]
    const k = this.pick(kinds)
    if (k === 'add' && s.length < MAX) {
      const uid = this.uid++
      s.splice(this.int(s.length + 1), 0, {
        uid,
        id: `r${uid}`,
        name: this.fresh('o'),
        layout: this.pick(LAYOUTS),
        sort: this.pick(SORTS),
      })
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

  // Back, forward, a link within the listing. While a write is on its way it waits, and the page writes the address
  // back from the working copy: it comes to nothing.
  private address() {
    if (this.busy()) return this.guarded('address')
    const wc = this.box.tabs.wc
    const ids = (this.box.tabs.list?.views ?? []).map((v) => v.id)
    const view = this.pick<string | null>([null, wc.viewKey || null, ...ids, 'zz'])
    const sort = this.pick<string | null>([null, ...SORTS])
    const key = JSON.stringify([view, sort])
    if (key === this.box.address) return
    this.log.push(`address ${key}`)
    this.box = { ...this.box, address: key }
    if ((view ?? '') === wc.viewKey) {
      if (sort === null) this.forget(wc.viewKey, 'sort')
      else this.note(wc.viewKey, 'sort', sort)
    }
    this.step({ kind: 'address', view: view ?? '', sort, base: this.baseOf(wc) })
  }

  // The page is left and opened again (a write may still be on its way; its answer goes to the page that is gone).
  private reopen() {
    const [view, sort] = JSON.parse(this.box.address) as [string | null, string | null]
    this.log.push('left and opened again')
    // Leaving calls off a read whose page is gone, unless a write waits for it.
    if (this.get && this.mutation?.refetch !== this.get) this.get = null
    this.box = this.open({ view, sort })
    this.checkChange()
  }
}

describe('the working copy, under random interleavings', () => {
  it(`keeps (a), (b), (c) and (d) over ${SEQUENCES} sequences`, () => {
    let refused = 0
    for (let seed = 1; seed <= SEQUENCES; seed++) {
      const sim = new Sim(seed)
      const r = rng(seed ^ 0x9e3779b9)
      const n = 5 + Math.floor(r() * 40)
      for (let i = 0; i < n; i++) sim.step1()
      sim.finish()
      refused += sim.refused
    }
    // The guard was put to the test: edits, reverts and picks were tried while a write was on its way.
    expect(refused).toBeGreaterThan(1000)
  }, 180_000)
})
