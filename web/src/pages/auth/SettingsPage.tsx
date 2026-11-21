import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { Navigate, useSearchParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api, ApiError } from '@/api/client'
import { keys, useAccount, useSession } from '@/api/hooks'
import type { AccountInfo, Identity, SessionInfo } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { TopBar } from '@/components/layout/TopBar'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { absoluteTime } from '@/lib/time'
import { useDocumentTitle } from '@/lib/useDocumentTitle'
import { t } from '@/i18n/zh-CN'

export default function SettingsPage() {
  useDocumentTitle(`${t.settings.title} · ${t.appName}`)
  const session = useSession()
  const accounts = session.data?.mode === 'accounts' && !!session.data.user
  const account = useAccount(accounts)
  // Single-user mode has no account to set up.
  if (session.data?.mode === 'single') return <Navigate to="/" replace />
  return (
    <>
      <TopBar>
        <h1 className="truncate text-sm font-medium">{t.settings.title}</h1>
      </TopBar>
      <div className="mx-auto w-full max-w-[640px] flex-1 space-y-6 px-4 py-6 sm:px-5">
        {account.isPending || !session.data ? (
          <PageFallback />
        ) : account.isError ? (
          <ErrorState message={`${t.settings.loadFailed}：${account.error.message}`} onRetry={() => void account.refetch()} />
        ) : (
          <Sections account={account.data} session={session.data} />
        )}
      </div>
    </>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="grid gap-4 rounded-lg border p-4">
      <h2 className="text-sm font-medium">{title}</h2>
      {children}
    </section>
  )
}

function Sections({ account, session }: { account: AccountInfo; session: SessionInfo }) {
  const [params, setParams] = useSearchParams()
  // `?error=` from a link that failed at the provider: shown once, then taken out of the address.
  const [linkError] = useState(() => {
    const code = params.get('error')
    return code ? (t.auth.errors[code] ?? t.auth.errors.unknown) : ''
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
      {linkError && <FormMessage focusRef={messageRef}>{`${t.settings.linkFailed}：${linkError}`}</FormMessage>}
      <Profile account={account} />
      <PasswordSection account={account} />
      <Identities account={account} oidc={session.methods?.oidc?.name ?? null} />
    </>
  )
}

function Profile({ account }: { account: AccountInfo }) {
  const qc = useQueryClient()
  const [name, setName] = useState(account.user.name)
  const [email, setEmail] = useState(account.user.email)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    setDone(false)
    try {
      const { user } = await api.updateAccount({ name: name.trim(), email: email.trim() })
      qc.setQueryData<AccountInfo>(keys.account, (prev) => prev && { ...prev, user })
      qc.setQueryData<SessionInfo>(keys.session, (prev) => prev && { ...prev, user })
      setName(user.name)
      setEmail(user.email)
      setDone(true)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t.auth.failed)
    }
    setBusy(false)
  }

  return (
    <Section title={t.settings.profile}>
      <form className="grid gap-4" onSubmit={(e) => void submit(e)}>
        <Field label={t.settings.name} value={name} onChange={(e) => { setName(e.target.value); setDone(false) }} autoComplete="name" required />
        <Field label={t.settings.email} help={t.settings.emailHelp} type="email" value={email} onChange={(e) => { setEmail(e.target.value); setDone(false) }} autoComplete="email" />
        <FormMessage>{error}</FormMessage>
        <div className="flex items-center gap-3">
          <Button type="submit" disabled={busy || !name.trim()}>
            {busy ? t.settings.saving : t.settings.save}
          </Button>
          {/* Announced when it appears. */}
          <span role="status" className="text-sm text-muted-foreground">
            {done ? t.settings.saved : ''}
          </span>
        </div>
      </form>
    </Section>
  )
}

function PasswordSection({ account }: { account: AccountInfo }) {
  const qc = useQueryClient()
  const has = account.hasPassword
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [again, setAgain] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [fieldError, setFieldError] = useState<{ current?: string; password?: string }>({})
  const [done, setDone] = useState('')

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setError('')
    setDone('')
    const problems: typeof fieldError = {}
    if (next.length < 8) problems.password = t.auth.tooShort
    else if (next !== again) problems.password = t.auth.mismatch
    setFieldError(problems)
    if (Object.keys(problems).length) return
    setBusy(true)
    try {
      // `current` is left out when the account has no password yet.
      await api.changePassword(has ? { current, new: next } : { new: next })
      setDone(has ? t.settings.done : t.settings.doneSet)
      setCurrent('')
      setNext('')
      setAgain('')
      if (!has) qc.setQueryData<AccountInfo>(keys.account, (prev) => prev && { ...prev, hasPassword: true })
    } catch (err) {
      if (err instanceof ApiError && err.status === 403) setFieldError({ current: t.settings.wrongCurrent })
      else if (err instanceof ApiError && err.code === 'invalid') setFieldError({ password: err.message })
      else setError(err instanceof ApiError ? err.message : t.auth.failed)
    }
    setBusy(false)
  }

  return (
    <Section title={has ? t.settings.changePassword : t.settings.setPassword}>
      {!has && <p className="text-sm text-muted-foreground">{t.settings.setHelp}</p>}
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
          <span role="status" className="text-sm text-muted-foreground">
            {done}
          </span>
        </div>
      </form>
    </Section>
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
      const { url } = await api.oidcStart({ intent: 'link', return: '/settings' })
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
    <Section title={t.settings.identities}>
      {account.identities.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t.settings.noIdentities}</p>
      ) : (
        <ul className="divide-y rounded-md border">
          {account.identities.map((i) => (
            <li key={i.id} className="flex items-center gap-3 px-3 py-2">
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium">{i.provider}</div>
                <div className="truncate text-xs text-muted-foreground">
                  {i.label} · {t.settings.linkedOn(absoluteTime(i.created))}
                </div>
              </div>
              <Button type="button" variant="outline" size="sm" onClick={() => { setError(''); setRemoving(i) }}>
                {t.settings.remove}
              </Button>
            </li>
          ))}
        </ul>
      )}
      <FormMessage>{error}</FormMessage>
      {oidc && (
        <div>
          <Button type="button" variant="outline" onClick={() => void link()}>
            {t.settings.link(oidc)}
          </Button>
        </div>
      )}
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
    </Section>
  )
}
