# Milestones: phase 2, connections and sign-in (2.7, 2.8, 2.7b)

**2.7 GitHub App, user authorization, automatic repository creation**

- When creating a space, choose "Create a repository on GitHub": under an
  organization, use the GitHub App's installation token; under a personal
  account, use GitHub user authorization (web authorization, storing a refresh
  token and renewing it automatically).
- The two points that [storage-and-sync.md §4.3](../storage-and-sync.md#43-remote-credentials-chosen-per-space) did not
  state clearly were tested against real GitHub on 2026-11-23; the conclusions
  are written in [storage-and-sync.md §4.3](../storage-and-sync.md#43-remote-credentials-chosen-per-space).
- Credentials are stored encrypted in the database as before, and tokens reach
  `git` only through environment variables.

**The 2.7 contract (2026-11-23)**: the API and error codes are written first
into `docs/api.md`: the "GitHub" section, `POST /api/spaces` under "Spaces", the
Remote object and `PUT …/remote` under "Remote repository", and "GitHub App"
under "Administration"; the front end and back end are built in parallel from
it.

- **One App per server, github.com only**. The App's configuration is stored in
  the state database (not in command-line flags, in line with 2.8's direction
  of moving OIDC into the admin console): App ID, private key, client id,
  client secret; the private key and the secret are encrypted with the server
  key (`Seal("site", …)`) and returned by no API; only "saved" and a
  fingerprint of the public part of the private key are shown. Before saving it
  is checked against GitHub (`GET /app` validates the private key and App ID; a
  made-up token is sent to `POST /applications/{client_id}/token` to validate
  the client id and secret, where `404` means the pair is correct), and if the
  check fails it is not stored. Missing App permissions (Contents write,
  Metadata read, Administration write) only produce a warning and are not
  rejected. Switching to a different App is rejected as long as any space or
  user still uses the current one (`409 provider_in_use`); changing the private
  key or secret of the same App is always allowed.
- **Tokens**: copied from Rackbook's `internal/githubapp`. Installation tokens
  live only in memory; the ones used for sync are narrowed to a single
  repository (`repository_ids`, `contents: write`), and the one used to create
  a repository is `administration: write`, not limited to a repository, and
  used only for that one request; a token is replaced only within 5 minutes
  before it expires, concurrent requests for the same token are merged into
  one, no lock is held during network requests, and the cache holds at most
  10,000 entries. The way the token is handed to `git` is exactly the same as
  the existing `token` credential (a credential helper in an environment
  variable, limited to `https://github.com`), so `gitstore` needs no change.
  Only spaces that are open get pulled, a space in use replaces its token at
  most every 55 minutes, and a closed space costs nothing.
- **User authorization**: web authorization, with `state` plus PKCE (S256).
  The callback, like the OIDC callback, is an exception to the cross-site rule,
  and requires the session to be the same user's. Each PetalBook user has at
  most one connection, and a GitHub account can connect to only one PetalBook
  user. The access token and refresh token are stored encrypted separately; the
  access token is renewed when it is used close to expiry (within 5 minutes),
  only once at a time per user, and the new pair is written to the database
  before it is used (GitHub replaces the refresh token on every renewal and the
  old one is invalid at once); every day, refresh tokens that will expire
  within 30 days are renewed once, so someone who does not open a space for
  months is not dropped. When the refresh token expires or is revoked, the
  connection becomes `expired`, and the spaces that sync with it enter `error`
  with the error code `connection_expired`: reads work as usual, writes answer
  `503` without changing anything, drafts are still saved, and after
  reconnecting it retries at once and makes up the commits, losing nothing.
  When a connection is disconnected, revoking the authorization at GitHub is
  attempted on a best-effort basis, and the tokens in the database and in
  memory are deleted regardless.
- **Whose token creates the repository**: the user's own token is used if it
  can be (`POST /user/repos` under a personal account, `POST /orgs/{org}/repos`
  under an organization the user's connection can see), so GitHub itself checks
  whether this person may create repositories in this organization and whether
  they may create private ones. An administrator can share an organization's
  installation with some tenants; members of those tenants can use the
  installation token to create and connect repositories there even if they have
  not connected GitHub; by default it is not shared, otherwise any member could
  use the App to create repositories in the organization.
- **Whose token syncs**: repositories under an organization use the
  installation token (`via: "app"`), without depending on any one person being
  there and connected; under a personal account, the installation token is also
  used when the App is installed on that account and can reach the new
  repository, otherwise this person's user token is used (`via: "user"`). For a
  space synced with a user token, it stops (`connection_missing`) when the
  bound person cannot sign in, has lost `manage`, or has disconnected, so that
  the token of someone who was removed from the space is not still being used.
- **Creating the repository when creating a space**: extend `POST /api/spaces`
  with `storage.sync: {provider: "github", owner, name, private, description,
  create}` (called `github` before 2.7b), with no separate API. Order: checks
  that need no network (fields, `--max-spaces` (from 3.3 this is the space
  quota), owner, whether the token exists) → create the space on disk (no
  record yet, nobody can see it) → create an empty repository on GitHub
  (`auto_init: false`, so the space's first commit is the repository's first
  commit) → choose and verify the credential used for sync → push `main` (a
  just-created repository sometimes cannot be found for a few seconds, so "not
  found" and 5xx are retried up to 4 times within 15 seconds, and a rejected
  credential is not retried) → in one transaction write the space, the owner
  and the remote, then make the space visible. Failure before the repository
  is created: nothing is left anywhere; failure after the repository is
  created: the space on disk is deleted and the empty repository on GitHub is
  **left, not deleted** (we never delete anything on the remote, and may not
  even have permission to), the error response carries `repo`, and the same
  request with `create: false` connects the new space to that empty
  repository. A process crash in the middle is the same, except there is no
  response.
- **A reminder for the empty repository left behind (decided by the owner,
  2026-11-23)**: when a failure after creating the repository happens, the
  repository is still left, and at the same time a reminder is recorded for
  this person, visible later in the GitHub row of the account page (it can also
  go next to the space list), stating the repository name and link, with two
  actions: "Sync to it and try again" (that is `POST /api/spaces` with
  `create: false`, the name and repository taken from the reminder, no separate
  API) and "I deleted it"
  (`DELETE /api/connections/github/leftovers/{repoId}`, which also answers
  `204` when there is no such reminder). Stored per user:
  `github_leftovers(user_id, repo_id, full_name, html_url, space_name,
  created)`, with primary key `(user_id, repo_id)`, deleted along with the
  user; another failure for the same repository just replaces it with a new
  one; each person keeps at most the latest 20. It is cleared when the person
  handles it, or **when any space connects to this repository** (`PutRemote`,
  in the same transaction, deletes this reminder for everyone by `gh_repo_id`,
  so a successful retry and a manual `PUT …/remote` both clear it). The
  reminders are in `leftovers` of `GET /api/connections`, which the account
  page and the create-space dialog have to read anyway, so no extra request. A
  crash gives no response and cannot be recorded, so repositories left by a
  crash are not among them (see above).
- **"Only select repositories" installations**: we do not bet on whether GitHub
  automatically adds repositories created by an App to the installation. After
  creating, a token is requested for the new repository separately as a test;
  if it cannot reach it and the caller's connection can see this installation,
  the user token is used to add the repository to the installation
  (`PUT /user/installations/{id}/repositories/{repoId}`) and it is tried again;
  if that still fails, a personal account falls back to `via: "user"`, and
  under an organization it answers `409 repo_not_in_installation` (with
  `repo`). Whether Administration is organization-level or repository-level: we
  do not bet on that either; when GitHub refuses to create the repository, the
  answer is passed on as it is, `403 provider_refused`, with `message`
  relaying GitHub's reason. In both outcomes the API needs no change; the real
  tests only decide how the UI hints are written and which path is the
  default.
- **Switching an existing space to GitHub**: the credential of `PUT …/remote`
  gets one more kind,
  `{"type": "connection", "provider": "github", "via": "app" | "user"}`; for a
  space that already follows a remote, `PUT` replaces the address and
  credential in place; the same repository has the same history, so only the
  credential is replaced, and the old key or token is deleted. An SSH-form
  github.com address is accepted and stored in https form. `via: "app"`
  requires that the caller's connection can push to this repository, or that
  the installation is shared with the caller's tenant; otherwise anyone with
  `manage` could connect a space to any repository the App can reach.
- **The Remote object** gains `errorCode`: it exposes the existing internal
  classification (`auth`, `hostkey`, `clone`, `rescued`) and adds four GitHub
  ones (`connection_expired`, `connection_missing`, `installation_gone`,
  `provider_not_configured`). A space in one of these states retries every 10
  minutes, and retries at once when the cause goes away (reconnecting, the
  administrator saving the App).
- **Lists are all paged, never fetched whole**: the installation list, the
  accounts where repositories can be created, and the repositories that can be
  connected, with at most one GitHub request (30 items) per page and an opaque
  cursor; GitHub cannot filter the repository list by name, so `q` is not
  offered, and in the UI one can type `owner/name` directly.
- **State database** (test on both SQLite and PostgreSQL): `remotes` gains
  `cred_type = 'connection'` and the columns `provider`, `gh_via`,
  `gh_installation_id`, `gh_repo_id`, `gh_user_id`; a new table
  `github_authorizations` (one row per user, GitHub account id unique, both
  tokens encrypted, with expiry time, state and a version number used to
  "replace only the pair from before the renewal"); `github_installations`
  (installation id, account) and `github_installation_tenants` (installation ×
  tenant: which tenants it is shared with); the App's configuration goes in
  `site_settings`.
- **A place kept for 3.1**: `remotes: {require: null | "any" | a provider id,
  scope: "new" | "all", graceDays}` of `GET/PUT /api/admin/storage` and the
  error code `409 remote_required` are only reserved in `docs/api.md`, not
  implemented. Creating a space and creating a repository are already one step
  that is "either wholly succeeds or leaves no space", so 3.1 only needs to add
  a check before the first step.
- **Merged with the tenants of 3.3 (2026-11-23)**:
  - The App's settings and the installation list are site-wide:
    `/api/admin/connections/github…` is for site administrators only; a tenant
    administrator gets `403 forbidden`, as with other site-only APIs; the
    connections on the admin console's "Storage & sync" page are also shown
    only to site administrators. The settings are still in `site_settings`,
    with no tenant layer.
  - **Shared installations are counted per tenant (decided by the owner,
    2026-11-23, replacing the earlier site-wide sharing)**: a site
    administrator shares an organization's installation with **a few chosen
    tenants** (`PUT /api/admin/connections/github/installations/{id}`,
    `{tenants: [id]}`, replacing the whole set, `[]` means not shared); only
    members of those tenants can use the installation token to create and
    connect repositories there, and only they see it in
    `GET /api/connections/github/owners` and `repos`; other tenants can use
    that organization only when their own GitHub connection reaches it. The
    decision to share stays with site administrators and tenant administrators
    cannot touch it (`/api/admin/connections/github…` is site-only). Stored in
    the state database: in 0013 `github_installations` has no sharing column,
    and there is a separate `github_installation_tenants(installation_id,
    tenant_id)` with a two-column primary key and an index on `tenant_id`,
    deleted when the installation is forgotten (deleting the App); `tenant_id`
    is not a foreign key, because the `tenants` table is in 0018 and both
    migration orders must work, and when a tenant is deleted the code deletes
    its sharing rows (`DeleteTenant`). Tested on both SQLite and PostgreSQL.
    Queries in the database go through `Scope` (`ListGitHubInstallationsOf`,
    `GitHubInstallationOf`, `GitHubInstallationShared`: a zero-value scope sees
    nothing), and the service takes the scope from the tenant the user is in;
    in the scope test's allowlist the old three "site-wide sharing" entries are
    removed and replaced by two for site administrators, a write and a read
    (`SetGitHubInstallationTenants`, `GitHubInstallationTenants`), and
    `UserTenant`; `github_installation_tenants` is added to the tables that are
    scanned. The isolation test gains a real cross-tenant case: after sharing
    with B, B's members can see and create, A's members cannot see, cannot
    create (`owner_not_allowed`), listing repositories gives `404`, and no
    repository is created. The tenants are picked on the installation row in
    the admin console.
  - A user's GitHub connection belongs to the user, so it lives in the user's
    tenant along with the user, with no extra `tenant_id`. A GitHub account can
    connect to only one PetalBook user; this **cross-tenant** check (the GitHub
    account id is unique site-wide); the answer is only `account_in_use`, not
    saying who or in which tenant.
  - A space created with a GitHub repository belongs to the creator's tenant,
    and goes through 3.3's space quota like an ordinary new space
    (`403 quota_exceeded`, ones being created also count, recorded once per
    person and once per tenant) and the usage hook. A credential with
    `via: "user"` must be from a person in the space's tenant: only a site
    administrator, who can open any space, can run into this, and `PUT …/remote`
    answers `403 forbidden` before asking GitHub; when showing the remote, this
    person is also looked up only in the space's tenant.
  - The cross-user methods in the state database (the daily renewal, the App's
    own counts, revoking all connections when the App is deleted, the
    installation list) are all site-wide, and are written into the scope test's
    allowlist with the reason noted; the scope test also counts
    `github_authorizations`, `github_installations` and `remotes`.
  - Migrations: 0013 does not touch the tables changed by 0018 (0018 adds
    columns to users and spaces and rebuilds groups), so both orders work:
    a database migrated on this branch runs 0014, 0016 and 0018 after the merge;
    a database migrated on main runs 0013. Both orders are tested (SQLite and
    PostgreSQL).
  - In the isolation test every 2.7 route has an entry:
    `/api/admin/connections/github…` is tested as site-only, and
    `/api/connections/github…` is the caller's own connection, or an
    installation shared with the caller's tenant.

**2.7: what was built**:

- The migration is `0013_github.sql` (`0011_api_tokens.sql` and
  `0012_oauth.sql` belong to feat/mcp). Migrations are recorded by their own
  numbers, and missing ones are applied at the next start, so a database that
  was migrated only on this branch will run 0011 and 0012 after the merge; the
  numbers themselves must not collide, so 0013 is used here. The test on the
  number of migrations follows the files and does not hard-code it. Tests run on
  both SQLite and PostgreSQL.
- The code has three layers: `internal/githubapp` is the client to GitHub (JWT,
  installation token cache, user authorization, REST calls, with a configurable
  base address that defaults to `api.github.com` and `github.com`; tests use the
  fake GitHub in `githubapptest` and do not connect to the real one);
  `internal/github` is how the server uses this App (settings, authorization
  and renewal, accounts where repositories can be created, choosing the
  credential, the token for Git); `internal/spaces` holds creating and
  switching spaces (`CreateWithSync`). The token for Git still goes through the
  environment-variable credential helper of `gitstore.Token`, and `gitstore`
  gained only one error type that says "the credential itself cannot be
  obtained".
- There are two hidden start-up flags for the base addresses,
  `--github-api-url` and `--github-url`, for tests only (that only github.com is
  supported is unchanged).
- When creating a space, the cap is counted as "existing plus being created", so
  concurrent requests cannot exceed it (from 3.3 this is the space quota, and
  it is counted this way per person and per tenant). The retry intervals for the
  first push are 1, 3 and 7 seconds (at most four tries), within a total
  deadline of 15 seconds, in which both the waiting and each push itself count;
  when the deadline arrives, the push in progress is stopped. Only "not found",
  GitHub 5xx and being unable to connect are retried; for a credential GitHub
  does not recognize, a private repository also answers "Repository not found",
  which cannot be told apart, and the deadline is the backstop. Other
  authentication failures are not retried.
- A space being staged has a marker file `staging/<id>` (created before the
  directory, deleted on `Commit` or `Abort`). At start-up, for a marker older
  than 15 minutes with no `spaces` row, its directory and the marker are
  deleted; if there is a row (a crash after the transaction), only the marker is
  deleted. Only ids that have a marker are looked at, so real spaces and
  directories not staged by this code are never touched.
- The six questions for the owner were all done with the defaults in brackets.

Known issues:

- When two processes connect to the same PostgreSQL, they may renew with the
  same refresh token at the same time: GitHub honors only the first, and the
  other is treated as an invalid authorization, so the connection is dropped.
  The service now runs as a single instance, so this is acceptable. For multiple
  processes later, a per-user lock must be added around "renew and replace"
  (`SELECT … FOR UPDATE`, or an advisory lock).
- When reconnecting right after disconnecting, the new row's `version` starts
  again from 1; if a renewal begun before the disconnect is still waiting for
  GitHub's reply, it passes the version check, marks the new connection
  expired (usually requiring one more reconnect), and, when revoking the old
  authorization also failed, may even write back the old account's token. It
  happens only if one renewal spans the entire "disconnect + browser
  authorization". The in-process generation could be checked once more before
  writing back, or the version number could keep increasing after a reconnect.
- For a space synced with someone's GitHub connection, if the server's App
  client is gone (the admin console does not allow deleting it while anyone is
  connected, so this is almost unreachable), a write answers
  `409 provider_not_configured` instead of the `503 unavailable` the document
  says: the credential error wraps GitHub's error, and `fail()` recognizes the
  latter first. Nothing is lost; later, make the credential error stop wrapping
  this error, or order by situation.

Not doing in 2.7:

- The device code flow (the web does not need it; left for the phase 4
  client).
- Webhooks (an App being uninstalled is found only when a token renewal is
  next refused); using push events in place of pulling every minute is left for
  3.1 to look at together.
- GitHub Enterprise Server, multiple Apps, GitHub sign-in (sign-in is in 2.8,
  and it is a different thing from "connect a GitHub account" here, stored
  separately in the database).
- Single-user mode: there are no accounts, and this whole section answers `404`.

Questions for the owner (in brackets is the default when there is no reply):

1. **The two tested points** (left over from [storage-and-sync.md §4.3](../storage-and-sync.md#43-remote-credentials-chosen-per-space)):
   tested on 2026-11-23 against real GitHub, and the conclusions are written
   back to [storage-and-sync.md §4.3](../storage-and-sync.md#43-remote-credentials-chosen-per-space).
2. When something fails after creating the repository, leave the empty
   repository or try to delete it? (Default: leave it, give the repository in
   the answer, and retry with `create: false`; decided 2026-11-23: leave it,
   and keep a reminder for this person that can be seen later, see above.)
3. An organization's installation is not shared by default, and only people who
   have connected GitHub themselves and whom GitHub allows can create
   repositories under the organization, is that OK? (Default: not shared, the
   administrator turns it on one by one; decided 2026-11-23: sharing is picked
   per tenant.)
4. New repositories are private by default; may public repositories be created?
   (Default: allowed to choose, private by default.)
5. The installation token used for sync is narrowed to a single repository
   (safer), rather than one token per installation (fewer renewals). When there
   are many spaces, the former renews once an hour for each space in use.
   (Default: per single repository.)
6. When a GitHub connection is disconnected, it is disconnected even if spaces
   still use it, with only an advance warning of how many spaces are affected,
   is that OK? (Default: yes; revoking is the user's right, and the space loses
   nothing.)

**2.8 passkey, SAML, multiple sign-in providers, sign-in CAPTCHA**

- passkey and SAML use authcore's `passkey` and `saml`. A passkey can be bound
  to both local accounts and external accounts; an administrator can require
  certain people to use a passkey.
- Change "one OIDC provider" to "several": one button each on the sign-in page,
  and linking and unlinking one by one under the account's "Security"; GitHub
  sign-in gets its own adapter (see "Several sign-in methods" under milestone
  1.7 in [phase-0-1.md](phase-0-1.md)).
- When the same email signs in from different providers, accounts are not
  merged automatically; the rule is the same as in 1.7.
- **Sign-in page, CAPTCHA, rate limiting and lockout** (proposed by the owner,
  2026-11-22: make it the same as KazuhaHub's Report Portal and
  Passwall-Sub-Panel, SSO included). Where the two projects do the same thing,
  copy it; where they differ, follow Report Portal, which is newer and also
  uses authcore, and add Passwall-Sub-Panel's per-IP requests-per-minute limit.
  - **The four states of the sign-in page**, decided by the "sign-in mode"
    (`login_mode`), which only governs how the page is displayed:
    1. `dual` password and SSO (default): user name, password, (when needed) a
       CAPTCHA, sign in; below it "or sign in with", one button per provider,
       "Sign in with {name}"; links such as forgotten password.
    2. `sso_first` SSO first: the provider buttons are on top, the password form
       is folded into a link "Use a password instead", and expands only when
       clicked.
    3. `sso_redirect` SSO only (automatic redirect): with only one provider it
       jumps straight there, showing "Going to {name}…"; with several it does not
       jump, and lists the buttons and "Use a password instead". It does not
       jump when the address carries `?local=1` or when coming back from a failed
       SSO with `?sso_error=`, to avoid bouncing back and forth after a failure.
    4. `local_only` password only: just the form, with no SSO part.
    When no provider is enabled, it is displayed as `local_only` whatever the
    setting.
  - **Refusing password sign-in** (`sso_only`) is a separate switch, governing
    whether the server accepts passwords at all: once on, password sign-in by
    any non-administrator is refused (403 `local_login_refused`), and
    administrators are not restricted, as a back door for when SSO is broken.
    It is checked on save: it cannot be turned on while registration is open;
    it cannot be turned on if there is no administrator who can sign in with a
    password. This switch does not appear in the public API, so others do not
    learn which accounts are worth guessing. With no providers it has no effect.
  - **Providers** can be several (OIDC and SAML), each with a `slug`, a name and
    an icon; the sign-in page reads `GET /api/auth/providers` (the provider
    list, `login_mode`, and two computed booleans `local` and `sso`), and
    clicking a button navigates the whole page to
    `/api/auth/{oidc|saml}/{slug}/start`. A failed SSO returns to
    `/login?sso_error=<code>`, and the page shows the matching reason.
  - **CAPTCHA** uses authcore's `captcha`: self-hosted image (default, no
    traffic leaves the server), Turnstile, reCAPTCHA, hCaptcha.
    - Where it is used: sign-in, accepting an invitation, first-time
      administrator setup, and later password recovery and public sign-up; each
      place has its own switch. **SSO gets no CAPTCHA** (left to the identity
      provider).
    - When it is required: each place can choose "every time" or "only after N
      wrong attempts" (the latter by default, 3 by default, counted per IP and
      per user name separately, and it is required when either one is reached).
    - The page asks `GET /api/captcha?ctx=login&account=<user name>` whether one
      is needed, and if so receives the challenge (an image) at the same time,
      and asks again when the user name changes. A wrong answer returns 400 with
      the error code `captcha_failed`, and the page gets a new challenge. The
      CAPTCHA is checked before the password hash is computed, and a wrong
      answer does not count as a wrong password. An error in third-party
      verification is treated as failure (better to block).
    - Third-party secrets are stored encrypted in the database, and the admin
      page shows only "Saved · Change"; when the provider is switched, the old
      secret is not carried over; when a third party is chosen, both the site
      key and the secret are required. Third-party verification checks that the
      returned host name is this service's address.
    - Challenges live in memory, at most ten thousand, expire after 5 minutes,
      and each can be answered only once.
  - **Rate limiting and lockout**:
    - A per-IP requests-per-minute limit (default 10), applied to sign-in,
      accepting an invitation, first-time setup, fetching a CAPTCHA, passkey
      sign-in and the SAML callback; exceeding it gives 429 with `Retry-After`.
    - Failure count limit: within a time window (default 15 minutes), failures
      are counted separately per IP, per user name and per IP+user name, with a
      default limit of 10; the per-IP check is made before the hash is computed,
      and the per-user-name check only after the password is wrong, so a person
      with the right password is not blocked by someone else's failures. Beyond
      the limit it is 429 `rate_limited` with `Retry-After`. The current
      behavior is to be changed to match this.
    - Lockout (off by default): after the limit is reached, lock for a period
      (default 15 minutes), with a scope that can be chosen: "IP + user name"
      (default, recommended), "IP only", "user name only" (which lets others lock
      a given account, so the UI must warn).
    - Counts are in memory (single instance) and reset on restart; lockouts and
      policy changes are written to the audit log.
  - **The admin console "Security" page**: sign-in (sign-in mode, refuse
    password sign-in, with a hint when there are no providers), CAPTCHA
    (provider, secrets, per-place switches, trigger mode and count), lockout
    (switch, duration, scope), limits (requests per minute, failure limit, time
    window, session duration). These settings are stored in the state database
    and take effect at once when changed, with no restart; the OIDC that can
    currently be configured only through command-line flags is moved here too.
  - Single-user mode has no sign-in, so none of the above applies.

**The 2.8 contract** (2026-11-23): the API and error codes are written in
`docs/api.md` under "Signing in", "Account", "Users and invites", and, within
"Administration", "Security settings" and "Sign-in providers"; the front end and
back end are built from it.

Settled:

- **The identity key does not change**: it is still "issuer + subject" (OIDC's
  issuer + `sub`, SAML's IdP entity ID + `NameID`, GitHub's address + numeric
  id, never a changeable login name). So the existing `identities` table needs
  no migration, and after the OIDC provider configured on the command line is
  moved into the database, the identities everyone already bound still work.
  Two providers cannot use the same issuer (`provider_issuer_in_use`), and an
  identity belongs to only one provider; delete one and add it back with the
  same issuer, and the identities come back too.
- **The old API is kept**: `POST /api/auth/oidc/start` and
  `GET /api/auth/oidc/callback` are unchanged and serve only the one OIDC
  provider marked `legacyCallback`; its callback address is still the old one,
  so nothing needs to change on the IdP side. At the first start of 2.8, if
  `--oidc-*` is given and nothing has ever been imported into the database, a
  provider with slug `sso` is created (secret stored encrypted, `legacyCallback`
  on, automatic account creation following `--oidc-auto-create`), and the
  sign-in mode stays `dual` with `sso_only` off, so nobody is locked out. After
  that the database is authoritative: if the flags and the database disagree
  (or that provider was deleted), only a log line and a `flags_ignored` notice
  on the security page appear, the flags no longer take effect, and a later
  version removes them.
- **Two ways to start a sign-in**: the buttons on the sign-in page navigate the
  whole page to `GET /api/auth/{oidc|saml|github}/{slug}/start` (for sign-in
  only, the cross-site rule unchanged); first-time setup, accepting an
  invitation and linking an identity must carry a token or a session, and use
  `POST …/start` to get the address (the same as the pre-2.8
  `POST /api/auth/oidc/start`), so the token does not go into the address bar.
- **SAML**: the ACS receives a form that the IdP submits from another site, and
  a `Lax` cookie is not sent along, so this sign-in's cookie is
  `__Secure-petalbook_saml_<slug>`, `SameSite=None`, limited to the ACS path,
  and SAML therefore requires a public `https` address. The ACS is not subject
  to the "JSON only" and cross-site rules, and prevents forgery by requiring
  `RelayState` to equal the cookie and `InResponseTo` to match this sign-in.
  Transient `NameID` is rejected. IdP-initiated sign-in is off by default.
- **The GitHub adapter goes into 2.8**: the 2.8 plan already had it, and it does
  not depend on 2.7 either (it is only an OAuth authorization code exchange plus
  reading `/user`, `/user/emails` and `/user/orgs`, and the user token is used
  only within that one request); if 2.7 is merged first, it shares 2.7's HTTP
  client. When a GitHub provider turns on automatic account creation, an
  organization must be specified, otherwise anyone with a GitHub account could
  create an account.
- **Email merging**: by default, never merge by email. When a provider has
  `mergeVerifiedEmail` on, merging into the one matching account happens only
  if the provider declares the email verified (OIDC's `email_verified`, GitHub's
  verified primary email; SAML has no such claim, so turning it on amounts to
  the administrator vouching for every email the IdP gives), **and the local
  account's email is also verified** (from a provider that was vouched for, or
  set and confirmed by an administrator; the user changing their own email
  clears this mark); it never merges into an administrator account, a disabled
  account, or an account that already has an identity from the same provider;
  when two accounts match, it answers `no_account`. Otherwise someone could
  change their own account's email to somebody else's, and when that person
  first signs in with SSO they would land in the other person's account.
- **`sso_only`**: once on, non-administrators are refused for both password
  **and passkey** (`local_login_refused`, decided after the password is
  checked), and a password cannot be used when accepting a `member` invitation
  either (otherwise the account created could never sign in). It takes effect
  only when the sign-in page actually shows an SSO button (following Report
  Portal: under `local_only` the buttons are hidden, and refusing passwords
  too would lock the door). While it is on, changing a role or disabling someone
  so that there is no "enabled administrator with a password" is also refused
  (409).
- **passkey**: both local accounts and external accounts can use it, and it
  signs in by itself (it is not a second factor), so user verification (PIN or
  biometrics) is required at sign-in, and discoverable credentials are
  preferred at registration. The user handle is just the user id. At most 20 per
  person. Registration requires a sign-in within the last 10 minutes (following
  Rackbook). When starting by user name, for a user who does not exist or has no
  passkey it returns a fake credential id computed from the user name and the
  server key, looking the same as for a person who has one passkey. A counter
  that goes backward (suspected copy) is rejected outright, the stored count is
  not updated, and it is written to the audit log. The host name of the public
  address is the RP ID; after the host name changes, all the old passkeys stop
  working, and a notice is shown at start-up.
- **Requiring a passkey**: can be by person (`passkeyRequired` of
  `PATCH /api/users/{id}`) or by role (`passkeys.requireRoles`). For such a
  person who has a passkey, even a correct password answers
  `403 passkey_required` and the sign-in page has them use a passkey; for one
  with no passkey yet, password sign-in leads to a held session (`hold:
  "passkey"` in `GET /api/session`) that can only register a passkey and sign
  out, all other requests are `403 passkey_required`, and it is released
  immediately after the first one is registered. Signing in through SSO is not
  held (the IdP decides how to verify). If the passkey is lost, an administrator
  deletes it, and the next password sign-in is held again to register a new
  one.
- **CAPTCHA**: of the four providers, the self-hosted image keeps traffic inside
  and is the default. The contexts are sign-in, accepting an invitation (both
  checking and accepting count) and first-time setup; `forgot` and `register`
  just reserve the names and always answer `required: false`. Each place has its
  own switch and chooses "every time" or "after N wrong attempts" (the latter by
  default, 3), counted per IP, and for sign-in per user name too. All are off by
  default (both reference projects are off by default).
- **Rate limiting and failure limits**: the existing `failLimiter` (free tries
  followed by doubling waits) and the 120 per minute used for first-time setup
  and invitations are replaced by the new approach: 10 per minute per IP, with
  one quota shared by sign-in, first-time setup, checking and accepting
  invitations, fetching a CAPTCHA, passkey start and finish, and the SAML ACS;
  failures are counted in three separate counts by IP, user name and IP+user
  name, with a fixed window counted from the first failure; the start of SSO
  keeps its own 30 per minute. The order of decisions at sign-in: per-minute
  count → CAPTCHA → failures per IP → lockout → password hash → only after a
  wrong password look at the failures per user name and per combination →
  `sso_only` → passkey required. A wrong first-time setup token or invitation
  token counts as a failure for the IP.
- **Memory is capped**: the per-minute counts and the three failure tables each
  have at most 10,000 keys, dropping the longest-unseen first (locked ones
  last); CAPTCHA challenges and passkey ceremonies each at most 10,000 and 5
  minutes; SSO sign-ins in progress 10,000 and 10 minutes; SAML used assertion
  ids at most 65,536. All of it is in one process and resets on restart.
- **Settings and providers are cached in memory**: read once at start-up, and
  re-read at once when an administrator changes them, rebuilding only the
  provider that changed; changes made from the command line
  (`petalbook security reset`) take effect within 5 minutes at the latest (the
  revision number is checked every 5 minutes, not on every request).
  `GET /api/auth/providers` does not query the database. OIDC discovery
  documents and SAML metadata are stored in the database and also kept in
  memory, and are not fetched at sign-in; metadata configured by address is
  re-fetched in the background every 24 hours.
- **Queries per sign-in**: a passkey sign-in is one query (by credential id)
  plus one write (the counter and the last-used time), with the user coming from
  the session cache; SAML validation does not query the database (metadata and
  the replay cache are in memory), and then it is one identity query, like OIDC.
  Whether a passkey is required and whether one exists travels with the cached
  user, so a held session needs no database query per request.
- **The audit log is new** (there was none before): one table in the state
  database, recording sign-in successes and failures (unknown user names too),
  limit hits (once per key per window), lockouts, counter regressions, identity
  binding and unbinding (noting whether by the user, email match or an
  administrator), passkey addition and deletion, changes to security settings
  and providers (only the changed fields, with secrets written only as
  `"changed"`), and administrators changing users. It is written in batches in
  the background; the queue holds at most 10,000, and when full it drops and
  records one `dropped`; it keeps 100,000 entries or 90 days.
  `GET /api/admin/audit` reads it in pages.
- **Checks on save**: `sso_only` is rejected when there is no enabled
  administrator with a password (`local_admin_required`), and rejected when
  registration is open (there is no registration yet, so the check is kept in
  advance); a third-party CAPTCHA needs the site key and secret (switching
  provider requires the new secret in the same request, and the old one is not
  carried over), and also needs `--public-url` (used to check the host name);
  when passkeys cannot be used, requiring a passkey is not allowed, and when
  someone is required to have one, passkeys cannot be turned off. All fields
  are validated first and then stored, and out-of-range values are rejected
  rather than clamped to the boundary.
- **The emergency command** `petalbook security reset --data DIR`: sets the
  sign-in mode back to `dual`, turns off `sso_only`, lockout and all CAPTCHAs,
  and clears the per-role passkey requirements; providers and people are not
  touched.
- JSON fields use camelCase following the convention of `docs/api.md`
  (`loginMode`, `ssoOnly`), while the values are still `dual`, `sso_first` and
  so on; the SSO failure parameter changes from `error=` to `sso_error=`, and a
  failed link returns to `/settings?sso_error=`.

Copied:

- **Report Portal**: the sign-in mode and `sso_only` split into two axes, with
  their degradation rules (no provider means `local_only`, `sso_only` has no
  effect, administrators are not restricted); the two save checks
  `local_admin_required` and `sso_only_with_registration`; the shape of
  `GET /api/auth/providers`; icons can only be built-in ones, not external
  addresses; the paths `/api/auth/{oidc|saml}/{slug}/…`; the `sso_error` codes
  (including the `saml_*` group); SAML's `SameSite=None` cookie limited to the
  ACS path and rejection of transient `NameID`; the shape of
  `GET /api/captcha`, `captcha_failed` answering 400, the CAPTCHA checked before
  the hash, errors treated as failures, host name checking, secrets not carried
  across providers; failures counted in three counts by IP, user name and
  combination, with the IP one before the hash and the user name one after the
  password is wrong; the three lockout scopes and the warning for "user name
  only"; unknown user names also audited; a passkey counter regression rejected
  without writing back; for "required but none yet", the session is held and
  only a fixed route table is let through, and for "required but impossible to
  satisfy", the save is rejected; partial updates, and validating everything
  first before storing.
- **Passwall-Sub-Panel**: the per-IP requests-per-minute limit, shared by the
  sign-in related APIs (including fetching a CAPTCHA, passkey and the SAML ACS),
  default 10, taking effect at once when changed. Also `Retry-After` was added,
  which it does not have.
- **Rackbook**: the passkey ceremony id goes in a query parameter, and at
  completion the request body is just the credential from the browser;
  discoverable credentials and user verification are required (passwordless
  sign-in); a sign-in within the last 10 minutes is needed before registering;
  authcore's own bounded in-memory ceremony store is used. Rackbook has no SAML
  wired in, and for SAML only Report Portal is the reference.
- Where the two projects disagree, follow Report Portal: counts in memory
  (Passwall queries the audit event table on every sign-in); lockout reuses the
  failure limit (Passwall has another threshold); CAPTCHA switches per place
  (Passwall has only one switch); when something is required but there is no
  available way, do not let it through (Passwall opens up automatically). Where
  it differs from Report Portal, follow what the owner decided above in this
  text: the CAPTCHA can be set to "after N wrong attempts" at every place
  (Report Portal allows that only for sign-in); lockout is 15 minutes by default
  (Report Portal's is 30).

Not doing:

- TOTP and other second factors (a passkey here is an independent sign-in
  method); SCIM; SAML single logout, encrypted assertions, an API for rotating
  the signing key; assigning roles by the IdP's groups or attributes (groups of
  phase 3); a session duration per provider; reCAPTCHA v3 scores; OIDC
  multi-tenant `common` issuers (a specific tenant must be filled in); sharing
  counts across several instances; password recovery and public sign-up
  themselves (only the context names are reserved).

Pending the owner's confirmation (in brackets is the default now followed):

1. When `sso_only` is on, are passkey sign-ins by non-administrators also
   refused? (Refused: `sso_only` means the IdP decides who gets in, and a
   passkey would bypass a deactivation on the IdP side.)
2. Is a person required to have a passkey also held when signing in through SSO?
   (Not held; both reference projects govern only local password accounts.)
3. With 10 per minute per IP shared by these APIs, would it be too tight when an
   office shares one exit address? A sign-in is two requests, fetching a CAPTCHA
   plus the submission. (10 as designed; `--trusted-proxy` must be configured
   correctly.)
4. CAPTCHA off by default at every place? (Off, as in both reference projects;
   once on, by default it is required only after 3 wrong attempts.)
5. Put GitHub sign-in into 2.8? (Yes; automatic account creation must specify an
   organization.)
6. Email merging also requires the local account's email to be verified, and
   never merges into an administrator account? (Required; so for an existing
   account to be merged, an administrator must confirm the email in user
   management.)
7. After `--oidc-*` is imported once, the database is authoritative, and the
   flags only warn and do not take effect? (Yes, and a later version removes the
   flags.)
8. When passkey sign-in is started by user name, the number of passkeys a
   person with several has can be told; is that acceptable? (Acceptable; if not,
   keep only the user-name-free way.)
9. Audit log keeps 100,000 entries or 90 days? (Yes.)
10. Use `loginMode` and `ssoOnly` in JSON rather than the `login_mode` written in
    the design? (Use camelCase, consistent with the other APIs.)

**2.8 as built (2026-11-23)**: everything in the plan above is done; the API is
in `docs/api.md` under "Signing in", "Account", "Users and invites" and
"Administration". All ten items under "Pending the owner's confirmation" were
done with the defaults in brackets. What was settled during implementation:

- **Libraries**: passkey, SAML, CAPTCHA and OIDC all use authcore's `passkey`,
  `saml`, `captcha` and `oidc`, with the version unchanged (`f3886de`).
  authcore's `saml` requires the user to repeat, in its own `go.mod`, its
  `replace` for `crewjam/saml` (swapped for a pinned commit of
  `openctemio/saml`), which has been added; the virtual authenticator in tests
  is `descope/virtualwebauthn`, and the fake IdP is `crewjam/saml`'s
  `IdentityProvider`.
- **Storage**: migration `0014_signin.sql` (main goes up to 0010, `feat/mcp`
  uses 0011 and 0012, `feat/github-app` uses 0013). New tables
  `sign_in_providers` (secrets encrypted with `secret.Box`, discovery documents
  and metadata in the `cache` column), `passkeys` (credential JSON, `rp_id`,
  `synced`) and `audit_log`; `users` gains `email_verified` and
  `passkey_required`, and `sessions` gains `held`. The security settings are one
  row in `site_settings` with the key `security`, whose `updated` is the
  revision number: after a command-line change, the running service reads it at
  the latest after `CacheTTL` (5 minutes), and each request only compares the
  time in memory, with no database query. The migration-count test was changed
  to count files.
- **Old flags**: at the first start, `--oidc-*` creates a provider with slug
  `sso` (`legacyCallback` on, enabled, `autoCreate` following the flag) and
  records an `oidc_imported` row in `site_settings`. Afterwards, if the flags
  disagree with the database, or that provider was deleted, it only writes a log
  line and shows a `flags_ignored` notice on the security page; once deleted,
  starting again with the flags does not re-create it. The identities table was
  not changed, and the identities bound before still work. The sign-in mode
  stays `dual` and `ssoOnly` off, so nobody is locked out.
- **The order of decisions at sign-in** follows the contract: per-minute count,
  CAPTCHA, failures per IP, lockout, password hash, only after a wrong password
  record the user name and combination failures, `sso_only`, passkey required.
  The IP one is "reserve a slot first, give it back on success" (as before), so
  for a batch of wrong passwords arriving at once, at most the limit number get
  to the hash. A wrong CAPTCHA, a required passkey and `sso_only` do not count as
  a wrong password. A locked request is recorded in the audit log as
  `login_failed` with reason `rate_limited`; the lockout itself is recorded as
  `lockout`. Failures that anyone can fabricate are kept from flooding the audit
  log: wrong CAPTCHA answers, and addresses that are locked or at the limit, are
  recorded at most one row per address (IPv6 by /64) per reason per minute;
  passkey ceremonies that never started and SSO callbacks matching no sign-in
  are not recorded.
- **SAML**: the metadata gives only the signing certificate and not an
  encryption certificate, because we reject encrypted assertions, and an IdP
  that sees an encryption certificate would encrypt. When linking (`link`), what
  the ACS receives is a POST from another site, the session cookie (`Lax`) is
  not sent along, so who it is is decided by the user id in the record of the
  attempt that started it, and only a signed-in person can create that record.
  The attempt's cookie is `__Secure-`, which sibling subdomains can also plant,
  so for linking a second cookie that belongs only to this host,
  `__Host-petalbook_saml_link_<slug>` (`SameSite=None; Secure; Path=/`), is sent
  as well, its hash is stored in the record, and the link happens only if the
  ACS matches it. Metadata configured by address is re-fetched in the background
  every 24 hours (counted from start-up), and when it cannot be fetched, the
  last good copy in the database continues to be used.
- **passkey**: the ceremony started by user name is kept in memory (5 minutes and
  at most ten thousand, like ceremonies), and only at the end is it known which
  user it was; the fake credential id is computed with HMAC, and the key is
  generated randomly at first use and stored encrypted in `site_settings`,
  unchanged after restart. A counter regression is rejected by the credential
  store's write-back, writing the audit entry `passkey_clone`, and the stored
  count does not move. Each passkey remembers the host at registration
  (`rp_id`); after the host name changes, the security page shows
  `passkey_host_changed`, and a log line is written at start-up too.
- **A user's `passkeyRequired`**: in the response it is the effective value (the
  person is required, or their role is required), and what `PATCH` changes is
  only the person's own mark.
- **GitHub**: uses the authorization code plus PKCE; for organizations it looks
  only at the first 500 of `/user/orgs` (5 pages).
- **Creating a provider**: if it can be reached, it follows `enabled` in the
  request (on by default); if it cannot be reached, it is stored as the contract
  says, off by default. Two providers cannot use the same issuer; the issuer of
  SAML is known only after the metadata is fetched, so the check is made then.
- **Memory**: per-minute counts and the three failure tables each at ten thousand
  keys (locked ones dropped last); CAPTCHA challenges and passkey ceremonies each
  ten thousand and 5 minutes (authcore's own bounded store); SSO sign-ins in
  progress ten thousand and 10 minutes; SAML used assertion ids 65,536; the audit
  queue ten thousand, and when full it drops and writes one `dropped` row in the
  next batch.

Tests: every flow runs on both SQLite and PostgreSQL
(`scripts/test-postgres.sh`), with a fake OIDC provider, a fake SAML IdP, a fake
GitHub, a fake siteverify and a virtual authenticator. Coverage: upgrading from
the old flags without locking anyone out, the save checks and the various
refusals of `sso_only`, the CAPTCHA before the hash, the order of the limits and
the three lockout scopes, SAML attack cases (wrong RelayState or cookie, another
`Destination`, unsigned or altered, SHA-1, two or encrypted assertions,
duplicate attributes, transient or missing `NameID`, replay, IdP-initiated),
every exclusion condition of email merging, holding and releasing for required
passkeys, and the discovery document not being fetched at sign-in.

**2.8 known issues**

- WebAuthn in a real browser and the real scripts of third-party CAPTCHAs have
  not been tested, only the virtual authenticator and a fake siteverify.
- The `sso_only_with_registration` save check is kept in advance; public
  registration does not exist yet.
- SAML signing keys cannot be rotated (the contract was not going to do it
  anyway); when the certificate expires after 10 years the provider must be
  rebuilt.
- After an OIDC provider's discovery document is stored in the database it does
  not update by itself; click "Refresh".
- The answer of `GET /api/captcha?account=` shows whether a given user name has
  recently been wrong several times (whether a CAPTCHA is needed is decided by
  the failure count per user name). It reveals little, and is accepted.
- When `ssoOnly` is on and a new provider that would be enabled is created
  (`enabled` not given or `true`) and there is no enabled administrator with a
  password, it is rejected directly with `400 local_admin_required` rather than
  first being stored as disabled. The case of enabling an existing provider with
  `PATCH` has a test; this creation case does not.
- Decided when merging main (2.6): a held session (one that must register a
  passkey first) cannot create an API token or consent on the authorization page
  (both `403 passkey_required`); tokens created earlier and OAuth grants are not
  affected by the hold and work as usual, and to stop them, revoke them (see
  "Held sessions, API tokens and OAuth grants" in `docs/api.md`). This way a
  token request does not need to look up passkeys every time. The audit log
  gained `token_created`, `token_revoked` and `grant_revoked`; an OAuth grant is
  created only when the token is exchanged (`/oauth/token`), which is not at the
  API layer, so `grant_created` is left for later.
- Decided when merging with 2.7: a held session cannot touch any GitHub API
  (viewing, connecting, disconnecting, listing owners and repositories, GitHub's
  callback, creating a space with a repository, all `403 passkey_required`), and
  GitHub accounts connected before the hold are not affected. GitHub sign-in
  (`/api/auth/github/{slug}/…`) and connecting a GitHub account for repositories
  (`/api/connections/github/…`) are also listed separately in the cross-site
  exceptions. The three audit event kinds (`connection_provider_changed`,
  `account_connected`, `account_disconnected`) were added in 2.7b, together with
  the front end's list of kinds, the text, the mock server and the table in the
  API document.
- passkey and single sign-on hand the return address (`return`) to the server,
  which accepts only up to 2048 bytes, and anything longer goes back to the home
  page (password sign-in redirects within the page and has no such limit). The
  address of `/oauth/authorize` is usually much shorter, but when a client uses
  a very long `client_id` (a metadata document address) and `state` it may
  exceed that, and then after sign-in it will not return to the authorization
  flow, and the client has to start again.

**The 2.7b contract (2026-11-23)**: general connections, and "Storage & sync".
The API and error codes are written in the "Connections" section of
`docs/api.md` (the "GitHub" section of 2.7 was renamed "The GitHub provider",
and the administrator part is under "Connections" in "Administration"). Decided
by the owner on 2026-11-23: this is a milestone after the 2.7 fixes and before
the 2.7 merge, done on the 2.7 branch and merged together with 2.7 (as the last
part of the PR of 2.7, reviewed once separately on the branch). The 3.1 contract
is in `feat/storage`; the routes, error codes and `github` field there are
renamed according to the mapping below.

Settled:

- **Naming: "Storage & sync"** (decided by the owner, 2026-11-23). The UI and
  the documents no longer have "code hosting" or a separate "GitHub" section:
  - The admin page "Storage & sync" merges 3.1's "Storage" page and 2.7's
    "GitHub" admin page into one page: the upper half is the rules for "where it
    is stored" (3.1's require-remote, grace period, the cap for local-only
    spaces, allowed hosts; 3.5's site storage budget and storage pressure, see
    "the 3.5 contract" in [phase-3-storage.md](phase-3-storage.md)); the lower
    half is "Connections": one card per platform (brand icon, whether it is
    configured, whether partial sync is supported, GitHub's App and installation
    list, later GitLab/Gitea OAuth applications). Whichever of 2.7 and 3.1 is
    merged later is responsible for merging the two pages.
  - The row on the account page is called "Connected accounts", with one line per
    platform (one for GitHub, later one each for GitLab and so on), and 2.7's
    "empty repository reminder" goes under the line of the matching platform.
  - The choice when creating a space is called "Where it is stored": This
    server / Sync with GitHub / Sync with GitLab / … (listing only the platforms
    configured site-wide that this person can use), and last "Other (by
    address)".
  - The UI does not say Git and repository to ordinary users: it says "Sync with
    GitHub", "Where it is on GitHub" and "Something went wrong with syncing". The
    admin page and the advanced option "Other (by address)" say Git, repository
    and deploy key as they are.
- **One provider interface in the back end**: a provider describes itself (type,
  host, display name, capabilities), connects a person (start authorization,
  callback, renewal, disconnect), lists the places where repositories can be
  created and the existing repositories, creates a repository, gives the
  credential for Git, and checks its own configuration. The providers in the
  server are one table in memory, looked up by id.
  - **GitHub is the first implementation**, and all the behavior of 2.7 stays:
    creating the App in one click with the App Manifest, installation and
    authorization done in one GitHub flow, organization installations shared per
    tenant, tokens narrowed to a single repository. `internal/githubapp` is not
    touched, and only a layer implementing this interface is added on top of it.
  - **GitLab** (gitlab.com and self-hosted) and **Gitea/Forgejo/Codeberg** are
    done in their own milestone, 3.8: one OAuth application per host, configured
    by an administrator, and users connect once.
  - **Plain Git by address** (deploy key or token) is kept, as a provider of type
    `git`: there is no "connection", and the credential is still one per space.
  - Capabilities (`capabilities`) are one set per provider: whether it can create
    repositories, whether it has installations (only GitHub), whether personal
    accounts can be connected, whether it can serve as a tenant's storage target,
    whether partial sync is supported (detected automatically from 3.5, see
    below). The UI shows options only by capability, and does not hard-code
    platform names.
- **The API is general** (decided by the owner, 2026-11-23: not yet released,
  choose the clean general shape, leave no aliases):
  - `GET /api/connections` lists the providers (type, capabilities, this person's
    connection state, empty repository reminders); 2.7's `/api/github/…` becomes
    `/api/connections/github/…`, `/api/admin/github…` becomes
    `/api/admin/connections/github…`; the tenant's (3.6) becomes
    `/api/admin/tenants/{tenant}/connections/{provider}…`.
  - The `github` of `POST /api/spaces` becomes `storage.sync`: `{provider, owner,
    name, private, create, assets}`; without it, it means "this server".
  - The Remote credential `type: "github"` becomes `type: "connection"`, with
    `provider` and `via` (`app`, `user`, with `tenant` added in 3.6).
  - Error codes drop the `github_` prefix and carry `provider`: for example
    `github_authorization_expired` becomes `connection_expired`,
    `github_not_configured` becomes `provider_not_configured`, and
    `github_repo_not_in_installation` becomes `repo_not_in_installation`. The
    mapping table is in `docs/api.md`.
  - 3.1's `remotes.require` takes `null`, `"any"` or a provider id (replacing
    `"github"`); 3.1's `github.assets` becomes `storage.sync.assets`.
