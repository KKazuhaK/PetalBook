import { useState } from 'react'
import { toast } from 'sonner'
import { errorMessage, useSetFolderTemplate, useTemplates } from '@/api/hooks'
import { Spinner } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { baseName } from '@/lib/folders'
import { templateLabel } from '@/lib/templates'
import { templateMessage } from '@/lib/templateErrors'
import { t } from '@/i18n'
import { tt } from '@/i18n/templates'

// Sets the template a page made in a folder starts with, or takes it away (needs `manage`). Loaded when it is first
// opened.
export default function SetTemplateDialog({
  path,
  spaceName,
  onClose,
}: {
  path: string
  spaceName: string
  onClose: () => void
}) {
  const templates = useTemplates()
  const set = useSetFolderTemplate()
  const own = templates.data?.defaults.folders[path] ?? ''
  // What the person picked; nothing until they pick, so the box follows the data until then.
  const [picked, setPicked] = useState<string | null>(null)
  const value = picked ?? own
  const list = templates.data?.templates ?? []
  const where = path === '' ? spaceName : baseName(path)

  const submit = async () => {
    if (value === own) return onClose()
    try {
      await set.mutateAsync({ folder: path, template: value })
      toast.success(value === '' ? tt.setDefault.removed : tt.setDefault.done(value))
      onClose()
    } catch (e) {
      toast.error(t.fmt.labelled(tt.setDefault.failed, templateMessage(e, errorMessage(e, ''))))
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
            <DialogTitle>{tt.setDefault.title(where)}</DialogTitle>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="set-template">{tt.setDefault.label}</Label>
            <NativeSelect
              id="set-template"
              value={value}
              disabled={templates.isPending}
              onChange={(e) => setPicked(e.target.value)}
            >
              <option value="">{tt.setDefault.none}</option>
              {list.map((x) => (
                <option key={x.name} value={x.name}>
                  {templateLabel(x)}
                </option>
              ))}
            </NativeSelect>
            {templates.isSuccess && list.length === 0 && (
              <p className="text-xs text-muted-foreground">{tt.setDefault.noTemplates}</p>
            )}
            <p className="text-xs text-muted-foreground">{tt.setDefault.help}</p>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {tt.cancel}
            </Button>
            <Button type="submit" disabled={templates.isPending || set.isPending}>
              {set.isPending && <Spinner />}
              {tt.setDefault.confirm}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
