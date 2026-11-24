import { useState } from 'react'
import { toast } from 'sonner'
import { X } from 'lucide-react'
import { useActiveSpace, useSession, useTags, useTree } from '@/api/hooks'
import { ErrorState, PageFallback } from '@/components/common'
import { Button } from '@/components/ui/button'
import { NativeSelect } from '@/components/ui/native-select'
import { formatBytes } from '@/lib/bytes'
import { ol } from '@/i18n/offline'
import {
  DEFAULT_CAP_BYTES,
  DEFAULT_CONFIG,
  RECENT_KEPT,
  configure,
  dropCopy,
  syncNow,
  type OfflineConfig,
  type OfflineMode,
} from '@/lib/offline'
import { useOfflineAllowed } from '@/lib/offline/allowed'
import { useOffline } from '@/lib/offline/status'
import { useCopy } from '@/lib/offline/useCopy'
import { SettingsNote, SettingsSection } from './SettingsParts'

// The "Offline" section: what this space keeps on this device, so that it can be browsed, read and searched without a
// network (milestone 4.1). Per device and per account; nothing here goes to the server. A chunk of its own, loaded
// when the section is opened.

const MODES: OfflineMode[] = ['catalog', 'text']

export default function OfflinePanel({ space }: { space: string }) {
  const session = useSession().data
  const user = session?.user?.id
  const { info, syncing, reload, failed } = useCopy(user, space)
  const tree = useTree()
  const tags = useTags()
  const offline = useOffline()
  const { spaces } = useActiveSpace()
  const [busy, setBusy] = useState(false)
  const allowed = useOfflineAllowed(user)
  // The administrators turned it off: no controls, only the reason (the app deletes this account's copies).
  if (allowed === false)
    return (
      <SettingsSection title={ol.title} description={ol.help}>
        <p className="rounded-lg border bg-muted/40 p-3 text-sm text-muted-foreground" data-slot="offline-off">
          {ol.offByAdmin}
        </p>
      </SettingsSection>
    )
  if (typeof indexedDB === 'undefined') return <SettingsNote>{ol.unsupported}</SettingsNote>
  if (failed) return <ErrorState message={ol.loadFailed} onRetry={reload} />
  if (!user || !info) return <PageFallback />

  const inbox = spaces.find((s) => s.id === space)?.inbox ?? ''
  const mode: 'off' | OfflineMode = info.config?.mode ?? 'off'
  const cfg: OfflineConfig = info.config ?? DEFAULT_CONFIG

  // Writes the choice, then syncs at once (what was chosen is fetched now, not at the next minute).
  const choose = async (next: OfflineConfig | null) => {
    if (busy) return
    setBusy(true)
    try {
      await configure(user, space, next)
      reload()
      if (next) void syncNow(user, space).finally(reload)
      else toast.success(ol.cleared)
    } catch {
      toast.error(ol.failed)
    } finally {
      setBusy(false)
    }
  }
  const change = (patch: Partial<OfflineConfig>) => void choose({ ...cfg, ...patch })

  const folderChoices = (tree.data?.folders ?? [])
    .map((f) => f.path)
    .filter((p) => p !== '' && !cfg.folders.includes(p))
  const tagChoices = (tags.data?.tags ?? []).map((x) => x.tag).filter((x) => !cfg.tags.includes(x))
  const state = info.state
  const when = state && state.syncedAt > 0 ? new Date(state.syncedAt).toLocaleString() : ''

  return (
    <div className="space-y-8">
      <SettingsSection title={ol.title} description={ol.help}>
        <p className="rounded-lg border bg-muted/40 p-3 text-xs text-muted-foreground" data-slot="offline-unencrypted">
          {session?.mode === 'accounts' ? ol.unencrypted : ol.unencryptedSingle}
        </p>
        <div role="radiogroup" aria-label={ol.modeLabel} className="grid gap-2 rounded-lg border p-4">
          {(['off', ...MODES] as const).map((m) => (
            <label key={m} className="flex items-start gap-2 text-sm">
              <input
                type="radio"
                name="offline-mode"
                className="mt-1"
                checked={mode === m}
                disabled={busy}
                onChange={() => (m === 'off' ? void choose(null) : change({ mode: m }))}
              />
              <span>
                <span className="font-medium">{ol.modes[m]}</span>
                <span className="block text-xs text-muted-foreground">{ol.modeHelp[m]}</span>
              </span>
            </label>
          ))}
        </div>
      </SettingsSection>

      {mode === 'text' && (
        <SettingsSection title={ol.scopeTitle} description={ol.scopeHelp(formatBytes(DEFAULT_CAP_BYTES))}>
          <div className="grid gap-2 rounded-lg border p-4">
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={cfg.favourites}
                disabled={busy}
                onChange={(e) => change({ favourites: e.target.checked })}
              />
              {ol.favourites}
            </label>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={cfg.inbox}
                disabled={busy}
                onChange={(e) => change({ inbox: e.target.checked })}
              />
              {inbox ? ol.inboxNamed(inbox) : ol.inbox}
            </label>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={cfg.recent}
                disabled={busy}
                onChange={(e) => change({ recent: e.target.checked })}
              />
              {ol.recent(RECENT_KEPT)}
            </label>
          </div>
          <Chosen
            title={ol.folders}
            items={cfg.folders}
            choices={folderChoices}
            addLabel={ol.addFolder}
            disabled={busy}
            onChange={(folders) => change({ folders })}
          />
          <Chosen
            title={ol.tags}
            items={cfg.tags}
            choices={tagChoices}
            addLabel={ol.addTag}
            disabled={busy}
            onChange={(t) => change({ tags: t })}
          />
        </SettingsSection>
      )}

      {mode !== 'off' && state && (
        <SettingsSection title={ol.statusTitle}>
          <div className="grid gap-1.5 rounded-lg border p-4 text-sm" data-slot="offline-status">
            <p>{state.complete && when ? ol.syncedAt(when) : ol.neverSynced}</p>
            <p>{ol.catalogCount(info.catalog)}</p>
            {mode === 'text' && <p>{ol.textCount(info.texts)}</p>}
            <p>{ol.usedHere(formatBytes(info.bytes))}</p>
            <p>{ol.usedDevice(formatBytes(info.deviceBytes), formatBytes(DEFAULT_CAP_BYTES))}</p>
            <div
              className="h-1.5 overflow-hidden rounded-full bg-muted"
              role="progressbar"
              aria-valuemin={0}
              aria-valuemax={DEFAULT_CAP_BYTES}
              aria-valuenow={Math.min(info.deviceBytes, DEFAULT_CAP_BYTES)}
              aria-label={ol.usedDevice(formatBytes(info.deviceBytes), formatBytes(DEFAULT_CAP_BYTES))}
            >
              <div
                className="h-full bg-brand"
                style={{ width: `${Math.min(100, (info.deviceBytes / DEFAULT_CAP_BYTES) * 100)}%` }}
              />
            </div>
            {state.overCap && <p className="text-amber-600 dark:text-amber-400">{ol.overCap}</p>}
            {state.disabled && <p className="text-amber-600 dark:text-amber-400">{ol.disabled}</p>}
            {state.error && <p className="text-destructive">{ol.lastError(state.error)}</p>}
            {offline && <p className="text-muted-foreground">{ol.offlineNow}</p>}
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              size="sm"
              disabled={syncing || busy || offline}
              onClick={() => void syncNow(user, space).finally(reload)}
            >
              {syncing ? ol.syncing : ol.syncNow}
            </Button>
            <Button
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={() => void dropCopy(user, space).then(() => (reload(), toast.success(ol.cleared)))}
            >
              {ol.clear}
            </Button>
          </div>
        </SettingsSection>
      )}
    </div>
  )
}

