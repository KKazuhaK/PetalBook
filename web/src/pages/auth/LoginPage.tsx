import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Link, Navigate, useNavigate, useSearchParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { api, ApiError, providerStartUrl } from '@/api/client'
import { applySignIn, keys, useAuthProviders, useSession } from '@/api/hooks'
import type { AuthProviders, EndedReason, SessionInfo } from '@/api/types'
import { AccountCard } from '@/components/auth/AccountCard'
import { AuthLayout, OrDivider } from '@/components/auth/AuthLayout'
import { CaptchaField, useCaptcha } from '@/components/auth/Captcha'
import { Field, FormMessage } from '@/components/auth/Field'
import { PasskeyButton } from '@/components/auth/PasskeyButton'
import { ProviderLinks, WIDE } from '@/components/auth/ProviderButtons'
import { ProviderIcon } from '@/components/auth/ProviderIcon'
import { PageFallback, ErrorState } from '@/components/common'
import { Button, buttonVariants } from '@/components/ui/button'
import { safeReturn } from '@/lib/auth'
import { lastAccount, rememberAccount, setPendingMethod, type LastAccount, type LastMethod } from '@/lib/lastAccount'
import { t } from '@/i18n'
import { ts } from '@/i18n/signin'
import { tm } from '@/i18n/mail'

// The sentence for a `?sso_error=` code. Own keys only: a code like `constructor` is not a message.
export const ssoErrorMessage = (code: string | null): string =>
  code ? (Object.hasOwn(ts.ssoErrors, code) ? ts.ssoErrors[code] : ts.ssoErrors.unknown) : ''

// Whether the person's last way of signing in can still be used here.
function usable(m: LastMethod, p: AuthProviders): boolean {
  if (m.type === 'password') return p.local
  if (m.type === 'passkey') return p.passkey
  return p.sso && p.providers.some((x) => x.kind === m.kind && x.slug === m.slug)
}

export default function LoginPage() {
  const session = useSession()
  const qc = useQueryClient()
  const [params] = useSearchParams()
  // "Use another account" (and the way back from a disabled one): the ordinary page, with nothing about the last person.
  const [dismissed, setDismissed] = useState(false)
  const dismiss = () => {
    setDismissed(true)
    qc.setQueryData<SessionInfo>(keys.session, (prev) => prev && { ...prev, endedReason: undefined })
  }
  const ret = safeReturn(params.get('return'))
  const signedIn = session.data?.mode === 'accounts' && !!session.data.user
  const needsProviders = session.data?.mode === 'accounts' && !session.data.setup && !signedIn
  const providers = useAuthProviders(needsProviders)

  if (session.isPending) return <PageFallback />
  if (session.isError)
    return (
      <ErrorState
        message={t.fmt.labelled(t.auth.loadFailed, session.error.message)}
        onRetry={() => void session.refetch()}
      />
    )
  const s = session.data
  // Nothing to sign in to, or already signed in, or nobody exists yet.
  if (s.mode === 'single') return <Navigate to="/" replace />
  if (s.setup) return <Navigate to="/setup" replace />
  if (s.user) return <Navigate to={ret} replace />
  const reason = dismissed ? null : (s.endedReason ?? null)
  // A disabled account is told so, and nothing about it is offered: no name, no form.
  if (reason === 'disabled') return <DisabledNotice ret={ret} onBack={dismiss} />
  if (providers.isPending) return <PageFallback />
  if (providers.isError)
    return (
      <ErrorState
        message={t.fmt.labelled(ts.login.loadFailed, providers.error.message)}
        onRetry={() => void providers.refetch()}
      />
    )
  return (
    <LoginForm
      key={reason ? 'ended' : 'plain'}
      reason={reason}
      account={reason ? lastAccount() : null}
      onOther={dismiss}
      ret={ret}
      providers={providers.data}
      ssoError={params.get('sso_error')}
      local={params.get('local') === '1'}
    />
  )
}

