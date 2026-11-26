import { useState } from 'react'
import { LogOut, Plus, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import {
  forgetSpace,
  putMember,
  useActiveSpace,
  useMembers,
  useRemoveMember,
  useSession,
  useSetMemberRole,
} from '@/api/hooks'
import type { Member, SpaceInfo, SpaceRole } from '@/api/types'
import { peopleApi, usePickRunner } from '@/api/people'
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
import { Button } from '@/components/ui/button'
import { NativeSelect } from '@/components/ui/native-select'
import { AddPeopleDialog } from '@/components/people/AddPeopleDialog'
import { RowMenu } from '@/components/people/RowMenu'
import { can } from '@/lib/permissions'
import { t } from '@/i18n'
import { tp } from '@/i18n/people'
import { tg } from '@/i18n/groups'
import { useQueryClient } from '@tanstack/react-query'
import { EffectiveMembers, GroupMembers } from './MemberGroups'
import { SettingsNote, SettingsSection } from './SettingsParts'

// The built-in roles, from the one with the most to the one with the least.
const ROLES: readonly SpaceRole[] = ['owner', 'editor', 'viewer']

// What a refusal means, in plain words.
export function memberError(e: unknown): string {
  if (!(e instanceof ApiError)) return t.members.failed
  switch (e.code) {
    case 'user_not_found':
      return t.members.errors.userNotFound
    case 'exists':
      return t.members.errors.exists
    case 'last_owner':
      return t.members.errors.lastOwner
    case 'personal_space':
      return t.members.errors.personalSpace
    case 'member_via_group':
      return e.message
    case 'space_disabled':
      return t.spaces.disabledTitle
    default:
      return e.message || t.members.failed
  }
}

// The people of a space. Anyone who can read the space sees the list and may
// leave; changing it (adding, roles, removing others) is for the `members`
// permission. A personal space is for one person: it says so and has no form.
export function MembersPanel({ space }: { space: string }) {
  const { spaces, resolving } = useActiveSpace()
  const current = spaces.find((s) => s.id === space)
  // The names of a space's people are open to whoever manages them, in a view or not.
  const readable = can(current, 'read') || can(current, 'members')
  const members = useMembers(space, readable)
  if (resolving) return <PageFallback />
  if (!current) return <SettingsNote>{t.editor.spaceMissing}</SettingsNote>
  if (current.disabled) return <DisabledSpace space={current} />
  if (members.isPending) return <PageFallback />
  if (members.isError)
    return (
      <ErrorState
        message={t.fmt.labelled(t.members.loadFailed, members.error.message)}
        onRetry={() => void members.refetch()}
      />
    )
  const manage = can(current, 'members')
  return (
    <div className="space-y-8">
      <MemberList space={current} members={members.data.members} manage={manage} />
      {current.personal ? (
        <SettingsNote>{t.members.personalNote}</SettingsNote>
      ) : (
        !manage && <SettingsNote>{t.members.noManage}</SettingsNote>
      )}
      {!current.personal && <GroupMembers space={current} groups={members.data.groups} manage={manage} />}
      {!current.personal && <EffectiveMembers space={space} />}
    </div>
  )
}

// A space an administrator switched off cannot be opened, so its members cannot be
// listed; the server still lets a member leave it (it only touches the membership).
function DisabledSpace({ space }: { space: SpaceInfo }) {
  const userId = useSession().data?.user?.id
  const remove = useRemoveMember(space.id)
  const [asking, setAsking] = useState(false)
  const [error, setError] = useState('')
  // An administrator who is not a member has nothing to leave; neither has the owner of a personal space.
  const canLeave = !space.personal && space.role !== '' && userId !== undefined
  const leave = async () => {
    setError('')
    try {
      await remove.mutateAsync(userId!)
      forgetSpace(space.id)
      toast.success(t.members.left)
    } catch (e) {
      setError(memberError(e))
    }
    setAsking(false)
  }
  return (
    <div className="space-y-4">
      <SettingsNote>{t.spaces.disabledTitle}</SettingsNote>
      {canLeave && (
        <>
          <p className="text-sm text-muted-foreground">{t.members.disabledLeaveHelp}</p>
          <FormMessage>{error}</FormMessage>
          <Button type="button" variant="outline" size="sm" onClick={() => setAsking(true)}>
            <LogOut aria-hidden="true" />
            {t.members.leave}
          </Button>
          <AlertDialog open={asking} onOpenChange={setAsking}>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>{t.members.leaveTitle}</AlertDialogTitle>
                <AlertDialogDescription>{t.members.leaveHelp(space.name)}</AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>{t.members.cancel}</AlertDialogCancel>
                <AlertDialogAction variant="destructive" onClick={() => void leave()}>
                  {t.members.leaveConfirm}
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </>
      )}
    </div>
  )
}

function MemberList({ space, members, manage }: { space: SpaceInfo; members: Member[]; manage: boolean }) {
  const qc = useQueryClient()
  const setRole = useSetMemberRole(space.id)
  const remove = useRemoveMember(space.id)
  const [error, setError] = useState('')
  const [asking, setAsking] = useState<Member | null>(null)
  // An owner giving up their own ownership is asked first: they cannot take it back.
  const [demoting, setDemoting] = useState<{ member: Member; role: SpaceRole } | null>(null)
  const [adding, setAdding] = useState(false)

  const change = async (m: Member, role: SpaceRole, confirmed = false) => {
    if (role === m.role) return
    if (m.you && m.role === 'owner' && !confirmed) return setDemoting({ member: m, role })
    setError('')
    try {
      putMember(qc, space.id, await setRole.mutateAsync({ user: m.userId, role }))
      toast.success(t.members.changed)
    } catch (e) {
      setError(memberError(e))
    }
  }
  const confirm = async () => {
    const m = asking
    if (!m) return
    setError('')
    try {
      await remove.mutateAsync(m.userId)
      if (m.you) forgetSpace(space.id)
      toast.success(m.you ? t.members.left : t.members.removed(m.name))
    } catch (e) {
      setError(memberError(e))
    }
    setAsking(null)
  }

  return (
    <SettingsSection title={t.members.title} id="members-heading" description={t.members.intro}>
      {manage && !space.personal && (
        <div className="flex justify-end">
          <Button type="button" onClick={() => setAdding(true)}>
            <Plus aria-hidden="true" />
            {tp.add}
          </Button>
        </div>
      )}
      <FormMessage>{error}</FormMessage>
      <ul className="divide-y rounded-lg border">
        {members.map((m) => (
          <li key={m.userId} className="flex flex-wrap items-center gap-x-4 gap-y-2 px-3 py-2.5">
            <div className="min-w-0 flex-1 basis-48">
              <div className="flex items-center gap-2">
                <span className="truncate text-sm font-medium">{m.name}</span>
                {m.you && <span className="text-xs text-muted-foreground">{t.members.you}</span>}
                {m.viaGroups && (
                  <span className="text-xs text-muted-foreground">
                    {tg.space.viaGroups}
                    {m.effectiveRole && m.effectiveRole !== m.role
                      ? ` · ${tg.space.effective(t.roles[m.effectiveRole])}`
                      : ''}
                  </span>
                )}
              </div>
              <div className="truncate text-xs text-muted-foreground">{m.username}</div>
            </div>
            <div className="w-32">
              {manage ? (
                <NativeSelect
                  className="h-8"
                  aria-label={t.fmt.labelled(m.name, t.members.role)}
                  value={m.role}
                  disabled={setRole.isPending}
                  onChange={(e) => void change(m, e.target.value as SpaceRole)}
                >
                  {ROLES.map((r) => (
                    <option key={r} value={r}>
                      {t.roles[r]}
                    </option>
                  ))}
                </NativeSelect>
              ) : (
                <span className="text-sm">{t.roles[m.role]}</span>
              )}
            </div>
            <RowMenu
              label={t.fmt.labelled(tp.list.more, m.name)}
              actions={
                m.you && !space.personal
                  ? [{ label: t.members.leave, icon: <LogOut />, onSelect: () => setAsking(m) }]
                  : manage && !m.you
                    ? [{ label: t.members.remove, icon: <Trash2 />, onSelect: () => setAsking(m), destructive: true }]
                    : []
              }
            />
          </li>
        ))}
      </ul>
      <AlertDialog open={demoting !== null} onOpenChange={(o) => !o && setDemoting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t.members.demoteTitle}</AlertDialogTitle>
            <AlertDialogDescription>
              {demoting ? t.members.demoteHelp(space.name, t.roles[demoting.role]) : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t.members.cancel}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                const d = demoting
                setDemoting(null)
                if (d) void change(d.member, d.role, true)
              }}
            >
              {t.members.demoteConfirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <AlertDialog open={asking !== null} onOpenChange={(o) => !o && setAsking(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{asking?.you ? t.members.leaveTitle : t.members.removeTitle}</AlertDialogTitle>
            <AlertDialogDescription>
              {asking ? (asking.you ? t.members.leaveHelp(space.name) : t.members.removeHelp(asking.name)) : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t.members.cancel}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={() => void confirm()}>
              {asking?.you ? t.members.leaveConfirm : t.members.removeConfirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      {manage && !space.personal && (
        <AddMemberDialog space={space.id} members={members} open={adding} onOpenChange={setAdding} />
      )}
    </SettingsSection>
  )
}

function AddMemberDialog({
  space,
  members,
  open,
  onOpenChange,
}: {
  space: string
  members: Member[]
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const run = (rows: Parameters<typeof peopleApi.spaceBatch>[1], dryRun: boolean) =>
    peopleApi.spaceBatch(space, rows, dryRun)
  const pick = usePickRunner(run)
  return (
    <AddPeopleDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t.members.addTitle}
      description={tp.addHelp}
      kind="people"
      roles={ROLES.map((r) => ({ value: r, label: t.roles[r] }))}
      defaultRole="editor"
      excluded={new Set(members.map((m) => m.userId))}
      pick={pick}
      csvRun={run}
      csvHelp={tp.csv.help}
    />
  )
}
