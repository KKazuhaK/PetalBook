import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Link, Navigate, useNavigate } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '@/api/client'
import { applySignIn, useAuthProviders, useSession } from '@/api/hooks'
import type { PublicProvider } from '@/api/types'
import { AuthLayout, OrDivider } from '@/components/auth/AuthLayout'
import { CaptchaField, useCaptcha } from '@/components/auth/Captcha'
import { ProviderActions } from '@/components/auth/ProviderButtons'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { Button } from '@/components/ui/button'
import { forgetFragmentToken, fragmentToken } from '@/lib/auth'
import { t } from '@/i18n'
import { ts } from '@/i18n/signin'

// First run: the server has no user yet. The setup token comes from the address
// (`/setup#<token>`, as the server prints it), or is pasted.
export default function SetupPage() {
  const session = useSession()
  if (session.isPending) return <PageFallback />
  if (session.isError)
    return (
      <ErrorState
        message={t.fmt.labelled(t.auth.loadFailed, session.error.message)}
        onRetry={() => void session.refetch()}
      />
    )
  const s = session.data
  if (s.mode === 'single') return <Navigate to="/" replace />
  if (!s.setup) return <Navigate to={s.user ? '/' : '/login'} replace />
  return <SetupForm />
}

function SetupForm() {
  const providers = useAuthProviders()
  const list = providers.data?.sso ? providers.data.providers : []
  const captcha = useCaptcha('setup')
  const qc = useQueryClient()
  const navigate = useNavigate()
  // Read once; the address bar no longer has it afterwards.
  const [fromLink] = useState(() => fragmentToken('setup'))
  const [pasted, setPasted] = useState('')
  const token = (fromLink || pasted).trim()
  const [username, setUsername] = useState('')
  const [name, setName] = useState('')
  const [pass, setPass] = useState('')
  const [again, setAgain] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [fieldError, setFieldError] = useState<{ username?: string; password?: string; token?: string }>({})
  const messageRef = useRef<HTMLParagraphElement>(null)

  useEffect(() => {
    if (error) messageRef.current?.focus()
  }, [error])
  // The token is only needed until the administrator exists.
  useEffect(() => () => forgetFragmentToken('setup'), [])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setError('')
    const problems: typeof fieldError = {}
    if (!token) problems.token = t.setup.tokenRequired
    if (pass.length < 8) problems.password = t.auth.tooShort
    else if (pass !== again) problems.password = t.auth.mismatch
    setFieldError(problems)
    if (Object.keys(problems).length) return
    setBusy(true)
    const sent = captcha.value()
    try {
      const answer = await api.setup({
        token,
        username: username.trim(),
        name: name.trim() || undefined,
        password: pass,
        ...(sent ? { captcha: sent } : {}),
      })
      applySignIn(qc, answer)
      navigate('/', { replace: true })
    } catch (err) {
      setBusy(false)
      if (err instanceof ApiError && err.code === 'captcha_failed') {
        setError(sent ? ts.captcha.failed : ts.captcha.required)
        void captcha.refresh()
      } else if (err instanceof ApiError && err.code === 'rate_limited')
        setError(ts.login.rateLimited(t.auth.wait(err.retryAfter ?? 60)))
      else if (err instanceof ApiError && err.status === 403) setFieldError({ token: t.setup.badToken })
      else if (err instanceof ApiError && err.code === 'exists') setError(t.setup.exists)
      else if (err instanceof ApiError && err.code === 'invalid') setFieldError({ username: err.message })
      else setError(err instanceof ApiError ? err.message : t.auth.failed)
    }
  }

  const sso = async (p: PublicProvider) => {
    setError('')
    if (!token) return setFieldError({ token: t.setup.tokenRequired })
    try {
      const { url } = await api.providerStart(p.kind, p.slug, { intent: 'setup', token, return: '/' })
      window.location.assign(url)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t.auth.failed)
    }
  }

  return (
    <AuthLayout title={t.setup.title} intro={t.setup.intro}>
      <FormMessage focusRef={messageRef}>{error}</FormMessage>
      {error === t.setup.exists && (
        <Link to="/login" className="text-sm text-brand underline underline-offset-4">
          {t.setup.toLogin}
        </Link>
      )}
      <form className="grid gap-4" onSubmit={(e) => void submit(e)} noValidate>
        {!fromLink && (
          <Field
            label={t.setup.tokenLabel}
            help={t.setup.tokenHelp}
            error={fieldError.token}
            value={pasted}
            onChange={(e) => setPasted(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            autoFocus
            required
          />
        )}
        <Field
          label={t.auth.username}
          help={t.auth.usernameHelp}
          error={fieldError.username}
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          autoComplete="username"
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          autoFocus={!!fromLink}
          required
        />
        <Field label={t.auth.nameOptional} value={name} onChange={(e) => setName(e.target.value)} autoComplete="name" />
        <Field
          label={t.auth.password}
          help={t.auth.passwordHelp}
          error={fieldError.password}
          type="password"
          value={pass}
          onChange={(e) => setPass(e.target.value)}
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
        {fromLink && fieldError.token && <FormMessage>{fieldError.token}</FormMessage>}
        <CaptchaField captcha={captcha} disabled={busy} />
        <Button type="submit" disabled={busy || captcha.missing}>
          {busy ? t.setup.submitting : t.setup.submit}
        </Button>
      </form>
      {list.length > 0 && (
        <>
          <OrDivider />
          <p className="text-xs text-muted-foreground">{ts.setupSso.help}</p>
          <ProviderActions providers={list} label={ts.setupSso.withProvider} onPick={(p) => void sso(p)} />
        </>
      )}
    </AuthLayout>
  )
}
