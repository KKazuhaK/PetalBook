import { useEffect, useRef, useState, type FormEvent } from 'react'
import { KeyRound } from 'lucide-react'
import { useSearchParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api, ApiError } from '@/api/client'
import { keys, useAccount, useSession } from '@/api/hooks'
import type { AccountInfo } from '@/api/types'
import { ssoErrorMessage } from '@/pages/auth/LoginPage'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { t } from '@/i18n'
import { ts } from '@/i18n/signin'
import { SignInMethods } from './SignInMethods'

// Security (accounts mode only): the account's one list of sign-in methods, with the password form opening from
// its password row.
export function SecurityPanel() {
  const session = useSession()
  const account = useAccount(session.data?.mode === 'accounts' && !!session.data.user)
  if (account.isPending || !session.data) return <PageFallback />
  if (account.isError)
    return (
      <ErrorState
        message={t.fmt.labelled(t.settings.loadFailed, account.error.message)}
        onRetry={() => void account.refetch()}
      />
    )
  return (
    <div className="space-y-8">
      <Sections account={account.data} />
    </div>
  )
}

function Sections({ account }: { account: AccountInfo }) {
  const [params, setParams] = useSearchParams()
  const [passwordOpen, setPasswordOpen] = useState(false)
  const has = account.hasPassword
  // `?sso_error=` from a link that failed at the provider: shown once, then taken out of the address.
  const [linkError] = useState(() => ssoErrorMessage(params.get('sso_error')))
  useEffect(() => {
    if (params.has('sso_error'))
      setParams(
        (p) => {
          const next = new URLSearchParams(p)
          next.delete('sso_error')
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
      {linkError && (
        <FormMessage focusRef={messageRef}>{t.fmt.labelled(ts.identities.linkFailed, linkError)}</FormMessage>
      )}
      <SignInMethods account={account} onPassword={() => setPasswordOpen(true)} />
      <Dialog open={passwordOpen} onOpenChange={setPasswordOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{has ? t.settings.changePassword : t.settings.setPassword}</DialogTitle>
            <DialogDescription className="sr-only">
              {has ? t.settings.passwordIsSet : t.settings.setHelp}
            </DialogDescription>
          </DialogHeader>
          <PasswordForm
            account={account}
            onDone={(message) => {
              setPasswordOpen(false)
              toast.success(message)
            }}
          />
        </DialogContent>
      </Dialog>
    </>
  )
}

// The form is built when the dialog opens and dropped when it closes, so it
// starts empty each time.
export function PasswordForm({ account, onDone }: { account: AccountInfo; onDone: (message: string) => void }) {
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
      {has && (
        <Field
          label={t.settings.current}
          error={fieldError.current}
          type="password"
          value={current}
          onChange={(e) => setCurrent(e.target.value)}
          autoComplete="current-password"
          required
        />
      )}
      <Field
        label={t.settings.newPassword}
        help={t.auth.passwordHelp}
        error={fieldError.password}
        type="password"
        value={next}
        onChange={(e) => setNext(e.target.value)}
        autoComplete="new-password"
        required
      />
      <Field
        label={t.auth.confirmPassword}
        type="password"
        value={again}
        onChange={(e) => setAgain(e.target.value)}
        autoComplete="new-password"
        required
      />
      <FormMessage>{error}</FormMessage>
      <div className="flex items-center gap-3">
        <Button type="submit" disabled={busy}>
          <KeyRound aria-hidden="true" />
          {has ? t.settings.changePassword : t.settings.setPassword}
        </Button>
      </div>
    </form>
  )
}
