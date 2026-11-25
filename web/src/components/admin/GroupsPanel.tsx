import { useMemo, useState, type FormEvent, useRef } from 'react'
import { scrollFadeTouchClass, useScrollFade } from '@/components/ui/scroll-fade'
import { Search } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import {
  useAddGroupPeople,
  useAdminGroups,
  useCreateGroup,
  useDeleteGroup,
  useGroupPeople,
  useGroupSpaces,
  useRemoveGroupPerson,
  useUpdateGroup,
  useUserSearch,
  useUsers,
  USER_SEARCH_LIMIT,
} from '@/api/admin'
import type { Group, GroupSource } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import { useDebounced } from '@/lib/useDebounced'
import { t } from '@/i18n'
import { tg } from '@/i18n/groups'

const ta = tg.admin
const MAX_ADD = 500

// What a refusal of a group call means, in plain words.
export function groupError(e: unknown): string {
  if (!(e instanceof ApiError)) return ta.failed
  switch (e.code) {
    case 'exists':
      return ta.errors.exists
    case 'group_limit':
      return e.message || ta.errors.groupLimit
    case 'group_synced':
      return ta.errors.groupSynced
    case 'user_not_found':
      return ta.errors.userNotFound
    case 'not_found':
    case 'group_not_found':
      return ta.errors.notFound
    case 'invalid':
      return e.message || ta.errors.invalid
    default:
      return e.message || ta.failed
  }
}

