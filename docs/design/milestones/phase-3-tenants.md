# Milestones: phase 3, tenants (3.3)

**The 3.3 contract** (2026-11-23): the endpoints and error codes are written in
`docs/api.md`, in the new "Tenants, registration and mail" section, plus
additions in "Signing in" (`register` and `forgot` in `GET /api/auth/providers`,
and the `forgot` and `register` contexts of the captcha), "Session" (`siteAdmin`
and `tenant` on User), "Spaces" (the space count now answers `quota_exceeded`)
and "Administration" (the tenant administrator's scope,
`sso_only_with_registration`).

**Where the scope differs from the original plan**: the direction above said
3.3 would not do the registration flow itself (email verification, password
recovery, abuse prevention, terms). The owner's requirements when the contract
was written on 2026-11-23 brought the basics of these into 3.3: email
verification, password recovery, terms links, per-address limits and a mail
sending cap are all done. They all send email, so they are done together with
the mail settings. Quotas are still "fields and measurement first, only warn,
do not refuse". Only the space count, which already refuses, keeps refusing.

What was decided:

- **With a single tenant nothing changes and nothing costs more**:
  - After the upgrade there is one default tenant (fixed id `0000000000`, slug
    `default`), and all users, spaces, groups and invites from before the
    upgrade are in it. All administrators from before the upgrade become site
    administrators.
  - The permission check of every request is unchanged. The per-person cache of
    1.7 / 3.2 only holds spaces of this tenant (membership is guaranteed to be
    in the same tenant at write time, see below), so the hot path adds no
    tenant comparison. Whether a tenant is disabled is a small in-memory table,
    checked when the session is read.
  - Pages are not in the state database, so the number of pages has nothing to
    do with tenants: a space with tens of thousands of pages touches not one
    page row during the migration.
- **Usernames are still unique across the whole site**: signing in (password,
  passkey, SSO) does not need to pick a tenant first. The account that signs in
  decides the tenant, so the sign-in code and endpoints do not change. Public
  endpoints (registration, password recovery, `GET /api/auth/providers`) use a
  `tenant` parameter (slug) to pick the tenant, and without it the default
  tenant is used. Telling tenants apart by host name is only reserved.
- **How far 3.3 opens up**: the whole plumbing is done, plus create, delete and
  edit of tenants for the site administrator. "Whether a second tenant can be
  created" is a switch (`/api/admin/tenancy`), off by default. While it is off,
  there is only the default tenant, and no tenant entry point is visible in the
  UI. Moving people or spaces between tenants is not done (see "Not doing").
- **How data isolation is done**:
  - `users`, `spaces`, `groups`, `invites` and `audit_log` carry `tenant_id`.
    The rest (members, drafts, remotes, favourites, tokens, grants, passkeys,
    identities) hang under them and are reached through them.
  - In the storage layer, every method that reads or lists these five tables
    based on something the client gave must take a "scope" parameter (one
    tenant, or all of them for the site administrator). Without it, it does not
    compile. Methods that really must cross tenants (by session id, token,
    username, sign-in by identity, background jobs) state the reason next to the
    code and go into an allow table.
  - The link between two rows is guarded by the write statement itself: adding
    a member, adding a group to a space, adding a person to a group and
    transferring a space are all `INSERT … SELECT`, joining both sides on the
    same `tenant_id`. Even if the API layer misses a check, no cross-tenant
    link can be created, and the answer is the same as when the id does not
    exist.
  - An id from another tenant is always treated as nonexistent (`404`), and
    nothing is revealed about what other tenants have.
  - The strength of isolation is stated plainly: same process, same database,
    separated by query conditions. Anyone who can read the server's disk or
    database can read all tenants. No stronger isolation is promised.
  - **How to test** (run on both SQLite and PostgreSQL): a two-tenant fixture.
    For every route in `routes.go`, A's administrator, member and token call it
    with B's id, and each must behave as if the id did not exist, and no id of
    B may appear in any list. If a new route is added without being in this
    table, the test fails. Another test lists the exported storage-layer
    methods that touch these five tables, and fails for any that has no scope
    and is not in the allow table. At the end of each test using the fixture, a
    query asserts there are no cross-tenant members, group members or space
    groups. The original single-tenant tests run unchanged.
- **Two kinds of administrator**: `role` is still `admin` / `member`, and now
  means "within their own tenant". `siteAdmin` is added. A tenant administrator
  has everything the administrator had before 3.3, within their tenant, but not
  the server's own things: security, sign-in providers, mail sending, machine
  access, the site-wide settings of daily text, server status, site-wide
  settings, creating, deleting and disabling tenants, and the storage of 3.1.
  The "at least one enabled administrator" rule of 1.7 and 2.8 now counts site
  administrators. Only a site administrator can set `siteAdmin`.
- **Sign-in providers are still at the site level**: the sign-in page of every
  tenant lists the same providers. A provider gains a `tenant`, the tenant that
  accounts created by `autoCreate` are put into. A group mapping can point to a
  group of any tenant, and at sign-in sync only those pointing to the person's
  own tenant are applied. Group names become unique within a tenant.
- **Layering of settings**: following "Layered settings and locking", made into
  an interface. Each setting has a key, a kind (boolean, option, cap) and a
  layer it lives in. Each layer either sets a value or follows the layer above
  (`null`). Each layer sets `allow` / `restrict` (a subset of the options, or a
  cap `max`) / `lock` for the layers below, and a lower layer can only be
  stricter. A value that is locked or goes beyond a limit is kept and marked
  `inEffect: false`, and is restored when unlocked. Effective values are cached
  by (tenant, settings revision, version of the space settings file), and are
  not computed layer by layer at request time. The settings in 3.3: the
  registration switch, the email domains registration allows, registrations per
  hour, the terms and privacy links, per-person quotas for number of spaces /
  disk / pages, the `own` / `api` switch of daily text, and the **tenant's
  daily text default** (what 2.3 called "inserted between space and site": the
  tenant can pick this tenant's space or the site's external API, while pasted
  text stays at the site level only). The `allow` of `/api/admin/daily` and the
  new endpoint write the same record.
- **Hooks left for 3.1 and 2.7** (they are on other branches, and 3.3 does not
  depend on their code):
  - 3.1's `storage.remotes.require` (strictness ordered `null` < `any` <
    `github`), `graceDays` and `localSpaces.maxBytes` are added to the settings
    table when they merge in. The layers are site and tenant, the site default
    is `lock` for lower layers (decided by 3.1 itself), and a tenant can only
    tighten.
  - The byte count for quotas reads `spaces.usage_bytes`: in 3.3 it is written
    by the background measurement of 1.7. After 3.1 merges in, partial clones
    and cleaned-out spaces do not change it (what is left on the server is not
    how big the space is), and only the measurement of a full working copy
    writes it.
  - 2.7's GitHub App settings have no tenant layer.
  - The migration number is `0018`: 0013 and 0015 are left for 2.7 and 3.1, and
    0017 is already used by 3.1's `0017_assets.sql` (changed on 2026-11-23; it
    was originally written as 0017).
- **Registration**:
  - One switch per tenant. The site default is `false` and `lock` for tenants.
    To turn it on, the site administrator first releases it, then it is turned
    on at the tenant (or turned on directly at the site).
  - Preconditions for opening registration: mail sending is enabled and there
    is a `--public-url`. It cannot be turned on while `ssoOnly` is on
    (`sso_only_with_registration`). The reverse check that 2.8 left behind also
    becomes real: `ssoOnly` cannot be turned on while any tenant has
    registration open. When mail sending is turned off, registration is
    effectively closed, and the setting is not touched.
  - Flow: after submission, only a "pending registration" is stored (the
    password is already hashed, and the username is held first), and one email
    with a link is sent, valid for 24 hours. The account is created only when
    the link is clicked (`member`, email verified, with a personal space, and
    the time and address of the agreement to the terms recorded). An address
    nobody can receive mail at can never become an account.
  - The answer is always the same `202`: if the address already has an
    account, no registration is created, and instead an email "you already
    have an account" with a password recovery link is sent. If the mail sending
    cap is exceeded, nothing is sent and nothing is said. A taken username is
    said outright (`409 exists`; usernames are visible to people in the same
    space anyway).
  - When registration is turned off, registrations not yet confirmed are
    voided.
  - Creating an account through a provider's `autoCreate` is not registration,
    and the rules do not change.
- **Email verification**: a signed-in person asks for a verification link for
  their own current email. The link also works on another device (no session
  needed), and old links are void after the email is changed. When the email is
  changed and the old email was verified, a reminder without a link is sent to
  the old email.
- **Password recovery**: by username or email, and the answer is always the
  same. Mail is only sent to enabled accounts with a verified email. An account
  with a password receives a link valid for 1 hour and usable once, and asking
  again voids the earlier link. An account with no password (SSO only), or one
  whose password sign-in is refused by `ssoOnly`, only receives "please sign in
  with such-and-such". **A reset never gives an account a password it could not
  use before** (otherwise someone disabled in the IdP could get back in through
  their email). After a reset, all of the person's sessions end, and the
  per-username failure count and lockout are cleared. It does not sign in
  directly, so that `ssoOnly` and the requirement of a passkey still apply as
  usual at sign-in. An administrator can also send someone a reset link.
