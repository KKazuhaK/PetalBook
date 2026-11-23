import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import type { SavedView, ViewList } from '@/api/types'
import { loadViewId, saveViewId, type ViewState } from '@/lib/savedViews'
import { catchUp, initialWorkingCopy, reduceWorkingCopy, type Patch, type Tabs, type WorkingCopy, type WorkingEvent, type Written } from '@/lib/workingCopy'
import { t } from '@/i18n'

// The views of a folder or a tag as the tabs show them, and the working copy of the one shown, kept together: every
// event goes through `reduceWorkingCopy`, and the list the tabs show changes in the same step as the working copy, so
// the two never disagree. The browser remembers the view shown; the address carries the sort of the view it names.
//
// While a write of the person's views is on its way (`busy`: this page's, or one still running from before) nothing is
// taken in (views read, the address) and nothing can be picked or changed; the page shows it all off. Once it has settled, its answer and the
// views read after it are taken in together (`catchUp`).

interface Box extends Tabs<ViewList> {
  // Writes this page has started and not yet seen settle (the mutation may not say it is on its way yet).
  writing: number
  // The address (view and sort) last handed to the reducer.
  address: string
  remember: { id: string; n: number } | null
  notice: { name: string; link: boolean; n: number } | null
}

export interface Address {
  view: string | null
  sort: string | null
}

const addressKey = (a: Address) => JSON.stringify([a.view, a.sort])

export function useWorkingCopy(on: boolean, idKey: string, data: ViewList | undefined, address: Address, mutating: boolean, baseOf: (views: readonly SavedView[], wc: WorkingCopy) => ViewState) {
  const [stored, setBox] = useState<Box>(() => ({
    list: null,
    wc: initialWorkingCopy(loadViewId(idKey), address),
    seen: undefined,
    answer: null,
    writing: 0,
    address: addressKey(address),
    remember: null,
    notice: null,
  }))
  const note = (box: Box, r: { lost: { name: string; link?: boolean } | null; remember: string | null }): Box => ({
    ...box,
    remember: r.remember === null ? box.remember : { id: r.remember, n: (box.remember?.n ?? 0) + 1 },
    notice: r.lost ? { name: r.lost.name, link: r.lost.link === true, n: (box.notice?.n ?? 0) + 1 } : box.notice,
  })
  const step = (box: Box, e: WorkingEvent): Box => {
    const r = reduceWorkingCopy(box.wc, e)
    return r.wc === box.wc && r.remember === null ? box : note({ ...box, wc: r.wc }, r)
  }

  const busy = mutating || stored.writing > 0
  let box = stored
  // Decided while rendering, so nothing is ever drawn with a working copy that belongs to another list.
  if (on && !busy) {
    const r = catchUp(box, data)
    if (r.tabs !== box) box = note({ ...box, ...r.tabs }, r)
  }
  // The address too waits for the write: the page writes it back from the working copy meanwhile (back and forward
  // within the listing are off like the tabs).
  const key = addressKey(address)
  if (on && !busy && key !== box.address) box = { ...step(box, { kind: 'address', view: address.view ?? '', sort: address.sort, base: baseOf(box.list?.views ?? [], box.wc) }), address: key }
  if (box !== stored) setBox(box)

  useEffect(() => {
    if (stored.remember) saveViewId(idKey, stored.remember.id)
  }, [idKey, stored.remember])
  useEffect(() => {
    if (stored.notice) toast.error(stored.notice.link ? t.views.linkGone : stored.notice.name ? t.views.lost(stored.notice.name) : t.views.lostUnnamed)
  }, [stored.notice])

  // Nothing the person does to the view shown is taken while a write is on its way (the page has it all off too).
  const apply = (e: WorkingEvent) => {
    if (!busy) setBox((b) => (b.writing > 0 ? b : step(b, e)))
  }
  return {
    list: box.list,
    wc: box.wc,
    busy,
    pick: (id: string) => apply({ kind: 'pick', id, views: box.list?.views ?? [] }),
    edit: (patch: Patch, base: ViewState) => apply({ kind: 'edit', patch, base }),
    revert: () => apply({ kind: 'revert' }),
    // A write of the person starts, and settles (with its answer when there is one).
    begin: () => setBox((b) => ({ ...b, writing: b.writing + 1 })),
    end: (w: Written | null, read: boolean) => setBox((b) => ({ ...b, writing: b.writing - 1, ...(w ? { answer: w, unread: !read } : {}) })),
  }
}
