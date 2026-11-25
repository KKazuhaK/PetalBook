import { useEffect, useRef } from 'react'
import { useNavigate } from 'react-router'
import { api } from '@/api/client'
import { rememberSpace, useSession, useUnreadCount } from '@/api/hooks'
import { browserNoticeDetails, browserNotifyState, showBrowserNotice } from '@/lib/browserNotify'
import { tn } from '@/i18n/notify'
import { describeNotification } from './notifyText'

// Shows a browser notice when the bell finds more unread than it did, while nobody is looking at the page (hidden, or
// another window in front). It renders nothing; it only listens to the count the bell already keeps. The notice says how
// many came; with the person's "details" preference it says what the newest is (an event, not a problem that was there
// already: the derived problems come first in the list, so the newest event is looked for among the rest).
export function BrowserNotifier() {
  const signedIn = !!useSession().data?.user
  const count = useUnreadCount(signedIn).data
  const before = useRef<number | null>(null)
  const navigate = useNavigate()
  useEffect(() => {
    if (count === undefined) return
    const was = before.current
    before.current = count
    if (was === null || count <= was || browserNotifyState() !== 'on') return
    if (document.visibilityState === 'visible' && document.hasFocus()) return
    const n = count - was
    if (!browserNoticeDetails()) {
      showBrowserNotice(tn.browser.count(n), '', () => navigate('/notifications'))
      return
    }
    void api
      .notifications({ unread: true, limit: 1 })
      .then((r) => {
        const newest = r.items.find((i) => i.at !== null) ?? r.items[0]
        const d = newest ? describeNotification(newest) : null
        showBrowserNotice(d?.title ?? tn.browser.count(n), d?.body ?? '', () => {
          if (d?.space) rememberSpace(d.space)
          navigate(d?.to || '/notifications')
        })
      })
      .catch(() => undefined)
  }, [count, navigate])
  return null
}
