// The history of a page, opened from the editor: a list of its versions, one
// compared with another on the rendered page (or as plain Markdown lines), and
// a way back to one of them.
//
// While this is open the editor is not shown (it stays mounted underneath,
// hidden and inert) so there is never a question which of the two is being
// looked at, and nothing is sent to the server because of it: the page's own
// save state is only read.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ArrowLeft, ChevronDown, ChevronUp, History, ListTree, RotateCcw } from 'lucide-react'
import { useHistory, useVersion } from '@/api/hooks'
import type { HistoryItem } from '@/api/types'
import type { SaveState } from '@/editor/saveController'
import { diffLines, diffVersions, type DiffResult, type VersionContent } from '@/diff'
import { AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { NativeSelect } from '@/components/ui/native-select'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { ErrorState, Spinner } from '@/components/common'
import { useIsMobile } from '@/hooks/use-mobile'
import { absoluteTime, clockTime, shortDate } from '@/lib/time'
import { t } from '@/i18n'
import { DiffView, changeStops } from './DiffView'
import { HeaderChanges } from './HeaderChanges'
import { RawDiff } from './RawDiff'
import { VersionList, describeVersion } from './VersionList'

type Mode = 'previous' | 'current' | 'pick'
const CURRENT = 'current'

export interface HistoryModeProps {
  space: string
  pageId: string
  // The folder of the page, which relative image addresses are resolved against.
  folder?: string
  // The editor's content as it is now (unsaved text included), read when the panel opens.
  getCurrent: () => VersionContent
  saveState: SaveState
  onClose: () => void
  // Puts a version in the editor as an ordinary edit. `saveFirst`: write what is
  // there as a version before replacing it. Resolves to whether it was done.
  onRestore: (version: VersionContent, saveFirst: boolean, when: string) => Promise<boolean>
  // The person may look at versions but not change the page: no way to go back to one.
  readOnly?: boolean
}

// undefined: still loading; null: nothing there (before the first version, or a deletion).
type Content = VersionContent | null | undefined

const when = (iso: string) => absoluteTime(iso).replace(/:\d{2}$/, '')
// Short enough for a phone's title row.
const whenShort = (iso: string) => {
  const d = new Date(iso)
  return `${shortDate(d)} ${clockTime(d)}`
}

function useContent(space: string, pageId: string, item: HistoryItem | null) {
  const q = useVersion(space, pageId, item && item.change !== 'delete' ? item.commit : null)
  const content = useMemo<Content>(() => {
    if (!item || item.change === 'delete') return null
    return q.data ? { meta: q.data.meta, body: q.data.body } : undefined
  }, [item, q.data])
  return { content, error: q.isError ? q.error : null, retry: () => void q.refetch() }
}

export default function HistoryMode({ space, pageId, folder = '', getCurrent, saveState, onClose, onRestore, readOnly = false }: HistoryModeProps) {
  const isMobile = useIsMobile()
  const history = useHistory(space, pageId, true)
  const items = useMemo(() => history.data?.pages.flatMap((p) => p.items) ?? [], [history.data])
  const [current] = useState<VersionContent>(getCurrent)

  const [picked, setPicked] = useState<string | null>(null)
  const [mode, setMode] = useState<Mode>('previous')
  const [pick, setPick] = useState<{ from: string; to: string }>({ from: '', to: CURRENT })
  const [view, setView] = useState<'rendered' | 'raw'>('rendered')
  const [listOpen, setListOpen] = useState(false)
  const [restoreOpen, setRestoreOpen] = useState(false)
  const [restoring, setRestoring] = useState(false)

  // The newest version is looked at first: the one that was newest when the list
  // arrived. A refetch that brings a newer one does not change what is being
  // looked at.
  const [initial, setInitial] = useState<string | null>(null)
  if (initial === null && items[0]) setInitial(items[0].commit)
  const selected = picked ?? initial ?? items[0]?.commit ?? null
  const selIndex = items.findIndex((i) => i.commit === selected)
  const selItem: HistoryItem | null = items[selIndex] ?? null
  const previous: HistoryItem | undefined = items[selIndex + 1]

  // The version before the selected one is the next in the list; if the list
  // has not got that far yet it is fetched.
  const waitingForOlder = mode === 'previous' && selIndex >= 0 && !previous && history.hasNextPage
  const { fetchNextPage, isFetchingNextPage } = history
  // Asked once per version looked at: a fetch that keeps failing is not repeated
  // for ever (the list shows the error and its retry).
  const askedFor = useRef<string | null>(null)
  useEffect(() => {
    if (!waitingForOlder || isFetchingNextPage || askedFor.current === selected) return
    askedFor.current = selected
    void fetchNextPage()
  }, [waitingForOlder, isFetchingNextPage, fetchNextPage, selected])

  // The two sides of the comparison.
  const byCommit = (c: string) => items.find((i) => i.commit === c) ?? null
  const [baseItem, targetItem, targetIsCurrent] = ((): [HistoryItem | null, HistoryItem | null, boolean] => {
    if (mode === 'previous') return [previous ?? null, selItem, false]
    if (mode === 'current') return [selItem, null, true]
    const from = byCommit(pick.from || previous?.commit || '')
    const to = pick.to === CURRENT ? null : byCommit(pick.to)
    // The older one goes on the left, whichever way round they were chosen.
    if (from && to && items.indexOf(from) < items.indexOf(to)) return [to, from, false]
    return [from, to, pick.to === CURRENT]
  })()
  const base = useContent(space, pageId, baseItem)
  const target = useContent(space, pageId, targetItem)
  const baseContent: Content = waitingForOlder ? undefined : base.content
  const targetContent: Content = targetIsCurrent ? current : target.content
  const loadError = base.error ?? target.error

  const result = useMemo<DiffResult | null>(() => (baseContent === undefined || targetContent === undefined ? null : diffVersions(baseContent, targetContent)), [baseContent, targetContent])
  const lines = useMemo(() => (view === 'raw' && baseContent !== undefined && targetContent !== undefined ? diffLines(baseContent?.body ?? '', targetContent?.body ?? '') : null), [view, baseContent, targetContent])

  // --- jumping between changes ---
  const [stops, setStops] = useState<HTMLElement[][]>([])
  const [at, setAt] = useState(-1)
  const onRendered = useCallback((root: HTMLElement | null) => {
    setStops(root ? changeStops(root) : [])
    setAt(-1)
  }, [])
  const goTo = (delta: 1 | -1) => {
    if (stops.length === 0) return
    const next = at < 0 ? (delta > 0 ? 0 : stops.length - 1) : (at + delta + stops.length) % stops.length
    for (const group of stops) for (const el of group) el.classList.remove('pb-diff-current')
    for (const el of stops[next]) el.classList.add('pb-diff-current')
    stops[next][0].scrollIntoView({ block: 'center', behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
    setAt(next)
  }

  // Escape leaves history (a dialog or the list sheet handles its own first).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !e.defaultPrevented && !restoreOpen && !listOpen) onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose, restoreOpen, listOpen])

  // --- picking ---
  const select = (commit: string) => {
    setPicked(commit)
    setListOpen(false)
  }
  const chooseMode = (m: Mode) => {
    if (m === 'pick') setPick({ from: previous?.commit ?? '', to: selected ?? CURRENT })
    setMode(m)
  }

  // --- restoring ---
  const subject = useContent(space, pageId, selItem)
  const subjectResult = useMemo(() => (restoreOpen && subject.content ? diffVersions(subject.content, current) : null), [restoreOpen, subject.content, current])
  const conflict = saveState === 'conflict'
  const unsaved = !conflict && saveState !== 'saved'
  const canRestore = !!selItem && selItem.change !== 'delete' && !!subject.content

  const restore = async (saveFirst: boolean) => {
    if (!subject.content || !selItem) return
    setRestoring(true)
    const done = await onRestore(subject.content, saveFirst, when(selItem.time)).catch(() => false)
    setRestoring(false)
    if (done) setRestoreOpen(false)
  }

  // --- what is shown ---
  const counts = result?.stats
  const status = (() => {
    if (history.isPending) return 'loading'
    if (history.isError) return 'error'
    if (items.length === 0) return 'empty'
    return 'ok'
  })()

  const versionList = (showTitle: boolean) => (
    <VersionList
      showTitle={showTitle}
      items={items}
      selected={selected}
      onSelect={select}
      hasMore={!!history.hasNextPage}
      loadingMore={history.isFetchingNextPage}
      onLoadMore={() => void history.fetchNextPage()}
      onRefresh={() => void history.refetch()}
      refreshing={history.isRefetching && !history.isFetchingNextPage}
    />
  )

  const pickOptions = (withCurrent: boolean) => (
    <>
      {withCurrent && <option value={CURRENT}>{saveState === 'saved' ? t.history.currentContent : t.history.currentContentUnsaved}</option>}
      {items.map((i) => (
        <option key={i.commit} value={i.commit}>
          {when(i.time)} · {i.author} · {describeVersion(i, undefined).kind}
        </option>
      ))}
    </>
  )

  return (
    <div className="flex min-h-0 flex-1">
      <div className="min-w-0 flex-1">
        {/* Only this row stays in view: on a phone there is no room for more. */}
        <div className="sticky top-12 z-20 border-b bg-background/95 backdrop-blur">
          <div className="mx-auto flex max-w-[720px] items-center gap-2 px-4 py-2 md:px-5">
            <Button variant="outline" size="sm" onClick={onClose}>
              <ArrowLeft />
              {t.history.back}
            </Button>
            <div className="flex min-w-0 flex-1 items-center gap-1.5 text-sm">
              <History className="size-4 shrink-0 text-muted-foreground" />
              <h1 className="truncate font-medium max-sm:sr-only">{selItem ? `${whenShort(selItem.time)} · ${selItem.author}` : t.history.open}</h1>
            </div>
            {view === 'rendered' && result && stops.length > 0 && (
              <div className="flex shrink-0 items-center gap-0.5">
                <span className="mr-1 hidden text-xs text-muted-foreground tabular-nums sm:inline" aria-live="polite">
                  {at >= 0 ? t.history.changePosition(at + 1, stops.length) : ''}
                </span>
                <Button variant="ghost" size="icon-sm" onClick={() => goTo(-1)} aria-label={t.history.prevChange} title={t.history.prevChange}>
                  <ChevronUp />
                </Button>
                <Button variant="ghost" size="icon-sm" onClick={() => goTo(1)} aria-label={t.history.nextChange} title={t.history.nextChange}>
                  <ChevronDown />
                </Button>
              </div>
            )}
            {isMobile && (
              <Button variant="outline" size="sm" onClick={() => setListOpen(true)}>
                <ListTree />
                {t.history.openList(items.length)}
              </Button>
            )}
          </div>
        </div>

        {status === 'ok' && (
          <div className="mx-auto flex max-w-[720px] flex-wrap items-end gap-x-4 gap-y-2 px-4 pt-3 md:px-5">
            <label className="grid gap-1 text-xs text-muted-foreground">
              {t.history.compareWith}
              <NativeSelect value={mode} onChange={(e) => chooseMode(e.target.value as Mode)} className="w-auto min-w-36">
                <option value="previous">{t.history.modes.previous}</option>
                <option value="current">{t.history.modes.current}</option>
                <option value="pick">{t.history.modes.pick}</option>
              </NativeSelect>
            </label>
            {mode === 'pick' && (
              <>
                <label className="grid min-w-0 gap-1 text-xs text-muted-foreground">
                  {t.history.from}
                  <NativeSelect value={pick.from} onChange={(e) => setPick((p) => ({ ...p, from: e.target.value }))} className="w-auto max-w-56">
                    {pickOptions(false)}
                  </NativeSelect>
                </label>
                <label className="grid min-w-0 gap-1 text-xs text-muted-foreground">
                  {t.history.to}
                  <NativeSelect value={pick.to} onChange={(e) => setPick((p) => ({ ...p, to: e.target.value }))} className="w-auto max-w-56">
                    {pickOptions(true)}
                  </NativeSelect>
                </label>
              </>
            )}
            <div role="group" aria-label={t.history.view} className="flex items-center gap-1">
              <Button variant={view === 'rendered' ? 'secondary' : 'ghost'} size="sm" aria-pressed={view === 'rendered'} onClick={() => setView('rendered')}>
                {t.history.rendered}
              </Button>
              <Button variant={view === 'raw' ? 'secondary' : 'ghost'} size="sm" aria-pressed={view === 'raw'} onClick={() => setView('raw')}>
                {t.history.raw}
              </Button>
            </div>
            {!readOnly && (
              <Button size="sm" className="ml-auto" disabled={!canRestore || restoring} onClick={() => setRestoreOpen(true)} title={selItem ? t.history.restoreAt(when(selItem.time)) : undefined}>
                <RotateCcw />
                {t.history.restore}
              </Button>
            )}
          </div>
        )}

        <div className="mx-auto w-full max-w-[720px] px-4 pt-5 md:px-5">
          <p className="mb-4 text-xs text-muted-foreground">{t.history.banner}</p>
          {status === 'loading' && (
            <p role="status" className="flex items-center gap-2 py-10 text-sm text-muted-foreground">
              <Spinner />
              {t.history.loading}
            </p>
          )}
          {status === 'error' && <ErrorState message={t.fmt.labelled(t.history.loadFailed, history.error?.message ?? '')} onRetry={() => { askedFor.current = null; void history.refetch() }} />}
          {status === 'empty' && (
            <div className="py-12 text-center">
              <p className="text-sm">{t.history.empty}</p>
              <p className="mt-1 text-xs text-muted-foreground">{t.history.emptyHint}</p>
            </div>
          )}
          {status === 'ok' && (
            <>
              <Summary mode={mode} item={selItem} kind={selItem ? describeVersion(selItem, items[selIndex + 1]).kind : ''} base={baseItem} targetIsCurrent={targetIsCurrent} counts={counts} />
              {view === 'rendered' && <Legend />}
              {loadError && <ErrorState message={t.fmt.labelled(t.history.versionFailed, loadError.message)} onRetry={() => { base.retry(); target.retry() }} />}
              {!loadError && !result && (
                <p role="status" className="flex items-center gap-2 py-8 text-sm text-muted-foreground">
                  <Spinner />
                  {waitingForOlder ? t.history.olderMissing : t.history.loadingVersion}
                </p>
              )}
              {!loadError && result && (
                <div className="mt-4">
                  <HeaderChanges changes={result.header} />
                  {view === 'rendered' ? (
                    <>
                      {result.identical && <p className="mb-4 rounded-md border bg-muted/40 px-3 py-2 text-sm">{selItem?.change === 'move' && mode === 'previous' ? t.history.onlyMoved : t.history.identical}</p>}
                      <DiffView doc={result.doc} label={t.history.open} onRendered={onRendered} space={space} folder={folder} />
                    </>
                  ) : (
                    lines && <RawDiff diff={lines} />
                  )}
                </div>
              )}
            </>
          )}
        </div>
      </div>

      {!isMobile && (
        <aside aria-label={t.history.list} className="sticky top-12 hidden h-[calc(100svh-3rem)] w-72 shrink-0 self-start overflow-y-auto border-l md:block lg:w-80">
          {status === 'ok' ? versionList(true) : <div className="p-3 text-xs text-muted-foreground">{status === 'loading' ? t.history.loading : ''}</div>}
        </aside>
      )}

      {isMobile && (
        <Sheet open={listOpen} onOpenChange={setListOpen}>
          <SheetContent side="right" showCloseButton className="w-full data-[side=right]:w-full data-[side=right]:sm:max-w-none">
            <SheetHeader className="border-b">
              <SheetTitle>{t.history.list}</SheetTitle>
            </SheetHeader>
            <div className="min-h-0 flex-1 overflow-y-auto">{versionList(false)}</div>
          </SheetContent>
        </Sheet>
      )}

      <RestoreDialog
        open={restoreOpen}
        onOpenChange={setRestoreOpen}
        busy={restoring}
        item={selItem}
        conflict={conflict}
        unsaved={unsaved}
        same={!!subjectResult?.identical}
        onRestore={(saveFirst) => void restore(saveFirst)}
      />
    </div>
  )
}

function Summary({ mode, item, kind, base, targetIsCurrent, counts }: { mode: Mode; item: HistoryItem | null; kind: string; base: HistoryItem | null; targetIsCurrent: boolean; counts: DiffResult['stats'] | undefined }) {
  if (!item) return null
  const against = mode === 'previous' ? (base ? when(base.time) : null) : null
  return (
    <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 text-sm">
      <span className="font-medium">{`${when(item.time)} · ${item.author} · ${kind}`}</span>
      {mode === 'previous' && <span className="text-muted-foreground">{against ? t.fmt.labelled(t.history.modes.previous, against) : t.history.firstVersion}</span>}
      {mode === 'current' && <span className="text-muted-foreground">{t.history.modes.current}</span>}
      {mode === 'pick' && <span className="text-muted-foreground">{targetIsCurrent ? t.history.currentContent : ''}</span>}
      {counts && (
        <span className="ml-auto font-medium tabular-nums" aria-label={t.history.counts(counts.changes, counts.inserted, counts.deleted)}>
          {counts.changes === 0 ? t.history.changes(0) : `${t.history.changes(counts.changes)} · ${t.history.countsShort(counts.inserted, counts.deleted)}`}
          {counts.formatted > 0 && ` · ${t.history.formatChanges(counts.formatted)}`}
        </span>
      )}
    </div>
  )
}

// What the colours and lines mean, each shown the way it is drawn.
function Legend() {
  return (
    <ul aria-label={t.history.legend} className="pb-diff-sample mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs">
      <li>
        <ins>{t.history.added}</ins>
      </li>
      <li>
        <del>{t.history.removed}</del>
      </li>
      <li>
        <span className="pb-diff-fmt">{t.history.formatted}</span>
      </li>
      <li>
        <span className="pb-diff-move">{t.history.legendMoved}</span>
      </li>
    </ul>
  )
}

function RestoreDialog({ open, onOpenChange, busy, item, conflict, unsaved, same, onRestore }: { open: boolean; onOpenChange: (o: boolean) => void; busy: boolean; item: HistoryItem | null; conflict: boolean; unsaved: boolean; same: boolean; onRestore: (saveFirst: boolean) => void }) {
  const h = t.history
  const info = conflict ? h.restoreConflict : same ? h.restoreSame : null
  return (
    <AlertDialog open={open} onOpenChange={(o) => !busy && onOpenChange(o)}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{h.restoreTitle}</AlertDialogTitle>
          <AlertDialogDescription>{info ?? (item ? h.restoreDescription(when(item.time), item.author) : '')}</AlertDialogDescription>
          {!info && unsaved && <p className="text-sm font-medium text-warning">{h.restoreUnsaved}</p>}
        </AlertDialogHeader>
        <AlertDialogFooter>
          {info ? (
            <AlertDialogCancel>{h.restoreClose}</AlertDialogCancel>
          ) : (
            <>
              <AlertDialogCancel disabled={busy}>{h.restoreCancel}</AlertDialogCancel>
              {unsaved ? (
                <>
                  <Button variant="outline" disabled={busy} onClick={() => onRestore(false)}>
                    {h.restoreDiscard}
                  </Button>
                  <Button disabled={busy} onClick={() => onRestore(true)} title={h.restoreSaveFirstHint}>
                    {busy ? <Spinner /> : null}
                    {h.restoreSaveFirst}
                  </Button>
                </>
              ) : (
                <Button disabled={busy} onClick={() => onRestore(false)}>
                  {busy ? <Spinner /> : null}
                  {h.restoreConfirm}
                </Button>
              )}
            </>
          )}
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
