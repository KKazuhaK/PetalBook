import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Navigate, useNavigate } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '@/api/client'
import { applySignIn, useAuthProviders, useSession } from '@/api/hooks'
import type { InviteCheck, PublicProvider } from '@/api/types'
import { AuthLayout, OrDivider } from '@/components/auth/AuthLayout'
import { CaptchaField, useCaptcha } from '@/components/auth/Captcha'
import { ProviderActions } from '@/components/auth/ProviderButtons'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { Button } from '@/components/ui/button'
import { forgetFragmentToken, fragmentToken } from '@/lib/auth'
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'
import { ts } from '@/i18n/signin'
import { tu } from '@/i18n/usage'

// The invite token comes from the address (`/invite#<token>`) or is pasted. It
// is checked with the server before the person is asked for anything.
export default function InvitePage() {
  const session = useSession()
  if (session.isPending) return <PageFallback />
  if (session.isError) return <ErrorState message={t.fmt.labelled(t.auth.loadFailed, session.error.message)} onRetry={() => void session.refetch()} />
  if (session.data.mode === 'single') return <Navigate to="/" replace />
  return <InviteForm />
}

type Check = { state: 'none' } | { state: 'captcha' } | { state: 'checking' } | { state: 'ok'; invite: InviteCheck } | { state: 'invalid' } | { state: 'error'; message: string }

function InviteForm() {
  const providers = useAuthProviders()
  const list = providers.data?.sso ? providers.data.providers : []
  const captcha = useCaptcha('invite')
  // The server refused a password because only single sign-on may create accounts.
  const [ssoOnly, setSsoOnly] = useState(false)
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

  // One check per token, as soon as there is one and the captcha is known: when the server wants a captcha for it,
  // the person solves it first (`captcha` state).
  const ready = captcha.challenge !== null || captcha.loadError !== ''
  const runCheck = (signal?: AbortSignal) => {
    const sent = captcha.value()
    return api.checkInvite(token, sent, signal).then(
      (invite) => {
        setCheck({ state: 'ok', invite })
        // What the invite starts the form with: an administrator's own words win, so only what is still empty is filled.
        setUsername((v) => v || invite.username || '')
        setName((v) => v || invite.name || '')
        // A challenge is used once, right or wrong: the form below needs its own.
        if (sent) void captcha.refresh()
      },
      (e: unknown) => {
        if (e instanceof DOMException && e.name === 'AbortError') return
        if (e instanceof ApiError && e.code === 'captcha_failed') {
          setCheck({ state: 'captcha' })
          setError(sent ? ts.captcha.failed : ts.captcha.required)
          void captcha.refresh()
        } else if (e instanceof ApiError && e.code === 'rate_limited') setCheck({ state: 'error', message: ts.login.rateLimited(t.auth.wait(e.retryAfter ?? 60)) })
        else setCheck(e instanceof ApiError && e.code === 'not_found' ? { state: 'invalid' } : { state: 'error', message: e instanceof ApiError ? e.message : t.auth.failed })
      },
    )
  }
  useEffect(() => {
    // Waiting for the person to solve the captcha (the screen below says so) is not a check.
    if (!token || !ready || captcha.missing) return
    const ctl = new AbortController()
    void runCheck(ctl.signal)
    return () => ctl.abort()
    // Once per token (or a pasted one again), when the captcha is known; not at every key of an answer.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token, attempt, ready])
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
    const sent = captcha.value()
    try {
      const { user } = await api.acceptInvite({ token, username: username.trim(), name: name.trim() || undefined, password: pass, ...(sent ? { captcha: sent } : {}) })
      await applySignIn(qc, user)
      navigate('/', { replace: true })
    } catch (err) {
      setBusy(false)
      if (err instanceof ApiError && err.code === 'captcha_failed') {
        setError(sent ? ts.captcha.failed : ts.captcha.required)
        void captcha.refresh()
      } else if (err instanceof ApiError && err.code === 'local_login_refused') setSsoOnly(true)
      else if (err instanceof ApiError && err.code === 'rate_limited') setError(ts.login.rateLimited(t.auth.wait(err.retryAfter ?? 60)))
      else if (err instanceof ApiError && err.code === 'exists') setFieldError({ username: t.invite.taken })
      else if (err instanceof ApiError && err.code === 'not_found') setCheck({ state: 'invalid' })
      else if (err instanceof ApiError && err.code === 'invalid') setFieldError({ username: err.message })
      else setError(err instanceof ApiError ? err.message : t.auth.failed)
    }
  }

  const sso = async (p: PublicProvider) => {
    setError('')
    try {
      const { url } = await api.providerStart(p.kind, p.slug, { intent: 'invite', token, return: '/' })
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
          <CaptchaField captcha={captcha} />
          <Button type="submit" disabled={!pasted.trim() || captcha.missing}>
            {t.invite.check}
          </Button>
        </form>
      </AuthLayout>
    )
  }
  if (check.state === 'captcha' || (check.state === 'checking' && ready && captcha.missing)) {
    return (
      <AuthLayout title={t.invite.title} intro={t.invite.noToken}>
        <FormMessage focusRef={messageRef}>{error}</FormMessage>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            setError('')
            setCheck({ state: 'checking' })
            void runCheck()
          }}
        >
          <CaptchaField captcha={captcha} />
          <Button type="submit" disabled={captcha.missing}>
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
      {(check.invite.username || check.invite.name || check.invite.email) && (
        <div className="grid gap-1 rounded-lg border bg-muted/40 px-3 py-2 text-sm" role="note" data-slot="invite-prefill">
          <p>{tu.invite.prefilled}</p>
          {check.invite.email && <p className="text-xs break-words text-muted-foreground">{tu.invite.email(check.invite.email)}</p>}
        </div>
      )}
      {ssoOnly && <FormMessage>{ts.inviteSso.ssoOnly}</FormMessage>}
      {!ssoOnly && (
      <form className="grid gap-4" onSubmit={(e) => void submit(e)} noValidate>
        <Field label={t.auth.username} help={t.auth.usernameHelp} error={fieldError.username} value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" autoCapitalize="none" autoCorrect="off" spellCheck={false} autoFocus required />
        <Field label={t.auth.nameOptional} value={name} onChange={(e) => setName(e.target.value)} autoComplete="name" />
        <Field label={t.auth.password} help={t.auth.passwordHelp} error={fieldError.password} type="password" value={pass} onChange={(e) => setPass(e.target.value)} autoComplete="new-password" required />
        <Field label={t.auth.confirmPassword} type="password" value={again} onChange={(e) => setAgain(e.target.value)} autoComplete="new-password" required />
        <CaptchaField captcha={captcha} disabled={busy} />
        <Button type="submit" disabled={busy || captcha.missing}>
          {busy ? t.invite.submitting : t.invite.submit}
        </Button>
      </form>
      )}
      {list.length > 0 && (
        <>
          {!ssoOnly && <OrDivider />}
          <ProviderActions providers={list} label={ts.inviteSso.withProvider} onPick={(p) => void sso(p)} />
        </>
      )}
    </AuthLayout>
  )
}
