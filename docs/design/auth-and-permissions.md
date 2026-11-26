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

An account's email address is its UPN, unique on the server. Behind the layered
setting `accounts.multipleEmails` (off by default) an account may have several:
each is unique on the server and held by the same rule (an address another
account holds verified cannot be added; one held only unverified goes to
whoever proves it first). Any address the account holds verified signs in and
resets, and (once the organisation part of 3.6 lands) will meet an
organisation's domain requirement, which calls `HasVerifiedEmailIn`; the
primary one is the UPN and gets the notices. A new address counts only once its link is followed,
only a verified address can become primary, and the primary cannot be removed.
Turned off, the extra addresses are kept but unused. See docs/api.md, "More
than one email address".

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

### 16.1 Permission keys (3.6 part 1, built)

What the conventions above became in code:

- **`internal/authz`** (server only; the engine never imports it, see
  `internal/boundary`). `Register(name, scope, capability, description)` at init
  returns a `Key`; a duplicate name, or one that does not begin with its scope,
  panics. The bit of a key exists only in the process: anything stored or sent
  names the key. A `Set` is `[2]uint64` (up to 128 keys), so a check is one AND.
- **Subject.** `Subject{Site Set; Tenant map[tenant]Set}`; a tenant key in
  `Site` is held in every tenant. The session cache builds it when it loads a
  person (from `role` and `site_admin` today; from 3.7 the person's memberships
  of organisations fill `Tenant`) and keeps it with the user, so
  `Principal.Access` costs no query and no allocation on a request.
- **Built-in roles, in code**: site administrator = every site and tenant key, in
  every tenant; tenant administrator = every tenant key in their tenant; member =
  `tenant.spaces.create`; viewer = none. Custom roles (stored) are a later part.
- **The check** is `allow(c, key, tenant)`, the tenant always explicit (the
  route table checks the caller's own): (1) the tenant's capability, which a
  holder of the key across the site is not bound by, `403 capability_off`;
  (2) the key, `403 forbidden` naming it; (3) a hook that allows everything
  until 3.7 checks the caller's standing in that organisation.
- **Routes** say `needs(key)` in place of the old `admin` and `siteAdmin`
  levels; space routes keep `inSpace(perm.X)`, and `perm.Set` stays the space
  level's bitset, its four permissions registered as `space.read`,
  `space.write`, `space.members.manage` and `space.manage` (the API keeps the
  short names). A golden file pins every route's decision for a site
  administrator, a tenant administrator, a member and an API token, written
  before the change; the keys decide the same except where listed below.
- **Admin keys never grant content.** No site or tenant key reads or writes a
  space; content goes only through space keys, held as a member of the space or,
  for reading, in an administrator's audited view.
- **The tenant administrator's bypass is closed** (the owner's decision,
  2026-11-25). An administrator who was not a member of a space had `members`
  and `manage` there (`spaces.AdminPerms`), enough to add themselves, make
  anyone an owner, or point the space's remote at their own repository: taking
  the space over unnoticed. Now `tenant.spaces.manage` covers a space's
  settings and members except its owners and its remote. Making, unmaking,
  demoting or removing an owner (a person or a group), giving oneself or a group
  one is in a role, and changing the remote (`PUT`/`DELETE …/remote`,
  `…/remote/key`, `…/remote/accept`, `…/remote/assets`, `…/remote/assets/move`)
  answer `403 takeover_required` to an administrator who is not a member. The
  one way is the new key `tenant.spaces.takeover` through
  `POST /api/admin/spaces/{space}/takeover` with a reason: the administrator
  becomes the owner and the owners editors, it is audited
  (`space_taken_over`), and each replaced owner gets a `space_taken_over` notification, written with the audit row in one transaction.
  `…/transfer` needs the same key and is audited and told the same way. The
  built-in tenant (and site) administrator holds `takeover`, but only these two
  endpoints use it, and a transfer to oneself is refused (it would be a takeover
  without a reason). A site administrator may take over a space of any tenant
  (the owner's decision, 2026-11-25), which makes the one membership that joins
  two tenants (`TransferOwnership` accepts it only for a site administrator); a
  tenant administrator only within their tenant; a personal space nobody.
  Groups that held the owner role become editors (part 2, below).
- Whether someone reaches a space they are not a member of is decided by
  `tenant.spaces.manage` in the space's tenant, not by the role name.
- Token and OAuth principals carry no `Access`: no route outside spaces takes
  them, so nothing is built for them per request.
- Left for later parts then: the in-handler checks and the group path, done in
  part 2 (16.2).

### 16.2 One authorisation path, custom-role tables (3.6 part 2, built)

- **Every check asks a key.** What a handler decides beyond its route's key
  asks the caller's subject: `c.can(key)` (held in their own tenant, a site
  key across the site) or `c.siteWide(key)` (a tenant key held in every
  tenant) in `internal/api`, `Subject.Can`/`SiteWide` in `internal/auth` and
  `internal/spaces`. Three site keys were added for what only a site
  administrator did and no route names: `site.admins.manage` (change, delete,
  make or end a site administrator, reset their password or passkeys, end
  their sessions, disconnect their accounts, revoke their tokens);
  `site.spaces.personal.meta` (personal spaces in the bin and on a person's
  page, restored with their person, managed without being a member); and
  `site.spaces.personal.content` (the audited view and the export of a
  personal space). The last is **built-in only** (`authz.BuiltInOnly`): no
  custom role may list it (`InsertRole` refuses it, the subject builder drops
  it), so a person's content stays the built-in site administrator's alone. The other moved checks use existing keys: the disk and
  restore counts and evict/pin are `site.storage.manage`; a tenant's slug,
  state and quota and a quota above the tenant's are `site.tenants.manage`;
  an early purge `site.recycle.purge`; a history export, a site-only export or
  job, the site's audit rows, a site-set setting, reading a person's sessions
  and invites into another tenant are the route's tenant key held across the
  site. The built-in roles decide exactly as before.
