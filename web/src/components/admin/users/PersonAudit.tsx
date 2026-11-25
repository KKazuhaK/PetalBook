import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { adminApi } from '@/api/admin'
import { keys } from '@/api/hooks'
import type { AuditItem, User } from '@/api/types'
import { detailText } from '@/components/admin/AuditSection'
import { Badge } from '@/components/ui/badge'
import { DataTable, type Column } from '@/components/ui/data-table'
import { absoluteTime } from '@/lib/time'
import { useTablePrefs } from '@/lib/tablePrefs'
import { useCursorPages } from '@/lib/useCursorPages'
import { t } from '@/i18n'
import { tc } from '@/i18n/console'
import { tsec } from '@/i18n/security'

// The audit rows about this person, newest first, a page at a time (the server's own paging).
export function PersonAudit({ u }: { u: User }) {
  const prefs = useTablePrefs('person-audit')
  const pages = useCursorPages(`${u.id}|${prefs.prefs.pageSize}`)
  const list = useQuery({
    queryKey: [...keys.users, 'audit', u.id, pages.cursor, prefs.prefs.pageSize],
    queryFn: ({ signal }) =>
      adminApi.audit({ user: u.id, cursor: pages.cursor || undefined, limit: prefs.prefs.pageSize }, signal),
    placeholderData: (prev) => prev,
    staleTime: 10_000,
  })
  const rows = list.data?.items ?? []
  const next = list.data?.next ?? ''
  const columns = useMemo<Column<AuditItem>[]>(
    () => [
      {
        id: 'kind',
        header: tc.person.audit.kind,
        required: true,
        min: 150,
        weight: 2,
        cell: (r) => <Badge variant="secondary">{tsec.audit.kinds[r.kind] ?? r.kind}</Badge>,
      },
      {
        id: 'at',
        header: tc.person.audit.time,
        min: 150,
        weight: 2,
        priority: 2,
        cell: (r) => <time dateTime={r.at}>{absoluteTime(r.at)}</time>,
      },
      { id: 'actor', header: tc.person.audit.actor, min: 120, weight: 1.5, priority: 4, cell: (r) => r.actor ?? '' },
      { id: 'ip', header: tc.person.audit.ip, min: 120, weight: 1.5, priority: 5, cell: (r) => r.ip },
      {
        id: 'detail',
        header: tc.person.audit.detail,
        min: 200,
        weight: 4,
        priority: 3,
        cell: (r) => <span className="font-mono text-xs text-muted-foreground">{detailText(r.detail)}</span>,
      },
    ],
    [],
  )
  return (
    <div className="min-w-0 space-y-3" data-slot="person-audit">
      <div>
        <h3 className="text-sm font-semibold">{tc.person.audit.title}</h3>
        <p className="text-xs text-muted-foreground">{tc.person.audit.help}</p>
      </div>
      <DataTable
        prefs={prefs}
        columns={columns}
        rows={rows}
        rowId={(r) => r.id}
        rowLabel={(r) => tsec.audit.kinds[r.kind] ?? r.kind}
        rowAttrs={(r) => ({ 'data-audit': r.id })}
        loading={list.isPending}
        fetching={list.isFetching && !list.isPending}
        error={list.isError ? t.fmt.labelled(tc.person.audit.loadFailed, list.error.message) : undefined}
        onRetry={() => void list.refetch()}
        empty={tc.person.audit.empty}
        paging={{
          page: pages.page,
          hasPrev: pages.hasPrev,
          hasNext: next !== '',
          onPrev: pages.prev,
          onNext: () => pages.next(next),
        }}
      />
    </div>
  )
}
