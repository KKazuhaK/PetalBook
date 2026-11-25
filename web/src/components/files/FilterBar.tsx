import { ListFilter, X } from 'lucide-react'
import { useTags, useUsedSources } from '@/api/hooks'
import type { PageType } from '@/api/types'
import { Button } from '@/components/ui/button'
import { NativeSelect } from '@/components/ui/native-select'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { filterableFields, filterCount, noFilters, type TableFilters } from '@/lib/folderTable'
import { t } from '@/i18n'

const sorts = [
  ['-updated', () => t.list.sorts['-updated']],
  ['updated', () => t.list.sorts.updated],
  ['-created', () => t.list.sorts['-created']],
  ['created', () => t.list.sorts.created],
  ['title', () => t.list.sorts.title],
  ['-title', () => t.list.sorts['-title']],
] as const

function toggle(list: string[], v: string): string[] {
  return list.includes(v) ? list.filter((x) => x !== v) : [...list, v]
}

// The chips of what the folder is filtered by, each with a button that takes it away.
function chipsOf(
  f: TableFilters,
  type: PageType | undefined,
): { key: string; text: string; remove: (f: TableFilters) => TableFilters }[] {
  const out: { key: string; text: string; remove: (f: TableFilters) => TableFilters }[] = []
  for (const tag of f.tags)
    out.push({ key: `tag:${tag}`, text: `#${tag}`, remove: (x) => ({ ...x, tags: x.tags.filter((v) => v !== tag) }) })
  for (const s of f.sources)
    out.push({
      key: `src:${s}`,
      text: `${t.ftable.columns.source}: ${s}`,
      remove: (x) => ({ ...x, sources: x.sources.filter((v) => v !== s) }),
    })
  for (const [k, vals] of Object.entries(f.fields)) {
    const title = type?.fields.find((x) => x.key === k)?.title || k
    for (const v of vals)
      out.push({
        key: `${k}:${v}`,
        text: `${title}: ${v}`,
        remove: (x) => ({ ...x, fields: { ...x.fields, [k]: (x.fields[k] ?? []).filter((y) => y !== v) } }),
      })
  }
  for (const [name, label] of [
    ['created', t.ftable.sections.created],
    ['updated', t.ftable.sections.updated],
  ] as const) {
    const r = f[name]
    if (r.from || r.to)
      out.push({
        key: name,
        text: `${label}: ${t.ftable.range(r.from, r.to)}`,
        remove: (x) => ({ ...x, [name]: { from: '', to: '' } }),
      })
  }
  return out
}

