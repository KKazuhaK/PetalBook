import { useState } from 'react'
import { useNavigate } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '@/api/client'
import { applySession, keys, useAccount } from '@/api/hooks'
import type { SessionInfo } from '@/api/types'
import { AuthLayout } from '@/components/auth/AuthLayout'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { PasswordForm } from '@/components/settings/SecurityPanel'
import { Button } from '@/components/ui/button'
import { t } from '@/i18n'
import { ts } from '@/i18n/signin'

// A session held until the person chooses a new password (`hold: "password"`): they signed in with a temporary
// password an administrator set. They see this and nothing else, and can sign out. Choosing a password lifts the
// hold on the server; the session is then read again and the page they asked for shows.
export default function PasswordChangePage() {
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
    <AuthLayout title={ts.held.passwordTitle} intro={ts.held.passwordHelp}>
      {account.isPending ? (
        <PageFallback />
      ) : account.isError ? (
        <ErrorState message={account.error.message} onRetry={() => void account.refetch()} />
      ) : (
        <PasswordForm
          account={{ ...account.data, hasPassword: true }}
          onDone={() => qc.setQueryData<SessionInfo>(keys.session, (prev) => prev && { ...prev, hold: null })}
        />
      )}
      <FormMessage>{error}</FormMessage>
      <Button type="button" variant="outline" className="self-start" disabled={busy} onClick={() => void signOut()}>
        {busy ? ts.held.signingOut : ts.held.signOut}
      </Button>
    </AuthLayout>
  )
}
