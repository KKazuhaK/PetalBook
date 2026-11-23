import { useIsMutating } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import type { SavedView, ViewList } from '@/api/types'
import { loadViewId, saveViewId, type ViewState } from '@/lib/savedViews'
import { initialWorkingCopy, reduceWorkingCopy, type Patch, type WorkingCopy, type WorkingEvent, type Written } from '@/lib/workingCopy'
import { t } from '@/i18n'

// The views of a folder or a tag as the tabs show them, and the working copy of the one shown, kept together: every
// event goes through `reduceWorkingCopy`, and the list the tabs show changes in the same step as the working copy, so
// the two never disagree. The browser remembers the view shown; the address carries the sort of the view it names.

interface Box {
  // The views the tabs show (null until they are read).
  list: ViewList | null
  wc: WorkingCopy
  // The cache entry and the "a write is on its way" last handed to the reducer.
  seen: ViewList | undefined
  pending: boolean
  // The address (view and sort) last handed to the reducer.
  address: string
  remember: { id: string; n: number } | null
  notice: { name: string; n: number } | null
}

export interface Address {
  view: string | null
  sort: string | null
}

const addressKey = (a: Address) => JSON.stringify([a.view, a.sort])

export function useWorkingCopy(on: boolean, idKey: string, data: ViewList | undefined, address: Address, baseOf: (views: readonly SavedView[], wc: WorkingCopy) => ViewState) {
  const pending = useIsMutating({ mutationKey: ['saveViews'] }) > 0
  const [stored, setBox] = useState<Box>(() => ({
    list: null,
    wc: initialWorkingCopy(loadViewId(idKey), address),
    seen: undefined,
    pending: false,
    address: addressKey(address),
    remember: null,
    notice: null,
  }))
  const step = (box: Box, e: WorkingEvent, list?: ViewList): Box => {
    const r = reduceWorkingCopy(box.wc, e)
    return {
      ...box,
      wc: r.wc,
      list: r.adopt && list ? list : box.list,
      remember: r.remember === null ? box.remember : { id: r.remember, n: (box.remember?.n ?? 0) + 1 },
      notice: r.lost ? { name: r.lost.name, n: (box.notice?.n ?? 0) + 1 } : box.notice,
    }
  }

  let box = stored
  // The views read (again), or a write of the person settled: decided while rendering, so nothing is ever drawn with a
  // working copy that belongs to another list.
  if (on && data && (data !== box.seen || pending !== box.pending)) box = { ...step(box, { kind: 'list', views: data.views, pending }, data), seen: data, pending }
  const key = addressKey(address)
  if (on && key !== box.address) box = { ...step(box, { kind: 'address', view: address.view ?? '', sort: address.sort, base: baseOf(box.list?.views ?? [], box.wc) }), address: key }
  if (box !== stored) setBox(box)

  useEffect(() => {
    if (stored.remember) saveViewId(idKey, stored.remember.id)
  }, [idKey, stored.remember])
  useEffect(() => {
    if (stored.notice) toast.error(stored.notice.name ? t.views.lost(stored.notice.name) : t.views.lostUnnamed)
  }, [stored.notice])

  const apply = (e: WorkingEvent, list?: ViewList) => setBox((b) => step(b, e, list))
  return {
    list: box.list,
    wc: box.wc,
    pick: (id: string) => apply({ kind: 'pick', id, views: box.list?.views ?? [] }),
    edit: (patch: Patch, base: ViewState) => apply({ kind: 'edit', patch, base }),
    revert: () => apply({ kind: 'revert' }),
    // The answer of a write of the person, with the views it brings: called before the cache has them.
    written: (w: Written, list: ViewList) => apply({ kind: 'written', ...w }, list),
  }
}
