import { useState } from 'react'
import { Link } from 'react-router'
import { Bell } from 'lucide-react'
import { useMarkAllNotificationsRead, useNotificationList, useSession, useUnreadCount } from '@/api/hooks'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { useIsMobile } from '@/hooks/use-mobile'
import { tn } from '@/i18n/notify'
import { topBarButton } from '@/components/layout/topBarStyles'
import { NotificationRow } from './NotificationRow'
import { viewAsId } from '@/lib/viewAs'

// How many of the latest the popover shows; the page has the rest.
const POPOVER_ITEMS = 5

const triggerCls = topBarButton

function Dot({ count }: { count: number }) {
  return (
    <span
      aria-hidden="true"
      data-slot="bell-dot"
      className={`absolute top-1.5 right-1.5 size-2.5 rounded-full border-2 border-background bg-destructive transition-all duration-200 ${count > 0 ? 'scale-100 opacity-100' : 'scale-0 opacity-0'}`}
    />
  )
}

// The bell in the top bar of every page, with a red dot while something is unread. On a desktop or a tablet it opens a
// popover with the latest items; on a phone it goes straight to the full page.
export function NotificationBell() {
  const session = useSession().data
  // A view as a person has no bell: the person's notifications are their own (lib/viewAs).
  const signedIn = !!session?.user && viewAsId() === null
  const count = useUnreadCount(signedIn).data ?? 0
  const mobile = useIsMobile()
  const [open, setOpen] = useState(false)
  if (!signedIn) return null
  const label = count > 0 ? tn.bell.unread(count) : tn.bell.label
  if (mobile)
    return (
      <Link to="/notifications" aria-label={label} data-slot="bell" className={triggerCls}>
        <Bell className="size-[18px]" />
        <Dot count={count} />
      </Link>
    )
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger aria-label={label} data-slot="bell" className={triggerCls}>
        <Bell className="size-[18px]" />
        <Dot count={count} />
      </PopoverTrigger>
      <PopoverContent align="end" sideOffset={6} className="w-[min(24rem,calc(100vw-1.5rem))] gap-0 p-0">
        {open && <BellPanel close={() => setOpen(false)} />}
      </PopoverContent>
    </Popover>
  )
}

function BellPanel({ close }: { close: () => void }) {
  const list = useNotificationList('', false, POPOVER_ITEMS)
  const markAll = useMarkAllNotificationsRead()
  const items = list.data?.pages[0]?.items.slice(0, POPOVER_ITEMS) ?? []
  const unread = list.data?.pages[0]?.unread ?? 0
  return (
    <section aria-label={tn.title} data-slot="bell-panel">
      <div className="flex items-center justify-between gap-2 border-b px-4 py-2">
        <h2 className="text-[0.9375rem] font-semibold">{tn.title}</h2>
        <button
          type="button"
          disabled={unread === 0 || markAll.isPending}
          onClick={() => markAll.mutate(undefined)}
          className="h-8 rounded-md px-2 text-[0.8125rem] text-brand outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50 disabled:text-muted-foreground disabled:no-underline pointer-coarse:h-10"
        >
          {tn.markAll}
        </button>
      </div>
      <div className="max-h-[min(26rem,60vh)] overflow-y-auto overscroll-contain">
        {list.isPending && <p className="px-4 py-6 text-sm text-muted-foreground">{tn.loading}</p>}
        {list.isError && (
          <div className="flex flex-col items-start gap-2 px-4 py-4 text-sm">
            <p className="text-muted-foreground">{tn.loadFailed}</p>
            <button type="button" onClick={() => void list.refetch()} className="text-brand hover:underline">
              {tn.retry}
            </button>
          </div>
        )}
        {list.isSuccess && items.length === 0 && (
          <div className="px-4 py-6 text-sm">
            <p className="font-medium">{tn.empty}</p>
            <p className="mt-1 text-muted-foreground">{tn.emptyHelp}</p>
          </div>
        )}
        {items.map((n) => (
          <NotificationRow key={n.id} item={n} onOpen={close} />
        ))}
      </div>
      <div className="border-t px-4 py-2 text-[0.8125rem]">
        <Link
          to="/notifications"
          onClick={close}
          className="inline-flex h-8 items-center text-brand hover:underline pointer-coarse:h-10"
        >
          {tn.viewAll}
        </Link>
      </div>
    </section>
  )
}
