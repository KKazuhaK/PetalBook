import { ChevronRight } from 'lucide-react'
import { tp } from '@/i18n/props'

// The fields the system keeps (the page's id and the day it was made): shown, never edited. Collapsed, so they stay out
// of the way.
export function SystemFields({ id, created }: { id: string; created?: string }) {
  return (
    <details className="group text-xs text-muted-foreground" data-slot="system-fields">
      <summary className="flex min-h-8 cursor-pointer list-none items-center gap-1 outline-none focus-visible:ring-2 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden">
        <ChevronRight className="size-3.5 transition-transform group-open:rotate-90" aria-hidden />
        {tp.panel.system}
      </summary>
      <dl className="mt-1 grid grid-cols-[minmax(0,7rem)_minmax(0,1fr)] gap-x-3 gap-y-1" title={tp.panel.systemHelp}>
        <dt>{tp.panel.systemId}</dt>
        <dd className="font-mono text-foreground/80 select-all" data-field="id">
          {id}
        </dd>
        {created && (
          <>
            <dt>{tp.panel.systemCreated}</dt>
            <dd className="font-mono text-foreground/80" data-field="created">
              {created}
            </dd>
          </>
        )}
      </dl>
    </details>
  )
}
