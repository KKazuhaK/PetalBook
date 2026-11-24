import { useState } from 'react'
import { Link, useLocation } from 'react-router'
import { Cloud, X } from 'lucide-react'
import { useConnections, useSpaceId, useSpacePermissions, useSpaces } from '@/api/hooks'
import { settingsPath } from '@/components/settings/sections'
import type { SettingsState } from '@/components/settings/useSettings'
import { absoluteTime } from '@/lib/time'
import { ts } from '@/i18n/storage'

// A line above the pages of a space that the storage policy asks to follow a
// remote repository and that does not yet (docs/api.md, "Server storage"): it
// says by when, or that the space is read only now. Everyone in the space sees
// it; those who manage it are shown the way to connect. A notice in its grace
// period can be put away for the day; an overdue one stays, because it
// explains why saving fails.
const dayKey = (space: string) => `pb.storageNotice.${space}`
const today = () => new Date().toISOString().slice(0, 10)

function readDismissed(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

export function StorageNoticeBanner() {
  const space = useSpaceId()
  const location = useLocation()
  const { manage } = useSpacePermissions()
  const spaces = useSpaces()
  const key = dayKey(space)
  const [dismissed, setDismissed] = useState(() => ({ key, day: readDismissed(key) }))
  const gone = (dismissed.key === key ? dismissed.day : readDismissed(key)) === today()
  const note = spaces.data?.spaces.find((s) => s.id === space)?.storage?.remote
  // A requirement of a provider (2.7b's connections) is shown by the platform's name.
  const byProvider = !!note && note.required !== 'any'
  const connections = useConnections(byProvider)
  if (!note) return null
  const providerName = connections.data?.providers.find((p) => p.id === note.required)?.name ?? note.required
  const when = absoluteTime(note.deadline)
  const overdue = note.overdue
  if (!overdue && gone) return null
  const dismiss = () => {
    try {
      localStorage.setItem(key, today())
    } catch {
      // Storage blocked: put away until the page is left.
    }
    setDismissed({ key, day: today() })
  }
  const tone = overdue ? 'border-destructive/30 bg-destructive/10 text-destructive' : 'border-amber-500/30 bg-amber-500/10 text-amber-900 dark:text-amber-200'
  return (
    <div role="status" data-slot="storage-notice" data-overdue={overdue} className={`flex items-start gap-2 border-b px-4 py-2 text-sm ${tone}`}>
      <Cloud className="mt-0.5 size-4 shrink-0" aria-hidden />
      <p className="min-w-0 flex-1 break-words">
        <span className="font-medium">{overdue ? ts.notice.overdueTitle : ts.notice.graceTitle}.</span> {overdue ? ts.notice.overdueHelp(when) : ts.notice.graceHelp(when)}
        {note.required !== 'any' && <> {ts.notice.providerHelp(providerName)}</>}{' '}
        {manage && (
          <Link to={settingsPath('sync')} state={{ from: location.pathname + location.search + location.hash } satisfies SettingsState} className="font-medium underline underline-offset-2">
            {ts.notice.connect}
          </Link>
        )}
      </p>
      {!overdue && (
        <button type="button" onClick={dismiss} aria-label={ts.notice.dismiss} title={ts.notice.dismiss} className="-m-1 rounded p-1 outline-none hover:bg-amber-500/20 focus-visible:ring-2 focus-visible:ring-ring/50">
          <X className="size-4" />
        </button>
      )}
    </div>
  )
}
