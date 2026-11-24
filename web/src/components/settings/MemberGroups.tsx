import { useState, type FormEvent } from 'react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { useAddGroup, useEffectiveMembers, useGroupSearch, useRemoveGroup, useSetGroupRole } from '@/api/hooks'
import type { GroupMember, GroupRef, SpaceInfo, SpaceRole } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { useDebounced } from '@/lib/useDebounced'
import { t } from '@/i18n'
import { tg } from '@/i18n/groups'
import { SettingsSection } from './SettingsParts'

const tgs = tg.space
const ROLES: readonly SpaceRole[] = ['owner', 'editor', 'viewer']

// What a refusal of a group call means, in plain words.
export function groupMemberError(e: unknown): string {
  if (!(e instanceof ApiError)) return t.members.failed
  switch (e.code) {
    case 'group_not_found':
    case 'not_found':
      return tgs.errors.notFound
    case 'exists':
      return tgs.errors.exists
    case 'group_limit':
      return tgs.errors.limit
    case 'personal_space':
      return tgs.errors.personalSpace
    case 'no_permission':
      return t.members.noManage
    default:
      return e.message || t.members.failed
  }
}

// The groups that are members of a space, with their roles; whoever may change members
// also changes them here and adds more (shared spaces only).
export function GroupMembers({ space, groups, manage }: { space: SpaceInfo; groups: GroupMember[]; manage: boolean }) {
  const setRole = useSetGroupRole(space.id)
  const remove = useRemoveGroup(space.id)
  const [error, setError] = useState('')
  const [asking, setAsking] = useState<GroupMember | null>(null)

  const change = async (g: GroupMember, role: SpaceRole) => {
    if (role === g.role) return
    setError('')
    try {
      await setRole.mutateAsync({ groupId: g.groupId, role })
      toast.success(tgs.changed)
    } catch (e) {
      setError(groupMemberError(e))
    }
  }
  const confirm = async () => {
    const g = asking
    if (!g) return
    setError('')
    try {
      await remove.mutateAsync(g.groupId)
      toast.success(tgs.removed(g.name))
    } catch (e) {
      setError(groupMemberError(e))
    }
    setAsking(null)
  }

  return (
    <SettingsSection title={tgs.title} id="space-groups-heading" description={tgs.help}>
      <FormMessage>{error}</FormMessage>
      {groups.length === 0 ? (
        <p className="text-sm text-muted-foreground">{tgs.none}</p>
      ) : (
        <ul className="divide-y rounded-lg border" data-slot="space-groups">
          {groups.map((g) => (
            <li
              key={g.groupId}
              className="flex flex-wrap items-center gap-x-4 gap-y-2 px-3 py-2.5"
              data-group={g.groupId}
            >
              <div className="min-w-0 flex-1 basis-48">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="truncate text-sm font-medium">{g.name}</span>
                  <Badge variant={g.source === 'synced' ? 'secondary' : 'outline'}>{tg.sourceNames[g.source]}</Badge>
                  {g.yours && <span className="text-xs text-muted-foreground">{tgs.yours}</span>}
                </div>
                <div className="text-xs text-muted-foreground">{tgs.people(g.people)}</div>
              </div>
              <div className="w-32">
                {manage ? (
                  <NativeSelect
                    className="h-8"
                    aria-label={t.fmt.labelled(g.name, tgs.role)}
                    value={g.role}
                    disabled={setRole.isPending}
                    onChange={(e) => void change(g, e.target.value as SpaceRole)}
                  >
                    {ROLES.map((r) => (
                      <option key={r} value={r}>
                        {t.roles[r]}
                      </option>
                    ))}
                  </NativeSelect>
                ) : (
                  <span className="text-sm">{t.roles[g.role]}</span>
                )}
              </div>
              {manage && (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  aria-label={t.fmt.labelled(tgs.remove, g.name)}
                  onClick={() => setAsking(g)}
                >
                  {tgs.remove}
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {manage && !space.personal && <AddGroup space={space.id} taken={groups.map((g) => g.groupId)} />}
      <AlertDialog open={asking !== null} onOpenChange={(o) => !o && setAsking(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tgs.removeTitle}</AlertDialogTitle>
            <AlertDialogDescription>{asking ? tgs.removeHelp(asking.name) : ''}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t.members.cancel}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={() => void confirm()}>
              {tgs.removeConfirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsSection>
  )
}

// Finding a group by name (anyone signed in may) and giving it a role.
function AddGroup({ space, taken }: { space: string; taken: string[] }) {
  const add = useAddGroup(space)
  const [term, setTerm] = useState('')
  const found = useGroupSearch(useDebounced(term.trim(), 250))
  const [chosen, setChosen] = useState<GroupRef | null>(null)
  const [role, setRole] = useState<SpaceRole>('viewer')
  const [error, setError] = useState('')
  const matches = (found.data?.groups ?? []).filter((g) => !taken.includes(g.id))

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (add.isPending) return
    if (!chosen) return setError(tgs.pick)
    setError('')
    try {
      const g = await add.mutateAsync({ groupId: chosen.id, role })
      toast.success(tgs.added(g.name))
      setChosen(null)
      setTerm('')
    } catch (err) {
      setError(groupMemberError(err))
    }
  }

  return (
    <form
      className="grid gap-3 rounded-lg border p-3"
      onSubmit={(e) => void submit(e)}
      noValidate
      aria-label={tgs.addTitle}
    >
      <div>
        <h3 className="text-sm font-semibold">{tgs.addTitle}</h3>
        <p className="text-xs text-muted-foreground">{tgs.addHelp}</p>
      </div>
      <div className="grid gap-1.5">
        <Label htmlFor="space-group-search">{tgs.search}</Label>
        <Input
          id="space-group-search"
          type="search"
          value={term}
          onChange={(e) => setTerm(e.target.value)}
          placeholder={tgs.searchPlaceholder}
          autoComplete="off"
          spellCheck={false}
          className="max-w-xs"
        />
        {term.trim() === '' ? (
          <p className="text-xs text-muted-foreground">{tgs.searchHint}</p>
        ) : found.isSuccess && matches.length === 0 ? (
          <p className="text-xs text-muted-foreground">{tgs.noMatch}</p>
        ) : null}
      </div>
      {matches.length > 0 && (
        <ul className="max-h-40 divide-y overflow-y-auto rounded-md border" aria-label={tgs.search}>
          {matches.map((g) => (
            <li key={g.id} className="flex items-center justify-between gap-3 px-3 py-1.5 text-sm">
              <span className="min-w-0 truncate">
                {g.name} <span className="text-xs text-muted-foreground">{tg.sourceNames[g.source]}</span>
              </span>
              <Button
                type="button"
                variant={chosen?.id === g.id ? 'secondary' : 'outline'}
                size="sm"
                aria-pressed={chosen?.id === g.id}
                aria-label={t.fmt.labelled(tgs.choose, g.name)}
                onClick={() => setChosen(g)}
              >
                {chosen?.id === g.id ? tgs.chosen : tgs.choose}
              </Button>
            </li>
          ))}
        </ul>
      )}
      <div className="flex flex-wrap items-end gap-3">
        <div className="grid gap-1.5">
          <Label htmlFor="space-group-role">{tgs.role}</Label>
          <div className="w-32">
            <NativeSelect id="space-group-role" value={role} onChange={(e) => setRole(e.target.value as SpaceRole)}>
              {ROLES.map((r) => (
                <option key={r} value={r}>
                  {t.roles[r]}
                </option>
              ))}
            </NativeSelect>
          </div>
        </div>
        <Button type="submit" disabled={add.isPending}>
          {add.isPending && <Spinner />}
          {chosen ? `${add.isPending ? tgs.adding : tgs.add}: ${chosen.name}` : add.isPending ? tgs.adding : tgs.add}
        </Button>
      </div>
      <FormMessage>{error}</FormMessage>
    </form>
  )
}

// Everyone who can reach the space and how, a page at a time: loaded when asked for.
export function EffectiveMembers({ space }: { space: string }) {
  const [shown, setShown] = useState(false)
  const [text, setText] = useState('')
  const q = useDebounced(text.trim(), 300)
  const list = useEffectiveMembers(space, q, shown)
  const items = list.data?.pages.flatMap((p) => p.items) ?? []
  return (
    <SettingsSection title={tgs.effectiveTitle} id="effective-members-heading" description={tgs.effectiveHelp}>
      <div>
        <Button type="button" variant="outline" size="sm" aria-expanded={shown} onClick={() => setShown((s) => !s)}>
          {shown ? tgs.effectiveHide : tgs.effectiveShow}
        </Button>
      </div>
      {shown && (
        <div className="grid gap-3">
          <Input
            type="search"
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder={tgs.effectiveSearchPlaceholder}
            aria-label={tgs.effectiveSearch}
            className="max-w-xs"
            autoComplete="off"
          />
          {list.isPending ? (
            <PageFallback />
          ) : list.isError && !list.data ? (
            <ErrorState
              message={t.fmt.labelled(tgs.effectiveLoadFailed, list.error.message)}
              onRetry={() => void list.refetch()}
            />
          ) : items.length === 0 ? (
            <p className="text-sm text-muted-foreground">{q ? tgs.effectiveNoMatch(q) : tgs.effectiveNone}</p>
          ) : (
            <ul className="divide-y rounded-lg border" data-slot="effective-members">
              {items.map((m) => (
                <li
                  key={m.userId}
                  className="flex flex-wrap items-center gap-x-4 gap-y-1 px-3 py-2"
                  data-user={m.userId}
                >
                  <div className="min-w-0 flex-1 basis-48">
                    <div className="flex items-center gap-2">
                      <span className="truncate text-sm font-medium">{m.name}</span>
                      {m.you && <span className="text-xs text-muted-foreground">{t.members.you}</span>}
                    </div>
                    <div className="truncate text-xs text-muted-foreground">{m.username}</div>
                  </div>
                  <div className="min-w-0 text-xs text-muted-foreground">
                    <div>
                      {tgs.direct}: {m.directRole ? t.roles[m.directRole] : tgs.noDirect}
                    </div>
                    {m.groups.length > 0 && (
                      <div className="break-words">
                        {tgs.viaLabel}: {m.groups.map((g) => `${g.name} (${t.roles[g.role]})`).join(', ')}
                      </div>
                    )}
                  </div>
                  <Badge variant="secondary">{m.role ? t.roles[m.role] : ''}</Badge>
                </li>
              ))}
            </ul>
          )}
          {list.hasNextPage && (
            <div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={list.isFetchingNextPage}
                onClick={() => void list.fetchNextPage()}
              >
                {list.isFetchingNextPage ? tgs.loadingMore : tgs.loadMore}
              </Button>
            </div>
          )}
        </div>
      )}
    </SettingsSection>
  )
}
