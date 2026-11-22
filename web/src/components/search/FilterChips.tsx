import { useMemo, useState, type ReactNode } from 'react'
import { Calendar, Check, ChevronDown, Folder, Tag as TagIcon, Type, X } from 'lucide-react'
import { useTags, useTree } from '@/api/hooks'
import { TagChip } from '@/components/TagChip'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { t } from '@/i18n'
import { type DateFilter, type DatePreset, type SearchFilters } from './searchModel'

// The row of chips under the search box. Each one either toggles (title only)
// or opens a small panel to choose from; a chip with a value says it and has
// its own button to clear it.

const chipBase = 'inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50'
const chipOff = 'text-muted-foreground hover:bg-muted hover:text-foreground'
const chipOn = 'border-ring/50 bg-accent text-accent-foreground'

function Chip({ icon, label, value, onClear, children }: { icon: ReactNode; label: string; value?: string; onClear: () => void; children: (close: () => void) => ReactNode }) {
  const [open, setOpen] = useState(false)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <span className="inline-flex items-center">
        <PopoverTrigger render={<button type="button" className={`${chipBase} ${value ? `${chipOn} rounded-r-none border-r-0` : chipOff}`} />}>
          {icon}
          <span>{label}</span>
          {value && <span className="max-w-32 truncate font-medium">{value}</span>}
          {!value && <ChevronDown className="size-3 opacity-60" />}
        </PopoverTrigger>
        {value && (
          <button type="button" aria-label={t.search.clear(label)} onClick={onClear} className={`${chipBase} ${chipOn} rounded-l-none px-1.5`}>
            <X className="size-3" />
          </button>
        )}
      </span>
      <PopoverContent align="start" className="w-64 gap-2 p-2">
        {children(() => setOpen(false))}
      </PopoverContent>
    </Popover>
  )
}

function FilterInput({ value, onChange, placeholder }: { value: string; onChange: (v: string) => void; placeholder: string }) {
  return (
    <input
      type="search"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      placeholder={placeholder}
      aria-label={placeholder}
      className="h-8 w-full rounded-md border border-input bg-background px-2 text-sm outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/40"
    />
  )
}

function FolderPicker({ value, onPick }: { value: string | null; onPick: (path: string) => void }) {
  const { data, isPending, isError } = useTree()
  const [filter, setFilter] = useState('')
  const folders = useMemo(() => {
    const needle = filter.trim().toLowerCase()
    return (data?.folders ?? []).filter((f) => f.path !== '' && (!needle || f.path.toLowerCase().includes(needle)))
  }, [data, filter])
  return (
    <>
      <FilterInput value={filter} onChange={setFilter} placeholder={t.search.filterFolders} />
      <div role="listbox" aria-label={t.search.chips.folder} className="max-h-60 overflow-y-auto overscroll-contain">
        {isPending ? (
          <p className="px-2 py-3 text-xs text-muted-foreground">{t.search.loading}</p>
        ) : isError ? (
          <p className="px-2 py-3 text-xs text-destructive">{t.search.failed}</p>
        ) : folders.length === 0 ? (
          <p className="px-2 py-3 text-xs text-muted-foreground">{t.search.noFolders}</p>
        ) : (
          folders.map((f) => {
            const parts = f.path.split('/')
            return (
              <button
                key={f.path}
                type="button"
                role="option"
                aria-selected={value === f.path}
                onClick={() => onPick(f.path)}
                className="flex w-full items-center gap-1.5 rounded-md py-1 pr-2 text-left text-sm outline-none hover:bg-accent focus-visible:bg-accent"
                style={{ paddingLeft: 8 + (parts.length - 1) * 12 }}
              >
                <Folder className="size-3.5 shrink-0 text-muted-foreground" />
                <span className="truncate">{parts[parts.length - 1]}</span>
                <span className="ml-auto text-xs text-muted-foreground tabular-nums">{f.pages}</span>
                {value === f.path && <Check className="size-3.5" />}
              </button>
            )
          })
        )}
      </div>
    </>
  )
}

function TagPicker({ value, onToggle }: { value: string[]; onToggle: (tag: string) => void }) {
  const { data, isPending, isError } = useTags()
  const [filter, setFilter] = useState('')
  const tags = useMemo(() => {
    const needle = filter.trim().toLowerCase()
    return (data?.tags ?? []).filter((x) => !needle || x.tag.toLowerCase().includes(needle))
  }, [data, filter])
  return (
    <>
      <FilterInput value={filter} onChange={setFilter} placeholder={t.search.filterTags} />
      <div role="listbox" aria-multiselectable="true" aria-label={t.search.chips.tags} className="max-h-60 overflow-y-auto overscroll-contain">
        {isPending ? (
          <p className="px-2 py-3 text-xs text-muted-foreground">{t.search.loading}</p>
        ) : isError ? (
          <p className="px-2 py-3 text-xs text-destructive">{t.search.failed}</p>
        ) : tags.length === 0 ? (
          <p className="px-2 py-3 text-xs text-muted-foreground">{t.search.noTags}</p>
        ) : (
          tags.map((x) => (
            <button key={x.tag} type="button" role="option" aria-selected={value.includes(x.tag)} onClick={() => onToggle(x.tag)} className="flex w-full items-center gap-2 rounded-md px-2 py-1 text-left text-sm outline-none hover:bg-accent focus-visible:bg-accent">
              <TagChip tag={x.tag} color={x.color ?? null} />
              <span className="ml-auto text-xs text-muted-foreground tabular-nums">{x.pages}</span>
              {value.includes(x.tag) && <Check className="size-3.5" />}
            </button>
          ))
        )}
      </div>
    </>
  )
}

