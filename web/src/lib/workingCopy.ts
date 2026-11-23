// The working copy of the saved view shown in a folder or a tag: which view is shown, what it was when it was last
// seen, and what is changed in it without being saved (the sort too). One function decides, for everything that can
// happen to the views (a refetch, a refused write, the person's own write, a pick, the address), what becomes of it:
// it follows the same view (by its id, or, for a view written by hand that is still "@n", by its name and settings),
// or it is dropped, and the person is told whenever something they changed is dropped by anything but their own doing.
// A change is never put on a view other than the one it was made on. Pure, so it is tested without a page.

import type { SavedView } from '../api/types.ts'
import { isHandMark, sameState, snapshotOf, stateOfView, stateSig, viewAtMark, viewSig, type ViewSnapshot, type ViewState } from './savedViews.ts'

export type Patch = Partial<ViewState>

export interface WorkingCopy {
  // The view shown and changed: a real id, "@n" for a view written by hand, '' for the implicit view (none is saved).
  viewKey: string
  // What the view was when it was last seen; null until the views have been read once.
  snapshot: ViewSnapshot | null
  // What is changed in it, sort included. Only what differs from the view is kept.
  patch: Patch
  // The change is only what the address brought (a link, a reload): nothing was changed in this visit yet.
  fromAddress?: true
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
  // How the view shown looked when the write was sent (what create, duplicate and save write), and what it was without
  // the person's changes then. What is shown when the answer comes and differs from `working` was changed while the
  // write was on its way: it is kept as a change of the view shown after the answer. No base: the same as `working`.
  working?: ViewState
  base?: ViewState
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
  // Something the person changed was dropped by someone else's doing: the view it was made on, by name ('' unknown);
  // link: it was only the sort an address brought, for a view that is not there.
  lost: { name: string; link?: boolean } | null
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

// What is shown and is not what was written: compared key by key over the whole state, so a value set back to what the
// view said before the write is kept too (it differs from what was written).
function unwritten(shown: ViewState, written: ViewState): Patch {
  const out: Record<string, unknown> = {}
  const keys = new Set([...Object.keys(shown), ...Object.keys(written)] as (keyof ViewState)[])
  for (const k of keys) if (JSON.stringify(shown[k]) !== JSON.stringify(written[k])) out[k] = shown[k]
  return out as Patch
}

const samePatch = (a: Patch, b: Patch) => JSON.stringify(Object.entries(a).sort()) === JSON.stringify(Object.entries(b).sort())
const sameSnap = (a: ViewSnapshot | null, b: ViewSnapshot | null) => a === b || (a !== null && b !== null && a.id === b.id && a.name === b.name && a.sig === b.sig)
const sameWc = (a: WorkingCopy, b: WorkingCopy) => a.viewKey === b.viewKey && sameSnap(a.snapshot, b.snapshot) && samePatch(a.patch, b.patch) && a.fromAddress === b.fromAddress
const matches = (v: SavedView, s: ViewSnapshot) => {
  const now = snapshotOf(v)
  return now.name === s.name && now.sig === s.sig
}

// The working copy a listing starts with: the view remembered in the browser, unless the address names a view and a
// sort for it. A sort without a view belongs to the implicit view only.
export function initialWorkingCopy(remembered: string, address: { view: string | null; sort: string | null }): WorkingCopy {
  if (address.sort !== null && (address.view !== null || remembered === '')) return { viewKey: address.view ?? '', snapshot: null, patch: { sort: address.sort }, fromAddress: true }
  return { viewKey: remembered, snapshot: null, patch: {} }
}

const showing = (to: SavedView | null, patch: Patch): WorkingCopy => (to ? { viewKey: to.id, snapshot: snapshotOf(to), patch: prune(patch, stateOfView(to)) } : { viewKey: '', snapshot: IMPLICIT, patch: {} })
const kept = (wc: WorkingCopy, next: WorkingCopy, remember: string | null = null): Outcome => ({ wc: sameWc(wc, next) ? wc : next, adopt: true, lost: null, remember })
const wait = (wc: WorkingCopy): Outcome => ({ wc, adopt: false, lost: null, remember: null })
// The same view with a change the person made in this visit.
const changed = (wc: WorkingCopy, patch: Patch): WorkingCopy => ({ viewKey: wc.viewKey, snapshot: wc.snapshot, patch })

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
    // A write of the person is on its way and the view says something else now: that may be the write itself, and only
    // its answer says what of what is shown was written. Until then the view stays as it is shown.
    if (pending && found.to && wc.snapshot !== null && viewSig(found.to) !== wc.snapshot.sig) return wait(wc)
    const next = found.to ? showing(found.to, wc.patch) : { viewKey: '', snapshot: IMPLICIT, patch: wc.patch }
    return kept(wc, next, found.to && found.to.id !== wc.viewKey ? found.to.id : null)
  }
  // The person's own write may be what changed the view; its answer says where the view went.
  if (pending && wc.snapshot !== null) return wait(wc)
  return {
    wc: showing(found.to, {}),
    adopt: true,
    // Whatever is dropped is said: a change of this visit as such, the sort of an address (a link, a reload) whose view
    // is not there as such.
    lost: !hasEdits(wc.patch) ? null : wc.snapshot === null && wc.fromAddress ? { name: '', link: true } : { name: wc.snapshot?.name ?? '' },
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
  // The view the write was about, as the answer names it.
  const onNow = map.get(e.on)
  // The write is about the view shown (a list read while it was on its way may have named it already).
  const mine = wc.viewKey === e.on || (onNow !== undefined && wc.viewKey === onNow.id)
  // What was shown when the write was made went into the new view, the copy or the saved view; what is shown now and
  // differs from that was changed while it was on its way, and stays as a change of the view shown after: no edit is
  // lost. Nothing else is ever carried: what the person sees of the view is the same before and after the answer. When
  // the view shown is no longer the one the write started from (a pick again, after a list with the write in it), the
  // change is already one of the view as it is now, and stays as it is.
  const left = (): Patch => {
    if (!e.working) return {}
    if (e.base && wc.viewKey !== '' && wc.snapshot !== null && wc.snapshot.sig !== stateSig(e.base)) return wc.patch
    return unwritten({ ...(e.base ?? e.working), ...wc.patch }, e.working)
  }
  const show = (to: SavedView | null, patch: Patch): Outcome => ({ wc: showing(to, patch), adopt: true, lost: null, remember: to ? to.id : '' })
  if (mine && (e.op === 'create' || e.op === 'duplicate')) return show(e.got.at(-1) ?? null, left())
  if (mine && e.op === 'save') return onNow ? show(onNow, left()) : onList(changed(wc, left()), e.got, false)
  // The person deleted the view shown: what was changed in it goes with it, and is never put on the view shown next.
  if (mine && e.op === 'delete') return { ...show(e.got[0] ?? null, {}), remember: '' }
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
      return kept(wc, changed(wc, prune({ ...wc.patch, ...e.patch }, e.base)))
    case 'revert':
      return kept(wc, changed(wc, {}))
    case 'address': {
      if (e.view !== wc.viewKey) return kept(wc, wc)
      const rest: Patch = { ...wc.patch }
      delete rest.sort
      const patch = e.sort === null ? rest : prune({ ...rest, sort: e.sort }, e.base)
      // The address saying what the working copy says (it is written from it) changes nothing.
      return samePatch(patch, wc.patch) ? kept(wc, wc) : kept(wc, changed(wc, patch))
    }
  }
}
