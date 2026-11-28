# Milestones: phase 3, tenant autonomy, providers and icons (3.6 to 3.9)

**The 3.6 contract** (2026-11-23): the endpoints and error codes are written in
the new "Tenant autonomy" section of `docs/api.md`. It finishes off the
permission model: everything under (b) in the list above is done as layers,
everything under (c) belongs to the tenant, and which of them a tenant can use
is decided by its capabilities. The owner added several more things on the same
day: self-service sign-up for organizations, separating social sign-in from SSO,
finding the SSO entry point by email domain, per-tenant capabilities and plans,
storage modes, and the site admin being able to see and change any tenant's
configuration. All of them are folded in here.

Decided:

- **Per-tenant capabilities** (decided by the owner 2026-11-23; capabilities are
  the switches in the "limits table" below; for billing and payment see "The
  billing boundary", neither is done):
  - A set of capabilities = the site default + a per-tenant override. Only the
    site admin can change them; the tenant admin can only view them. At least
    these exist (key names and defaults are in `docs/api.md`):
    - Sign-in: the tenant's own SSO providers (`signin.sso`), domain
      verification (`signin.domains`), managed accounts
      (`signin.managedAccounts`, 3.7), the choice of social sign-in methods
      (`signin.socialChoice`), IdP group mapping to organization admin
      (`signin.idpAdmins`, formerly the site-wide switch `auth.idpAdmins`);
    - Data: **letting tenant admins view and export the contents of organization
      spaces** (`data.adminView`, the viewing and export of 3.5; personal spaces
      are never visible, per 3.7), downloading the audit log
      (`data.auditDownload`);
    - Integrations: its own SMTP, captcha keys, connections (GitHub App etc.,
      2.7b), the daily text endpoint (`integrations.smtp`, `.captcha`,
      `.connections`, `.daily`); API tokens and MCP (`integrations.tokens`,
      `.mcp`);
    - Storage: the allowed storage modes (`storage.modes`, see below), the
      per-file ceiling (`storage.maxFileBytes`), the tenant total
      (`storage.quota`; the tenant `quota` of 3.3 moves here), the ceiling on
      per-person quota (`storage.perPerson`), the pinned allowance
      (`storage.pinnedBytes`);
    - Other: offline copies (`other.offline`, 4.1), open sign-up into this tenant
      (`other.registration`), the cap on people (`other.people`).
  - **Capabilities decide "whether it exists", layers decide "how much"**: a
    capability decides whether an item under (c), or the tenant layer of an item
    under (b), exists in this tenant. When it exists, its value is still decided
    by the layers as before. Every item under (b) and (c) in the list is marked
    with its capability.
  - When a capability is off: writes to this tenant's related settings and
    endpoints answer `403 capability_off`, with the capability's key name. Reads
    still answer, with an extra `capability: {key, on: false}`; the UI greys them
    out, with the note "Needs to be turned on site-wide" next to them. What is
    stored (providers, domains, SMTP, tenant-layer values) is **not deleted**, it
    just has no effect; turning the capability on again restores it as it was.
    Storage is the exception: existing spaces carry on using the mode and storage
    target they have now, and only new spaces can no longer use it (see "Storage
    modes" and problem 2 below).
    The site admin is not bound by capabilities, and can set things up for a
    tenant in advance even while a capability is off.
  - Turning off a capability that is in use (there are providers, there are
    domains, spaces using the storage mode, accounts being managed) first shows a
    preview (what will stop, how many people or spaces are affected), then uses
    the second confirmation of 3.5. If it is not in use, the change is made
    directly.
    **Known gap after 3.6 part 4:** the preview and confirmation are not built
    for `sync.assetsRepository` (spaces with an attachment repository keep it;
    only new ones are refused) or `storage.modes` (no space records its mode, so
    there is no count). They arrive with the first thing that can be counted,
    with `change_capabilities` in `POST /api/admin/confirm`.
  - **Every site-wide setting can be "default" or "forced"** (a general rule the
    owner restated on 2026-11-23): each site-wide setting supports both uses: only
    giving a default (`allow`, which the layers below can change), limiting the
    range (`restrict`), forcing (`lock`, which the layers below cannot change).
    No site-wide setting is "default only" or "forced only". When a new setting
    is registered it must state whether it exists at the tenant layer and the
    space layer, and which side "stricter" means. A setting that exists only at
    the site layer (such as the storage budget or tiers) has no lower layer as
    before, and so is naturally forced.
  - **Feature switches** (the owner, 2026-11-23: for example when listing in
    mainland China later, sharing and other features will have to be turned off,
    so make it general):
    - Every user-facing feature registers a switch key `features.*`, for example
      `features.share.links` (share links), `features.share.public` (public
      publishing of [reserved.md §14](../reserved.md#14-reserved-public-publishing)), `features.invite.external` (inviting people outside the
      organization), `features.signin.<provider>` (each social sign-in),
      `features.connections.<provider>` (each storage and sync provider),
      `features.daily`, `features.mcp`, `features.export`, and later
      collaboration and AI and so on. The registry states which endpoints and
      UI each key governs, and a new feature registers its switch at the same
      time it adds endpoints (the same practice as the permission keys of
      [auth-and-permissions.md §16](../auth-and-permissions.md#16-reserved-custom-roles-and-permissions); a test checks that every user-facing route hangs
      off some switch or is marked "always on").
    - **Layering as before**: site -> tenant, using the `allow` / `restrict` /
      `lock` of 3.3 plus the `tighten` above. A site `lock` set to off is a
      **forced off**, and the tenant cannot change it. When the site only gives a
      default, the tenant can turn it on or off itself within the allowed range.
      The default is everything on (except features that are not finished yet).
    - **When off**: the UI does not show the entry point (it is not greyed out;
      users do not need to know the feature exists), and the endpoint answers
      `403 feature_off` (with the key name). The parts of a read that are not
      affected answer as usual. What already exists (share links already sent,
      accounts already connected) is **not deleted**, it just becomes invalid, and
      turning the switch back on restores it. Turning off a switch that is in use
      first shows a preview (how many links, how many people are affected), then
      uses the second confirmation of 3.5.
    - **Sharing modes** (the owner, 2026-11-23): sharing is not just on or off;
      it has tiers by mode, from broad to strict: `public` (anyone with the link
      can view, no sign-in), `people` (shared only with specified emails or
      accounts, who must sign in and whose identity must match before they can
      view), and `off`. The site can give only a default, or limit the allowed
      tiers (`restrict`, for example allowing only `people`), or force one tier
      (`lock`). A tenant sets its own default or forced tier within what the site
      allows. The person sharing can only choose among the tiers in effect. When
      things are tightened, shares already sent that are broader than the new
      setting become invalid (not deleted, and restored if loosened again), with
      a preview of how many are affected first, then the second confirmation. A
      `people` share sent to an email that has no account yet requires the
      recipient to sign up or sign in with that email before viewing, and is
      bound by the sign-up switch and the joining rules.
    - **Region presets**: the site can choose a named preset (`site.profile`, for
      example `cn`) that applies a group of switch values and `lock`s in one go.
      Individual items can still be adjusted afterwards. A preset is only a list
      of values, not a branch in the code, and the code recognizes only the
      switch keys.
    - **The set of effective switches** is sent to the frontend with
      `GET /api/meta` (per tenant after sign-in). The frontend shows entry points
      according to it, and the app uses it too. On the API side, the server's own
      decision is what counts.
    - **What listing in mainland China also needs** (a contract will be written
      then; only noted here): usually a separate deployment (servers in China, a
      separate domain, ICP filing), with data not leaving the country (the
      Personal Information Protection Law); public sharing needs content review
      or must be turned off; sign-in and storage with Google, GitHub and so on are
      unreliable inside China, so they are replaced with providers that work
      there (WeChat, phone number and so on). This is exactly what
      `features.signin.*` / `features.connections.*` and the `cn` preset are used
      to turn off and to swap.
  - **Plans**: "a named set of capabilities and limits", which belongs to the
    internal billing module (see "The billing boundary"). 3.6 only does its
    storage and manual editing by the site admin. A tenant can be attached to one
    plan. The effective capabilities = the site default -> the plan -> the
    tenant's own overrides. Changing the plan replaces the whole set of
    capabilities: the tenant's own overrides are cleared, and the response lists
    what was cleared. Plans have create, read, update and delete for the site
    admin. Changing a plan affects all tenants attached to it (the preview gives
    the count, and it is audited). A plan with tenants attached cannot be
    deleted. There are no prices, bills, trial periods or renewals. Those belong
    to the billing module, not 3.6.
  - A tenant created through organization self-service sign-up is attached to the
    plan the site designates (`tenant.selfService.plan`; without one, the site
    default is used).
  - **Cost**: each tenant's effective capabilities live in memory, one copy (about
    20 keys, a few hundred KB for a thousand tenants). They are computed at
    startup, and when something changes only that one tenant is recomputed (when a
    plan or the site default changes, the tenants attached to it are recomputed).
    At request time it is one map lookup, with no database query.
- **The site admin can see and change any tenant** (decided by the owner
  2026-11-23):
  - The admin page has a "tenant detail" page (`/admin/tenants/<slug>`) with
    several tabs: overview (plan, usage, number of people, number of spaces,
    storage mode, recent audit), capabilities, settings (all layered keys, each
    saying which layer its value comes from), storage (mode, storage target,
    spaces waiting to be moved), sign-in (SSO, the choice of social sign-in,
    domains, joining method, management), mail and integrations (SMTP, captcha,
    daily text, connections), limits table and usage, members and admins, audit.
    Each tab uses the tenant's own endpoints, and the site admin can read and
    write them for every tenant. `GET …/overview` gives the overview in one call.
  - The site admin can write tenant-layer values directly, and can also lock a
    tenant's value (`siteLock`), so that tenant's admin cannot change it.
  - The tenant admin can see which things the site decided: layered values have
    `reason` (site-forced, minimum requirement, limited range) and `setBy` (this
    tenant-layer value was written by the site admin), and `siteLock`.
    Capabilities and limits have a source (site default, plan, billing, manual).
    Things the tenant owns carry `updatedBy` (whether the site admin changed
    them).
  - Every change is audited, with the tenant and whether the actor was the site
    admin or the tenant admin. The tenant admin can see, in the tenant's own
    audit, the site admin's changes to this tenant.
- **Storage modes** (plan A, decided by the owner 2026-11-23):
  - Three: **server storage** (`server`, only on the server); **own remote**
    (`own`, each person or space connects its own remote, 2.7 / 3.1); **the
    tenant's own storage target** (`tenant`): the tenant configures its own
    GitHub organization (its own App, or an installation shared with it), or its
    own GitLab / Gitea host and group (with credentials), and after that
    **every space in this tenant automatically gets a repository of its own
    there** (the automatic repository creation flow of 2.7, with the target
    replaced by the tenant's place). One repository per space, **not** one big
    repository per tenant.
  - The site decides which modes there are (`storage.modes.available`), and
    whether one is forced (`lock` on `storage.mode`) or only a default is given
    (`restrict` to the modes that are available). A tenant can only choose its
    own default among the modes the site gives and that its capability
    `storage.modes` also allows, and can also lock it so spaces cannot switch.
    When a space is created, the mode in effect is used; where there is a choice,
    the person creating the space chooses among the allowed ones.
  - **And the remote requirement of 3.1**: `storage.remotes.require` of 3.1 is no
    longer set separately, and is derived from the modes. When a space can use
    `server` it is `null`, and when it cannot it requires a remote (when
    `storage.hosts` is left with only `github.com`, it is `github`). The `scope`,
    `graceDays`, exemptions and `localSpaces.maxBytes` of 3.1 stay as they are,
    and only govern `server` spaces. A `tenant` space follows a remote by nature,
    so it meets the requirement.
  - **Attachment repository**: when `storage.assets` is `repository`, a `tenant`
    space gets an attachment repository (`<repository name>-assets`) created on
    the same target. `own` follows 3.1. `server` has no remote, and attachments
    stay in the space.
  - **Wire the attachments rule** (round 2 built it, nothing passes its inputs
    yet): `settings.AssetsRule(ctx, tenant, AssetsInput{OwnStorage, RepositoryOff})`
    is called from `internal/api/assetspolicy.go` with both inputs `false`.
    When this milestone makes `tenant` spaces, pass `OwnStorage: true` for a
    tenant whose spaces use a target of its own (so the site's
    `storage.assets.ownStorageExempt` takes effect), and pass `RepositoryOff`
    from the limits table's `sync.assetsRepository`. The tenant-target code
    should pass them per tenant, not per request, so the cached lookup stays one
    map read.
  - **Partial cache and eviction**: a `tenant` space, like an `own` one, is
    subject to the partial clone, on-demand fetch, eviction and pinning of 3.1
    (pinning within the capability `storage.pinnedBytes`). A `server` space is
    never evicted.
  - **Credentials**: a `tenant` space uses the tenant's credentials
    (`via: "tenant"`), not any one person's, so deleting a person in 3.5 does not
    leave it stuck at "credentials invalid". Replacing the tenant's token takes
    effect for all spaces at once (the spaces reference the same sealed record,
    which is not copied). The target's host must be in `storage.hosts`, with a
    public network check when connecting. The API requests that create
    repositories count against the tenant's outbound share, and later pulls and
    pushes follow the 3.1 rhythm and do not count against the share.
  - When a space is created, the repository is created first, and if that
    cannot be done the space is not created (`502 storage_target_error`, with the
    reason the target gave). The repository name is the space's `slug`; if it is
    taken, the last 6 characters of the space id are added. Always private.
    Deleting a space (3.5) still leaves the remote repository alone.
  - **Site storage organization** (decided by the owner 2026-11-23: to be
    supported, the risk is known): the site admin can make a GitHub organization
    (an installation of the site's App) the storage the site provides, shared with
    all tenants (`"tenants": "all"`), and every space automatically gets its own
    repository there (still one repository per space). Naming:
    - Space: `<tenant identifier>-<space identifier>`, and for a personal space
      `<username>-<space identifier>`. An attachment repository adds `-assets`;
    - Only lowercase letters, digits and `-`. Names that cannot be used directly
      (such as Chinese) are transliterated or replaced with the space id, with a
      total length no more than the provider's limit (GitHub 100). If the name is
      taken, the last 6 characters of the space id are added;
    - Always private. The repository description gives the space name, space id
      and tenant id, and the topic `petalbook` is added;
    - Renaming a space or tenant **does not rename the repository** (the server
      finds it by the recorded repository id, not by name);
    - Deleting a space goes to the recycle bin first, and **the repository is
      deleted only on purge** (repositories in the site storage organization are
      managed by the server, unlike the "never touch" of `own` and `tenant`).
    Risk (written here, not blocking): GitHub's terms of use do not welcome using
    it as general storage for a service, and at volume it could be rate limited or
    suspended. So it is one optional mode, not the only copy, and attachments
    should go on the server or in object storage where possible.
  - **The repository carries its own identity, so it can be recognized again even
    if the server's data is lost** (the owner, 2026-11-23): each space's
    `.petalbook/space.yml` has the space id and tenant id written in (written when
    the space is created, filled in when the mode is switched), and the remote
    repository's description and topic record another copy. The site admin has
    "rebuild from the storage organization": it lists the repositories with the
    `petalbook` topic and rebuilds spaces from the ids inside, with the pages and
    all the history in the repository. Accounts, members, permissions and
    settings are not in the repository, and depend on the state database backup
    (see 5.17).
  - **Changing the mode** (the tenant changes its default, the site or a
    capability removes a mode, the tenant changes its storage target): new spaces
    use the new one. Existing spaces **stay as they are and keep working**, the
    owner and tenant admin see a "move over" prompt, and nothing is deleted.
    Moving is a background task: moving to `tenant` creates the repository on the
    target, pushes the full history and switches the remote; moving to `server`
    fetches all the content back and disconnects the remote; moving to `own` goes
    through the connection flow of 3.1. Old remote repositories are never touched.
    Only when `server` is no longer allowed does the `scope` and grace period of
    3.1 apply (read-only when it expires, nothing deleted). When the storage
    target is changed, the old target is kept for spaces not yet moved, and a
    target still in use by spaces cannot be deleted. Moving many at once uses the
    batching and second confirmation of 3.5.
  - **Both directions must be movable** (the owner, 2026-11-23): site storage
    organization -> the tenant's own organization or host, site storage
    organization -> a person's own repository, and the reverse (`tenant` / `own`
    -> site storage organization), the same as the `server` moves in and out
    above, moved one space at a time or in bulk for a whole tenant. The steps to
    move one space, each recording progress and continuing after a restart or
    rolling back:
    1. **Complete the history first**: after 3.1 the server often holds a partial
       clone, so before moving, fetch all commits and files back from the old
       remote;
    2. **Create the repository in the new place** (naming and credentials of the
       new mode) and push all the history, during which the space works as usual;
    3. **Brief read-only** (seconds, the UI says "moving"): push the commits added
       in the meantime, then switch the remote;
    4. **Verify**: it counts as done only if the new repository's head equals the
       one on the server, otherwise roll back to the old remote;
    5. **Old repository**: the user's or tenant's own is never touched. One in the
       site storage organization is kept for the `recycle.days` grace period, and
       deleted when it expires after confirming no space points to it any more.
    The space id does not change, and drafts, offline copies (the cursor of 4.1)
    and links are unaffected. The attachment repository moves along with it.
    Before moving into the site storage organization, the quota is checked (only
    what is with us counts as usage), and if it would be exceeded the move is
    refused with an explanation. Moving out requires that the new place is already
    writable (the tenant has configured its own App or connection, or the person
    has connected their account). Moving in requires the site to allow that mode.
  - Only hosts with a repository-creation API are supported: GitHub
    (github.com), GitLab, Gitea / Forgejo. Hosts with only Git and no API cannot be
    a storage target (they can still be connected with `own`).
- **Layering gets one more kind, "minimum requirement"**: `lower` adds `tighten`
  (the layers below can only be stricter than the site's own value) to the
  `allow` / `restrict` / `lock` of 3.3. Each key states what "stricter" means
  (which side of a boolean is strict, a smaller upper limit is stricter, the order
  of options, a list can only be pruned). Keys with no order cannot choose
  `tighten`. The site can write a sentence of explanation for each rule (at most
  200 characters), and the tenant admin sees "Set centrally by the site admin"
  and this sentence next to the greyed-out control. In the API these are
  `forced`, `reason`, `allowed` / `max`, `editable`.
- **The effective value is still cached by (tenant, generation, space settings
  version)**, with one map lookup at request time. The roughly 30 new keys come to
  only a few MB even for a thousand tenants. When a capability changes, that
  tenant's generation also goes up by one. The permission check (the per-person
  cache of 1.7 / 3.2) does not change.
- **Site defaults** are set by what makes sense, not by "same as before": for
  security keys the site `lower` defaults to `tighten`; `storage.mode` defaults to
  `server`, with `restrict` to the modes the site has; keys for "whether it may
  borrow the site's resources" default to `allow` and `true`.
- **Which security settings can reach the tenant**: those that take effect after
  the password check (`ssoOnly`, requiring a passkey, session length) can, and the
  tenant can only make them stricter. Those that take effect before sign-in
  (per-address quotas, failure caps, lockout, the captcha on the sign-in page)
  exist only at the site level, because at sign-in time the tenant is not yet
  known, and a stricter tenant value would leak which tenant a username is in.
- **Social sign-in and SSO are separate** (replacing the `legacyCallback` of 2.8
  and the old `--oidc-*` parameters; 3.6 deletes both):
  - Social sign-in (Google, GitHub, Microsoft personal accounts, Apple,
    GitLab.com and so on) is configured by the site, and each is a "Continue with
    Google" button on the sign-in page. Identities are unique site-wide, so one
    Google account corresponds to one account on the server (the same as GitHub in
    2.7). A tenant with the capability `signin.socialChoice` can prune which of
    them the people in this tenant can use.
  - SSO (enterprise OIDC / SAML) belongs to the tenant (needs the capability
    `signin.sso`): it can only sign in to, create and merge this tenant's
    accounts, and group mapping points only to this tenant's groups. The site can
    also have its own SSO providers, for all or specified tenants.
  - Each provider has its `class` (`social` or `sso`) and its ownership (`site` or
    some tenant) set when it is created.
  - The identity key is (ownership, issuer, subject), where ownership is `site` or
    a tenant. So two companies can both use Entra ID without sharing identities. A
    tenant's SSO never signs in the site admin (the tenant admin controls the
    provider and could point it at their own identity provider). When the issuer
    is changed, its old identities are unlinked.
- **One account, several ways to sign in** (decided by the owner 2026-11-23): the
  same account can have email plus password, Google, GitHub, Microsoft and so on
  at once, bound and unbound at any time under "Sign-in methods" on the account
  page, and whichever one is used signs in to the same account. This continues
  the existing practice of 1.x / 2.8 (`intent: link` for binding,
  `DELETE /api/account/identities/{id}` for unbinding, an account with no
  password can set one later), and 3.6 adds:
  - The account page lists the password, each identity and the passkeys as one
    "Sign-in methods" table, and each row says when it was last used to sign in.
    Unbinding the last method that can sign in is still refused (`409 conflict`).
  - **Preset templates**: when the site admin adds social sign-in, they first
    choose a template (Google, Microsoft, Apple, GitHub, GitLab.com). The template
    fills in the issuer or the various addresses, the scopes needed, `class:
    social` and the brand icon, and the admin only fills in the client id and
    secret. Templates ship with the program and are not fetched from the web.
    - **Apple** (confirmed as wanted by the owner 2026-11-23): when the iOS app is
      listed, as soon as the app has third-party sign-in the App Store requires
      "Sign in with Apple" to be offered as well, so it is a required template.
      Apple gives the name only at the first sign-in, and the email may be a relay
      address (`privaterelay.appleid.com`). It is treated as a verified email, but
      is not used for email-domain SSO routing or for joining an organization by
      domain.
    - **Templates are split into common and more** (the owner, 2026-11-23): "Add
      sign-in method" by default lists only the few common ones (Google,
      Microsoft, Apple, GitHub). The rest are under "More templates", opened and
      chosen from there, grouped by region:
      - Mainland China: WeChat (QR scan for a website app on the open platform),
        WeCom, DingTalk, Feishu, QQ, Alipay;
      - Other regions: LINE, Kakao, Naver, Yandex;
      - General: GitLab.com, Discord, Slack, X, Facebook, Amazon.
      Templates carry a region label. The region preset `site.profile` decides
      which count as "common" (with `cn`, the common ones are WeChat, WeCom,
      DingTalk, Feishu and phone number, and the overseas ones go into "More").
      Most mainland platforms are not standard OIDC: WeChat uses `appid` instead
      of `client_id`, its token endpoint parameters and response format are its
      own, and the subject is the `unionid`. So behind each of these templates is a
      very small adapter (only in the two steps of fetching the token and fetching
      user info), so it is not pure configuration. These adapters are used only
      for site-wide social sign-in. **A tenant's own SSO supports only standard
      SAML and OIDC** (decided by the owner 2026-11-23): the enterprise editions
      of WeCom, DingTalk, Feishu and so on also go through their standard SAML /
      OIDC interfaces, and no special code is written for any one of them.
      Directory sync (SCIM or each vendor's API) comes later. Applying for apps on
      these platforms generally needs a mainland company entity, which the owner
      will arrange when the time comes.
    - **Sign-in methods in mainland China** (done when deploying in mainland
      China, reserved for now): WeChat sign-in (a variant of OAuth 2.0, no email,
      the subject is the `unionid`, and an email or phone number must be added to
      the account separately), phone number plus SMS code (a new sign-in method
      `kind: phone`, needing an SMS provider, rate limits per number and per IP,
      and code abuse protection). So "an account's sign-in methods" cannot assume
      that each carries an email, and the rule that emails are unique site-wide
      does not apply to identities with no email.
  - **Custom providers**: besides templates, the site admin can add any OpenID
    Connect provider (the existing `kind: oidc`, by discovery document) and
    providers **that have only OAuth 2.0 and no OIDC** (the new `kind: oauth2`:
    the three addresses for authorization, token and user info, the scopes, and
    the field names for taking the subject, email and name from the user info). An
    email given by `oauth2` is by default not counted as verified (it does not
    take part in merging by verified email), unless the admin says which field in
    the user info means "verified" and the provider's "trust verified email
    addresses" switch is on, as for OIDC. PKCE (S256) is always used. Built in
    d6, in the nested `oauth2` block of the provider (no per-provider
    account-creation switch: the registration mode decides; it never claims an
    SSO-waiting account). `oauth2` can only be a
    site-wide `social` provider; a tenant's SSO is still only OIDC and SAML.
  - For the endpoints see "Several sign-in methods per account" in "Sign-in:
    social providers and SSO" of `docs/api.md`.
- **Finding SSO by email domain** (needs the capability `signin.domains`):
  - A tenant claims a domain and verifies it by DNS (decided by the owner
    2026-11-23). An unverified claim does nothing. There are two kinds of record,
    either one: TXT (the default) `_petalbook.<domain>` with the value
    `petalbook-verify=<verification code>`; or CNAME `_petalbook.<domain>` pointing
    to `<verification code>.verify.<this site's domain>` (this site's domain is the
    host name of `--public-url`; only the CNAME target is compared, it is not
    resolved). The verification code is valid for 7 days, and after it expires it
    must be generated again. Only this one domain is recognized (subdomains are
    claimed separately). Public email domains cannot be claimed.
  - Verified claims are rechecked automatically once a day. If the record cannot
    be found for 7 days in a row, the claim is cancelled, the tenant admin is
    notified and it is audited. A DNS error does not count as not found.
  - The "Check now" button: once a minute per domain, 20 times an hour per tenant,
    and `domains.checksPerMinute` (default 60) times a minute site-wide. During
    cooldown it answers `429 rate_limited` with `Retry-After`, and the UI shows a
    countdown. The result is shown on the spot (found, not found, wrong value, and
    for a wrong value the value that was seen), and every check is audited. Manual
    checks do not affect the daily automatic recheck.
  - **Two tenants claim the same domain**: there can be many unverified claims,
    but only one verified one, and whoever verifies first gets it. A later
    verification answers `409 domain_claimed`, without saying which tenant (and
    only someone who can change that domain's DNS can get to this step). The site
    admin can transfer it.
  - The sign-in page first asks for an email or username (when the server has
    verified domains). `POST /api/auth/discover` looks only at the domain and does
    not look up accounts, so the answer is the same whether or not an account
    exists. A username always gets the answer "no SSO". It counts against the
    per-address per-minute quota of 2.8, and is answered from memory. When
    `ssoOnly` is in effect for the domain's tenant, the page hides the password
    box and offers only "Continue with <the tenant's identity provider>", with
    "Sign in with a password" still next to it (the admin's back door; the
    `ssoOnly` of 2.8 does not block admins). There is also a "Sign in with SSO"
    entry that asks only for the email and redirects in the same way.
  - The email is only a hint (`login_hint`): which account is signed in is decided
    by the identity the identity provider returns, so routing by domain cannot
    sign a person in to an account outside the tenant the provider belongs to.
  - Usernames can still sign in with a password as before. As for the four sign-in
    modes of 2.8: `auth.loginMode` becomes layered, and a page with `?tenant=`
    uses the tenant's; `dual` and `sso_first` gain "ask for the email first";
    `sso_redirect` goes straight to the tenant's SSO on the tenant's page;
    `local_only` turns off the buttons and the SSO entry, but not the password
    back door.
  - When a tenant forces SSO, signing up a password account with an address on a
    verified domain answers `409 sso_required`, with the SSO entry point.
- **Emails are unique site-wide, and email sign-in comes first** (decided by the
  owner 2026-11-23):
  - The username is still the unique handle and id (used in `@mentions`, commit
    authors and links).
  - The sign-in box takes an email or a username, and in the UI the email comes
    first ("Email" is written first and the username is the secondary way of
    writing it). `POST /api/login` takes `identifier` (replacing the old
    `username`).
  - An email is unique across all accounts site-wide (including disabled ones and
    those in the recycle bin, released only on purge), verified or not. **There
    are no exceptions.** Changing it to an address someone else already uses
    answers `409 email_taken`, and invitations and admin account creation are the
    same. When an address is already taken at sign-up, the same `202` as in 3.3 is
    answered, and a "you already have an account" email is sent to that account
    (3.3 only looked inside this tenant, now it looks site-wide). An address held
    only "unverified" by another account goes, at sign-up confirmation (or a
    trusted sign-in, below), to the person proving it; the original account's
    email is cleared, the person is told in the site, and `email_released` is
    audited. A verified holder is never displaced.
  - Built in part 5 (owner decisions 2026-11-25):
    - **Storage.** `user_emails` (account, address, key, primary, verified, who
      vouched, created), unique on the key and on one primary per account. The
      primary address is the UPN; `users.email*` is its copy, written in the same
      transaction by `store.SetPrimaryEmail` (a test checks they never drift, and
      another that nothing else in the store writes the copy). The shape already
      allows several addresses per account (several rows, each verified or not,
      an atomic primary switch, removing one that is not primary) for the
      follow-up behind `accounts.multipleEmails`; signing in and resetting by
      address read a verified row of `user_emails`, never `users.email`. The old
      `users_email (tenant_id, email_key)` index is dropped.
    - **The key.** The domain as IDNA (UTS #46) punycode, lower case; the local
      part ASCII-lowercased when ASCII, otherwise NFC, case folded, NFC. So
      `用户@例子.公司` and its punycode form are one address, and turning the
      setting below on and off never lets two accounts share one. Folding joins
      what may be two mailboxes (U+017F folds to "s"): they count as one address
      for uniqueness, but proving one never takes the other from its holder.
      Before part 5 an address outside ASCII got an empty key and escaped
      uniqueness.
    - **Addresses outside ASCII** are behind the site setting
      `users.emailNonAscii` (site only, off by default; `users.` like
      `users.reuseUsernames`, since addresses are unique on the whole server).
      Off: refused with `400 email_not_ascii` wherever an address is set, and a
      typed one finds nobody. On: accepted and compared by the key. Mail to them
      needs a relay with SMTPUTF8.
    - **Sign-in never attaches to an account by its address.** 2.8's merge by
      verified address is removed. An address the sign-in vouches for that
      another account holds verified ends in `sso_error=email_in_use`: the
      person signs in to that account and links the provider there. One held
      unverified moves to the new account; one the sign-in does not vouch for is
      left out of it.
    - **Which sign-ins vouch for an address** is one function, `trustsEmail`;
      the address is then verified with `verified_by = provider:<slug>`. Google
      and Apple: `email_verified=true`. GitHub: an address its API lists as
      verified, on github.com only (a GitHub Enterprise server needs the
      provider's trust switch: its administrators can mark any address
      verified). Microsoft: only `xms_edov`; never its plain `email` claim, not
      even on a domain verified here (the "nOAuth" takeover; recorded decision
      of step d1, 2026-11-27: the safe rule, which the owner may loosen later).
      Apple's private-relay addresses are verified but never meet another
      account's by address. An organisation's
      SSO (OIDC or SAML): only addresses on its verified domains, each exactly
      (a subdomain is claimed by itself), so nothing
      until domain verification exists (a configured SAML provider is treated
      so). A configured OIDC provider: only with the administrators' opt-in (the
      provider's `mergeVerifiedEmail` switch, now "trust this provider's
      verified email addresses", default off) and `email_verified`. Google,
      Apple and Microsoft arrive with step (d)'s templates; the rule is wired
      for the kinds of today.
    - **A tenant administrator's "mark verified"** (`verified_by = tenant_admin`)
      shows as verified but holds the address no more than an unverified one
      (`store.EmailRow.Holds`): whoever proves the address takes it, it finds
      nobody for a reset, and the member may confirm it themself, which clears
      the mark. A site administrator's mark counts as verified.
    - **Known issue after 3.6 part 5:** an existing account cannot claim an
      address another account holds unverified just by setting it
      (`PATCH /api/account` is `409 email_taken`, not a pending change); only a
      new registration's confirmation or a trusted SSO sign-in takes it, or an
      administrator clears it first. Follow-up, with the several-addresses PR:
      "add email, then confirm" takes an unverified address.
  - Together with "ask for the email first" above: the first step of the sign-in
    page asks for an email or username, `discover` looks only at the domain, if
    there is SSO it goes to SSO, and otherwise the second step asks for the
    password (or passkey).
- **Joining an organization** (decided by the owner 2026-11-23): each organization
  chooses for itself, and several can be on at once:
  - (a) Invitation only: always available.
  - (b) Automatic join for emails on a verified domain (`join.domain: auto`, off by
    default): someone who signs up with an address on this domain (after the
    address is confirmed), or creates an account through social sign-in where the
    provider vouches for the address, gets an account created in this tenant, and
    the person is told which tenant they joined.
  - (c) Emails on a verified domain apply to join, and an admin approves: after
    sign-up confirmation the account is not created straight away, and becomes a
    join request (holding the username, and expiring if not handled in 30 days).
    The admin approves it in the list (and can change the role) or rejects it.
    Approval creates the account, and rejection sends an email and releases the
    username.
  - (d) Signing in through the organization's own SSO joins automatically (the
    account is created at once, which is the provider's `autoCreate`). It can let
    through only people in the groups listed in the IdP, and others are refused at
    sign-in (checked at every sign-in, not only when the account is created).
  - (b) and (c) need the capability `signin.domains`, and (d) needs `signin.sso`.
    On a domain, (b) and (c) are either-or, and can override the organization's
    default per domain.
  - The default role of someone who joins: `member` or `viewer` (invitations still
    use the role written on the invitation). `viewer` in an organization is new:
    it cannot create spaces in the organization and is at most read-only in the
    organization's spaces. The permission cache stores one more ceiling, and the
    check is still a single lookup.
  - IdP groups map to the organization's groups, continuing the `groups.map` of
    3.2. IdP groups mapping to organization admin (`groups.admins`) needs this
    tenant to have the capability `signin.idpAdmins` (off site-wide by default).
    Every promotion to admin through it is audited. Only people promoted through
    it are demoted, and manually set admins are left alone. `siteAdmin` is never
    given.
  - Removal from an IdP group takes effect at the next sign-in (same as 3.2). To
    take effect at once, disable the person. SCIM is still in phase 5 (5.10).
  - 3.6 is first done as "one person, one tenant": (b), (c) and (d) apply only to
    new accounts. 3.7 changes it to adding an organization to an account.
- **Organization self-service sign-up** (following Notion, decided by the owner
  2026-11-23): a site-wide switch `tenant.selfService`, off by default, requiring
  `tenancy` and mail to be on first. The sign-up page gets one more option,
  "Create my own organization". After the address is confirmed the tenant is
  created, and then an account is created in it, as this tenant's admin (not the
  site admin), with a personal space. The new tenant starts from the site
  defaults, with sign-up closed, and its capabilities use the plan named by
  `tenant.selfService.plan` (without one, the site default). The caps (site-wide,
  changeable by the site admin): 3 per person (`perPerson`; 3.6 counts, by
  confirmed address, the tenants it created that still exist; from 3.7 it counts
  by account), 20 an hour site-wide, 1000 in total. When exceeded, the same `202`
  is still answered, and an email is sent saying it was not created. Individuals
  follow the flow of 3.3.
  - An organization with a verified domain can turn on "do not let people on this
    domain create another organization" (`restrictOrgs`, per domain, off by
    default): creating an organization with an address on this domain answers
    `409 domain_managed`, with this organization's name and the ways to join
    (automatic join, application or SSO), and the page guides to this
    organization. It blocks only creating organizations, not signing up personal
    accounts.
  - In 3.6 an address can have only one account (emails are unique site-wide, and
    an account is in only one tenant), so an address that already has an account
    that comes to create an organization gets, as in 3.3, only "you already have
    an account". Creating an organization again from the switcher menu by someone
    already signed in is in 3.7.
- **Each tenant's limits table** (decided by the owner 2026-11-23): set manually
  by the site admin, one table per tenant. The capabilities above are the switch
  items in the table (the keys do not change), and the table also has count caps:
  - People: the number of people (`other.people`);
  - Spaces: total and per person (`storage.quota.spaces`,
    `storage.perPerson.spaces`);
  - Storage: the total (`storage.quota.bytes`, which is this tenant's "storage
    allocation" in the 3.5 storage pressure), a single file
    (`storage.maxFileBytes`), pinning (`storage.pinnedBytes`);
  - Sync: the allowed storage modes (`storage.modes`), and whether the attachment
    repository can be used (`sync.assetsRepository`, new);
  - Usage (new): API and MCP requests per minute (`usage.apiPerMinute`,
    `usage.mcpPerMinute`), mails per day (`usage.mailPerDay`), external interface
    calls per minute (`usage.outboundPerMinute`, which replaces the site-wide
    `outbound.tenantPerMinute` as this tenant's share, and when not set that is
    still used);
  - All the capabilities of 3.6.
  - **Each value records its source**: `site` (the site default), `plan` (the
    tenant's plan), `billing` (written by the billing module, which does not exist
    now), `manual` (written by hand by the site admin). **Manual wins**: the
    effective value = manual -> billing -> plan -> site default, taking the first
    that exists. The former source `tenant` is `manual`.
  - **It can carry start and end dates** (`from`, `until`, both optional), for
    example to give extra quota temporarily for a month. A value outside its
    period is treated as absent. The memory records the time of the next start or
    end, and when it arrives only that one tenant is recomputed, with no polling.
  - **Events**: changes (including the start and end of dated values) and going
    over a limit (at most one per tenant per key per day) are now written to the
    audit. Webhooks are added later (only reserved, not done). When a limit is
    exceeded, usage-type limits reject as usual (API and MCP answer
    `429 rate_limited`, and mail cannot be queued), and count-type limits follow
    the rule of 3.3.
  - **Usage can be exported per tenant**: one summary row per day
    (`tenant_usage_daily`, written by the daily reconciliation). The site admin
    and this tenant's admin export it as CSV or JSON Lines for a date range.
- **Where attachments go** (`storage.assets`, decided by the owner 2026-11-23):
  the same repository / a separate attachment repository, in three layers: site
  -> tenant -> space owner. The site can force it (`lock`) or only give a default
  (`allow`). When the tenant's limits table turns off `sync.assetsRepository`, only
  the same repository is possible. Since round 2 (2026-11-24) the default is the
  separate repository, there is a site lock for one tenant, and a site switch
  `storage.assets.ownStorageExempt` (default on) leaves tenants on their own
  storage out of the site's value and lock; see phase-3-tenants.md, "Where
  attachments go".
- **Remotes that cannot sync partially** (`storage.partial.unsupported`, decided
  by the owner 2026-11-23): the only partial-sync policy, `allow` (the default,
  uses a full clone) or `refuse` (connecting such a remote answers
  `409 partial_unsupported`, with the host). Layers: site and tenant, and
  `refuse` is stricter. Whether it is supported is decided by the automatic
  detection of 3.5. Refusal applies only to new connections, and ones already
  connected keep working.
- **The billing boundary** (decided by the owner 2026-11-23; **billing and
  payment do nothing for now**):
  - Plans, limits, metering, bills and renewals belong to one **internal billing
    module**. It writes the limits table through an internal interface (a Go
    interface, not HTTP), with the source `billing`. The site admin's manual edits
    stay as they are, and manual wins.
  - Only **payment** is external (Stripe, Alipay, WeChat Pay and so on), through
    payment adapters and their callbacks. This boundary is reserved: the adapter
    interface and the callback path (`/api/billing/callbacks/{adapter}`) are only
    written in the contract, and no route is built.
  - The contract has no "token or interface for an external billing system": an
    external system cannot change the limits table directly, and can only have the
    internal billing module do so through the payment callback.
  - The UI for the site admin to edit the limits table by hand ("limits table and
    usage" on the tenant detail page) is done as planned.
- **Everything a tenant owns is in memory** (SSO providers, the domain table, SMTP,
  captcha keys, daily text endpoints, GitHub App, storage target): read at
  startup, and when changed only that one tenant's is read again. No database
  query is made for them at request time. Per-tenant caps: 10 SSO, 20 domains, 1
  sender, 1 captcha set, 20 endpoints, 1000 texts, 1 GitHub App, 1 storage target
  in use (plus old targets that still have spaces not yet moved away).
- **Mail**: if the tenant has its own SMTP it is used (only `starttls` / `tls`,
  public addresses, ports 25 / 465 / 587 / 2525). If not, the site's is borrowed
  according to `mail.useSite` (the sender address is the site's, the display name
  is the tenant name, and the tenant is not allowed to pose as the site domain
  when sending). If neither exists, mail is treated as off. The site-wide queue
  limit does not change, and each tenant may take at most 200 mails.
- **Daily text**: a tenant's own endpoint has its own sealed secret, `rps` and
  cache (keys carry the tenant), and does not use the site's quota. Whether the
  site's endpoints can be used depends on `daily.useSiteApis`.
- **GitHub** (through the provider interface of 2.7b; the routes are under
  `/api/admin/tenants/{tenant}/connections/github…`): a tenant can register its
  own App (github.com only, needs the capability `integrations.connections`), and
  its installations are shared only within this tenant. Installations of the
  site's App are shared per tenant (all or a few specified ones). When connecting
  to GitHub, the tenant's App is used first, and if there is none the site's is
  used according to `connections.useSiteApps`. One GitHub account still connects
  to only one person on the server. `internal/githubapp` takes the App as a
  parameter and does not use global variables, so the interface defined in 3.4
  was already multi-App.
- **The other storage settings**: the `storage.remotes.scope`, `.graceDays`,
  `hosts` and `localSpaces.maxBytes` of 3.1 get a tenant layer, and the site
  decides whether it is forced, a minimum requirement or only a default. Where
  attachments go (`storage.assets`) has three layers: site, tenant, space owner.
  When the attachment repository is required, it applies only to new spaces and
  spaces that have just connected a remote. Existing spaces get a one-click move,
  with no automatic move. An exemption is counted at the layer that granted it.
  Pinning (`pin`) takes up the site's disk, and a tenant admin can pin only within
  the capability `storage.pinnedBytes` (default 0). Anyone can evict the cache.
- **Migration** `0021_tenant_autonomy.sql`, tested on both SQLite and PostgreSQL.
  Databases used only for development and demos can open it, and no upgrade that
  keeps data is promised (when the same email has several accounts in a
  development database, the earliest keeps it and the others are cleared).
- **Outward actions**: the addresses a tenant fills in make the server connect out
  (identity providers, SMTP, endpoints, DNS, storage targets). Self-service
  sign-up lets anyone create a tenant, so it is off by default. A tenant's sender
  sends mail to real people in the tenant's name. A storage target creates
  repositories in the tenant's GitHub organization or Git host. Tests always use
  in-process fakes (a fake SMTP, a fake OIDC, a fake DNS resolver, fake GitHub and
  GitLab interfaces), and never touch the outside.

Not doing (reasons in parentheses):

- One account in several tenants, switching workspaces (it would redo all the
  assumptions of "one person, one tenant"; the owner brought it forward to 3.7 on
  2026-11-23).
- Telling tenants apart by host name (same as 3.3, goes into 5.14).
- A tenant's own per-address quotas and lockout, and the sign-in page captcha (see
  above, they would leak which tenant a username is in).
- Moving people and spaces between tenants (same as 3.3).
- A tenant's own GitHub Enterprise Server App (same as 2.7, only github.com is
  supported).
- Billing and payment: prices, bills, metering, trials, renewals, automatic
  downgrade on expiry, payment channels (see "The billing boundary": only the
  internal interface and the place for payment adapters are kept, and nothing is
  done).
- A storage target with one big repository per tenant (what the owner decided is
  one repository per space); Git hosts without a repository-creation API as a
  storage target.

Decided by the owner 2026-11-23:

1. Addresses that a tenant admin fills in make the server connect out: accepted as
   designed (a public network check on the resolved address at connection time,
   counted against the site-wide outbound cap and the tenant's share, and can be
   turned off with the capabilities and switches such as `mail.useSite`).
2. Organization self-service sign-up follows Notion: a site-wide switch, off by
   default. When on, 3 per person by default (changeable by the site admin) and 20
   an hour site-wide. An organization with a verified domain can forbid people on
   that domain from creating another organization, and guide them to itself.
3. Automatic join by domain: accepted (the tenant turns it on itself, off by
   default).
4. When a domain is claimed by two tenants, whoever verifies first gets it (the
   site admin can transfer it, and the latecomer cannot see who).
5. Domain verification uses DNS: TXT (the default) or CNAME, the verification code
   is valid for 7 days, rechecked daily, cancelled with a notification after 7
   days in a row with no record. "Check now" has a cooldown and caps, and the
   result is shown on the spot and audited.
6. Sign-in: the username is still the unique handle. Sign-in takes an email or a
   username, with the email first. Emails are unique site-wide, relying on a unique
   index, with no exceptions.
7. Four ways to join an organization (invitation only, automatic by domain, domain
   application plus approval, instant join through the organization's SSO with
   admission by IdP group possible), in any combination; the default role of
   those who join; IdP groups mapping to groups and (when the capability exists)
   to organization admin. Removal from an IdP group takes effect at the next
   sign-in, and SCIM is still in phase 5.
8. v0.x does no upgrade compatibility: all the requirements in 3.5, 3.6 and 3.7
   written for "still the same after upgrade" are deleted, and the new rules are
   written directly (see "The order of 3.5, 3.6 and 3.4" above).
9. Each tenant has one set of capabilities, the site default plus per-tenant
   overrides, changed only by the site admin. Capabilities decide whether it
   exists, layers decide how much. When a capability is off it answers
   `403 capability_off` and the UI is greyed out with "Needs to be turned on
   site-wide". Plans (a set of capabilities, where changing the plan replaces the
   whole set) are reserved, and billing is not done.
10. Storage modes use plan A: the site decides which modes there are and whether
    they are forced or default. The tenant chooses among what its capabilities
    allow. The tenant's own storage target is one repository per space. When the
    mode is changed, existing spaces get the option to move, and nothing is
    deleted.
11. The site admin can see and change any tenant's configuration, capabilities and
    storage modes, and every change is audited. The tenant admin can see which
    things the site decided or locked. There is a "tenant detail" page.
12. Each tenant has one limits table, set manually by the site admin: people,
    spaces (total and per person), storage (total, single file, pinning), sync
    (allowed modes, attachment repository), usage (API / MCP rate, mails per day,
    external interface calls) and the capabilities of 3.6. Each value records its
    source (manual / plan / billing), and manual wins. It can carry start and end
    dates. Changes and going over a limit now go into the audit, and webhooks are
    added later. Usage can be exported per tenant.
13. Where attachments go has three layers (site -> tenant -> space owner), and the
    site can force it or only give a default.
14. Partial sync keeps only one policy, "remotes that cannot sync partially: allow
    (full) / refuse", default allow, layered.
15. Billing stays internal: plans, limits, metering, bills and renewals belong to
    the internal billing module, which writes the limits table through an internal
    interface. Only payment is external (payment adapters and callbacks), and only
    the boundary is reserved. The contract has no token or interface for an
    external billing system. The UI for admins to edit by hand stays. Billing and
    payment are both not done now.

For the owner to decide (do it by the default in parentheses for now):

1. Among the site's default capabilities, whether "tenant admins view and export
   the contents of organization spaces" (`data.adminView`) is on or off (on: it
   continues what 3.5 decided, "admins can look, and every look leaves an audit
   record", and the site admin can turn it off per tenant. The alternative is off
   by default and turned on per tenant or plan, which is more conservative on
   privacy).
2. After a tenant loses a capability or switches to a smaller plan, what happens
   to the spaces already using that thing (keep working as usual, and only new
   ones cannot: spaces on a storage target keep syncing, and only cannot be
   created any more. The alternative is a grace period, after which they become
   read-only, which will work better once billing exists).

**The 3.7 contract** (2026-11-23): the endpoints and error codes are written in
the new "Several organisations per account" section of `docs/api.md`. On the same
day the owner brought "one account in several organizations" forward from phase 5
(formerly 5.14) to here, following Notion. The design is written after 3.6 is
merged.

**Session shapes reserved in 3.6** (no behaviour yet): `sessions` has
`current_tenant` ('' is the person's own context), `proofs` (JSON: tenant id to
`{method, provider, at, seen}`, method `sso|domain|password|passkey|social`, times
in epoch milliseconds) and `client` (`web|app`, which picks the site-set sliding
lifetime: app 30 days, web 7). An organization that enforces its SSO or verified
domain gates only its own context: its spaces open only in a session that holds a
live proof for it; switching between verified contexts is free. Sign-in fills
`client` (`web` until a request says otherwise) and the home proof. The session
cache keeps what it read, never writes it back, and the hourly update writes
`last_used` only; `current_tenant` and `proofs` change only through
`UpdateSessionState`, which drops the session's cache entry. A request adds no
query and no write.

Decided:

- **A person's home and organizations**: each account has one home
  (`users.tenant_id` is now called the home, with the column name unchanged) and a
  membership table `tenant_members` (tenant, person, role
  `admin` / `member` / `viewer`, how they joined, when). Personal spaces are in the
  home, and a shared space belongs to one organization. An account can be a member
  of several organizations: by invitation, by joining by domain, by instant join
  through the organization's SSO, or by creating one itself. The organization is
  switched at the top left of the UI, and "Personal" is one of the entries. When
  there is only one place to go, the switcher is not shown.
- **Organization-enforced SSO and per-organization verification** (the owner
  2026-11-24):
  - An organization admin can turn on "enforce SSO" (`signin.enforceSSO`, which can
    be turned on only after the 3.6 tenant SSO is configured and has passed its
    test): to access this organization's spaces and data, the session must carry
    this organization's most recent SSO verification. A session signed in with a
    password, social sign-in or a passkey cannot enter this organization. **The
    personal area and other organizations are not affected.**
  - **No sign-out, no signing in again**: the session additionally records
    "per-organization verification" (tenant, provider, verification time, expiry).
    When switching from the personal area to this organization, the organization
    in the switcher list has a lock next to it (not an exclamation mark, which
    looks like an error) and "Verification needed". Clicking it opens a dialog:
    "Acme requires signing in with your company account", saying that the
    personal space is not affected and the person will not be signed out, with
    "Cancel" on the left of the buttons and the main button "Continue with
    <IdP name>" on the right. The IdP's name and icon are filled in by the tenant
    itself when configuring SSO. After verification the person returns to where
    they were going. Opening a page in the organization directly from a link opens
    the same dialog in place, and opens that page after verification. The API
    answers `403 sso_required` (with `tenant`, the provider's name and icon, and
    the address to start verification).
  - **Validity periods differ by client** (the owner 2026-11-24):
    - By default **all are sliding**: the app **30 days**, the web **7 days**
      (used within the period, it is extended by another period);
    - **Set only by the site admin** (`signin.orgVerify.app`, `.web`: sliding or
      fixed, the number of days, and an optional "maximum validity", not set by
      default). **Tenants have no such layer**, following the practice of OpenAI
      Business and others, and organization admins cannot change it. This is a
      setting that exists only at the site layer (see "Every site-wide setting can
      be "default" or "forced"" above).
    The cost of sliding is stated: if the company disables a person on its side,
    they will not be signed out automatically as long as they keep using it. The
    answers are that the organization admin removes them in PetalBook (effective
    at once), the site sets a maximum validity, and SCIM later. The client type is
    decided by how the session was created (the app's sign-in flow and device
    token), not by request headers. Even if someone imitates the app in a browser,
    they only get the period the site set for the app.
  - **Tokens by scope**: when creating a token one chooses the scope (the personal
    area only, or tick several organizations). A token for the personal area
    always works, regardless of any organization's settings. To access the data of
    an organization with enforced SSO, the token must first be authorized once
    through that organization's SSO (following GitHub), and the expiry of the
    session's verification does not affect an already authorized token. When a
    person is removed from the organization, the token becomes invalid for that
    organization. An organization turning off the token capability
    (`integrations.tokens`) affects only that organization.
  - **Emergency accounts**: an organization can designate one or two owners as
    emergency accounts, which enter without SSO to fix the configuration. Every
    use is audited and the other admins are notified.
  - Site admins are not bound by organization SSO, and content still goes only
    through the audited "view". Offline copies: when the verification expires, the
    copy of this organization in the app stops syncing and shows a lock, and
    continues after verification again.
  - Part 4 of 3.6 does the enforced-SSO setting and tenant SSO. The per-organization
    verification, the lock and dialog at switching, and token scopes are done in
    3.7 (they all depend on an account belonging to several organizations).
- **Who manages the account**: when `users.held` is true, the admin of the tenant
  that is the home manages the account itself (disable, reset password, change
  email, delete, that is, those of 3.3 and 3.5). When false (a personal account),
  only the site admin and the person themselves can. All accounts are personal
  accounts: the home is the default tenant, and an organization entered through
  invitation, sign-up, by domain or by SSO adds only a membership row. Only
  managed accounts (below) are `held`.
- **An organization admin manages only the organization's things**: this
  organization's spaces, groups, members (roles, removal), joining methods and
  settings. They never look at members' personal spaces (decided by the owner
  2026-11-23, no exceptions), and cannot touch the account itself of one that is
  not managed by this organization. Removing a person follows the rules of 3.5 for
  deleting a person, done only in this organization's spaces (spaces that are
  theirs alone and have no other members go into this organization's recycle bin,
  those with other members are transferred, and the rest are left), and their
  group memberships in this organization and the tokens bound to this organization
  are removed together. A person can also leave an organization themselves (the
  last admin cannot leave). The site admin still sees and manages everything.
