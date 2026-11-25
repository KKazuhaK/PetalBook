import { useProblemCount } from '@/api/hooks'
import { t } from '@/i18n'

// A small red dot where the settings of a space are reached, when the space has problems (the page that lists them is
// opened from the settings, no longer from the sidebar). Nothing while there are none or the count is not known.
export function ProblemDot({ className = '' }: { className?: string }) {
  const count = useProblemCount().data ?? 0
  if (count === 0) return null
  return (
    <span
      role="img"
      aria-label={t.general.checkDot}
      title={t.general.checkDot}
      data-slot="problem-dot"
      className={`size-2 shrink-0 rounded-full bg-destructive ${className}`}
    />
  )
}