const PRESETS: DatePreset[] = ['today', 'week', 'month']

export function dateFilterLabel(f: DateFilter | null): string | undefined {
  if (!f) return undefined
  if (f.kind === 'preset') return t.search.presets[f.preset]
  if (f.from && f.to) return `${f.from} – ${f.to}`
  if (f.from) return t.search.fromDay(f.from)
  if (f.to) return t.search.toDay(f.to)
  return t.search.presets.custom
}

function DatePicker({ value, onPick }: { value: DateFilter | null; onPick: (f: DateFilter) => void }) {
  const [from, setFrom] = useState(value?.kind === 'custom' ? (value.from ?? '') : '')
  const [to, setTo] = useState(value?.kind === 'custom' ? (value.to ?? '') : '')
  const valid = (from !== '' || to !== '') && (from === '' || to === '' || from <= to)
  return (
    <>
      <div role="group" aria-label={t.search.presetsLabel} className="grid gap-0.5">
        {PRESETS.map((p) => {
          const on = value?.kind === 'preset' && value.preset === p
          return (
            <button key={p} type="button" aria-pressed={on} onClick={() => onPick({ kind: 'preset', preset: p })} className="flex items-center justify-between rounded-md px-2 py-1.5 text-left text-sm outline-none hover:bg-accent focus-visible:bg-accent">
              {t.search.presets[p]}
              {on && <Check className="size-3.5" />}
            </button>
          )
        })}
      </div>
      <div className="grid gap-2 border-t pt-2">
        <div className="px-0.5 text-xs font-medium text-muted-foreground">{t.search.presets.custom}</div>
        <div className="grid grid-cols-2 gap-2">
          <label className="grid gap-1 text-xs text-muted-foreground">
            {t.search.from}
            <input type="date" value={from} max={to || undefined} onChange={(e) => setFrom(e.target.value)} className="h-8 rounded-md border border-input bg-background px-1.5 text-sm text-foreground outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/40" />
          </label>
          <label className="grid gap-1 text-xs text-muted-foreground">
            {t.search.to}
            <input type="date" value={to} min={from || undefined} onChange={(e) => setTo(e.target.value)} className="h-8 rounded-md border border-input bg-background px-1.5 text-sm text-foreground outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/40" />
          </label>
        </div>
        <button
          type="button"
          disabled={!valid}
          onClick={() => onPick({ kind: 'custom', ...(from ? { from } : {}), ...(to ? { to } : {}) })}
          className="h-8 rounded-md bg-primary text-sm font-medium text-primary-foreground outline-none hover:bg-primary/90 focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-50"
        >
          {t.search.apply}
        </button>
      </div>
    </>
  )
}

export function FilterChips({ filters, onChange }: { filters: SearchFilters; onChange: (next: SearchFilters) => void }) {
  const set = (patch: Partial<SearchFilters>) => onChange({ ...filters, ...patch })
  const folderName = filters.folder === null ? undefined : filters.folder
  return (
    <div role="group" aria-label={t.search.filters} className="flex flex-wrap items-center gap-1.5">
      <button type="button" aria-pressed={filters.titleOnly} onClick={() => set({ titleOnly: !filters.titleOnly })} className={`${chipBase} ${filters.titleOnly ? chipOn : chipOff}`}>
        <Type className="size-3" />
        {t.search.chips.titleOnly}
        {filters.titleOnly && <Check className="size-3" />}
      </button>
      <Chip icon={<Folder className="size-3" />} label={t.search.chips.folder} value={folderName} onClear={() => set({ folder: null })}>
        {(close) => (
          <FolderPicker
            value={filters.folder}
            onPick={(path) => {
              set({ folder: path })
              close()
            }}
          />
        )}
      </Chip>
      <Chip icon={<TagIcon className="size-3" />} label={t.search.chips.tags} value={filters.tags.length ? (filters.tags.length === 1 ? filters.tags[0] : t.search.tagCount(filters.tags.length)) : undefined} onClear={() => set({ tags: [] })}>
        {() => <TagPicker value={filters.tags} onToggle={(tag) => set({ tags: filters.tags.includes(tag) ? filters.tags.filter((x) => x !== tag) : [...filters.tags, tag] })} />}
      </Chip>
      <Chip icon={<Calendar className="size-3" />} label={t.search.chips.updated} value={dateFilterLabel(filters.updated)} onClear={() => set({ updated: null })}>
        {(close) => (
          <DatePicker
            value={filters.updated}
            onPick={(f) => {
              set({ updated: f })
              close()
            }}
          />
        )}
      </Chip>
      <Chip icon={<Calendar className="size-3" />} label={t.search.chips.created} value={dateFilterLabel(filters.created)} onClear={() => set({ created: null })}>
        {(close) => (
          <DatePicker
            value={filters.created}
            onPick={(f) => {
              set({ created: f })
              close()
            }}
          />
        )}
      </Chip>
    </div>
  )
}
