import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Link, Navigate, useNavigate, useSearchParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { api, ApiError, providerStartUrl } from '@/api/client'
import { applySignIn, useAuthProviders, useSession } from '@/api/hooks'
import type { AuthProviders } from '@/api/types'
import { AuthLayout, OrDivider } from '@/components/auth/AuthLayout'
import { CaptchaField, useCaptcha } from '@/components/auth/Captcha'
import { Field, FormMessage } from '@/components/auth/Field'
import { PasskeyButton } from '@/components/auth/PasskeyButton'
import { ProviderLinks } from '@/components/auth/ProviderButtons'
import { PageFallback, ErrorState } from '@/components/common'
import { Button } from '@/components/ui/button'
import { safeReturn } from '@/lib/auth'
import { t } from '@/i18n'
import { ts } from '@/i18n/signin'

// The sentence for a `?sso_error=` code. Own keys only: a code like `constructor` is not a message.
export const ssoErrorMessage = (code: string | null): string => (code ? (Object.hasOwn(ts.ssoErrors, code) ? ts.ssoErrors[code] : ts.ssoErrors.unknown) : '')

export default function LoginPage() {
  const session = useSession()
  const [params] = useSearchParams()
  const ret = safeReturn(params.get('return'))
  const signedIn = session.data?.mode === 'accounts' && !!session.data.user
  const needsProviders = session.data?.mode === 'accounts' && !session.data.setup && !signedIn
  const providers = useAuthProviders(needsProviders)

  if (session.isPending) return <PageFallback />
  if (session.isError) return <ErrorState message={t.fmt.labelled(t.auth.loadFailed, session.error.message)} onRetry={() => void session.refetch()} />
  const s = session.data
  // Nothing to sign in to, or already signed in, or nobody exists yet.
  if (s.mode === 'single') return <Navigate to="/" replace />
  if (s.setup) return <Navigate to="/setup" replace />
  if (s.user) return <Navigate to={ret} replace />
  if (providers.isPending) return <PageFallback />
  if (providers.isError) return <ErrorState message={t.fmt.labelled(ts.login.loadFailed, providers.error.message)} onRetry={() => void providers.refetch()} />
  return <LoginForm ret={ret} providers={providers.data} ssoError={params.get('sso_error')} local={params.get('local') === '1'} />
}

function LoginForm({ ret, providers, ssoError, local }: { ret: string; providers: AuthProviders; ssoError: string | null; local: boolean }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const list = providers.providers
  // With no enabled provider the page is the form alone, whatever else the answer says.
  const mode = list.length === 0 ? 'local_only' : providers.loginMode
  const showButtons = list.length > 0 && providers.sso && mode !== 'local_only'
  // A page that sends people on to the provider never does it for someone who asked for the form, or who just came back from a failure.
  const redirecting = mode === 'sso_redirect' && list.length === 1 && !local && !ssoError
  // `sso_first` folds the form behind a link; `sso_redirect` has it only on `?local=1`.
  const formByDefault = mode === 'dual' || mode === 'local_only' || (mode === 'sso_redirect' && local) || local
  const [formOpen, setFormOpen] = useState(formByDefault)

  useEffect(() => {
    if (redirecting) window.location.assign(providerStartUrl(list[0].kind, list[0].slug, ret))
  }, [redirecting, list, ret])

  const [username, setUsername] = useState('')
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
  const captcha = useCaptcha('login', { auto: formOpen && !redirecting })
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
      const { user } = await api.login({ username: username.trim(), password: pass, ...(sent ? { captcha: sent } : {}) })
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
  const sso = showButtons && <ProviderLinks providers={list} ret={ret} />
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

  return (
    <AuthLayout title={t.login.title}>
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
          <button type="button" className="self-start text-sm text-brand underline underline-offset-4" onClick={() => setFormOpen(true)}>
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
            <Field label={t.auth.password} type="password" value={pass} onChange={(e) => setPass(e.target.value)} autoComplete="current-password" required />
            <CaptchaField captcha={captcha} disabled={busy} />
            <Button type="submit" disabled={busy || waiting || !username || !pass || captcha.missing}>
              {busy ? t.login.submitting : t.login.submit}
            </Button>
          </form>
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
