import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api, ApiError } from '@/api/client'
import { keys, useAccount, useSession } from '@/api/hooks'
import type { AccountInfo, Identity } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'
import { SettingsRow, SettingsRows, SettingsSection } from './SettingsParts'

// Security: the password and the linked sign-ins (accounts mode only). Moved
// from the Profile panel; the password form and the link and unlink calls are
// as they were, the form now opening from a button.
export function SecurityPanel() {
  const session = useSession()
  const account = useAccount(session.data?.mode === 'accounts' && !!session.data.user)
  if (account.isPending || !session.data) return <PageFallback />
  if (account.isError) return <ErrorState message={t.fmt.labelled(t.settings.loadFailed, account.error.message)} onRetry={() => void account.refetch()} />
  return (
    <div className="space-y-8">
      <Sections account={account.data} oidc={session.data.methods?.oidc?.name ?? null} />
    </div>
  )
}

function Sections({ account, oidc }: { account: AccountInfo; oidc: string | null }) {
  const [params, setParams] = useSearchParams()
  // `?error=` from a link that failed at the provider: shown once, then taken out of the address.
  const [linkError] = useState(() => {
    const code = params.get('error')
    // Own keys only: a code like `constructor` is not a message.
    return code ? (Object.hasOwn(t.auth.errors, code) ? t.auth.errors[code as keyof typeof t.auth.errors] : t.auth.errors.unknown) : ''
  })
  useEffect(() => {
    if (params.has('error'))
      setParams(
        (p) => {
          const next = new URLSearchParams(p)
          next.delete('error')
          return next
        },
        { replace: true },
      )
    // Once, on arrival.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  const messageRef = useRef<HTMLParagraphElement>(null)
  useEffect(() => {
    if (linkError) messageRef.current?.focus()
  }, [linkError])
  return (
    <>
      {linkError && <FormMessage focusRef={messageRef}>{t.fmt.labelled(t.settings.linkFailed, linkError)}</FormMessage>}
      <PasswordSection account={account} />
      <Identities account={account} oidc={oidc} />
    </>
  )
}

function PasswordSection({ account }: { account: AccountInfo }) {
  const has = account.hasPassword
  const [open, setOpen] = useState(false)
  return (
    <SettingsSection title={t.settings.password}>
      <SettingsRows>
        <SettingsRow title={t.settings.password} help={has ? t.settings.passwordIsSet : t.settings.setHelp}>
          <Button type="button" variant="outline" onClick={() => setOpen(true)}>
            {has ? t.settings.changePassword : t.settings.setPassword}
          </Button>
        </SettingsRow>
      </SettingsRows>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{has ? t.settings.changePassword : t.settings.setPassword}</DialogTitle>
            <DialogDescription className="sr-only">{has ? t.settings.passwordIsSet : t.settings.setHelp}</DialogDescription>
          </DialogHeader>
          <PasswordForm
            account={account}
            onDone={(message) => {
              setOpen(false)
              toast.success(message)
            }}
          />
        </DialogContent>
      </Dialog>
    </SettingsSection>
  )
}

// The form is built when the dialog opens and dropped when it closes, so it
// starts empty each time.
function PasswordForm({ account, onDone }: { account: AccountInfo; onDone: (message: string) => void }) {
  const qc = useQueryClient()
  const has = account.hasPassword
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [again, setAgain] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [fieldError, setFieldError] = useState<{ current?: string; password?: string }>({})

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setError('')
    const problems: typeof fieldError = {}
    if (next.length < 8) problems.password = t.auth.tooShort
    else if (next !== again) problems.password = t.auth.mismatch
    setFieldError(problems)
    if (Object.keys(problems).length) return
    setBusy(true)
    try {
      // `current` is left out when the account has no password yet.
      await api.changePassword(has ? { current, new: next } : { new: next })
      if (!has) qc.setQueryData<AccountInfo>(keys.account, (prev) => prev && { ...prev, hasPassword: true })
      onDone(has ? t.settings.done : t.settings.doneSet)
      return
    } catch (err) {
      if (err instanceof ApiError && err.status === 403) setFieldError({ current: t.settings.wrongCurrent })
      else if (err instanceof ApiError && err.code === 'invalid') setFieldError({ password: err.message })
      else setError(err instanceof ApiError ? err.message : t.auth.failed)
    }
    setBusy(false)
  }

  return (
    <form className="grid gap-4" onSubmit={(e) => void submit(e)} noValidate>
      {/* Lets a password manager tie the new password to this account. */}
      <input type="text" name="username" autoComplete="username" value={account.user.username} readOnly hidden />
      {has && <Field label={t.settings.current} error={fieldError.current} type="password" value={current} onChange={(e) => setCurrent(e.target.value)} autoComplete="current-password" required />}
      <Field label={t.settings.newPassword} help={t.auth.passwordHelp} error={fieldError.password} type="password" value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" required />
      <Field label={t.auth.confirmPassword} type="password" value={again} onChange={(e) => setAgain(e.target.value)} autoComplete="new-password" required />
      <FormMessage>{error}</FormMessage>
      <div className="flex items-center gap-3">
        <Button type="submit" disabled={busy}>
          {has ? t.settings.changePassword : t.settings.setPassword}
        </Button>
      </div>
    </form>
  )
}

function Identities({ account, oidc }: { account: AccountInfo; oidc: string | null }) {
  const qc = useQueryClient()
  const [removing, setRemoving] = useState<Identity | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const link = async () => {
    setError('')
    try {
      const { url } = await api.oidcStart({ intent: 'link', return: '/settings/security' })
      window.location.assign(url)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t.auth.failed)
    }
  }

  const remove = async () => {
    if (!removing) return
    setBusy(true)
    try {
      await api.removeIdentity(removing.id)
      qc.setQueryData<AccountInfo>(keys.account, (prev) => prev && { ...prev, identities: prev.identities.filter((i) => i.id !== removing.id) })
      setRemoving(null)
    } catch (err) {
      setRemoving(null)
      // 409: it is the only way in.
      if (err instanceof ApiError && err.code === 'conflict') setError(t.settings.onlyWay)
      else toast.error(err instanceof ApiError ? err.message : t.auth.failed)
    }
    setBusy(false)
  }

  // Nothing to show where no provider is set up and nothing is linked.
  if (!oidc && account.identities.length === 0) return null
  return (
    <SettingsSection title={t.settings.identities}>
      <SettingsRows>
        {oidc && (
          <SettingsRow title={t.settings.identities} help={account.identities.length === 0 ? t.settings.noIdentities : t.settings.identitiesHelp}>
            <Button type="button" variant="outline" onClick={() => void link()}>
              {t.settings.link(oidc)}
            </Button>
          </SettingsRow>
        )}
        {account.identities.map((i) => (
          <SettingsRow key={i.id} title={i.provider} help={`${i.label} · ${t.settings.linkedOn(absoluteTime(i.created))}`}>
            <Button type="button" variant="outline" size="sm" onClick={() => { setError(''); setRemoving(i) }}>
              {t.settings.remove}
            </Button>
          </SettingsRow>
        ))}
      </SettingsRows>
      <FormMessage>{error}</FormMessage>
      <AlertDialog open={removing !== null} onOpenChange={(open) => !open && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t.settings.removeTitle}</AlertDialogTitle>
            <AlertDialogDescription>{removing ? t.settings.removeHelp(removing.label || removing.provider) : ''}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t.settings.cancel}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" disabled={busy} onClick={() => void remove()}>
              {t.settings.removeConfirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsSection>
  )
}
