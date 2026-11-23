import type { ReactNode } from 'react'
import type { Grant } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { absoluteTime } from '@/lib/time'
import { tk } from '@/i18n/tokens'
import { oa } from '@/i18n/oauth'

// One connected app as the lists show it: who it is, what it may do, where, and
// the action. Like a token's row, everything that can be long wraps: a name is
// whatever the app called itself, an identifier is a URL.
export function GrantItem({ grant, owner, action }: { grant: Grant; owner?: string; action?: ReactNode }) {
  return (
    <li className="grid w-full min-w-0 grid-cols-1 gap-2 px-3 py-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-start sm:gap-4" data-slot="grant-item">
      <div className="grid min-w-0 gap-1.5">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          <span className="min-w-0 text-sm font-medium break-words">{grant.client.name}</span>
          <Badge variant="outline">{tk.accessNames[grant.access]}</Badge>
        </div>
        <code className="min-w-0 text-xs break-all text-muted-foreground" aria-label={oa.yours.clientId}>
          {grant.client.id}
        </code>
        {owner !== undefined && <p className="min-w-0 text-xs break-words text-muted-foreground">{`${oa.admin.owner}: ${owner}`}</p>}
        <ul className="flex min-w-0 flex-wrap gap-1" aria-label={tk.spaces}>
          {grant.spaces.map((s) => (
            <li key={s.id} className="max-w-full min-w-0 rounded-md bg-muted px-1.5 py-0.5 text-xs break-all" title={s.name === '' ? oa.yours.gone : undefined}>
              {s.name === '' ? `${s.id} ${oa.yours.gone}` : s.name}
            </li>
          ))}
        </ul>
        <p className="min-w-0 text-xs break-words text-muted-foreground">
          {[oa.yours.createdOn(absoluteTime(grant.created)), ...(grant.lastUsed ? [oa.yours.lastUsed(absoluteTime(grant.lastUsed))] : [])].join(' · ')}
        </p>
      </div>
      {action && <div className="sm:justify-self-end">{action}</div>}
    </li>
  )
}
