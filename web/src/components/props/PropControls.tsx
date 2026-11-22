import { useState } from 'react'
import { ExternalLink, X } from 'lucide-react'
import { parse, stringify } from 'yaml'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { fits, hasValue, isDay, showValue } from '@/lib/pageTypes'
import type { TypeField } from '@/api/types'
import { tp } from '@/i18n/props'

// One control for the value of a field, by its kind (docs/api.md, "Page types").
// `onChange` is given the new value, or undefined when the value was emptied
// (the key then goes from the header). A value that does not fit its field is
// shown as it is, marked wrong, and stays until the person changes it.

const WRONG = 'border-destructive text-destructive ring-3 ring-destructive/20'
const BOX = 'w-full rounded-lg border border-input bg-transparent px-2.5 py-1 text-base outline-none transition-colors focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 aria-invalid:border-destructive aria-invalid:text-destructive aria-invalid:ring-3 aria-invalid:ring-destructive/20 md:text-sm dark:bg-input/30'

export interface ControlProps {
  field: Pick<TypeField, 'kind' | 'options'>
  value: unknown
  onChange: (value: unknown) => void
  readOnly?: boolean
  id?: string
  // The accessible name of the control.
  label: string
}

// What marks a value as not fitting; the controls call it so the rule is one.
export const isWrong = (field: ControlProps['field'], value: unknown) => hasValue(value) && !fits(field, value)

export function WrongNote({ children }: { children?: string }) {
  return (
    <p role="note" data-wrong="true" className="mt-0.5 text-xs text-destructive">
      {children ?? tp.panel.wrong}
    </p>
  )
}

function TextControl({ field, value, onChange, readOnly, id, label }: ControlProps) {
  const wrong = isWrong(field, value)
  const text = typeof value === 'string' ? value : hasValue(value) ? showValue(value) : ''
  const multi = field.kind === 'longtext'
  if (multi)
    return (
      <textarea id={id} aria-label={label} aria-invalid={wrong || undefined} readOnly={readOnly} rows={2} value={text} onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value)} className={`${BOX} min-h-[3.5rem] resize-y [field-sizing:content]`} />
    )
  return <Input id={id} aria-label={label} aria-invalid={wrong || undefined} readOnly={readOnly} value={text} onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value)} />
}

function UrlControl({ field, value, onChange, readOnly, id, label }: ControlProps) {
  const wrong = isWrong(field, value)
  const text = typeof value === 'string' ? value : hasValue(value) ? showValue(value) : ''
  // Only addresses that are safe to open from here.
  const href = /^(https?:\/\/|mailto:)/i.test(text.trim()) ? text.trim() : null
  return (
    <div className="flex items-center gap-1.5">
      <Input id={id} aria-label={label} aria-invalid={wrong || undefined} readOnly={readOnly} value={text} onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value)} />
      {href && (
        <a href={href} target="_blank" rel="noreferrer noopener" aria-label={tp.panel.openLink(label)} title={tp.panel.openLink(label)} className="flex size-8 shrink-0 items-center justify-center rounded-lg border text-muted-foreground transition-colors hover:bg-muted hover:text-foreground">
          <ExternalLink className="size-4" />
        </a>
      )}
    </div>
  )
}

export function SelectControl({ field, value, onChange, readOnly, id, label, className }: ControlProps & { className?: string }) {
  const options = field.options ?? []
  const wrong = isWrong(field, value)
  const current = typeof value === 'string' ? value : hasValue(value) ? showValue(value) : ''
  return (
    <NativeSelect id={id} aria-label={label} aria-invalid={wrong || undefined} disabled={readOnly} value={current} onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value)} className={`${wrong ? WRONG : ''} ${className ?? ''}`}>
      <option value="">{tp.panel.none}</option>
      {current !== '' && !options.includes(current) && <option value={current}>{current}</option>}
      {options.map((o) => (
        <option key={o} value={o}>
          {o}
        </option>
      ))}
    </NativeSelect>
  )
}

function MultiControl({ field, value, onChange, readOnly, label }: ControlProps) {
  const options = field.options ?? []
  // A value that is not a list is one wrong entry, kept until the person picks.
  const list: unknown[] = Array.isArray(value) ? value : hasValue(value) ? [value] : []
  const wrongWhole = hasValue(value) && !Array.isArray(value)
  const change = (next: unknown[]) => onChange(next.length ? next : undefined)
  return (
    <div role="group" aria-label={label} aria-invalid={isWrong(field, value) || undefined} className="flex flex-wrap items-center gap-1.5">
      {options.map((o) => {
        const on = list.includes(o)
        return (
          <button
            key={o}
            type="button"
            aria-pressed={on}
            disabled={readOnly}
            onClick={() => change(on ? list.filter((x) => x !== o) : [...list, o])}
            className={`inline-flex h-6 items-center rounded-full border px-2.5 text-xs transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring/50 disabled:cursor-default ${on ? 'border-transparent bg-secondary text-secondary-foreground' : 'border-dashed text-muted-foreground hover:text-foreground'}`}
          >
            {o}
          </button>
        )
      })}
      {list
        .filter((x) => !options.includes(x as string) || wrongWhole)
        .map((x, i) => (
          <span key={`${showValue(x)}-${i}`} data-wrong="true" className="inline-flex h-6 items-center gap-1 rounded-full border border-destructive px-2.5 text-xs text-destructive">
            {showValue(x)}
            {!readOnly && (
              <button type="button" aria-label={tp.panel.removeValue(showValue(x))} onClick={() => change(wrongWhole ? [] : list.filter((y) => y !== x))} className="rounded-full outline-none hover:bg-destructive/10 focus-visible:ring-2 focus-visible:ring-destructive/40">
                <X className="size-3" />
              </button>
            )}
          </span>
        ))}
    </div>
  )
}

