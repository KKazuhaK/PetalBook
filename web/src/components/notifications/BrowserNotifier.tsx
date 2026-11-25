import { useEffect, useRef } from 'react'
import { useNavigate } from 'react-router'
import { api } from '@/api/client'
import { rememberSpace, useSession, useUnreadCount } from '@/api/hooks'
import { browserNotifyState, showBrowserNotice } from '@/lib/browserNotify'
import { tn } from '@/i18n/notify'
import { describeNotification } from './notifyText'

// Shows a browser notice when the bell finds more unread than it did, while nobody is looking at the page (hidden, or
// another window in front). It renders nothing; it only listens to the count the bell already keeps.
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
    void api
      .notifications({ unread: true, limit: 1 })
      .then((r) => {
        const first = r.items[0]
        const d = first ? describeNotification(first) : null
        showBrowserNotice(d?.title ?? tn.browser.shown(count - was), d?.body ?? '', () => {
          if (d?.space) rememberSpace(d.space)
          navigate(d?.to || '/notifications')
        })
      })
      .catch(() => undefined)
  }, [count, navigate])
  return null
}
