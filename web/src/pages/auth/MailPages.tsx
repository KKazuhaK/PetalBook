import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Link, Navigate, useNavigate, useSearchParams } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '@/api/client'
import { applySignIn, useSession } from '@/api/hooks'
import { AuthLayout } from '@/components/auth/AuthLayout'
import { CaptchaField, useCaptcha } from '@/components/auth/Captcha'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { Button } from '@/components/ui/button'
import { forgetFragmentToken, fragmentToken } from '@/lib/auth'
import { lang, t } from '@/i18n'
import { tm } from '@/i18n/mail'
import { ts } from '@/i18n/signin'

// The pages of registration, email verification and password reset (docs/api.md, "Registration", "Email
// verification", "Password reset"). Their links carry the token in the address fragment, read once and taken out of
// the address bar (see fragmentToken).

// The language a message is asked for: the page's own.
export const mailLang = () => (lang === 'en' ? 'en' : 'zh-Hans')

// A person who is not on an accounts server has none of these pages.
function useAccountsOnly() {
  const session = useSession()
  if (session.isPending) return <PageFallback />
  if (session.isError) return <ErrorState message={t.fmt.labelled(t.auth.loadFailed, session.error.message)} onRetry={() => void session.refetch()} />
  if (session.data.mode === 'single') return <Navigate to="/" replace />
  return null
}

const rateText = (err: ApiError) => ts.login.rateLimited(t.auth.wait(err.retryAfter ?? 60))

// --- registration ---------------------------------------------------------------

export function RegisterPage() {
  const gate = useAccountsOnly()
  const [params] = useSearchParams()
  const tenant = params.get('tenant') ?? undefined
  const info = useQuery({ queryKey: ['register', tenant ?? ''], queryFn: ({ signal }) => api.registerInfo(tenant, signal), enabled: gate === null, retry: false })
  if (gate) return gate
  if (info.isPending) return <PageFallback />
  if (info.isError) return <ErrorState message={t.fmt.labelled(tm.register.loadFailed, info.error.message)} onRetry={() => void info.refetch()} />
  if (!info.data.open) {
    return (
      <AuthLayout title={tm.register.title} intro={tm.register.closed}>
        <p className="text-sm text-muted-foreground">{tm.register.closedHelp}</p>
        <Link to="/login" className="text-sm text-brand underline underline-offset-4">{tm.links.backToLogin}</Link>
      </AuthLayout>
    )
  }
  return <RegisterForm tenant={tenant} info={info.data} />
}

