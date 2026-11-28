import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Link, Navigate, useNavigate, useSearchParams } from 'react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError, providerStartUrl } from '@/api/client'
import { applySignIn, useSession } from '@/api/hooks'
import { AuthLayout } from '@/components/auth/AuthLayout'
import { CaptchaField, useCaptcha } from '@/components/auth/Captcha'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { Button, buttonVariants } from '@/components/ui/button'
import { forgetFragmentToken, fragmentToken, passFragmentToken } from '@/lib/auth'
import type { DiscoveredSSO, RegisteredOrg, RegisterInfo, RegisterOrg } from '@/api/types'
import { absoluteTime } from '@/lib/time'
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
  if (session.isError)
    return (
      <ErrorState
        message={t.fmt.labelled(t.auth.loadFailed, session.error.message)}
        onRetry={() => void session.refetch()}
      />
    )
  if (session.data.mode === 'single') return <Navigate to="/" replace />
  return null
}

const rateText = (err: ApiError) => ts.login.rateLimited(t.auth.wait(err.retryAfter ?? 60))

// --- registration ---------------------------------------------------------------

export function RegisterPage() {
  const gate = useAccountsOnly()
  const [params] = useSearchParams()
  const tenant = params.get('tenant') ?? undefined
  const info = useQuery({
    queryKey: ['register', tenant ?? ''],
    queryFn: ({ signal }) => api.registerInfo(tenant, signal),
    enabled: gate === null,
    retry: false,
  })
  if (gate) return gate
  if (info.isPending) return <PageFallback />
  if (info.isError)
    return (
      <ErrorState
        message={t.fmt.labelled(tm.register.loadFailed, info.error.message)}
        onRetry={() => void info.refetch()}
      />
    )
  if (!info.data.open && !info.data.orgCreation.open && !info.data.domainJoin) {
    // What to tell a person who cannot sign up here follows the tenant's mode: an invitation is the way in, or nobody
    // is let in at all, or the mode is open but the mail it needs is not set up.
    const mode = info.data.mode
    const [intro, help] =
      mode === 'invite'
        ? [tm.register.inviteOnly, tm.register.inviteOnlyHelp]
        : mode === 'open'
          ? [tm.register.unavailable, tm.register.unavailableHelp]
          : [tm.register.closed, tm.register.closedHelp]
    return (
      <AuthLayout title={tm.register.title} intro={intro}>
        <p className="text-sm text-muted-foreground">{help}</p>
        <Link to="/login" className="text-sm text-brand underline underline-offset-4">
          {tm.links.backToLogin}
        </Link>
      </AuthLayout>
    )
  }
  return <RegisterForm tenant={tenant} info={info.data} wantsOrg={params.get('org') === '1'} />
}

