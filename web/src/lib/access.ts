// What the signed-in person may do outside spaces. The server says it in `user.permissions` of GET /api/session and of every sign-in answer
// (the permission keys they hold in their own tenant, internal/authz); the app asks here and never compares role names or
// the site-administrator flag, so a custom role on the server needs no change in the app. A lint-style test
// (access.test.ts) keeps such checks out of every other file. What a person may do in a space is lib/permissions.ts.

import type { PermissionKey, SessionUser, User } from '@/api/types'

type Caller = Pick<SessionUser, 'permissions'> | null | undefined

// Whether the person holds the key. Someone whose permissions are not known (none came with the session) holds none.
export function can(user: Caller, key: PermissionKey): boolean {
  return user?.permissions?.includes(key) ?? false
}

// Whether the person administers anything: holds a key beyond the one every member holds. The administration is
// theirs to open.
export function administers(user: Caller): boolean {
  if (!user?.permissions) return false
  return user.permissions.some((k) => k !== 'tenant.spaces.create' && !k.startsWith('space.'))
}

// Whether the person acts across the whole site (a site administrator): sees every tenant and the server's own
// state. Asked by the key of the tenants themselves.
export function acrossSite(user: Caller): boolean {
  return can(user, 'site.tenants.manage')
}

// About an account shown (not the caller): whether it is an administrator, and of what.
export function isAdminAccount(u: Pick<User, 'role'>): boolean {
  return u.role === 'admin'
}

export function adminKindOf(u: Pick<User, 'role' | 'siteAdmin'>): 'site' | 'tenant' | null {
  if (u.role !== 'admin') return null
  return u.siteAdmin ? 'site' : 'tenant'
}
