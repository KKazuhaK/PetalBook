// The working copy of the saved view shown in a folder or a tag: which view is shown, what it was when it was last
// seen, and what is changed in it without being saved (the sort too). One function decides, for everything that can
// happen to the views (a refetch, a refused write, the person's own write, a pick, the address), what becomes of it:
// it follows the same view (by its id, or, for a view written by hand that is still "@n", by its name and settings),
// or it is dropped, and the person is told whenever something they changed is dropped by anything but their own doing.
// A change is never put on a view other than the one it was made on. Pure, so it is tested without a page.

import type { SavedView } from '../api/types.ts'
import { isHandMark, sameState, snapshotOf, stateOfView, viewAtMark, type ViewSnapshot, type ViewState } from './savedViews.ts'

export type Patch = Partial<ViewState>

export interface WorkingCopy {
  // The view shown and changed: a real id, "@n" for a view written by hand, '' for the implicit view (none is saved).
  viewKey: string
  // What the view was when it was last seen; null until the views have been read once.
  snapshot: ViewSnapshot | null
  // What is changed in it, sort included. Only what differs from the view is kept.
  patch: Patch
}

// The snapshot of the implicit view, once the views were read and there was none.
export const IMPLICIT: ViewSnapshot = { id: '', name: '', sig: '' }

export type ViewOp = 'save' | 'create' | 'duplicate' | 'rename' | 'reorder' | 'delete'

// The answer to a write of this person: what was sent (in order) and what the server answered (in the same order).
export interface Written {
  op: ViewOp
  // The view the write was about: the one saved, renamed, moved, deleted or copied; for a new view, the one shown when
  // it was made.
  on: string
  sent: readonly { id?: string; name?: unknown }[]
  got: readonly SavedView[]
}

export type WorkingEvent =
  // The views as they are now: read again, brought by a refused write, or the first reading. `pending`: a write of this
  // person is on its way, which may explain a change it cannot place yet.
  | { kind: 'list'; views: readonly SavedView[]; pending: boolean }
  | ({ kind: 'written' } & Written)
  | { kind: 'pick'; id: string; views: readonly SavedView[] }
  | { kind: 'edit'; patch: Patch; base: ViewState }
  | { kind: 'revert' }
  // The address changed (back, forward, a link): its sort is the sort of the view it names, of no other.
  | { kind: 'address'; view: string; sort: string | null; base: ViewState }

export interface Outcome {
  wc: WorkingCopy
  // false: the list of the event is not shown yet (a write of the person is on its way and will say where the view went).
  adopt: boolean
  // Something the person changed was dropped by someone else's doing: the view it was made on, by name ('' unknown).
  lost: { name: string } | null
  // What the browser remembers as the view of this listing from now on; null: as it was.
  remember: string | null
}

export const hasEdits = (p: Patch): boolean => Object.keys(p).length > 0

// The patch without what the view already says.
export function prune(patch: Patch, base: ViewState): Patch {
  const out: Record<string, unknown> = {}
  for (const k of Object.keys(patch) as (keyof ViewState)[]) {
    const v = patch[k]
    if (v !== undefined && !sameState({ ...base, [k]: v } as ViewState, base)) out[k] = v
  }
  return out as Patch
}

const samePatch = (a: Patch, b: Patch) => JSON.stringify(Object.entries(a).sort()) === JSON.stringify(Object.entries(b).sort())
const sameSnap = (a: ViewSnapshot | null, b: ViewSnapshot | null) => a === b || (a !== null && b !== null && a.id === b.id && a.name === b.name && a.sig === b.sig)
const sameWc = (a: WorkingCopy, b: WorkingCopy) => a.viewKey === b.viewKey && sameSnap(a.snapshot, b.snapshot) && samePatch(a.patch, b.patch)
const matches = (v: SavedView, s: ViewSnapshot) => {
  const now = snapshotOf(v)
  return now.name === s.name && now.sig === s.sig
}

// The working copy a listing starts with: the view remembered in the browser, unless the address names a view and a
// sort for it. A sort without a view belongs to the implicit view only.
export function initialWorkingCopy(remembered: string, address: { view: string | null; sort: string | null }): WorkingCopy {
  if (address.sort !== null && (address.view !== null || remembered === '')) return { viewKey: address.view ?? '', snapshot: null, patch: { sort: address.sort } }
  return { viewKey: remembered, snapshot: null, patch: {} }
}

const showing = (to: SavedView | null, patch: Patch): WorkingCopy => (to ? { viewKey: to.id, snapshot: snapshotOf(to), patch: prune(patch, stateOfView(to)) } : { viewKey: '', snapshot: IMPLICIT, patch: {} })
const kept = (wc: WorkingCopy, next: WorkingCopy, remember: string | null = null): Outcome => ({ wc: sameWc(wc, next) ? wc : next, adopt: true, lost: null, remember })

