import { useState } from 'react'
import { Link, useLocation } from 'react-router'
import { TriangleAlert, X } from 'lucide-react'
import { useSpaceId, useSpacePermissions, useTypes } from '@/api/hooks'
import type { SettingsNotice } from '@/api/types'
import { settingsPath } from '@/components/settings/sections'
import type { SettingsState } from '@/components/settings/useSettings'
import { t } from '@/i18n'

// A line above the pages of a space, for those who manage it, while a settings file of the space cannot be read or
// lost entries: the details are in the settings (Types). It can be put away; a notice that says something new shows it
// again. Others never see it (the server tells only those who manage the space).

const alarming = (n: SettingsNotice): boolean => n.unreadable || (n.lost?.length ?? 0) > 0

const dismissKey = (space: string) => `pb.settingsNotice.${space}`
// What the line is about: once put away, it stays away until this changes.
const noticeSig = (list: readonly SettingsNotice[]): string =>
  JSON.stringify(
    list
      .filter(alarming)
      .map((n) => [n.file, n.entry ?? '', n.unreadable, n.since?.commit ?? n.since?.time ?? '', n.lost ?? []]),
  )

function readDismissed(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

export function SettingsNoticeBanner() {
  const space = useSpaceId()
  const location = useLocation()
  const { manage } = useSpacePermissions()
  const types = useTypes(manage)
  const key = dismissKey(space)
  const [dismissed, setDismissed] = useState(() => ({ key, sig: readDismissed(key) }))
  const gone = dismissed.key === key ? dismissed.sig : readDismissed(key)
  if (!manage) return null
  const list = (types.data?.settingsNotices ?? []).filter(alarming)
  if (list.length === 0) return null
  const sig = noticeSig(list)
  if (gone === sig) return null
  const dismiss = () => {
    try {
      localStorage.setItem(key, sig)
    } catch {
      // Storage blocked: put away until the page is left.
    }
    setDismissed({ key, sig })
  }
  return (
    <div
      role="status"
      data-slot="settings-notice"
      className="flex items-start gap-2 border-b border-amber-500/30 bg-amber-500/10 px-4 py-2 text-sm text-amber-900 dark:text-amber-200"
    >
      <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
      <p className="min-w-0 flex-1">
        {list.some((n) => n.unreadable) ? t.settingsNotice.unreadable : t.settingsNotice.lost}{' '}
        <Link
          to={settingsPath('types')}
          state={{ from: location.pathname + location.search + location.hash } satisfies SettingsState}
          className="font-medium underline underline-offset-2"
        >
          {t.settingsNotice.details}
        </Link>
      </p>
      <button
        type="button"
        onClick={dismiss}
        aria-label={t.settingsNotice.dismiss}
        title={t.settingsNotice.dismiss}
        className="-m-1 rounded p-1 outline-none hover:bg-amber-500/20 focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        <X className="size-4" />
      </button>
    </div>
  )
}