- **Requests carry the current organization**: every request from the web client
  carries `X-PetalBook-Tenant` (the organization's `slug` is also in the address).
  When it is absent, the one last used in the session is used. The server compares
  it with this person's membership set in memory (one lookup), and if the person
  is not a member it is treated as nonexistent. Endpoints addressed by space id do
  not look at it, and judge by the organization the space is in. The session is
  not bound to an organization, so two tabs can be open on different
  organizations.
- **Permission cache**: in the per-person cache of 1.7 / 3.2, each space also
  stores its organization, plus the person's membership set and the role in each
  organization (the ceiling for `viewer`). When membership changes, this person's
  cache is invalidated, the same as for group changes. The check on each request
  is still one lookup, and tokens get one extra comparison, "the space's
  organization equals the token's organization".
- **Tokens, OAuth, MCP**: an API token and an OAuth authorization are bound to one
  organization (or "Personal") when created, and afterwards reach only the spaces
  there. The authorization page lets the person choose the organization. MCP tools
  see only that organization's spaces. When the organization does not have the
  capability `integrations.tokens` / `.mcp`, tokens and authorizations bound to it
  answer `403 capability_off`.
- **An organization's security requirements govern only its own content**: the
  organization's `ssoOnly`, requiring a passkey and session length are checked
  when this person enters this organization. The session records, per
  organization, "what was used and when it was proven": if the organization
  enforces SSO and this session was not signed in through its SSO, entering this
  organization answers `403 tenant_sso_required` with the entry point, and signing
  in once through SSO makes it up, without signing in to the whole account again.
  The person's home and other organizations are not affected. The account's own
  sign-in rules (failure cap, lockout) are still site-wide, and the account-level
  session length follows the home.
- **Usage and quota**: `usage` rows are recorded by (tenant, person). A space is
  counted against the organization it is in and the share of the person who
  created it in this organization, and a personal space is counted against the
  home. The total rules for site and tenant (3.3) do not change.
- **Audit**: each row carries the organization it speaks of. An organization admin
  sees this organization's rows (members joining and leaving, roles, matters of
  this organization's spaces, sign-ins through this organization's SSO). Matters of
  the account itself (password, passkey, changing email) are written in the home
  and seen by whoever manages this account.
