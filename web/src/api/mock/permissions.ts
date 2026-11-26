// The registry of permission keys, as the server has it (internal/authz): the mock answers GET /api/admin/permissions
// with it and grants the built-in roles from it.
import type { PermissionInfo, PermissionKey } from '../types.ts'

export const PERMISSIONS: PermissionInfo[] = [
  { name: 'site.settings.manage', scope: 'site', capability: '', description: "Read and change the site's settings" },
  {
    name: 'site.security.manage',
    scope: 'site',
    capability: '',
    description: 'Read and change the sign-in security of the site',
  },
  {
    name: 'site.signin.manage',
    scope: 'site',
    capability: '',
    description: 'Add, change and remove sign-in providers',
  },
  {
    name: 'site.mail.manage',
    scope: 'site',
    capability: '',
    description: 'Read and change the mail settings, and send a test message',
  },
  {
    name: 'site.access.manage',
    scope: 'site',
    capability: '',
    description: 'Read and change who may register and sign in',
  },
  { name: 'site.tenancy.manage', scope: 'site', capability: '', description: 'Turn tenants on and off' },
  { name: 'site.tenants.manage', scope: 'site', capability: '', description: 'List, create and delete tenants' },
  {
    name: 'site.connections.manage',
    scope: 'site',
    capability: '',
    description: 'Set up the providers people connect to',
  },
  {
    name: 'site.daily.manage',
    scope: 'site',
    capability: '',
    description: 'Set up the daily text: its interfaces, providers and lines',
  },
  { name: 'site.status.read', scope: 'site', capability: '', description: "Read the server's status" },
  {
    name: 'site.storage.manage',
    scope: 'site',
    capability: '',
    description: "Change the server's storage budget and a space's storage, and evict spaces",
  },
  {
    name: 'site.recycle.purge',
    scope: 'site',
    capability: '',
    description: 'Purge an item of the recycle bin for good',
  },
  { name: 'tenant.settings.read', scope: 'tenant', capability: '', description: 'Read the tenant and its settings' },
  { name: 'tenant.settings.write', scope: 'tenant', capability: '', description: 'Change the tenant and its settings' },
  {
    name: 'tenant.users.read',
    scope: 'tenant',
    capability: '',
    description: 'List people and read their accounts, memberships, sessions, identities and passkeys',
  },
  {
    name: 'tenant.users.manage',
    scope: 'tenant',
    capability: '',
    description: "Change people's accounts, end their sessions, remove identities and passkeys, send password resets",
  },
  { name: 'tenant.users.delete', scope: 'tenant', capability: '', description: 'Delete people' },
  {
    name: 'tenant.invites.manage',
    scope: 'tenant',
    capability: '',
    description: 'Create, list and withdraw invitations',
  },
  {
    name: 'tenant.groups.read',
    scope: 'tenant',
    capability: '',
    description: 'List groups, their members and their spaces',
  },
  {
    name: 'tenant.groups.manage',
    scope: 'tenant',
    capability: '',
    description: 'Create, change and delete groups and their members',
  },
  { name: 'tenant.spaces.create', scope: 'tenant', capability: '', description: 'Create spaces' },
  { name: 'tenant.spaces.read', scope: 'tenant', capability: '', description: "List the tenant's spaces" },
  {
    name: 'tenant.spaces.manage',
    scope: 'tenant',
    capability: '',
    description:
      'Manage a space without being its member: its settings and members, but not its owners or its remote; measure, disable and enable it',
  },
  {
    name: 'tenant.spaces.takeover',
    scope: 'tenant',
    capability: '',
    description: 'Take over a space (become its owner) or transfer it; audited, and its owners are told',
  },
  { name: 'tenant.spaces.delete', scope: 'tenant', capability: '', description: 'Delete spaces' },
  {
    name: 'tenant.data.view',
    scope: 'tenant',
    capability: 'data.adminView',
    description: 'Open an audited, read-only view of a space',
  },
  {
    name: 'tenant.data.export',
    scope: 'tenant',
    capability: '',
    description: 'Export spaces and download the exports',
  },
  {
    name: 'tenant.storage.read',
    scope: 'tenant',
    capability: '',
    description: 'Read the storage used by the tenant and its people',
  },
  { name: 'tenant.recycle.read', scope: 'tenant', capability: '', description: 'List the recycle bin' },
  {
    name: 'tenant.recycle.restore',
    scope: 'tenant',
    capability: '',
    description: 'Restore an item of the recycle bin',
  },
  {
    name: 'tenant.bulk.run',
    scope: 'tenant',
    capability: '',
    description: 'Confirm and run bulk actions, and follow their jobs',
  },
  { name: 'tenant.usage.read', scope: 'tenant', capability: '', description: 'Read the usage of the tenant' },
  { name: 'tenant.audit.read', scope: 'tenant', capability: '', description: 'Read the audit log' },
  {
    name: 'tenant.tokens.read',
    scope: 'tenant',
    capability: '',
    description: "List people's API tokens and app authorizations",
  },
  {
    name: 'tenant.tokens.revoke',
    scope: 'tenant',
    capability: '',
    description: "Revoke people's API tokens and app authorizations",
  },
  { name: 'tenant.permissions.read', scope: 'tenant', capability: '', description: 'List the permission keys' },
  { name: 'space.read', scope: 'space', capability: '', description: 'See pages, history, attachments and search' },
  {
    name: 'space.write',
    scope: 'space',
    capability: '',
    description: 'Create, change, move and delete pages and folders',
  },
  {
    name: 'space.members.manage',
    scope: 'space',
    capability: '',
    description: 'Add, change and remove the members of the space',
  },
  {
    name: 'space.manage',
    scope: 'space',
    capability: '',
    description: 'Rename the space; its inbox, remote, sync, types and templates',
  },
]

// The keys of a built-in role in the caller's own tenant (internal/authz): a site administrator every site and tenant
// key, a tenant administrator every tenant key, a member tenant.spaces.create.
export function permissionsOfRole(role: string, siteAdmin: boolean): PermissionKey[] {
  if (role === 'admin' && siteAdmin) return PERMISSIONS.filter((p) => p.scope !== 'space').map((p) => p.name)
  if (role === 'admin') return PERMISSIONS.filter((p) => p.scope === 'tenant').map((p) => p.name)
  if (role === 'member') return ['tenant.spaces.create']
  return []
}
