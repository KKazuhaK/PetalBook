import { useAdminStorage } from '@/api/admin'
import { ErrorState, PageFallback } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { formatBytes } from '@/lib/bytes'
import { absoluteTime } from '@/lib/time'
import { tg } from '@/i18n/manage'

function Card({ label, value, help, slot }: { label: string; value: string; help?: string; slot: string }) {
  return (
    <div className="grid content-start gap-0.5 rounded-lg border p-3" data-slot="summary-card" data-card={slot}>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="text-sm font-medium break-words tabular-nums">{value}</dd>
      {help && <p className="text-xs text-muted-foreground">{help}</p>}
    </div>
  )
}

// What the server holds, from the stored measurements: the top of storage management. A tenant administrator gets
// their tenant's numbers and not the disk's.
export function StorageSummary() {
  const q = useAdminStorage()
  const s = q.data?.summary
  return (
    <SettingsSection title={tg.summary.title} id="manage-summary-heading" description={tg.summary.help}>
      {q.isPending ? (
        <PageFallback />
      ) : q.isError || !s ? (
        <ErrorState message={tg.summary.loadFailed} onRetry={() => void q.refetch()} />
      ) : (
        <>
          <dl className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3" data-slot="storage-summary">
            <Card
              slot="spaces"
              label={tg.summary.spaces}
              value={tg.summary.spacesValue(
                s.spaces.local + s.spaces.full + s.spaces.partial + s.spaces.evicted + s.spaces.archived,
              )}
              help={tg.summary.spacesBreak(
                s.spaces.local,
                s.spaces.full,
                s.spaces.partial,
                s.spaces.evicted,
                s.spaces.archived,
              )}
            />
            <Card slot="people" label={tg.summary.people} value={tg.summary.peopleValue(s.people, s.dormant)} />
            <Card slot="held" label={tg.summary.held} value={formatBytes(s.heldBytes)} help={tg.summary.heldHelp} />
            <Card slot="usage" label={tg.summary.usage} value={formatBytes(s.usageBytes)} help={tg.summary.usageHelp} />
            <Card slot="attachments" label={tg.summary.attachments} value={formatBytes(s.assetCacheBytes)} />
            <Card
              slot="recycle"
              label={tg.summary.recycle}
              value={tg.summary.recycleValue(s.recycle.items, formatBytes(s.recycle.bytes))}
            />
            <Card
              slot="exports"
              label={tg.summary.exports}
              value={tg.summary.exportsValue(s.exports.items, formatBytes(s.exports.bytes))}
            />
            <Card
              slot="remotes"
              label={tg.summary.remotes}
              value={tg.summary.remotesValue(s.remotes.inGrace, s.remotes.overdue)}
            />
            {s.diskFreeBytes !== undefined && (
              <Card slot="disk" label={tg.summary.disk} value={formatBytes(s.diskFreeBytes)} />
            )}
          </dl>
          <p className="text-xs text-muted-foreground">
            {s.measuredAt ? tg.summary.measured(absoluteTime(s.measuredAt)) : tg.summary.notMeasured}
          </p>
          <p className="rounded-lg border p-3 text-xs text-muted-foreground" data-slot="where-explain">
            {tg.where.explain}
          </p>
        </>
      )}
    </SettingsSection>
  )
}