- **Admin page**: the organization's "Members" page reads the membership table and
  lists the role, how they joined, whether they are managed by this organization,
  and whether they are held. Operations on the account itself are shown only for
  people managed by this organization. The site admin's "Users" page lists each
  account's home and all its organizations.
- **Several things from 3.6 become multi-membership here** (the shape of the API
  does not change):
  - Automatic join by domain: an existing account is added to the organization at
    its next sign-in (or when an address on this domain is verified), and the
    person is told. They can leave (except managed accounts).
  - Applying to join: a signed-in person applies under "Organizations you can
    join", and the admin approves or rejects from the list as in 3.6.
  - Instant join through SSO: new accounts follow 3.6. For existing accounts see
    open question 1 below for the owner.
  - Invitation: when the invited address already has an account, accepting the
    invitation adds a membership row (3.6 answers `409 email_taken`). The
    invitation's response no longer differs by whether the address has an account.
  - Creating an organization by oneself: a signed-in person creates it from the
    switcher menu, 3 per person counted by account (3.6 counts by address), and
    `restrictOrgs` is checked against the domain of this person's verified email.
- **Managed accounts** (following Notion's managed users): turned on per verified
  domain, off by default. It needs this organization to have the capability
  `signin.managedAccounts` (when the capability is turned off, management stops and
  the accounts become personal accounts again), and also an enabled SSO provider
  that routes to this domain. When on, accounts whose verified email is on this
  domain belong to this organization (`held`, with this organization as the home):
  - They cannot create personal spaces, and cannot create other organizations
    (`403 managed_account`);
  - They must sign in through this organization's SSO, and password, passkey and
    social sign-in do not work (the site admin is not managed: skipped when turning
    on, and audited);
  - Joining follows the rules above;
  - Existing personal spaces are not deleted and the organization admin cannot see
    them. The person chooses "move into the organization" (it becomes an
    organization space, and the person is still its owner) or "export" (the person
    exports their own personal space, the export of 3.5, 7 days);
  - Before turning on there is a preview (how many accounts will be managed, how
    many are skipped because they are managed by another organization), and the
    second confirmation of 3.5 is needed. One audit record is written for each
    managed account, and the person sees an explanation at their next sign-in.
    When management is turned off the accounts become personal accounts again, and
    existing memberships are left alone.
  - When management is not turned on, people on this domain keep their own personal
    spaces and are also members of the organization (the owner's main use).
- **Migration** `0022_memberships.sql`, tested on both SQLite and PostgreSQL.
  Databases used only for development and demos can open it. No upgrade that keeps
  data is promised, and there are no "migrated" members or management: the rules
  are as written above.
- **Isolation**: the isolation table and three tests of 3.3 stay as they are, and
  the check of `INSERT … SELECT` changes from "two rows with the same `tenant_id`"
  to "this person has a membership row in the organization the space is in". A
  personal space can be shared only with people who are in some organization
  together with the owner. The fixed data adds an account that is in two
  organizations, and every route must answer separately by the current
  organization.

Not doing (reasons in parentheses):

- Telling organizations apart by host name (same as 3.3, still in 5.14).
- Moving spaces between organizations (still in 5.14; the "move into the
  organization" of managed accounts only moves from a person's home to an
  organization).
- Guests (people who are not members and enter only some spaces; the
  organization's `viewer` stands in for now).
- SCIM (still in 5.10).

Decided by the owner 2026-11-23:

1. An organization admin sees only the organization's spaces, and cannot see
   anyone's personal spaces, with no exceptions. Personal spaces can be seen only
   by the person and the site admin.
2. Only managed accounts are managed by the organization (`held`). All other
   accounts are personal accounts, and the organization manages only membership.

For the owner to decide (do it by the default in parentheses for now):

1. When an organization's SSO meets an existing personal account whose verified
   email is on a domain the organization has verified, can it sign straight in and
   add membership (yes, but the session obtained this way reaches only this
   organization, and entering the person's home and other organizations requires
   the account's own sign-in methods. It never signs in the site admin. The
   alternative is that the person must first sign in by their own method and
   confirm the link once).
2. Whether a managed account can also be a member of other organizations (yes:
   when management is turned on, existing memberships are kept, and later it can
   join other organizations by invitation, but cannot create organizations itself.
   The alternative is that turning it on removes them from other organizations and
   forbids joining any later, which gives the company stricter control over where
   data goes).

**The 3.8 contract** (2026-11-23): connections to GitLab and Gitea. The endpoints
and error codes are written in the new "Connections to GitLab and Gitea" section
of `docs/api.md`. Added by the owner 2026-11-23, and the design is written after
2.7b is merged.

Decided:

- **Two new provider types**, both implementing the provider interface of 2.7b and
  running the same contract tests:
  - `gitlab`: gitlab.com and self-hosted GitLab;
  - `gitea`: Gitea, Forgejo, and Codeberg (Forgejo on codeberg.org).
- **One OAuth app per host, configured by the admin**: the host address, client id
  and client secret (sealed with the server key, and never returned by any
  endpoint). The site admin configures the site's. A tenant with the capability
  `integrations.connections` can configure its own (for use only by people in
  this tenant). Before saving, the host is checked once (the instance's version
  endpoint, and one run of the token-exchange error path with the client
  credentials). The host must be in `storage.hosts`, and a public network check is
  done on the resolved address at connection time (the same outbound restriction
  as 3.6). There are at most 20 such providers per server and 5 per tenant. The UI
  gives the callback address to enter on GitLab / Gitea and the scopes to tick.
- **A user connects once**: authorization code plus PKCE, with the callback
  `/api/connections/{provider}/authorization/callback` (the same route as GitHub's,
  with a different `{provider}`). The access token and refresh token are sealed
  separately. They are renewed when close to expiry, once at a time per person,
  with the new pair written to the database before use, and once a day those
  expiring within 30 days are renewed. One external account (by host and account
  id) can connect to only one person on the server. On disconnect, a revocation is
  sent to the host as best effort.
- **Scopes**: only the minimum scopes needed to create repositories and to read
  and write them, fixed at implementation time according to each platform's
  documentation and written into `docs/api.md`. Tests use an in-process fake
  GitLab and fake Gitea, and do not connect to the outside.
- **No "installation"**: sync uses the token of the person who connected
  (`via: "user"`, with the rules of 2.7: it stops when that person cannot sign in,
  loses `manage`, or disconnects, and the space goes into `connection_missing`
  with nothing lost). A tenant's storage target uses the credentials the tenant
  configured (a GitLab group access token, or the token of a bot account in a
  Gitea organization, `via: "tenant"`).
- **Creating repositories**: in the person's own namespace, or in a group /
  organization where they can create projects. Always private. When the `slug` is
  taken, a suffix is added as in 3.6. The flow after that is the same as 2.7
  (create on disk first, create the remote, push, and write the record in one
  transaction; if it fails after the remote is created, the remote is kept and not
  deleted, and an empty-repository reminder is recorded). The attachment
  repository follows 3.1.
- **Partial sync**: automatic detection as in 3.5, recorded per host. GitLab and
  newer Gitea / Forgejo generally support it, and the detection result is what
  counts.
- **Lists are all paginated**, with at most one request to the host per page.
  GitLab and Gitea can filter by name, so there is a `q`.

Not done in 3.8: GitHub Enterprise Server and Bitbucket (they can still be used as
plain Git by address); webhooks; the device code flow (the client of phase 4).

Decided by the owner 2026-11-23:

1. GitLab (gitlab.com and self-hosted) and Gitea / Forgejo / Codeberg are done in
   their own milestone: one OAuth app per host, configured by an admin, and each
   user connects once.

**3.8 as built (2026-11-24)**: the contract above was done, and tenants
configuring their own providers and the tenant storage target are left to 3.6
(see "Not done" below). For the endpoints see "Connections to GitLab and Gitea" in
`docs/api.md` and its "As built".

- **Code**: the new package `internal/forge`. A `Provider` is one OAuth app on one
  host, and implements `connections.Provider` and `Administered` of 2.7b. The two
  platforms differ only in the `driver` (`gitlab.go`, `gitea.go`: the
  authorization address, token address, version probe, user, places where one can
  create, and creating, listing and reading repositories). Connecting, renewal,
  the token handed to Git and the admin page are all one copy. At startup the
  `Manager` reads all the providers from the database into the registry, creates
  new ones at the admin's request (`connections.Creator`), and renews once a day.
  The registry was therefore changed so that entries can be added and removed, and
  made locked (`Add`, `Remove`, `GetFor`, `ListFor`). `forgetest` is an in-process
  fake GitLab and fake Gitea (authorization code plus PKCE, refresh token
  rotation, REST, and Git over `git http-backend`), in `internal/forge/forgetest`.
  `conntest.Run` runs once for each of the two.
- **State database** (migration 0019, tested on both SQLite and PostgreSQL):
  `providers`, `provider_authorizations`, `provider_leftovers`. `providers.owner`
  is `site` or a tenant id, with `(owner, host)` unique. The GitHub tables are
  untouched. The `provider` column of `remotes` stores the provider id, and
  `gh_via` can only be `user`. The table list of the scope test gained these three
  tables.
- **Host check** (before saving, `PATCH`, `POST …/check`): the version endpoint
  answers in each platform's own way (for someone not signed in GitLab answers
  `401 {"message": "401 Unauthorized"}`, which also counts; Gitea answers the
  version number; Forgejo's version number carries `+gitea-X.Y.Z`, and the latter
  is what is compared). GitLab must be at least 15 and Gitea at least 1.19 (which
  has PKCE). Then one run is made against the token address with a fake
  authorization code: `invalid_grant` shows the host recognizes this client, and
  `invalid_client` is refused. A refusal is `400 invalid` plus `reason`
  (`host_invalid`, `host_not_public`, `host_unreachable`, `not_this_platform`,
  `unsupported_version`, `client_rejected`, `type_invalid`). GitLab does not tell
  the version to someone not signed in, so after the first person connects it is
  read again and recorded. If the version is too old, the provider is set to not
  configured.
- **Outbound**: every request from a provider to the host goes through one client.
  After resolution and before connecting, it checks that the address is public
  (reusing `dailyfetch.Public` of the daily text), it does not follow redirects and
  does not use a proxy from the environment. Only `--remote-unsafe` (for tests)
  allows plain http and internal addresses. The host also has to pass
  `storage.hosts` (`spaces.Manager.HostAllowed`, taken out of the old
  `hostAllowed`).
- **Scopes**: GitLab needs `api` (only that one can create projects;
  `read_repository` and `write_repository` cover only Git data). A Gitea OAuth app
  token is as broad as that person's own permissions, the authorization request
  carries no scope, and the admin page says so. The username used for Git is
  `oauth2`, with the token as the password.
- **Tokens and renewal**: the access token and refresh token are sealed separately
  (with the provider id and the person's id as the binding). Renewal follows the
  GitHub scheme: renew when within 5 minutes, one at a time per person, the new
  pair is written to the database before use, and a version number in the database
  prevents overwriting. GitLab refresh tokens do not expire. A Gitea refresh token
  is a JWT, and its `exp` is read (the signature is not verified, only so that the
  daily renewal knows which are about to expire). If the refresh is refused
  (`invalid_grant`), the connection is set to `expired`, and the remote of spaces
  using it is `connection_expired`: reads work as usual, writes get `503`, and
  drafts are kept. After reconnecting, `ConnectionFixed` wakes the space.
- **Creating repositories and the credentials for Git**: a personal namespace, or a
  group the person is allowed to create in (GitLab groups and subgroups, Gitea
  organizations, by the team's `can_create_org_repo`). `ChooseCredential` is always
  `via: "user"`: an OAuth token cannot be narrowed to one repository the way a
  GitHub installation token can, so this person's token is the credential for the
  space. A person's connection is used only for spaces where that person is a
  manager, and another person's connection is never borrowed (there is a test).
  `via: "app"` is `400 invalid` for them.
- **Partial sync**: on the first repository creation or connecting to an existing
  repository, each provider uses that person's token to request the Git protocol v2
  advertisement from the repository's `info/refs?service=git-upload-pack`, looks at
  whether `fetch=` has `filter`, and records it in `providers.partial_sync`, and
  looks again every 30 days after that. `capabilities.partialSync` and the admin
  page's `partialSync` take it. Once the per-host table of 3.5 is built, this column
  is replaced by it.
- **Web**: in the connection part of the admin page "Storage and sync", below the
  GitHub card is "Hosts": one card per host (name, host, whether it is usable,
  version, whether partial sync is supported, callback address, requested scopes,
  usage, check, edit, remove) and an "Add host" form. On the account page "Connected
  accounts", for these providers there is one "Connect X" button with no
  "installation", and the wording all uses the provider name the server gives
  (`src/i18n/forge`), and the UI looks only at `capabilities.installations`. The
  "Where it is stored" of a new space is already listed by capability, so the new
  providers appear directly. Brand icons are still only GitHub's (the rest is 3.9).
- **Audit**: `connection_provider_changed` (`created`, `saved`, `removed`),
  `account_connected`, `account_disconnected`, with no secrets in them.

How the contract was understood when it was built:

- **Tenants configuring their own providers is not done**. In the contract,
  `POST /api/admin/tenants/{tenant}/connections` needs the capability
  `integrations.connections`, and that belongs to 3.6. 3.8 did the groundwork it
  needs: `providers.owner`, `Descriptor.TenantID`, `Registry.GetFor/ListFor`, the
  provider itself refusing people from other tenants (`404`), the `owner` in
  `GET /api/connections` being `"site"` or `{"tenant": id}`, the admin list seeing
  everything, `(owner, host)` unique, and the tenant id in the secret's purpose.
  Tests write a row into the database and then `Reload` to verify. The routes, the
  cap of 5 (the constant `TenantLimit` exists but is unused) and the
  `tenant_connection_changed` audit are left to 3.6. The tenant storage target
  (`via: "tenant"`) is likewise 3.6, and `capabilities.storageTarget` is `true` for
  the two new providers.
- **Name conflicts**: the contract says "when the slug is taken, add a suffix as in
  3.6", and that is the storage target of 3.6. Here the `name_taken` (`409`) of 2.7
  is followed, with no automatic suffix.
- **`PUT` and `PATCH`** are both accepted on `/api/admin/connections/{provider}`,
  and both are "change whichever is given" (GitHub's `PUT` was already like that).
  The id, type and host cannot be changed. A duplicate id or host is `409 exists`,
  the cap is `409 provider_limit`, and on deletion, if connections or spaces are
  using it, it is `409 provider_in_use` with `connections` and `spaces`.
- **The public network check covers only the server's own requests to the host**
  (OAuth, REST, the partial sync probe). The `git` subprocess connects to the host
  itself, and does not go through it. That layer still relies only on
  `storage.hosts`, like other remotes. If 3.6 wants to govern Git's outbound
  traffic, it has to be done in gitstore's runner.
- **Providers live only in one process's memory**. With several processes sharing
  one database, a newly created provider appears in the other processes only at the
  next daily `Reload` (or restart), and deletion is the same. Several processes at
  the same time is not something 3.8 is to solve.
- **The migration number** used is 0019 (now the next free one on `main`). If 3.5
  also uses 0019, the one merged later changes its number.

Changed after the 3.8 review (2026-11-24, written in "As built (3.8)" of
`docs/api.md`):

- When the host answers `401` to a call with a token, the connection is no longer
  set to expired directly. First one renewal is made (one at a time per person), and
  if the renewal works, the call is made again with the new token. If another
  request has already swapped the token, the swapped one is used, and no further
  refresh token is spent. It expires only when the refresh token itself is refused,
  and only the version the token belongs to expires. GitHub's side (`userFailed` of
  `internal/github`) is written the same way. There are nine call sites, which were
  not changed this time, and that is noted below.
- Saving, checking and reloading providers no longer overwrite with the old whole
  row: saving reads the row only after it has the lock. Checking and the first
  connection that read the version write only the three columns status, version and
  time, and only when the client has not been changed. `Reload` reads the row under
  the lock. A check no longer writes a known version as empty. When the client id
  is changed, all connections are set to expired and tokens are deleted, and people
  connect again. Changing only the secret does not touch the connections.
- When a Gitea admin connects, the connection carries `admin: true`, and the web
  shows a warning in Chinese and English. Renewal follows the token's own lifetime:
  it renews only in the last tenth, so Gitea's default 730-hour refresh token is
  renewed about three days ahead, no longer once a day.
- What the host says has line breaks and control characters removed before it goes
  into errors and logs, its length is limited, and anything that looks like a token
  (including JWTs) is blanked out. When the provider is not loaded, the Git username
  is taken from the type stored in the database (`oauth2` for GitLab and Gitea), and
  no longer falls back to `x-access-token`.

Still open in the 3.8 as built (recorded, not changed):

- **Renewal across processes**: with several processes sharing a database, each may
  renew the same refresh token, and the later one fails. The design is one process,
  and this is not handled.
- **`provider_in_use` is not atomic**: between checking the number of connections
  and spaces and the deletion, another request can connect one more.
- **The wording of the detail** of `connection_provider_changed` in the audit has
  not been fixed.
- **`DeleteTenantConnProviderRows`** (when a tenant is deleted) belongs to 3.6.
- **The `git` subprocess connects to the host itself**, without the public network
  check (SSRF), and belongs to 3.6. For now it relies only on `storage.hosts`.
- **GitHub's `userFailed`** (`internal/github/repos.go`) has the same problem: one
  `401` expires it and deletes the refresh token, and the one expired is the current
  row, not the version the token belongs to. As `forge` does, the nine call sites
  would each have to retry once carrying their token. That is not a small change,
  and is done separately.

**The 3.9 contract** (2026-11-23): avatars and brand icons. The endpoints and error
codes are written in the new "Avatars and brand icons" section of `docs/api.md`.
Added by the owner 2026-11-23, and the design is written after 3.5 is merged (the
brand icon part depends on nothing else).

Decided:

- **Brand icons**:
  - GitHub's icon was already done in 2.7b (the Simple Icons path inlined into the
    web client). 3.9 adds the rest in the same way.
  - GitHub, GitLab, Gitea, Forgejo, Codeberg, Google, Microsoft and Apple use the
    official brand icons: the Simple Icons set (CC0, single-color SVG, with the
    color using `currentColor` so that it follows the theme). When a brand is not in
    Simple Icons (it takes icons down at a brand owner's request), the icon the
    brand owner publishes itself is used, handled in the same way. Trademarks still
    belong to each brand, and this is stated in `NOTICE`.
  - Used in: sign-in buttons, "Connected accounts", a space's remote, the "Storage
    and sync" page, and "Where it is stored" for a new space.
  - "Sign in with X" follows each company's brand guidelines (button text, color,
    spacing, minimum size, for example the sign-in button guidelines of Google,
    Microsoft and Apple), and does not change the brand's colors and shapes.
  - Icons are bundled with the web client (only the few that are used) and are
    **not fetched from outside**.
- **A person's avatar**: set by the person. The default is the first letter of the
  name plus a color (the color is taken from 12 by the person's id, with enough
  contrast in both the light and dark themes). One click can use the avatar of a
  connected account (2.7b, 3.8) or of a sign-in identity (the social sign-in of
  2.8): the server fetches it once (passing the outbound public network check),
  re-encodes it as it would an upload and stores it, and does not fetch it again
  afterwards. It is stored on the server (a file outside the state database,
  `DATA/avatars/`), and the state database records only the version.
- **A space's icon** (the avatar of a team space): set by the space's manager
  (`manage`). It can be an emoji, the name of a built-in icon, or an uploaded
  image. **It is stored in the space's own repository**: the image is
  `.petalbook/icon.png` or `.petalbook/icon.webp`, and the emoji and icon name are
  written in the `icon` of `space.yml`, and they travel with export, sync and
  moving. Changing the icon is an ordinary commit. In addition, a 64px small copy
  (at most 16 KB) is stored in the space's row in the state database, so when the
  space is evicted or archived the list still shows it.
- **An organization's logo**: set by the tenant admin (the site admin can too),
  stored on the server, the same as a person's avatar.
- **Rules for images**: every upload is re-encoded on the server as a 256px square
  PNG or WebP (PNG when there is transparency). The original accepted is at most
  1 MB, only PNG, JPEG, WebP and GIF are accepted (the first frame is taken), and
  **SVG is never accepted**. Before decoding, the size in the header is checked,
  and anything over 40 million pixels is rejected outright (to guard against
  decompression bombs). All metadata (EXIF and so on) is dropped. An image found in
  a repository (a `.petalbook/icon.*` that someone else pushed) goes through the
  same check and re-encoding before display, and if it does not pass it is treated
  as no icon, and one entry is reported on the validation page of 2.5.
- **How it is served**: `/api/avatars/…`, one address carrying a version number,
  with a long cache. The response carries the image type in `Content-Type`,
  `X-Content-Type-Options: nosniff` and
  `Content-Security-Policy: default-src 'none'`.
- **Gravatar is not used by default** (privacy: it would hand the hash of the email
  to a third party).

Not done in 3.9: animated avatars; icons for things other than spaces (folders,
pages); Gravatar.

Decided by the owner 2026-11-23:

1. Brand icons use the official ones (Simple Icons, CC0, single-color SVG, themable
   color), used in sign-in buttons, connections, remotes and the storage page.
   "Sign in with X" follows each company's guidelines. They are bundled with the web
   client and not fetched from outside.
2. A person's avatar is set by the person, defaulting to the first letter plus a
   color, with one click to use the avatar of a connected account, stored on the
   server. A space's icon is set by the space manager, and can be an emoji, an icon
   or an uploaded image, stored in the space's own repository, with a small copy
   kept in the state database. An organization's logo is set by the tenant admin and
   stored on the server. Every upload is re-encoded on the server as a 256px PNG /
   WebP, at most 1 MB, with no SVG accepted. Images in a repository are checked in
   the same way. Gravatar is not used by default.
