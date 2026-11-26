// Viewing the app as a person (docs/api.md, "Viewing as a person"). An administrator opens a view from the person's
// page; it opens in a new tab at `/as/<id>`. That tab, and only that tab, sends the header PetalBook-View-As: <id> on
// every request, and the server answers it as the person, read only.
//
// The id lives in the tab's sessionStorage, which no other tab sees, and is read once when the tab loads
// (`startViewAs`, called before the app is drawn): nothing sets it while the app renders, and the API client adds the
// header in the one place that adds the scope's (api/client.ts). Leaving the view clears it and closes the tab.

export const VIEW_AS_HEADER = 'PetalBook-View-As'
const KEY = 'petalbook.viewAs'
// The address the console opens: `/as/<id>`.
const ENTRY = /^\/as\/([A-Za-z0-9_-]{1,80})\/?$/

let current: string | null = null

// Reads the tab's view, taking it from the address first when the tab was opened at `/as/<id>` (the address then
// becomes `/`, so a reload stays in the view without the id in the address). Called once, before the app is drawn.
export function startViewAs(loc: Pick<Location, 'pathname'> = window.location): string | null {
  const m = ENTRY.exec(loc.pathname)
  try {
    if (m) {
      window.sessionStorage.setItem(KEY, m[1])
      window.history.replaceState(null, '', '/')
    }
    current = window.sessionStorage.getItem(KEY)
  } catch {
    // No storage (a private window that refuses it): the view lives as long as this page.
    current = m ? m[1] : null
    if (m) window.history.replaceState(null, '', '/')
  }
  if (current) {
    document.documentElement.dataset.viewAs = ''
    freezeSharedStorage()
  } else delete document.documentElement.dataset.viewAs
  return current
}

// localStorage is shared with the administrator's own tabs: what the person's state would write there (last space,
// recents, sidebar, preferences) must not reach them. In a view tab every write to it is dropped; reads still work.
// sessionStorage (the tab's own) is untouched. Undone by `forgetViewAs`.
let unfreeze: (() => void) | null = null
function freezeSharedStorage(): void {
  if (unfreeze) return
  try {
    const local = window.localStorage
    const proto = Object.getPrototypeOf(local) as Storage
    const { setItem, removeItem, clear } = proto
    const guard = <A extends unknown[]>(fn: (this: Storage, ...a: A) => void) =>
      function (this: Storage, ...a: A) {
        if (this !== local) return fn.apply(this, a)
      }
    proto.setItem = guard(setItem)
    proto.removeItem = guard(removeItem)
    proto.clear = guard(clear)
    unfreeze = () => {
      proto.setItem = setItem
      proto.removeItem = removeItem
      proto.clear = clear
      unfreeze = null
    }
  } catch {
    // No storage: nothing to leak.
  }
}

// The id of this tab's view, or null when the tab is not one.
export const viewAsId = (): string | null => current

// The header every request of this tab carries.
export const viewAsHeaders = (): Record<string, string> => (current ? { [VIEW_AS_HEADER]: current } : {})

// Forgets this tab's view (the tab then is the administrator's own again).
export function forgetViewAs(): void {
  current = null
  try {
    window.sessionStorage.removeItem(KEY)
  } catch {
    // nothing kept
  }
  delete document.documentElement.dataset.viewAs
  unfreeze?.()
}

// Minutes left until `until`, at least 0.
export const minutesLeft = (until: string, now = Date.now()): number =>
  Math.max(0, Math.ceil((Date.parse(until) - now) / 60_000))
