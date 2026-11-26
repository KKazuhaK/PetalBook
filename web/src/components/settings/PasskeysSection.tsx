import { useState, type FormEvent } from 'react'
import { Pencil, Plus, Save, Trash2 } from 'lucide-react'
import { useNavigate } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api, ApiError } from '@/api/client'
import { applySession, keys, useAuthProviders } from '@/api/hooks'
import type { AccountInfo, Passkey } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
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
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { createCredential, passkeyFailure } from '@/lib/webauthn'
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'
import { ts } from '@/i18n/signin'
import { SettingsSection } from './SettingsParts'

// A person's passkeys (docs/api.md, "Account passkeys"): the list, adding one, renaming and deleting. It is also
// the page a held session is left with, where adding the first one lifts the hold.
export function PasskeysSection({
  account,
  returnTo = '/settings/security',
  onAdded,
}: {
  account: AccountInfo
  returnTo?: string
  onAdded?: () => void
}) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const providers = useAuthProviders()
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [needLogin, setNeedLogin] = useState(false)
  const [renaming, setRenaming] = useState<Passkey | null>(null)
  const [removing, setRemoving] = useState<Passkey | null>(null)
  const list = account.passkeys ?? []
  const unavailable = providers.data !== undefined && !providers.data.passkey

  const setList = (passkeys: Passkey[]) =>
    qc.setQueryData<AccountInfo>(keys.account, (prev) => prev && { ...prev, passkeys })

  const add = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    setNeedLogin(false)
    try {
      const { ceremony, options } = await api.passkeyRegisterBegin(name.trim() || ts.passkeys.defaultName)
      const credential = await createCredential(options)
      const { passkeys } = await api.passkeyRegisterFinish(ceremony, credential)
      setList(passkeys)
      setName('')
      toast.success(ts.passkeys.added)
      // The first passkey lifts a hold: the session says so on its next read.
      void qc.invalidateQueries({ queryKey: keys.session })
      onAdded?.()
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.code === 'recent_login_required') setNeedLogin(true)
        else if (err.code === 'too_many_passkeys') setError(ts.passkeys.tooMany)
        else if (err.code === 'exists') setError(ts.passkeys.exists)
        else setError(err.message)
      } else {
        const why = passkeyFailure(err)
        setError(
          why === 'cancelled'
            ? ts.passkeys.cancelled
            : why === 'unsupported'
              ? ts.passkeys.unsupported
              : ts.passkeys.failed,
        )
      }
    }
    setBusy(false)
  }

  // Registering needs a sign-in from the last ten minutes: end this session and sign in again, coming back here.
  const signInAgain = async () => {
    try {
      await api.logout()
    } catch (e) {
      return setError(e instanceof ApiError ? e.message : t.auth.failed)
    }
    applySession(qc, null)
    navigate(`/login?return=${encodeURIComponent(returnTo)}`, { replace: true })
  }

  const remove = async () => {
    if (!removing) return
    const target = removing
    try {
      await api.removePasskey(target.id)
      setList(list.filter((p) => p.id !== target.id))
      toast.success(ts.passkeys.removed)
      // Someone who must have a passkey is held at the next sign-in, not now.
      void qc.invalidateQueries({ queryKey: keys.session })
    } catch (err) {
      if (err instanceof ApiError && err.code === 'conflict') setError(ts.passkeys.onlyWay)
      else toast.error(err instanceof ApiError ? err.message : t.auth.failed)
    }
    setRemoving(null)
  }

  return (
    <SettingsSection title={ts.passkeys.title} id="passkeys-heading" description={ts.passkeys.help}>
      {account.user.passkeyRequired && <FormMessage tone="info">{ts.passkeys.required}</FormMessage>}
      {list.length === 0 ? (
        <p className="text-sm text-muted-foreground">{ts.passkeys.empty}</p>
      ) : (
        <ul className="divide-y rounded-lg border" aria-label={ts.passkeys.title}>
          {list.map((p) => (
            <li key={p.id} className="flex flex-wrap items-center gap-x-4 gap-y-2 px-3 py-2.5" data-passkey={p.id}>
              <div className="min-w-0 flex-1 basis-48">
                <div className="flex min-w-0 items-center gap-2">
                  <span className="min-w-0 truncate text-sm font-medium">{p.name}</span>
                  {p.synced && (
                    <Badge variant="secondary" title={ts.passkeys.syncedHelp}>
                      {ts.passkeys.synced}
                    </Badge>
                  )}
                </div>
                <div className="text-xs break-words text-muted-foreground">
                  {ts.passkeys.created(absoluteTime(p.created))} ·{' '}
                  {p.lastUsed ? ts.passkeys.lastUsed(absoluteTime(p.lastUsed)) : ts.passkeys.neverUsed}
                </div>
              </div>
              <div className="flex gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  aria-label={t.fmt.labelled(ts.passkeys.rename, p.name)}
                  onClick={() => setRenaming(p)}
                >
                  <Pencil aria-hidden="true" />
                  {ts.passkeys.rename}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  aria-label={t.fmt.labelled(ts.passkeys.remove, p.name)}
                  onClick={() => {
                    setError('')
                    setRemoving(p)
                  }}
                >
                  <Trash2 aria-hidden="true" />
                  {ts.passkeys.remove}
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}
      {unavailable && list.length === 0 ? (
        <p className="text-sm text-muted-foreground">{ts.passkeys.unavailable}</p>
      ) : (
        <form className="grid grid-cols-1 gap-3 sm:flex sm:items-end" onSubmit={(e) => void add(e)}>
          <div className="min-w-0 sm:flex-1">
            <Field
              label={ts.passkeys.nameLabel}
              help={ts.passkeys.nameHelp}
              value={name}
              onChange={(e) => setName(e.target.value)}
              maxLength={64}
              placeholder={ts.passkeys.defaultName}
              autoComplete="off"
            />
          </div>
          <Button type="submit" disabled={busy}>
            <Plus aria-hidden="true" />
            {busy ? ts.passkeys.adding : ts.passkeys.add}
          </Button>
        </form>
      )}
      <FormMessage>{error}</FormMessage>
      {needLogin && (
        <div className="grid gap-2 rounded-md border bg-muted/40 p-3" role="alert">
          <p className="text-sm">{ts.passkeys.recentLogin}</p>
          <Button type="button" variant="outline" size="sm" className="self-start" onClick={() => void signInAgain()}>
            {ts.passkeys.recentLoginAction}
          </Button>
        </div>
      )}
      <RenameDialog
        passkey={renaming}
        onClose={() => setRenaming(null)}
        onSaved={(p) => setList(list.map((x) => (x.id === p.id ? p : x)))}
      />
      <AlertDialog open={removing !== null} onOpenChange={(open) => !open && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{ts.passkeys.removeTitle}</AlertDialogTitle>
            <AlertDialogDescription>{removing ? ts.passkeys.removeHelp(removing.name) : ''}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{ts.passkeys.cancel}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={() => void remove()}>
              {ts.passkeys.removeConfirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsSection>
  )
}

function RenameDialog({
  passkey,
  onClose,
  onSaved,
}: {
  passkey: Passkey | null
  onClose: () => void
  onSaved: (p: Passkey) => void
}) {
  return (
    <Dialog open={passkey !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{ts.passkeys.renameTitle}</DialogTitle>
          <DialogDescription className="sr-only">{passkey?.name}</DialogDescription>
        </DialogHeader>
        {passkey && <RenameForm key={passkey.id} passkey={passkey} onClose={onClose} onSaved={onSaved} />}
      </DialogContent>
    </Dialog>
  )
}

function RenameForm({
  passkey,
  onClose,
  onSaved,
}: {
  passkey: Passkey
  onClose: () => void
  onSaved: (p: Passkey) => void
}) {
  const [name, setName] = useState(passkey.name)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy || !name.trim()) return
    setBusy(true)
    setError('')
    try {
      onSaved(await api.renamePasskey(passkey.id, name.trim()))
      toast.success(ts.passkeys.renamed)
      onClose()
      return
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t.auth.failed)
    }
    setBusy(false)
  }
  return (
    <form className="grid gap-4" onSubmit={(e) => void submit(e)}>
      <Field
        label={ts.passkeys.nameLabel}
        value={name}
        onChange={(e) => setName(e.target.value)}
        maxLength={64}
        autoComplete="off"
        autoFocus
        required
      />
      <FormMessage>{error}</FormMessage>
      <div className="flex gap-2">
        <Button type="submit" disabled={busy || !name.trim()}>
          <Save aria-hidden="true" />
          {busy ? ts.passkeys.saving : ts.passkeys.save}
        </Button>
        <Button type="button" variant="ghost" onClick={onClose}>
          {ts.passkeys.cancel}
        </Button>
      </div>
    </form>
  )
}
