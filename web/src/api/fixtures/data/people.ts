// The people a fixture server knows. The caller of a route is whoever is signed in, with the permissions declared here.

import registries from '../../generated/registries.json' with { type: 'json' }
import type { PermissionKey } from '../../generated/permissionKeys.ts'
import type { SessionUser } from '../../types.ts'

export interface UserRec extends SessionUser {
  // What a login with this username must send. The fixture checks nothing else about a password.
  password: string
  // A person with a passkey, for the passkey sign-in.
  passkeys?: number
}

const all = registries.permissions.map((p) => p.name as PermissionKey)
export const MEMBER_PERMISSIONS: PermissionKey[] = ['tenant.spaces.create', 'space.read', 'space.write']

export const MOCK_ADMIN = { username: 'admin', password: 'petalbook-admin', name: '管理员' }
export const SETUP_TOKEN = 'setup-token'
export const INVITE_TOKEN = 'invite-token'

const tenant = { id: 'default', name: 'Default', slug: 'default' }
const created = '2026-01-05T08:00:00Z'

export const DEMO_ADMIN: UserRec = {
  id: '01FXADMIN1',
  username: MOCK_ADMIN.username,
  password: MOCK_ADMIN.password,
  name: MOCK_ADMIN.name,
  email: 'admin@example.test',
  emailVerified: true,
  role: 'admin',
  disabled: false,
  passkeyRequired: false,
  siteAdmin: true,
  tenant,
  created,
  permissions: all,
}

// Single-user mode has one person, "local", who is allowed everything.
export const DEMO_SINGLE_USER: UserRec = {
  ...DEMO_ADMIN,
  id: 'local',
  username: 'local',
  name: 'local',
  email: '',
  emailVerified: false,
}

export const DEMO_MEMBER: UserRec = {
  id: '01FXMEMBR1',
  username: 'carol',
  password: 'a-long-password',
  name: '卡罗',
  email: 'carol@example.test',
  emailVerified: true,
  role: 'member',
  disabled: false,
  passkeyRequired: false,
  siteAdmin: false,
  tenant,
  created,
  permissions: MEMBER_PERMISSIONS,
}
