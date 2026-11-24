import { useMemo, useState } from 'react'
import { Folder as FolderIcon, Home } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { baseName, childFolders, itemName, planDrop, type FileItem, type FolderIndex } from '@/lib/folders'
import { t } from '@/i18n'

interface Row {
  path: string
  name: string
  depth: number
}

// Every folder, each under its parent, the space itself first.
function rowsOf(index: FolderIndex, spaceName: string): Row[] {
  const out: Row[] = [{ path: '', name: spaceName, depth: 0 }]
  const walk = (parent: string, depth: number) => {
    for (const f of childFolders(index, parent)) {
      out.push({ path: f.path, name: f.name, depth })
      walk(f.path, depth + 1)
    }
  }
  walk('', 1)
  return out
}

// Where the selected items can go. A folder cannot go into itself or into
// something inside it, and a place where everything already is is only marked.
export function MoveItemsDialog({
  items,
  index,
  spaceName,
  onMove,
  onClose,
}: {
  items: FileItem[]
  index: FolderIndex
  spaceName: string
  onMove: (target: string) => Promise<void>
  onClose: () => void
}) {
  const [filter, setFilter] = useState('')
  const [chosen, setChosen] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const rows = useMemo(() => rowsOf(index, spaceName), [index, spaceName])
  const q = filter.trim().toLowerCase()
  // A filter shows matching folders flat, with their paths, since their parents may not match.
  const shown = q
    ? rows
        .filter((r) => r.path.toLowerCase().includes(q) || r.name.toLowerCase().includes(q))
        .map((r) => ({ ...r, depth: 0 }))
    : rows
  const title =
    items.length === 1
      ? t.manage.moveTitleOne(itemName(items[0]) || t.list.blankPage)
      : t.manage.moveTitleMany(items.length)

  const verdict = (path: string) => {
    const plan = planDrop(items, path, index)
    if (plan.ok) return null
    return plan.reason === 'nothing'
      ? t.manage.moveCurrent
      : plan.reason === 'exists'
        ? t.manage.folderExistsThere(path === '' ? spaceName : baseName(path))
        : t.manage.cannotMoveIntoSelf
  }

  const submit = async () => {
    if (chosen === null) return
    setBusy(true)
    try {
      await onMove(chosen)
    } finally {
      onClose()
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <form
          className="grid gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            void submit()
          }}
        >
          <DialogHeader>
            <DialogTitle className="truncate">{title}</DialogTitle>
            <DialogDescription>{t.manage.moveHelp}</DialogDescription>
          </DialogHeader>
          {rows.length > 8 && (
            <Input
              autoFocus
              autoComplete="off"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder={t.manage.moveFilter}
              aria-label={t.manage.moveFilter}
            />
          )}
          <div
            role="radiogroup"
            aria-label={t.manage.moveTitleMany(items.length)}
            className="max-h-72 overflow-y-auto rounded-lg border p-1"
          >
            {shown.length === 0 && <p className="px-2 py-3 text-sm text-muted-foreground">{t.manage.moveNoMatch}</p>}
            {shown.map((r) => {
              const why = verdict(r.path)
              const selected = chosen === r.path
              return (
                <button
                  key={r.path}
                  type="button"
                  role="radio"
                  aria-checked={selected}
                  disabled={why !== null}
                  title={why ?? undefined}
                  onClick={() => setChosen(r.path)}
                  className={`flex w-full items-center gap-1.5 rounded-md py-1.5 pr-2 text-left text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50 enabled:hover:bg-muted disabled:opacity-45 ${selected ? 'bg-primary/10 font-medium text-primary' : ''}`}
                  style={{ paddingLeft: 8 + r.depth * 16 }}
                >
                  {r.path === '' ? <Home className="size-4 shrink-0" /> : <FolderIcon className="size-4 shrink-0" />}
                  <span className="min-w-0 flex-1 truncate">{q && r.path !== '' ? r.path : r.name}</span>
                  {why === t.manage.moveCurrent && (
                    <span className="shrink-0 text-xs text-muted-foreground">{why}</span>
                  )}
                </button>
              )
            })}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t.manage.cancel}
            </Button>
            <Button type="submit" disabled={chosen === null || busy}>
              {t.manage.moveConfirm}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
