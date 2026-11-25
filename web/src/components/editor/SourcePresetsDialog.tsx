import { useState } from 'react'
import { toast } from 'sonner'
import { useSetSourcePresets } from '@/api/hooks'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { DEFAULT_SOURCES } from '@/lib/sources'
import { t } from '@/i18n'

// The presets the input suggests (after the sources pages already use): one to a line, kept in the space's space.yml.
// Emptied, the space goes back to the defaults of the language.
export function SourcePresetsDialog({
  presets,
  chosen,
  onClose,
}: {
  presets: string[]
  chosen: boolean
  onClose: () => void
}) {
  const save = useSetSourcePresets()
  const [text, setText] = useState(presets.join('\n'))
  const [error, setError] = useState('')
  const submit = async (list: string[] | null) => {
    setError('')
    try {
      await save.mutateAsync(list)
      toast.success(t.sources.saved)
      onClose()
    } catch (e) {
      setError(e instanceof Error && e.message ? e.message : t.sources.failed)
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t.sources.presetsTitle}</DialogTitle>
          <DialogDescription>{t.sources.presetsHelp}</DialogDescription>
        </DialogHeader>
        <textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          rows={8}
          aria-label={t.sources.presetsTitle}
          className="rounded-lg border bg-transparent p-3 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        />
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
        <DialogFooter className="gap-2 sm:justify-between">
          <Button
            type="button"
            variant="ghost"
            className="h-10"
            disabled={!chosen || save.isPending}
            onClick={() => void submit(null)}
            title={DEFAULT_SOURCES.join(' · ')}
          >
            {t.sources.useDefaults}
          </Button>
          <div className="flex gap-2">
            <Button type="button" variant="outline" className="h-10" onClick={onClose}>
              {t.icons.cancel}
            </Button>
            <Button
              type="button"
              className="h-10"
              disabled={save.isPending}
              onClick={() =>
                void submit(
                  text
                    .split('\n')
                    .map((l) => l.trim())
                    .filter(Boolean),
                )
              }
            >
              {t.icons.save}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
