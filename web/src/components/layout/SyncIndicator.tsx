import { Link } from 'react-router'
import { CircleAlert, CirclePause, Cloud, CloudOff } from 'lucide-react'
import type { SyncState } from '@/api/types'
import { spaceUrls } from '@/lib/urls'
import { t } from '@/i18n/zh-CN'

// Where a space stands with its remote, next to the space's name: nothing for a
// space that is only on the server, a quiet cloud while all is well, and a
// word with an icon when something needs attention. Each links to the settings.
export function SyncIndicator({ space, sync }: { space: string; sync: SyncState }) {
  if (sync === 'local') return null
  const label = sync === 'ok' ? t.sync.ok : t.sync[sync]
  const Icon = sync === 'ok' ? Cloud : sync === 'offline' ? CloudOff : sync === 'rewritten' ? CirclePause : CircleAlert
  const attention = sync !== 'ok'
  return (
    <Link
      to={spaceUrls(space).settings()}
      title={`${label}。${t.sync.open}`}
      aria-label={`${t.sync.label}：${label}。${t.sync.open}`}
      className={`flex shrink-0 items-center gap-1 rounded-md px-1.5 py-1 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring/50 ${attention ? (sync === 'error' ? 'text-destructive hover:bg-destructive/10' : 'text-warning hover:bg-warning/10') : 'text-muted-foreground hover:bg-sidebar-accent'}`}
    >
      <Icon className="size-4" />
      {attention && <span>{t.sync.short[sync]}</span>}
    </Link>
  )
}
