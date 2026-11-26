import { Ban, CircleCheck, Download, HardDrive, Pin, PinOff, RotateCcw, Trash2, X, type LucideIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { ConfirmAction } from '@/api/types'
import { tg } from '@/i18n/manage'
import { tapTarget } from './shared'

const ICONS: Record<ConfirmAction, LucideIcon> = {
  delete_spaces: Trash2,
  delete_users: Trash2,
  purge: Trash2,
  restore: RotateCcw,
  evict: HardDrive,
  pin: Pin,
  unpin: PinOff,
  disable_spaces: Ban,
  enable_spaces: CircleCheck,
  disable_users: Ban,
  enable_users: CircleCheck,
  export: Download,
}

// The actions on what is ticked in a list.
export function BulkBar({
  count,
  actions,
  onAction,
  onClear,
}: {
  count: number
  actions: ConfirmAction[]
  onAction: (a: ConfirmAction) => void
  onClear: () => void
}) {
  if (count === 0) return null
  return (
    <div
      role="toolbar"
      data-slot="bulk-bar"
      className="flex flex-wrap items-center gap-2 rounded-lg border bg-muted/40 px-3 py-2"
    >
      <span className="text-sm font-medium">{tg.bulk.selected(count)}</span>
      {actions.map((a) => {
        const Icon = ICONS[a]
        return (
          <Button
            className={tapTarget}
            key={a}
            type="button"
            size="sm"
            variant={a.startsWith('delete') || a === 'purge' ? 'destructive' : 'outline'}
            onClick={() => onAction(a)}
          >
            <Icon aria-hidden="true" />
            {tg.bulk.actions[a]}
          </Button>
        )
      })}
      <Button className={tapTarget} type="button" size="sm" variant="ghost" onClick={onClear}>
        <X aria-hidden="true" />
        {tg.bulk.clear}
      </Button>
    </div>
  )
}