// A slug suggested from an organisation's name: lower case letters and digits, joined by single dashes, at most 32
// characters. Empty when the name has nothing to make one of.
export function slugFromName(name: string): string {
  const s = name
    .normalize('NFKD')
    .replace(/[\u0300-\u036f]/g, '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+/, '')
    .slice(0, 32)
    .replace(/-+$/, '')
  return s.length >= 2 ? s : ''
}
const SLUG = /^[a-z0-9][a-z0-9-]{1,31}$/

function RegisterForm({ tenant, info, wantsOrg }: { tenant?: string; info: RegisterInfo; wantsOrg: boolean }) {
  // Joining by the domain of a work address (3.6 d5) is offered as joining is, while registration is closed.
  const byDomainOnly = !info.open && info.domainJoin
  const joinOpen = info.open || info.domainJoin
  const orgOpen = info.orgCreation.open
  const [mode, setMode] = useState<'join' | 'org'>(orgOpen && (wantsOrg || !joinOpen) ? 'org' : 'join')
  const org = mode === 'org'
  const [orgName, setOrgName] = useState('')
  const [orgSlug, setOrgSlug] = useState('')
  const [slugEdited, setSlugEdited] = useState(false)
  const captcha = useCaptcha('register', { tenant: org ? undefined : tenant })
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
  // Where an address of an organisation that requires its single sign-on signs in instead (409 sso_required).
  const [ssoFor, setSsoFor] = useState<{ sso: DiscoveredSSO; email: string } | null>(null)
  const messageRef = useRef<HTMLParagraphElement>(null)
  const domains = info.domains ?? []
  const termsUrl = info.termsUrl ?? ''
  const privacyUrl = info.privacyUrl ?? ''
  const needsTerms = termsUrl !== '' || privacyUrl !== ''
  const min = info.password?.minLength ?? 8
  useEffect(() => {
    if (error) messageRef.current?.focus()
  }, [error])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setError('')
    setSsoFor(null)
    const problems: Record<string, string> = {}
    if (pass.length < min) problems.password = tm.register.tooShort(min)
    else if (pass !== again) problems.password = tm.register.mismatch
    if (needsTerms && !accept) problems.terms = tm.register.termsRequired
    if (org) {
      if (!orgName.trim()) problems.orgName = tm.register.orgNameRequired
      if (!SLUG.test(orgSlug)) problems.orgSlug = tm.register.orgSlugInvalid
    }
    setFields(problems)
    if (Object.keys(problems).length) return
    setBusy(true)
    const answer = captcha.value()
    try {
      await api.register({
        ...(tenant && !org ? { tenant } : {}),
        ...(org ? { org: { name: orgName.trim(), slug: orgSlug } } : {}),
        username: username.trim(),
        name: name.trim() || username.trim(),
        email: email.trim(),
        password: pass,
        ...(needsTerms ? { acceptTerms: accept } : {}),
        ...(answer ? { captcha: answer } : {}),
        lang: mailLang(),
      })
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
      else if (err instanceof ApiError && err.code === 'slug_taken') setFields({ orgSlug: tm.register.orgSlugTaken })
      else if (err instanceof ApiError && err.code === 'slug_reserved')
        setFields({ orgSlug: tm.register.orgSlugReserved })
      else if (err instanceof ApiError && err.code === 'email_domain_not_allowed')
        setFields({ email: tm.register.domainNotAllowed })
      else if (err instanceof ApiError && err.code === 'terms_required') setFields({ terms: tm.register.termsRequired })
      else if (err instanceof ApiError && err.code === 'sso_required') {
        setError(err.message)
        const sso = (err.body as { sso?: DiscoveredSSO } | undefined)?.sso
        if (sso) setSsoFor({ sso, email: email.trim() })
      } else if (err instanceof ApiError && err.code === 'invalid') setError(err.message)
      else setError(err instanceof ApiError ? err.message : t.auth.failed)
    }
  }

  if (sent) {
    return (
      <AuthLayout title={tm.register.sentTitle} intro={tm.register.sentBody(sent)}>
        <Button
          type="button"
          variant="outline"
          onClick={() => {
            setSent('')
            setBusy(false)
          }}
        >
          {tm.register.sentAgain}
        </Button>
        <Link to="/login" className="text-sm text-brand underline underline-offset-4">
          {tm.links.backToLogin}
        </Link>
      </AuthLayout>
    )
  }
  return (
    <AuthLayout
      title={org ? tm.register.orgDialogTitle : tm.register.title}
      intro={
        org ? tm.register.orgIntro : byDomainOnly ? tm.register.domainOnly : tm.register.intro(info.tenant?.name ?? '')
      }
    >
      <FormMessage focusRef={messageRef}>{error}</FormMessage>
      {ssoFor && (
        <a
          href={providerStartUrl(ssoFor.sso.kind, ssoFor.sso.slug, '/', ssoFor.email)}
          className={`${buttonVariants({ variant: 'outline' })} h-10 min-w-0`}
        >
          <span className="min-w-0 truncate">{ts.ended.continueWith(ssoFor.sso.name)}</span>
        </a>
      )}
      <form className="grid gap-4" onSubmit={(e) => void submit(e)} noValidate>
        {joinOpen && orgOpen && (
          <div role="radiogroup" aria-label={tm.register.choiceLabel} className="grid gap-2 sm:grid-cols-2">
            {(
              [
                ['join', byDomainOnly ? tm.register.title : tm.register.join(info.tenant?.name ?? '')],
                ['org', tm.register.createOrg],
              ] as const
            ).map(([value, text]) => (
              <button
                key={value}
                type="button"
                role="radio"
                aria-checked={mode === value}
                onClick={() => setMode(value)}
                className={`min-h-11 rounded-lg border px-3 py-2 text-left text-sm font-medium transition-colors focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none ${mode === value ? 'border-brand bg-brand/10 text-foreground' : 'border-input text-muted-foreground hover:bg-muted'}`}
              >
                {text}
              </button>
            ))}
          </div>
        )}
        {org && (
          <>
            <Field
              label={tm.register.orgName}
              error={fields.orgName}
              value={orgName}
              maxLength={80}
              onChange={(e) => {
                setOrgName(e.target.value)
                if (!slugEdited) setOrgSlug(slugFromName(e.target.value))
              }}
              autoComplete="organization"
              required
            />
            <Field
              label={tm.register.orgSlug}
              help={tm.register.orgSlugHelp}
              error={fields.orgSlug}
              value={orgSlug}
              maxLength={32}
              onChange={(e) => {
                setSlugEdited(true)
                setOrgSlug(e.target.value.toLowerCase())
              }}
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              required
            />
          </>
        )}
        <Field
          label={tm.register.username}
          help={t.auth.usernameHelp}
          error={fields.username}
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          autoComplete="username"
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          autoFocus
          required
        />
        <Field label={tm.register.name} value={name} onChange={(e) => setName(e.target.value)} autoComplete="name" />
        <Field
          label={tm.register.email}
          type="email"
          help={
            byDomainOnly && !org
              ? tm.register.domainOnlyHelp
              : domains.length && !org
                ? `${tm.register.emailHelp} ${tm.register.domains(domains.join(', '))}`
                : tm.register.emailHelp
          }
          error={fields.email}
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          autoComplete="email"
          required
        />
        <Field
          label={tm.register.password}
          help={t.auth.passwordHelp}
          error={fields.password}
          type="password"
          value={pass}
          onChange={(e) => setPass(e.target.value)}
          autoComplete="new-password"
          required
        />
        <Field
          label={tm.register.confirmPassword}
          type="password"
          value={again}
          onChange={(e) => setAgain(e.target.value)}
          autoComplete="new-password"
          required
        />
        {needsTerms && (
          <div className="grid gap-1">
            <label className="flex items-start gap-2 text-sm">
              <input
                type="checkbox"
                className="mt-1"
                checked={accept}
                onChange={(e) => setAccept(e.target.checked)}
                aria-invalid={fields.terms ? true : undefined}
              />
              <span>
                {tm.register.accept}{' '}
                {termsUrl && (
                  <a
                    href={termsUrl}
                    target="_blank"
                    rel="noreferrer"
                    className="text-brand underline underline-offset-4"
                  >
                    {tm.register.terms}
                  </a>
                )}
                {termsUrl && privacyUrl && ` ${tm.register.and} `}
                {privacyUrl && (
                  <a
                    href={privacyUrl}
                    target="_blank"
                    rel="noreferrer"
                    className="text-brand underline underline-offset-4"
                  >
                    {tm.register.privacy}
                  </a>
                )}
              </span>
            </label>
            {fields.terms && <p className="text-xs text-destructive">{fields.terms}</p>}
          </div>
        )}
        <CaptchaField captcha={captcha} disabled={busy} />
        <Button type="submit" disabled={busy || captcha.missing}>
          {busy ? tm.register.submitting : org ? tm.register.orgSubmit : tm.register.submit}
        </Button>
      </form>
      <Link to="/login" className="text-sm text-brand underline underline-offset-4">
        {tm.links.backToLogin}
      </Link>
    </AuthLayout>
  )
}

