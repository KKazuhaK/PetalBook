import { RefreshCw } from 'lucide-react'
import { useAdminStatus } from '@/api/admin'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { Button } from '@/components/ui/button'
import { formatBytes } from '@/lib/bytes'
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'
import { ta } from '@/i18n/admin'
import { ts } from '@/i18n/storage'

// What the server says about itself (GET /api/admin/status).
export function StatusPanel() {
  const status = useAdminStatus()
  if (status.isPending) return <PageFallback />
  if (status.isError)
    return (
      <ErrorState
        message={t.fmt.labelled(ta.status.loadFailed, status.error.message)}
        onRetry={() => void status.refetch()}
      />
    )
  const s = status.data
  const st = ta.status
  const uptime = Math.max(0, s.uptimeSeconds)
  const rows: [string, string, string?][] = [
    [st.version, s.version],
    [st.started, absoluteTime(s.startedAt)],
    [
      st.uptime,
      st.duration(Math.floor(uptime / 86_400), Math.floor((uptime % 86_400) / 3600), Math.floor((uptime % 3600) / 60)),
    ],
    [st.database, s.database],
    [st.spaces, String(s.spaces)],
    [st.users, String(s.users)],
    [st.openSpaces, String(s.openSpaces), st.openSpacesHelp],
    [st.disk, s.diskFreeBytes === undefined ? st.unknown : formatBytes(s.diskFreeBytes)],
    [st.goVersion, s.goVersion],
    [
      ts.status.spaces,
      ts.status.spacesValue(
        s.storage.spaces.local,
        s.storage.spaces.full,
        s.storage.spaces.partial,
        s.storage.spaces.evicted,
        s.storage.spaces.restoring,
      ),
    ],
    [ts.status.clones, formatBytes(s.storage.cloneBytes)],
    [ts.status.indexes, formatBytes(s.storage.indexBytes)],
    [
      ts.status.assetCache,
      ts.status.assetCacheValue(
        formatBytes(s.storage.assetCacheBytes),
        s.storage.assetsBudget === null ? ts.status.noBudget : formatBytes(s.storage.assetsBudget),
      ),
    ],
    [ts.status.budget, s.storage.clonesBudget === null ? ts.status.noBudget : formatBytes(s.storage.clonesBudget)],
    [ts.status.restoresWaiting, String(s.storage.restoresWaiting)],
    [ts.status.remotes, ts.status.remotesValue(s.storage.remotes.inGrace, s.storage.remotes.overdue)],
  ]
  return (
    <SettingsSection title={st.title} id="status-heading">
      <dl className="divide-y rounded-lg border" aria-labelledby="status-heading">
        {rows.map(([name, value, help]) => (
          <div key={name} className="flex flex-wrap items-baseline justify-between gap-x-6 gap-y-0.5 px-3 py-2.5">
            <dt className="text-sm">
              {name}
              {help && <span className="ml-2 text-xs text-muted-foreground">{help}</span>}
            </dt>
            <dd className="text-sm font-medium tabular-nums">{value}</dd>
          </div>
        ))}
      </dl>
      <div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={status.isFetching}
          onClick={() => void status.refetch()}
        >
          {status.isFetching ? <Spinner /> : <RefreshCw />}
          {st.refresh}
        </Button>
      </div>
    </SettingsSection>
  )
}
