import { useState } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { useAddGroup, useEffectiveMembers, useRemoveGroup, useSetGroupRole } from '@/api/hooks'
import type { GroupMember, SpaceInfo, SpaceRole } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
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
import { NativeSelect } from '@/components/ui/native-select'
import { AddPeopleDialog } from '@/components/people/AddPeopleDialog'
import { RowMenu } from '@/components/people/RowMenu'
import { useDebounced } from '@/lib/useDebounced'
import { t } from '@/i18n'
import { tp } from '@/i18n/people'
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
  const [adding, setAdding] = useState(false)

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
      {manage && !space.personal && (
        <div className="flex justify-end">
          <Button type="button" onClick={() => setAdding(true)}>
            <Plus aria-hidden="true" />
            {tp.add}
          </Button>
        </div>
      )}
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
                <RowMenu
                  label={t.fmt.labelled(tp.list.more, g.name)}
                  actions={[{ label: tgs.remove, icon: <Trash2 />, onSelect: () => setAsking(g), destructive: true }]}
                />
              )}
            </li>
          ))}
        </ul>
      )}
      {manage && !space.personal && (
        <AddGroupDialog space={space.id} taken={groups.map((g) => g.groupId)} open={adding} onOpenChange={setAdding} />
      )}
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

// Finding groups by name (anyone signed in may), several at once, and giving them a role.
function AddGroupDialog({
  space,
  taken,
  open,
  onOpenChange,
}: {
  space: string
  taken: string[]
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const add = useAddGroup(space)
  return (
    <AddPeopleDialog
      open={open}
      onOpenChange={onOpenChange}
      title={tgs.addTitle}
      description={tp.addGroupsHelp}
      kind="groups"
      roles={ROLES.map((r) => ({ value: r, label: t.roles[r] }))}
      defaultRole="viewer"
      excluded={new Set(taken)}
      pick={async (picks, role) => {
        for (const g of picks) {
          try {
            await add.mutateAsync({ groupId: g.id, role: role as SpaceRole })
          } catch (e) {
            throw new Error(`${g.label}: ${groupMemberError(e)}`, { cause: e })
          }
        }
      }}
    />
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