// The page a confirmation link opens: it makes the account, signs the person in and goes to the app.
export function RegisterConfirmPage() {
  const gate = useAccountsOnly()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [token] = useState(() => fragmentToken('register-confirm'))
  const [state, setState] = useState<
    | { kind: 'working' }
    | { kind: 'error'; message: string }
    | { kind: 'org'; org: RegisteredOrg }
    | { kind: 'joined'; org: RegisterOrg }
  >(token ? { kind: 'working' } : { kind: 'error', message: '' })
  const started = useRef(false)
  useEffect(() => () => forgetFragmentToken('register-confirm'), [])
  useEffect(() => {
    if (gate !== null || !token || started.current) return
    started.current = true
    api.registerConfirm(token, mailLang()).then(
      (answer) => {
        // A request to join waits for the organisation's administrators: the waiting page, with the same token.
        if (answer.request) {
          passFragmentToken('register-waiting', token)
          navigate('/register/waiting', { replace: true })
          return
        }
        const { tenant: org, joined, ...signedIn } = answer
        applySignIn(qc, signedIn)
        // A person who made an organisation is told so, and what its address is; one who joined one, which.
        if (org) setState({ kind: 'org', org })
        else if (joined) setState({ kind: 'joined', org: joined })
        else navigate('/', { replace: true })
      },
      async (err: unknown) => {
        const code = err instanceof ApiError ? err.code : ''
        // The link of a sign-up that became a request to join is used: it opens the request's waiting page.
        if (code === 'not_found') {
          const waiting = await api.joinStatus(token).then(
            () => true,
            () => false,
          )
          if (waiting) {
            passFragmentToken('register-waiting', token)
            navigate('/register/waiting', { replace: true })
            return
          }
        }
        setState({
          kind: 'error',
          message:
            code === 'not_found'
              ? tm.confirm.invalid
              : code === 'registration_closed'
                ? tm.confirm.closed
                : code === 'exists'
                  ? tm.confirm.exists
                  : code === 'org_limit'
                    ? tm.confirm.orgLimit
                    : code === 'join_pending'
                      ? tm.confirm.joinPending
                      : (code === 'join_limit' || code === 'email_domain_not_allowed') && err instanceof ApiError
                        ? err.message
                        : code === 'sso_required' && err instanceof ApiError
                          ? err.message
                          : code === 'rate_limited' && err instanceof ApiError
                            ? rateText(err)
                            : tm.confirm.failed,
        })
      },
    )
  }, [gate, token, qc, navigate])
  if (gate) return gate
  if (!token)
    return (
      <AuthLayout title={tm.confirm.title}>
        <FormMessage>{tm.confirm.noToken}</FormMessage>
      </AuthLayout>
    )
  if (state.kind === 'working')
    return (
      <AuthLayout title={tm.confirm.title} intro={tm.confirm.working}>
        <div aria-busy="true" role="status" className="h-16 animate-pulse rounded-lg bg-muted" />
      </AuthLayout>
    )
  if (state.kind === 'org')
    return (
      <AuthLayout
        title={tm.confirm.orgReady(state.org.name)}
        intro={tm.confirm.orgBody(state.org.name, state.org.slug)}
      >
        {state.org.renamed && <FormMessage tone="info">{tm.confirm.orgRenamed(state.org.slug)}</FormMessage>}
        <Button type="button" onClick={() => navigate('/', { replace: true })}>
          {tm.confirm.orgContinue}
        </Button>
      </AuthLayout>
    )
  if (state.kind === 'joined')
    return (
      <AuthLayout title={tm.confirm.joinedTitle(state.org.name)} intro={tm.confirm.joinedBody(state.org.name)}>
        <Button type="button" onClick={() => navigate('/', { replace: true })}>
          {tm.confirm.orgContinue}
        </Button>
      </AuthLayout>
    )
  return (
    <AuthLayout title={tm.confirm.title}>
      <FormMessage>{state.message}</FormMessage>
      <Link to="/register" className="text-sm text-brand underline underline-offset-4">
        {tm.confirm.toRegister}
      </Link>
    </AuthLayout>
  )
}

