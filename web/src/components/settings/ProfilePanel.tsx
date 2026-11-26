import { useState } from 'react'
import { MailCheck, Pencil } from 'lucide-react'
import { api, ApiError } from '@/api/client'
import { useAccount, useAuthProviders, useSession } from '@/api/hooks'
import type { AccountInfo } from '@/api/types'
import { Avatar } from '@/components/admin/users/Avatar'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { SettingsSection } from './SettingsParts'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ChangeEmailDialog, EditProfileDialog } from '@/components/people/ProfileDialogs'
import { lang, languages, t } from '@/i18n'
import { tg } from '@/i18n/groups'
import { tp } from '@/i18n/people'
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

// The values, read-only: the avatar, name, username, email and language. One button edits name, username and
// language in a dialog; the email has its own button (the new address is verified); the password is in Security.
function Profile({ account }: { account: AccountInfo }) {
  const [editing, setEditing] = useState(false)
  const [changingEmail, setChangingEmail] = useState(false)
  const user = account.user
  const language = languages.find((l) => l.code === lang)?.name ?? lang
  return (
    <SettingsSection title={t.settings.profile}>
      <div className="flex flex-wrap items-center gap-3">
        <Avatar name={user.name} size="lg" />
        <div className="min-w-0 flex-1">
          <div className="truncate text-base font-medium">{user.name}</div>
          <div className="truncate text-sm text-muted-foreground">{user.username}</div>
        </div>
        <Button type="button" onClick={() => setEditing(true)}>
          <Pencil aria-hidden="true" />
          {tp.profile.editProfile}
        </Button>
      </div>
      <dl className="grid gap-3 rounded-lg border p-3 text-sm sm:grid-cols-[8rem_1fr]" data-slot="profile-values">
        <dt className="text-muted-foreground">{tp.profile.name}</dt>
        <dd className="min-w-0 break-words">{user.name}</dd>
        <dt className="text-muted-foreground">{tp.profile.username}</dt>
        <dd className="min-w-0 break-words">{user.username}</dd>
        <dt className="text-muted-foreground">{tp.profile.email}</dt>
        <dd className="flex min-w-0 flex-wrap items-center gap-2">
          <span className="break-all">{user.email || tp.profile.noEmail}</span>
          <Button type="button" variant="outline" size="sm" onClick={() => setChangingEmail(true)}>
            {tp.profile.change}
          </Button>
        </dd>
        <dt className="text-muted-foreground">{tp.profile.language}</dt>
        <dd>{language}</dd>
        <dt className="text-muted-foreground">{tp.profile.password}</dt>
        <dd className="text-xs text-muted-foreground">{tp.profile.passwordNote}</dd>
      </dl>
      {user.email && <EmailVerification user={user} />}
      <EditProfileDialog user={user} open={editing} onOpenChange={setEditing} />
      <ChangeEmailDialog user={user} open={changingEmail} onOpenChange={setChangingEmail} />
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
            <MailCheck aria-hidden="true" />
            {busy ? tm.account.sending : tm.account.verify}
          </Button>
        )}
      </div>
      {!user.emailVerified && <p className="text-xs text-muted-foreground">{tm.account.help}</p>}
      {message && <FormMessage tone={message.tone}>{message.text}</FormMessage>}
    </div>
  )
}