- **3.6's tenant storage targets use the same interface**: a storage target is
  "one provider + one place (GitHub organization, GitLab group, Gitea
  organization) + the tenant's credential". GitLab/Gitea targets have to wait
  for 3.8. 3.6's capability `integrations.github` is renamed
  `integrations.connections`, and the layered key `github.useSiteApp` is renamed
  `connections.useSiteApps`.
- **State database**: GitHub's own tables (`github_authorizations`,
  `github_installations`, `github_installation_tenants`) keep their names and
  belong to the GitHub implementation; `cred_type` of `remotes` takes `ssh`,
  `token` and `connection`, with one more column `provider`. The migration uses
  the next free number at merge time, tested on both SQLite and PostgreSQL;
  v0.x does no upgrade compatibility: development databases created before 2.7b
  must be deleted and rebuilt, and the migration does not convert old rows in it
  (for example `cred_type = 'github'`).
- **Tests**: the provider interface has one shared contract test (the fake
  provider runs through it, the GitHub implementation runs through it, and the
  two implementations of 3.8 will run this suite later too).

Not doing in 2.7b: the GitLab and Gitea implementations (3.8); brand icons other
than GitHub's (3.9; GitHub's icon is done in 2.7b, inlined into the web page
with the CC0 paths of Simple Icons, with no external network connection);
automatic discovery of providers (administrators add them by hand).