- **Mail sending**: one SMTP is configured, at the site level only (host, port,
  `starttls` / `tls`, with `none` allowed only for a relay on the local
  loopback address; the certificate is always verified, and there is no option
  to skip it; username and password are used only over TLS with `PLAIN`; the
  password is stored encrypted in the database with `secret.key`). It uses Go's
  standard library `net/smtp`, with no new dependency. Mail goes in a queue
  **in memory** (at most 1000 messages). Each is tried 3 times, and if it cannot
  be sent within 10 minutes it is dropped and logged. A restart loses mail not
  yet sent, but each one is a link the person can ask for again, and this also
  keeps tokens from landing in the database in readable form. Mail uses the
  template for the request's language (`lang`, `Accept-Language`), and the
  templates are in the i18n files.
- **Quotas**:
  - Whose they count: a space counts against its first owner (the one in the
    admin console list, the default author of commits) and its tenant, and
    follows along on transfer. Group members, editors and read-only members do
    not count.
  - How to measure cheaply: the space count is added and subtracted on create,
    delete and transfer. Bytes and page count reuse 1.7's background
    measurement (one space at a time, at most once every 6 hours), which also
    does one `COUNT` on the space's index to get the page count. Each
    measurement adds the difference, in the same transaction, to the usage row
    of the owner and of the tenant, so reading anyone's usage is a single row.
    Once a day a grouped query recomputes everything and corrects drift.
  - What is refused: only the space count (`--max-spaces` becomes the setting
    `quota.spacesPerPerson`, and when the flag is given the flag wins; the
    error code changes from `403 forbidden` to `403 quota_exceeded`, and an old
    client still sees a 403). Bytes, pages and tenant headcount only warn
    (shown to the person and the administrator, with one audit entry). When
    refusal is wanted later, bytes will use `507 quota_exceeded`.
- **Basics of abuse prevention**: all new public endpoints fall under 2.8's
  "per IP per minute" allowance. A wrong token counts as a failure for the IP.
  The `register` and `forgot` contexts of the captcha become real (off by
  default). At most 3 emails per hour to the same receiving address, at most 5
  triggered per hour from the same source address, registrations per tenant per
  hour (default 50), at most 1000 pending registrations per tenant, and at most
  1000 queued emails across the whole server. The email domains for
  registration can be restricted. Tokens are 32 random bytes, placed only after
  the `#` of the link (so they do not go into logs or `Referer`), only the
  SHA-256 is stored in the database, and a token can be used once. Counts are in
  memory, as in 2.8.
- **Terms and privacy**: the site and each tenant can each set two `https`
  addresses, terms and privacy. If either is set, the registration page has a
  checkbox that must be ticked, and the account records the time of agreement
  and the address at that time.
- **Migration** (`0018_tenants.sql`, tested on both SQLite and PostgreSQL):
  - Create `tenants` and insert the default tenant. Add
    `ADD COLUMN tenant_id … DEFAULT '0000000000'` to the five tables. Adding a
    column with a constant default only changes the table definition on SQLite
    and only metadata from PostgreSQL 11, and neither rewrites existing rows.
  - `UPDATE users SET site_admin = 1 WHERE role = 'admin'` touches only the few
    administrator rows.
  - New indexes are on users, spaces, groups and invites (a few thousand rows,
    milliseconds), and on the audit log, at most 100,000 rows (about a hundred
    milliseconds). Making the group name unique per tenant: on PostgreSQL, drop
    the constraint and create a new index. On SQLite the `groups` table (at most
    10,000 rows) must be rebuilt, and a rebuild needs foreign keys turned off
    first, which cannot be done inside a transaction. So `migrate.go` gains a
    kind of step that "runs separately, before this version's transaction", and
    afterwards the result is checked with `PRAGMA foreign_key_check`.
  - Altogether: for a server with tens of thousands of pages and many users,
    the migration takes under a second, and the downtime is the restart of the
    upgrade itself. An old-version program that sees the new structure still
    refuses to start (existing behavior).
- **Single-user mode**: no tenants, registration, mail sending or quotas, and
  these endpoints all answer `404`. Settings go site → space → personal, with
  no tenant layer.
- **Outward actions** (sent outside the server, received by real people):
  - Sending real email to real addresses. **By default nothing is sent**: mail
    sending is off by default. Until the administrator configures and enables
    SMTP, registration, verification and password recovery are all off, and no
    setting can turn registration on. After it is enabled, mail is sent only for
    the person's own requests (asking for a link) and the administrator's test.
    3.3 sends no notifications, reminders or bulk mail.
  - The administrator's test email goes to the address the administrator
    enters, and it can be tested even when mail sending is not enabled.
  - Turning on registration means letting the server accept public account
    creation, so it is off by default and locked at the site level, and the site
    administrator must release it deliberately.
  - The terms and privacy links appear on the public registration page.

Not doing (the reason is in parentheses):

- **Telling tenants apart by host name**, and a separate sign-in page address
  for each tenant (it would touch the cookie domain, `--public-url`, the
  passkey RP ID and the SAML address; use the slug parameter for now).
- **Tenants configuring their own sign-in providers and their own security
  settings** (one issuer can have only one provider, and an identity is unique
  across the site by issuer plus subject; separating them would need a redesign
  of the identity key).
- **Moving people or spaces between tenants** (members, groups, tokens and
  drafts would all need checking and moving; no demand yet).
- **A database per tenant, or encryption** (see the strength of isolation
  above; nothing stronger is promised).
- **A separate sender for each tenant**, bounce handling, a persistent mail
  queue, a mail statistics page.
- **Refusal for byte, page and headcount quotas** (decide the rule after
  seeing the measured usage).
- **Requiring existing users to agree again after the terms are updated.**
- **Waiting for administrator approval after registration**, invite-code
  registration, a blocklist of disposable email addresses.
- **TOTP and other second factors** (same as 2.8).
- **Counts and caches shared across processes** (same as 2.8 and 3.2; the
  service is still a single instance).

For the owner to decide (do the default in parentheses for now):

1. Usernames are unique site-wide, and sign-in does not pick a tenant (the
   only way; otherwise the sign-in page, passkeys and SSO would all need to
   know the tenant first).
2. Whether 3.3 only does the plumbing for the default tenant, or also opens
   up creating more tenants (the plumbing plus create, delete and edit by the
   site administrator, behind a switch that is off by default).
3. Public endpoints use `?tenant=slug` to pick the tenant, and host name is only
   reserved (yes).
4. In 3.3 sign-in providers are still only at the site level, and the tenant
   administrator cannot configure them (yes).
5. Registration must verify the email, so registration cannot be turned on
   without mail sending configured (yes).
6. An SSO-only account cannot use password recovery to add a password (cannot;
   it only receives "please sign in with such-and-such").
7. The mail queue is in memory and lost on restart (yes; all are links the
   person can ask for again).
8. SMTP does not offer skipping certificate verification (not offered; a
   self-signed relay is put on the local loopback address and uses `none`).
9. Quotas only refuse the space count, and bytes, pages and headcount only warn
   (yes).
10. A space's usage counts against the first owner (yes; the same person as the
    default commit author).
11. When registration is turned off, unconfirmed registrations are voided
    (voided).
12. When the email is changed, a reminder is sent to the old verified email
    (send it, without a link).
13. An administrator can send someone a reset link (can).
14. After the terms are updated, existing users are not required to agree again
    (not required; only the time and address of agreement are recorded).
15. Registrations per tenant per hour default to 50, and 3 emails per hour to
    the same address (use these).
16. A tenant administrator can see the audit log of their own tenant, but not
    the site-wide kind of entries (can).
17. The site default of the registration switch is "off, and locked for
    tenants" (yes; the site administrator must release it first).

**3.3 as built (part 1, 2026-11-23)**: the tenant model, layered settings and
data isolation from the contract above are done. Mail, registration, email
verification, password recovery and quotas are left for the parts below (the
seams are all left in place, see the last item). For the endpoints see
"Tenants, registration and mail" in `docs/api.md` (what this part does and does
not do is written under "Storage"). The migration is `0018_tenants.sql` (tested
on both SQLite and PostgreSQL). The owner's questions were done with the
defaults in parentheses. What was decided during implementation:

