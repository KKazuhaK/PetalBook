import { useState, type ReactNode } from 'react'
import { toast } from 'sonner'
import { useGroupSearch } from '@/api/hooks'
import {
  useBatchRunner,
  usePeopleSearch,
  type BatchAnswer,
  type BatchRunner,
  type PeopleScope,
  type PersonRef,
} from '@/api/people'
import { FormMessage } from '@/components/auth/Field'
import { Spinner } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { useDebounced } from '@/lib/useDebounced'
import { tp } from '@/i18n/people'
import { CsvBody, useCsvImport, type RoleOption } from './csvImport'
import { DialogTabs, ResponsiveDialog } from './ResponsiveDialog'

// Something to pick: a person (by username, which is what a batch names) or a group (by id).
export interface Pick {
  id: string
  label: string
  sub?: string
  // What the add action is given for it.
  key: string
}

export type Tab = 'pick' | 'csv' | 'extra'

function usePicks(kind: 'people' | 'groups', q: string, scope?: PeopleScope) {
  const text = useDebounced(q.trim(), 250)
  const people = usePeopleSearch(text, kind === 'people', scope)
  const groups = useGroupSearch(text, kind === 'groups')
  const enough = [...text].length >= (kind === 'people' ? 2 : 1)
  const query = kind === 'people' ? people : groups
  const items: Pick[] = !enough
    ? []
    : kind === 'people'
      ? (people.data?.people ?? []).map((p: PersonRef) => ({
          id: p.id,
          key: p.username,
          label: p.name,
          sub: p.username,
        }))
      : (groups.data?.groups ?? []).map((g) => ({ id: g.id, key: g.id, label: g.name }))
  return { items, enough, pending: enough && query.isFetching && items.length === 0, error: enough && query.isError }
}

function PickBody({
  kind,
  selected,
  setSelected,
  excluded,
  scope,
}: {
  kind: 'people' | 'groups'
  selected: Map<string, Pick>
  setSelected: (m: Map<string, Pick>) => void
  excluded: ReadonlySet<string>
  scope?: PeopleScope
}) {
  const [q, setQ] = useState('')
  const found = usePicks(kind, q, scope)
  const toggle = (p: Pick) => {
    const next = new Map(selected)
    if (next.has(p.id)) next.delete(p.id)
    else next.set(p.id, p)
    setSelected(next)
  }
  return (
    <div className="grid gap-3">
      <div className="grid gap-1.5">
        <Label htmlFor="add-people-search">{kind === 'people' ? tp.searchPeople : tp.searchGroups}</Label>
        <Input
          id="add-people-search"
          autoFocus
          type="search"
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          placeholder={kind === 'people' ? tp.searchPeoplePlaceholder : tp.searchGroupsPlaceholder}
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        {kind === 'people' && <p className="text-xs text-muted-foreground">{tp.tenantOnly}</p>}
      </div>
      <div className="min-h-24 rounded-lg border" aria-live="polite">
        {!found.enough ? (
          <p className="p-3 text-sm text-muted-foreground">{kind === 'people' ? tp.minChars : tp.typeToSearch}</p>
        ) : found.error ? (
          <p className="p-3 text-sm text-destructive">{tp.searchFailed}</p>
        ) : found.pending ? (
          <div className="p-3">
            <Spinner />
          </div>
        ) : found.items.length === 0 ? (
          <p className="p-3 text-sm text-muted-foreground">{tp.noResult}</p>
        ) : (
          <ul className="max-h-56 divide-y overflow-y-auto">
            {found.items.map((p) => {
              const already = excluded.has(p.id)
              return (
                <li key={p.id}>
                  <label
                    className={`flex min-h-11 cursor-pointer items-center gap-3 px-3 py-2 text-sm ${already ? 'opacity-60' : 'hover:bg-muted/50'}`}
                  >
                    <input
                      type="checkbox"
                      className="size-4 shrink-0 accent-[var(--brand)]"
                      checked={already || selected.has(p.id)}
                      disabled={already}
                      onChange={() => toggle(p)}
                    />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate font-medium">{p.label}</span>
                      {p.sub && <span className="block truncate text-xs text-muted-foreground">{p.sub}</span>}
                    </span>
                    {already && <span className="text-xs text-muted-foreground">{tp.alreadyIn}</span>}
                  </label>
                </li>
              )
            })}
          </ul>
        )}
      </div>
      {selected.size > 0 && (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="font-medium">{tp.selected(selected.size)}</span>
          <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
            {[...selected.values()].map((p) => p.label).join('、')}
          </span>
          <Button type="button" variant="ghost" size="xs" onClick={() => setSelected(new Map())}>
            {tp.clearSelected}
          </Button>
        </div>
      )}
    </div>
  )
}

// Adding people (or groups) from a dialog: a picker of the tenant's own people or groups with several choices and
// a role, and a CSV file that is checked row by row before it is applied. `pick` adds the choices; `csvRun`
// sends the rows to the server's batch endpoint (a dry run to check, a real one to apply).
export function AddPeopleDialog(props: AddPeopleProps) {
  // Mounted only while open: a dialog opened again starts clean.
  return props.open ? <AddPeopleInner {...props} /> : null
}

