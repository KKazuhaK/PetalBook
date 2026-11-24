import { useState, type FormEvent } from 'react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { forgetSpace, putMember, useActiveSpace, useAddMember, useMembers, useRemoveMember, useSession, useSetMemberRole } from '@/api/hooks'
import type { Member, SpaceInfo, SpaceRole } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { can } from '@/lib/permissions'
import { t } from '@/i18n'
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
  if (members.isError) return <ErrorState message={t.fmt.labelled(t.members.loadFailed, members.error.message)} onRetry={() => void members.refetch()} />
  const manage = can(current, 'members')
  return (
    <div className="space-y-8">
      <MemberList space={current} members={members.data.members} manage={manage} />
      {current.personal ? <SettingsNote>{t.members.personalNote}</SettingsNote> : manage ? <AddMember space={space} /> : <SettingsNote>{t.members.noManage}</SettingsNote>}
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
      <FormMessage>{error}</FormMessage>
      <ul className="divide-y rounded-lg border">
        {members.map((m) => (
          <li key={m.userId} className="flex flex-wrap items-center gap-x-4 gap-y-2 px-3 py-2.5">
            <div className="min-w-0 flex-1 basis-48">
              <div className="flex items-center gap-2">
                <span className="truncate text-sm font-medium">{m.name}</span>
                {m.you && <span className="text-xs text-muted-foreground">{t.members.you}</span>}
                {m.viaGroups && <span className="text-xs text-muted-foreground">{tg.space.viaGroups}{m.effectiveRole && m.effectiveRole !== m.role ? ` · ${tg.space.effective(t.roles[m.effectiveRole])}` : ''}</span>}
              </div>
              <div className="truncate text-xs text-muted-foreground">{m.username}</div>
            </div>
            <div className="w-32">
              {manage ? (
                <NativeSelect className="h-8" aria-label={t.fmt.labelled(m.name, t.members.role)} value={m.role} disabled={setRole.isPending} onChange={(e) => void change(m, e.target.value as SpaceRole)}>
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
            {m.you && !space.personal ? (
              <Button type="button" variant="outline" size="sm" onClick={() => setAsking(m)}>
                {t.members.leave}
              </Button>
            ) : manage && !m.you ? (
              <Button type="button" variant="outline" size="sm" aria-label={t.fmt.labelled(t.members.remove, m.name)} onClick={() => setAsking(m)}>
                {t.members.remove}
              </Button>
            ) : null}
          </li>
        ))}
      </ul>
      <AlertDialog open={demoting !== null} onOpenChange={(o) => !o && setDemoting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t.members.demoteTitle}</AlertDialogTitle>
            <AlertDialogDescription>{demoting ? t.members.demoteHelp(space.name, t.roles[demoting.role]) : ''}</AlertDialogDescription>
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
            <AlertDialogDescription>{asking ? (asking.you ? t.members.leaveHelp(space.name) : t.members.removeHelp(asking.name)) : ''}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t.members.cancel}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={() => void confirm()}>
              {asking?.you ? t.members.leaveConfirm : t.members.removeConfirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsSection>
  )
}

function AddMember({ space }: { space: string }) {
  const add = useAddMember(space)
  const [username, setUsername] = useState('')
  const [role, setRole] = useState<SpaceRole>('editor')
  const [error, setError] = useState('')

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (add.isPending) return
    const name = username.trim()
    if (name === '') return setError(t.members.usernameRequired)
    setError('')
    try {
      const m = await add.mutateAsync({ username: name, role })
      toast.success(t.members.added(m.name))
      setUsername('')
    } catch (err) {
      setError(memberError(err))
    }
  }

  return (
    <SettingsSection title={t.members.addTitle} id="add-member-heading" description={t.members.addHelp}>
      <form className="flex flex-wrap items-end gap-3" onSubmit={(e) => void submit(e)} noValidate>
        <div className="min-w-48 flex-1">
          <Field label={t.members.username} value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" autoCapitalize="none" spellCheck={false} />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="add-member-role">{t.members.role}</Label>
          <div className="w-32">
            <NativeSelect id="add-member-role" value={role} onChange={(e) => setRole(e.target.value as SpaceRole)} aria-describedby="add-member-role-help">
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
          {add.isPending ? t.members.adding : t.members.add}
        </Button>
        <p id="add-member-role-help" className="basis-full text-xs text-muted-foreground">
          {t.members.roleHelp[role]}
        </p>
      </form>
      <FormMessage>{error}</FormMessage>
    </SettingsSection>
  )
}
