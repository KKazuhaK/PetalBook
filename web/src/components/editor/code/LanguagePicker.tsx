import { useId, useMemo, useState, type KeyboardEvent } from 'react'
import { Check, ChevronDown } from 'lucide-react'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { t } from '@/i18n'
import { filterLanguages, languageLabel, languageWord, resolveLanguage } from './languages'

interface Choice {
  id: string | null
  name: string
}

// The language of a code block: the current one as a button, and under it a
// searchable list. Arrow keys move through the list from the search box.
export function LanguagePicker({
  value,
  onPick,
  onClosed,
}: {
  value: unknown
  onPick: (id: string | null) => void
  onClosed?: () => void
}) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const listId = useId()

  const current = resolveLanguage(value)
  const label = languageLabel(value) ?? t.code.plain
  const choices: Choice[] = useMemo(() => {
    const plain = { id: null, name: t.code.plain }
    const q = query.trim().toLowerCase()
    const showPlain = !q || t.code.plain.toLowerCase().includes(q) || 'plain text'.includes(q) || 'text'.includes(q)
    return [...(showPlain ? [plain] : []), ...filterLanguages(query).map((l) => ({ id: l.id, name: l.name }))]
  }, [query])

  const pick = (choice: Choice) => {
    setOpen(false)
    onPick(choice.id)
  }
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing) return
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActive((a) => Math.min(a + 1, choices.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActive((a) => Math.max(a - 1, 0))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      const choice = choices[active]
      if (choice) pick(choice)
    }
  }

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (next) {
          setQuery('')
          setActive(0)
        } else onClosed?.()
      }}
    >
      <PopoverTrigger
        type="button"
        aria-label={`${t.code.language}: ${label}`}
        className="inline-flex h-6 items-center gap-1 rounded-md px-1.5 text-xs text-muted-foreground outline-none hover:bg-background/60 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 aria-expanded:bg-background/60 aria-expanded:text-foreground"
        // Keeps the cursor in the code while the list opens.
        onMouseDown={(e) => e.preventDefault()}
      >
        {label}
        <ChevronDown className="size-3" aria-hidden />
      </PopoverTrigger>
      <PopoverContent align="end" className="w-52 gap-1.5 p-1.5">
        <input
          autoFocus
          value={query}
          onChange={(e) => {
            setQuery(e.target.value)
            setActive(0)
          }}
          onKeyDown={onKeyDown}
          placeholder={t.code.search}
          aria-label={t.code.search}
          role="combobox"
          aria-expanded
          aria-controls={listId}
          aria-activedescendant={choices[active] ? `${listId}-${active}` : undefined}
          className="h-7 w-full rounded-md border border-input bg-background px-2 text-sm outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/50"
        />
        <div id={listId} role="listbox" aria-label={t.code.language} className="max-h-56 overflow-y-auto">
          {choices.length === 0 && <p className="px-2 py-1.5 text-xs text-muted-foreground">{t.code.noMatch}</p>}
          {choices.map((choice, i) => {
            const selected = choice.id === null ? languageWord(value) === null : current?.id === choice.id
            return (
              <div
                key={choice.id ?? 'plain'}
                id={`${listId}-${i}`}
                ref={(el) => {
                  if (i === active) el?.scrollIntoView?.({ block: 'nearest' })
                }}
                role="option"
                aria-selected={selected}
                onMouseMove={() => setActive(i)}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => pick(choice)}
                className={`flex cursor-default items-center justify-between rounded-md px-2 py-1 text-sm ${i === active ? 'bg-muted' : ''}`}
              >
                {choice.name}
                {selected && <Check className="size-3.5 text-muted-foreground" aria-hidden />}
              </div>
            )
          })}
        </div>
      </PopoverContent>
    </Popover>
  )
}
