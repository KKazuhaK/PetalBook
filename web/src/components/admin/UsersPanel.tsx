import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { adminApi, useInvites, useUsers } from '@/api/admin'
import { keys, useSession } from '@/api/hooks'
import type { CreatedInvite, Passkey, Role, User } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { ErrorState, PageFallback } from '@/components/common'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'
import { tsec } from '@/i18n/security'

// Users and invites, for administrators. Moved from the old users page; the
// lists and forms are as they were.
export function UsersPanel() {
  const session = useSession()
  const me = session.data?.user
  const isAdmin = session.data?.mode === 'accounts' && me?.role === 'admin'
  const users = useUsers(isAdmin)
  if (!session.data) return <PageFallback />
  if (!isAdmin) return <ErrorState message={t.users.adminOnly} />
  if (users.isPending) return <PageFallback />
  if (users.isError) return <ErrorState message={t.fmt.labelled(t.users.loadFailed, users.error.message)} onRetry={() => void users.refetch()} />
  return (
    <div className="space-y-8">
      <UserList users={users.data.users} meId={me!.id} />
      <Invites users={users.data.users} />
    </div>
  )
}

// What a 409 from changing a user means, in plain words.
function conflictMessage(e: unknown, disabling: boolean): string {
  if (e instanceof ApiError && e.code === 'passkey_unavailable') return tsec.users.passkeyUnavailableCode
  if (e instanceof ApiError && e.code === 'conflict') return disabling && /自己|themself|yourself/i.test(e.message) ? t.users.selfDisable : e.message || t.users.lastAdmin
  return e instanceof ApiError ? e.message : t.users.changeFailed
}

function UserList({ users, meId }: { users: User[]; meId: string }) {
  const qc = useQueryClient()
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<{ id: string; message: string } | null>(null)
  const [open, setOpen] = useState<string | null>(null)
  const messageRef = useRef<HTMLParagraphElement>(null)

  const change = async (u: User, patch: { role?: Role; disabled?: boolean; passkeyRequired?: boolean; email?: string; emailVerified?: boolean }) => {
    setBusy(u.id)
    setError(null)
    try {
      const { user } = await adminApi.updateUser(u.id, patch)
      qc.setQueryData<{ users: User[] }>(keys.users, (prev) => prev && { users: prev.users.map((x) => (x.id === user.id ? { ...x, ...user } : x)) })
    } catch (e) {
      // Whatever the server refused, the list may be out of date: ask again.
      void qc.invalidateQueries({ queryKey: keys.users })
      setError({ id: u.id, message: conflictMessage(e, patch.disabled === true) })
      setTimeout(() => messageRef.current?.focus(), 0)
      setBusy(null)
      return false
    }
    setBusy(null)
    return true
  }

  return (
    <SettingsSection title={t.users.title} id="users-heading">
      <FormMessage focusRef={messageRef}>{error ? error.message : ''}</FormMessage>
      <ul className="divide-y rounded-lg border">
        {users.map((u) => {
          const self = u.id === meId
          return (
            <li key={u.id} className="flex flex-wrap items-center gap-x-4 gap-y-2 px-3 py-2.5">
              <div className="min-w-0 flex-1 basis-48">
                <div className="flex items-center gap-2">
                  <span className="truncate text-sm font-medium">{u.name}</span>
                  {self && <span className="text-xs text-muted-foreground">{t.users.you}</span>}
                  {u.disabled && <Badge variant="secondary">{t.users.disabled}</Badge>}
                </div>
                <div className="truncate text-xs text-muted-foreground">
                  {u.username}
                  {u.email ? ` · ${u.email}` : ''}
                  {u.email && u.emailVerified ? ` · ${tsec.users.verified}` : ''}
                </div>
                <div className="text-xs break-words text-muted-foreground" data-slot="user-counts">
                  {tsec.users.counts(u.passkeys ?? 0, u.sso ?? 0)}
                </div>
              </div>
              <label className="flex items-center gap-2 text-xs text-muted-foreground">
                <Switch checked={u.passkeyRequired} disabled={busy === u.id} aria-label={tsec.users.passkeyRequiredFor(u.name)} onCheckedChange={(on) => void change(u, { passkeyRequired: on })} />
                {tsec.users.passkeyRequired}
              </label>
              <div className="w-28">
                <NativeSelect
                  className="h-8"
                  aria-label={t.fmt.labelled(u.name, t.users.role)}
                  value={u.role}
                  disabled={busy === u.id}
                  onChange={(e) => void change(u, { role: e.target.value as Role })}
                >
                  <option value="member">{t.roles.member}</option>
                  <option value="admin">{t.roles.admin}</option>
                </NativeSelect>
              </div>
              <Button type="button" variant="outline" size="sm" disabled={busy === u.id} onClick={() => void change(u, { disabled: !u.disabled })} aria-label={t.fmt.labelled(u.disabled ? t.users.enable : t.users.disable, u.name)}>
                {u.disabled ? t.users.enable : t.users.disable}
              </Button>
              <Button type="button" variant="ghost" size="sm" aria-expanded={open === u.id} aria-label={tsec.users.editFor(u.name)} onClick={() => setOpen(open === u.id ? null : u.id)}>
                {open === u.id ? tsec.users.close : tsec.users.edit}
              </Button>
              {open === u.id && <UserDetails u={u} busy={busy === u.id} save={(patch) => change(u, patch)} />}
            </li>
          )
        })}
      </ul>
    </SettingsSection>
  )
}