function RegisterForm({ tenant, info }: { tenant?: string; info: Extract<Awaited<ReturnType<typeof api.registerInfo>>, { open: true }> }) {
  const captcha = useCaptcha('register')
  const [username, setUsername] = useState('')
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [pass, setPass] = useState('')
  const [again, setAgain] = useState('')
  const [accept, setAccept] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [fields, setFields] = useState<Record<string, string>>({})
  const [sent, setSent] = useState('')
  const messageRef = useRef<HTMLParagraphElement>(null)
  const needsTerms = info.termsUrl !== '' || info.privacyUrl !== ''
  const min = info.password.minLength
  useEffect(() => {
    if (error) messageRef.current?.focus()
  }, [error])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setError('')
    const problems: Record<string, string> = {}
    if (pass.length < min) problems.password = tm.register.tooShort(min)
    else if (pass !== again) problems.password = tm.register.mismatch
    if (needsTerms && !accept) problems.terms = tm.register.termsRequired
    setFields(problems)
    if (Object.keys(problems).length) return
    setBusy(true)
    const answer = captcha.value()
    try {
      await api.register({ ...(tenant ? { tenant } : {}), username: username.trim(), name: name.trim() || username.trim(), email: email.trim(), password: pass, ...(needsTerms ? { acceptTerms: accept } : {}), ...(answer ? { captcha: answer } : {}), lang: mailLang() })
      setSent(email.trim())
    } catch (err) {
      setBusy(false)
      setPass('')
      setAgain('')
      if (err instanceof ApiError && err.code === 'captcha_failed') {
        setError(answer ? ts.captcha.failed : ts.captcha.required)
        void captcha.refresh()
        return
      }
      void captcha.refresh()
      if (err instanceof ApiError && err.code === 'rate_limited') setError(rateText(err))
      else if (err instanceof ApiError && err.code === 'registration_closed') setError(tm.register.closedNow)
      else if (err instanceof ApiError && err.code === 'exists') setFields({ username: tm.register.taken })
      else if (err instanceof ApiError && err.code === 'email_domain_not_allowed') setFields({ email: tm.register.domainNotAllowed })
      else if (err instanceof ApiError && err.code === 'terms_required') setFields({ terms: tm.register.termsRequired })
      else if (err instanceof ApiError && err.code === 'invalid') setError(err.message)
      else setError(err instanceof ApiError ? err.message : t.auth.failed)
    }
  }

  if (sent) {
    return (
      <AuthLayout title={tm.register.sentTitle} intro={tm.register.sentBody(sent)}>
        <Button type="button" variant="outline" onClick={() => { setSent(''); setBusy(false) }}>{tm.register.sentAgain}</Button>
        <Link to="/login" className="text-sm text-brand underline underline-offset-4">{tm.links.backToLogin}</Link>
      </AuthLayout>
    )
  }
  return (
    <AuthLayout title={tm.register.title} intro={tm.register.intro(info.tenant.name)}>
      <FormMessage focusRef={messageRef}>{error}</FormMessage>
      <form className="grid gap-4" onSubmit={(e) => void submit(e)} noValidate>
        <Field label={tm.register.username} help={t.auth.usernameHelp} error={fields.username} value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" autoCapitalize="none" autoCorrect="off" spellCheck={false} autoFocus required />
        <Field label={tm.register.name} value={name} onChange={(e) => setName(e.target.value)} autoComplete="name" />
        <Field label={tm.register.email} type="email" help={info.domains.length ? `${tm.register.emailHelp} ${tm.register.domains(info.domains.join(', '))}` : tm.register.emailHelp} error={fields.email} value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="email" required />
        <Field label={tm.register.password} help={t.auth.passwordHelp} error={fields.password} type="password" value={pass} onChange={(e) => setPass(e.target.value)} autoComplete="new-password" required />
        <Field label={tm.register.confirmPassword} type="password" value={again} onChange={(e) => setAgain(e.target.value)} autoComplete="new-password" required />
        {needsTerms && (
          <div className="grid gap-1">
            <label className="flex items-start gap-2 text-sm">
              <input type="checkbox" className="mt-1" checked={accept} onChange={(e) => setAccept(e.target.checked)} aria-invalid={fields.terms ? true : undefined} />
              <span>
                {tm.register.accept}{' '}
                {info.termsUrl && <a href={info.termsUrl} target="_blank" rel="noreferrer" className="text-brand underline underline-offset-4">{tm.register.terms}</a>}
                {info.termsUrl && info.privacyUrl && ` ${tm.register.and} `}
                {info.privacyUrl && <a href={info.privacyUrl} target="_blank" rel="noreferrer" className="text-brand underline underline-offset-4">{tm.register.privacy}</a>}
              </span>
            </label>
            {fields.terms && <p className="text-xs text-destructive">{fields.terms}</p>}
          </div>
        )}
        <CaptchaField captcha={captcha} disabled={busy} />
        <Button type="submit" disabled={busy || captcha.missing}>{busy ? tm.register.submitting : tm.register.submit}</Button>
      </form>
      <Link to="/login" className="text-sm text-brand underline underline-offset-4">{tm.links.backToLogin}</Link>
    </AuthLayout>
  )
}

