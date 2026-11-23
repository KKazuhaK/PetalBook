import { useRef, useState, type FormEvent } from 'react'
import { ArrowLeft, ArrowRight, Copy, PenLine, Plus, RotateCcw, Save, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { useSaveViews } from '@/api/hooks'
import type { PutViewsInput, SavedView, ViewList, ViewScope } from '@/api/types'
import { Button } from '@/components/ui/button'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/common'
import { cutBytes, freshName, MAX_NAME_BYTES, MAX_VIEWS, moveView, nameBytes, withState, type ViewState } from '@/lib/savedViews'
import type { ViewOp, Written } from '@/lib/workingCopy'
import { t } from '@/i18n'

// The tabs above the listing of a folder or a tag: its saved views, one of them shown. What is done to the list of
// views (a new one, a new name, a copy, the order, a removal) is saved at once; what is done inside a view (its sort,
// filters, columns) is the working copy that "Save" writes and "Revert" drops. Only people who may write can save; the
// others can switch and change things for themselves.

type Draft = Omit<SavedView, 'id'> & { id?: string }

interface Props {
  scope: ViewScope
  views: SavedView[]
  rev: string
  active: SavedView | null
  // The working copy of the view shown: what the layout, the sort, the filters, … are now.
  working: ViewState
  modified: boolean
  write: boolean
  onPick: (id: string) => void
  // The answer of a write: what was sent and what came back, in the same order, with the list it brings. Said before
  // the cache has the views, so the view shown and its working copy move in the same step as the tabs.
  onWritten: (w: Written, list: ViewList) => void
  onRevert: () => void
}

export function ViewBar({ scope, views, rev, active, working, modified, write, onPick, onWritten, onRevert }: Props) {
  // What the write in flight was, for its answer.
  const answer = useRef<((list: ViewList) => void) | null>(null)
  const save = useSaveViews(scope, (list) => answer.current?.(list))
  const [dialog, setDialog] = useState<null | { kind: 'create' } | { kind: 'rename'; view: SavedView }>(null)
  const [removing, setRemoving] = useState<SavedView | null>(null)
  const drag = useRef<string | null>(null)
  const full = views.length >= MAX_VIEWS

  // Writes the whole list. Nothing is thrown: the person is told what happened. A 409 leaves the views as the server
  // has them in the cache (the hook puts them there), so the tabs show what the other person made.
  const run = async (op: ViewOp, on: string, next: Draft[]): Promise<SavedView[] | null> => {
    try {
      answer.current = (list) => onWritten({ op, on, sent: next, got: list.views, working }, list)
      return (await save.mutateAsync({ baseRev: rev, views: next } satisfies PutViewsInput)).views
    } catch (e) {
      if (e instanceof ApiError && e.code === 'conflict') toast.error(t.views.conflict)
      else toast.error(t.views.failed(e instanceof Error ? e.message : ''))
      return null
    } finally {
      answer.current = null
    }
  }

  const shown = active?.id ?? ''
  const saveWorking = async () => {
    if (!active) return
    if (await run('save', active.id, views.map((v) => (v.id === active.id ? withState(v, working) : v)))) toast.success(t.views.saved)
  }

  const create = async (name: string) => {
    // A new view is the way things look now (for a folder with no view yet, the implicit one).
    if (!(await run('create', shown, [...views, withState({ name } as Draft, working)]))) throw new Error('not saved')
  }
  const rename = async (view: SavedView, name: string) => {
    if (!(await run('rename', view.id, views.map((v) => (v.id === view.id ? { ...v, name } : v))))) throw new Error('not saved')
  }
  const duplicate = async (view: SavedView) => {
    const { id, ...rest } = view
    void id
    // The name of the copy has to fit the server's limit too: the long name is cut, not the words around it (room is left for " 20").
    const room = MAX_NAME_BYTES - nameBytes(t.views.copyName('')) - 3
    const copy: Draft = { ...rest, name: freshName(views.map((v) => v.name), t.views.copyName(cutBytes(view.name, room))) }
    // The copy of the view being shown is made from what is shown.
    await run('duplicate', view.id, [...views, view.id === active?.id ? withState(copy, working) : copy])
  }
  const reorder = async (id: string, to: number) => {
    const next = moveView(views, id, to)
    if (next.every((v, i) => v.id === views[i].id)) return
    await run('reorder', id, next)
  }
  const remove = async (view: SavedView) => {
    await run('delete', view.id, views.filter((v) => v.id !== view.id))
  }

  if (views.length === 0 && !write) return null
  const index = active ? views.findIndex((v) => v.id === active.id) : -1
  const busy = save.isPending

  return (
    <div className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-2" data-slot="view-bar">
      {views.length > 0 && (
        <div role="tablist" aria-label={t.views.label} className="flex min-w-0 max-w-full items-center gap-1 overflow-x-auto">
          {views.map((v) => {
            const on = v.id === active?.id
            return (
              <button
                key={v.id}
                type="button"
                role="tab"
                aria-selected={on}
                data-view={v.id}
                draggable={write && !busy}
                onClick={() => onPick(v.id)}
                onDragStart={(e) => {
                  drag.current = v.id
                  if (e.dataTransfer) {
                    e.dataTransfer.effectAllowed = 'move'
                    e.dataTransfer.setData('application/x-petalbook-view', v.id)
                  }
                }}
                onDragOver={(e) => {
                  if (drag.current && drag.current !== v.id) e.preventDefault()
                }}
                onDrop={(e) => {
                  const id = drag.current
                  drag.current = null
                  if (!id || id === v.id) return
                  e.preventDefault()
                  void reorder(id, views.findIndex((x) => x.id === v.id))
                }}
                onDragEnd={() => (drag.current = null)}
                className={`inline-flex h-8 shrink-0 items-center gap-1.5 rounded-lg border px-3 text-sm outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 ${on ? 'border-transparent bg-muted font-medium text-foreground' : 'border-transparent text-muted-foreground hover:text-foreground'}`}
              >
                {v.name}
                {on && modified && (
                  <span data-slot="modified" className="rounded-full bg-amber-500/15 px-1.5 text-[0.7rem] text-amber-700 dark:text-amber-300">
                    {t.views.modified}
                  </span>
                )}
              </button>
            )
          })}
        </div>
      )}

      {write && (
        <div role="group" aria-label={t.views.actions} className="flex items-center gap-0.5">
          <IconButton label={full ? t.views.tooMany(MAX_VIEWS) : t.views.add} disabled={busy || full} onClick={() => setDialog({ kind: 'create' })}>
            <Plus />
          </IconButton>
          {active && (
            <>
              <IconButton label={t.views.rename} disabled={busy} onClick={() => setDialog({ kind: 'rename', view: active })}>
                <PenLine />
              </IconButton>
              <IconButton label={full ? t.views.tooMany(MAX_VIEWS) : t.views.duplicate} disabled={busy || full} onClick={() => void duplicate(active)}>
                <Copy />
              </IconButton>
              <IconButton label={t.views.moveLeft} disabled={busy || index <= 0} onClick={() => void reorder(active.id, index - 1)}>
                <ArrowLeft />
              </IconButton>
              <IconButton label={t.views.moveRight} disabled={busy || index < 0 || index >= views.length - 1} onClick={() => void reorder(active.id, index + 1)}>
                <ArrowRight />
              </IconButton>
              <IconButton label={t.views.remove} disabled={busy} onClick={() => setRemoving(active)}>
                <Trash2 />
              </IconButton>
            </>
          )}
        </div>
      )}

      {active && modified && (
        <div className="flex items-center gap-1.5" data-slot="view-changes">
          {write && (
            <Button size="sm" onClick={() => void saveWorking()} disabled={busy}>
              {busy ? <Spinner /> : <Save />}
              {t.views.save}
            </Button>
          )}
          <Button variant="outline" size="sm" onClick={onRevert} disabled={busy}>
            <RotateCcw />
            {t.views.revert}
          </Button>
          {!write && <span className="text-xs text-muted-foreground">{t.views.readOnly}</span>}
        </div>
      )}

      {dialog && (
        <NameDialog
          title={dialog.kind === 'create' ? t.views.createTitle : t.views.renameTitle}
          help={dialog.kind === 'create' ? t.views.createHelp : undefined}
          initial={dialog.kind === 'create' ? freshName(views.map((v) => v.name), t.views.defaultName) : dialog.view.name}
          submit={dialog.kind === 'create' ? t.views.create : t.views.rename}
          onSubmit={(name) => (dialog.kind === 'create' ? create(name) : rename(dialog.view, name))}
          onClose={() => setDialog(null)}
        />
      )}
      {removing && (
        <AlertDialog open onOpenChange={(o) => !o && setRemoving(null)}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{t.views.deleteTitle(removing.name)}</AlertDialogTitle>
              <AlertDialogDescription>{t.views.deleteHelp}</AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>{t.views.cancel}</AlertDialogCancel>
              <AlertDialogAction
                variant="destructive"
                onClick={() => {
                  const view = removing
                  setRemoving(null)
                  void remove(view)
                }}
              >
                {t.views.deleteConfirm}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </div>
  )
}

function IconButton({ label, disabled, onClick, children }: { label: string; disabled?: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <Button variant="ghost" size="icon-sm" aria-label={label} title={label} disabled={disabled} onClick={onClick}>
      {children}
    </Button>
  )
}

// Asks for a name. Mounted only while open, so the field starts from `initial` each time.
function NameDialog({ title, help, initial, submit, onSubmit, onClose }: { title: string; help?: string; initial: string; submit: string; onSubmit: (name: string) => Promise<void>; onClose: () => void }) {
  const [value, setValue] = useState(initial)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const send = async (e: FormEvent) => {
    e.preventDefault()
    const name = value.trim()
    if (name === '' || nameBytes(name) > MAX_NAME_BYTES || /[\r\n]/.test(name)) return setError(t.views.nameInvalid)
    setBusy(true)
    try {
      await onSubmit(name)
      onClose()
    } catch {
      // Said by the toast; the name stays for another try.
      setBusy(false)
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent>
        <form className="grid gap-4" onSubmit={(e) => void send(e)} noValidate>
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            {help && <DialogDescription>{help}</DialogDescription>}
          </DialogHeader>
          <div className="grid gap-1.5">
            <Input aria-label={t.views.name} autoFocus value={value} onChange={(e) => setValue(e.target.value)} aria-invalid={error !== ''} />
            {error && (
              <p role="alert" className="text-xs text-destructive">
                {error}
              </p>
            )}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={busy}>
              {t.views.cancel}
            </Button>
            <Button type="submit" disabled={busy}>
              {busy ? <Spinner /> : null}
              {submit}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

