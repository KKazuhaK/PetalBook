import { lazy, Suspense, useState } from 'react'
import type { IconColor } from '@/api/types'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { isIconColor } from '@/lib/icons/colors'
import { noteRecent } from '@/lib/icons/recent'
import { t } from '@/i18n'
import { PageIcon } from './PageIcon'

// The set of icons and the words to search it by are a chunk of their own: nothing of it is fetched until a picker is
// opened, and it is never fetched from anywhere but this server.
const IconPickerBody = lazy(() => import('./IconPickerBody'))

// "Icon and colour" of a folder or a page: pick a Lucide icon (search by name, tag or Chinese) or type an emoji, and one
// of eight colours. `onSave` gets "" for no icon and "" for the default colour.
export function IconPicker({
  open,
  onOpenChange,
  description,
  icon,
  color,
  fallback,
  onSave,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  description: string
  icon: string
  color: string
  // What is drawn when there is no icon (the default of the folder, the page or its type).
  fallback?: React.ReactNode
  onSave: (icon: string, color: IconColor | '') => Promise<unknown>
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[min(40rem,calc(100dvh-2rem))] w-full flex-col sm:max-w-lg max-sm:inset-0 max-sm:h-dvh max-sm:max-h-none max-sm:max-w-none max-sm:translate-x-0 max-sm:translate-y-0 max-sm:rounded-none">
        {open && (
          <PickerForm
            description={description}
            icon={icon}
            color={isIconColor(color) ? color : ''}
            fallback={fallback}
            onSave={onSave}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function PickerForm({
  description,
  icon: first,
  color: firstColor,
  fallback,
  onSave,
  onClose,
}: {
  description: string
  icon: string
  color: IconColor | ''
  fallback?: React.ReactNode
  onSave: (icon: string, color: IconColor | '') => Promise<unknown>
  onClose: () => void
}) {
  const [draft, setDraft] = useState<{ icon: string; color: IconColor | '' }>({ icon: first, color: firstColor })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const save = async (next: { icon: string; color: IconColor | '' }) => {
    setBusy(true)
    setError('')
    try {
      await onSave(next.icon, next.color)
      if (next.icon) noteRecent(next.icon)
      onClose()
    } catch (e) {
      setError(e instanceof Error && e.message ? e.message : t.icons.failed)
      setBusy(false)
    }
  }
  return (
    <>
      <DialogHeader>
        <DialogTitle className="flex items-center gap-2">
          <PageIcon icon={draft.icon} color={draft.color} fallback={fallback} className="size-5" />
          {t.icons.title}
        </DialogTitle>
        <DialogDescription>{description}</DialogDescription>
      </DialogHeader>
      <Suspense fallback={<p className="py-10 text-center text-sm text-muted-foreground">{t.icons.loading}</p>}>
        <IconPickerBody icon={draft.icon} color={draft.color} onChange={setDraft} />
      </Suspense>
      <label className="flex items-center gap-2 text-sm">
        <span className="shrink-0 text-muted-foreground">{t.icons.emoji}</span>
        <input
          value={/^[a-z][a-z0-9-]*$/.test(draft.icon) ? '' : draft.icon}
          onChange={(e) => setDraft({ ...draft, icon: e.target.value.trim().slice(0, 16) })}
          placeholder={t.icons.emojiPlaceholder}
          aria-label={t.icons.emoji}
          className="h-10 min-w-0 flex-1 rounded-lg border bg-transparent px-3 outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        />
      </label>
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
          disabled={busy || (first === '' && firstColor === '')}
          onClick={() => void save({ icon: '', color: '' })}
        >
          {t.icons.remove}
        </Button>
        <div className="flex gap-2">
          <Button type="button" variant="outline" className="h-10" onClick={onClose}>
            {t.icons.cancel}
          </Button>
          <Button type="button" className="h-10" disabled={busy} onClick={() => void save(draft)}>
            {t.icons.save}
          </Button>
        </div>
      </DialogFooter>
    </>
  )
}
