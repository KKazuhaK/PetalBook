import { useState } from 'react'
import { Filter, Plus, X } from 'lucide-react'
import type { FilterOp, PropFilter, TypeField } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { filterOps } from '@/lib/pageTypes'
import { tp } from '@/i18n/props'

// At most as many as the server takes.
export const MAX_FILTERS = 10

const labelOf = (f: Pick<TypeField, 'key' | 'title'>) => f.title || f.key

// What a filter says, as text: "Status is todo, doing".
export function describeFilter(f: PropFilter, fields: readonly TypeField[]): string {
  const field = fields.find((x) => x.key === f.key)
  const name = field ? labelOf(field) : f.key
  const values = f.values ?? []
  switch (f.op) {
    case 'empty':
      return tp.filter.describeEmpty(name)
    case 'notEmpty':
      return tp.filter.describeNotEmpty(name)
    case 'in':
      return tp.filter.describeIn(name, values.map((v) => (v === true ? tp.filter.yes : v === false ? tp.filter.no : String(v))).join(', '))
    case 'notIn':
      return tp.filter.describeNotIn(name, values.map(String).join(', '))
    case 'contains':
      return tp.filter.describeContains(name, String(values[0] ?? ''))
    case 'range': {
      const [from, to] = values.map((v) => (v === null || v === '' ? '' : String(v)))
      return tp.filter.describeRange(name, from ?? '', to ?? '')
    }
  }
}

// The filters on a table: the ones in force as chips that can be taken away, and
// a small form to add one. A filter is made of a field, an op that suits the
// kind of the field, and its values.
export function TableFilters({ fields, filters, onChange }: { fields: readonly TypeField[]; filters: PropFilter[]; onChange: (next: PropFilter[]) => void }) {
  const [adding, setAdding] = useState(false)
  return (
    <div className="flex flex-wrap items-center gap-2" data-slot="table-filters">
      {filters.map((f, i) => (
        <span key={i} data-filter={f.key} className="inline-flex h-7 items-center gap-1 rounded-full border bg-secondary/60 pr-1 pl-3 text-xs">
          {describeFilter(f, fields)}
          <button type="button" aria-label={tp.filter.remove(describeFilter(f, fields))} onClick={() => onChange(filters.filter((_, j) => j !== i))} className="flex size-5 items-center justify-center rounded-full outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50">
            <X className="size-3" />
          </button>
        </span>
      ))}
      {!adding && fields.length > 0 && (
        <Button variant="outline" size="sm" disabled={filters.length >= MAX_FILTERS} onClick={() => setAdding(true)}>
          <Filter />
          {tp.filter.add}
        </Button>
      )}
      {adding && (
        <AddFilter
          fields={fields}
          onCancel={() => setAdding(false)}
          onAdd={(f) => {
            onChange([...filters, f])
            setAdding(false)
          }}
        />
      )}
    </div>
  )
}

function AddFilter({ fields, onAdd, onCancel }: { fields: readonly TypeField[]; onAdd: (f: PropFilter) => void; onCancel: () => void }) {
  const [key, setKey] = useState(fields[0]?.key ?? '')
  const field = fields.find((f) => f.key === key) ?? fields[0]
  const ops = filterOps(field.kind)
  const [op, setOp] = useState<FilterOp>(ops[0])
  // The op belongs to the field: another field may not have it.
  const activeOp: FilterOp = ops.includes(op) ? op : ops[0]
  const [chosen, setChosen] = useState<string[]>([])
  const [text, setText] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')

  const options = field.kind === 'checkbox' ? ['true', 'false'] : (field.options ?? [])
  const numeric = field.kind === 'number'

  const built = (): PropFilter | null => {
    switch (activeOp) {
      case 'empty':
      case 'notEmpty':
        return { key: field.key, op: activeOp }
      case 'in':
        return chosen.length ? { key: field.key, op: 'in', values: field.kind === 'checkbox' ? chosen.map((c) => c === 'true') : chosen } : null
      case 'contains':
        return text.trim() ? { key: field.key, op: 'contains', values: [text.trim()] } : null
      case 'range': {
        if (from === '' && to === '') return null
        const end = (v: string) => (v === '' ? null : numeric ? Number(v) : v)
        if ((from !== '' && numeric && !Number.isFinite(Number(from))) || (to !== '' && numeric && !Number.isFinite(Number(to)))) return null
        return { key: field.key, op: 'range', values: [end(from), end(to)] }
      }
    }
  }
  const filter = built()

  return (
    <form
      aria-label={tp.filter.add}
      className="flex flex-wrap items-center gap-2 rounded-lg border bg-muted/30 p-2"
      onSubmit={(e) => {
        e.preventDefault()
        if (filter) onAdd(filter)
      }}
    >
      <div className="w-36">
        <NativeSelect
          aria-label={tp.filter.field}
          value={field.key}
          onChange={(e) => {
            setKey(e.target.value)
            setChosen([])
            setText('')
            setFrom('')
            setTo('')
          }}
        >
          {fields.map((f) => (
            <option key={f.key} value={f.key}>
              {labelOf(f)}
            </option>
          ))}
        </NativeSelect>
      </div>
      <div className="w-36">
        <NativeSelect aria-label={tp.filter.op} value={activeOp} onChange={(e) => setOp(e.target.value as FilterOp)}>
          {ops.map((o) => (
            <option key={o} value={o}>
              {tp.filter.ops[o]}
            </option>
          ))}
        </NativeSelect>
      </div>
      {activeOp === 'in' && (
        <div role="group" aria-label={tp.filter.values} className="flex flex-wrap items-center gap-1.5">
          {options.map((o) => {
            const on = chosen.includes(o)
            return (
              <button key={o} type="button" aria-pressed={on} onClick={() => setChosen(on ? chosen.filter((c) => c !== o) : [...chosen, o])} className={`inline-flex h-6 items-center rounded-full border px-2.5 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring/50 ${on ? 'border-transparent bg-secondary text-secondary-foreground' : 'border-dashed text-muted-foreground hover:text-foreground'}`}>
                {field.kind === 'checkbox' ? (o === 'true' ? tp.filter.yes : tp.filter.no) : o}
              </button>
            )
          })}
        </div>
      )}
      {activeOp === 'contains' && <Input aria-label={tp.filter.text} className="w-44" value={text} onChange={(e) => setText(e.target.value)} />}
      {activeOp === 'range' && (
        <div className="flex items-center gap-1.5">
          <Input aria-label={tp.filter.from} type={numeric ? 'text' : 'date'} inputMode={numeric ? 'decimal' : undefined} className="w-36" value={from} onChange={(e) => setFrom(e.target.value)} />
          <span className="text-xs text-muted-foreground">–</span>
          <Input aria-label={tp.filter.to} type={numeric ? 'text' : 'date'} inputMode={numeric ? 'decimal' : undefined} className="w-36" value={to} onChange={(e) => setTo(e.target.value)} />
        </div>
      )}
      <Button type="submit" size="sm" disabled={!filter}>
        <Plus />
        {tp.filter.apply}
      </Button>
      <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
        {tp.cancel}
      </Button>
    </form>
  )
}
