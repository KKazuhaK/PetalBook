import { useMemo, useState, type KeyboardEvent } from 'react'
import { Tag, X } from 'lucide-react'
import { t } from '@/i18n'

// Chips plus an input. A tag is committed on Enter, comma (half or full
// width) or when the box loses focus; Backspace in the empty box removes the
// last one. Suggestions come from the tags already used elsewhere.
export function TagsEditor({
  tags,
  onChange,
  suggestions,
  readOnly = false,
}: {
  tags: string[]
  onChange: (tags: string[]) => void
  suggestions: { tag: string; pages: number }[]
  readOnly?: boolean
}) {
  const [text, setText] = useState('')
  const [focused, setFocused] = useState(false)
  const [active, setActive] = useState(-1)

  const matches = useMemo(() => {
    const q = text.trim().toLowerCase()
    return suggestions.filter((s) => !tags.includes(s.tag) && (q === '' || s.tag.toLowerCase().includes(q))).slice(0, 8)
  }, [suggestions, tags, text])

  const commit = (raw: string) => {
    const tag = raw.replace(/[,，\r\n]+/g, ' ').trim()
    setText('')
    setActive(-1)
    if (tag && !tags.includes(tag)) onChange([...tags, tag])
  }

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing) return
    if (e.key === 'Enter') {
      e.preventDefault()
      commit(active >= 0 && matches[active] ? matches[active].tag : text)
    } else if (e.key === 'Backspace' && text === '' && tags.length) {
      onChange(tags.slice(0, -1))
    } else if (e.key === 'ArrowDown' && matches.length) {
      e.preventDefault()
      setActive((i) => (i + 1) % matches.length)
    } else if (e.key === 'ArrowUp' && matches.length) {
      e.preventDefault()
      setActive((i) => (i <= 0 ? matches.length - 1 : i - 1))
    } else if (e.key === 'Escape') {
      setActive(-1)
      setFocused(false)
    }
  }

  // Read only: the tags as they are, with nothing to add or take away.
  if (readOnly) {
    if (tags.length === 0) return null
    return (
      <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label={t.editor.tagsLabel}>
        <Tag className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
        {tags.map((tag) => (
          <span
            key={tag}
            className="inline-flex h-6 items-center rounded-full bg-secondary px-2.5 text-xs text-secondary-foreground"
          >
            {tag}
          </span>
        ))}
      </div>
    )
  }

  return (
    <div className="relative">
      <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label={t.editor.tagsLabel}>
        <Tag className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
        {tags.map((tag) => (
          <span
            key={tag}
            className="inline-flex h-6 items-center gap-1 rounded-full bg-secondary pr-1 pl-2.5 text-xs text-secondary-foreground"
          >
            {tag}
            <button
              type="button"
              aria-label={t.editor.removeTag(tag)}
              onClick={() => onChange(tags.filter((x) => x !== tag))}
              className="flex size-4 items-center justify-center rounded-full text-muted-foreground hover:bg-foreground/10 hover:text-foreground"
            >
              <X className="size-3" />
            </button>
          </span>
        ))}
        <input
          value={text}
          onChange={(e) => {
            const v = e.target.value
            // A typed comma ends the tag, like Enter. (Not while composing:
            // the IME may be producing a full-width comma as punctuation.)
            if (/[,，]$/.test(v) && !(e.nativeEvent as InputEvent).isComposing) commit(v)
            else {
              setText(v)
              setActive(-1)
            }
          }}
          onKeyDown={onKeyDown}
          onFocus={() => setFocused(true)}
          onBlur={() => {
            setFocused(false)
            if (text.trim()) commit(text)
          }}
          placeholder={tags.length ? '' : t.editor.tagsPlaceholder}
          aria-label={t.editor.tagsPlaceholder}
          className="h-6 min-w-24 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground/60"
        />
      </div>
      {focused && matches.length > 0 && (
        <ul
          role="listbox"
          className="absolute top-full left-5 z-40 mt-1 max-h-60 w-56 overflow-auto rounded-lg bg-popover p-1 text-sm shadow-md ring-1 ring-foreground/10"
        >
          {matches.map((m, i) => (
            <li
              key={m.tag}
              role="option"
              aria-selected={i === active}
              // mousedown, not click: the input must not lose focus first.
              onMouseDown={(e) => {
                e.preventDefault()
                commit(m.tag)
              }}
              className={`flex cursor-pointer items-center justify-between rounded-md px-2 py-1.5 ${i === active ? 'bg-muted' : 'hover:bg-muted/60'}`}
            >
              <span className="truncate">{m.tag}</span>
              <span className="text-xs text-muted-foreground tabular-nums">{m.pages}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
