// The working copy of the saved view shown in a folder or a tag: which view is shown, what it was when it was last
// seen, and what is changed in it without being saved (the sort too). One function decides, for everything that can
// happen to the views (a list read, the answer of the person's own write, a pick, an edit, the address), what becomes
// of it: it follows the same view (by its id, or, for a view written by hand that is still "@n", by its name and
// settings), or it is dropped, and the person is told whenever something they changed is dropped by anything but their
// own doing. A change is never put on a view other than the one it was made on. Pure, so it is tested without a page.
//
// The person's own writes of the views are exclusive: while one is on its way nothing can be changed, picked or
// written (the page turns the tabs and the tools off), and no list is taken in. When it has settled, its answer only
// says where the view just written went; the views read after it are then taken in by the usual rules (see `catchUp`).

import type { SavedView } from '../api/types.ts'
import {
  isHandMark,
  sameState,
  snapshotOf,
  stateOfView,
  viewAtMark,
  type ViewSnapshot,
  type ViewState,
} from './savedViews.ts'

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
}

export type WorkingEvent =
  // The views as they are now: read again (after a write of the person too), or the first reading.
  | { kind: 'list'; views: readonly SavedView[] }
  | ({ kind: 'written' } & Written)
  | { kind: 'pick'; id: string; views: readonly SavedView[] }
  | { kind: 'edit'; patch: Patch; base: ViewState }
  | { kind: 'revert' }
  // The address changed (back, forward, a link): its sort is the sort of the view it names, of no other.
  | { kind: 'address'; view: string; sort: string | null; base: ViewState }

// Something the person changed was dropped by someone else's doing: the view it was made on, by name ('' unknown);
// link: it was only the sort an address brought, for a view that is not there.
export type Lost = { name: string; link?: boolean }

