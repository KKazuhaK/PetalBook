# Sign-in and permissions

## 6. Sign-in and permissions

### 6.1 Sign-in methods

| Method | Notes | Phase |
|---|---|---|
| Local account | Password. The first start creates an administrator, people are added by invitation link, and registration is closed by default | 1 |
| OIDC | authcore `oidc`, keeping Rackbook's configuration shape | 1 |
| passkey | authcore `passkey`, can be bound to both local and external accounts | 2 |
| SAML | authcore `saml` | 2 |

The administrator can configure which methods are enabled. One account can have
several at once (email plus password, Google, GitHub, Microsoft, ...), and bind
and unbind them at any time. Besides the preset templates, the site
administrator can also add any OIDC / OAuth 2.0 provider (decided by the owner
on 2026-11-23, done in 3.6, see the 3.6 contract in
[phase-3-autonomy-and-providers.md](milestones/phase-3-autonomy-and-providers.md)).

### 6.2 Roles

- **Instance level**: administrator, member.
- **Space level**: owner, editor, read-only.
- **Private space**: one is created automatically for each user, and only that
  user is a member.
- **The administrator can see everything** (the owner's decision of
  2026-11-22, replacing the earlier "the administrator cannot see other
  people's private spaces in the UI and API"): the administrator can view and
  manage all spaces on the server and the content in them. The reason is that
  this could never keep out someone with access to the server's disk, so making
  it invisible was only an illusion. Real secrecy from the administrator relies
  on the encrypted space below. Until 1.7 is implemented, the API still has the
  old behavior.
- **Reserved: encrypted space.** Content is encrypted and decrypted in the
  browser. The server and the repository hold only ciphertext, so the
  administrator physically cannot see it. The costs must be stated: server-side
  search, history comparison, server-side rendering and password recovery
  cannot be done, or need another approach. Not done now, but two things are
  held to: permission checks do not assume the server can read the body, and
  the page read and write API can later be swapped for a form where "the body
  is ciphertext".
- **Reserved: custom roles.** Besides the built-in owner, editor and read-only,
  roles can later be defined by the admin, each made of a set of permissions
  (view, edit, delete, manage members, manage sync and import, and so on). So
  permission checks are written from the start as "does this person have
  permission X on this space". A role is only a set of permissions, and role
  names are not compared all over the code.
- **Shared space**: members can be users or groups.
- **Groups**: from the group claims of OIDC/SAML (with a mapping), or created
  manually in the app.

### 6.3 Machine access

- API token: limited to a space, either read-only or read-write.
- MCP is at `/mcp`, reusing Rackbook's implementation, and both ways of
  connecting are supported:
  1. **Direct connection**: this service has its own OAuth authorization
     service, and any MCP-capable client can connect by entering the address.
     An API token can also be used.
  2. **Through MCPHub**: tokens issued by MCPHub are accepted, and each person
     calls with their own identity. An MCPHub user is bound to an account in
     this app as one kind of external identity.
- MCP permissions are exactly the same as the web app's: it can only see
  spaces the user has access to, and read-only members cannot write.
- Tools: list spaces and folders, search, read a page, create, modify, move,
  tag, view history. Search and listing use the same index. Writes also follow
  the draft and commit rules. The commit author is the person the token
  belongs to, and the commit message notes that it came from MCP.

## 16. Reserved: custom roles and permissions

Not done for now (the owner asked on 2026-11-23 to reserve it). In the future
the admin can **create roles themselves** ("identities"), tick a set of
permissions for each, such as "read some kind of content", "view usage and
audit", "change mail settings", "delete people", and then assign the role to
people or groups. The built-in roles (site administrator, tenant administrator,
and a space's owner / editor / read-only) would then be just a few preset
roles.

Also restating a rule that is already decided: **the site administrator can
manage and operate all settings in all tenants** (the tenant detail page of
3.6, `setBy`, `siteLock`). For content, as in 3.5: viewing and exporting must go
through a "view" that is recorded in the audit log, and personal spaces are
never visible to the tenant administrator.

The conventions to hold to now, for this purpose:

- **Check by permission, not by role name.** The code asks "does this person
  have permission X in this scope", not "is he an administrator". The space
  level is already like this (`perm.Set.Has`). At the admin level, from part 1
  of 3.6, every admin endpoint registers the permission key it needs in the
  route table (the same key names as the admin item list and the capabilities
  of 3.6, such as `tenant.settings.write`, `users.delete`, `data.view`,
  `audit.read`), and the check goes through one function. The built-in site
  administrator and tenant administrator are "all keys in that scope".
- **Permission keys are a registry.** When a new feature adds an endpoint, it
  registers its key, scope (site / tenant / space) and description at the same
  time, and a test checks that every admin route is registered. Future custom
  roles only pick from the registry, and the UI is generated from it too.
- **Roles and grants are stored separately.** The current `role` string (space
  members) and the administrator flag do not change. Later, add `roles` (id,
  scope, name, set of permission keys) and a table for "grant a role to a person
  or group, in which scope", with the built-in roles as rows that cannot be
  deleted. The space level's `perm.Set` is 4 bits now, and will then be widened
  or changed to a set of keys.
- **Guardrails.** A custom role cannot grant permissions the granter does not
  have themselves. Roles created in a tenant cannot touch site-level keys.
  Changes that would lock oneself out (removing the last person who can manage
  roles) are rejected. Every role change and grant is audited.
- The isolation table (3.3) and the "capabilities" of 3.6 stay in front as
  before: first ask whether the tenant has the capability, then ask whether
  this person has the permission.