// Every group on the server, a page at a time. Opening one shows its details, its
// people and the spaces it is in; deleting is asked about, and forced only when
// the group is still in use.
export function GroupsPanel() {
  const fadeRef = useRef<HTMLDivElement>(null)
  useScrollFade(fadeRef)
  const [text, setText] = useState('')
  const [source, setSource] = useState<GroupSource | ''>('')
  const q = useDebounced(text.trim(), 300)
  const groups = useAdminGroups(q, source)
  const [creating, setCreating] = useState(false)
  const [openId, setOpenId] = useState<string | null>(null)
  const items = groups.data?.pages.flatMap((p) => p.items) ?? []
  // The group being managed is read from the list, so a change shows at once.
  const open = items.find((g) => g.id === openId) ?? null

  return (
    <SettingsSection title={ta.title} id="admin-groups-heading" description={ta.help}>
      <div className="flex flex-wrap items-end gap-3">
        <div className="relative min-w-48 max-w-sm flex-1">
          <Search
            aria-hidden="true"
            className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
          />
          <Input
            type="search"
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder={ta.searchPlaceholder}
            aria-label={ta.search}
            className="pl-8"
            autoComplete="off"
          />
        </div>
        <div className="w-40">
          <NativeSelect
            aria-label={ta.source}
            value={source}
            onChange={(e) => setSource(e.target.value as GroupSource | '')}
          >
            <option value="">{ta.sourceAll}</option>
            <option value="local">{tg.sourceNames.local}</option>
            <option value="synced">{tg.sourceNames.synced}</option>
          </NativeSelect>
        </div>
        <Button type="button" onClick={() => setCreating(true)}>
          {ta.create}
        </Button>
      </div>

      {groups.isPending ? (
        <PageFallback />
      ) : groups.isError && !groups.data ? (
        <ErrorState
          message={t.fmt.labelled(ta.loadFailed, groups.error.message)}
          onRetry={() => void groups.refetch()}
        />
      ) : items.length === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground">{q ? ta.noMatch(q) : ta.empty}</p>
      ) : (
        <>
          <div ref={fadeRef} className={`${scrollFadeTouchClass} rounded-lg border`}>
            <table className="w-full min-w-[36rem] text-left text-sm">
              <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
                <tr>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {ta.name}
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {ta.source}
                  </th>
                  <th scope="col" className="px-3 py-2 text-right font-medium">
                    {ta.people}
                  </th>
                  <th scope="col" className="px-3 py-2 text-right font-medium">
                    {ta.spaces}
                  </th>
                  <th scope="col" className="px-3 py-2 text-right font-medium">
                    {ta.actions}
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y">
                {items.map((g) => (
                  <tr key={g.id} data-group={g.id}>
                    <th scope="row" className="max-w-64 px-3 py-2.5 font-normal">
                      <div className="truncate font-medium">{g.name}</div>
                      {g.description && <div className="truncate text-xs text-muted-foreground">{g.description}</div>}
                    </th>
                    <td className="px-3 py-2.5 whitespace-nowrap">
                      <Badge variant={g.source === 'synced' ? 'secondary' : 'outline'}>
                        {tg.sourceNames[g.source]}
                      </Badge>
                    </td>
                    <td className="px-3 py-2.5 text-right tabular-nums">{g.people}</td>
                    <td className="px-3 py-2.5 text-right tabular-nums">{g.spaces}</td>
                    <td className="px-3 py-2.5 text-right">
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        aria-label={t.fmt.labelled(ta.open, g.name)}
                        onClick={() => setOpenId(g.id)}
                      >
                        {ta.open}
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="flex flex-col items-center gap-2">
            {groups.hasNextPage && (
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={groups.isFetchingNextPage}
                onClick={() => void groups.fetchNextPage()}
              >
                {groups.isFetchingNextPage && <Spinner />}
                {groups.isFetchingNextPage ? ta.loadingMore : ta.loadMore}
              </Button>
            )}
            <span className="text-xs text-muted-foreground">
              {groups.hasNextPage ? ta.loaded(items.length) : ta.allLoaded(items.length)}
            </span>
          </div>
        </>
      )}

      {creating && <CreateDialog onClose={() => setCreating(false)} onCreated={(g) => setOpenId(g.id)} />}
      {open && <GroupDialog group={open} onClose={() => setOpenId(null)} />}
    </SettingsSection>
  )
}

function CreateDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (g: Group) => void }) {
  const create = useCreateGroup()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [source, setSource] = useState<GroupSource>('local')
  const [error, setError] = useState('')
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (create.isPending) return
    if (name.trim() === '') return setError(ta.nameRequired)
    setError('')
    try {
      const g = await create.mutateAsync({ name: name.trim(), description: description.trim(), source })
      toast.success(ta.created(g.name))
      onClose()
      onCreated(g)
    } catch (err) {
      setError(groupError(err))
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form className="grid gap-4" onSubmit={(e) => void submit(e)} noValidate>
          <DialogHeader>
            <DialogTitle>{ta.createTitle}</DialogTitle>
            <DialogDescription>{ta.createHelp}</DialogDescription>
          </DialogHeader>
          <Field
            label={ta.nameLabel}
            requiredMark
            help={ta.nameHelp}
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={80}
            autoComplete="off"
          />
          <Field
            label={ta.description}
            help={ta.descriptionHelp}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            maxLength={500}
            autoComplete="off"
          />
          <div className="grid gap-1.5">
            <Label htmlFor="group-source">{ta.sourceLabel}</Label>
            <NativeSelect
              id="group-source"
              value={source}
              onChange={(e) => setSource(e.target.value as GroupSource)}
              aria-describedby="group-source-help"
            >
              <option value="local">{tg.sourceNames.local}</option>
              <option value="synced">{tg.sourceNames.synced}</option>
            </NativeSelect>
            <p id="group-source-help" className="text-xs text-muted-foreground">
              {tg.sourceHelp[source]}
            </p>
          </div>
          <FormMessage>{error}</FormMessage>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {ta.cancel}
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending && <Spinner />}
              {create.isPending ? ta.creating : ta.create}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// One group: its name and description, its people and the spaces it is in.
function GroupDialog({ group, onClose }: { group: Group; onClose: () => void }) {
  const [asking, setAsking] = useState(false)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{group.name}</DialogTitle>
          <DialogDescription>
            {tg.sourceNames[group.source]} · {tg.sourceHelp[group.source]}
          </DialogDescription>
        </DialogHeader>
        <Details group={group} />
        <People group={group} />
        <GroupSpaces group={group} />
        <DialogFooter className="sm:justify-between">
          <Button type="button" variant="outline" className="text-destructive" onClick={() => setAsking(true)}>
            {ta.delete}
          </Button>
          <Button type="button" variant="outline" onClick={onClose}>
            {ta.close}
          </Button>
        </DialogFooter>
        {asking && <DeleteDialog group={group} onClose={() => setAsking(false)} onDeleted={onClose} />}
      </DialogContent>
    </Dialog>
  )
}

