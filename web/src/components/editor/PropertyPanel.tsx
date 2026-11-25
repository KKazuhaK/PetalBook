import { useId, useState } from 'react'
import { ChevronRight, PenLine, X } from 'lucide-react'
import { useSpacePermissions, useTypes } from '@/api/hooks'
import { RenameFieldDialog } from './RenameFieldDialog'
import type { PageMeta } from '@/api/types'
import { FieldControl, isWrong, WrongNote } from '@/components/props/PropControls'
import { Skeleton } from '@/components/ui/skeleton'
import { isPropertyKey, showValue } from '@/lib/pageTypes'
import { tp } from '@/i18n/props'

// The properties of a page of a type, below its subtitle and tags: one control
// per field of the type, in the type's order. It edits the header keys that are
// not built in, which the editor keeps in the same draft as the title and the
// tags; the editor decides what happens to a change, this only reports it.
//
// It is its own chunk and is only asked for when the page has a type.
//
// - A value that does not fit its field is kept and shown as wrong.
// - A key of the header that the type does not list is in a collapsed group at
//   the end, each with a button that clears it.
export default function PropertyPanel({
  typeName,
  values,
  onChange,
  readOnly,
  onFieldRenamed,
}: {
  typeName: string
  values: PageMeta
  onChange: (next: PageMeta) => void
  readOnly: boolean
  // A key of the type was renamed from here: the server rewrote this page's header too.
  onFieldRenamed?: (from: string, to: string) => void
}) {
  const types = useTypes()
  const { manage } = useSpacePermissions()
  const [renaming, setRenaming] = useState<string | null>(null)
  const idBase = useId()
  const type = types.data?.types.find((x) => x.name === typeName)
  const fields = type?.fields ?? []
  const inType = new Set(fields.map((f) => f.key))
  const extra = Object.keys(values).filter((k) => isPropertyKey(k) && !inType.has(k))

  // The header with this key changed; a key that is emptied is no key at all.
  const set = (key: string, value: unknown) => {
    const next: PageMeta = { ...values }
    if (value === undefined || value === null) delete next[key]
    else next[key] = value
    onChange(next)
  }

  const name = type?.title || type?.name || typeName
  let notice: string | null = null
  if (types.isError) notice = tp.panel.loadFailed
  else if (types.isSuccess && !type) notice = tp.panel.missingType(typeName)
  else if (type?.error) notice = type.error

  return (
    <section
      aria-label={tp.panel.label(name)}
      data-slot="property-panel"
      className="rounded-lg border bg-muted/20 px-3 py-2.5 text-sm"
    >
      <h2 className="mb-1.5 text-xs font-medium text-muted-foreground">{tp.panel.heading(name)}</h2>
      {types.isPending ? (
        <div aria-busy="true" className="grid gap-1.5">
          <Skeleton className="h-7 w-full" />
          <Skeleton className="h-7 w-2/3" />
        </div>
      ) : (
        <>
          {notice && <p className="mb-1.5 text-xs text-muted-foreground">{notice}</p>}
          {fields.length > 0 && (
            <div className="grid grid-cols-[minmax(0,7rem)_minmax(0,1fr)] items-start gap-x-3 gap-y-2 max-sm:grid-cols-1 max-sm:gap-y-1">
              {fields.map((f, i) => {
                const id = `${idBase}-${i}`
                const label = f.title || f.key
                const wrong = isWrong(f, values[f.key])
                return (
                  <div key={f.key} data-field={f.key} data-kind={f.kind} className="contents">
                    <div className="group/key flex min-w-0 items-start gap-1">
                      <label
                        htmlFor={id}
                        className="min-w-0 flex-1 truncate pt-1.5 text-xs text-muted-foreground max-sm:pt-0"
                        title={label}
                      >
                        {label}
                      </label>
                      {manage && !readOnly && (
                        <button
                          type="button"
                          data-slot="rename-key"
                          aria-label={tp.panel.renameKey(f.key)}
                          title={tp.panel.renameKey(f.key)}
                          onClick={() => setRenaming(f.key)}
                          className="flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground opacity-0 transition-opacity outline-none group-focus-within/key:opacity-100 group-hover/key:opacity-100 pointer-coarse:opacity-60 hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50"
                        >
                          <PenLine className="size-3" />
                        </button>
                      )}
                    </div>
                    <div className="min-w-0">
                      <FieldControl
                        field={f}
                        value={values[f.key]}
                        onChange={(v) => set(f.key, v)}
                        readOnly={readOnly}
                        id={id}
                        label={label}
                      />
                      {wrong && f.kind !== 'date' && f.kind !== 'checkbox' && f.kind !== 'other' && <WrongNote />}
                    </div>
                  </div>
                )
              })}
            </div>
          )}
          {extra.length > 0 && (
            <details className="group mt-2 border-t pt-1.5" data-slot="not-in-type">
              <summary className="flex cursor-pointer list-none items-center gap-1 text-xs text-muted-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden">
                <ChevronRight className="size-3.5 transition-transform group-open:rotate-90" aria-hidden />
                {tp.panel.notInType(extra.length)}
              </summary>
              <ul className="mt-1.5 grid gap-1">
                {extra.map((k) => (
                  <li key={k} data-key={k} className="flex items-center gap-2 text-xs">
                    <span className="w-28 shrink-0 truncate font-mono text-muted-foreground" title={k}>
                      {k}
                    </span>
                    <span className="min-w-0 flex-1 truncate" title={showValue(values[k])}>
                      {showValue(values[k])}
                    </span>
                    {!readOnly && (
                      <button
                        type="button"
                        aria-label={tp.panel.clearKey(k)}
                        title={tp.panel.clear}
                        onClick={() => set(k, undefined)}
                        className="flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
                      >
                        <X className="size-3.5" />
                      </button>
                    )}
                  </li>
                ))}
              </ul>
            </details>
          )}
        </>
      )}
      {renaming && type && (
        <RenameFieldDialog type={type} field={renaming} onClose={() => setRenaming(null)} onRenamed={onFieldRenamed} />
      )}
    </section>
  )
}
