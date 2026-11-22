import { useState } from 'react'
import { toast } from 'sonner'
import { errorMessage, useBindFolder, useTypes } from '@/api/hooks'
import { Spinner } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { bindingSource, folderTypeName } from '@/lib/pageTypes'
import { baseName } from '@/lib/folders'
import { t } from '@/i18n'
import { tp } from '@/i18n/props'

// Binds a folder to a type, or takes the binding away. A folder with no binding
// of its own has the type of the nearest folder above it, which is said here.
// Loaded when it is first opened.
export default function SetTypeDialog({ path, spaceName, onClose }: { path: string; spaceName: string; onClose: () => void }) {
  const types = useTypes()
  const bind = useBindFolder()
  const own = types.data?.bindings.find((b) => b.folder === path)?.type ?? ''
  // What the person picked; nothing until they pick, so the box follows the data until then.
  const [picked, setPicked] = useState<string | null>(null)
  const value = picked ?? own
  const bindings = types.data?.bindings ?? []
  const inherited = own === '' ? folderTypeName(bindings, path) : ''
  const from = inherited ? bindingSource(bindings, path) : null
  const titleOf = (name: string) => types.data?.types.find((x) => x.name === name)?.title || name
  const where = path === '' ? spaceName : baseName(path)

  const submit = async () => {
    if (value === own) return onClose()
    try {
      await bind.mutateAsync({ folder: path, type: value })
      toast.success(value === '' ? tp.setType.removed : tp.setType.done(titleOf(value)))
      onClose()
    } catch (e) {
      toast.error(t.fmt.labelled(tp.setType.failed, errorMessage(e, '')))
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
            <DialogTitle>{tp.setType.title(where)}</DialogTitle>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="set-type">{tp.setType.label}</Label>
            <NativeSelect id="set-type" value={value} disabled={types.isPending} onChange={(e) => setPicked(e.target.value)}>
              <option value="">{tp.setType.none}</option>
              {(types.data?.types ?? [])
                .filter((x) => !x.error)
                .map((x) => (
                  <option key={x.name} value={x.name}>
                    {x.title || x.name}
                  </option>
                ))}
            </NativeSelect>
            {types.isError && <p className="text-xs text-destructive">{tp.types.loadFailed}</p>}
            {types.isSuccess && types.data.types.length === 0 && <p className="text-xs text-muted-foreground">{tp.setType.noTypes}</p>}
            {inherited !== '' && picked === null && <p className="text-xs text-muted-foreground">{tp.setType.inherited(titleOf(inherited), from === '' || from === null ? spaceName : baseName(from))}</p>}
            <p className="text-xs text-muted-foreground">{tp.setType.help}</p>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {tp.cancel}
            </Button>
            <Button type="submit" disabled={types.isPending || bind.isPending}>
              {bind.isPending && <Spinner />}
              {tp.setType.confirm}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