// Where the view of the working copy is in `views`. keep: it is there (the same view); otherwise `to` is what is shown
// instead, and `mark` says it is the view at the place of a remembered "@n" (the best guess at the first reading).
function locate(wc: WorkingCopy, views: readonly SavedView[]): { to: SavedView | null; keep: boolean; mark?: boolean } {
  if (views.length === 0) return { to: null, keep: wc.viewKey === '' }
  const here = views.find((v) => v.id === wc.viewKey)
  if (wc.snapshot === null) {
    if (here) return { to: here, keep: true }
    const mark = viewAtMark(views, wc.viewKey)
    return mark ? { to: mark, keep: false, mark: true } : { to: views[0], keep: false }
  }
  if (wc.viewKey === '') return { to: views[0], keep: false }
  // A real id stays the view's whatever is done to it; only its removal ends it.
  if (!isHandMark(wc.viewKey)) return here ? { to: here, keep: true } : { to: views[0], keep: false }
  // "@n" is a place: the view is the one with the name and settings it had, if there is exactly one.
  const snap = wc.snapshot
  if (here && matches(here, snap)) return { to: here, keep: true }
  const alike = views.filter((v) => matches(v, snap))
  return alike.length === 1 ? { to: alike[0], keep: true } : { to: views[0], keep: false }
}

function onList(wc: WorkingCopy, views: readonly SavedView[], pending: boolean): Outcome {
  const found = locate(wc, views)
  if (found.keep) {
    const next = found.to ? showing(found.to, wc.patch) : { viewKey: '', snapshot: IMPLICIT, patch: wc.patch }
    return kept(wc, next, found.to && found.to.id !== wc.viewKey ? found.to.id : null)
  }
  // The person's own write may be what changed the view; its answer says where the view went.
  if (pending && wc.snapshot !== null) return { wc, adopt: false, lost: null, remember: null }
  return {
    wc: showing(found.to, {}),
    adopt: true,
    lost: hasEdits(wc.patch) ? { name: wc.snapshot?.name ?? '' } : null,
    remember: found.mark && found.to ? found.to.id : '',
  }
}

function onWritten(wc: WorkingCopy, e: Written): Outcome {
  // The answer lists the views in the order sent: each view sent is the one at its place, if the name is the same.
  const map = new Map<string, SavedView>()
  e.sent.forEach((d, i) => {
    const g = e.got[i]
    if (d.id !== undefined && g && g.name === d.name) map.set(d.id, g)
  })
  const mine = wc.viewKey === e.on
  const show = (to: SavedView | null): Outcome => ({ wc: showing(to, {}), adopt: true, lost: null, remember: to ? to.id : '' })
  // What was shown went into the new view, the copy or the saved view: nothing is left over, and nothing is lost.
  if (mine && (e.op === 'create' || e.op === 'duplicate')) return show(e.got.at(-1) ?? null)
  if (mine && e.op === 'save') {
    const to = map.get(e.on)
    return to ? show(to) : onList({ ...wc, patch: {} }, e.got, false)
  }
  if (mine && e.op === 'delete') return { ...show(e.got[0] ?? null), remember: '' }
  // A rename, a move, or a write about another view: the view shown goes on under the id the answer gave it.
  const to = map.get(wc.viewKey)
  if (to) return kept(wc, showing(to, wc.patch), to.id !== wc.viewKey ? to.id : null)
  return onList(wc, e.got, false)
}

export function reduceWorkingCopy(wc: WorkingCopy, e: WorkingEvent): Outcome {
  switch (e.kind) {
    case 'list':
      return onList(wc, e.views, e.pending)
    case 'written':
      return onWritten(wc, e)
    case 'pick': {
      if (e.id === wc.viewKey) return kept(wc, wc)
      const to = e.views.find((v) => v.id === e.id)
      return { wc: { viewKey: e.id, snapshot: to ? snapshotOf(to) : null, patch: {} }, adopt: true, lost: null, remember: e.id }
    }
    case 'edit':
      return kept(wc, { ...wc, patch: prune({ ...wc.patch, ...e.patch }, e.base) })
    case 'revert':
      return kept(wc, { ...wc, patch: {} })
    case 'address': {
      if (e.view !== wc.viewKey) return kept(wc, wc)
      const rest: Patch = { ...wc.patch }
      delete rest.sort
      return kept(wc, { ...wc, patch: e.sort === null ? rest : prune({ ...rest, sort: e.sort }, e.base) })
    }
  }
}
