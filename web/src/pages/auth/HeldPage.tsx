import { useState } from 'react'
import { useLocation, useNavigate } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '@/api/client'
import { applySession, useAccount } from '@/api/hooks'
import { AuthLayout } from '@/components/auth/AuthLayout'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { PasskeysSection } from '@/components/settings/PasskeysSection'
import { Button } from '@/components/ui/button'
import { t } from '@/i18n'
import { ts } from '@/i18n/signin'

// A session held until a passkey is registered (`hold: "passkey"`): the person sees this and nothing else, and
// can sign out. The first passkey added lifts the hold (the session is read again), and the page it stood in for
// shows: the address is kept, so a consent page an MCP client sent the person to (/oauth/consent?req=…) comes back,
// also after the fresh sign-in that registering may ask for.
export default function HeldPage() {
  const { pathname, search } = useLocation()
  const account = useAccount(true)
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const signOut = async () => {
    setBusy(true)
    setError('')
    try {
      await api.logout()
    } catch (e) {
      setBusy(false)
      return setError(e instanceof ApiError ? e.message : t.auth.failed)
    }
    applySession(qc, null)
    navigate('/login', { replace: true })
  }

  return (
    <AuthLayout title={ts.held.title} intro={ts.held.help}>
      {account.isPending ? (
        <PageFallback />
      ) : account.isError ? (
        <ErrorState message={t.fmt.labelled(ts.passkeys.loadFailed, account.error.message)} onRetry={() => void account.refetch()} />
      ) : (
        <PasskeysSection account={account.data} returnTo={pathname + search} />
      )}
      <FormMessage>{error}</FormMessage>
      <Button type="button" variant="outline" className="self-start" disabled={busy} onClick={() => void signOut()}>
        {busy ? ts.held.signingOut : ts.held.signOut}
      </Button>
    </AuthLayout>
  )
}
