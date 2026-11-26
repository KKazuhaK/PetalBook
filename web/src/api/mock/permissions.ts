// The registry of permission keys, generated from the server's (internal/authz): the mock answers GET /api/admin/permissions
// with it and grants the built-in roles from it.
import registries from '../generated/registries.json' with { type: 'json' }
import type { PermissionInfo, PermissionKey } from '../types.ts'

export const PERMISSIONS: PermissionInfo[] = registries.permissions.map(({ name, scope, feature, description }) => ({
  name: name as PermissionKey,
  scope: scope as PermissionInfo['scope'],
  feature,
  description,
}))

// The keys of a built-in role in the caller's own tenant (internal/authz): a site administrator every site and tenant
// key, a tenant administrator every tenant key, a member tenant.spaces.create.
export function permissionsOfRole(role: string, siteAdmin: boolean): PermissionKey[] {
  if (role === 'admin' && siteAdmin) return PERMISSIONS.filter((p) => p.scope !== 'space').map((p) => p.name)
  if (role === 'admin') return PERMISSIONS.filter((p) => p.scope === 'tenant').map((p) => p.name)
  if (role === 'member') return ['tenant.spaces.create']
  return []
}
