import { lang } from '@/i18n'
import type { DailyOption } from '@/api/types'
import { Button } from '@/components/ui/button'

// The two small controls the places that choose external daily-text interfaces share (the administrators' default and
// a space's own choice): the options of a built-in interface, as checkboxes with an "All" state, and the buttons that
// put the chosen interfaces of a language in order.

export interface ChoiceWords {
  // "All": nothing is sent to the service, which gives every kind.
  all: string
  // The name of an option (`categories`); the key itself when the words do not know it.
  optionName: (key: string) => string
  up: (name: string) => string
  down: (name: string) => string
  remove: (name: string) => string
}

// The values of one option as the person sees them, in the language of the page.
const labelOf = (v: DailyOption['values'][number]) => (lang === 'en' ? v.label.en : v.label.zh) || v.value

export function OptionChoices({
  options,
  chosen,
  onChange,
  words,
  disabled,
  owner,
}: {
  options: DailyOption[]
  // The values chosen by option key; none for a key is all of them.
  chosen: Record<string, string[]> | undefined
  onChange: (next: Record<string, string[]>) => void
  words: ChoiceWords
  disabled?: boolean
  // What the options belong to (an interface id), for the ids of the controls.
  owner: string
}) {
  return (
    <>
      {options.map((opt) => {
        const picked = chosen?.[opt.key] ?? []
        const set = (values: string[]) => {
          const next = { ...chosen }
          // Every value is the same as none: nothing is sent.
          if (values.length === 0 || values.length === opt.values.length) delete next[opt.key]
          else next[opt.key] = opt.values.map((v) => v.value).filter((v) => values.includes(v))
          onChange(next)
        }
        return (
          <fieldset
            key={opt.key}
            className="grid min-w-0 gap-1.5 border-0 p-0"
            data-slot="api-options"
            data-option={opt.key}
            disabled={disabled}
          >
            <legend className="mb-1 text-xs font-medium text-muted-foreground">{words.optionName(opt.key)}</legend>
            <div className="flex flex-wrap gap-x-4 gap-y-1.5">
              <label className="flex items-center gap-1.5 text-sm">
                <input
                  type="checkbox"
                  id={`opt-${owner}-${opt.key}-all`}
                  data-value=""
                  checked={picked.length === 0}
                  onChange={() => set([])}
                />
                {words.all}
              </label>
              {opt.values.map((v) => (
                <label key={v.value} className="flex items-center gap-1.5 text-sm">
                  <input
                    type="checkbox"
                    id={`opt-${owner}-${opt.key}-${v.value}`}
                    data-value={v.value}
                    checked={picked.includes(v.value)}
                    onChange={(e) => set(e.target.checked ? [...picked, v.value] : picked.filter((x) => x !== v.value))}
                  />
                  {labelOf(v)}
                </label>
              ))}
            </div>
          </fieldset>
        )
      })}
    </>
  )
}

// Move up, move down and remove, for one chosen interface of a list.
export function ListButtons({
  name,
  first,
  last,
  onUp,
  onDown,
  onRemove,
  words,
  disabled,
}: {
  name: string
  first: boolean
  last: boolean
  onUp: () => void
  onDown: () => void
  onRemove?: () => void
  words: ChoiceWords
  disabled?: boolean
}) {
  return (
    <span className="flex shrink-0 items-center gap-1" data-slot="api-order">
      <Button
        type="button"
        variant="ghost"
        size="sm"
        data-action="up"
        aria-label={words.up(name)}
        disabled={disabled || first}
        onClick={onUp}
      >
        ↑
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="sm"
        data-action="down"
        aria-label={words.down(name)}
        disabled={disabled || last}
        onClick={onDown}
      >
        ↓
      </Button>
      {onRemove && (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          data-action="remove"
          aria-label={words.remove(name)}
          disabled={disabled}
          onClick={onRemove}
        >
          ×
        </Button>
      )}
    </span>
  )
}

// A list with the item at index i moved by one place (a copy; the same list at an edge).
export function moved<T>(list: readonly T[], i: number, by: -1 | 1): T[] {
  const j = i + by
  if (j < 0 || j >= list.length) return [...list]
  const out = [...list]
  ;[out[i], out[j]] = [out[j], out[i]]
  return out
}
