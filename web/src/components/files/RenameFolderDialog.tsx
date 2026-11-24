import { useState } from 'react'
import { toast } from 'sonner'
import { ApiError, isConflict } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { baseName, checkFolderName, parentOf, type FolderIndex } from '@/lib/folders'
import { t } from '@/i18n'

// A rename is a move to the sibling path with the new name; nothing else about the folder changes.
export function RenameFolderDialog({
  path,
  index,
  onRename,
  onClose,
}: {
  path: string
  index: FolderIndex
  onRename: (from: string, to: string) => Promise<void>
  onClose: () => void
}) {
  const current = baseName(path)
  const [value, setValue] = useState(current)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    const checked = checkFolderName(parentOf(path), value)
    if ('error' in checked) return setError(checked.error)
    if (checked.path === path) return onClose()
    if (index.byPath.has(checked.path)) return setError(t.folder.exists)
    setBusy(true)
    try {
      await onRename(path, checked.path)
      onClose()
    } catch (e) {
      setBusy(false)
      if (isConflict(e) || (e instanceof ApiError && e.code === 'exists')) setError(t.folder.exists)
      else if (e instanceof ApiError && e.code === 'invalid') setError(e.message || t.folder.invalid)
      else toast.error(t.fmt.labelled(t.manage.renameFailed, e instanceof Error ? e.message : ''))
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            void submit()
          }}
        >
          <DialogHeader>
            <DialogTitle>{t.manage.renameTitle}</DialogTitle>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="rename-folder-name">{t.manage.renameLabel}</Label>
            <Input
              id="rename-folder-name"
              autoFocus
              autoComplete="off"
              value={value}
              onFocus={(e) => e.currentTarget.select()}
              onChange={(e) => setValue(e.target.value)}
              aria-invalid={error ? true : undefined}
            />
            {error && <p className="text-xs text-destructive">{error}</p>}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t.manage.cancel}
            </Button>
            <Button type="submit" disabled={busy}>
              {t.manage.renameConfirm}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
