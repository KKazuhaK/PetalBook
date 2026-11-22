import { useState, type FormEvent } from 'react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { putMember, useAddMember, useMembers, useRemoveMember, useSetMemberRole, useSpaces } from '@/api/hooks'
import type { Member, SpaceInfo, SpaceRole } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { can } from '@/lib/permissions'
import { t } from '@/i18n'
import { useQueryClient } from '@tanstack/react-query'
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
  const spaces = useSpaces()
  const current = spaces.data?.spaces.find((s) => s.id === space)
  const readable = can(current, 'read')
  const members = useMembers(space, readable)
  if (spaces.isPending) return <PageFallback />
  if (!current) return <SettingsNote>{t.editor.spaceMissing}</SettingsNote>
  if (current.disabled) return <SettingsNote>{t.spaces.disabledTitle}</SettingsNote>
  if (members.isPending) return <PageFallback />
  if (members.isError) return <ErrorState message={t.fmt.labelled(t.members.loadFailed, members.error.message)} onRetry={() => void members.refetch()} />
  const manage = can(current, 'members')
  return (
    <div className="space-y-8">
      <MemberList space={current} members={members.data.members} manage={manage} />
      {current.personal ? <SettingsNote>{t.members.personalNote}</SettingsNote> : manage ? <AddMember space={space} /> : <SettingsNote>{t.members.noManage}</SettingsNote>}
    </div>
  )
}

function MemberList({ space, members, manage }: { space: SpaceInfo; members: Member[]; manage: boolean }) {
  const qc = useQueryClient()
  const setRole = useSetMemberRole(space.id)
  const remove = useRemoveMember(space.id)
  const [error, setError] = useState('')
  const [asking, setAsking] = useState<Member | null>(null)

  const change = async (m: Member, role: SpaceRole) => {
    if (role === m.role) return
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
