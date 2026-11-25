import { useState } from 'react'
import { toast } from 'sonner'
import { useRenameTypeField } from '@/api/hooks'
import type { PageType } from '@/api/types'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { tp } from '@/i18n/props'

// The key of a property is the type's, not a page's: renaming it is one action for the type and every page that uses it,
// made in one commit. Only for those who manage the space.
export function RenameFieldDialog({ type, field, onClose }: { type: PageType; field: string; onClose: () => void }) {
  const rename = useRenameTypeField()
  const [key, setKey] = useState(field)
  const [error, setError] = useState('')
  const submit = async () => {
    setError('')
    try {
      const r = await rename.mutateAsync({ name: type.name, baseRev: type.rev, from: field, to: key.trim() })
      toast.success(tp.panel.renameDone(r.pages))
      onClose()
    } catch (e) {
      setError(e instanceof Error && e.message ? e.message : tp.panel.renameFailed)
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{tp.panel.renameTitle}</DialogTitle>
          <DialogDescription>{tp.panel.renameHelp(field, type.title || type.name)}</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            void submit()
          }}
        >
          <label className="grid gap-1 text-sm">
            {tp.panel.renameNew}
            <input
              autoFocus
              value={key}
              onChange={(e) => setKey(e.target.value)}
              className="h-10 rounded-lg border bg-transparent px-3 font-mono outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
            />
          </label>
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" className="h-10" onClick={onClose}>
              {tp.panel.cancel}
            </Button>
            <Button
              type="submit"
              className="h-10"
              disabled={rename.isPending || key.trim() === '' || key.trim() === field}
            >
              {tp.panel.renameConfirm}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
