import { usePerson } from '@/api/manage'
import { Badge } from '@/components/ui/badge'
import { LocationBadge, useIsSite } from '../manage/shared'
import { tg } from '@/i18n/manage'
import { ts } from '@/i18n/storage'
import type { User } from '@/api/types'
import { ErrorState } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { formatBytes } from '@/lib/bytes'
import { absoluteTime } from '@/lib/time'
import { tc } from '@/i18n/console'

// What the person holds on the server, from the same stored measurements as the storage management (never a live walk
// of their files), and the spaces counted for them.
export function PersonUsage({ u }: { u: User }) {
  const person = usePerson(u.id)
  const site = useIsSite()
  if (person.isPending) return <div aria-busy="true" className="h-24 animate-pulse rounded-lg bg-muted" />
  if (person.isError) return <ErrorState message={tc.person.usage.loadFailed} onRetry={() => void person.refetch()} />
  const p = person.data
  const numbers: [string, string][] = [
    [tc.person.usage.spaces, String(p.spaces)],
    [tc.person.usage.held, formatBytes(p.heldBytes)],
    [tc.person.usage.counted, formatBytes(p.usageBytes)],
    [tc.person.usage.lastActive, p.lastActive ? absoluteTime(p.lastActive) : tc.users.never],
    [tc.person.usage.measured, p.measuredAt ? absoluteTime(p.measuredAt) : tc.person.usage.none],
  ]
  return (
    <div className="grid min-w-0 gap-8" data-slot="person-usage">
      <SettingsSection title={tc.person.usage.title} id="person-usage-numbers" description={tc.person.usage.help}>
        <dl className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {numbers.map(([k, v]) => (
            <div key={k} className="rounded-lg border px-3 py-2.5">
              <dt className="text-xs text-muted-foreground">{k}</dt>
              <dd className="mt-0.5 text-base font-medium tabular-nums">{v}</dd>
            </div>
          ))}
        </dl>
        <p className="text-xs text-muted-foreground">
          {tg.people.spacesLine(p.spaces, p.remoteSpaces, p.evictedSpaces, p.pinnedSpaces)} ·{' '}
          {p.connected ? tg.people.connected : tg.people.notConnected} ·{' '}
          {p.firstWrite ? tg.people.firstWrite(absoluteTime(p.firstWrite)) : tg.people.neverWrote} ·{' '}
          {tg.people.deadlines(p.graceSpaces, p.overdueSpaces)}
        </p>
      </SettingsSection>
      <SettingsSection title={tc.person.usage.owned} id="person-usage-spaces">
        {p.items.length === 0 ? (
          <p className="text-sm text-muted-foreground">{tc.person.usage.noSpaces}</p>
        ) : (
          <ul className="divide-y rounded-lg border" data-slot="person-spaces">
            {p.items.map((s) => (
              <li
                key={s.id}
                className="grid min-w-0 gap-1 px-3 py-2 sm:grid-cols-[1fr_auto] sm:items-center"
                data-space={s.id}
              >
                <div className="min-w-0">
                  <div className="truncate text-sm font-medium">
                    {s.name} {s.personal && <Badge variant="outline">{tg.recycle.personal}</Badge>}
                  </div>
                  <div className="text-xs text-muted-foreground tabular-nums">
                    {s.sizeBytes === null ? tc.person.usage.none : formatBytes(s.sizeBytes)} ·{' '}
                    {ts.spaces.caches[s.storage.cache]}
                  </div>
                </div>
                <LocationBadge location={s.location} counted={s.counted} />
              </li>
            ))}
          </ul>
        )}
        {!site && <p className="text-xs text-muted-foreground">{tg.people.tenantNote}</p>}
      </SettingsSection>
    </div>
  )
}