Decided by the owner on 2026-11-23:

1. "Storage & sync" replaces "code hosting" and the separate "GitHub": one page
   in the admin console, "Connected accounts" on the account page, "Where it is
   stored" for a new space; ordinary users are not told about Git and
   repositories.
2. One provider interface in the back end, with GitHub (2.7's App) as the first
   implementation; GitLab and Gitea/Forgejo/Codeberg in their own milestone
   (3.8); plain Git by address is kept; the API is general, GitHub-specific
   routes become per-provider ones with no aliases; 3.6's tenant storage targets
   use the same interface.

**2.7b: what was built**:

- **The interface is in `internal/connections`**: the `Provider` interface, the
  `Registry` (looked up by id, one table in memory), the general `Error` (with
  `Provider`) and one shared contract test (`connections/conntest`: the fake
  provider runs through it, the GitHub implementation runs through it). `git`
  (plain Git by address) is a provider with no connection, and `repos` returns
  an empty list. The GitHub implementation is the `Service` of `internal/github`,
  a layer added on `internal/githubapp`, with behavior unchanged.
- **Web**: the admin page "Storage & sync" (`/admin/storage`) holds the
  connections, one card per platform, with 3.1's storage rules and 3.5's budget
  to be placed above the cards later; the account page "Connected accounts"
  (`/settings/connections`); "Where it is stored" when creating a space. The
  words Git and repository do not appear in the ordinary user's UI; GitHub's
  icon is a Simple Icons path, inlined in the web page.
- **Migration**: 0013 has not been merged yet, so it is changed in place:
  `remotes` gains `provider`, and the value of `cred_type` is `connection`.
  Development databases that have already run the old 0013 must be deleted and
  rebuilt (v0.x does no upgrade compatibility).
- **Audit**: added `connection_provider_changed`, `account_connected` and
  `account_disconnected`.
- **Five problems from the 2.7 review were fixed on the way**:
  1. When creating the App in one click, GitHub has already handed out the App
     but saving fails (GitHub cannot be reached): it no longer says "nothing
     was saved". The handed-out key and secret stay in memory encrypted for ten
     minutes, and the administrator can click "Try saving again"
     (`POST /api/admin/connections/github/manifest/retry`); once expired, the
     administrator is told that the App is already on GitHub, with its name and
     address, to fill in by hand.
  2. When the App is automatically re-checked, it writes back only if the record
     in the database is still the one that was read (compare and swap), so it
     will not overwrite a new App that was just saved.
  3. The result "this person has not installed the App" is cached for one
     minute, so listing owners does not use up the App's rate limit, and the log
     is not written every time.
  4. A user token refused when creating a repository under an organization where
     the App is not installed: answers `app_not_installed` (with `installUrl`) or
     `provider_refused`, no longer `app_missing_permission`.
  5. Attempts to create the App in one click have their own small table (at most
     64, the earliest going first when full), and attempts to connect accounts
     cannot push it out.

**Outside phase 2, but to remember after 1.7**

- The known issues of 1.7 (see "1.7 as built" in [phase-0-1.md](phase-0-1.md)), each to be handled when
  the related code is touched.
- The four steps of "does not fit locally" in 4.5 ([phase-4.md](phase-4.md)),
  together with "force following the remote" and "separate attachment
  repository", are milestone 3.1 at the start of phase 3 (see below).
