// After a deploy, a page that was already open asks for chunk files whose
// hashed names the new build no longer has. Fetching the new index.html fixes
// it, so the first such failure reloads the page, once.

import { flushAllSaves } from '@/editor/saveController'

export const RELOAD_KEY = 'petalbook.staleChunkReload'
// A second chunk failure this soon after a reload means the chunk is really
// missing; reloading again would loop.
export const RELOAD_WINDOW_MS = 10_000
// How long a pending save may hold the reload up.
const FLUSH_WAIT_MS = 4_000

// Chrome, Firefox and Safari word the same failure differently.
const CHUNK_ERROR =
  /error loading dynamically imported module|Failed to fetch dynamically imported module|Importing a module script failed/i

export function isChunkLoadError(error: unknown): boolean {
  const message = error instanceof Error ? error.message : typeof error === 'string' ? error : ''
  return CHUNK_ERROR.test(message)
}

// Replaceable by tests.
export const env = { reload: () => window.location.reload() }

interface Deps {
  storage?: Pick<Storage, 'getItem' | 'setItem'> | null
  reload?: () => void
  // Stores whatever is typed and unsaved.
  flush?: () => Promise<unknown>
  now?: () => number
}

let pending = false

export function resetStaleChunkForTests() {
  pending = false
}

function sessionStore(): Deps['storage'] {
  try {
    return window.sessionStorage
  } catch {
    return null
  }
}

// True when the page is being reloaded (or already is): the caller should show
// nothing and let it happen. False when a reload was just tried, so the failure
// is real and the person should be told.
export function recoverFromStaleChunk(deps: Deps = {}): boolean {
  if (pending) return true
  const storage = deps.storage === undefined ? sessionStore() : deps.storage
  const now = (deps.now ?? Date.now)()
  let last = NaN
  try {
    last = Number(storage?.getItem(RELOAD_KEY))
  } catch {
    // no storage: treated as not yet reloaded
  }
  if (Number.isFinite(last) && last > 0 && now - last < RELOAD_WINDOW_MS) return false
  try {
    storage?.setItem(RELOAD_KEY, String(now))
  } catch {
    // Without a flag the guard cannot work; do not risk a loop.
    return false
  }
  pending = true
  const reload = deps.reload ?? (() => env.reload())
  const flush = deps.flush ?? flushAllSaves
  // Typing is stored first; a save that fails or hangs must not stop the reload.
  const settled = Promise.resolve()
    .then(flush)
    .catch(() => undefined)
  const timeout = new Promise<void>((r) => setTimeout(r, FLUSH_WAIT_MS))
  void Promise.race([settled, timeout]).then(() => reload())
  return true
}

// Vite reports a failed preload of a dynamic import on window.
export function installStaleChunkHandler() {
  window.addEventListener('vite:preloadError', (event) => {
    // If the reload is not going to happen, let the error reach the boundary.
    if (recoverFromStaleChunk()) event.preventDefault()
  })
}
