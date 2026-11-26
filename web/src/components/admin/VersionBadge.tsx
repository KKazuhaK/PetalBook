import { Info } from 'lucide-react'
import { useAdminStatus } from '@/api/admin'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { zonedTime } from '@/lib/time'
import { tc } from '@/i18n/console'

const tv = tc.version

// Which build this server is: a small button at the foot of the administration's sidebar, and a popover with the version,
// the commit and when it was built (or committed: a build that injected no time of its own has only the commit's), in
// the viewer's own time. Only the administrators of the whole server may read it (it comes from the status).
export function VersionBadge({ site, collapsed }: { site: boolean; collapsed: boolean }) {
  const status = useAdminStatus(site, 10 * 60_000)
  const s = status.data
  if (!site || !s) return null
  const version = /^\d/.test(s.version) ? `v${s.version}` : s.version
  const { build } = s
  const rows: [string, string][] = [
    [tv.version, s.version],
    [tv.commit, build.commit ? (build.modified ? `${build.commit} (${tv.modified})` : build.commit) : tv.unknown],
    [build.timeKind === 'build' ? tv.buildTime : tv.commitTime, build.time ? zonedTime(build.time) : tv.unknown],
  ]
  return (
    <Popover>
      <PopoverTrigger
        data-slot="version-badge"
        aria-label={tv.label(s.version)}
        title={collapsed ? tv.label(s.version) : undefined}
        className={`flex h-10 items-center rounded-md text-xs text-muted-foreground outline-none transition-colors hover:bg-sidebar-accent/60 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 data-popup-open:bg-sidebar-accent/60 ${collapsed ? 'mx-auto w-10 justify-center' : 'w-full gap-2 px-3'}`}
      >
        <Info className="size-4 shrink-0" aria-hidden="true" strokeWidth={1.75} />
        {!collapsed && <span className="truncate tabular-nums">{version}</span>}
      </PopoverTrigger>
      <PopoverContent side="top" align="start" className="w-[min(20rem,calc(100vw-2rem))]">
        <p className="font-medium">{tv.title}</p>
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-xs">
          {rows.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-muted-foreground">{k}</dt>
              <dd className="min-w-0 break-words">{v}</dd>
            </div>
          ))}
        </dl>
      </PopoverContent>
    </Popover>
  )
}
