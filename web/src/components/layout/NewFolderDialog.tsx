import { useState } from 'react'
import { toast } from 'sonner'
import { ApiError, isConflict } from '@/api/client'
import { useCreateFolder } from '@/api/hooks'
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
import { checkFolderName } from '@/lib/folders'
import { t } from '@/i18n'

// Asks for a name only; the folder is made inside `parent`.
export function NewFolderDialog({
  open,
  onOpenChange,
  parent,
  spaceName,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  parent: string
  spaceName: string
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/* Mounted only while open so the field starts empty each time. */}
      {open && <NewFolderForm parent={parent} spaceName={spaceName} onDone={() => onOpenChange(false)} />}
    </Dialog>
  )
}

function NewFolderForm({ parent, spaceName, onDone }: { parent: string; spaceName: string; onDone: () => void }) {
  const [value, setValue] = useState('')
  const [error, setError] = useState('')
  const create = useCreateFolder()

  const submit = async () => {
    const checked = checkFolderName(parent, value)
    if ('error' in checked) return setError(checked.error)
    setError('')
    try {
      await create.mutateAsync(checked.path)
      onDone()
    } catch (e) {
      if (isConflict(e) || (e instanceof ApiError && e.code === 'exists')) setError(t.folder.exists)
      else if (e instanceof ApiError && e.code === 'invalid') setError(e.message || t.folder.invalid)
      else toast.error(t.fmt.labelled(t.folder.failed, e instanceof Error ? e.message : ''))
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
          <DialogTitle>{t.folder.dialogTitle}</DialogTitle>
          <DialogDescription>
            {t.folder.dialogIn(parent === '' ? spaceName : parent.slice(parent.lastIndexOf('/') + 1))}
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-1.5">
          <Label htmlFor="new-folder-name">{t.folder.nameLabel}</Label>
          <Input
            id="new-folder-name"
            autoFocus
            autoComplete="off"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            aria-invalid={error ? true : undefined}
          />
          {error && <p className="text-xs text-destructive">{error}</p>}
        </div>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onDone}>
            {t.folder.cancel}
          </Button>
          <Button type="submit" disabled={create.isPending}>
            {t.folder.create}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  )
}