// The waiting page of a sign-up that asked to join an organisation (3.6 d5): what became of the request, and a way to
// withdraw it while it waits. The registration link's token is the request's: it stays in this tab only.
export function RegisterWaitingPage() {
  const gate = useAccountsOnly()
  const qc = useQueryClient()
  const [token] = useState(() => fragmentToken('register-waiting'))
  const [asking, setAsking] = useState(false)
  const status = useQuery({
    queryKey: ['register', 'waiting', token],
    queryFn: ({ signal }) => api.joinStatus(token, signal),
    enabled: gate === null && token !== '',
    retry: false,
  })
  const withdraw = useMutation({
    mutationFn: () => api.withdrawJoin(token),
    onSuccess: (s) => {
      qc.setQueryData(['register', 'waiting', token], s)
      setAsking(false)
    },
  })
  if (gate) return gate
  if (!token)
    return (
      <AuthLayout title={tm.waiting.title}>
        <FormMessage>{tm.waiting.noToken}</FormMessage>
      </AuthLayout>
    )
  if (status.isPending)
    return (
      <AuthLayout title={tm.waiting.title} intro={tm.waiting.loading}>
        <div aria-busy="true" role="status" className="h-16 animate-pulse rounded-lg bg-muted" />
      </AuthLayout>
    )
  if (status.isError)
    return (
      <AuthLayout title={tm.waiting.title}>
        <FormMessage>
          {status.error instanceof ApiError && status.error.code === 'not_found'
            ? tm.waiting.invalid
            : tm.waiting.failed}
        </FormMessage>
        <Link to="/register" className="text-sm text-brand underline underline-offset-4">
          {tm.waiting.toRegister}
        </Link>
      </AuthLayout>
    )
  const s = status.data
  const name = s.tenant.name
  const text =
    s.state === 'pending'
      ? tm.waiting.pending(name)
      : s.state === 'approved'
        ? tm.waiting.approved(name)
        : s.state === 'denied'
          ? tm.waiting.denied(name)
          : s.state === 'withdrawn'
            ? tm.waiting.withdrawn
            : s.state === 'expired'
              ? tm.waiting.expired
              : tm.waiting.cancelled(name)
  return (
    <AuthLayout title={tm.waiting.title} intro={text}>
      <div className="grid min-w-0 gap-1 text-sm" data-slot="join-status" data-state={s.state}>
        <p className="min-w-0 break-words text-muted-foreground">{tm.waiting.account(s.username, s.email)}</p>
        {s.state === 'pending' && (
          <p className="text-muted-foreground">{tm.waiting.expires(absoluteTime(s.expires))}</p>
        )}
      </div>
      <FormMessage>
        {withdraw.error ? (withdraw.error instanceof ApiError ? withdraw.error.message : t.auth.failed) : ''}
      </FormMessage>
      {s.state === 'pending' &&
        (asking ? (
          <div className="grid gap-3 rounded-lg border p-3" role="group" aria-label={tm.waiting.withdrawTitle}>
            <p className="text-sm font-medium">{tm.waiting.withdrawTitle}</p>
            <p className="text-sm text-muted-foreground">{tm.waiting.withdrawBody}</p>
            <div className="flex flex-col gap-2 sm:flex-row">
              <Button
                type="button"
                variant="destructive"
                disabled={withdraw.isPending}
                onClick={() => withdraw.mutate()}
              >
                {withdraw.isPending ? tm.waiting.withdrawing : tm.waiting.withdraw}
              </Button>
              <Button type="button" variant="outline" onClick={() => setAsking(false)}>
                {tm.waiting.cancel}
              </Button>
            </div>
          </div>
        ) : (
          <Button type="button" variant="outline" onClick={() => setAsking(true)}>
            {tm.waiting.withdraw}
          </Button>
        ))}
      {s.state === 'approved' ? (
        <Link to="/login" className={buttonVariants()}>
          {tm.waiting.signIn}
        </Link>
      ) : (
        s.state !== 'pending' && (
          <Link to="/register" className="text-sm text-brand underline underline-offset-4">
            {tm.waiting.toRegister}
          </Link>
        )
      )}
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
  if (!token)
    return (
      <AuthLayout title={tm.verify.title}>
        <FormMessage>{tm.verify.noToken}</FormMessage>
      </AuthLayout>
    )
  return (
    <AuthLayout title={tm.verify.title} intro={state === 'working' ? tm.verify.working : undefined}>
      {state === 'done' && <FormMessage tone="info">{tm.verify.done}</FormMessage>}
      {state === 'invalid' && <FormMessage>{tm.verify.invalid}</FormMessage>}
      {state === 'failed' && <FormMessage>{tm.verify.failed}</FormMessage>}
      {state !== 'working' && (
        <Link to="/" className="text-sm text-brand underline underline-offset-4">
          {tm.verify.toApp}
        </Link>
      )}
    </AuthLayout>
  )
}

// --- password reset ----------------------------------------------------------------------

export function ForgotPage() {
  const gate = useAccountsOnly()
  const [params] = useSearchParams()
  const tenant = params.get('tenant') ?? undefined
  const captcha = useCaptcha('forgot', { tenant })
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
      await api.forgotPassword({
        ...(tenant ? { tenant } : {}),
        login: login.trim(),
        ...(answer ? { captcha: answer } : {}),
        lang: mailLang(),
      })
      setSent(true)
    } catch (err) {
      setBusy(false)
      void captcha.refresh()
      if (err instanceof ApiError && err.code === 'captcha_failed')
        setError(answer ? ts.captcha.failed : ts.captcha.required)
      else if (err instanceof ApiError && err.code === 'password_reset_off') setError(tm.forgot.off)
      else if (err instanceof ApiError && err.code === 'rate_limited') setError(rateText(err))
      else setError(err instanceof ApiError ? err.message : tm.forgot.failed)
    }
  }
  if (sent) {
    return (
      <AuthLayout title={tm.forgot.sentTitle} intro={tm.forgot.sentBody}>
        <Link to="/login" className="text-sm text-brand underline underline-offset-4">
          {tm.links.backToLogin}
        </Link>
      </AuthLayout>
    )
  }
  return (
    <AuthLayout title={tm.forgot.title} intro={tm.forgot.intro}>
      <FormMessage focusRef={messageRef}>{error}</FormMessage>
      <form className="grid gap-4" onSubmit={(e) => void submit(e)}>
        <Field
          label={tm.forgot.login}
          value={login}
          onChange={(e) => setLogin(e.target.value)}
          autoComplete="username"
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          autoFocus
          required
        />
        <CaptchaField captcha={captcha} disabled={busy} />
        <Button type="submit" disabled={busy || !login.trim() || captcha.missing}>
          {busy ? tm.forgot.submitting : tm.forgot.submit}
        </Button>
      </form>
      <Link to="/login" className="text-sm text-brand underline underline-offset-4">
        {tm.links.backToLogin}
      </Link>
    </AuthLayout>
  )
}

