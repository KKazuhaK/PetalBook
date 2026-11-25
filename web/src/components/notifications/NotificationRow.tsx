import { Link } from 'react-router'
import type { NotificationItem } from '@/api/types'
import { rememberSpace, useMarkNotificationsRead } from '@/api/hooks'
import { relativeTime } from '@/lib/time'
import { tn } from '@/i18n/notify'
import { describeNotification } from './notifyText'

// One notification: a red dot while unread, the title, a line of body and when. Clicking goes to its target and marks it
// read (a derived one is marked as it says now); a link, so it also opens in another tab.
export function NotificationRow({
  item,
  onOpen,
  now,
}: {
  item: NotificationItem
  // Called after a click, for the popover that holds the row to close.
  onOpen?: () => void
  now?: number
}) {
  const mark = useMarkNotificationsRead()
  const d = describeNotification(item)
  const click = () => {
    if (!item.read) mark.mutate([item.id])
    // The settings of a space are the ones of the space used last.
    if (d.space) rememberSpace(d.space)
    onOpen?.()
  }
  const inner = (
    <>
      <span
        aria-hidden="true"
        data-slot="notification-dot"
        className={`mt-2 size-2 shrink-0 rounded-full transition-colors duration-200 ${item.read ? 'bg-border' : 'bg-destructive'}`}
      />
      <span className="flex min-w-0 flex-col gap-0.5">
        <span className="text-sm font-semibold break-words">
          {d.title}
          {!item.read && <span className="sr-only"> ({tn.unreadMark})</span>}
        </span>
        {d.body && <span className="text-[0.8125rem] break-words text-muted-foreground">{d.body}</span>}
        <span className="text-xs text-muted-foreground">{item.at ? relativeTime(item.at, now) : tn.ongoing}</span>
      </span>
    </>
  )
  const cls = `flex min-h-10 gap-3 border-b px-4 py-3 text-left outline-none transition-colors duration-150 last:border-b-0 hover:bg-accent focus-visible:bg-accent focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset ${item.read ? '' : 'bg-brand/5'}`
  return d.to ? (
    <Link to={d.to} onClick={click} data-slot="notification" data-unread={!item.read} className={cls}>
      {inner}
    </Link>
  ) : (
    <button type="button" onClick={click} data-slot="notification" data-unread={!item.read} className={`${cls} w-full`}>
      {inner}
    </button>
  )
}
