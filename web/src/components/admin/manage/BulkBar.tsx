import { Button } from '@/components/ui/button'
import type { ConfirmAction } from '@/api/types'
import { tg } from '@/i18n/manage'
import { tapTarget } from './shared'

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
      {actions.map((a) => (
        <Button
          className={tapTarget}
          key={a}
          type="button"
          size="sm"
          variant={a.startsWith('delete') || a === 'purge' ? 'destructive' : 'outline'}
          onClick={() => onAction(a)}
        >
          {tg.bulk.actions[a]}
        </Button>
      ))}
      <Button className={tapTarget} type="button" size="sm" variant="ghost" onClick={onClear}>
        {tg.bulk.clear}
      </Button>
    </div>
  )
}