function Details({ group }: { group: Group }) {
  const update = useUpdateGroup()
  const [name, setName] = useState(group.name)
  const [description, setDescription] = useState(group.description)
  const [error, setError] = useState('')
  const dirty = name.trim() !== group.name || description.trim() !== group.description
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (update.isPending || !dirty) return
    if (name.trim() === '') return setError(ta.nameRequired)
    setError('')
    try {
      await update.mutateAsync({ id: group.id, name: name.trim(), description: description.trim() })
      toast.success(ta.saved)
    } catch (err) {
      setError(groupError(err))
    }
  }
  return (
    <form className="grid gap-3" onSubmit={(e) => void submit(e)} noValidate aria-label={ta.details}>
      <h3 className="text-sm font-semibold">{ta.details}</h3>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field
          label={ta.nameLabel}
          value={name}
          onChange={(e) => setName(e.target.value)}
          maxLength={80}
          autoComplete="off"
        />
        <Field
          label={ta.description}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          maxLength={500}
          autoComplete="off"
        />
      </div>
      {group.mappedFrom.length > 0 && (
        <p className="text-xs text-muted-foreground">
          {ta.mapped}: {group.mappedFrom.map((m) => ta.fromProvider(m.provider, m.value)).join(' · ')}
        </p>
      )}
      <FormMessage>{error}</FormMessage>
      <div>
        <Button type="submit" size="sm" disabled={!dirty || update.isPending}>
          {update.isPending && <Spinner />}
          {update.isPending ? ta.saving : ta.save}
        </Button>
      </div>
    </form>
  )
}

