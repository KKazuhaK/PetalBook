// Small helpers for the sign-in pages.

// Where to go after signing in: only a plain path inside the app. A full URL, a
// protocol-relative `//host`, a backslash, a control character, or one of the
// sign-in pages themselves (which would loop) all become `/`.
export function safeReturn(raw: string | null | undefined): string {
  if (!raw || !raw.startsWith('/') || raw.startsWith('//') || raw.includes('\\')) return '/'
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(raw)) return '/'
  if (/^\/(login|setup|invite)([/?#]|$)/.test(raw)) return '/'
  return raw
}

// The sign-in address that brings the person back to `path` afterwards.
export function loginUrl(path: string): string {
  const back = safeReturn(path)
  return back === '/' ? '/login' : `/login?return=${encodeURIComponent(back)}`
}

// One-time tokens (the setup token, an invite) travel in the address fragment,
// `/setup#<token>`, so they are never sent to a server, a proxy or an access
// log. They are read once, taken out of the address bar at once, and kept in
// this variable only, never in a query string, in storage, in a log or in an
// error report.
const held = new Map<string, string>()
export function fragmentToken(page: string): string {
  if (typeof window === 'undefined') return ''
  const hash = window.location.hash.replace(/^#/, '')
  if (hash) {
    try {
      held.set(page, decodeURIComponent(hash))
    } catch {
      held.set(page, hash)
    }
    window.history.replaceState(window.history.state, '', window.location.pathname + window.location.search)
  }
  return held.get(page) ?? ''
}
export function forgetFragmentToken(page: string) {
  held.delete(page)
}
