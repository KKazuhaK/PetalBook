import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router'
import { adminApi } from '@/api/admin'
import { keys } from '@/api/hooks'
import type { User } from '@/api/types'
import { ErrorState } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { Badge } from '@/components/ui/badge'
import { t } from '@/i18n'
import { tc } from '@/i18n/console'

// Where a person belongs: the spaces they are a member of with their role there, and the groups they are in.
export function PersonSpaces({ u }: { u: User }) {
  const list = useQuery({
    queryKey: [...keys.users, 'memberships', u.id],
    queryFn: ({ signal }) => adminApi.userMemberships(u.id, signal),
    staleTime: 10_000,
  })
  if (list.isPending) return <div aria-busy="true" className="h-24 animate-pulse rounded-lg bg-muted" />
  if (list.isError) return <ErrorState message={tc.person.memberships.loadFailed} onRetry={() => void list.refetch()} />
  const { spaces, groups } = list.data
  return (
    <div className="grid min-w-0 gap-8" data-slot="person-spaces">
      <SettingsSection title={tc.person.memberships.spaces} id="person-spaces-list">
        {spaces.length === 0 ? (
          <p className="text-sm text-muted-foreground">{tc.person.memberships.noSpaces}</p>
        ) : (
          <ul className="divide-y rounded-lg border">
            {spaces.map((s) => (
              <li
                key={s.id}
                className="flex min-h-12 min-w-0 flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2"
                data-space={s.id}
              >
                <span className="min-w-0 flex-1 basis-40 truncate text-sm font-medium">{s.name}</span>
                {s.personal && <Badge variant="outline">{tc.person.memberships.personal}</Badge>}
                <span className="text-xs text-muted-foreground">
                  {t.roles[s.role as keyof typeof t.roles] ?? s.role}
                </span>
              </li>
            ))}
          </ul>
        )}
      </SettingsSection>
      <SettingsSection title={tc.person.memberships.groups} id="person-groups-list">
        {groups.length === 0 ? (
          <p className="text-sm text-muted-foreground">{tc.person.memberships.noGroups}</p>
        ) : (
          <ul className="divide-y rounded-lg border">
            {groups.map((g) => (
              <li
                key={g.id}
                className="flex min-h-12 min-w-0 flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2"
                data-group={g.id}
              >
                <Link
                  to="/admin/groups"
                  className="min-w-0 flex-1 basis-40 truncate text-sm font-medium underline-offset-4 hover:underline"
                >
                  {g.name}
                </Link>
                <Badge variant="outline">
                  {g.source === 'synced' ? tc.person.memberships.synced : tc.person.memberships.local}
                </Badge>
              </li>
            ))}
          </ul>
        )}
      </SettingsSection>
    </div>
  )
}