function People({ group }: { group: Group }) {
  const [text, setText] = useState('')
  const q = useDebounced(text.trim(), 300)
  const people = useGroupPeople(group.id, q)
  const remove = useRemoveGroupPerson()
  const [error, setError] = useState('')
  const items = people.data?.pages.flatMap((p) => p.items) ?? []
  const local = group.source === 'local'
  const drop = async (userId: string, name: string) => {
    setError('')
    try {
      await remove.mutateAsync({ id: group.id, userId })
      toast.success(ta.personRemoved(name))
    } catch (err) {
      setError(groupError(err))
    }
  }
  return (
    <section className="grid gap-3" aria-labelledby="group-people-heading">
      <h3 id="group-people-heading" className="text-sm font-semibold">
        {ta.peopleTitle} <span className="font-normal text-muted-foreground tabular-nums">({group.people})</span>
      </h3>
      {!local && <p className="text-xs text-muted-foreground">{ta.syncedPeopleNote}</p>}
      <Input
        type="search"
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder={ta.peopleSearch}
        aria-label={ta.peopleSearch}
        className="max-w-xs"
        autoComplete="off"
      />
      <FormMessage>{error}</FormMessage>
      {people.isPending ? (
        <PageFallback />
      ) : people.isError && !people.data ? (
        <ErrorState message={people.error.message} onRetry={() => void people.refetch()} />
      ) : items.length === 0 ? (
        <p className="text-sm text-muted-foreground">{q ? ta.noPeopleMatch(q) : ta.noPeople}</p>
      ) : (
        <ul className="divide-y rounded-lg border">
          {items.map((p) => (
            <li key={p.userId} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
              <div className="min-w-0 flex-1 basis-40">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="truncate text-sm font-medium">{p.name}</span>
                  {p.disabled && <Badge variant="outline">{ta.disabledUser}</Badge>}
                </div>
                <div className="truncate text-xs text-muted-foreground">{p.username}</div>
                {p.from.length > 0 && (
                  <div className="truncate text-xs text-muted-foreground">
                    {ta.from} {p.from.map((f) => ta.fromProvider(f.provider, f.value)).join(' · ')}
                  </div>
                )}
              </div>
              {local && (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  aria-label={ta.removePersonLabel(p.name)}
                  disabled={remove.isPending}
                  onClick={() => void drop(p.userId, p.name)}
                >
                  {ta.removePerson}
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {people.hasNextPage && (
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={people.isFetchingNextPage}
            onClick={() => void people.fetchNextPage()}
          >
            {people.isFetchingNextPage ? ta.loadingMore : ta.loadMore}
          </Button>
        </div>
      )}
      {local && <AddPeople group={group} />}
    </section>
  )
}

// Adding people to a local group: pick them from a search, or paste usernames.
function AddPeople({ group }: { group: Group }) {
  const add = useAddGroupPeople()
  const [term, setTerm] = useState('')
  const found = useUserSearch(useDebounced(term.trim(), 250))
  const [picked, setPicked] = useState<Map<string, string>>(new Map())
  const [pasted, setPasted] = useState('')
  const everyone = useUsers(pasted.trim() !== '')
  const [error, setError] = useState('')
  const matches = found.data?.users ?? []
  const truncated = matches.length >= USER_SEARCH_LIMIT

  // The pasted usernames that are accounts, and those that are not.
  const resolved = useMemo(() => {
    const names = pasted
      .split(/\r?\n/)
      .map((l) => l.trim().toLowerCase())
      .filter(Boolean)
    const byName = new Map((everyone.data?.users ?? []).map((u) => [u.username, u]))
    const known = names.map((n) => byName.get(n)).filter((u): u is NonNullable<typeof u> => u !== undefined)
    const unknown = everyone.data ? names.filter((n) => !byName.has(n)) : []
    return { ids: known.map((u) => u.id), unknown }
  }, [pasted, everyone.data])

  const send = async (ids: string[]) => {
    if (add.isPending || ids.length === 0) return
    if (ids.length > MAX_ADD) return setError(ta.tooMany)
    setError('')
    try {
      const r = await add.mutateAsync({ id: group.id, userIds: ids })
      toast.success(ta.addedPeople(r.added, r.already))
      setPicked(new Map())
      setPasted('')
    } catch (err) {
      setError(groupError(err))
    }
  }
  const toggle = (id: string, label: string, on: boolean) =>
    setPicked((m) => {
      const next = new Map(m)
      if (on) next.set(id, label)
      else next.delete(id)
      return next
    })

  return (
    <div className="grid gap-3 rounded-lg border p-3" data-slot="add-people">
      <div>
        <h4 className="text-sm font-medium">{ta.addPeople}</h4>
        <p className="text-xs text-muted-foreground">{ta.addPeopleHelp}</p>
      </div>
      <div className="grid gap-1.5">
        <Label htmlFor="group-pick-search">{ta.pickSearch}</Label>
        <Input
          id="group-pick-search"
          type="search"
          value={term}
          onChange={(e) => setTerm(e.target.value)}
          placeholder={ta.pickPlaceholder}
          autoComplete="off"
          spellCheck={false}
          className="max-w-xs"
        />
      </div>
      {found.isSuccess && matches.length === 0 && <p className="text-xs text-muted-foreground">{ta.pickNone}</p>}
      {matches.length > 0 && (
        <ul className="max-h-48 divide-y overflow-y-auto rounded-md border" aria-label={ta.pickSearch}>
          {matches.map((u) => (
            <li key={u.id}>
              <label className="flex cursor-pointer items-center gap-3 px-3 py-1.5 text-sm">
                <input
                  type="checkbox"
                  checked={picked.has(u.id)}
                  onChange={(e) => toggle(u.id, u.name, e.target.checked)}
                  aria-label={`${u.name} (${u.username})`}
                />
                <span className="min-w-0 truncate">
                  {u.name} <span className="text-xs text-muted-foreground">{u.username}</span>
                </span>
              </label>
            </li>
          ))}
        </ul>
      )}
      {truncated && <p className="text-xs text-muted-foreground">{ta.pickNarrow(USER_SEARCH_LIMIT)}</p>}
      <div className="flex flex-wrap items-center gap-3">
        <Button
          type="button"
          size="sm"
          disabled={picked.size === 0 || add.isPending}
          onClick={() => void send([...picked.keys()])}
        >
          {add.isPending && <Spinner />}
          {picked.size > 0 ? ta.addSelected(picked.size) : ta.adding.replace('…', '')}
        </Button>
        {picked.size > 0 && <span className="text-xs text-muted-foreground">{ta.pickSelected(picked.size)}</span>}
      </div>
      <div className="grid gap-1.5">
        <Label htmlFor="group-paste">{ta.pasteLabel}</Label>
        <Textarea
          id="group-paste"
          value={pasted}
          onChange={(e) => setPasted(e.target.value)}
          rows={3}
          spellCheck={false}
          autoCapitalize="none"
          className="font-mono text-xs"
        />
        {resolved.unknown.length > 0 && (
          <p className="text-xs text-destructive">{ta.pasteUnknown(resolved.unknown.join(', '))}</p>
        )}
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={resolved.ids.length === 0 || resolved.unknown.length > 0 || add.isPending}
            onClick={() => void send(resolved.ids)}
          >
            {ta.pasteAdd}
          </Button>
        </div>
      </div>
      <FormMessage>{error}</FormMessage>
    </div>
  )
}

function GroupSpaces({ group }: { group: Group }) {
  const spaces = useGroupSpaces(group.id)
  const items = spaces.data?.pages.flatMap((p) => p.items) ?? []
  return (
    <section className="grid gap-2" aria-labelledby="group-spaces-heading">
      <h3 id="group-spaces-heading" className="text-sm font-semibold">
        {ta.spacesTitle} <span className="font-normal text-muted-foreground tabular-nums">({group.spaces})</span>
      </h3>
      <p className="text-xs text-muted-foreground">{ta.spacesHelp}</p>
      {spaces.isPending ? (
        <PageFallback />
      ) : spaces.isError && !spaces.data ? (
        <ErrorState message={spaces.error.message} onRetry={() => void spaces.refetch()} />
      ) : items.length === 0 ? (
        <p className="text-sm text-muted-foreground">{ta.noSpaces}</p>
      ) : (
        <ul className="divide-y rounded-lg border">
          {items.map((s) => (
            <li key={s.id} className="flex items-center justify-between gap-3 px-3 py-2 text-sm">
              <span className="min-w-0 truncate">{s.name}</span>
              <Badge variant="outline">{t.roles[s.role]}</Badge>
            </li>
          ))}
        </ul>
      )}
      {spaces.hasNextPage && (
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={spaces.isFetchingNextPage}
            onClick={() => void spaces.fetchNextPage()}
          >
            {spaces.isFetchingNextPage ? ta.loadingMore : ta.loadMore}
          </Button>
        </div>
      )}
    </section>
  )
}

function DeleteDialog({ group, onClose, onDeleted }: { group: Group; onClose: () => void; onDeleted: () => void }) {
  const del = useDeleteGroup()
  const [error, setError] = useState('')
  // Set when the server says the group is still in use: the numbers, and a second, forced, button.
  const [inUse, setInUse] = useState<{ spaces: number; mappings: number } | null>(null)
  const run = async (force: boolean) => {
    if (del.isPending) return
    setError('')
    try {
      await del.mutateAsync({ id: group.id, force })
      toast.success(ta.deleted(group.name))
      onClose()
      onDeleted()
    } catch (err) {
      if (err instanceof ApiError && err.code === 'group_in_use') {
        const b = err.body as { spaces?: number; mappings?: number } | undefined
        setInUse({ spaces: b?.spaces ?? 0, mappings: b?.mappings ?? 0 })
      } else {
        setError(groupError(err))
      }
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{ta.deleteTitle(group.name)}</DialogTitle>
          <DialogDescription>{inUse ? ta.deleteInUse(inUse.spaces, inUse.mappings) : ta.deleteHelp}</DialogDescription>
        </DialogHeader>
        <FormMessage>{error}</FormMessage>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            {ta.cancel}
          </Button>
          <Button type="button" variant="destructive" disabled={del.isPending} onClick={() => void run(inUse !== null)}>
            {del.isPending && <Spinner />}
            {inUse ? ta.deleteForce : ta.deleteConfirm}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