export function ResetPasswordPage() {
  const gate = useAccountsOnly()
  const [token] = useState(() => fragmentToken('reset-password'))
  const [check, setCheck] = useState<
    { kind: 'checking' } | { kind: 'ok'; username: string } | { kind: 'invalid' } | { kind: 'error'; message: string }
  >({ kind: 'checking' })
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
        setCheck(
          err instanceof ApiError && err.code === 'not_found'
            ? { kind: 'invalid' }
            : {
                kind: 'error',
                message:
                  err instanceof ApiError
                    ? err.code === 'rate_limited'
                      ? rateText(err)
                      : err.message
                    : tm.reset.failed,
              },
        )
      },
    )
    return () => ctl.abort()
  }, [gate, token])
  if (gate) return gate
  if (!token)
    return (
      <AuthLayout title={tm.reset.title}>
        <FormMessage>{tm.reset.noToken}</FormMessage>
      </AuthLayout>
    )
  if (done) {
    return (
      <AuthLayout title={tm.reset.title}>
        <FormMessage tone="info">{tm.reset.done}</FormMessage>
        <Link to="/login" className="text-sm text-brand underline underline-offset-4">
          {tm.links.backToLogin}
        </Link>
      </AuthLayout>
    )
  }
  if (check.kind === 'checking')
    return (
      <AuthLayout title={tm.reset.title} intro={tm.reset.checking}>
        <div aria-busy="true" role="status" className="h-16 animate-pulse rounded-lg bg-muted" />
      </AuthLayout>
    )
  if (check.kind === 'invalid' || check.kind === 'error') {
    return (
      <AuthLayout title={tm.reset.title}>
        <FormMessage>{check.kind === 'invalid' ? tm.reset.invalid : check.message}</FormMessage>
        <Link to="/forgot" className="text-sm text-brand underline underline-offset-4">
          {tm.reset.again}
        </Link>
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
      else
        setError(
          err instanceof ApiError ? (err.code === 'rate_limited' ? rateText(err) : err.message) : tm.reset.failed,
        )
    }
  }
  return (
    <AuthLayout title={tm.reset.title} intro={tm.reset.intro(check.username)}>
      <FormMessage>{error}</FormMessage>
      <form className="grid gap-4" onSubmit={(e) => void submit(e)} noValidate>
        <Field
          label={tm.reset.password}
          help={t.auth.passwordHelp}
          type="password"
          value={pass}
          onChange={(e) => setPass(e.target.value)}
          autoComplete="new-password"
          autoFocus
          required
        />
        <Field
          label={tm.reset.confirm}
          type="password"
          value={again}
          onChange={(e) => setAgain(e.target.value)}
          autoComplete="new-password"
          required
        />
        <Button type="submit" disabled={busy}>
          {busy ? tm.reset.submitting : tm.reset.submit}
        </Button>
      </form>
    </AuthLayout>
  )
}
