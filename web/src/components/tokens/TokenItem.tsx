import type { ReactNode } from 'react'
import type { ApiToken } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { absoluteTime } from '@/lib/time'
import { tk } from '@/i18n/tokens'

// One token as the lists show it: who it is, what it may do, where, until when,
// and the action. Everything that can be long wraps (names and prefixes are
// whatever a person typed or a random string), so nothing pushes a phone's
// screen sideways.
export function TokenItem({ token, owner, action }: { token: ApiToken; owner?: string; action?: ReactNode }) {
  const ended = token.state !== 'active'
  return (
    <li className="grid w-full min-w-0 grid-cols-1 gap-2 px-3 py-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-start sm:gap-4" data-slot="token-item" data-state={token.state}>
      <div className={`grid min-w-0 gap-1.5 ${ended ? 'opacity-70' : ''}`}>
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          <span className="min-w-0 text-sm font-medium break-words">{token.name}</span>
          <Badge variant={token.state === 'active' ? 'default' : token.state === 'revoked' ? 'destructive' : 'secondary'}>{tk.states[token.state]}</Badge>
          <Badge variant="outline">{tk.accessNames[token.access]}</Badge>
        </div>
        <code className="min-w-0 text-xs break-all text-muted-foreground" aria-label={tk.prefix}>
          {token.prefix}…
        </code>
        {owner !== undefined && <p className="min-w-0 text-xs break-words text-muted-foreground">{`${tk.admin.owner}: ${owner}`}</p>}
        <ul className="flex min-w-0 flex-wrap gap-1" aria-label={tk.spaces}>
          {token.spaces.map((s) => (
            <li key={s.id} className="max-w-full min-w-0 rounded-md bg-muted px-1.5 py-0.5 text-xs break-all" title={s.name === '' ? tk.gone : undefined}>
              {s.name === '' ? `${s.id} ${tk.gone}` : s.name}
            </li>
          ))}
        </ul>
        <p className="min-w-0 text-xs break-words text-muted-foreground">
          {[
            tk.createdOn(absoluteTime(token.created)),
            token.expires ? tk.expiresOn(absoluteTime(token.expires)) : tk.neverExpires,
            token.lastUsed ? tk.lastUsed(absoluteTime(token.lastUsed)) : tk.neverUsed,
          ].join(' · ')}
        </p>
      </div>
      {action && <div className="sm:justify-self-end">{action}</div>}
    </li>
  )
}
