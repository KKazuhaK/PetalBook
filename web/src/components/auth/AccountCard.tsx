import { safeAvatar, type LastAccount } from '@/lib/lastAccount'
import { ts } from '@/i18n/signin'

// The person the sign-in page is offering to sign in again: avatar (or the first letter of the name), the name and
// the username or email. Nothing in it is a control.
export function AccountCard({ account }: { account: LastAccount }) {
  const avatar = safeAvatar(account.avatarUrl)
  const second = account.email || account.username
  return (
    <div
      role="group"
      aria-label={ts.ended.accountLabel}
      className="flex min-w-0 items-center gap-3 rounded-lg border bg-muted/40 px-3 py-2.5 animate-in fade-in-0 duration-200 motion-reduce:animate-none"
    >
      {avatar ? (
        <img src={avatar} alt="" referrerPolicy="no-referrer" className="size-10 shrink-0 rounded-full object-cover" />
      ) : (
        <span
          aria-hidden="true"
          className="flex size-10 shrink-0 items-center justify-center rounded-full bg-brand/15 text-base font-medium text-brand"
        >
          {Array.from(account.name || account.username)[0]?.toUpperCase() ?? '?'}
        </span>
      )}
      <div className="grid min-w-0 flex-1">
        <span className="truncate text-sm font-medium">{account.name || account.username}</span>
        <span className="truncate text-xs text-muted-foreground">{second}</span>
      </div>
    </div>
  )
}