interface AddPeopleProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description?: string
  kind: 'people' | 'groups'
  roles?: RoleOption[]
  defaultRole?: string
  excluded?: ReadonlySet<string>
  // Where the picker searches people (the space or group being managed; the caller's own tenant by default).
  scope?: PeopleScope
  // Adds the picked ones with the chosen role; a refusal is thrown and shown in the dialog.
  pick?: (picks: Pick[], role: string) => Promise<void>
  csvRun?: BatchRunner
  csvHelp?: string
  mailPage?: string
  // A tab of its own after the others (the invitation link of a tenant).
  extra?: { label: string; body: ReactNode }
  // The tab it opens on (the first it has by default).
  startOn?: Tab
}

function AddPeopleInner({
  open,
  onOpenChange,
  title,
  description,
  kind,
  roles,
  defaultRole = '',
  excluded = new Set<string>(),
  scope,
  pick,
  csvRun,
  csvHelp,
  mailPage,
  extra,
  startOn,
}: AddPeopleProps) {
  const [tab, setTab] = useState<Tab>(startOn ?? (pick ? 'pick' : csvRun ? 'csv' : 'extra'))
  const [selected, setSelected] = useState<Map<string, Pick>>(new Map())
  const [role, setRole] = useState(defaultRole)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const run = useBatchRunner(csvRun ?? (() => Promise.reject(new Error('no batch'))))
  const csv = useCsvImport(
    (rows, dryRun) => run.mutateAsync({ rows, dryRun }) as Promise<BatchAnswer>,
    roles,
    defaultRole,
  )

  const tabs: { id: Tab; label: string }[] = []
  if (pick) tabs.push({ id: 'pick', label: tp.tabPick })
  if (csvRun) tabs.push({ id: 'csv', label: tp.tabCsv })
  if (extra) tabs.push({ id: 'extra', label: extra.label })

  const doPick = async () => {
    if (!pick || busy || selected.size === 0) return
    setBusy(true)
    setError('')
    try {
      await pick([...selected.values()], role)
      toast.success(tp.addedN(selected.size))
      onOpenChange(false)
    } catch (e) {
      setError(e instanceof Error ? e.message : tp.failed)
    }
    setBusy(false)
  }

  const cancel = (
    <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
      {tp.cancel}
    </Button>
  )
  let footer: ReactNode
  if (tab === 'pick')
    footer = (
      <>
        {cancel}
        <Button type="button" disabled={busy || selected.size === 0} onClick={() => void doPick()}>
          {busy ? tp.adding : selected.size > 0 ? tp.addSelected(selected.size) : tp.addPeople}
        </Button>
      </>
    )
  else if (tab === 'csv') {
    const done = csv.answer !== null && !csv.answer.dryRun
    footer = done ? (
      <Button type="button" onClick={() => onOpenChange(false)}>
        {tp.done}
      </Button>
    ) : csv.answer === null ? (
      <>
        {cancel}
        <Button type="button" disabled={csv.busy || csv.text.trim() === ''} onClick={() => void csv.check()}>
          {csv.busy ? tp.csv.checking : tp.csv.check}
        </Button>
      </>
    ) : (
      <>
        <Button type="button" variant="outline" onClick={csv.reset}>
          {tp.csv.again}
        </Button>
        <Button type="button" disabled={csv.busy || csv.actionable === 0} onClick={() => void csv.apply()}>
          {csv.busy ? tp.csv.applying : tp.csv.apply(csv.actionable)}
        </Button>
      </>
    )
  } else
    footer = (
      <Button type="button" onClick={() => onOpenChange(false)}>
        {tp.done}
      </Button>
    )

  return (
    <ResponsiveDialog open={open} onOpenChange={onOpenChange} title={title} description={description} footer={footer}>
      <div className="grid gap-4 py-1">
        {tabs.length > 1 && <DialogTabs tabs={tabs} value={tab} onChange={setTab} />}
        {tab === 'pick' && (
          <>
            <PickBody kind={kind} selected={selected} setSelected={setSelected} excluded={excluded} scope={scope} />
            {roles && (
              <div className="grid gap-1.5 sm:max-w-56">
                <Label htmlFor="add-people-role">{tp.role}</Label>
                <NativeSelect id="add-people-role" value={role} onChange={(e) => setRole(e.target.value)}>
                  {roles.map((r) => (
                    <option key={r.value} value={r.value}>
                      {r.label}
                    </option>
                  ))}
                </NativeSelect>
              </div>
            )}
            <FormMessage>{error}</FormMessage>
          </>
        )}
        {tab === 'csv' && <CsvBody csv={csv} roles={roles} help={csvHelp ?? ''} mailPage={mailPage} />}
        {tab === 'extra' && extra?.body}
      </div>
    </ResponsiveDialog>
  )
}
