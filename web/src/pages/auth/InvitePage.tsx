import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Navigate, useNavigate } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '@/api/client'
import { applySession, useSession } from '@/api/hooks'
import type { InviteCheck } from '@/api/types'
import { AuthLayout, OrDivider } from '@/components/auth/AuthLayout'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { Button } from '@/components/ui/button'
import { forgetFragmentToken, fragmentToken } from '@/lib/auth'
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'

// The invite token comes from the address (`/invite#<token>`) or is pasted. It
// is checked with the server before the person is asked for anything.
export default function InvitePage() {
  const session = useSession()
  if (session.isPending) return <PageFallback />
  if (session.isError) return <ErrorState message={t.fmt.labelled(t.auth.loadFailed, session.error.message)} onRetry={() => void session.refetch()} />
  if (session.data.mode === 'single') return <Navigate to="/" replace />
  return <InviteForm oidc={session.data.methods?.oidc?.name ?? null} />
}

type Check = { state: 'none' } | { state: 'checking' } | { state: 'ok'; invite: InviteCheck } | { state: 'invalid' } | { state: 'error'; message: string }

function InviteForm({ oidc }: { oidc: string | null }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [fromLink] = useState(() => fragmentToken('invite'))
  const [pasted, setPasted] = useState('')
  const [checkedToken, setCheckedToken] = useState(fromLink)
  const token = checkedToken
  // Bumped when a token is pasted again, so the same one is checked again.
  const [attempt, setAttempt] = useState(0)
  const [check, setCheck] = useState<Check>(fromLink ? { state: 'checking' } : { state: 'none' })
  const [username, setUsername] = useState('')
  const [name, setName] = useState('')
  const [pass, setPass] = useState('')
  const [again, setAgain] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [fieldError, setFieldError] = useState<{ username?: string; password?: string }>({})
  const messageRef = useRef<HTMLParagraphElement>(null)

  // One check per token, as soon as there is one.
  useEffect(() => {
    if (!token) return
    const ctl = new AbortController()
    api.checkInvite(token, ctl.signal).then(
      (invite) => setCheck({ state: 'ok', invite }),
      (e) => {
        if (e instanceof DOMException && e.name === 'AbortError') return
        setCheck(e instanceof ApiError && e.code === 'not_found' ? { state: 'invalid' } : { state: 'error', message: e instanceof ApiError ? e.message : t.auth.failed })
      },
    )
    return () => ctl.abort()
  }, [token, attempt])
  useEffect(() => {
    if (error) messageRef.current?.focus()
  }, [error])
  useEffect(() => () => forgetFragmentToken('invite'), [])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setError('')
    const problems: typeof fieldError = {}
    if (pass.length < 8) problems.password = t.auth.tooShort
    else if (pass !== again) problems.password = t.auth.mismatch
    setFieldError(problems)
    if (Object.keys(problems).length) return
    setBusy(true)
    try {
      const { user } = await api.acceptInvite({ token, username: username.trim(), name: name.trim() || undefined, password: pass })
      applySession(qc, user)
      navigate('/', { replace: true })
    } catch (err) {
      setBusy(false)
      if (err instanceof ApiError && err.code === 'exists') setFieldError({ username: t.invite.taken })
      else if (err instanceof ApiError && err.code === 'not_found') setCheck({ state: 'invalid' })
      else if (err instanceof ApiError && err.code === 'invalid') setFieldError({ username: err.message })
      else setError(err instanceof ApiError ? err.message : t.auth.failed)
    }
  }

  const sso = async () => {
    setError('')
    try {
      const { url } = await api.oidcStart({ intent: 'invite', token, return: '/' })
      window.location.assign(url)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t.auth.failed)
    }
  }

  if (check.state === 'none') {
    return (
      <AuthLayout title={t.invite.title} intro={t.invite.noToken}>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            setCheckedToken(pasted.trim())
            setCheck({ state: 'checking' })
            setAttempt((n) => n + 1)
          }}
        >
          <Field label={t.invite.tokenLabel} value={pasted} onChange={(e) => setPasted(e.target.value)} autoComplete="off" spellCheck={false} autoFocus required />
          <Button type="submit" disabled={!pasted.trim()}>
            {t.invite.check}
          </Button>
        </form>
      </AuthLayout>
    )
  }
  if (check.state === 'checking') {
    return (
      <AuthLayout title={t.invite.title} intro={t.invite.checking}>
        <div aria-busy="true" role="status" className="h-24 animate-pulse rounded-lg bg-muted" />
      </AuthLayout>
    )
  }
  if (check.state === 'invalid' || check.state === 'error') {
    return (
      <AuthLayout title={t.invite.title}>
        <FormMessage focusRef={messageRef}>{check.state === 'invalid' ? t.invite.invalid : check.message}</FormMessage>
        <Button type="button" variant="outline" onClick={() => setCheck({ state: 'none' })}>
          {t.invite.tokenLabel}
        </Button>
      </AuthLayout>
    )
  }

  return (
    <AuthLayout title={t.invite.title} intro={t.fmt.sentences(t.invite.role(t.roles[check.invite.role]), t.invite.expires(absoluteTime(check.invite.expires)))}>
      <FormMessage focusRef={messageRef}>{error}</FormMessage>
      <form className="grid gap-4" onSubmit={(e) => void submit(e)} noValidate>
        <Field label={t.auth.username} help={t.auth.usernameHelp} error={fieldError.username} value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" autoCapitalize="none" autoCorrect="off" spellCheck={false} autoFocus required />
        <Field label={t.auth.nameOptional} value={name} onChange={(e) => setName(e.target.value)} autoComplete="name" />
        <Field label={t.auth.password} help={t.auth.passwordHelp} error={fieldError.password} type="password" value={pass} onChange={(e) => setPass(e.target.value)} autoComplete="new-password" required />
        <Field label={t.auth.confirmPassword} type="password" value={again} onChange={(e) => setAgain(e.target.value)} autoComplete="new-password" required />
        <Button type="submit" disabled={busy}>
          {busy ? t.invite.submitting : t.invite.submit}
        </Button>
      </form>
      {oidc && (
        <>
          <OrDivider />
          <Button type="button" variant="outline" onClick={() => void sso()}>
            {t.invite.sso(oidc)}
          </Button>
        </>
      )}
    </AuthLayout>
  )
}