// The page a confirmation link opens: it makes the account, signs the person in and goes to the app.
export function RegisterConfirmPage() {
  const gate = useAccountsOnly()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [token] = useState(() => fragmentToken('register-confirm'))
  const [state, setState] = useState<{ kind: 'working' } | { kind: 'error'; message: string }>(token ? { kind: 'working' } : { kind: 'error', message: '' })
  const started = useRef(false)
  useEffect(() => () => forgetFragmentToken('register-confirm'), [])
  useEffect(() => {
    if (gate !== null || !token || started.current) return
    started.current = true
    api.registerConfirm(token).then(
      async ({ user }) => {
        await applySignIn(qc, user)
        navigate('/', { replace: true })
      },
      (err: unknown) => {
        const code = err instanceof ApiError ? err.code : ''
        setState({ kind: 'error', message: code === 'not_found' ? tm.confirm.invalid : code === 'registration_closed' ? tm.confirm.closed : code === 'exists' ? tm.confirm.exists : code === 'rate_limited' && err instanceof ApiError ? rateText(err) : tm.confirm.failed })
      },
    )
  }, [gate, token, qc, navigate])
  if (gate) return gate
  if (!token) return <AuthLayout title={tm.confirm.title}><FormMessage>{tm.confirm.noToken}</FormMessage></AuthLayout>
  if (state.kind === 'working') return <AuthLayout title={tm.confirm.title} intro={tm.confirm.working}><div aria-busy="true" role="status" className="h-16 animate-pulse rounded-lg bg-muted" /></AuthLayout>
  return (
    <AuthLayout title={tm.confirm.title}>
      <FormMessage>{state.message}</FormMessage>
      <Link to="/register" className="text-sm text-brand underline underline-offset-4">{tm.confirm.toRegister}</Link>
    </AuthLayout>
  )
}

// --- email verification --------------------------------------------------------------

export function VerifyEmailPage() {
  const gate = useAccountsOnly()
  const qc = useQueryClient()
  const [token] = useState(() => fragmentToken('verify-email'))
  const [state, setState] = useState<'working' | 'done' | 'invalid' | 'failed'>('working')
  const started = useRef(false)
  useEffect(() => () => forgetFragmentToken('verify-email'), [])
  useEffect(() => {
    if (gate !== null || !token || started.current) return
    started.current = true
    api.confirmEmail(token).then(
      () => {
        setState('done')
        void qc.invalidateQueries({ queryKey: ['account'] })
        void qc.invalidateQueries({ queryKey: ['session'] })
      },
      (err: unknown) => setState(err instanceof ApiError && err.code === 'not_found' ? 'invalid' : 'failed'),
    )
  }, [gate, token, qc])
  if (gate) return gate
  if (!token) return <AuthLayout title={tm.verify.title}><FormMessage>{tm.verify.noToken}</FormMessage></AuthLayout>
  return (
    <AuthLayout title={tm.verify.title} intro={state === 'working' ? tm.verify.working : undefined}>
      {state === 'done' && <FormMessage tone="info">{tm.verify.done}</FormMessage>}
      {state === 'invalid' && <FormMessage>{tm.verify.invalid}</FormMessage>}
      {state === 'failed' && <FormMessage>{tm.verify.failed}</FormMessage>}
      {state !== 'working' && <Link to="/" className="text-sm text-brand underline underline-offset-4">{tm.verify.toApp}</Link>}
    </AuthLayout>
  )
}

// --- password reset ----------------------------------------------------------------------

export function ForgotPage() {
  const gate = useAccountsOnly()
  const [params] = useSearchParams()
  const tenant = params.get('tenant') ?? undefined
  const captcha = useCaptcha('forgot')
  const [login, setLogin] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [sent, setSent] = useState(false)
  const messageRef = useRef<HTMLParagraphElement>(null)
  useEffect(() => {
    if (error) messageRef.current?.focus()
  }, [error])
  if (gate) return gate
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    const answer = captcha.value()
    try {
      await api.forgotPassword({ ...(tenant ? { tenant } : {}), login: login.trim(), ...(answer ? { captcha: answer } : {}), lang: mailLang() })
      setSent(true)
    } catch (err) {
      setBusy(false)
      void captcha.refresh()
      if (err instanceof ApiError && err.code === 'captcha_failed') setError(answer ? ts.captcha.failed : ts.captcha.required)
      else if (err instanceof ApiError && err.code === 'password_reset_off') setError(tm.forgot.off)
      else if (err instanceof ApiError && err.code === 'rate_limited') setError(rateText(err))
      else setError(err instanceof ApiError ? err.message : tm.forgot.failed)
    }
  }
  if (sent) {
    return (
      <AuthLayout title={tm.forgot.sentTitle} intro={tm.forgot.sentBody}>
        <Link to="/login" className="text-sm text-brand underline underline-offset-4">{tm.links.backToLogin}</Link>
      </AuthLayout>
    )
  }
  return (
    <AuthLayout title={tm.forgot.title} intro={tm.forgot.intro}>
      <FormMessage focusRef={messageRef}>{error}</FormMessage>
      <form className="grid gap-4" onSubmit={(e) => void submit(e)}>
        <Field label={tm.forgot.login} value={login} onChange={(e) => setLogin(e.target.value)} autoComplete="username" autoCapitalize="none" autoCorrect="off" spellCheck={false} autoFocus required />
        <CaptchaField captcha={captcha} disabled={busy} />
        <Button type="submit" disabled={busy || !login.trim() || captcha.missing}>{busy ? tm.forgot.submitting : tm.forgot.submit}</Button>
      </form>
      <Link to="/login" className="text-sm text-brand underline underline-offset-4">{tm.links.backToLogin}</Link>
    </AuthLayout>
  )
}