export function DateControl({ field, value, onChange, readOnly, id, label, className }: ControlProps & { className?: string }) {
  const wrong = isWrong(field, value)
  return (
    <div className="flex flex-col">
      <Input id={id} type="date" aria-label={label} aria-invalid={wrong || undefined} readOnly={readOnly} disabled={readOnly} value={isDay(value) ? value : ''} onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value)} className={className} />
      {wrong && (
        <span data-wrong="true" className="mt-0.5 text-xs text-destructive">
          {showValue(value)}
        </span>
      )}
    </div>
  )
}

// A number is typed as text, so that "1." and "-" can be on the way to one; the
// header only gets a number, and a text that is not one is shown as wrong.
export function NumberControl({ field, value, onChange, readOnly, id, label, className }: ControlProps & { className?: string }) {
  const [typed, setTyped] = useState<string | null>(null)
  const shown = typed ?? (hasValue(value) ? showValue(value) : '')
  const wrong = typed !== null ? typed.trim() !== '' && !Number.isFinite(Number(typed)) : isWrong(field, value)
  return (
    <Input
      id={id}
      inputMode="decimal"
      aria-label={label}
      aria-invalid={wrong || undefined}
      readOnly={readOnly}
      value={shown}
      className={className}
      onChange={(e) => {
        const text = e.target.value
        setTyped(text)
        if (text.trim() === '') onChange(undefined)
        else if (Number.isFinite(Number(text))) onChange(Number(text))
      }}
      onBlur={() => setTyped(null)}
    />
  )
}

export function CheckboxControl({ field, value, onChange, readOnly, id, label }: ControlProps) {
  const wrong = isWrong(field, value)
  return (
    <div className="flex items-center gap-2">
      <input id={id} type="checkbox" aria-label={label} aria-invalid={wrong || undefined} disabled={readOnly} checked={value === true} onChange={(e) => onChange(e.target.checked)} className="size-4 accent-[var(--color-brand,currentColor)] aria-invalid:outline aria-invalid:outline-2 aria-invalid:outline-destructive" />
      {wrong && (
        <span data-wrong="true" className="text-xs text-destructive">
          {showValue(value)}
        </span>
      )}
    </div>
  )
}

// A field of a kind the app does not know: the value as YAML, edited as text. What
// is typed is kept as typed until it reads as YAML; only then does the header
// change, so nothing is lost on the way.
function OtherControl({ value, onChange, readOnly, id, label }: ControlProps) {
  const [typed, setTyped] = useState<string | null>(null)
  let error = false
  if (typed !== null) {
    try {
      parse(typed)
    } catch {
      error = true
    }
  }
  const shown = typed ?? (hasValue(value) ? stringify(value).trimEnd() : '')
  return (
    <div className="flex flex-col">
      <textarea
        id={id}
        aria-label={label}
        aria-invalid={error || undefined}
        readOnly={readOnly}
        rows={2}
        spellCheck={false}
        value={shown}
        onChange={(e) => {
          const text = e.target.value
          setTyped(text)
          try {
            const next = parse(text) as unknown
            onChange(next === null || next === undefined || next === '' ? undefined : next)
          } catch {
            // Not YAML yet: the text stays here, the header as it was.
          }
        }}
        onBlur={() => !error && setTyped(null)}
        className={`${BOX} min-h-[3.5rem] resize-y font-mono [field-sizing:content]`}
      />
      {error && <WrongNote>{tp.panel.notYaml}</WrongNote>}
    </div>
  )
}

export function FieldControl(props: ControlProps) {
  switch (props.field.kind) {
    case 'select':
      return <SelectControl {...props} />
    case 'multiselect':
      return <MultiControl {...props} />
    case 'date':
      return <DateControl {...props} />
    case 'number':
      return <NumberControl {...props} />
    case 'checkbox':
      return <CheckboxControl {...props} />
    case 'url':
      return <UrlControl {...props} />
    case 'other':
      return <OtherControl {...props} />
    default:
      return <TextControl {...props} />
  }
}