// What an administrator can do for one person beyond the row: their address, and the passkeys of someone who lost a device.
function UserDetails({ u, busy, save }: { u: User; busy: boolean; save: (patch: { email?: string; emailVerified?: boolean }) => Promise<boolean> }) {
  const qc = useQueryClient()
  const [email, setEmail] = useState(u.email)
  const [saved, setSaved] = useState(false)
  const [passkeys, setPasskeys] = useState<Passkey[] | null>(null)
  const [error, setError] = useState('')
  const [removing, setRemoving] = useState<Passkey | null>(null)

  // The person's passkeys are read when the details open, and not before.
  useEffect(() => {
    const ctl = new AbortController()
    adminApi.userPasskeys(u.id, ctl.signal).then(
      (r) => setPasskeys(r.passkeys),
      (e: unknown) => {
        if (e instanceof DOMException && e.name === 'AbortError') return
        setError(tsec.users.loadPasskeysFailed)
      },
    )
    return () => ctl.abort()
  }, [u.id])

  const saveEmail = async (e: FormEvent) => {
    e.preventDefault()
    setSaved(false)
    // An address set on its own is not verified; saying so is the next switch.
    if (await save({ email: email.trim() })) setSaved(true)
  }
  const remove = async () => {
    if (!removing) return
    const target = removing
    try {
      await adminApi.deleteUserPasskey(u.id, target.id)
      setPasskeys((list) => list && list.filter((p) => p.id !== target.id))
      qc.setQueryData<{ users: User[] }>(keys.users, (prev) => prev && { users: prev.users.map((x) => (x.id === u.id ? { ...x, passkeys: Math.max(0, (x.passkeys ?? 1) - 1) } : x)) })
      toast.success(tsec.users.passkeyDeleted)
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t.auth.failed)
    }
    setRemoving(null)
  }

  return (
    <div className="grid min-w-0 basis-full grid-cols-1 gap-4 border-t pt-3" data-slot="user-details">
      <form className="grid min-w-0 grid-cols-1 gap-2 sm:flex sm:items-end" onSubmit={(e) => void saveEmail(e)}>
        <div className="grid min-w-0 gap-1.5 sm:flex-1">
          <Label htmlFor={`email-${u.id}`}>{tsec.users.email}</Label>
          <Input id={`email-${u.id}`} type="email" value={email} onChange={(e) => { setEmail(e.target.value); setSaved(false) }} autoComplete="off" />
        </div>
        <Button type="submit" variant="outline" disabled={busy || email.trim() === u.email}>
          {tsec.users.saveEmail}
        </Button>
        <span role="status" className="text-xs text-muted-foreground">
          {saved ? tsec.users.emailSaved : ''}
        </span>
      </form>
      <label className="flex min-w-0 items-start gap-3 text-sm">
        <Switch checked={u.emailVerified} disabled={busy || !u.email} aria-label={tsec.users.emailVerified} onCheckedChange={(on) => void save({ emailVerified: on })} className="mt-0.5" />
        <span className="min-w-0">
          {tsec.users.emailVerified}
          <span className="block text-xs break-words text-muted-foreground">{tsec.users.emailVerifiedHelp}</span>
        </span>
      </label>
      <div className="grid min-w-0 gap-2">
        <h4 className="text-sm font-medium">{tsec.users.passkeys}</h4>
        <p className="text-xs break-words text-muted-foreground">{tsec.users.passkeysHelp}</p>
        {passkeys === null ? (
          error ? null : <div aria-busy="true" className="h-8 animate-pulse rounded-md bg-muted" />
        ) : passkeys.length === 0 ? (
          <p className="text-sm text-muted-foreground">{tsec.users.noPasskeys}</p>
        ) : (
          <ul className="divide-y rounded-lg border">
            {passkeys.map((p) => (
              <li key={p.id} className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2" data-passkey={p.id}>
                <div className="min-w-0 flex-1 basis-40">
                  <div className="truncate text-sm">{p.name}</div>
                  <div className="text-xs break-words text-muted-foreground">
                    {absoluteTime(p.created)}
                    {p.lastUsed ? ` · ${absoluteTime(p.lastUsed)}` : ''}
                  </div>
                </div>
                <Button type="button" variant="outline" size="sm" aria-label={tsec.users.deletePasskeyFor(p.name)} onClick={() => setRemoving(p)}>
                  {tsec.users.deletePasskey}
                </Button>
              </li>
            ))}
          </ul>
        )}
        <FormMessage>{error}</FormMessage>
      </div>
      <AlertDialog open={removing !== null} onOpenChange={(open) => !open && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tsec.users.deletePasskeyTitle}</AlertDialogTitle>
            <AlertDialogDescription>{removing ? tsec.users.deletePasskeyHelp(removing.name, u.name) : ''}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t.settings.cancel}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={() => void remove()}>
              {tsec.users.deletePasskey}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function Invites({ users }: { users: User[] }) {
  const qc = useQueryClient()
  const invites = useInvites()
  const [role, setRole] = useState<Role>('member')
  const [days, setDays] = useState('7')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // Shown once, from the response of the request that made it; never stored.
  const [fresh, setFresh] = useState<CreatedInvite | null>(null)
  const nameOf = (id: string) => users.find((u) => u.id === id)?.name ?? id

  const create = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    const n = Number(days)
    if (!Number.isInteger(n) || n < 1 || n > 30) return setError(t.users.daysHelp)
    setBusy(true)
    setError('')
    try {
      const inv = await adminApi.createInvite({ role, days: n })
      setFresh(inv)
      void qc.invalidateQueries({ queryKey: keys.invites })
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t.auth.failed)
    }
    setBusy(false)
  }

  const revoke = async (id: string) => {
    try {
      await adminApi.revokeInvite(id)
      qc.setQueryData<{ invites: { id: string }[] }>(keys.invites, (prev) => prev && { invites: prev.invites.filter((i) => i.id !== id) })
      toast.success(t.users.revoked)
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t.auth.failed)
    }
  }

  return (
    <SettingsSection title={t.users.invites} id="invites-heading">
      <p className="text-sm text-muted-foreground">{t.users.invitesHelp}</p>
      <form className="flex flex-wrap items-end gap-3 rounded-lg border p-3" onSubmit={(e) => void create(e)} noValidate>
        <div className="grid gap-1.5">
          <Label htmlFor="invite-role">{t.users.inviteRole}</Label>
          <div className="w-32">
            <NativeSelect id="invite-role" value={role} onChange={(e) => setRole(e.target.value as Role)}>
              <option value="member">{t.roles.member}</option>
              <option value="admin">{t.roles.admin}</option>
            </NativeSelect>
          </div>
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="invite-days">{t.users.inviteDays}</Label>
          <Input id="invite-days" className="w-24" type="number" inputMode="numeric" min={1} max={30} value={days} onChange={(e) => setDays(e.target.value)} aria-describedby="invite-days-help" />
        </div>
        <Button type="submit" disabled={busy}>
          {busy ? t.users.creating : t.users.createInvite}
        </Button>
        <p id="invite-days-help" className="basis-full text-xs text-muted-foreground">
          {t.users.daysHelp}
        </p>
      </form>
      <FormMessage>{error}</FormMessage>
      {fresh && <FreshInvite invite={fresh} onDismiss={() => setFresh(null)} />}

      <h3 className="mt-2 text-sm font-medium">{t.users.open}</h3>
      {invites.isPending ? (
        <div aria-busy="true" className="h-12 animate-pulse rounded-lg bg-muted" />
      ) : invites.isError ? (
        <ErrorState message={invites.error.message} onRetry={() => void invites.refetch()} />
      ) : invites.data.invites.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t.users.noInvites}</p>
      ) : (
        <ul className="divide-y rounded-lg border">
          {invites.data.invites.map((i) => (
            <li key={i.id} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-3 py-2.5">
              <div className="min-w-0 flex-1 basis-48">
                <div className="text-sm font-medium">{t.roles[i.role]}</div>
                <div className="text-xs text-muted-foreground">
                  {t.users.expires(absoluteTime(i.expires))} · {t.users.createdBy(nameOf(i.createdBy))}
                </div>
              </div>
              <Button type="button" variant="outline" size="sm" onClick={() => void revoke(i.id)} aria-label={t.fmt.labelled(t.users.revoke, `${t.roles[i.role]}, ${absoluteTime(i.expires)}`)}>
                {t.users.revoke}
              </Button>
            </li>
          ))}
        </ul>
      )}
    </SettingsSection>
  )
}

