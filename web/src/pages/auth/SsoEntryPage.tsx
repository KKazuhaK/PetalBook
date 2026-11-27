import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Link, Navigate, useSearchParams } from 'react-router'
import { api, ApiError, providerStartUrl } from '@/api/client'
import { useSession } from '@/api/hooks'
import { AuthLayout } from '@/components/auth/AuthLayout'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { Button } from '@/components/ui/button'
import { safeReturn } from '@/lib/auth'
import { setPendingMethod } from '@/lib/lastAccount'
import { t } from '@/i18n'
import { ts } from '@/i18n/signin'

// A text link of this page: 40px high at least, for a finger on a phone or tablet.
const LINK = 'inline-flex min-h-10 items-center self-start text-sm text-brand underline underline-offset-4'

// "Sign in with SSO" (3.6 d4): a work address only; its domain says which organisation's identity provider to go
// to, and the address goes along as the hint. Whether anyone has an account is never asked.
export default function SsoEntryPage() {
  const session = useSession()
  const [params] = useSearchParams()
  const ret = safeReturn(params.get('return'))
  const [email, setEmail] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [going, setGoing] = useState('')
  const messageRef = useRef<HTMLParagraphElement>(null)

  // A new message takes focus, so it is read out and keyboard users are where it is.
  useEffect(() => {
    if (error) messageRef.current?.focus()
  }, [error])

  if (session.isPending) return <PageFallback />
  if (session.isError)
    return (
      <ErrorState
        message={t.fmt.labelled(t.auth.loadFailed, session.error.message)}
        onRetry={() => void session.refetch()}
      />
    )
  if (session.data.mode === 'single') return <Navigate to="/" replace />
  if (session.data.user) return <Navigate to={ret} replace />

  const back = ret === '/' ? '/login' : `/login?return=${encodeURIComponent(ret)}`

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const address = email.trim()
    if (busy || !address) return
    setError('')
    if (!address.includes('@')) {
      setError(ts.ssoEntry.needAddress)
      return
    }
    setBusy(true)
    try {
      const { sso } = await api.discover(address)
      if (!sso) {
        setError(ts.ssoEntry.notFound)
        setBusy(false)
        return
      }
      setPendingMethod({ type: 'sso', kind: sso.kind, slug: sso.slug, name: sso.name })
      setGoing(sso.name)
      window.location.assign(providerStartUrl(sso.kind, sso.slug, ret, address))
    } catch (err) {
      setBusy(false)
      if (err instanceof ApiError && err.code === 'rate_limited')
        setError(ts.login.rateLimited(t.auth.wait(err.retryAfter ?? 60)))
      else setError(err instanceof ApiError ? err.message : t.login.failed)
    }
  }

  return (
    <AuthLayout title={ts.ssoEntry.title} intro={ts.ssoEntry.intro}>
      <FormMessage focusRef={messageRef}>{error}</FormMessage>
      {going ? (
        <p role="status" className="text-sm text-muted-foreground">
          {ts.login.goingTo(going)}
        </p>
      ) : (
        <form className="grid gap-4" onSubmit={(e) => void submit(e)} noValidate>
          <Field
            label={ts.ssoEntry.email}
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            autoComplete="username email"
            autoCapitalize="none"
            autoCorrect="off"
            spellCheck={false}
            className="h-10"
            autoFocus
            required
          />
          <Button type="submit" className="h-10" disabled={busy || !email.trim()}>
            {busy ? ts.login.finding : ts.login.next}
          </Button>
        </form>
      )}
      <Link to={back} className={LINK}>
        {ts.ended.backToSignIn}
      </Link>
    </AuthLayout>
  )
}
