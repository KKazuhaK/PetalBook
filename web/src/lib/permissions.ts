// What the caller may do in a space. The server says it in `permissions` of the
// space; the app asks here and never compares role names, so a role added on
// the server later (a custom one, say) needs no change in the app.

import type { Permission, SpaceInfo } from '@/api/types'

type Subject = Pick<SpaceInfo, 'permissions' | 'disabled'> | null | undefined

// A disabled space cannot be opened by anyone, whatever the permissions they
// would have, so it grants nothing here.
export function can(space: Subject, permission: Permission): boolean {
  return !!space && !space.disabled && space.permissions.includes(permission)
}

export type SpacePermissions = Record<Permission, boolean>

export function permissionsOf(space: Subject): SpacePermissions {
  return { read: can(space, 'read'), write: can(space, 'write'), members: can(space, 'members'), manage: can(space, 'manage') }
}
