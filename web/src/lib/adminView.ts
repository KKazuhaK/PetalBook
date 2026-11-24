// The audited views an administrator has open (docs/api.md, "Viewing a space"). The server keeps a view per session in
// memory and does not say which are open, so the page remembers the ones it started, for as long as the tab lives:
// that is what it needs to show the banner and to know the space may be read. A server that forgot (a restart) answers
// 403 to the first read, and the page asks for a reason again.

import { useSyncExternalStore } from 'react'

const KEY = 'pb.adminViews'
type Views = Record<string, number>
const listeners = new Set<() => void>()
let cache: { raw: string | null; views: Views } = { raw: null, views: {} }

function read(): Views {
  let raw: string | null
  try {
    raw = sessionStorage.getItem(KEY)
  } catch {
    // Blocked storage: views are remembered until the page is reloaded (below).
    return memory
  }
  if (raw === cache.raw) return cache.views
  let views: Views = {}
  try {
    const parsed: unknown = raw ? JSON.parse(raw) : {}
    if (parsed && typeof parsed === 'object') views = parsed as Views
  } catch {
    views = {}
  }
  cache = { raw, views }
  return views
}
let memory: Views = {}
function write(views: Views) {
  memory = views
  try {
    sessionStorage.setItem(KEY, JSON.stringify(views))
  } catch {
    // Kept in memory only.
  }
  listeners.forEach((l) => l())
}

// When the view of the space ends, in ms; null when there is none (or it is over).
export function viewUntil(space: string, now = Date.now()): number | null {
  const t = read()[space]
  return typeof t === 'number' && t > now ? t : null
}
export function rememberView(space: string, untilIso: string) {
  const t = Date.parse(untilIso)
  if (Number.isNaN(t)) return
  write({ ...read(), [space]: t })
}
export function forgetView(space: string) {
  const { [space]: _gone, ...rest } = read()
  void _gone
  write(rest)
}
const subscribe = (l: () => void) => {
  listeners.add(l)
  return () => void listeners.delete(l)
}
// The views as a snapshot: a new object only when one was started or ended.
export function useAdminViews(): Views {
  return useSyncExternalStore(subscribe, read, read)
}
export const hasView = (views: Views, space: string, now = Date.now()) => (views[space] ?? 0) > now
