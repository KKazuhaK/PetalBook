import { useMemo, useState } from 'react'
import { ArrowDown, ArrowUp, Plus, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { errorMessage, useBindFolder, useDeleteType, useSaveType, useTypes } from '@/api/hooks'
import type { FieldKind, FolderBinding, PageType, TypeInput } from '@/api/types'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import { FIELD_KINDS, fieldKeyProblem, typeNameProblem } from '@/lib/pageTypes'
import { tp } from '@/i18n/props'
import { SettingsNote, SettingsSection } from './SettingsParts'

// The page types of the space: what fields the pages of a kind have. Everyone
// who can read the space sees them; making, changing and deleting them, and
// unbinding folders, is for those who may manage it. The code is a chunk of its
// own, loaded when this section is opened.
export default function TypesPanel({ canManage }: { canManage: boolean }) {
  const types = useTypes()
  // The type being edited, or `new`.
  const [open, setOpen] = useState<string | 'new' | null>(null)

  if (types.isPending) return <PageFallback />
  if (types.isError) return <ErrorState message={tp.types.loadFailed} onRetry={() => void types.refetch()} />
  const { types: list, bindings } = types.data

  return (
    <div className="space-y-8">
      <SettingsSection title={tp.types.title} description={canManage ? tp.types.help : tp.types.helpReadOnly}>
        {list.length === 0 && open !== 'new' && <SettingsNote>{tp.types.none}</SettingsNote>}
        <ul className="grid gap-2">
          {list.map((type) => (
            <li key={type.name} data-type={type.name} className="rounded-lg border">
              <button
                type="button"
                aria-expanded={open === type.name}
                onClick={() => setOpen(open === type.name ? null : type.name)}
                className="flex w-full items-center justify-between gap-3 rounded-lg px-3 py-2.5 text-left outline-none transition-colors hover:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring/50"
              >
                <span className="min-w-0">
                  <span className="block truncate text-sm font-medium">{type.title || type.name}</span>
                  <span className="block truncate font-mono text-xs text-muted-foreground">{type.name}</span>
                </span>
                <span className="shrink-0 text-xs text-muted-foreground">{type.error ? tp.types.broken : tp.types.fieldCount(type.fields.length)}</span>
              </button>
              {open === type.name && (
                <div className="border-t p-3">
                  {type.error ? <BrokenType type={type} canManage={canManage} bindings={bindings} onGone={() => setOpen(null)} /> : <TypeEditor key={JSON.stringify(type)} type={type} others={list.map((x) => x.name)} bindings={bindings} canManage={canManage} onClose={() => setOpen(null)} onGone={() => setOpen(null)} />}
                </div>
              )}
            </li>
          ))}
          {open === 'new' && (
            <li className="rounded-lg border p-3" data-type="new">
              <TypeEditor type={null} others={list.map((x) => x.name)} bindings={bindings} canManage={canManage} onClose={() => setOpen(null)} onGone={() => setOpen(null)} onCreated={(name) => setOpen(name)} />
            </li>
          )}
        </ul>
        {canManage && open !== 'new' && (
          <div>
            <Button variant="outline" size="sm" onClick={() => setOpen('new')}>
              <Plus />
              {tp.types.newType}
            </Button>
          </div>
        )}
      </SettingsSection>
      <BindingsSection types={list} bindings={bindings} canManage={canManage} />
    </div>
  )
}

// --- the folders bound to types ---------------------------------------------------

function BindingsSection({ types, bindings, canManage }: { types: PageType[]; bindings: FolderBinding[]; canManage: boolean }) {
  const bind = useBindFolder()
  const title = (name: string) => types.find((x) => x.name === name)?.title || name
  return (
    <SettingsSection title={tp.bindings.title} description={tp.bindings.help}>
      {bindings.length === 0 ? (
        <SettingsNote>{tp.bindings.none}</SettingsNote>
      ) : (
        <ul className="divide-y divide-border/70 rounded-lg border">
          {bindings.map((b) => (
            <li key={b.folder} data-binding={b.folder} className="flex items-center justify-between gap-3 px-3 py-2">
              <span className="min-w-0 truncate text-sm">
                <span className="font-mono">{b.folder === '' ? tp.bindings.root : b.folder}</span>
                <span className="mx-2 text-muted-foreground">→</span>
                <span>{title(b.type)}</span>
              </span>
              {canManage && (
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={bind.isPending}
                  aria-label={tp.bindings.unbindOf(b.folder === '' ? tp.bindings.root : b.folder)}
                  onClick={() => bind.mutate({ folder: b.folder, type: '' }, { onError: (e) => toast.error(errorMessage(e, tp.bindings.failed)) })}
                >
                  {tp.bindings.unbind}
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
    </SettingsSection>
  )
}

// --- a type whose file cannot be read -----------------------------------------------

function BrokenType({ type, canManage, bindings, onGone }: { type: PageType; canManage: boolean; bindings: FolderBinding[]; onGone: () => void }) {
  return (
    <div className="grid gap-3">
      <p role="alert" className="text-sm text-destructive">
        {type.error}
      </p>
      <p className="text-xs text-muted-foreground">{tp.types.brokenHelp}</p>
      {canManage && <DeleteType type={type} bindings={bindings} onGone={onGone} />}
    </div>
  )
}

// --- delete ----------------------------------------------------------------------------

function DeleteType({ type, bindings, onGone }: { type: PageType; bindings: FolderBinding[]; onGone: () => void }) {
  const del = useDeleteType()
  const [asking, setAsking] = useState(false)
  const bound = bindings.filter((b) => b.type === type.name).length
  return (
    <>
      <Button variant="outline" size="sm" className="text-destructive" onClick={() => setAsking(true)}>
        <Trash2 />
        {tp.types.delete}
      </Button>
      <AlertDialog open={asking} onOpenChange={setAsking}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tp.types.deleteTitle(type.title || type.name)}</AlertDialogTitle>
            <AlertDialogDescription>{tp.types.deleteHelp(bound)}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tp.cancel}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={del.isPending}
              onClick={() =>
                del.mutate(type.name, {
                  onSuccess: () => {
                    toast.success(tp.types.deleted)
                    onGone()
                  },
                  onError: (e) => toast.error(errorMessage(e, tp.types.deleteFailed)),
                })
              }
            >
              {tp.types.deleteConfirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

// --- the editor of one type ----------------------------------------------------------

interface FieldDraft {
  // Only to key the rows while they move.
  id: number
  key: string
  title: string
  kind: FieldKind
  options: string
  // The type has it already: its key is fixed (a new key is a new field).
  existing: boolean
}

const picks = (k: FieldKind) => k === 'select' || k === 'multiselect'
const optionList = (text: string) => text.split('\n').map((l) => l.trim()).filter((l) => l !== '')

let nextId = 1
function draftsOf(type: PageType | null): FieldDraft[] {
  return (type?.fields ?? []).map((f) => ({ id: nextId++, key: f.key, title: f.title, kind: f.kind, options: (f.options ?? []).join('\n'), existing: true }))
}

function inputOf(title: string, fields: FieldDraft[]): TypeInput {
  return {
    title: title.trim(),
    fields: fields.map((f) => ({
      key: f.key,
      title: f.title.trim(),
      // A kind the app cannot make stays as the file has it.
      ...(f.kind === 'other' ? {} : { kind: f.kind }),
      ...(picks(f.kind) ? { options: optionList(f.options) } : {}),
    })),
  }
}

function TypeEditor({ type, others, bindings, canManage, onClose, onGone, onCreated }: { type: PageType | null; others: string[]; bindings: FolderBinding[]; canManage: boolean; onClose: () => void; onGone: () => void; onCreated?: (name: string) => void }) {
  const save = useSaveType()
  const [name, setName] = useState('')
  const [title, setTitle] = useState(type?.title ?? '')
  const [fields, setFields] = useState<FieldDraft[]>(() => draftsOf(type))
  const [error, setError] = useState('')
  const isNew = type === null

  const before = useMemo(() => JSON.stringify(inputOf(type?.title ?? '', draftsOf(type))), [type])
  const input = inputOf(title, fields)
  const dirty = isNew || JSON.stringify(input) !== before

  const nameProblem = isNew ? typeNameProblem(name.trim(), others) : null
  const fieldProblems = fields.map((f, i) => {
    if (!f.existing) {
      const p = fieldKeyProblem(f.key.trim())
      if (p) return p === 'empty' ? 'empty' : p
    }
    if (fields.some((g, j) => j < i && g.key.trim() === f.key.trim())) return 'twice'
    if (picks(f.kind)) {
      const list = optionList(f.options)
      if (list.length === 0) return 'noOptions'
      if (new Set(list).size !== list.length) return 'dupOptions'
    }
    return null
  })
  const invalid = (isNew && (name.trim() === '' || nameProblem !== null)) || fieldProblems.some((p) => p !== null)

  if (!canManage && type) return <TypeView type={type} />

  const patch = (i: number, change: Partial<FieldDraft>) => setFields((list) => list.map((f, j) => (j === i ? { ...f, ...change } : f)))
  const move = (i: number, by: -1 | 1) =>
    setFields((list) => {
      const to = i + by
      if (to < 0 || to >= list.length) return list
      const next = [...list]
      ;[next[i], next[to]] = [next[to], next[i]]
      return next
    })

  const submit = async () => {
    if (invalid || save.isPending) return
    setError('')
    // Keys are as typed, trimmed; the server takes them as they are.
    const body: TypeInput = { ...input, fields: input.fields.map((f) => ({ ...f, key: f.key.trim() })) }
    try {
      const saved = await save.mutateAsync({ name: isNew ? name.trim() : type.name, input: body })
      toast.success(tp.types.saved)
      if (isNew) onCreated?.(saved.name)
    } catch (e) {
      setError(errorMessage(e, tp.types.saveFailed))
    }
  }

  const nameText = { empty: tp.types.nameEmpty, long: tp.types.nameLong, chars: tp.types.nameChars, exists: tp.types.nameExists }

  return (
    <form
      className="grid gap-4"
      onSubmit={(e) => {
        e.preventDefault()
        void submit()
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="grid gap-1.5">
          <Label htmlFor="type-name">{tp.types.name}</Label>
          {isNew ? (
            <Input id="type-name" autoFocus autoComplete="off" value={name} onChange={(e) => setName(e.target.value)} aria-invalid={nameProblem !== null && name !== '' ? true : undefined} aria-describedby="type-name-help" />
          ) : (
            <Input id="type-name" readOnly value={type.name} className="font-mono" aria-describedby="type-name-help" />
          )}
          <p id="type-name-help" className={`text-xs ${nameProblem && name !== '' ? 'text-destructive' : 'text-muted-foreground'}`}>
            {nameProblem && name !== '' ? nameText[nameProblem] : tp.types.nameHelp}
          </p>
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="type-title">{tp.types.displayTitle}</Label>
          <Input id="type-title" autoComplete="off" value={title} onChange={(e) => setTitle(e.target.value)} />
          <p className="text-xs text-muted-foreground">{tp.types.displayTitleHelp}</p>
        </div>
      </div>

      <div className="grid gap-2">
        <h4 className="text-xs font-medium text-muted-foreground">{tp.types.fields}</h4>
        {fields.length === 0 && <p className="text-xs text-muted-foreground">{tp.types.noFields}</p>}
        <ul className="grid gap-2">
          {fields.map((f, i) => {
            const label = f.title.trim() || f.key || String(i + 1)
            const problem = fieldProblems[i]
            return (
              <li key={f.id} data-field={f.key} className="grid gap-2 rounded-lg border bg-muted/20 p-2.5">
                <div className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)_9rem_auto] items-start gap-2 max-sm:grid-cols-2">
                  <Input aria-label={tp.types.fieldKey(label)} value={f.key} readOnly={f.existing} placeholder={tp.types.fieldKeyPlaceholder} autoComplete="off" className={f.existing ? 'font-mono text-muted-foreground' : 'font-mono'} onChange={(e) => patch(i, { key: e.target.value })} aria-invalid={problem === 'empty' ? undefined : problem && problem !== 'noOptions' && problem !== 'dupOptions' ? true : undefined} />
                  <Input aria-label={tp.types.fieldTitle(label)} value={f.title} placeholder={tp.types.fieldTitlePlaceholder} autoComplete="off" onChange={(e) => patch(i, { title: e.target.value })} />
                  {f.kind === 'other' ? (
                    <div className="flex h-8 items-center text-xs text-muted-foreground" title={tp.types.otherHelp}>
                      {tp.kinds.other}
                    </div>
                  ) : (
                    <NativeSelect aria-label={tp.types.fieldKind(label)} value={f.kind} onChange={(e) => patch(i, { kind: e.target.value as FieldKind })}>
                      {FIELD_KINDS.map((k) => (
                        <option key={k} value={k}>
                          {tp.kinds[k]}
                        </option>
                      ))}
                    </NativeSelect>
                  )}
                  <div className="flex items-center gap-0.5 max-sm:col-span-2 max-sm:justify-end">
                    <Button type="button" variant="ghost" size="icon-sm" aria-label={tp.types.moveUp(label)} disabled={i === 0} onClick={() => move(i, -1)}>
                      <ArrowUp />
                    </Button>
                    <Button type="button" variant="ghost" size="icon-sm" aria-label={tp.types.moveDown(label)} disabled={i === fields.length - 1} onClick={() => move(i, 1)}>
                      <ArrowDown />
                    </Button>
                    <Button type="button" variant="ghost" size="icon-sm" aria-label={tp.types.removeField(label)} onClick={() => setFields((list) => list.filter((_, j) => j !== i))}>
                      <Trash2 />
                    </Button>
                  </div>
                </div>
                {picks(f.kind) && (
                  <div className="grid gap-1">
                    <Textarea aria-label={tp.types.fieldOptions(label)} rows={3} value={f.options} placeholder={tp.types.optionsPlaceholder} onChange={(e) => patch(i, { options: e.target.value })} aria-invalid={problem === 'noOptions' || problem === 'dupOptions' ? true : undefined} />
                    <p className="text-xs text-muted-foreground">{tp.types.optionsHelp}</p>
                  </div>
                )}
                {problem && (
                  <p role="alert" className="text-xs text-destructive">
                    {{ empty: tp.types.keyEmpty, long: tp.types.keyLong, chars: tp.types.keyChars, builtin: tp.types.keyBuiltin, twice: tp.types.keyTwice, noOptions: tp.types.noOptions, dupOptions: tp.types.dupOptions }[problem]}
                  </p>
                )}
              </li>
            )
          })}
        </ul>
        <div>
          <Button type="button" variant="outline" size="sm" onClick={() => setFields((list) => [...list, { id: nextId++, key: '', title: '', kind: 'text', options: '', existing: false }])}>
            <Plus />
            {tp.types.addField}
          </Button>
        </div>
        {fields.some((f) => f.existing) && <p className="text-xs text-muted-foreground">{tp.types.keyFixedHelp}</p>}
      </div>

      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>{type && <DeleteType type={type} bindings={bindings} onGone={onGone} />}</div>
        <div className="flex gap-2">
          <Button type="button" variant="outline" onClick={onClose}>
            {isNew || dirty ? tp.cancel : tp.close}
          </Button>
          <Button type="submit" disabled={!dirty || invalid || save.isPending}>
            {save.isPending && <Spinner />}
            {isNew ? tp.types.create : tp.types.save}
          </Button>
        </div>
      </div>
    </form>
  )
}

// What someone who may not manage sees: the fields, as text.
function TypeView({ type }: { type: PageType }) {
  return (
    <div className="grid gap-2">
      <p className="text-xs text-muted-foreground">{tp.types.nameLabel(type.name)}</p>
      {type.fields.length === 0 ? (
        <p className="text-xs text-muted-foreground">{tp.types.noFields}</p>
      ) : (
        <ul className="divide-y divide-border/70 rounded-lg border">
          {type.fields.map((f) => (
            <li key={f.key} data-field={f.key} className="grid gap-0.5 px-3 py-2 text-sm">
              <span className="flex flex-wrap items-baseline justify-between gap-2">
                <span className="font-medium">{f.title || f.key}</span>
                <span className="text-xs text-muted-foreground">{tp.kinds[f.kind]}</span>
              </span>
              {f.options && f.options.length > 0 && <span className="text-xs text-muted-foreground">{f.options.join(', ')}</span>}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
