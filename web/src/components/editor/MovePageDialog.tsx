import { useId, useState } from 'react'
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
import { Label } from '@/components/ui/label'
import { checkFolderPath } from '@/lib/folders'
import { t } from '@/i18n'

export function MovePageDialog({
  open,
  onOpenChange,
  current,
  folders,
  onMove,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  current: string
  folders: string[]
  onMove: (folder: string) => Promise<void>
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {open && <MoveForm current={current} folders={folders} onMove={onMove} onClose={() => onOpenChange(false)} />}
    </Dialog>
  )
}

function MoveForm({
  current,
  folders,
  onMove,
  onClose,
}: {
  current: string
  folders: string[]
  onMove: (folder: string) => Promise<void>
  onClose: () => void
}) {
  const [value, setValue] = useState(current)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const listId = useId()

  const submit = async () => {
    // Empty means the root folder.
    let path = ''
    if (value.trim() !== '') {
      const checked = checkFolderPath(value)
      if ('error' in checked) return setError(checked.error)
      path = checked.path
    }
    if (path === current) return onClose()
    setBusy(true)
    try {
      await onMove(path)
      onClose()
    } catch {
      setBusy(false)
    }
  }

  return (
    <DialogContent>
      <form
        className="grid gap-4"
        onSubmit={(e) => {
          e.preventDefault()
          void submit()
        }}
      >
        <DialogHeader>
          <DialogTitle>{t.move.title}</DialogTitle>
          <DialogDescription>{t.move.help}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-1.5">
          <Label htmlFor={listId}>{t.move.pathLabel}</Label>
          <Input
            id={listId}
            list={`${listId}-folders`}
            autoFocus
            value={value}
            placeholder={t.nav.rootFolder}
            onChange={(e) => setValue(e.target.value)}
            aria-invalid={error ? true : undefined}
          />
          <datalist id={`${listId}-folders`}>
            {folders
              .filter((f) => f !== '')
              .map((f) => (
                <option key={f} value={f} />
              ))}
          </datalist>
          {error && <p className="text-xs text-destructive">{error}</p>}
        </div>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            {t.move.cancel}
          </Button>
          <Button type="submit" disabled={busy}>
            {t.move.confirm}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  )
}
