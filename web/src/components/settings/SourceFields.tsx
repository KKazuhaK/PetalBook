import { Plus, Trash2 } from 'lucide-react'
import { useId, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { RequiredMark } from '@/components/ui/field-mark'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import {
  FILTER_OPS,
  opTakesValues,
  parseValues,
  SOURCE_KINDS,
  type SourceDraft,
  type SourceKind,
} from '@/lib/dailySettings'
import type { PropFilter } from '@/api/types'
import { th } from '@/i18n/home'

export interface SourceChoices {
  folders: string[]
  tags: string[]
  types: { name: string; title: string }[]
}

// Where the pages of a daily text are taken from: a folder (with those below it), a tag or a type, and filters on
// their properties. With `choices` the value is picked from what the space has; without them (another space, which
// is not the one open) it is typed and the server checks it.
export function SourceFields({
  draft,
  onChange,
  choices,
  disabled,
}: {
  draft: SourceDraft
  onChange: (d: SourceDraft) => void
  choices?: SourceChoices
  disabled?: boolean
}) {
  const id = useId()
  const options: { value: string; label: string }[] | null =
    choices === undefined
      ? null
      : draft.kind === 'folder'
        ? choices.folders.map((f) => ({ value: f, label: f }))
        : draft.kind === 'tag'
          ? choices.tags.map((x) => ({ value: x, label: x }))
          : choices.types.map((x) => ({ value: x.name, label: x.title || x.name }))
  // A value that is no longer among the choices (a folder that was deleted) stays visible, so it is not lost silently.
  const withCurrent =
    options && draft.value !== '' && !options.some((o) => o.value === draft.value)
      ? [{ value: draft.value, label: draft.value }, ...options]
      : options

  const setFilter = (i: number, patch: Partial<PropFilter>) =>
    onChange({ ...draft, filter: draft.filter.map((f, j) => (j === i ? { ...f, ...patch } : f)) })
  return (
    <div className="grid grid-cols-1 gap-4" data-slot="source-fields">
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-[10rem_minmax(0,1fr)]">
        <div className="grid gap-1.5">
          <Label htmlFor={`${id}-kind`}>{th.settings.kindLabel}</Label>
          <NativeSelect
            id={`${id}-kind`}
            value={draft.kind}
            disabled={disabled}
            onChange={(e) => onChange({ ...draft, kind: e.target.value as SourceKind, value: '' })}
          >
            {SOURCE_KINDS.map((k) => (
              <option key={k} value={k}>
                {th.settings.kind[k]}
              </option>
            ))}
          </NativeSelect>
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor={`${id}-value`}>
            {th.settings.valueLabel[draft.kind]}
            <RequiredMark />
          </Label>
          {withCurrent ? (
            <NativeSelect
              id={`${id}-value`}
              aria-required="true"
              value={draft.value}
              disabled={disabled}
              onChange={(e) => onChange({ ...draft, value: e.target.value })}
            >
              <option value="">{withCurrent.length === 0 ? th.settings.noChoices : th.settings.choose}</option>
              {withCurrent.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </NativeSelect>
          ) : (
            <Input
              id={`${id}-value`}
              aria-required="true"
              value={draft.value}
              disabled={disabled}
              onChange={(e) => onChange({ ...draft, value: e.target.value })}
              autoComplete="off"
            />
          )}
        </div>
      </div>

      <fieldset className="grid gap-2" disabled={disabled}>
        <legend className="text-sm font-medium">{th.settings.filtersTitle}</legend>
        <p className="text-xs text-muted-foreground">{th.settings.filtersHelp}</p>
        {draft.filter.map((f, i) => (
          <div
            key={i}
            className="grid grid-cols-[1fr_9rem_auto] items-end gap-2 max-sm:grid-cols-1"
            data-slot="source-filter"
          >
            <div className="grid gap-1">
              <Label className="text-xs" htmlFor={`${id}-fk${i}`}>
                {th.settings.filterKey}
              </Label>
              <Input
                id={`${id}-fk${i}`}
                value={f.key}
                onChange={(e) => setFilter(i, { key: e.target.value })}
                autoComplete="off"
              />
            </div>
            <div className="grid gap-1">
              <Label className="text-xs" htmlFor={`${id}-fo${i}`}>
                {th.settings.filterOp}
              </Label>
              <NativeSelect
                id={`${id}-fo${i}`}
                value={f.op}
                onChange={(e) =>
                  setFilter(i, {
                    op: e.target.value as PropFilter['op'],
                    values: opTakesValues(e.target.value as PropFilter['op']) ? (f.values ?? []) : undefined,
                  })
                }
              >
                {FILTER_OPS.map((op) => (
                  <option key={op} value={op}>
                    {th.settings.ops[op as keyof typeof th.settings.ops]}
                  </option>
                ))}
              </NativeSelect>
            </div>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={th.settings.removeFilter}
              title={th.settings.removeFilter}
              onClick={() => onChange({ ...draft, filter: draft.filter.filter((_, j) => j !== i) })}
            >
              <Trash2 />
            </Button>
            {opTakesValues(f.op) && (
              <div className="grid gap-1 sm:col-span-3">
                <Label className="text-xs" htmlFor={`${id}-fv${i}`}>
                  {th.settings.filterValues}
                </Label>
                <ValuesInput
                  id={`${id}-fv${i}`}
                  values={f.values ?? []}
                  onChange={(values) => setFilter(i, { values })}
                />
              </div>
            )}
          </div>
        ))}
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => onChange({ ...draft, filter: [...draft.filter, { key: '', op: 'in', values: [] }] })}
          >
            <Plus />
            {th.settings.addFilter}
          </Button>
        </div>
      </fieldset>
    </div>
  )
}

// The values are typed in one box; what is typed (a comma at the end, say) stays as typed while the list follows it.
function ValuesInput({
  id,
  values,
  onChange,
}: {
  id: string
  values: PropFilter['values']
  onChange: (values: string[]) => void
}) {
  const [text, setText] = useState((values ?? []).map(String).join(', '))
  return (
    <Input
      id={id}
      value={text}
      onChange={(e) => {
        setText(e.target.value)
        onChange(parseValues(e.target.value))
      }}
      autoComplete="off"
    />
  )
}