// The link to send. The token is in the response of the creating request only.
function FreshInvite({ invite, onDismiss }: { invite: CreatedInvite; onDismiss: () => void }) {
  const link = `${window.location.origin}/invite#${invite.token}`
  const input = useRef<HTMLInputElement>(null)
  const [copied, setCopied] = useState(false)
  const [failed, setFailed] = useState(false)

  const copy = async () => {
    setFailed(false)
    try {
      await navigator.clipboard.writeText(link)
      setCopied(true)
    } catch {
      // No clipboard permission: select it so Ctrl+C works.
      input.current?.select()
      setFailed(true)
    }
  }

  return (
    <div className="grid gap-2 rounded-lg border border-brand/40 bg-brand/5 p-3" role="group" aria-labelledby="fresh-invite-title">
      <h3 id="fresh-invite-title" className="text-sm font-medium">
        {t.users.linkTitle}
      </h3>
      <p className="text-xs text-muted-foreground">{t.users.linkHelp}</p>
      <div className="flex flex-wrap gap-2">
        <Input ref={input} className="min-w-0 flex-1 basis-56 font-mono text-xs" readOnly value={link} aria-label={t.users.linkTitle} onFocus={(e) => e.currentTarget.select()} />
        <Button type="button" onClick={() => void copy()}>
          {copied ? t.users.copied : t.users.copy}
        </Button>
        <Button type="button" variant="ghost" onClick={onDismiss}>
          {t.users.dismiss}
        </Button>
      </div>
      <span role="status" className="text-xs text-destructive">
        {failed ? t.users.copyFailed : ''}
      </span>
    </div>
  )
}