- **Migration**:
  - 0017 is already 3.1's `0017_assets.sql`, so 0018 is used, and every place
    in the docs that said 0017 was changed.
  - One migration file for the whole milestone. This part only holds the
    tenants table, `tenant_id` on the five tables, `users.site_admin`,
    `invites.site_admin` (administrator invites from before the upgrade are
    also set to 1), `users.email_verified_by`, `sign_in_providers.tenant_id` and
    `layered_settings`. The tables and columns needed by the later parts are
    added to the same file before merging.
  - On SQLite, making group names "unique within a tenant" needs a rebuild of
    `groups`, and a rebuild needs foreign keys off first. `PRAGMA foreign_keys`
    has no effect inside a transaction, so for any version in `migrate.go` that
    needs this (`foreignKeysOff`), SQLite runs the whole migration on **one
    fixed connection**: foreign keys are turned off before the transaction, a Go
    step rebuilds the table, and before the transaction ends
    `PRAGMA foreign_key_check` runs (if any dangling row exists it reports an
    error and the whole migration rolls back), and foreign keys are turned back
    on at the end whether it succeeded or not. PostgreSQL needs none of this: it
    uses the original single transaction, with `ALTER TABLE` to drop the
    constraint and create the new index.
  - The test runs the migration on a database with the 3.2 structure and data
    in it (`storetest.EachAtVersion`): the default tenant is there, all rows
    are in the default tenant, the two administrators became site
    administrators and the members did not, after the rebuild not one group
    member or group placement in a space is missing, a group with the same name
    can be created again in another tenant, and foreign keys still block. A
    second test puts in one dangling group-member row, and asserts that the
    migration fails, rolls back, and `foreign_keys` is turned back on.
- **Scope (`Scope`)**: the storage layer's `Scope` is "one tenant" or "all", and
  the zero value sees nothing (forgetting to give one means empty). Every
  function that reads, lists or changes by id the five tables takes it.
  `store/scope_test.go` uses `go/parser` to find every exported function whose
  queries touch these five tables but that has no `Scope` parameter, and fails
  if it is not in the allow table with a reason. It also fails for allow-table
  entries that already take a scope or no longer touch these tables, so it
  cannot go stale. The allow table holds: sign-in by username, identity or
  token hash, where "the account decides the tenant"; background jobs; steps
  that continue from a row already found within scope; and writes that carry
  their own tenant.
- **Links are guarded by the statement**: `AddMember`, `AddSpaceGroup`,
  `AddLocalMembers`, `ApplySynced` and `TransferOwnership` are all
  `INSERT … SELECT`, joining both sides on `tenant_id`. Across tenants nothing
  is written, and the answer is the same as when the id does not exist. A group
  mapping in `ApplySynced` (sign-in sync) that points to another tenant
  therefore brings this person nothing.
- **Cost per request**: the permission check and 3.2's per-person cache are
  untouched, with no tenant comparison. After the session is read there is only
  one extra `TenantDisabled`: one atomic read that returns right away when no
  tenant is disabled. Measured (`PETALBOOK_PERF=1 go test ./internal/api -run
  PerRequestCost`, SQLite, one tenant, two runs each, compared with 3.2's
  commit `64684cb`): `GET /api/spaces/{id}/tags` (session plus permission
  check) 75.4 / 76.2 µs against 77.3 / 76.0 µs, 108 allocations against 108,
  memory 11.9 KB against 12.0 KB, within noise; `GET /api/session` 35.7 / 35.1
  µs against 37.0 / 38.1 µs, where the extra 13 allocations and 1 KB come from
  the two extra fields `siteAdmin` and `tenant` in the answer, not from the
  check.
