import { useRef, useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { adminApi, useInvites, useUserRow } from '@/api/admin'
import { keys } from '@/api/hooks'
import type { CreatedInvite, Role } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { ErrorState } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'

// The name of whoever made an invite: one person by their id (cached), their id until it is read.
function CreatedBy({ id }: { id: string }) {
  const who = useUserRow(id).data?.user
  return <>{t.users.createdBy(who?.name ?? id)}</>
}

export function InvitesPanel() {
  const qc = useQueryClient()
  const invites = useInvites()
  const [role, setRole] = useState<Role>('member')
  const [days, setDays] = useState('7')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // Shown once, from the response of the request that made it; never stored.
  const [fresh, setFresh] = useState<CreatedInvite | null>(null)

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
      qc.setQueryData<{ invites: { id: string }[] }>(
        keys.invites,
        (prev) => prev && { invites: prev.invites.filter((i) => i.id !== id) },
      )
      toast.success(t.users.revoked)
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t.auth.failed)
    }
  }

  return (
    <SettingsSection title={t.users.invites} id="invites-heading">
      <p className="text-sm text-muted-foreground">{t.users.invitesHelp}</p>
      <form
        className="flex flex-wrap items-end gap-3 rounded-lg border p-3"
        onSubmit={(e) => void create(e)}
        noValidate
      >
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
          <Input
            id="invite-days"
            className="w-24"
            type="number"
            inputMode="numeric"
            min={1}
            max={30}
            value={days}
            onChange={(e) => setDays(e.target.value)}
            aria-describedby="invite-days-help"
          />
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
                  {t.users.expires(absoluteTime(i.expires))} · <CreatedBy id={i.createdBy} />
                </div>
              </div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => void revoke(i.id)}
                aria-label={t.fmt.labelled(t.users.revoke, `${t.roles[i.role]}, ${absoluteTime(i.expires)}`)}
              >
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
    <div
      className="grid gap-2 rounded-lg border border-brand/40 bg-brand/5 p-3"
      role="group"
      aria-labelledby="fresh-invite-title"
    >
      <h3 id="fresh-invite-title" className="text-sm font-medium">
        {t.users.linkTitle}
      </h3>
      <p className="text-xs text-muted-foreground">{t.users.linkHelp}</p>
      <div className="flex flex-wrap gap-2">
        <Input
          ref={input}
          className="min-w-0 flex-1 basis-56 font-mono text-xs"
          readOnly
          value={link}
          aria-label={t.users.linkTitle}
          onFocus={(e) => e.currentTarget.select()}
        />
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
