// The account that was signed in last in this browser, so that the sign-in page can offer it again when a session
// ended without the person signing out.
//
// Privacy, by design: it lives in this browser's localStorage and nowhere else. It holds the display name, the
// username, the email (it is the `login_hint` for a provider), the avatar address and how the person signed in last.
// Never a password, a token, a cookie value or anything else that proves who they are. Signing out on purpose
// forgets it (`api.logout`); a session that ended passively keeps it.

import type { ProviderKind, User } from '@/api/types'

export type LastMethod =
  { type: 'password' } | { type: 'passkey' } | { type: 'sso'; kind: ProviderKind; slug: string; name: string }

export interface LastAccount {
  name: string
  username: string
  email: string
  avatarUrl?: string
  method: LastMethod
}

const KEY = 'petalbook.lastAccount'
// The method of a sign-in that goes through the provider's own page: it is only known to this tab, and only until the
// person is back.
const PENDING = 'petalbook.signinMethod'
const PENDING_MS = 15 * 60_000

const KINDS: readonly string[] = ['oidc', 'saml', 'github', 'oauth2']
const str = (v: unknown, max = 200): v is string => typeof v === 'string' && v.length <= max

function parseMethod(v: unknown): LastMethod | null {
  if (typeof v !== 'object' || v === null) return null
  const m = v as Record<string, unknown>
  if (m.type === 'password' || m.type === 'passkey') return { type: m.type }
  if (m.type === 'sso' && str(m.kind, 10) && KINDS.includes(m.kind) && str(m.slug, 100) && m.slug && str(m.name))
    return { type: 'sso', kind: m.kind as ProviderKind, slug: m.slug, name: m.name }
  return null
}

// An address that is safe to put in an `<img>`: this site's own path (one `/` and then neither `/` nor `\`, which a
// browser reads as another host), or https. No whitespace, control character or backslash anywhere: browsers drop
// tabs and newlines from addresses, so `/\t/host` would be `//host`.
export const safeAvatar = (u: unknown): string | undefined =>
  str(u, 2000) && !/[\s\p{Cc}\\]/u.test(u) && (/^\/[^/]/.test(u) || /^https:\/\/[^/]/.test(u)) ? u : undefined

export function lastAccount(): LastAccount | null {
  try {
    const raw = localStorage.getItem(KEY)
    if (!raw) return null
    const v = JSON.parse(raw) as Record<string, unknown>
    const method = parseMethod(v.method)
    if (!method || !str(v.name) || !str(v.username, 100) || !v.username || !str(v.email, 254)) return null
    const avatarUrl = safeAvatar(v.avatarUrl)
    return { name: v.name, username: v.username, email: v.email, ...(avatarUrl ? { avatarUrl } : {}), method }
  } catch {
    return null
  }
}

function store(a: LastAccount) {
  try {
    const next = JSON.stringify(a)
    if (localStorage.getItem(KEY) !== next) localStorage.setItem(KEY, next)
  } catch {
    // Private windows and blocked storage: the page then simply does not offer the account.
  }
}

// What of a user is kept, with how they signed in. `avatarUrl` is for an account that has one.
export function rememberAccount(
  user: Pick<User, 'name' | 'username' | 'email'> & { avatarUrl?: string },
  method: LastMethod,
) {
  // A sign-in some other way: a provider's page that was left half way no longer stands for it.
  if (method.type !== 'sso') clearPendingMethod()
  const avatarUrl = safeAvatar(user.avatarUrl)
  store({
    name: user.name,
    username: user.username,
    email: user.email,
    ...(avatarUrl ? { avatarUrl } : {}),
    method,
  })
}

export function forgetLastAccount() {
  try {
    localStorage.removeItem(KEY)
    sessionStorage.removeItem(PENDING)
  } catch {
    // Nothing to forget where nothing can be kept.
  }
}

// Before the browser leaves for a provider's page: how this sign-in is going to happen.
export function setPendingMethod(m: LastMethod) {
  try {
    sessionStorage.setItem(PENDING, JSON.stringify({ method: m, at: Date.now() }))
  } catch {
    // As above.
  }
}

// A sign-in that did not go through a provider's page succeeded: what was pending is not how it happened.
export function clearPendingMethod() {
  try {
    sessionStorage.removeItem(PENDING)
  } catch {
    // As above.
  }
}

// When the person is back with a session: the method that was pending, once, if it is recent.
export function takePendingMethod(now = Date.now()): LastMethod | null {
  try {
    const raw = sessionStorage.getItem(PENDING)
    if (!raw) return null
    sessionStorage.removeItem(PENDING)
    const v = JSON.parse(raw) as { method?: unknown; at?: unknown }
    if (typeof v.at !== 'number' || now - v.at > PENDING_MS || now < v.at) return null
    return parseMethod(v.method)
  } catch {
    return null
  }
}

// The session of this page's user is there: keep who it is. How they signed in is what was pending, or what was
// kept for the same username, or (a session that began some other way) a password.
export function noteSession(user: User) {
  const pending = takePendingMethod()
  const before = lastAccount()
  const method =
    pending ?? (before && before.username === user.username ? before.method : { type: 'password' as const })
  rememberAccount(
    {
      name: user.name,
      username: user.username,
      email: user.email,
      avatarUrl: before?.username === user.username ? before.avatarUrl : undefined,
    },
    method,
  )
}
