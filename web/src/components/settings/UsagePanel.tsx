import { useAccountUsage } from '@/api/hooks'
import type { AccountUsage, TenantQuotaKey, UsageNotice } from '@/api/types'
import { ErrorState, PageFallback } from '@/components/common'
import { formatLimit, formatUsed, share } from '@/lib/usage'
import { absoluteTime } from '@/lib/time'
import { tu } from '@/i18n/usage'
import { SettingsSection } from './SettingsParts'

// The caller's own usage (accounts mode): spaces, disk and pages against their limits. The numbers are the ones
// the server measured in the background; a person over a limit is told, never stopped, except at the number of spaces.
export default function UsagePanel() {
  const usage = useAccountUsage()
  if (usage.isPending) return <PageFallback />
  if (usage.isError) return <ErrorState message={tu.account.loadFailed} onRetry={() => void usage.refetch()} />
  const u = usage.data
  return (
    <div className="grid w-full min-w-0 grid-cols-1 gap-8">
      <SettingsSection title={tu.account.title} description={tu.account.help} id="usage-heading">
        <ul className="grid gap-4" data-slot="usage-meters">
          {(['spaces', 'bytes', 'pages'] as const).map((k) => (
            <Meter
              key={k}
              kind={k}
              used={u[k].used}
              limit={u[k].limit}
              hint={k === 'spaces' ? tu.account.spacesHelp : undefined}
            />
          ))}
        </ul>
        <p className="text-xs text-muted-foreground" data-slot="usage-measured">
          {u.measuredAt ? tu.measuredAt(absoluteTime(u.measuredAt)) : tu.neverMeasured}
        </p>
        <p className="text-xs text-muted-foreground">{tu.measuredHelp}</p>
      </SettingsSection>
      {u.notices.length > 0 && <Notices usage={u} />}
    </div>
  )
}

function Meter({
  kind,
  used,
  limit,
  hint,
}: {
  kind: 'spaces' | 'bytes' | 'pages'
  used: number
  limit: number | null
  hint?: string
}) {
  const part = share(used, limit)
  const over = part !== null && part > 1
  const what = tu.quotas[kind]
  return (
    <li
      className="grid min-w-0 gap-1.5"
      data-slot="usage-meter"
      data-quota={kind}
      data-over={over ? 'true' : undefined}
    >
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-0.5">
        <span className="text-sm font-medium">{what}</span>
        <span className="text-sm tabular-nums break-words">
          {tu.account.used(formatUsed(kind, used), formatLimit(kind, limit))}
        </span>
      </div>
      {part !== null && (
        <div
          role="progressbar"
          aria-label={tu.account.barLabel(what, formatUsed(kind, used), formatLimit(kind, limit))}
          aria-valuemin={0}
          aria-valuemax={limit ?? 0}
          aria-valuenow={Math.min(used, limit ?? 0)}
          className="h-2 overflow-hidden rounded-full bg-muted"
        >
          <div
            className={`h-full rounded-full ${over ? 'bg-destructive' : 'bg-brand'}`}
            style={{ width: `${Math.min(100, Math.round(part * 100))}%` }}
          />
        </div>
      )}
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </li>
  )
}

const named = (n: UsageNotice) => tu.quotas[n.quota as TenantQuotaKey]

function Notices({ usage }: { usage: AccountUsage }) {
  return (
    <SettingsSection title={tu.account.notices.title} id="usage-notices-heading" description={tu.account.overHelp}>
      <ul className="grid gap-2" role="status" data-slot="usage-notices">
        {usage.notices.map((n) => (
          <li
            key={`${n.scope}-${n.quota}`}
            className="rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm break-words"
            data-notice={`${n.scope}-${n.quota}`}
          >
            {(n.scope === 'tenant' ? tu.account.notices.tenant : tu.account.notices.person)(
              named(n),
              formatUsed(n.quota, n.used),
              formatUsed(n.quota, n.limit),
            )}
          </li>
        ))}
      </ul>
    </SettingsSection>
  )
}
