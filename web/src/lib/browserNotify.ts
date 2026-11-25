// Browser notifications (the Notifications API), only after the person opts in. The choice is this browser's (the
// permission is too), so it is kept here in localStorage and is not one of the person's synced preferences. There is no
// push server yet: a notice is shown by an open tab of the app when the bell finds a new notification (a real Web Push,
// with a VAPID key, would also reach a browser with no tab open; it is reserved, see docs/api.md).

const KEY = 'pb.notify.browser'

export type BrowserNotifyState = 'unsupported' | 'insecure' | 'denied' | 'on' | 'off'

const api = (): typeof Notification | null =>
  typeof window !== 'undefined' && 'Notification' in window ? window.Notification : null

function wanted(): boolean {
  try {
    return localStorage.getItem(KEY) === '1'
  } catch {
    return false
  }
}

function remember(on: boolean) {
  try {
    if (on) localStorage.setItem(KEY, '1')
    else localStorage.removeItem(KEY)
  } catch {
    // Not kept: it is off again the next time the page loads.
  }
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
