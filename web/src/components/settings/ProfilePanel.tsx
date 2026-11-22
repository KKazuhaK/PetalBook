import { useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '@/api/client'
import { keys, useAccount, useSession } from '@/api/hooks'
import type { AccountInfo, SessionInfo } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { SettingsSection } from './SettingsParts'
import { Button } from '@/components/ui/button'
import { t } from '@/i18n'

// Profile: name and email (accounts mode only). The password and the linked
// sign-ins are on the Security panel.
export function ProfilePanel() {
  const session = useSession()
  const account = useAccount(session.data?.mode === 'accounts' && !!session.data.user)
  if (account.isPending || !session.data) return <PageFallback />
  if (account.isError) return <ErrorState message={t.fmt.labelled(t.settings.loadFailed, account.error.message)} onRetry={() => void account.refetch()} />
  return (
    <div className="space-y-8">
      <Sections account={account.data} />
    </div>
  )
}

function Sections({ account }: { account: AccountInfo }) {
  return <Profile account={account} />
}

function Profile({ account }: { account: AccountInfo }) {
  const qc = useQueryClient()
  const [name, setName] = useState(account.user.name)
  const [email, setEmail] = useState(account.user.email)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    setDone(false)
    try {
      const { user } = await api.updateAccount({ name: name.trim(), email: email.trim() })
      qc.setQueryData<AccountInfo>(keys.account, (prev) => prev && { ...prev, user })
      qc.setQueryData<SessionInfo>(keys.session, (prev) => prev && { ...prev, user })
      setName(user.name)
      setEmail(user.email)
      setDone(true)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t.auth.failed)
    }
    setBusy(false)
  }

  return (
    <SettingsSection title={t.settings.profile}>
      <form className="grid gap-4" onSubmit={(e) => void submit(e)}>
        <Field label={t.settings.name} value={name} onChange={(e) => { setName(e.target.value); setDone(false) }} autoComplete="name" required />
        <Field label={t.settings.email} help={t.settings.emailHelp} type="email" value={email} onChange={(e) => { setEmail(e.target.value); setDone(false) }} autoComplete="email" />
        <FormMessage>{error}</FormMessage>
        <div className="flex items-center gap-3">
          <Button type="submit" disabled={busy || !name.trim()}>
            {busy ? t.settings.saving : t.settings.save}
          </Button>
          {/* Announced when it appears. */}
          <span role="status" className="text-sm text-muted-foreground">
            {done ? t.settings.saved : ''}
          </span>
        </div>
      </form>
    </SettingsSection>
  )
}