// A list of chosen folders or tags, each with a button to take it out, and a box to add another.
function Chosen({
  title,
  items,
  choices,
  addLabel,
  disabled,
  onChange,
}: {
  title: string
  items: string[]
  choices: string[]
  addLabel: string
  disabled: boolean
  onChange: (items: string[]) => void
}) {
  return (
    <div className="grid gap-2 rounded-lg border p-4">
      <div className="text-sm font-medium">{title}</div>
      {items.length === 0 ? (
        <p className="text-xs text-muted-foreground">{ol.none}</p>
      ) : (
        <ul className="flex flex-wrap gap-1.5">
          {items.map((it) => (
            <li
              key={it}
              className="inline-flex max-w-full items-center gap-1 rounded-md border bg-muted/40 py-0.5 pr-0.5 pl-2 text-xs"
            >
              <span className="truncate">{it}</span>
              <button
                type="button"
                disabled={disabled}
                aria-label={ol.remove(it)}
                onClick={() => onChange(items.filter((x) => x !== it))}
                className="inline-flex size-5 items-center justify-center rounded hover:bg-muted"
              >
                <X className="size-3" />
              </button>
            </li>
          ))}
        </ul>
      )}
      {choices.length > 0 && (
        <NativeSelect
          aria-label={addLabel}
          className="h-8 max-w-full text-sm sm:w-64"
          value=""
          disabled={disabled}
          onChange={(e) => {
            if (e.target.value) onChange([...items, e.target.value])
          }}
        >
          <option value="">{addLabel}</option>
          {choices.map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </NativeSelect>
      )}
    </div>
  )
}