// The account of a person whose account was switched off: the message and the way back, nothing else.
function DisabledNotice({ ret, onBack }: { ret: string; onBack: () => void }) {
  const messageRef = useRef<HTMLParagraphElement>(null)
  useEffect(() => messageRef.current?.focus(), [])
  return (
    <AuthLayout title={t.login.title}>
      <FormMessage focusRef={messageRef}>{ts.ended.disabled}</FormMessage>
      <Link
        to={ret === '/' ? '/login' : `/login?return=${encodeURIComponent(ret)}`}
        onClick={onBack}
        className="inline-flex min-h-10 items-center self-start text-sm text-brand underline underline-offset-4"
      >
        {ts.ended.backToSignIn}
      </Link>
    </AuthLayout>
  )
}

function LoginForm({
  reason,
  account,
  onOther,
  ret,
  providers,
  ssoError,
  local,
}: {
  // Why the last session ended, and who it was (when this browser knows): the page then speaks to that person.
  reason: EndedReason | null
  account: LastAccount | null
  onOther: () => void
  ret: string
  providers: AuthProviders
  ssoError: string | null
  local: boolean
}) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const list = providers.providers
  // The person the page speaks to, if their last way of signing in still works here.
  const returning = account && usable(account.method, providers) ? account : null
  // With no enabled provider the page is the form alone, whatever else the answer says.
  const mode = list.length === 0 ? 'local_only' : providers.loginMode
  const showButtons = list.length > 0 && providers.sso && mode !== 'local_only'
  // A page that sends people on to the provider never does it for someone who asked for the form, or who just came back from a failure.
  const redirecting = mode === 'sso_redirect' && list.length === 1 && !local && !ssoError && !returning
  // `sso_first` folds the form behind a link; `sso_redirect` has it only on `?local=1`.
  const formByDefault = mode === 'dual' || mode === 'local_only' || (mode === 'sso_redirect' && local) || local
  // Derived, not stored: the same form stays mounted when `?local=1` is added to the address.
  const [openedByClick, setOpenedByClick] = useState(false)
  const formOpen = formByDefault || openedByClick

  useEffect(() => {
    if (redirecting) {
      setPendingMethod({ type: 'sso', kind: list[0].kind, slug: list[0].slug, name: list[0].name })
      window.location.assign(providerStartUrl(list[0].kind, list[0].slug, ret))
    }
  }, [redirecting, list, ret])

  const [username, setUsername] = useState(returning?.username ?? '')
  const [pass, setPass] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState(ssoErrorMessage(ssoError))
  // Set when the password was right and the account must use a passkey.
  const [passkeyOnly, setPasskeyOnly] = useState(false)
  // When the server says to wait, until when (ms since the epoch), and what time it is now.
  const [until, setUntil] = useState(0)
  const [now, setNow] = useState(() => Date.now())
  const messageRef = useRef<HTMLParagraphElement>(null)
  const remaining = Math.max(0, Math.ceil((until - now) / 1000))
  const returningPassword = returning?.method.type === 'password'
  // Someone who is back is asked about by name from the start.
  const captcha = useCaptcha('login', { auto: formOpen && !redirecting && !returningPassword })
  const askCaptcha = captcha.refresh
  useEffect(() => {
    if (returningPassword) void askCaptcha(username.trim().toLowerCase())
    // Once, for the person the page opened with.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [returningPassword])
  const askedFor = useRef('')

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

  // The captcha depends on the username (failures are counted per name): ask again when the field is left with another one.
  const usernameLeft = () => {
    const name = username.trim().toLowerCase()
    if (!name || name === askedFor.current) return
    askedFor.current = name
    void captcha.refresh(name)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy || remaining > 0) return
    setBusy(true)
    setError('')
    setPasskeyOnly(false)
    const sent = captcha.value()
    try {
      const { user } = await api.login({
        username: username.trim(),
        password: pass,
        ...(sent ? { captcha: sent } : {}),
      })
      rememberAccount(user, { type: 'password' })
      await applySignIn(qc, user)
      navigate(ret, { replace: true })
    } catch (err) {
      setBusy(false)
      if (err instanceof ApiError && err.code === 'rate_limited') {
        const wait = err.retryAfter ?? 60
        setNow(Date.now())
        setUntil(Date.now() + wait * 1000)
        setError(ts.login.rateLimited(t.auth.wait(wait)))
      } else if (err instanceof ApiError && err.code === 'captcha_failed') {
        // A new challenge, and say whether the old one was missing or wrong.
        setError(sent ? ts.captcha.failed : ts.captcha.required)
        void captcha.refresh(username.trim().toLowerCase())
      } else if (err instanceof ApiError && err.code === 'local_login_refused') setError(ts.login.localRefused)
      else if (err instanceof ApiError && err.code === 'passkey_required') setPasskeyOnly(true)
      else setError(err instanceof ApiError ? err.message : t.login.failed)
      setPass('')
    }
  }

  const passkeyDone = async (r: { user: Parameters<typeof applySignIn>[1]; return: string }) => {
    rememberAccount(r.user, { type: 'passkey' })
    await applySignIn(qc, r.user)
    navigate(safeReturn(r.return), { replace: true })
  }

  if (redirecting) {
    return (
      <AuthLayout title={t.login.title}>
        <p role="status" className="text-sm text-muted-foreground">
          {ts.login.goingTo(list[0].name)}
        </p>
      </AuthLayout>
    )
  }

  const waiting = remaining > 0
  const passkey = providers.passkey && (
    <PasskeyButton
      username={username}
      ret={ret}
      onDone={(r) => void passkeyDone(r)}
      onError={(m) => {
        setError(m)
        setPasskeyOnly(false)
      }}
    />
  )
  const note = (p: { kind: Parameters<typeof providerStartUrl>[0]; slug: string; name: string }) =>
    setPendingMethod({ type: 'sso', kind: p.kind, slug: p.slug, name: p.name })
  const sso = showButtons && <ProviderLinks providers={list} ret={ret} onPick={note} />
  const stay = new URLSearchParams()
  if (ret !== '/') stay.set('return', ret)
  stay.set('local', '1')
  const retry = () => {
    // One provider: go straight to it again. Otherwise the message is cleared and the person picks.
    if (list.length === 1 && showButtons) window.location.assign(providerStartUrl(list[0].kind, list[0].slug, ret))
    else {
      setError('')
      navigate(ret === '/' ? '/login' : `/login?return=${encodeURIComponent(ret)}`, { replace: true })
    }
  }

  // The page for someone coming back after a session ended: why, who, and only the way they came in last.
  if (returning) {
    const m = returning.method
    const provider = m.type === 'sso' ? list.find((x) => x.kind === m.kind && x.slug === m.slug) : undefined
    return (
      <AuthLayout title={t.login.title}>
        {reason && <FormMessage tone="info">{ts.ended[reason]}</FormMessage>}
        <FormMessage focusRef={messageRef}>
          {waiting ? ts.login.rateLimited(t.auth.wait(remaining)) : error}
        </FormMessage>
        <AccountCard account={returning} />
        {m.type === 'password' && (
          <>
            <form className="grid gap-4" onSubmit={(e) => void submit(e)}>
              {/* For the browser's password manager: whose password this is. */}
              <input
                type="text"
                name="username"
                value={username}
                autoComplete="username"
                readOnly
                tabIndex={-1}
                aria-hidden="true"
                className="sr-only"
              />
              <Field
                label={t.auth.password}
                type="password"
                value={pass}
                onChange={(e) => setPass(e.target.value)}
                autoComplete="current-password"
                className="h-10"
                autoFocus
                required
              />
              <CaptchaField captcha={captcha} disabled={busy} />
              <Button type="submit" className="h-10" disabled={busy || waiting || !pass || captcha.missing}>
                {busy ? t.login.submitting : t.login.submit}
              </Button>
            </form>
            {providers.forgot && (
              <Link
                to="/forgot"
                className="inline-flex min-h-10 items-center self-start text-sm text-brand underline underline-offset-4"
              >
                {tm.links.forgot}
              </Link>
            )}
          </>
        )}
        {m.type === 'passkey' && (
          <PasskeyButton
            username={returning.username}
            ret={ret}
            size="lg"
            onDone={(r) => void passkeyDone(r)}
            onError={(msg) => {
              setError(msg)
              setPasskeyOnly(false)
            }}
          />
        )}
        {m.type === 'sso' && provider && (
          <a
            href={providerStartUrl(provider.kind, provider.slug, ret, returning.email)}
            onClick={() => note(provider)}
            className={`${buttonVariants({ variant: 'outline' })} ${WIDE} h-10`}
          >
            <ProviderIcon icon={provider.icon} />
            <span className="min-w-0 truncate">{ts.ended.continueWith(provider.name)}</span>
          </a>
        )}
        <button
          type="button"
          className="inline-flex min-h-10 items-center self-start text-sm text-brand underline underline-offset-4"
          onClick={onOther}
        >
          {ts.ended.otherAccount}
        </button>
      </AuthLayout>
    )
  }

  return (
    <AuthLayout title={t.login.title}>
      {reason && <FormMessage tone="info">{ts.ended[reason]}</FormMessage>}
      <FormMessage focusRef={messageRef}>{waiting ? ts.login.rateLimited(t.auth.wait(remaining)) : error}</FormMessage>
      {ssoError && error && showButtons && (
        <Button type="button" variant="outline" size="sm" className="self-start" onClick={retry}>
          {ts.login.ssoRetry}
        </Button>
      )}
      {passkeyOnly && (
        <div className="grid gap-2 rounded-md border bg-muted/40 p-3" role="status">
          <p className="text-sm font-medium">{ts.login.passkeyRequired}</p>
          <p className="text-xs text-muted-foreground">{ts.login.passkeyRequiredHelp}</p>
        </div>
      )}
      {mode === 'sso_first' && !formOpen && (
        <>
          {sso}
          {passkey}
          <button
            type="button"
            className="self-start text-sm text-brand underline underline-offset-4"
            onClick={() => setOpenedByClick(true)}
          >
            {ts.login.usePassword}
          </button>
        </>
      )}
      {mode === 'sso_redirect' && !formOpen && (
        <>
          {sso}
          {passkey}
          <Link to={`/login?${stay.toString()}`} className="self-start text-sm text-brand underline underline-offset-4">
            {ts.login.usePassword}
          </Link>
        </>
      )}
      {formOpen && (
        <>
          {passkeyOnly && passkey}
          <form className="grid gap-4" onSubmit={(e) => void submit(e)}>
            <Field
              label={t.auth.username}
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              onBlur={usernameLeft}
              autoComplete="username webauthn"
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              autoFocus
              required
            />
            <Field
              label={t.auth.password}
              type="password"
              value={pass}
              onChange={(e) => setPass(e.target.value)}
              autoComplete="current-password"
              required
            />
            <CaptchaField captcha={captcha} disabled={busy} />
            <Button type="submit" disabled={busy || waiting || !username || !pass || captcha.missing}>
              {busy ? t.login.submitting : t.login.submit}
            </Button>
          </form>
          {(providers.forgot || providers.register) && (
            <div className="flex flex-wrap justify-between gap-x-4 gap-y-1 text-sm">
              {providers.forgot && (
                <Link to="/forgot" className="text-brand underline underline-offset-4">
                  {tm.links.forgot}
                </Link>
              )}
              {providers.register && (
                <Link to="/register" className="text-brand underline underline-offset-4">
                  {tm.links.register}
                </Link>
              )}
            </div>
          )}
          {!passkeyOnly && passkey && (
            <>
              <OrDivider />
              {passkey}
            </>
          )}
          {showButtons && (
            <>
              <div className="flex items-center gap-3 text-xs text-muted-foreground" aria-hidden="true">
                <span className="h-px flex-1 bg-border" />
                {ts.login.orSignInWith}
                <span className="h-px flex-1 bg-border" />
              </div>
              {sso}
            </>
          )}
        </>
      )}
    </AuthLayout>
  )
}
