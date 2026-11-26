// The registry of permission keys, as the server has it (internal/authz): the mock answers GET /api/admin/permissions
// with it and grants the built-in roles from it.
import type { PermissionInfo, PermissionKey } from '../types.ts'

export const PERMISSIONS: PermissionInfo[] = [
  { name: 'site.settings.manage', scope: 'site', feature: '', description: "Read and change the site's settings" },
  {
    name: 'site.security.manage',
    scope: 'site',
    feature: '',
    description: 'Read and change the sign-in security of the site',
  },
  {
    name: 'site.signin.manage',
    scope: 'site',
    feature: '',
    description: 'Add, change and remove sign-in providers',
  },
  {
    name: 'site.mail.manage',
    scope: 'site',
    feature: '',
    description: 'Read and change the mail settings, and send a test message',
  },
  {
    name: 'site.tenants.manage',
    scope: 'site',
    feature: '',
    description:
      "List, create and delete tenants; change a tenant's slug, state and quota, and give a person more than their tenant allows",
  },
  {
    name: 'site.connections.manage',
    scope: 'site',
    feature: '',
    description: 'Set up the providers people connect to',
  },
  {
    name: 'site.daily.manage',
    scope: 'site',
    feature: '',
    description: 'Set up the daily text: its interfaces, providers and lines',
  },
  { name: 'site.status.read', scope: 'site', feature: '', description: "Read the server's status" },
  {
    name: 'site.storage.manage',
    scope: 'site',
    feature: '',
    description:
      "Change the server's storage budget and a space's storage, evict and pin spaces, see the disk, and exempt a person from the site's requirement of a remote",
  },
  {
    name: 'site.recycle.purge',
    scope: 'site',
    feature: '',
    description: 'Purge an item of the recycle bin for good',
  },
  {
    name: 'site.admins.manage',
    scope: 'site',
    feature: '',
    description:
      'Change administrators of the whole server (their accounts, passwords, passkeys, sessions and tokens), delete them, and make or end one',
  },
  {
    name: 'site.spaces.personal.meta',
    scope: 'site',
    feature: '',
    description:
      "See people's personal spaces in the administration (in lists, the recycle bin and a person's page, with their size), restore them with their person, and manage one without being its member; not their content",
  },
  {
    name: 'site.spaces.personal.content',
    scope: 'site',
    feature: '',
    description:
      "Open an audited view of a person's personal space and export it; held by the built-in site administrator only",
  },
  { name: 'tenant.settings.read', scope: 'tenant', feature: '', description: 'Read the tenant and its settings' },
  { name: 'tenant.settings.write', scope: 'tenant', feature: '', description: 'Change the tenant and its settings' },
  {
    name: 'tenant.users.read',
    scope: 'tenant',
    feature: '',
    description: 'List people and read their accounts, memberships, sessions, identities and passkeys',
  },
  {
    name: 'tenant.users.manage',
    scope: 'tenant',
    feature: '',
    description: "Change people's accounts, end their sessions, remove identities and passkeys, send password resets",
  },
  { name: 'tenant.users.delete', scope: 'tenant', feature: '', description: 'Delete people' },
  {
    name: 'tenant.invites.manage',
    scope: 'tenant',
    feature: '',
    description: 'Create, list and withdraw invitations',
  },
  {
    name: 'tenant.groups.read',
    scope: 'tenant',
    feature: '',
    description: 'List groups, their members and their spaces',
  },
  {
    name: 'tenant.groups.manage',
    scope: 'tenant',
    feature: '',
    description: 'Create, change and delete groups and their members',
  },
  { name: 'tenant.spaces.create', scope: 'tenant', feature: '', description: 'Create spaces' },
  { name: 'tenant.spaces.read', scope: 'tenant', feature: '', description: "List the tenant's spaces" },
  {
    name: 'tenant.spaces.manage',
    scope: 'tenant',
    feature: '',
    description:
      'Manage a space without being its member: its settings and members, but not its owners or its remote; measure, disable and enable it',
  },
  {
    name: 'tenant.spaces.takeover',
    scope: 'tenant',
    feature: '',
    description: 'Take over a space (become its owner) or transfer it; audited, and its owners are told',
  },
  { name: 'tenant.spaces.delete', scope: 'tenant', feature: '', description: 'Delete spaces' },
  {
    name: 'tenant.data.view',
    scope: 'tenant',
    feature: 'features.adminView',
    description: 'Open an audited, read-only view of a space',
  },
  {
    name: 'tenant.users.viewAs',
    scope: 'tenant',
    feature: 'features.adminView',
    description:
      "View the app as a person sees it: read only, for an hour, audited; never personal spaces, drafts or the person's account",
  },
  {
    name: 'tenant.data.export',
    scope: 'tenant',
    feature: '',
    description: 'Export spaces and download the exports',
  },
  {
    name: 'tenant.storage.read',
    scope: 'tenant',
    feature: '',
    description: 'Read the storage used by the tenant and its people',
  },
  { name: 'tenant.recycle.read', scope: 'tenant', feature: '', description: 'List the recycle bin' },
  {
    name: 'tenant.recycle.restore',
    scope: 'tenant',
    feature: '',
    description: 'Restore an item of the recycle bin',
  },
  {
    name: 'tenant.bulk.run',
    scope: 'tenant',
    feature: '',
    description: 'Confirm and run bulk actions, and follow their jobs',
  },
  { name: 'tenant.usage.read', scope: 'tenant', feature: '', description: 'Read the usage of the tenant' },
  { name: 'tenant.audit.read', scope: 'tenant', feature: '', description: 'Read the audit log' },
  {
    name: 'tenant.tokens.read',
    scope: 'tenant',
    feature: '',
    description: "List people's API tokens and app authorizations",
  },
  {
    name: 'tenant.tokens.revoke',
    scope: 'tenant',
    feature: '',
    description: "Revoke people's API tokens and app authorizations",
  },
  { name: 'tenant.permissions.read', scope: 'tenant', feature: '', description: 'List the permission keys' },
  { name: 'space.read', scope: 'space', feature: '', description: 'See pages, history, attachments and search' },
  {
    name: 'space.write',
    scope: 'space',
    feature: '',
    description: 'Create, change, move and delete pages and folders',
  },
  {
    name: 'space.members.manage',
    scope: 'space',
    feature: '',
    description: 'Add, change and remove the members of the space',
  },
  {
    name: 'space.manage',
    scope: 'space',
    feature: '',
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
