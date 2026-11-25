// Browser notifications (the Notifications API), only after the person opts in. The opt-in is the person's, one of their
// preferences (prefsStore: their personal space when they have one), so the next person who signs in on this browser does
// not get notices for themselves because someone else said yes; the permission is the browser's, and both are needed.
// By default a notice says only how many came, not whose space or who: the screen may be seen by others. The person can
// ask for the details (DETAILS_KEY). There is no push server yet: a notice is shown by an open tab of the app when the
// bell finds a new notification (a real Web Push, with a VAPID key, would also reach a browser with no tab open; it is
// reserved, see docs/api.md).

import { prefsStore } from './prefsStore'

const KEY = 'pb.notify.browser'
const DETAILS_KEY = 'pb.notify.details'

export type BrowserNotifyState = 'unsupported' | 'insecure' | 'denied' | 'on' | 'off'

const api = (): typeof Notification | null =>
  typeof window !== 'undefined' && 'Notification' in window ? window.Notification : null

const wanted = (): boolean => prefsStore().read(KEY) === true

function remember(on: boolean) {
  if (on) prefsStore().write(KEY, true)
  else prefsStore().remove(KEY)
}

// Whether a notice names the space and the person (off unless the person asks).
export const browserNoticeDetails = (): boolean => prefsStore().read(DETAILS_KEY) === true
export function setBrowserNoticeDetails(on: boolean): void {
  if (on) prefsStore().write(DETAILS_KEY, true)
  else prefsStore().remove(DETAILS_KEY)
}

// What the setting is now: it can only be on while the browser allows notifications.
export function browserNotifyState(): BrowserNotifyState {
  const n = api()
  if (!n) return 'unsupported'
  if (typeof window.isSecureContext === 'boolean' && !window.isSecureContext) return 'insecure'
  if (n.permission === 'denied') return 'denied'
  return wanted() && n.permission === 'granted' ? 'on' : 'off'
}

// Asks the browser (this is what shows its prompt: call it from the person's click, after saying why) and turns the
// setting on when it is allowed.
export async function enableBrowserNotify(): Promise<BrowserNotifyState> {
  const n = api()
  if (!n) return 'unsupported'
  if (typeof window.isSecureContext === 'boolean' && !window.isSecureContext) return 'insecure'
  let permission = n.permission
  if (permission === 'default') {
    try {
      permission = await n.requestPermission()
    } catch {
      permission = 'default'
    }
  }
  remember(permission === 'granted')
  return browserNotifyState()
}

export function disableBrowserNotify(): BrowserNotifyState {
  remember(false)
  return browserNotifyState()
}

// Whether the bell should go on asking while the tab is in the background (a notice is for when nobody is looking).
export const browserNotifyOn = (): boolean => browserNotifyState() === 'on'

export function showBrowserNotice(title: string, body: string, onClick: () => void): void {
  const N = api()
  if (!N || browserNotifyState() !== 'on') return
  try {
    // One notice at a time: a new one takes the place of the one before.
    const n = new N(title, { body, tag: 'petalbook-notifications' })
    n.onclick = () => {
      try {
        window.focus()
        n.close()
      } catch {
        // Nothing to focus.
      }
      onClick()
    }
  } catch {
    // Some browsers only allow it from a service worker: nothing is shown.
  }
}
