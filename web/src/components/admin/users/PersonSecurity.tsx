import { useEffect, useState } from 'react'
import { Mail, Trash2, Unlink } from 'lucide-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { adminApi } from '@/api/admin'
import { keys, useAuthProviders } from '@/api/hooks'
import type { Passkey, PersonIdentity, User } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { SettingsSection } from '@/components/settings/SettingsParts'
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
import { Switch } from '@/components/ui/switch'
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'
import { tc } from '@/i18n/console'
import { tm } from '@/i18n/mail'
import { tsec } from '@/i18n/security'
import { mailLang } from '@/pages/auth/MailPages'
import { useRequiredRoles } from './PersonProfile'
import type { UserPatch } from './useUserChange'

// How a person signs in and what an administrator can do when they cannot: the passkey requirement, the passkeys of
// someone who lost a device, a reset mail, and (for the site's administrators) the accounts they connected.
export function PersonSecurity({
  u,
  busy,
  save,
  site,
}: {
  u: User
  busy: boolean
  save: (patch: UserPatch) => Promise<unknown>
  site: boolean
}) {
  const qc = useQueryClient()
  const requireRoles = useRequiredRoles(site)
  const [passkeys, setPasskeys] = useState<Passkey[] | null>(null)
  const [error, setError] = useState('')
  const [removing, setRemoving] = useState<Passkey | null>(null)
  const mailOn = useAuthProviders(true).data?.forgot === true
  const [sending, setSending] = useState(false)
  const [resetMsg, setResetMsg] = useState('')

  const sendReset = async () => {
    setSending(true)
    setResetMsg('')
    try {
      await adminApi.sendPasswordReset(u.id, mailLang())
      setResetMsg(tm.users.sent(u.username))
    } catch (e) {
      setResetMsg(e instanceof ApiError ? (tm.users.errors[e.code] ?? e.message) : t.auth.failed)
    }
    setSending(false)
  }

  // The person's passkeys are read when the page opens, and not before.
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

  const remove = async () => {
    if (!removing) return
    const target = removing
    try {
      await adminApi.deleteUserPasskey(u.id, target.id)
      setPasskeys((list) => list && list.filter((p) => p.id !== target.id))
      void qc.invalidateQueries({ queryKey: keys.users })
      toast.success(tsec.users.passkeyDeleted)
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t.auth.failed)
    }
    setRemoving(null)
  }

  return (
    <div className="grid min-w-0 gap-8" data-slot="person-security">
      <SettingsSection title={tsec.users.passkeys} id="person-passkeys" description={tsec.users.passkeysHelp}>
        <label className="flex min-h-10 items-center gap-3 text-sm">
          <Switch
            checked={u.passkeyRequired}
            disabled={busy || requireRoles.includes(u.role)}
            aria-label={tsec.users.passkeyRequiredFor(u.name)}
            onCheckedChange={(on) => void save({ passkeyRequired: on })}
          />
          {tsec.users.passkeyRequired}
          {requireRoles.includes(u.role) && (
            <span className="text-xs text-muted-foreground" data-slot="passkey-by-role">
              {tsec.users.passkeyByRole}
            </span>
          )}
        </label>
        {passkeys === null ? (
          error ? null : (
            <div aria-busy="true" className="h-10 animate-pulse rounded-md bg-muted" />
          )
        ) : passkeys.length === 0 ? (
          <p className="text-sm text-muted-foreground">{tsec.users.noPasskeys}</p>
        ) : (
          <ul className="divide-y rounded-lg border">
            {passkeys.map((p) => (
              <li
                key={p.id}
                className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2"
                data-passkey={p.id}
              >
                <div className="min-w-0 flex-1 basis-40">
                  <div className="truncate text-sm">{p.name}</div>
                  <div className="text-xs break-words text-muted-foreground">
                    {absoluteTime(p.created)}
                    {p.lastUsed ? ` · ${absoluteTime(p.lastUsed)}` : ''}
                  </div>
                </div>
                <Button
                  type="button"
                  variant="outline"
                  className="h-10"
                  aria-label={tsec.users.deletePasskeyFor(p.name)}
                  onClick={() => setRemoving(p)}
                >
                  <Trash2 aria-hidden="true" />
                  {tsec.users.deletePasskey}
                </Button>
              </li>
            ))}
          </ul>
        )}
        <FormMessage>{error}</FormMessage>
      </SettingsSection>
      {mailOn && u.email && u.emailVerified && (
        <div className="flex flex-wrap items-center gap-2">
          <Button type="button" variant="outline" className="h-10" disabled={sending} onClick={() => void sendReset()}>
            <Mail aria-hidden="true" />
            {sending ? tm.users.sending : tm.users.sendReset}
          </Button>
          {resetMsg && (
            <span role="status" className="text-xs text-muted-foreground">
              {resetMsg}
            </span>
          )}
        </div>
      )}
      {site && <ConnectedAccounts u={u} />}
      <AlertDialog open={removing !== null} onOpenChange={(open) => !open && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tsec.users.deletePasskeyTitle}</AlertDialogTitle>
            <AlertDialogDescription>
              {removing ? tsec.users.deletePasskeyHelp(removing.name, u.name) : ''}
            </AlertDialogDescription>
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

function ConnectedAccounts({ u }: { u: User }) {
  const qc = useQueryClient()
  const list = useQuery({
    queryKey: [...keys.users, 'identities', u.id],
    queryFn: ({ signal }) => adminApi.userIdentities(u.id, signal),
  })
  const [removing, setRemoving] = useState<PersonIdentity | null>(null)
  const [error, setError] = useState('')
  const remove = async () => {
    if (!removing) return
    const target = removing
    setError('')
    try {
      await adminApi.disconnectUserIdentity(u.id, target.id)
      void qc.invalidateQueries({ queryKey: keys.users })
      toast.success(tc.person.security.disconnected)
    } catch (e) {
      setError(e instanceof ApiError ? e.message : tc.person.security.failed)
    }
    setRemoving(null)
  }
  const label = (i: PersonIdentity) => [i.provider, i.label].filter(Boolean).join(' · ')
  return (
    <SettingsSection
      title={tc.person.security.connected}
      id="person-connected"
      description={tc.person.security.connectedHelp}
    >
      {list.isPending ? (
        <div aria-busy="true" className="h-10 animate-pulse rounded-md bg-muted" />
      ) : list.isError ? (
        <FormMessage>{tc.person.security.failed}</FormMessage>
      ) : list.data.identities.length === 0 ? (
        <p className="text-sm text-muted-foreground">{tc.person.security.noneConnected}</p>
      ) : (
        <ul className="divide-y rounded-lg border" data-slot="identities">
          {list.data.identities.map((i) => (
            <li
              key={i.id}
              className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2"
              data-identity={i.id}
            >
              <div className="min-w-0 flex-1 basis-40">
                <div className="truncate text-sm">{label(i)}</div>
                <div className="text-xs text-muted-foreground">{absoluteTime(i.created)}</div>
              </div>
              <Button
                type="button"
                variant="outline"
                className="h-10"
                aria-label={tc.person.security.disconnectFor(label(i))}
                onClick={() => setRemoving(i)}
              >
                <Unlink aria-hidden="true" />
                {tc.person.security.disconnect}
              </Button>
            </li>
          ))}
        </ul>
      )}
      <FormMessage>{error}</FormMessage>
      <AlertDialog open={removing !== null} onOpenChange={(open) => !open && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tc.person.security.disconnectTitle}</AlertDialogTitle>
            <AlertDialogDescription>
              {removing ? tc.person.security.disconnectHelp(label(removing), u.name) : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t.settings.cancel}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={() => void remove()}>
              {tc.person.security.disconnect}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsSection>
  )
}
