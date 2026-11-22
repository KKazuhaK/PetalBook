import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Navigate, useNavigate, useSearchParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '@/api/client'
import { applySession, useSession } from '@/api/hooks'
import { AuthLayout, OrDivider } from '@/components/auth/AuthLayout'
import { Field, FormMessage } from '@/components/auth/Field'
import { PageFallback, ErrorState } from '@/components/common'
import { Button } from '@/components/ui/button'
import { safeReturn } from '@/lib/auth'
import { t } from '@/i18n'

export default function LoginPage() {
  const session = useSession()
  const [params] = useSearchParams()
  const ret = safeReturn(params.get('return'))

  if (session.isPending) return <PageFallback />
  if (session.isError) return <ErrorState message={t.fmt.labelled(t.auth.loadFailed, session.error.message)} onRetry={() => void session.refetch()} />
  const s = session.data
  // Nothing to sign in to, or already signed in, or nobody exists yet.
  if (s.mode === 'single') return <Navigate to="/" replace />
  if (s.setup) return <Navigate to="/setup" replace />
  if (s.user) return <Navigate to={ret} replace />
  return <LoginForm ret={ret} oidc={s.methods?.oidc?.name ?? null} password={s.methods?.password !== false} initialError={params.get('error')} />
}

function LoginForm({ ret, oidc, password, initialError }: { ret: string; oidc: string | null; password: boolean; initialError: string | null }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [username, setUsername] = useState('')
  const [pass, setPass] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState(initialError ? (t.auth.errors[initialError] ?? t.auth.errors.unknown) : '')
  // When the server says to wait, until when (ms since the epoch), and what time it is now.
  const [until, setUntil] = useState(0)
  const [now, setNow] = useState(() => Date.now())
  const messageRef = useRef<HTMLParagraphElement>(null)
  const remaining = Math.max(0, Math.ceil((until - now) / 1000))

  useEffect(() => {
    if (until <= Date.now()) return
    const timer = setInterval(() => {
      setNow(Date.now())
      if (Date.now() >= until) clearInterval(timer)
    }, 1000)
    return () => clearInterval(timer)
  }, [until])

  // A new message takes focus, so it is read out and keyboard users are where it is.
  useEffect(() => {
    if (error) messageRef.current?.focus()
  }, [error])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy || remaining > 0) return
    setBusy(true)
    setError('')
    try {
      const { user } = await api.login({ username: username.trim(), password: pass })
      applySession(qc, user)
      navigate(ret, { replace: true })
    } catch (err) {
      setBusy(false)
      if (err instanceof ApiError && err.code === 'rate_limited') {
        const wait = err.retryAfter ?? 60
        setNow(Date.now())
        setUntil(Date.now() + wait * 1000)
        setError(t.login.rateLimited(t.auth.wait(wait)))
      } else setError(err instanceof ApiError ? err.message : t.login.failed)
      setPass('')
    }
  }

  const sso = async () => {
    setError('')
    try {
      const { url } = await api.oidcStart({ intent: 'login', return: ret })
      window.location.assign(url)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t.login.failed)
    }
  }

  const waiting = remaining > 0
  return (
    <AuthLayout title={t.login.title}>
      <FormMessage focusRef={messageRef}>{waiting ? t.login.rateLimited(t.auth.wait(remaining)) : error}</FormMessage>
      {password && (
        <form className="grid gap-4" onSubmit={(e) => void submit(e)}>
          <Field label={t.auth.username} value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" autoCapitalize="none" autoCorrect="off" spellCheck={false} autoFocus required />
          <Field label={t.auth.password} type="password" value={pass} onChange={(e) => setPass(e.target.value)} autoComplete="current-password" required />
          <Button type="submit" disabled={busy || waiting || !username || !pass}>
            {busy ? t.login.submitting : t.login.submit}
          </Button>
        </form>
      )}
      {oidc && (
        <>
          {password && <OrDivider />}
          <Button type="button" variant="outline" onClick={() => void sso()}>
            {t.login.sso(oidc)}
          </Button>
        </>
      )}
    </AuthLayout>
  )
}