- **Administrators**:
  - `role` now means "within their own tenant", and `siteAdmin` is added. Only a
    site administrator has the routes of `kindSiteAdmin` (security, sign-in
    providers, mail sending, machine access, the site-wide settings of daily
    text, server status, site-wide settings, creating and deleting tenants).
  - Where nothing says "to which tenant": `setup`, the command line
    `user add --admin`, and an administrator invited or promoted in the default
    tenant by a site administrator are all site administrators (administrators
    from before the upgrade were all the server's anyway). In other tenants, or
    when invited by a tenant administrator, they are not. `POST /api/invites`
    therefore gained `tenant` and `siteAdmin`. The contract did not say this; it
    was added during implementation.
  - "At least one usable administrator" counts site administrators, and does not
    count those in disabled tenants (they cannot sign in). Demoting, disabling,
    removing `siteAdmin` and disabling a tenant all check it. A tenant
    administrator touching a site administrator (including deleting their
    passkey) gets `403`.
  - 2.8's merge by verified email only looks within the provider's tenant. An
    email that a tenant administrator set as "verified" still shows that way,
    but it is recorded as `email_verified_by = 'tenant_admin'` and does not
    count as grounds for merging (otherwise he could fill in someone else's
    address for a person in his tenant, and when that person first signs in by
    SSO, merge the identity in). Only confirmation by the person, a guarantee
    from the provider, or one set by a site administrator counts.
  - Audit: each row has a tenant (by the person the row speaks of, then by the
    person who acted). A tenant administrator cannot see the kinds that belong
    to the server itself (including things like `security_changed` written in
    the default tenant before the upgrade, filtered out by kind).
- **Layered settings** (`internal/settings`):
  - The site layer lives in `layered_settings`, and the tenant layer is there
    too. The space layer is `space.yml`, and the personal layer is in its own
    tables, each read by its own code (in this part only the two daily text
    switches have a space layer). Effective values are cached as one table by
    (tenant, generation), and any write adds one to the generation. At request
    time it is one map lookup, not a walk through the layers.
  - The site-wide values of daily text's `daily.own`, `daily.api` and
    `daily.default` **are still kept in the original daily text record**
    (`SiteBinding`), so `/api/admin/daily` and `/api/admin/settings` are the
    same record, and 2.3's behavior is as before: not one existing assertion was
    changed (the tests were only changed in their calls because the storage
    functions gained a scope parameter, and the admin route tests widened
    "administrator" to "administrator or site administrator"). The tenant
    layer's `daily.default` is inserted between space and site. It can only pick
    this tenant's space or the API, and pasted text stays at the site level.
  - A boolean `restrict` can only be "only allow off" (`options: [false]`).
    Restrictions are compared by strictness: a tenant's rule must be at least as
    strict as the site's. A blocked value is kept and marked `inEffect: false`,
    and takes effect again once the rule is loosened. When the site tightens,
    `affected` in the answer is the number of tenants affected.
  - The lock is checked before "can it be turned on": a tenant changing
    `registration.open`, which the site has locked, gets `setting_locked`, and
    only after unlocking does `mail_not_configured` come into play (for now it
    is always that one, see below).
  - `GET /api/settings` gained `source` (`site` or `tenant`): whose lock, or
    whose value, turned it off, so that when the client greys out a control it
    can write "set by whom".
- **Reserved for 3.1 and 2.7**: the registry has none of their keys (their code
  is on other branches). A comment in the definitions file writes down the
  layer, kind and strictness (`null` < `any` < `github`) of 3.1's three keys and
  the site default `lock`, and 2.7 has no tenant layer. They only need to be
  added to `Definitions()` later, with nothing to change elsewhere.
- **Seams left for the later parts**:
  - Mail: `registrationReady()` is always `false` for now, so opening
    `registration.open` is always `409 mail_not_configured`, and a tenant's
    `registration.open` always reads as `false`. The reverse check for `ssoOnly`
    sits in the same place (`beforeOpening`).
  - Not done yet: the routes for registration, password recovery and email
    verification, the `registrations`, `email_tokens` and `usage` tables,
    columns such as `users.quota`, the `quota.*` settings, `register` /
    `forgot` of `GET /api/auth/providers` (for now always `false`, and `?tenant=`
    already validates), the tenant object's `usage`, and the pre-filled invite
    for `admin` in `POST /api/admin/tenants` (for now only validated).
  - An `audit` event can name its tenant directly (`"-"` means the server's
    own).
- **Isolation tests** (run on both SQLite and PostgreSQL): the two-tenant
  fixture of `api/isolation_test.go` has administrators, members, tokens, OAuth
  grants, groups, invites and spaces. `isolationRoutes` states how each
  non-space route is isolated (space routes are all tested automatically by
  `{space}`). `TestEveryRouteIsInTheIsolationTable` fails when a route is added
  to the route table without being registered. `TestTenantsAreIsolatedOnEveryRoute`
  has A's tenant administrator, member and token call every route with B's id,
  requires the answer to be exactly the same as for a nonexistent id (status
  code and error code), and no id or name of B may appear in the answer or in
  lists. At the end it asserts B's things did not change and there are no
  cross-tenant members, group members, space groups or token spaces. When
  `ScopeOf` was temporarily changed to always return all, this test immediately
  failed in a dozen places such as audit, grants and group members, so it really
  does block.
- **UI**: the admin console gained "Tenants" (a switch; the list only appears
  once the switch is on or there are already several tenants, and it can create,
  rename, disable, delete, and get the invite link for the first administrator)
  and "Layered settings" (a site administrator sees the site layer plus the
  tenant layer of one chosen tenant; a tenant administrator sees the layer of
  their own tenant; each item shows the site's lock / limit, values not in
  effect, and a rule editor). A user row says whether the person is a site
  administrator or tenant administrator, and a site administrator can change it.
  A tenant administrator does not see the pages that belong to the server
  itself. The "Tenants" page is always in the navigation for a site
  administrator (the switch is inside it), and while the switch is off the page
  has no list. The contract's "no tenant entry point is visible in the UI while
  it is off" means the tenant list and the word "tenant" in various places; the
  switch itself has to live somewhere. Chinese and English, the mock server,
  contract tests (run against both the mock and the real server) and phone
  width (rows wrap, columns can shrink) are all done.
- **Known issues**: when a site administrator promotes or invites an
  administrator in the default tenant, without `siteAdmin: false` the person
  becomes a site administrator. This is intentional (consistent with the
  pre-upgrade "an administrator is the server's administrator", and written in
  `docs/api.md`), but easy to be surprised by: to get a tenant administrator in
  the default tenant, `siteAdmin: false` must be sent explicitly.
  Two more:
  - When disabling a tenant, the check "refuse if, after disabling, no site
    administrator remains outside the tenant" can in fact never be reached: only
    a site administrator can disable a tenant, and must be outside this tenant,
    so such a person always remains. Also, that check is done outside the
    transaction.
  - When disabling a tenant there is also no check, while `ssoOnly` is on, of
    whether any of the remaining site administrators has a password. Other site
    administrators are still there and can enable the tenant again, so nobody is
    permanently locked out.
- **Not done yet**: see "Seams left for the later parts" above.

**3.3 as built (part 2, 2026-11-23)**: mail sending, registration, email
verification and password recovery are done. Quotas and usage are left for the
next part. For the endpoints see "Tenants, registration and mail" in
`docs/api.md` (the details decided during implementation are written under
"Storage", in "What the second part of 3.3 does"). The migration is still the
same `0018_tenants.sql` (with `registrations`, `email_tokens` and
`users.terms_accepted` added, tested on both SQLite and PostgreSQL). The owner's
questions were done with the defaults in parentheses. Also, at the
coordinator's request, one seam of part 1 was filled in: when a site
administrator sends `emailVerified: true` again for an email that already shows
as "verified" but carries the `tenant_admin` mark, the mark is now cleared too
(the failing test was written first), and the person clicking the verification
link clears it the same way. What was decided during implementation:

- **By default nothing is sent**:
  - Mail sending is off by default. Until `PUT /api/admin/mail` enables it,
    `GET /api/register` answers `open: false`, forgot-password answers
    `403 password_reset_off`, asking for a verification email answers
    `409 mail_not_configured`, and no setting can turn on registration.
  - Tests always connect to a fake SMTP server inside the process
    (`internal/mail/mailtest`, listening only on the loopback address, keeping
    mail and never forwarding it), so no test can reach any other mail server.
    The mock server (the web mock) never sends mail, and "sent" mail is only
    put in its `outbox`.
  - After it is enabled, mail is sent only for the person's own requests
    (asking for a link), the reminder to the old address when the email is
    changed, the administrator's test email, and a reset link the administrator
    clicks to send.
- **`internal/mail`**: uses only the standard library `net/smtp`. `starttls`
  requires the server to offer STARTTLS (if it does not, it fails, and never
  falls back to plain text), and `tls` encrypts from the first byte. Both
  verify the certificate and host name, with no option to skip (the trust root
  used by tests can only be passed in by test code, and is not in the
  settings). `none` is only allowed for loopback addresses, and after
  connecting it also checks that the peer address is loopback (to guard against
  a host name resolving elsewhere). `PLAIN` is only sent over TLS or loopback.
  Line breaks in headers are always removed, and a recipient is only accepted as
  plain `local@domain`. The queue is in memory, at most 1000 messages. A message
  is tried right away, again after 1 minute, and again after another 5 minutes,
  and is dropped when the third try fails or after 10 minutes (the log only
  writes the reason, not the address or body, because the body holds a link).
  Turning off mail sending empties the queue. Error text in the status has
  control characters removed, is truncated, and has the password replaced with
  `***`. The password is sealed with `secret.key` (purpose `mail`), the read
  endpoint only gives `hasPassword`, and the audit only writes `"changed"`.
- **Tokens**: 32 random bytes, placed after the `#` of the link, only the
  SHA-256 stored in the database (for registration, verification and reset
  alike), compared in constant time, and usable only once
  (`UPDATE … WHERE used = 0` is the single point that tells apart two
  simultaneous uses). Asking again voids the earlier link. Changing the email
  voids the old verification link (a link is also only valid for the address it
  was sent to). Any password change (by the person, by command line, by reset)
  voids reset links. All four endpoints that carry a token first go through the
  "per address per minute" allowance. A wrong token counts as one failure for
  that address, and a correct one gives it back.
- **Registration**:
  - Checked in the order of the contract, up to the first refusal. The
    per-tenant hourly count and the pending cap are counted for **every**
    request that passes the field checks, whether or not the address has an
    account, so the answer is the same. The password is hashed in all branches,
    so the amount of work does not leak either.
  - When the address already has a verified enabled account, no registration is
    created, and "you already have an account" with a reset link is sent (when
    that account cannot use a password, "please sign in with such-and-such" is
    sent). 3 emails per hour to the same address and 5 from the same source.
    Beyond that nothing is sent or stored (with no mail link, no registration
    is stored).
  - Terms: if a terms or privacy address is set, ticking is required, and the
    time and address of the agreement are stored in `registrations.terms`, and
    copied into `users.terms_accepted` when the account is created. The account,
    session, personal space and the audit `registered` are all created at once
    at confirmation. Deleting the pending row and creating the account are in
    the same transaction, so the link can only be used once.
  - Closing registration (changing the switch, changing `lower`, turning off
    mail sending, deleting a tenant, the hourly sweep) **deletes** pending
    registrations, so their links answer `404 not_found`.
    `403 registration_closed` is left for the moment when "the row is still
    there but the tenant is no longer open".
  - `sso_only_with_registration` is real in both directions: when opening
    registration it checks whether `ssoOnly` is set, and when
    `PUT /api/admin/security` turns `ssoOnly` on it checks whether any tenant's
    effective value is open (walking the tenant table, in memory). When the site
    unlocks (`lower` is no longer `lock`) while some tenant has stored `true`,
    mail and `ssoOnly` are checked first in the same way.
- **Password recovery**: the answer is always the same `202`, whether or not the
  account exists, has a verified email, or is disabled, and the tests compare
  the bytes of the response one by one. Only an account that has a password and
  is not refused by `ssoOnly` gets a link. The others get "please sign in with
  such-and-such". **A reset does not give an account a password it could not use
  before**, and it checks again at reset time (if `ssoOnly` was turned on after
  the link was sent, the link is void). After a reset, all of the person's
  sessions end, and the failure counts and lockouts by username and by (address,
  username) are cleared (`failTable` gained `resetWhere`). It does not sign in
  directly. A reset link sent by an administrator stays within the
  administrator's own scope, a tenant administrator cannot touch a site
  administrator, and it is not counted in the "5 from a source" cap.
- **Seams**: `registrationReady()` is now "mail sending is enabled and there is
  a `--public-url`". `register` of `GET /api/auth/providers` is that tenant's
  effective rule, and `forgot` is whether mail sending is enabled. The `forgot`
  and `register` contexts of the captcha entered `CaptchaContexts`, off by
  default.
- **UI**:
  - The registration page (`/register?tenant=`), the registration confirmation
    page (`/register/confirm#token`, which creates the account, signs in and
    enters the app), the email confirmation page (`/verify-email#token`), the
    forgot-password page (`/forgot`), and the reset page
    (`/reset-password#token`, which checks the link first and then lets the
    person enter a new password).
  - The token is read from the address fragment once and then removed from the
    address bar. The two links on the sign-in page only appear when `register`
    and `forgot` are true.
  - The email row on the account page (verified / not verified, with "send
    verification email" when the server can send mail). On the users page, a
    person with a verified email has "send reset link". The admin console gained
    a "Mail" page (SMTP settings, status and queue, test email), visible only to
    site administrators.
  - Chinese and English are both done, along with the mock server, contract
    tests (run against the mock server, and against the real server only
    asserting the answers "when mail sending is off") and phone width (forms are
    one column, buttons wrap).
- **Tests**: the Go tests run on both SQLite and PostgreSQL: `internal/mail`
  (fake SMTP: certificate verification, STARTTLS required, `none` loopback only,
  header injection, retry and drop, queue cap), every flow and every error code
  in `internal/api`; the web uses Vitest. Only the relevant packages were run,
  not `make check-full`.
- **Known issues**:
  - A timing difference in password recovery: the answers are exactly the same
    (the tests compare byte by byte), but when the account exists there is one
    more write (storing a reset token row) and one more lookup
    (`email_verified_by`), so in theory the response time could reveal whether
    the account exists. It was not made constant-time.
  - The mail status (`status`) is in memory, and after a restart it is "never
    sent".
  - The mail templates are in Go code (`internal/mail/templates.go`), with only
    `en` and `zh-Hans`. The "i18n files" the contract mentions mean this; the
    web i18n files do not handle mail the server sends.
  - At registration confirmation, if the username was taken by an administrator
    in the meantime, the answer is `409 exists`, and the pending row stays
    because the transaction rolls back, until it expires.
- **Fixes from the first review** (each started with a failing test,
  `internal/api/emailsafety_test.go` and others):
  - **Email addresses accept ASCII only**: Go's `strings.ToLower` turns `İ`
    (U+0130) into `i` and the Kelvin sign `K` (U+212A) into `k`, so
    `tom@gİthub.test` could match the account of `tom@github.test`, and "you
    already have an account" and the reset link would be sent to a look-alike
    address. The same folding could bypass `registration.domains`, and take over
    an account through 2.8's merge by email. Now `cleanEmail` and registration
    reject any non-ASCII character (`400 invalid`), comparison always uses
    `store.EmailKey` (which only lowercases A-Z), and the database folds only
    ASCII too (PostgreSQL uses `translate`, SQLite uses the registered
    `petalbook_ascii_lower`, independent of the database locale). Account mail
    is always sent to the `u.Email` stored in the database, never to the address
    the user typed.
  - **Existing non-ASCII addresses** (stored in 2.8 and earlier): not migrated
    and not rewritten, kept as they are. But they match no lookup
    (registration, recovery, merge, domain allowlist), and receive no account
    mail (the recovery link; a reset sent by an administrator answers
    `409 email_unverified`; asking for a verification email answers
    `400 invalid`; the reminder on email change is not sent). Once the person or
    an administrator changes it to an ASCII address, everything works as usual.
    When only the name is changed, the address is untouched. Tests:
    `TestAStoredNonASCIIAddressGetsNoAccountMail`,
    `TestUsersByEmailNeverFoldsBeyondASCII`.
  - **Registration probing**: when the address already has an account, the
    username is also held (a pending registration row that sends no link, also
    24 hours), so "the same username, again with another address" answers
    `409 exists` in both cases. The hourly allowance of the address is checked
    before the username is held, so both paths have the same order.
  - **Changing the email voids reset links**: a change by the person or by an
    administrator deletes all of that person's email tokens. `findReset` also
    requires the token's address to equal the account's current address.
  - **A tenant administrator's mark does not count**: an address with
    `email_verified_by = tenant_admin` is treated as unverified in recovery, in
    administrator-sent resets and in "you already have an account", the same as
    2.8's merge. Otherwise a tenant administrator could set his own email on a
    member, mark it verified, and reset the password.
  - **When the hourly allowance table is full, refuse**, and no longer push out
    the entry unused the longest (pushing out would give a limited address
    allowance again). Only entries unused for an hour make room.
  - `TestARetrySucceeds` was unstable under `-race`: the server received the mail
    earlier than the sender counted it, so it was changed to poll `Stats()`
    within the deadline.
  - **Noted in the second review**: for an address that only a tenant
    administrator marked as verified, the person asking for a verification email
    gets `409 email_verified`, while `docs/api.md` says the person can verify
    with a link. It does not get stuck (a site administrator can verify for them,
    or they can change the address, change it back and verify), and later this
    case will also be able to send a verification email.
- **Not done yet**: quotas and usage, and the pre-filled invite for `admin` in
  `POST /api/admin/tenants`, were finished in part 3.

**3.3 as built (part 3, 2026-11-23)**: quotas and usage are done, and `admin` in
`POST /api/admin/tenants` now really pre-fills the invite. For the endpoints see
"Quotas" and "What the third part of 3.3 does" in `docs/api.md`. The migration is
still the same `0018_tenants.sql` (with the `usage` table, `users.quota`,
`spaces.page_count` / `usage_bytes` / `usage_user`, and `invites.username` /
`name` / `email` added, tested on both SQLite and PostgreSQL). The owner's
questions were done with the defaults in parentheses (items 9 and 10: only the
space count is refused, and usage counts against the first owner). What was
decided during implementation:

- **How usage stays correct without counting anything in a request**:
  - One `usage` row per person and per tenant (space count, bytes, pages,
    headcount, what was exceeded, time of the latest measurement). Reading
    anyone's usage is one lookup by primary key. The space count check when
    creating a space reads this row (and the tenant's row when the tenant has a
    total), independent of how many spaces exist.
  - Changes are carried by the changing statement itself, in the same
    transaction: creating a space (the tenant gets one more), adding / changing
    / removing a member and transferring ownership (the space is re-recorded
    under the new first owner), deleting a space (each of the two rows loses
    what it held), creating a user (the tenant headcount gets one more), and
    each measurement (the difference from last time is added to the owner and
    tenant rows). Every add or subtract is an upsert of "current value plus
    difference", and the two rows are locked in a fixed order (sorted by kind
    and id first), so on PostgreSQL two transactions do not wait for each other.
  - **A column the contract did not mention: `spaces.usage_user`**, which
    records who the space currently counts against. Without it, once ownership
    changes, it would not be known from which row to subtract or how much (a
    measurement may have changed its byte count in the middle). With it,
    `settleOwner` is idempotent: it works out the first owner, and only moves
    when it differs from the recorded one, moving exactly what was added back
    then. The first owner is still the first owner member by
    `ORDER BY created, user_id`, the same person as the default commit author.
  - Reconciliation: a set of grouped queries (one per table) reads out the
    values that should be there and writes only the rows that differ. These
    reads must see one consistent state: on SQLite the transaction holds the only
    write connection, so this holds naturally. On PostgreSQL each statement has
    its own snapshot, so a write committed between two reads would be cancelled
    out by the difference, and a new row would be taken as stale and deleted
    (reproduced with eight concurrent writers). So the reconciliation first runs
    `LOCK TABLE usage IN SHARE ROW EXCLUSIVE MODE` and then reads. Every change
    writes usage in its own transaction, so when the lock is obtained there is
    nothing half-changed, and later changes wait for the reconciliation to
    commit (about 0.2 s, the same as queuing on SQLite). Before this, the
    reconciliation holds no other lock (the step "whose name does a space count
    against" locks space rows, and is done first in a separate transaction), so
    it cannot form a cycle with the writers' order (space, tenant, person,
    usage). **It runs once when the server starts** (a crash may leave
    differences, and the cost is the same as the once-a-day cost), and once a
    day after that (`RecountEvery`). In the migration, the rows are generated
    directly with two set statements (see the cost below). After the
    reconciliation, everyone is looked at once more for whether they are over.
  - Pages: the measurement counts pages along the way. If the space is open, it
    asks its index (one `COUNT`). If it is closed, it reads the index file
    directly, and **does not open the space just to count pages** (opening
    triggers a remote fetch, and also pushes other spaces out of memory). If the
    index file does not exist (the cache was cleared), only the bytes are
    updated, and the page count keeps its old value.
  - Bytes are still the directory walk of 1.7, and the first value of
    `usage_bytes` is `size_bytes` (one `UPDATE` in the migration).
- **Refusals and warnings**:
  - The space count is the only thing refused: the personal limit (the person's
    own, otherwise the tenant's `quota.spacesPerPerson`, otherwise the site's)
    and the tenant's total `quota.spaces`, answering `403 quota_exceeded`, with
    `quota`, `limit` and `used`. If `--max-spaces` is given, it is the site
    value and read-only (`fromFlag`), and if not given it is the stored 20. 0
    means unlimited.
  - **The basis for "counting spaces" changed, following the contract**: it used
    to count "spaces where I am an owner member", and now counts "spaces where I
    am the first owner". So being added as a second owner takes no quota, and
    only after a transfer does it count. The old test (a member promoted to owner
    takes quota) was changed to follow the contract, and an assertion that it
    counts after a transfer was added.
  - Bytes, pages and tenant headcount only warn: the hint in "Usage" on the
    account page, "Over" in the admin console's "Usage", the tenant's
    `usage.over`, and the audit `quota_exceeded`. **A crossing is recorded only
    once**: `usage.over` stores what this row is currently over, and only the
    "newly over" ones write an audit entry, so measuring again or looking again
    does not repeat it. Going down and then over again counts as a new one.
    "Over" means greater than, and equal does not count.
  - After a limit is changed, it is looked at immediately: when a `quota.*`
    setting, a person's `quota` or a tenant's `quota` is changed, the affected
    people (everyone for a site setting) are looked at again synchronously, so
    `over`, the hint and the audit do not wait for the next measurement. While
    looking, usage rows are read in pages of 500, and a row whose state did not
    change is not written.
  - A personal limit only has a "tighten downward" permission rule: a tenant
    administrator cannot set a person's limit higher than the tenant's (unlimited
    is the highest), `403 forbidden`, and a site administrator can. If the upper
    layer has locked or restricted it, a tenant administrator follows the
    stricter of the site and the tenant, and a site administrator follows the
    site (`409 setting_locked` / `setting_restricted`). The contract wrote this
    briefly, and this is how it was filled in. On refusal the whole request
    changes nothing (`quota` is checked before the other fields).
- **Additions to the contract** (all added fields, nothing old changed):
  - Each item of `GET /api/admin/usage` also has `tenant`, `own` (the person's
    own limit, the starting point of the edit form, so the whole user table need
    not be read for it) and `measuredAt`. The cursor is `bytes:id`.
  - A tenant's `usage` also has `measuredAt` and `over`. The user in the admin
    list and in `PATCH /api/users/{id}` carries `quota`.
  - `quota_exceeded` carries `used`. An audit row gains `scope`
    (`person` / `tenant`).
  - Invites gained `username` / `name` / `email`: the `admin` of
    `POST /api/admin/tenants` is stored in the invite, and
    `POST /api/invites/check` answers them back, and the accept page starts from
    them. On accept, if the username or name is left empty the one in the invite
    is used, and the email becomes the account's email (unverified). No email is
    sent.
  - `GET /api/settings` (settings a person can change themselves) does not list
    `quota.*`: a person has no right to change this layer themselves.
- **A hook for 3.1, kept clean**: `store.RecordMeasurement` is the only place
  that writes `usage_bytes` (and it also writes the size in the 1.7 list). After
  3.1 merges in, partial clones and cleaned-out spaces must not call it, and
  only use `SetSpaceSize` to write the size in the list, so `usage_bytes` keeps
  the value of the last full measurement (it does not shrink). Only a
  measurement of a full working copy calls `RecordMeasurement`. This branch has
  no 3.1, so every measurement is full for now.
- **A nil pointer that already existed was fixed along the way**: when the daily
  text settings binding hit an error on read (the context was cancelled), it
  dereferenced a nil `Allow`. The reconciliation goroutine ran into it when a
  test was finishing. Now it returns the error.
- **Cost** (20,000 people, 40,000 spaces, SQLite / PostgreSQL, numbers printed
  by the `PETALBOOK_PERF=1` test):
  - The migration step that generates the usage rows takes 0.28 s / 0.74 s (two
    set statements). A row-by-row upsert reconciliation starting from zero takes
    0.74 s / 1.5 s, so the migration does not use it.
  - The once-a-day reconciliation with no differences: 0.17 s / 0.19 s.
    Correcting 1,000 rows: 0.20 s / 0.26 s. On SQLite it holds the only write
    connection, and other write requests queue for about 0.2 s meanwhile. On
    PostgreSQL it locks the usage table (reads are not affected), and writes
    that change usage likewise queue for about 0.2 s.
  - Reading one person's usage 21 µs / 59 µs. A page of 50 people (sorted by
    bytes) 0.2 ms / 0.5 ms, and the page past ten thousand people 1.3 ms /
    2.0 ms. A page for one tenant 0.2 ms / 0.3 ms.
  - Recording one measurement 0.25 ms / 0.44 ms. Creating a space and adding an
    owner (including usage) 0.33 ms / 0.64 ms. Transferring a space 0.38 ms /
    0.83 ms.
  - A space of 20,000 pages: counting pages takes 0.15 ms when it is open, and
    0.5 ms reading the index file when it is closed. A whole measurement round
    (walking files 0.2 to 0.3 s, existing in 1.7) does not get more expensive
    with page count. Opening it to rebuild the index takes 1.4 s; that is the
    cost of opening, and measurement does not trigger it.
  - Looking at everyone again after a limit change: 20,000 people, nobody changes
    state 21 ms / 60 ms. Half of the people crossing the line (writing state,
    writing audit) 0.3 s / 0.7 s, done synchronously within the request that
    changed the setting.
  - The space count check for one person: reading his usage row takes 21 µs,
    where before it counted over the member table.
