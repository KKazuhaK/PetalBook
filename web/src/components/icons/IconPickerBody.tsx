import { useMemo, useState } from 'react'
import { Check } from 'lucide-react'
import type { IconColor } from '@/api/types'
import { CORE_CATEGORIES } from '@/lib/icons/coreIcons'
import { ICON_COLORS, iconColorVar } from '@/lib/icons/colors'
import { ALL_ICONS, ALL_NAMES, SEARCH_TEXT } from '@/lib/icons/lucideAll'
import { readRecent } from '@/lib/icons/recent'
import { isIconName } from '@/lib/icons/useIcons'
import { t } from '@/i18n'
import { PageIcon } from './PageIcon'

const PAGE = 120

// The body of the picker: the search, the recent icons, the colours, the categories and, behind a search or the "all"
// section, every Lucide icon. It imports the whole set, so it is a chunk of its own (see IconPicker).
export default function IconPickerBody({
  icon,
  color,
  onChange,
}: {
  icon: string
  color: IconColor | ''
  onChange: (next: { icon: string; color: IconColor | '' }) => void
}) {
  const [query, setQuery] = useState('')
  const [shown, setShown] = useState(PAGE)
  const recent = useMemo(() => readRecent().filter((n) => !isIconName(n) || n in ALL_ICONS), [])
  const q = query.trim().toLowerCase()
  // Every word has to be in the name, a tag or an alias.
  const found = useMemo(() => {
    if (!q) return []
    const words = q.split(/\s+/)
    return ALL_NAMES.filter((n) => words.every((w) => SEARCH_TEXT[n].includes(w)))
  }, [q])
  const pick = (name: string) => onChange({ icon: name, color })

  const cell = (name: string) => (
    <button
      key={name}
      type="button"
      role="option"
      aria-selected={icon === name}
      aria-label={isIconName(name) ? t.icons.pick(name) : name}
      title={name}
      onClick={() => pick(name)}
      className={`flex size-10 items-center justify-center rounded-md outline-none transition-colors hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50 ${icon === name ? 'bg-muted ring-1 ring-ring/60' : ''}`}
    >
      <PageIcon icon={name} color={color} className="size-5" />
    </button>
  )
  const grid = (names: readonly string[]) => (
    <div role="listbox" aria-label={t.icons.title} className="flex flex-wrap gap-0.5">
      {names.map(cell)}
    </div>
  )

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3" data-slot="icon-picker">
      <input
        type="search"
        value={query}
        onChange={(e) => {
          setQuery(e.target.value)
          setShown(PAGE)
        }}
        placeholder={t.icons.search}
        aria-label={t.icons.searchLabel}
        className="h-10 w-full rounded-lg border bg-transparent px-3 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      />
      <div role="radiogroup" aria-label={t.icons.color} className="flex flex-wrap items-center gap-1.5">
        <button
          type="button"
          role="radio"
          aria-checked={color === ''}
          aria-label={t.icons.noColor}
          title={t.icons.noColor}
          onClick={() => onChange({ icon, color: '' })}
          className={`flex size-8 items-center justify-center rounded-full border bg-foreground/80 transition-shadow ${color === '' ? 'ring-2 ring-ring ring-offset-2 ring-offset-background' : ''}`}
        />
        {ICON_COLORS.map((c) => (
          <button
            key={c}
            type="button"
            role="radio"
            aria-checked={color === c}
            aria-label={t.icons.pickColor(t.icons.colors[c])}
            title={t.icons.colors[c]}
            onClick={() => onChange({ icon, color: c })}
            style={{ backgroundColor: iconColorVar(c) }}
            className={`flex size-8 items-center justify-center rounded-full transition-shadow ${color === c ? 'ring-2 ring-ring ring-offset-2 ring-offset-background' : ''}`}
          >
            {color === c && <Check className="size-4 text-white mix-blend-difference" aria-hidden />}
          </button>
        ))}
      </div>
      <div className="min-h-0 flex-1 space-y-4 overflow-y-auto pr-1">
        {q ? (
          found.length === 0 ? (
            <p className="py-6 text-center text-sm text-muted-foreground">{t.icons.noResults}</p>
          ) : (
            <>
              {grid(found.slice(0, shown))}
              {found.length > shown && (
                <button
                  type="button"
                  onClick={() => setShown((n) => n + PAGE)}
                  className="h-10 w-full rounded-lg border text-sm hover:bg-muted"
                >
                  {t.icons.showMore(Math.min(PAGE, found.length - shown))}
                </button>
              )}
            </>
          )
        ) : (
          <>
            {recent.length > 0 && (
              <section>
                <h3 className="mb-1 text-xs font-medium text-muted-foreground">{t.icons.recent}</h3>
                {grid(recent)}
              </section>
            )}
            {Object.entries(CORE_CATEGORIES).map(([key, names]) => (
              <section key={key}>
                <h3 className="mb-1 text-xs font-medium text-muted-foreground">
                  {t.icons.categories[key as keyof typeof t.icons.categories] ?? key}
                </h3>
                {grid(names)}
              </section>
            ))}
            <section>
              <h3 className="mb-1 text-xs font-medium text-muted-foreground">{t.icons.all}</h3>
              {grid(ALL_NAMES.slice(0, shown))}
              {ALL_NAMES.length > shown && (
                <button
                  type="button"
                  onClick={() => setShown((n) => n + PAGE * 3)}
                  className="mt-2 h-10 w-full rounded-lg border text-sm hover:bg-muted"
                >
                  {t.icons.showMore(Math.min(PAGE * 3, ALL_NAMES.length - shown))}
                </button>
              )}
            </section>
          </>
        )}
      </div>
    </div>
  )
}
