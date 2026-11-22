import { useState } from 'react'
import { SlidersHorizontal } from 'lucide-react'
import type { PageType, PropFilter } from '@/api/types'
import { TableFilters } from '@/components/table/TableFilters'
import { columnsOf, hiddenKeyOf } from '@/components/table/columns'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { groupFieldsOf, MAX_CARD_FIELDS, type ViewState } from '@/lib/savedViews'
import { tb } from '@/i18n/boards'

// What a view can be adjusted by, above the listing: its filters, and a menu of the settings of its layout (the field a
// board is grouped by, the fields on its cards, the columns a table shows). Loaded on demand: a listing with none of
// these in use never needs it. The changes are the working copy's; the tabs say when it differs from what is saved.

interface Props {
  isTag: boolean
  type: PageType | undefined
  state: ViewState
  // The field the board is grouped by, which is the first single choice of the type until a view says one.
  group: string
  onChange: (patch: Partial<ViewState>) => void
}

export default function ViewTools({ isTag, type, state, group, onChange }: Props) {
  const fields = type?.fields ?? []
  const groupFields = groupFieldsOf(type)
  const wantsGroup = state.layout === 'board' || isTag
  const wantsColumns = state.layout === 'table' && fields.length > 0
  const wantsCard = state.layout === 'board' && fields.length > 0

  return (
    <div className="mb-3 flex flex-wrap items-center gap-2" data-slot="view-tools">
      <TableFilters fields={fields} filters={state.filter} onChange={(filter: PropFilter[]) => onChange({ filter })} />
      {(wantsGroup || wantsColumns || wantsCard) && (
        <Popover>
          <PopoverTrigger render={<Button variant="outline" size="sm" aria-label={tb.tools.settings} />}>
            <SlidersHorizontal />
            {tb.tools.settings}
          </PopoverTrigger>
          <PopoverContent align="start" className="w-72 gap-3">
            {wantsGroup && (
              <div className="grid gap-1.5">
                <label className="text-xs font-medium" htmlFor="view-group">
                  {tb.tools.group}
                </label>
                {isTag || groupFields.length === 0 ? (
                  <GroupKey value={state.group} onCommit={(v) => onChange({ group: v })} />
                ) : (
                  <NativeSelect id="view-group" value={group} onChange={(e) => onChange({ group: e.target.value })}>
                    {groupFields.map((f) => (
                      <option key={f.key} value={f.key}>
                        {f.title || f.key}
                      </option>
                    ))}
                  </NativeSelect>
                )}
                {isTag && <p className="text-xs text-muted-foreground">{tb.tools.groupKeyHelp}</p>}
              </div>
            )}
            {wantsCard && (
              <fieldset className="grid gap-1" aria-label={tb.tools.cardFields}>
                <legend className="text-xs font-medium">{tb.tools.cardFields}</legend>
                <p className="text-xs text-muted-foreground">{tb.tools.cardFieldsHelp(MAX_CARD_FIELDS)}</p>
                {state.cardFields && (
                  <Button variant="ghost" size="xs" className="justify-start" onClick={() => onChange({ cardFields: undefined })}>
                    {tb.tools.cardFieldsDefault}
                  </Button>
                )}
                {fields
                  .filter((f) => f.key !== group)
                  .map((f) => {
                    const chosen = state.cardFields ?? fields.filter((x) => x.key !== group).slice(0, MAX_CARD_FIELDS).map((x) => x.key)
                    const on = chosen.includes(f.key)
                    return (
                      <label key={f.key} className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-sm hover:bg-muted">
                        <input
                          type="checkbox"
                          checked={on}
                          disabled={!on && chosen.length >= MAX_CARD_FIELDS}
                          onChange={() => onChange({ cardFields: on ? chosen.filter((k) => k !== f.key) : [...chosen, f.key] })}
                          className="size-4"
                        />
                        {f.title || f.key}
                      </label>
                    )
                  })}
              </fieldset>
            )}
            {wantsColumns && (
              <fieldset className="grid gap-1" aria-label={tb.tools.columns}>
                <legend className="text-xs font-medium">{tb.tools.columns}</legend>
                <p className="text-xs text-muted-foreground">{tb.tools.columnsHelp}</p>
                {columnsOf(type)
                  .filter((c) => c.id !== 'title')
                  .map((c) => {
                    const key = hiddenKeyOf(c)
                    const hidden = state.hidden.includes(key)
                    return (
                      <label key={c.id} className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-sm hover:bg-muted">
                        <input type="checkbox" checked={!hidden} onChange={() => onChange({ hidden: hidden ? state.hidden.filter((k) => k !== key) : [...state.hidden, key] })} className="size-4" />
                        {c.label}
                      </label>
                    )
                  })}
              </fieldset>
            )}
          </PopoverContent>
        </Popover>
      )}
    </div>
  )
}

// The name of the property a tag's board is grouped by: written, and taken when the person leaves the field or presses Enter.
function GroupKey({ value, onCommit }: { value: string; onCommit: (v: string) => void }) {
  const [text, setText] = useState(value)
  const commit = () => {
    const v = text.trim()
    if (v !== value) onCommit(v)
  }
  return (
    <Input
      id="view-group"
      aria-label={tb.tools.groupKey}
      placeholder={tb.tools.groupKey}
      value={text}
      onChange={(e) => setText(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === 'Enter') {
          e.preventDefault()
          commit()
        }
      }}
    />
  )
}