- **Tests**: the Go tests run on both SQLite and PostgreSQL. `internal/store`:
  usage changes with create / delete / transfer / member changes /
  measurement, and each scenario ends by asserting "the reconciliation has
  nothing to correct". Drift made on purpose (editing a row, deleting a row,
  extra rows for people who no longer exist, a space recorded under the wrong
  person) is all corrected by the reconciliation, and running it again does
  nothing. A measurement can correct a difference left by a crash. After
  concurrency (6 people creating, measuring, transferring, deleting and
  measuring again at the same time, really in parallel on PostgreSQL) the
  counts are exactly the same as the result of reconciling from scratch. The
  order of the list, cursor, scope and `over`. The migration is correct from an
  old database (first owner, spaces without an owner, headcount).
  `internal/spaces`: refusal of the space count for a person and a tenant, and
  giving the quota back after deletion, concurrent creation cannot exceed the
  limit, a measurement writes bytes and pages and notifies the hook, closed
  spaces and spaces with no index, and the background reconciliation corrects
  itself and notifies the hook. `internal/quota`: the layers of limits, the
  basis of "over", auditing a crossing only once, and people beyond one page
  (500) are also fully looked at. `internal/api`: every rule and every error
  code (account usage, layered space limits, `--max-spaces`, the rules for a
  person's limit and changing nothing on refusal, a warning and audit once, list
  scope and cursor, tenant usage and totals, pre-filled invite). Two routes were
  added to the isolation table. The web uses Vitest: the mock server, contract
  tests (run against both the mock server and the real server), the account
  usage page, the admin console's usage page, the limit form on users and
  tenants, the pre-filled invite page, and phone-width class names. Only the
  relevant packages were run, not `make check-full`.
