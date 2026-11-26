import { useState, type ReactNode } from 'react'
import { ApiError } from '@/api/client'
import { USER_SEARCH_LIMIT, useUserSearch } from '@/api/admin'
import { useSession } from '@/api/hooks'
import { acrossSite } from '@/lib/access'
import type { SpaceLocation, User } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { t } from '@/i18n'
import { tg } from '@/i18n/manage'
import { useDebounced } from '@/lib/useDebounced'

// A site administrator sees every tenant and the server's own state; a tenant administrator the tenant's only.
export const useIsSite = (): boolean => acrossSite(useSession().data?.user)

// What a refusal of one of the storage management calls says, in plain words (the server's words when it has no
// sentence here).
export function manageError(e: unknown, fallback = tg.failed): string {
  if (!(e instanceof ApiError)) return fallback
  const codes = tg.errors as Record<string, string>
  return codes[e.code] ?? (e.message || fallback)
}

// A dialog of storage management fits a phone screen: never taller than the screen (the dynamic height, so a
// browser's own bars do not hide its top or its buttons), scrolled inside, and a long name or address wraps rather
// than widening it. A long title stops short of the close button rather than running under it.
export const fitScreen =
  'max-h-[calc(100dvh-2rem)] overflow-y-auto overscroll-contain wrap-anywhere [&_[data-slot=dialog-title]]:pr-6'

// A main action is at least 40px tall for a finger: on a narrow screen and on any touch screen.
export const tapTarget = 'max-md:h-10 pointer-coarse:h-10'

export const daysLeft = (iso: string, now = Date.now()): number =>
  Math.max(0, Math.ceil((Date.parse(iso) - now) / 86_400_000))

// Where the bytes of a space live, and whether they count toward quotas, in plain words.
export function LocationBadge({ location, counted }: { location: SpaceLocation; counted: boolean }) {
  return (
    <div className="grid gap-0.5" data-slot="space-location" data-location={location} data-counted={counted}>
      <span title={tg.where.help[location]}>{tg.where.names[location]}</span>
      <Badge
        variant={counted ? 'secondary' : 'outline'}
        className="w-fit"
        title={counted ? tg.where.countedHelp : tg.where.notCountedHelp}
      >
        {counted ? tg.where.counted : tg.where.notCounted}
      </Badge>
    </div>
  )
}

// Looks for a person on the server, a few at a time, and picks one of them (an heir, a new owner).
export function UserPicker({
  label,
  value,
  onChange,
  exclude = [],
}: {
  label: string
  value: User | null
  onChange: (u: User | null) => void
  exclude?: string[]
}) {
  const [term, setTerm] = useState('')
  const found = useUserSearch(useDebounced(term.trim(), 250))
  const matches = (found.data?.users ?? []).filter((u) => !exclude.includes(u.id))
  const choices = value && !matches.some((u) => u.id === value.id) ? [value, ...matches] : matches
  const truncated = (found.data?.users.length ?? 0) >= USER_SEARCH_LIMIT
  return (
    <div className="grid gap-1.5" data-slot="user-picker">
      <Label>{label}</Label>
      <Input
        type="search"
        value={term}
        onChange={(e) => setTerm(e.target.value)}
        placeholder={tg.picker.search}
        aria-label={`${label}: ${tg.picker.search}`}
        autoComplete="off"
        spellCheck={false}
      />
      <NativeSelect
        aria-label={label}
        value={value?.id ?? ''}
        onChange={(e) => onChange(choices.find((u) => u.id === e.target.value) ?? null)}
        disabled={found.isPending}
      >
        <option value="">{tg.picker.choose}</option>
        {choices.map((u) => (
          <option key={u.id} value={u.id}>
            {u.name} ({u.username})
          </option>
        ))}
      </NativeSelect>
      {!found.isPending && (
        <p className="text-xs text-muted-foreground">
          {matches.length === 0 ? tg.picker.none : truncated ? tg.picker.narrow(USER_SEARCH_LIMIT) : ''}
        </p>
      )}
    </div>
  )
}

// The two lines every list ends with.
export function ListFooter({
  shown,
  more,
  loading,
  onMore,
  error,
}: {
  shown: number
  more: boolean
  loading: boolean
  onMore: () => void
  error?: ReactNode
}) {
  return (
    <div className="flex flex-col items-center gap-2">
      {more && (
        <button
          type="button"
          disabled={loading}
          onClick={onMore}
          className={`h-8 rounded-lg border px-3 text-sm ${tapTarget} outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-50`}
        >
          {loading ? tg.loading : tg.loadMore}
        </button>
      )}
      <span className="text-xs text-muted-foreground">{more ? tg.loaded(shown) : tg.allLoaded(shown)}</span>
      {error}
    </div>
  )
}

export const closeText = t.common.close
