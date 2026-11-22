import { useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { isConflict } from '@/api/client'
import { ApiError } from '@/api/client'
import { useCreateFolder, useUrls } from '@/api/hooks'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { checkFolderPath } from '@/lib/folders'
import { t } from '@/i18n'

export function NewFolderDialog({ open, onOpenChange, parent }: { open: boolean; onOpenChange: (open: boolean) => void; parent: string }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/* Mounted only while open so the field restarts from the current folder each time. */}
      {open && <NewFolderForm parent={parent} onDone={() => onOpenChange(false)} />}
    </Dialog>
  )
}

function NewFolderForm({ parent, onDone }: { parent: string; onDone: () => void }) {
  const [value, setValue] = useState(parent ? `${parent}/` : '')
  const [error, setError] = useState('')
  const create = useCreateFolder()
  const navigate = useNavigate()
  const urls = useUrls()

  const submit = async () => {
    const checked = checkFolderPath(value)
    if ('error' in checked) return setError(checked.error)
    const path = checked.path
    setError('')
    try {
      await create.mutateAsync(path)
      onDone()
      navigate(urls.folder(path))
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
          <DialogDescription>{t.folder.dialogHelp}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-1.5">
          <Label htmlFor="new-folder-path">{t.folder.pathLabel}</Label>
          <Input id="new-folder-path" autoFocus value={value} onChange={(e) => setValue(e.target.value)} aria-invalid={error ? true : undefined} />
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