- **Known issues**:
  - The page count is the page count in the index: if someone edits files
    directly on the disk while the server is stopped, the index only catches up
    the next time the space is opened, and measurements before that count the old
    number. Bytes are measured, and not affected.
  - Disk usage is the directory size (including `.git`, with each hard link
    counted once), the same as 1.7. It is not de-duplicated per "person", and
    does not separate pages from attachments.
  - The headcount total only warns, and invites and registrations can still
    exceed it. A person who is already over has nothing taken back (the contract
    was only to warn in the first place).
  - The personal limit list (admin console "Usage") reads `own`, not the whole
    user table. But the "Users and invites" page always loaded the whole user
    table, and that was not changed.
  - When the site-wide limit is changed, looking at everyone again is done
    synchronously in the request (about 0.3 s for 20,000 people). A larger
    server could make it background work. It was not done now, because the
    request must see the new `over` at once.
  - When disabling a tenant, it only checks "one enabled site administrator
    remains elsewhere", and does not check whether the remaining one has a
    password. On a server with `ssoOnly` on, if the only administrator with a
    password in that tenant is disabled, then when the identity provider fails
    the only way left is command-line rescue.
- **Noted in the second review**:
  - The usage hook is called under `adminMu` in `Transfer` and `DeleteSpace`, and
    under `createMu` in `CreateWith`. While exactly one site-wide look-again
    (after a reconciliation or a site limit change) is running, these requests
    wait for it. It does not hold a database lock and the hook does not call back
    into spaces, so there is no deadlock, only delay.
  - In `TestARecountDuringWritesLeavesTheCountersExact`, `InsertUser` is called
    outside a transaction, and on PostgreSQL in rare timings the last
    reconciliation reports one corrected row. In production it is only called
    inside the account-creation transaction, so this only affects the test.
- **Not done yet**: everything in the contract for this part is done. Refusal for
  bytes, pages and headcount (`507 quota_exceeded`) is still left, to decide the
  rule after seeing measured usage. De-duplication per person and counting
  attachments separately were not done.

**List of admin items (2026-11-23, the basis of 3.5 and 3.6)**