export function ResetPasswordPage() {
  const gate = useAccountsOnly()
  const [token] = useState(() => fragmentToken('reset-password'))
  const [check, setCheck] = useState<{ kind: 'checking' } | { kind: 'ok'; username: string } | { kind: 'invalid' } | { kind: 'error'; message: string }>({ kind: 'checking' })
  const [pass, setPass] = useState('')
  const [again, setAgain] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState(false)
  useEffect(() => () => forgetFragmentToken('reset-password'), [])
  useEffect(() => {
    if (gate !== null || !token) return
    const ctl = new AbortController()
    api.checkReset(token, ctl.signal).then(
      (r) => setCheck({ kind: 'ok', username: r.username }),
      (err: unknown) => {
        if (err instanceof DOMException && err.name === 'AbortError') return
        setCheck(err instanceof ApiError && err.code === 'not_found' ? { kind: 'invalid' } : { kind: 'error', message: err instanceof ApiError ? (err.code === 'rate_limited' ? rateText(err) : err.message) : tm.reset.failed })
      },
    )
    return () => ctl.abort()
  }, [gate, token])
  if (gate) return gate
  if (!token) return <AuthLayout title={tm.reset.title}><FormMessage>{tm.reset.noToken}</FormMessage></AuthLayout>
  if (done) {
    return (
      <AuthLayout title={tm.reset.title}>
        <FormMessage tone="info">{tm.reset.done}</FormMessage>
        <Link to="/login" className="text-sm text-brand underline underline-offset-4">{tm.links.backToLogin}</Link>
      </AuthLayout>
    )
  }
  if (check.kind === 'checking') return <AuthLayout title={tm.reset.title} intro={tm.reset.checking}><div aria-busy="true" role="status" className="h-16 animate-pulse rounded-lg bg-muted" /></AuthLayout>
  if (check.kind === 'invalid' || check.kind === 'error') {
    return (
      <AuthLayout title={tm.reset.title}>
        <FormMessage>{check.kind === 'invalid' ? tm.reset.invalid : check.message}</FormMessage>
        <Link to="/forgot" className="text-sm text-brand underline underline-offset-4">{tm.reset.again}</Link>
      </AuthLayout>
    )
  }
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setError('')
    if (pass.length < 8) return setError(tm.reset.tooShort(8))
    if (pass !== again) return setError(tm.reset.mismatch)
    setBusy(true)
    try {
      await api.resetPassword(token, pass)
      setDone(true)
    } catch (err) {
      setBusy(false)
      if (err instanceof ApiError && err.code === 'not_found') setCheck({ kind: 'invalid' })
      else setError(err instanceof ApiError ? (err.code === 'rate_limited' ? rateText(err) : err.message) : tm.reset.failed)
    }
  }
  return (
    <AuthLayout title={tm.reset.title} intro={tm.reset.intro(check.username)}>
      <FormMessage>{error}</FormMessage>
      <form className="grid gap-4" onSubmit={(e) => void submit(e)} noValidate>
        <Field label={tm.reset.password} help={t.auth.passwordHelp} type="password" value={pass} onChange={(e) => setPass(e.target.value)} autoComplete="new-password" autoFocus required />
        <Field label={tm.reset.confirm} type="password" value={again} onChange={(e) => setAgain(e.target.value)} autoComplete="new-password" required />
        <Button type="submit" disabled={busy}>{busy ? tm.reset.submitting : tm.reset.submit}</Button>
      </form>
    </AuthLayout>
  )
}
