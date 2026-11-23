import { useState } from 'react'
import { Spinner } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import type { PageTemplate } from '@/api/types'
import { templateLabel } from '@/lib/templates'
import { tt } from '@/i18n/templates'

// The choice of a template for a new page: the folder's default is selected, "blank page" sends no template at all.
// Only asked when the space has templates. Loaded when it is first opened.
export default function NewPageDialog({ where, templates, initial, onCreate, onClose }: { where: string; templates: PageTemplate[]; initial: string; onCreate: (template: string) => Promise<void>; onClose: () => void }) {
  const [value, setValue] = useState(initial)
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    if (busy) return
    setBusy(true)
    try {
      // The page is made and opened (or the failure told) by the one who asked; the dialog is done either way.
      await onCreate(value)
    } finally {
      onClose()
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            void submit()
          }}
        >
          <DialogHeader>
            <DialogTitle>{tt.pick.title}</DialogTitle>
            <p className="text-xs text-muted-foreground">{tt.pick.where(where)}</p>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="new-page-template">{tt.pick.label}</Label>
            <NativeSelect id="new-page-template" autoFocus value={value} disabled={busy} onChange={(e) => setValue(e.target.value)}>
              <option value="">{tt.pick.blank}</option>
              {templates.map((x) => (
                <option key={x.name} value={x.name}>
                  {templateLabel(x)}
                  {x.name === initial ? ` ${tt.pick.isDefault}` : ''}
                </option>
              ))}
            </NativeSelect>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" disabled={busy} onClick={onClose}>
              {tt.cancel}
            </Button>
            <Button type="submit" disabled={busy}>
              {busy && <Spinner />}
              {busy ? tt.pick.creating : tt.pick.confirm}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