The principle the owner set on 2026-11-23: a tenant administrator can have any
permission as long as it does not affect the whole site (does not use the
site's spaces and resources). The site administrator is the highest level, and
can manage, see and change everything, including the configuration of any
tenant. For anything shared by the whole site, the site administrator decides
whether it is "enforced on all tenants" or "only a default, which can be taken
up voluntarily". A tenant administrator can configure it, but cannot violate the
site's enforcement. By this principle, every item an administrator can set in
the product falls into three kinds:

- **(a) Site only**: it truly affects the whole system or shared resources.
- **(b) Layered**: the site gives a value, and decides for the layers below
  "enforced" (`lock`), "minimum requirement" (`tighten`, new in 3.6: below, it
  can only be stricter), "restricted range" (`restrict`) or "only a default"
  (`allow`). The tenant sets within that range, and what should go lower goes on
  to the space and the person.
- **(c) Tenant-owned**: each tenant has its own copy, unrelated to the others,
  using none of the site's resources (outbound requests still go through the
  site's checks and total cap, see below).

Who may change these items is a **permission key** of the item (3.6 part 1,
built): every administration route names one, a site administrator holds every
site and tenant key, a tenant administrator every tenant key of their tenant,
and no key of the site or a tenant reaches a space's content. Managing a space
one is not a member of no longer includes its owners or its remote: that is the
explicit, audited takeover. See
[auth-and-permissions.md §16.1](../auth-and-permissions.md#161-permission-keys-36-part-1-built).

There is also a layer of **capabilities** (decided by the owner on 2026-11-23,
done in 3.6, see the 3.6 contract in
[phase-3-autonomy-and-providers.md](phase-3-autonomy-and-providers.md)): one
set per tenant, with site defaults and the site administrator overriding per
tenant, and the tenant administrator can only view. A capability governs
"whether it exists", and layering governs "how much": for the tenant layer of
(b) and every item of (c), the capability that decides whether it exists in this
tenant is marked. When the capability is off, the tenant layer of (b) does not
exist (the tenant follows the layer above), and the item of (c) is stored but
has no effect. Those marked "—" always exist. Capabilities themselves and plans
are (a).

**Default, forced, or forced for one organisation (3.6 part 7, built).** Every
setting in the registry (`internal/settings/registry.go`) has exactly one of
three things, and a test fails when one has none: a tenant layer; `ForTenant`,
for a site-only setting a site administrator may still set for one tenant
(`storage.assets.ownStorageExempt`, `exports.days`); or `ServerWide` with its
reason, for one that must be the same everywhere (`users.reuseUsernames` and
`users.emailNonAscii`: uniqueness over the whole server; `recycle.days`: one
purge job over the server's disk; `exports.maxBytes`: the room of every export
together).

**The site's rule for one tenant is the one model of "what an organisation may
have".** It is a row of the site's layer whose `layer_id` is the tenant
(`layered_settings`: `site`, the tenant, the key), holding a value in place of
the site's and a `lower` the tenant's layer is held to in place of the site's
(`settings.Rule`). A lock forces a value for that tenant ("Acme has no offline
copies": `offline.copies` locked to `false` for Acme); a restriction bounds it
("at most 10 spaces a person": `quota.spacesPerPerson` restricted to `max: 10`
for Acme); a value alone is the site's default for that tenant. It is the
site's own choice for that tenant, so the site's `lower` for tenants does not
hold it; only the site's lock over everyone beats it (kept, not in effect). The
tenant's own row is separate and is kept beside it, so no tenant write can
clear a rule, whatever its cache said; the `site_locked` column of the tenant's
row is no longer written (v0: it goes with the next schema change). Strongest
first: the site's lock over everyone > the site's rule for the tenant > the
tenant's value > the site's value for the tenant > the site's value > the
space's owner. A restriction bounds the value in effect, not only the tenant's
own: a value in effect it forbids (the site's, or the site's for the tenant)
becomes the nearest allowed one (`Lower.Nearest`: a limit's `max`, a boolean's
`false`, a choice's next stricter allowed choice, else the strictest allowed),
so "at most 10 spaces" is at most 10 whatever the site's value. The API's
`siteLock` is the short form of a locking rule; without a value it locks the
value in effect then, so locking never changes what the tenant sees. Removing
a rule deletes its row.

This replaces the limits sheet's capabilities for settings: nothing in
`internal/settings` reads `internal/limits`. The next part folds the sheet in:
the rule row gains a source (manual > billing > plan) and a window (starts,
ends, with one timer), plans become bundles of rules, and `internal/limits`,
`tenant_limits` and the `/capabilities` and `/limits` endpoints go, their keys
moving into this registry. `settings.siteRuleOf` is the one place that picks
the rule in effect from a tenant's site rows, so that is where source and
window are added; the precedence above and the resolver do not change.

The cache is per tenant: a site write bumps a site generation that every
tenant's snapshot is checked against, a tenant write bumps only that tenant's.
Warm, `For` is one `sync.Map` read and two atomic loads, no lock and no query.
The 2,000-tenant cap is gone; memory is bounded by sharing instead: a setting
a tenant has no row for points at the one entry computed with the site's
layer, so a tenant costs its map (about 1 KB at 20 keys, about 3 KB at 60) and
some 300 bytes for each key it sets or has a rule for, about 30 MB for ten
thousand tenants. A cold tenant reads its two kinds of rows (its own and the
site's for it) over the site's snapshot. A deleted tenant's entry and rows are
dropped. A change of the limits sheet still bumps the site generation (it is
rare, and goes with the sheet).

(a) Site only, 26 items:

| Item | Reason |
|---|---|
| `--listen`, `--public-url`, `--data`, `--database`, `--state-dir` | The address and storage location of the server itself |
| `--trusted-proxy`, `--allow-origin`, `--insecure-no-auth` | The trust boundary of the whole process toward the outside |
| `--max-open-spaces`, `--open-spaces-memory`, `--max-rebuilds`, `--space-idle`, `--pull-interval`, `--remote-timeout`, `--commit-*`, `--draft-*` | The memory, CPU and network rhythm of the process, shared by all tenants |
| `secret.key` | All sealed secrets depend on it |
| The tenancy switch `/api/admin/tenancy` | Decides whether this server is multi-tenant |
| Creating, deleting, disabling tenants, `slug` | It constrains the tenant itself, which cannot decide this for itself |
| **Each tenant's table of capabilities and limits** (including the tenant total, headcount limit, fixed quota, per-file ceiling, allowed storage modes, usage cap) **and the plan** | The limits table decides how much of the site's things a tenant can use. Plans and billing belong to an internal billing module (3.6, not done) |
| Granting `siteAdmin` | The highest permission can only be given by the highest level |
| Server status `/api/admin/status`, disk headroom | The state of the whole machine |
| `--max-upload`, `--max-import` as ceilings | Request bodies and temporary files take site-wide memory and disk. The per-file limit itself is (b) |
| 3.5's site storage budget `storage.siteBudget` and the site values of the storage pressure controller (the tiers of `storage.advanced`, hysteresis, emergency reserve, attachment cache cap; replacing 3.1's `cache.partial`, `evictAfterDays`, `minIdleHours`, `clonesBytes`, `minFreeBytes`, `assetsBytes`) | How the site's disk is freed up is shared by all tenants |
| Which storage modes the site offers `storage.modes.available` (3.6) | Server storage takes site disk. A tenant's own storage target makes the server connect outward on the tenant's behalf |
| Per-address request allowance, failure cap, lockout (`limits.*`, `lockout.*`) | At sign-in the tenant is not yet known. A stricter tenant cap would leak that a username exists, and in which tenant, from "when refusal starts" |
| The captcha on the sign-in page and `setup` (provider, keys, the `login` / `setup` switches) | Same as above: the sign-in form is site-wide |
| `passkeys.enabled` | The relying party is the site's host name |
| Audit log retention (100,000 rows / 90 days) | One site-wide table |
| The site-wide mail sending cap (queue 1000, 3 per hour per address) | The in-memory site-wide queue and abuse prevention |
| 3.5 recycle bin retention days `recycle.days` | The recycle bin takes site disk. What the owner wanted is a uniform 30 days |
| 3.5 export total `exports.maxBytes` | Takes site disk. (Export retention `exports.days` moved to (b) in 3.6 part 7: an export keeps the days of the tenant that made it, and the site may set them for one tenant) |
| The total outbound cap `outbound.perMinute`, per-tenant share `outbound.tenantPerMinute` | The site's outbound capacity and its outside reputation |
| The table of public email domains (domains that cannot be claimed) | Prevents claiming things like `gmail.com` |
| Arbitration of verified domains (handing over to another tenant) | A matter between two tenants, which only the level above can decide |
| Organization self-service sign-up `tenant.selfService`, its limits (how many per person, how many per hour site-wide), and the plan used for new tenants | Creating a tenant takes site resources |
| Usernames are unique site-wide; from 3.6 emails are also unique site-wide (rules, not settings) | Sign-in does not pick a tenant first (3.3), and email is the main way to sign in (3.6) |
| Whether a username can be reused after purge, `users.reuseUsernames` (3.5) | Usernames are unique site-wide, so only the site can decide |
| The site-wide cap of "check now" for domains, `domains.checksPerMinute` (3.6) | DNS lookups are the site's outbound |

From 3.6, the former `auth.tenantSso`, `auth.idpAdmins` and the tenant total
(including `quota.pinnedBytes`) are no longer separate site settings. They
become capabilities (`signin.sso`, `signin.idpAdmins`, `storage.quota`,
`storage.pinnedBytes`, `other.people`), which can differ per tenant.

(b) Layered, 28 items (for key names see "Tenant autonomy" in `docs/api.md`):

| Item | Layers | Which capability decides the tenant layer | Reason |
|---|---|---|---|
| `registration.open`, `.domains`, `.perHour` | site, tenant | `other.registration` | Already in 3.3. Opening registration uses the site's mail sending, so the site default is locked |
| `legal.termsUrl`, `.privacyUrl` | site, tenant | — | Already in 3.3. Each tenant's terms can differ |
| `quota.spacesPerPerson`, `.bytesPerPerson`, `.pagesPerPerson` | site, tenant, person | — (a tenant's value does not exceed the capability `storage.perPerson`) | Already in 3.3 |
| `daily.own`, `daily.api`, `daily.default` | site, tenant (space below) | — | Already in 3.3 |
| `daily.useSiteApis` | site, tenant | — | Whether a tenant can use the site's external APIs (using the site's allowance) |
| `auth.ssoOnly` | site, tenant | `signin.sso` | It only takes effect after the password check, so leaks nothing. A tenant can only be stricter |
| `auth.loginMode` | site, tenant | — | A sign-in page with `?tenant=` uses the tenant's |
| `auth.passkeys.requireRoles` | site, tenant | — | Required only after the password. A tenant can only be stricter |
| `auth.social` | site, tenant | `signin.socialChoice` | Of the site's social sign-ins, which ones people of this tenant can use (can only be cut down) |
| `session.idleDays`, `.maxDays` | site, tenant | — | Computed by the person's tenant when the session is read. A tenant can only be shorter |
| The captcha's three switches `register`, `forgot`, `invite` | site, tenant | — | These three forms know the tenant. A tenant can only be stricter |
| `mail.useSite` | site, tenant | — | Whether a tenant with no sender of its own can borrow the site's SMTP |
| `access.tokens`, `access.mcp` | site, tenant | `integrations.tokens`, `integrations.mcp` (when off, this tenant has no tokens or MCP) | If the site turns it off, it is off everywhere. When the site has it on, a tenant can turn it off for its own people |
| `connections.useSiteApps` (called `github.useSiteApp` before 2.7b) | site, tenant | — | Whether it can connect through the connections the site configured (GitHub App, and from 3.8 the OAuth apps of GitLab / Gitea) |
| **`storage.mode`** (storage mode, 3.6) | site, tenant, space (when creating a space) | The modes to choose from are the capability `storage.modes` | The site decides the default or enforces it. A tenant chooses among the modes the capability allows |
| `storage.remotes.scope`, `.graceDays` | site, tenant | — | 3.1's enforced remote. From 3.6, "whether a remote is needed" is derived from `storage.mode` |
| `storage.hosts` (`--remote-host`) | site, tenant | — | Which hosts the server connects to. A tenant can only cut down from the site's table |
| `storage.localSpaces.maxBytes` | site, tenant | — | Local-only spaces take site disk, and the site sets the cap |
| `storage.assets` (attachments in the space repository or the attachments repository) | site, tenant, space (owner) | `sync.assetsRepository` of the limits table (when off it can only be the space repository) | Three layers decided by the owner: the site can enforce (`lock`), or only give a default (`allow`). Separate (`repository`) is the default since round 2; see "Where attachments go" below |
| `storage.assets.ownStorageExempt` | site, or set for one tenant by the site (3.6 part 7) | — | Whether a tenant on its own storage target is left out of the site's `storage.assets` value and locks. Default on; reserved until 3.6 makes tenant targets |
| `storage.partial.unsupported` (remotes that cannot sync partially: allow (full clone) / refuse, 3.6) | site, tenant | — | The single partial-sync policy decided by the owner, default allow |
| The tenant-adjustable values of storage pressure (in `storage.advanced`, evicting this tenant's spaces, days before archiving, the login prefetch switch and "people active in the last N days", 3.5) | site, tenant | — | A tenant can only adjust within the site's range. Tiers, reserve and budget are site only |
| Exempting from the remote requirement (3.1's `remoteExempt`, a space's `exempt`) | site, tenant | — | An exemption only removes the requirement added by the layer that grants it |
| Pinned on the server (3.1's `pin`) | site, a tenant within the capability `storage.pinnedBytes` | `storage.pinnedBytes` (0 means cannot pin) | Pinning takes site disk |
| `files.maxBytes` (size of a single file) | site, tenant | — (a tenant's value does not exceed the capability `storage.maxFileBytes`) | Decided by the owner: the tenant configures it and the site sets the ceiling |
| `offline.copies` (4.1's allowing offline copies) | site, tenant (two layers made in 4.1, no space or person layer) | `other.offline` (when off, this tenant has no offline copies) | It takes devices, not the server. The site can lock or restrict it to off only (done in 4.1). 3.6 only adds the capability |
| Default settings of tenants created by organization self-service sign-up | site → new tenant | — | A new tenant starts from the site defaults |
| Reserved: default language, color scheme, greeting styles | site, tenant, person | — | Listed in "Layered settings and locking", keys to be added when it is done |
| Reserved: whether boards and views can be saved by editor members, and whether a space can open to joining | site, tenant, space | — | Same as above |

#### Where attachments go (round 2)

Decided by the owner on 2026-11-24. Attachments are **separate by default**: the
default of `storage.assets` is `repository`, so a new space made with a
repository on a provider also gets `<name>-assets`. A space with no remote keeps
its attachments in the space; and where a second repository cannot be had
(an existing repository with no attachments repository beside it, a name that is
taken, a credential that cannot reach it) the default falls back to `space`
without failing the creation. Only a request that asked for one is an error.

The order, strongest first: **the site's lock over everyone > the site's lock for
one tenant > the site's default > the tenant's default > the space's owner**.
Locks go down from the widest layer; for plain defaults the nearer layer
overrides the wider one, as every layered setting does. This reuses the layered
settings (the site's `lower`, the tenant's value and `lower`) and adds one thing
they lacked, the lock for one tenant: since 3.6 part 7, the site's rule for that
tenant (a site-layer row for the tenant, see "Default, forced, or forced for one
organisation" above; before, a tenant row marked `site_locked`). The tenant's
administrators cannot change its value or its rule; the site's lock over
everyone still beats it (the value is kept, not in effect). The space layer is the owner's request at creation, not a stored
setting.

The tenant's limits table (`sync.assetsRepository`, 3.6) is the last word: when
it is off only `space` is possible, whatever the layers say.

**Tenants on their own storage** (`storage.assets.ownStorageExempt`, site, default
on): a tenant whose spaces live on a target of its own (3.6's `storage.modes`
`tenant`) is not bound by the site's value or lock for `storage.assets`,
including the lock for that one tenant. It chooses freely inside its own layers.
Turned off, it is bound like any other tenant. The rule is built and tested
(`settings.AssetsRule` takes `OwnStorage` and `RepositoryOff`); the callers pass
`false` for both until 3.6 exists, so the switch is stored, shown and edited
but exempts nobody yet. 3.6 must pass the two inputs where it creates spaces
(see the 3.6 notes in phase-3-autonomy-and-providers.md).

Cost: the rule is read from the tenant's cached settings, a map lookup; creating
a space makes one more repository call when an attachments repository is made;
listing, sync and reading are unaffected.

(c) Tenant-owned, 16 items:

| Item | Which capability decides it | Reason and security constraints |
|---|---|---|
| This tenant's users, invites, roles, disabling, deleting (3.5) | — | Already so since 3.3. Cannot touch a site administrator |
| Local groups, group members | — | Already so in 3.2 and 3.3 |
| This tenant's spaces: disable, transfer, delete, restore, evict from cache (3.5) | — | Touches only its own tenant. Emptying the recycle bin early can only be done by the site (the decision involves the site's disk) |
| This tenant's storage summary, list of people and spaces (3.5 part 4) | — | Reads only the numbers stored for its own tenant. It does not reveal disk headroom, and the list has no personal spaces (`AdminScopeOf`). A site administrator can view by tenant |
| **Viewing and exporting the content of organization spaces** (3.5) | `data.adminView` | Audited every time. Personal spaces can never be seen (3.7) |
| Tenant name | — | Already so in 3.3 |
| **SSO providers** (enterprise OIDC / SAML) and their group mapping; mapping IdP groups to organization administrators | `signin.sso`; mapping to administrators also needs `signin.idpAdmins` | Can only sign in, create and merge accounts of this tenant. Never signs in a site administrator. When the issuer changes, the old identity is unlinked |
| **Email domains** (DNS verification) and "do not allow people on this domain to create another organization" | `signin.domains` | An unverified claim does nothing. A domain has only one verified claim |
| **Ways to join** (3.6): invite only, auto-join by domain, apply by domain with approval, instant join through this organization's SSO (can be let through by IdP group), the default role of joiners, mapping IdP groups to the organization's groups | Invites always exist. By domain needs `signin.domains`. Through SSO needs `signin.sso` | Only affects people entering this organization |
| **Managed accounts** (3.7) | `signin.managedAccounts` | Needs a verified domain. Only manages accounts whose email is on this domain, and does not touch other organizations |
| **Sender SMTP** | `integrations.smtp` | Only `starttls` / `tls`, public addresses, restricted ports. Without it, it borrows the site's through `mail.useSite` |
| **Captcha keys** | `integrations.captcha` | Used only on the three forms that know the tenant |
| **Daily text external API, pasted text, language** | `integrations.daily` | Its own sealed secret, its own `rps` and cache, not using the site's allowance. Outbound counts toward the site's total cap |
| **Connections** (2.7b): its own GitHub App and sharing of its installations; from 3.8 its own GitLab / Gitea OAuth apps | `integrations.connections` (called `integrations.github` before 2.7b) | Shared only within this tenant. For the site's App, the site decides which tenants an installation is shared with. Hosts go through `storage.hosts` and the public-address check |
| **Organization logo** (3.9) | — | The upload is re-encoded on the server and stored on the server |
| **Storage target** (3.6): a location on a provider (2.7b): the tenant's own GitHub organization (its own App or an installation shared with it), or its own GitLab / Gitea host and group (3.8), where each space automatically gets a repository | The capability `storage.modes` includes `tenant`. Using its own App also needs `integrations.connections` | Credentials are sealed and carry the tenant id. Hosts go through the site's `storage.hosts` and the public-address check |
| This tenant's audit log (read only; downloading needs `data.auditDownload`) | Viewing is always allowed | Already so in 3.3. It cannot see the site-wide kinds |

**Constraints on outbound requests** (every address a tenant administrator
fills in: issuer, SAML metadata, SMTP, daily text interface, DNS verification,
storage target): at connection time, 2.3's public-address check is done on the
resolved address (no loopback, internal network, link-local and so on, and
redirects are checked too, at most 3). It counts toward the site's total outbound
cap and this tenant's share. When exceeded, it is not sent and answers as "remote
unreachable". The answer size and timeout are the same as the site's. Secrets
are sealed with the site's `secret.key`, with the tenant id in the purpose, so
moved to another tenant's record they cannot be opened.
