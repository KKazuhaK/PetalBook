import { useId, useMemo, useState, type KeyboardEvent, type ReactNode } from 'react'
import { X } from 'lucide-react'

// One suggestion: a value, how many pages use it (when it is a used one), and whether it is a preset.
export interface Suggestion {
  value: string
  pages?: number
  preset?: boolean
}

// Chips plus an input with a list of suggestions below it: the one control for tags and for sources.
//
//   typing           filters the suggestions: used values first, then the presets
//   Tab              completes the first suggestion into the box (and stays)
//   ArrowDown/Up     move through the suggestions; Enter on one adds it
//   Enter, comma     adds what is typed as a chip; blur does the same
//   Backspace        in the empty box takes off the last chip
//   Escape           closes the list
//
// The list sits under the input, as wide as the row on a phone.
export function ChipInput({
  values,
  onChange,
  used,
  presets = [],
  icon,
  placeholder,
  label,
  removeLabel,
  readOnly = false,
  maxSuggestions = 8,
}: {
  values: string[]
  onChange: (values: string[]) => void
  used: { value: string; pages: number }[]
  presets?: string[]
  icon: ReactNode
  placeholder: string
  label: string
  removeLabel: (value: string) => string
  readOnly?: boolean
  maxSuggestions?: number
}) {
  const [text, setText] = useState('')
  const [focused, setFocused] = useState(false)
  const [open, setOpen] = useState(true)
  const [active, setActive] = useState(-1)
  const listId = useId()

  const matches = useMemo(() => {
    const q = text.trim().toLowerCase()
    const fits = (v: string) => !values.includes(v) && (q === '' || v.toLowerCase().includes(q))
    const first: Suggestion[] = used.filter((u) => fits(u.value)).map((u) => ({ value: u.value, pages: u.pages }))
    const seen = new Set(first.map((s) => s.value))
    const rest: Suggestion[] = presets.filter((p) => !seen.has(p) && fits(p)).map((p) => ({ value: p, preset: true }))
    return [...first, ...rest].slice(0, maxSuggestions)
  }, [used, presets, values, text, maxSuggestions])

  const commit = (raw: string) => {
    const v = raw.replace(/[,，\r\n]+/g, ' ').trim()
    setText('')
    setActive(-1)
    if (v && !values.includes(v)) onChange([...values, v])
  }

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing) return
    if (e.key === 'Enter') {
      e.preventDefault()
      commit(active >= 0 && matches[active] ? matches[active].value : text)
    } else if (e.key === 'Tab' && !e.shiftKey && text.trim() !== '' && matches.length > 0 && open) {
      // Complete the first suggestion into the box, so Enter then adds it.
      e.preventDefault()
      setText(matches[active >= 0 ? active : 0].value)
      setActive(-1)
    } else if (e.key === 'Backspace' && text === '' && values.length) {
      onChange(values.slice(0, -1))
    } else if (e.key === 'ArrowDown' && matches.length) {
      e.preventDefault()
      setOpen(true)
      setActive((i) => (i + 1) % matches.length)
    } else if (e.key === 'ArrowUp' && matches.length) {
      e.preventDefault()
      setOpen(true)
      setActive((i) => (i <= 0 ? matches.length - 1 : i - 1))
    } else if (e.key === 'Escape') {
      setActive(-1)
      setOpen(false)
    }
  }

  const chip = 'inline-flex h-6 items-center rounded-full bg-secondary text-xs text-secondary-foreground'

  // Read only: the chips as they are, with nothing to add or take away.
  if (readOnly) {
    if (values.length === 0) return null
    return (
      <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label={label}>
        {icon}
        {values.map((v) => (
          <span key={v} className={`${chip} px-2.5`}>
            {v}
          </span>
        ))}
      </div>
    )
  }

  const showList = focused && open && matches.length > 0
  return (
    <div className="relative">
      <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label={label}>
        {icon}
        {values.map((v) => (
          <span key={v} className={`${chip} gap-1 pr-1 pl-2.5`}>
            {v}
            <button
              type="button"
              aria-label={removeLabel(v)}
              onClick={() => onChange(values.filter((x) => x !== v))}
              className="flex size-4 items-center justify-center rounded-full text-muted-foreground hover:bg-foreground/10 hover:text-foreground"
            >
              <X className="size-3" />
            </button>
          </span>
        ))}
        <input
          role="combobox"
          aria-expanded={showList}
          aria-controls={listId}
          aria-autocomplete="list"
          value={text}
          onChange={(e) => {
            const v = e.target.value
            // A typed comma ends the chip, like Enter. (Not while composing:
            // the IME may be producing a full-width comma as punctuation.)
            if (/[,，]$/.test(v) && !(e.nativeEvent as InputEvent).isComposing) commit(v)
            else {
              setText(v)
              setActive(-1)
              setOpen(true)
            }
          }}
          onKeyDown={onKeyDown}
          onFocus={() => {
            setFocused(true)
            setOpen(true)
          }}
          onBlur={() => {
            setFocused(false)
            if (text.trim()) commit(text)
          }}
          placeholder={values.length ? '' : placeholder}
          aria-label={placeholder}
          className="h-8 min-w-24 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground/60 sm:h-6"
        />
      </div>
      {showList && (
        <ul
          id={listId}
          role="listbox"
          className="absolute top-full left-0 z-40 mt-1 max-h-60 w-full overflow-auto rounded-lg bg-popover p-1 text-sm shadow-md ring-1 ring-foreground/10 sm:left-5 sm:w-64"
        >
          {matches.map((m, i) => (
            <li
              key={m.value}
              role="option"
              aria-selected={i === active}
              // mousedown, not click: the input must not lose focus first.
              onMouseDown={(e) => {
                e.preventDefault()
                commit(m.value)
              }}
              className={`flex min-h-10 cursor-pointer items-center justify-between rounded-md px-2 py-1.5 transition-colors sm:min-h-0 ${i === active ? 'bg-muted' : 'hover:bg-muted/60'}`}
            >
              <span className="truncate">{m.value}</span>
              {m.pages !== undefined && <span className="text-xs text-muted-foreground tabular-nums">{m.pages}</span>}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
