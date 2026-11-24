import { useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '@/api/client'
import { keys, useAccount, useAuthProviders, useSession } from '@/api/hooks'
import type { AccountInfo, SessionInfo } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { SettingsSection } from './SettingsParts'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { t } from '@/i18n'
import { tg } from '@/i18n/groups'
import { tm } from '@/i18n/mail'
import { mailLang } from '@/pages/auth/MailPages'

// Profile: name and email (accounts mode only). The password and the linked
// sign-ins are on the Security panel.
export function ProfilePanel() {
  const session = useSession()
  const account = useAccount(session.data?.mode === 'accounts' && !!session.data.user)
  if (account.isPending || !session.data) return <PageFallback />
  if (account.isError)
    return (
      <ErrorState
        message={t.fmt.labelled(t.settings.loadFailed, account.error.message)}
        onRetry={() => void account.refetch()}
      />
    )
  return (
    <div className="space-y-8">
      <Sections account={account.data} />
    </div>
  )
}

function Sections({ account }: { account: AccountInfo }) {
  return (
    <>
      <Profile account={account} />
      <MyGroups account={account} />
    </>
  )
}

// The groups the person is in: names only, as everyone may see them.
function MyGroups({ account }: { account: AccountInfo }) {
  const groups = account.groups ?? []
  return (
    <SettingsSection title={tg.account.title} id="my-groups-heading" description={tg.account.help}>
      {groups.length === 0 ? (
        <p className="text-sm text-muted-foreground">{tg.account.none}</p>
      ) : (
        <ul className="flex flex-wrap gap-2" data-slot="my-groups">
          {groups.map((g) => (
            <li key={g.id}>
              <Badge variant={g.source === 'synced' ? 'secondary' : 'outline'}>
                {g.name} · {tg.account.source[g.source]}
              </Badge>
            </li>
          ))}
        </ul>
      )}
    </SettingsSection>
  )
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
      <EmailVerification user={account.user} />
      <form className="grid gap-4" onSubmit={(e) => void submit(e)}>
        <Field
          label={t.settings.name}
          value={name}
          onChange={(e) => {
            setName(e.target.value)
            setDone(false)
          }}
          autoComplete="name"
          required
        />
        <Field
          label={t.settings.email}
          help={t.settings.emailHelp}
          type="email"
          value={email}
          onChange={(e) => {
            setEmail(e.target.value)
            setDone(false)
          }}
          autoComplete="email"
        />
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

// Whether the address counts, and a way to ask for the link that makes it count (when the server can send mail).
function EmailVerification({ user }: { user: AccountInfo['user'] }) {
  const providers = useAuthProviders(true)
  const mailOn = providers.data?.forgot === true
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState<{ tone: 'error' | 'info'; text: string } | null>(null)
  if (!user.email)
    return (
      <p className="text-sm text-muted-foreground">
        {tm.account.title}: {tm.account.none}
      </p>
    )
  const ask = async () => {
    setBusy(true)
    setMessage(null)
    try {
      await api.requestEmailVerify(mailLang())
      setMessage({ tone: 'info', text: tm.account.sent(user.email) })
    } catch (err) {
      const code = err instanceof ApiError ? err.code : ''
      setMessage({
        tone: 'error',
        text:
          code === 'mail_not_configured'
            ? tm.account.notConfigured
            : code === 'rate_limited' && err instanceof ApiError
              ? tm.account.rateLimited(t.auth.wait(err.retryAfter ?? 60))
              : tm.account.failed,
      })
    }
    setBusy(false)
  }
  return (
    <div className="grid gap-2 text-sm" data-slot="email-verification">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">{tm.account.title}</span>
        <span className="break-all text-muted-foreground">{user.email}</span>
        <Badge variant={user.emailVerified ? 'secondary' : 'outline'}>
          {user.emailVerified ? tm.account.verified : tm.account.unverified}
        </Badge>
        {!user.emailVerified && mailOn && (
          <Button type="button" size="sm" variant="outline" disabled={busy} onClick={() => void ask()}>
            {busy ? tm.account.sending : tm.account.verify}
          </Button>
        )}
      </div>
      {!user.emailVerified && <p className="text-xs text-muted-foreground">{tm.account.help}</p>}
      {message && <FormMessage tone={message.tone}>{message.text}</FormMessage>}
    </div>
  )
}