- **Rules about the account acted on** ask `store.User`: `IsAdmin()` (an
  administrator is never merged by address and may sign in with a password
  under ssoOnly), `IsSiteAdmin()` (the last enabled one is kept; only who
  holds `site.admins.manage` changes one), and `store.IsAdminRole` for an
  invitation's role.
- **A source scan** (`internal/boundary`, go/ast, about 0.1 s) fails on a
  role compared with `"admin"` or `.SiteAdmin` used as a condition outside
  `internal/authz` and `internal/store`; copying the field is allowed. A
  vitest does the same for the web app outside `src/lib/access.ts`, whose
  `can(key)` reads `user.permissions`.
- **Custom roles are stored, with no endpoint yet.** `roles` (id, tenant_id
  `''` for the site's, scope, name, keys as a JSON list of key names, created,
  updated) and `role_grants` (role_id, subject_kind `user`|`group`,
  subject_id, tenant_id, space_id; indexed on the subject). The session cache
  builds a subject from the built-in role and, only while
  `Store.CustomRoles()` says some role exists (one atomic load, read at start,
  set when a role is inserted), from the person's grants and their groups'
  (one query, two index reads). Guardrails in the store: `InsertRole` and
  `UpdateRole` refuse a key that does not exist, one not of the role's scope
  (a site role holds site and tenant keys, a tenant role tenant keys only, a
  space role space keys) and a built-in-only key, and store keys by their
  current names; a site role belongs to no tenant and a tenant role to one;
  `InsertRoleGrant` refuses a tenant's role held in another tenant, and
  `HeldRoles` never reads such a row. The builder holds the same lines again:
  a tenant role adds only tenant keys, in the tenant it is held in; a site
  role held in a tenant only its tenant keys there; space grants are the
  space level's and add nothing outside spaces. A renamed key keeps working in stored roles through
  `authz.Alias`; a name that is no key grants nothing and is logged. A
  tenant's roles and grants go with the tenant, a group's grants with the
  group. `auth.Service.RolesChanged` drops the cached people when roles or
  grants change; a change of a person's groups drops that person
  (`spaces.Manager.OnMembershipChanged`, only while some role exists).
- **The group path is closed.** An administrator adding themselves to a group
  that gives them more in some space than they hold (directly or through
  another group) is `403 takeover_required`; with a `reason` it needs
  `tenant.spaces.takeover` and is audited (`group_self_added`, with the
  spaces). A group that holds a custom role outside spaces counts the same
  (its keys would be theirs): the choice was to count it as a gain rather
  than refuse site roles to groups, so groups stay a way to give roles, and
  the audit row names the roles. Adding other people is unchanged. The check
  is in `spaces.AddGroupPeople`, inside its transaction.
- **Owner groups on a takeover or transfer** become editors in the same
  transaction (`store.DemoteOwnerGroups`), their people's lists are dropped at
  once, and the audit row's `ownerGroups` names them.
- **Known, for the endpoint work.** A site administrator who maps an identity
  provider's group to a local group and then signs in through it joins that
  group without the takeover check: this was so before part 2, and the sync
  is audited (`groups_synced`). `Store.CustomRoles` is per process: a role
  written by another process (a second server on the same database) is not
  seen until that process restarts. Neither matters while no endpoint writes
  roles; the role endpoints must settle both (a check on the mapped sync, and
  a flag read from the database or told across processes).