export interface Outcome {
  wc: WorkingCopy
  lost: Lost | null
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

const samePatch = (a: Patch, b: Patch) =>
  JSON.stringify(Object.entries(a).sort()) === JSON.stringify(Object.entries(b).sort())
const sameSnap = (a: ViewSnapshot | null, b: ViewSnapshot | null) =>
  a === b || (a !== null && b !== null && a.id === b.id && a.name === b.name && a.sig === b.sig)
const sameWc = (a: WorkingCopy, b: WorkingCopy) =>
  a.viewKey === b.viewKey &&
  sameSnap(a.snapshot, b.snapshot) &&
  samePatch(a.patch, b.patch) &&
  a.fromAddress === b.fromAddress
const matches = (v: SavedView, s: ViewSnapshot) => {
  const now = snapshotOf(v)
  return now.name === s.name && now.sig === s.sig
}

// The working copy a listing starts with: the view remembered in the browser, unless the address names a view and a
// sort for it. A sort without a view belongs to the implicit view only.
export function initialWorkingCopy(
  remembered: string,
  address: { view: string | null; sort: string | null },
): WorkingCopy {
  if (address.sort !== null && (address.view !== null || remembered === ''))
    return { viewKey: address.view ?? '', snapshot: null, patch: { sort: address.sort }, fromAddress: true }
  return { viewKey: remembered, snapshot: null, patch: {} }
}

const showing = (to: SavedView | null, patch: Patch): WorkingCopy =>
  to
    ? { viewKey: to.id, snapshot: snapshotOf(to), patch: prune(patch, stateOfView(to)) }
    : { viewKey: '', snapshot: IMPLICIT, patch: {} }
const kept = (wc: WorkingCopy, next: WorkingCopy, remember: string | null = null): Outcome => ({
  wc: sameWc(wc, next) ? wc : next,
  lost: null,
  remember,
})
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

function onList(wc: WorkingCopy, views: readonly SavedView[]): Outcome {
  const found = locate(wc, views)
  if (found.keep) {
    const next = found.to ? showing(found.to, wc.patch) : { viewKey: '', snapshot: IMPLICIT, patch: wc.patch }
    return kept(wc, next, found.to && found.to.id !== wc.viewKey ? found.to.id : null)
  }
  return {
    wc: showing(found.to, {}),
    // Whatever is dropped is said: a change of this visit as such, the sort of an address (a link, a reload) whose view
    // is not there as such.
    lost: !hasEdits(wc.patch)
      ? null
      : wc.snapshot === null && wc.fromAddress
        ? { name: '', link: true }
        : { name: wc.snapshot?.name ?? '' },
    remember: found.mark && found.to ? found.to.id : '',
  }
}

// The answer of the person's own write. Nothing could change while it was on its way, so what is shown is what was
// sent. The answer only says where the view just written is now; the views read after it decide the rest.
function onWritten(wc: WorkingCopy, e: Written): Outcome {
  const mine = wc.viewKey === e.on
  // A new view, or a copy of the view shown: what was shown went into it, and it is shown.
  if (mine && (e.op === 'create' || e.op === 'duplicate')) {
    const made = e.got.at(-1)
    return made ? { wc: showing(made, wc.patch), lost: null, remember: made.id } : kept(wc, wc)
  }
  // The person deleted the view shown: what was changed in it goes with it; the views read next say what is shown.
  if (mine && e.op === 'delete') return { wc: { viewKey: '', snapshot: IMPLICIT, patch: {} }, lost: null, remember: '' }
  // A save, a rename, a move, or a write about another view: the view shown goes on under the id the answer gave it
  // (the answer lists the views in the order sent; a view is the one at its place, if the name is the same).
  const i = e.sent.findIndex((d) => d.id === wc.viewKey)
  const to = i < 0 ? undefined : e.got[i]
  if (!to || to.name !== e.sent[i].name) return kept(wc, wc)
  return kept(wc, showing(to, wc.patch), to.id !== wc.viewKey ? to.id : null)
}

export function reduceWorkingCopy(wc: WorkingCopy, e: WorkingEvent): Outcome {
  switch (e.kind) {
    case 'list':
      return onList(wc, e.views)
    case 'written':
      return onWritten(wc, e)
    case 'pick': {
      if (e.id === wc.viewKey) return kept(wc, wc)
      const to = e.views.find((v) => v.id === e.id)
      return { wc: { viewKey: e.id, snapshot: to ? snapshotOf(to) : null, patch: {} }, lost: null, remember: e.id }
    }
    case 'edit':
      return kept(wc, changed(wc, prune({ ...wc.patch, ...e.patch }, e.base)))
    case 'revert':
      return kept(wc, changed(wc, {}))
    case 'address': {
      // Another view's address, or the address saying what the working copy says (it is written from it): nothing.
      if (e.view !== wc.viewKey || e.sort === (wc.patch.sort ?? null)) return kept(wc, wc)
      const rest: Patch = { ...wc.patch }
      delete rest.sort
      // Before the views are read there is nothing to compare the sort with: it is kept as the address gives it, and
      // the first list decides (with the word about a link whose view is gone).
      const unread = wc.snapshot === null
      const patch =
        e.sort === null ? rest : unread ? { ...rest, sort: e.sort } : prune({ ...rest, sort: e.sort }, e.base)
      if (samePatch(patch, wc.patch)) return kept(wc, wc)
      return kept(
        wc,
        unread && (wc.fromAddress || !hasEdits(wc.patch))
          ? { ...changed(wc, patch), fromAddress: true }
          : changed(wc, patch),
      )
    }
  }
}

// What the tabs show and the working copy, as a page keeps them. `list` is what the tabs show; `seen`, the views last
// taken in; `answer`, the answer of the person's own write, kept until the write has settled. The views there when it
// has settled are never older than the answer: read after it, or, when that read did not come, the answer itself.
export interface Tabs<L extends { views: readonly SavedView[] }> {
  list: L | null
  wc: WorkingCopy
  seen: L | undefined
  answer: Written | null
}

// Takes in what came while nothing of the person's is on its way (never call it while a write is): first where the
// answer of the last write put the view, then the views as read now (after that write, when there was one), by the
// usual rules. The answer itself is never shown as the list.
export function catchUp<L extends { views: readonly SavedView[] }>(
  tabs: Tabs<L>,
  data: L | undefined,
): { tabs: Tabs<L>; lost: Lost | null; remember: string | null } {
  let { wc } = tabs
  let remember: string | null = null
  let lost: Lost | null = null
  if (tabs.answer) ({ wc, remember } = reduceWorkingCopy(wc, { kind: 'written', ...tabs.answer }))
  const fresh = data !== undefined && data !== tabs.seen
  if (fresh) {
    const r = reduceWorkingCopy(wc, { kind: 'list', views: data.views })
    wc = r.wc
    lost = r.lost
    remember = r.remember ?? remember
  }
  if (!tabs.answer && !fresh) return { tabs, lost, remember }
  return { tabs: { list: fresh ? data : tabs.list, wc, seen: fresh ? data : tabs.seen, answer: null }, lost, remember }
}