// Above a folder's listing: the Filter button with the list-filter icon, the filters in force as chips, and (on a
// phone) one button that holds the sort as well. Where the sort is not on the headers it is a select here.
export function FilterBar({
  filters,
  onChange,
  type,
  sort,
  onSort,
  sortDisabled,
  mobile,
  headerSort,
  folders,
}: {
  filters: TableFilters
  onChange: (f: TableFilters) => void
  type?: PageType
  sort: string
  onSort: (s: string) => void
  sortDisabled?: boolean
  mobile: boolean
  // The headers of a table sort by themselves, so the select is only for a phone.
  headerSort: boolean
  // Where the subfolders go in the table: first, or mixed in with the pages.
  folders?: { mixed: boolean; onToggle: () => void }
}) {
  const tags = useTags()
  const used = useUsedSources()
  const count = filterCount(filters)
  const chips = chipsOf(filters, type)
  const fields = filterableFields(type)
  const showSort = mobile || !headerSort
  const label = mobile ? t.ftable.sortAndFilter : t.ftable.filter
  const section = (title: string, children: React.ReactNode) => (
    <section className="grid gap-1.5">
      <h3 className="text-xs font-medium text-muted-foreground">{title}</h3>
      {children}
    </section>
  )
  const options = (values: string[], chosen: string[], on: (next: string[]) => void) =>
    values.length === 0 ? (
      <p className="text-xs text-muted-foreground">{t.ftable.noOptions}</p>
    ) : (
      <ul className="flex max-h-40 flex-wrap gap-1.5 overflow-y-auto">
        {values.map((v) => (
          <li key={v}>
            <button
              type="button"
              aria-pressed={chosen.includes(v)}
              onClick={() => on(toggle(chosen, v))}
              className={`inline-flex min-h-8 items-center rounded-full border px-3 text-xs transition-colors ${chosen.includes(v) ? 'border-primary bg-primary/10 text-foreground' : 'hover:bg-muted'}`}
            >
              {v}
            </button>
          </li>
        ))}
      </ul>
    )
  const dates = (name: 'created' | 'updated', title: string) =>
    section(
      title,
      <div className="flex items-center gap-2">
        {(['from', 'to'] as const).map((side) => (
          <label key={side} className="flex min-w-0 flex-1 items-center gap-1.5 text-xs text-muted-foreground">
            {t.ftable[side]}
            <input
              type="date"
              value={filters[name][side]}
              onChange={(e) => onChange({ ...filters, [name]: { ...filters[name], [side]: e.target.value } })}
              className="h-9 min-w-0 flex-1 rounded-md border bg-transparent px-2 text-sm text-foreground"
            />
          </label>
        ))}
      </div>,
    )
  return (
    <div className="mb-2 flex flex-wrap items-center gap-2" data-slot="filter-bar">
      <Popover>
        <PopoverTrigger
          data-slot="filter-button"
          aria-label={t.ftable.filterLabel}
          className="inline-flex h-10 items-center gap-1.5 rounded-lg border px-3 text-sm outline-none transition-colors hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50 data-popup-open:bg-muted"
        >
          <ListFilter className="size-4" strokeWidth={1.75} aria-hidden />
          {label}
          {count > 0 && (
            <span className="rounded-full bg-primary px-1.5 text-xs leading-5 text-primary-foreground tabular-nums">
              {count}
            </span>
          )}
        </PopoverTrigger>
        <PopoverContent
          align="start"
          className="max-h-[min(32rem,calc(100dvh-6rem))] w-80 max-w-[calc(100vw-1rem)] gap-4 overflow-y-auto p-3"
        >
          {showSort &&
            section(
              t.ftable.sortBy,
              <NativeSelect
                className="h-10"
                aria-label={t.list.sort}
                disabled={sortDisabled}
                value={sort || '-updated'}
                onChange={(e) => onSort(e.target.value)}
              >
                {sorts.map(([v, text]) => (
                  <option key={v} value={v}>
                    {text()}
                  </option>
                ))}
                {sort !== '' && !sorts.some(([v]) => v === sort) && (
                  <option value={sort}>{t.views.customSort(sort.replace(/^-?(prop:)?/, ''))}</option>
                )}
              </NativeSelect>,
            )}
          {section(
            t.ftable.sections.tags,
            options(
              (tags.data?.tags ?? []).map((x) => x.tag),
              filters.tags,
              (tags_) => onChange({ ...filters, tags: tags_ }),
            ),
          )}
          {section(
            t.ftable.sections.source,
            options(
              (used.data?.sources ?? []).map((x) => x.source),
              filters.sources,
              (sources) => onChange({ ...filters, sources }),
            ),
          )}
          {fields.map((f) =>
            section(
              f.title || f.key,
              options(f.options ?? [], filters.fields[f.key] ?? [], (v) =>
                onChange({ ...filters, fields: { ...filters.fields, [f.key]: v } }),
              ),
            ),
          )}
          {dates('created', t.ftable.sections.created)}
          {dates('updated', t.ftable.sections.updated)}
          {count > 0 && (
            <Button type="button" variant="ghost" className="h-10" onClick={() => onChange(noFilters())}>
              {t.ftable.clearAll}
            </Button>
          )}
        </PopoverContent>
      </Popover>
      {folders && (
        <button
          type="button"
          data-slot="folders-toggle"
          aria-pressed={folders.mixed}
          aria-label={t.ftable.foldersToggle}
          title={t.ftable.foldersToggle}
          onClick={folders.onToggle}
          className="ml-auto inline-flex h-10 items-center rounded-lg px-3 text-sm text-muted-foreground outline-none transition-colors hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
        >
          {folders.mixed ? t.ftable.foldersMixed : t.ftable.foldersFirst}
        </button>
      )}
      {chips.map((c) => (
        <span
          key={c.key}
          data-slot="filter-chip"
          className="inline-flex h-8 items-center gap-1 rounded-full border bg-secondary/60 pr-1 pl-3 text-xs"
        >
          {c.text}
          <button
            type="button"
            aria-label={t.ftable.removeFilter(c.text)}
            onClick={() => onChange(c.remove(filters))}
            className="flex size-6 items-center justify-center rounded-full text-muted-foreground hover:bg-foreground/10 hover:text-foreground"
          >
            <X className="size-3" aria-hidden />
          </button>
        </span>
      ))}
    </div>
  )
}
