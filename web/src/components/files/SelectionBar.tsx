import { FolderInput, Trash2, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { t } from '@/i18n'

// Shown while anything is selected: how many, and what can be done to all of
// them. It takes the place of the listing's own controls in the top bar (as
// mail and file apps do), so it is always in the same spot, never over the
// rows, and showing it moves nothing.
export function SelectionBar({ count, onMove, onDelete, onClear }: { count: number; onMove: () => void; onDelete: () => void; onClear: () => void }) {
  return (
    <div role="toolbar" aria-label={t.manage.selectionBar} className="flex items-center gap-1">
      <span className="mr-1 text-sm font-medium whitespace-nowrap tabular-nums" aria-live="polite">
        {t.manage.selected(count)}
      </span>
      <Button variant="ghost" size="sm" onClick={onMove}>
        <FolderInput />
        <span className="max-sm:hidden">{t.manage.move}</span>
      </Button>
      <Button variant="ghost" size="sm" onClick={onDelete} className="text-destructive hover:bg-destructive/10 hover:text-destructive">
        <Trash2 />
        <span className="max-sm:hidden">{t.manage.delete}</span>
      </Button>
      <Button variant="ghost" size="icon-sm" onClick={onClear} aria-label={t.manage.clearSelection} title={t.manage.clearSelection}>
        <X />
      </Button>
    </div>
  )
}
