# Settings fold: capabilities, limits and stray site settings become layered settings

Status: plan, 2026-11-26, on main f379e41. PR 1 (section 6) is built; PRs 2 and 3 are not.
Open question 2 is decided (owner, 2026-11-26): a change of plan clears **every**
manual rule of the tenant, dated or not, and leaves billing's; the answer lists
`cleared` only. One place: `store.DeleteManualRules`. Round 1 of the review also
took the tenant layer off `quota.*` (section 3.1).

Owner decisions this plan implements (2026-11-25/26, approved):

- **One model.** A capability or a limit is the site layer's rule for one
  tenant: a lock or a restriction, with a **source** (`manual` > `billing` >
  `plan`) and an optional **window** (`from`, `until`). A plan is a bundle of
  such rules. Quotas are settings.
- Gone, with no migration or compatibility code: `internal/limits`,
  `tenant_limits`, the `/capabilities` and `/limits` endpoints and their web code.
- A restriction clamps the value in effect ("max 10 means max 10"). A site value
  above its own max is refused (`setting_above_max`, #87).
- Whole-feature switches are named `features.*`. `integrations.smtp` and
  `mail.ownSender` are one capability, `mail.ownSender`, default **off**.
- The stray site settings fold in too: machine access, security, storage policy,
  tenancy, daily and the GitHub App.
- Efficiency first: one map read per request, no per-request query a cache can
  answer, atomic snapshots and generations as today. SQLite and PostgreSQL are
  both first-class.

## 1. Inventory (main f379e41)

### 1.1 The layered settings today

`internal/settings` is the engine to keep. It has:

- definitions (`registry.go:71`), with `Kind`, `Layers` and exactly one of a
  tenant layer, `ForTenant` or `ServerWide` (`def.go:82`);
- the `Lower` modes allow, restrict and lock (`def.go:53`), with `Allows`,
  `Nearest` (the clamp from #87, `def.go:288`), `Tightens` and `Combine`;
- three rows per key, in `layered_settings` (`schema.sql:411`):
  - the site's row (`site`, `''`);
  - the site's rule for one tenant (`site`, tenant id). `siteRuleOf`
    (`tenant.go:33`) is the one place that reads it, and its comment already
    says the source and window go there;
  - the tenant's own row (`tenant`, tenant id);
- resolution in `tenantEffective` (`tenant.go:132`): the site lock beats the
  site's rule for the tenant, which beats the tenant's value, then the site's
  value for the tenant, then the site's value. Then `Nearest` clamps the result;
- the cache (`service.go:48`): `siteGen` plus one generation per tenant slot, and
  an immutable snapshot per tenant. Tenants with no rows for a key share the
  site's `follow` entry. A warm read is one `sync.Map` load, two atomic loads and
  a map lookup;
- site bindings (`service.go:18`). Values live elsewhere: `files.maxBytes` comes
  from `--max-upload` (`service.go:86`), `quota.spacesPerPerson` from
  `--max-spaces` (`api/usage.go:35`), and `daily.own` and `daily.api` from the
  daily record (`api/dailysettings.go:21`);
- the column `layered_settings.site_locked` (`store/layered.go:17`, `:91`), which
  nothing has set since #87 moved per-tenant locks into the site row for that
  tenant. It is dead.

Registered keys (`registry.go:10-57`):

- registration: `registration.open`, `.domains`, `.perHour`;
- legal: `legal.termsUrl`, `.privacyUrl`;
- daily: `daily.own`, `.api`, `.default`;
- `offline.copies`;
- `files.maxBytes`;
- `recycle.days`;
- exports: `exports.days`, `.maxBytes`;
- users: `users.reuseUsernames`, `users.emailNonAscii`;
- storage: `storage.assets`, `storage.assets.ownStorageExempt`;
- quotas: `quota.spacesPerPerson`, `quota.bytesPerPerson`, `quota.pagesPerPerson`.
  Their person layer is `users.quota`, read by `internal/quota`.

### 1.2 The limits sheet (to remove)

| What | Where | Read by |
|---|---|---|
| Registry: 17 booleans, 1 list, 13 limits | `internal/limits/registry.go:69-100` | — |
| The site's default values | `site_settings['capabilities']` (`store/limits.go:10`, `limits/service.go:244`, `limits/admin.go:280`) | `limits.Service` |
| Plans | `plans` (`schema.sql:23`), `tenants.plan_id` | `limits.Service.load` |
| Overrides with source and window | `tenant_limits` (`schema.sql:51`), primary key (tenant, key, source), `manual` or `billing` | `limits.Service`. One timer for the next boundary (`limits/service.go:519-590`) |
| In-memory sheets | `limits.Service`, an atomic map of immutable `*Sheet` | see below |
| `On(tenant, cap)` | `limits/service.go:373` | `authz.Decide` (`authz/subject.go:176`). Only `tenant.data.view` names a capability (`authz/keys.go:46`, `data.adminView`) |
| `sync.assetsRepository` | `api/assetspolicy.go:21`, `api/assets.go:113` | `settings.AssetsInput.RepositoryOff` (`settings/assets.go:17`) |
| Tenant totals `other.people`, `storage.quota.*` | `quota/quota.go:107` (`TenantFrom`) | `quota.Service`, `api/tenants.go:50` (`quotaOf`), `api/limitsapi.go:106` |
| Endpoints | `api/routes.go:256-267`, `api/limitsapi.go` (`/api/admin/capabilities`, `…/tenants/{t}/capabilities`, `…/tenants/{t}/limits`, `…/tenants/{t}/plan`, `/api/admin/plans…`) | web |
| Wiring | `api/api.go:288-296` (`sheets.New`, `s.caps`, `quota.UseSheets`), `api/tenants.go:466` (`Forget`), `api.Handler.Limits()` (`api/api.go:187`, no caller outside tests) | — |
| Audit kinds | `capability_changed`, `tenant_limit_changed`, `capabilities_default_changed`, `plan_created`, `plan_changed`, `plan_deleted` (`api/limitsapi.go:282-627`) | audit page |

Defined but read by nothing (reserved):

- `signin.*`;
- `data.auditDownload`;
- `integrations.*`;
- `other.offline`, `other.registration`;
- `storage.modes`, `storage.maxFileBytes`, `storage.pinnedBytes`,
  `storage.perPerson.*`;
- `usage.*`.

### 1.3 Stray site settings (outside the settings service)

All of these are `site_settings` rows (`schema.sql:400`), each with its own
cache:

| Key | Holds | Owner, cache | Readers |
|---|---|---|---|
| `machine_access` | `{tokens, mcp}` | `tokens/service.go:34`, `:430`. Mutex plus 30 s TTL | `api/tokens.go`, `api/mcp.go`, `oauth/service.go`; `GET/PUT /api/admin/access` (`api/routes.go:237`) |
| `security` | login mode and `ssoOnly`, passkeys `enabled` and `requireRoles`, captcha provider, site key, sealed secret and per-context rules, lockout, rate limits and session days (`auth/security.go:50-80`) | `auth/security.go:182`. Atomic pointer, revision check every `CacheTTL` (5 min) so the CLI `security reset` (`cmd/petalbook/security.go`) is seen | `auth/login.go`, `captcha.go`, `passkeys.go`, `account.go`, `emailflows.go`, `session.go`, `providers.go`; `GET/PUT /api/admin/security` (`api/signin.go:73`) |
| `storage` | `remotes.require`, `.scope`, `.graceDays` and `.since`; `hosts.allowed`; `localSpaces.maxBytes`; `cache.*` (`spaces/storage.go:46`) | `spaces/storage.go:152`. Read once, then held | `spaces/evict.go`, `remote.go`, `assets.go`, `partial.go`; `GET/PUT /api/admin/storage` (`api/routes.go:191`, `:194`) |
| `tenancy` | `{enabled}` | `api/tenants.go:23`, `:90`. **Read from the database on every call** (tenant create and list) | `GET/PUT /api/admin/tenancy` (`api/routes.go:249`) |
| `daily` | default, languages, allow (`own`, `api`), the administrators' interfaces, the built-in providers' state (`api/dailyapis.go:41`) | `api/dailyapis.go:100`. Two of its fields are bound into settings | `/api/admin/daily*` (`api/routes.go:215-224`) |
| `github_app` | App id, sealed key, secret and webhook secret, observed slug, owner, permissions and gaps (`github/github.go:170`) | `github/github.go:225`, compare-and-set refresh (`github/admin.go:557`) | `/api/admin/connections/github…` |
| `mail` | the SMTP sender: host, port, user, sealed password, from (`auth/mailcfg.go:22`) | `auth/mailcfg.go:71` | `/api/admin/mail` |
| `oidc_imported`, `passkey_fake_key`, `first_user` | one-time markers and a sealed key | `auth/config.go:12`, `auth/passkeys.go:30`, `store/users.go:236` | internal |

Also outside settings, and staying outside:

- per-installation tenant sharing of the GitHub App
  (`PUT …/installations/{id}`, `api/connections.go:646`). This is data, not a
  rule;
- `sign_in_providers`. It moves with consolidation step 5 (the providers table).

Hot-path findings worth fixing on the way:

- `tenancyEnabled` runs a query on every call;
- `bindDraftFileLimit` (`api/filelimit.go:36`) runs `store.UserTenant`, one query
  per draft write, to find the tenant.

## 2. Target data shape

### 2.1 One table of rules

`layered_settings` gains a source and a window. `plans.settings` and
`tenant_limits` go. The baseline is edited in place, and dev databases are
recreated.

```sql
CREATE TABLE layered_settings (
  layer    TEXT COLLATE "C" NOT NULL,  -- 'site' | 'tenant' | 'plan'
  layer_id TEXT COLLATE "C" NOT NULL,  -- site: '' or a tenant id; tenant: tenant id; plan: plan id
  key      TEXT COLLATE "C" NOT NULL,
  source   TEXT COLLATE "C" NOT NULL DEFAULT 'manual'
           CHECK (source IN ('manual', 'billing', 'plan')),
  value    TEXT COLLATE "C" NOT NULL DEFAULT '',   -- JSON, '' = none
  lower    TEXT COLLATE "C" NOT NULL DEFAULT '',   -- JSON Lower, '' = none
  starts   BIGINT,                               -- ms, NULL = always
  ends     BIGINT,                               -- ms, NULL = never; counts in [starts, ends)
  set_by   TEXT COLLATE "C" NOT NULL DEFAULT '',  -- user id, 'billing', 'cli', 'profile:cn'
  updated  BIGINT NOT NULL,
  PRIMARY KEY (layer, layer_id, key, source),
  CHECK (ends IS NULL OR starts IS NULL OR ends > starts)
);
CREATE INDEX layered_settings_starts ON layered_settings(starts) WHERE starts IS NOT NULL;
CREATE INDEX layered_settings_ends   ON layered_settings(ends)   WHERE ends IS NOT NULL;
-- one row; every settings write bumps it in its transaction (see 2.5)
CREATE TABLE settings_rev (id BIGINT PRIMARY KEY CHECK (id = 1), rev BIGINT NOT NULL);
```

- **Rule row** = (key, tenant, value, lower, source, from, until). It is
  `layer='site'` with `layer_id` set to the tenant. `lower` is a lock or a
  restriction, `value` is the site's value for that tenant, and either may be
  empty.
- **The site row** (`site`, `''`) and **the tenant's own row** (`tenant`, id) have
  `source='manual'` and no window. Windows exist only on rules for a tenant.
- **A plan's rule** is a row with `layer='plan'`, `layer_id` set to the plan id
  and `source='plan'`, with no window. `plans` keeps `id`, `slug`, `name` and
  `description`. A plan does not store its own copy of the rules.
- `site_locked` is dropped. Partial indexes work the same on SQLite and
  PostgreSQL.
- **One row per (tenant, key, source).** A dated manual rule replaces an undated
  one, as today, with the warning `undated_value_replaced`. When the window ends
  the rule falls to billing, then the plan, then nothing. It does not return to
  the old manual rule.
- **A rule that has already ended is refused** (`400 setting_until_past`), a
  manual rule and billing's alike: it would never count.

### 2.2 Resolving the site's rule for a tenant

`siteRuleOf` becomes `ruleOf(def, manual, billing, plan, now)`. It returns:

- the rule in effect: the first **active** row, in the order manual, billing,
  plan. A row is active when `starts <= now < ends`, and a NULL bound always
  passes;
- `underlying`: the next active row, shown in the UI;
- `next`: the earliest `starts > now` or `ends > now` among those rows.

A whole rule replaces the one below it: a manual rule's value and lower both win
over the plan's, and the two are not merged field by field. Everything after
that is unchanged from #87:

> site lock over everyone > the rule in effect for the tenant (a lock forces its
> value, a restriction bounds the tenant) > the tenant's value > the rule's
> value > the site's value. The result is then clamped by `Nearest` to the
> restriction the tenant meets.

The order the owner set for `storage.assets` (site lock > per-tenant lock >
tenant default > site default > space owner) is this same order.

### 2.3 How a plan expands to rules

- A tenant's plan is `tenants.plan_id`. Add `PlanID` to the cached `TenantRef`
  (`store/tenants.go:240`) so no query is needed.
- `computeTenant` takes the plan's rule rows from an in-memory **plan snapshot**:
  every `layer='plan'` row, loaded once. That is at most 100 plans × about 70
  keys. It has its own `planGen`.
- Writing a plan bumps `planGen`. Every tenant snapshot records the `planGen` it
  was built at, so tenants on that plan recompute lazily. Tenants on other
  plans recompute too, which is cheap (see 2.6), and plan edits are rare.
- **Sharing.** A tenant with no manual or billing rule and no row of its own uses
  the plan's shared map (built once per plan, site generation and plan
  generation), just as tenants with no rows share the site's `follow` map today.
- `PUT …/tenants/{t}/plan` sets `plan_id` and clears every manual rule of the
  tenant, an undated adjustment and a dated grant alike (billing's stay), and
  lists them in the answer as `cleared` (open question 2, decided). It bumps
  that tenant's generation. A grant that should outlive the change is set again
  after it.
- The map a plan's tenants share is labelled with the generation of the plan
  rows it was built from (`planSnapshot.gen`), never the generation read later:
  a plan written meanwhile must not find its old rows under its new generation.
- A write by another process (seen through `settings_rev`) moves both the site
  and the plan generations and reads the tenants again, so a tenant's `plan_id`
  changed there is seen too.

### 2.4 Expiry without polling

- **Reads are correct without a timer.** Each snapshot stores `next` (Unix ms;
  `MaxInt64` when nothing is dated). `snapshotOf` treats a snapshot as current
  while the generations match **and** `now < next`. That costs one clock read
  and one comparison per request. A window that starts or ends makes the next
  read recompute that one tenant.
- **Side effects need one timer.** These are the audit entry for a boundary, the
  quota re-evaluation and notices. After every write, and when the timer fires,
  the server runs
  `SELECT MIN(starts) … WHERE starts > ?` and `SELECT MIN(ends) … WHERE ends > ?`
  (index seeks on the partial indexes) and arms one `time.Timer`.
- **On fire**, one indexed query finds the tenants whose bound is in
  `(last, now]`. The server then:
  - invalidates them;
  - writes `setting_rule_started` or `setting_rule_ended` (no actor);
  - re-evaluates quotas for them;
  - re-arms the timer.

  With nothing dated there is no timer. The clock is injected (`SetClock`), so
  tests do not sleep.
- Only the active server runs the timer, like the other background jobs. A
  standby reads correctly through `next`.

### 2.5 Changes from another process

The CLI `security reset`, `serve --profile cn` and a standby server write the
database directly, so the in-process generations are not enough. Every settings
write runs `UPDATE settings_rev SET rev = rev + 1` in its transaction. `For`
checks `settings_rev` **at most once per `RevCheck` (60 s), lazily on a request**:
a CAS on `lastCheck`, so one request pays one point read and concurrent
requests do not. If another process moved the revision, `siteGen` is bumped.
The cost is at most one indexed read per minute per busy process, and none when
idle. This replaces the 5-minute revision check of security
(`auth/security.go:182`) and the 30 s TTL of machine access
(`tokens/service.go:423`).

### 2.6 Cost

**Warm read**, as today plus one time comparison:

- one `sync.Map` load;
- three atomic loads (site, tenant, plan);
- the `now < next` check;
- one map lookup.

No lock, no query.

**Recompute of one tenant:**

- two primary-key prefix scans, (`site`, tid) and (`tenant`, tid), each returning
  only the rows that tenant has;
- the plan rules come from memory;
- O(keys) work, about 70 keys: tens of microseconds.

**Memory:**

- tenants without their own rows share the site or plan map;
- a tenant with *k* rows of its own adds about *k* × 300 B. A thousand tenants
  with ten rules each is about 3 MB;
- the plan snapshot is at most about 7,000 rows, about 2 MB, and in practice
  far less.

**Per-request queries this removes:**

- `tenancyEnabled`, which becomes a map read;
- the tenant lookup in `bindDraftFileLimit`, which is taken from the session's
  user or the `TenantRef` cache.

**Derived state** (the security state, the storage policy, the assets rule) is
rebuilt only when the generation it was built at changes. Each one is an atomic
pointer keyed by `siteGen`, as the security state is today.

### 2.7 Billing and authorisation

- `settings.BillingWriter` replaces `limits.BillingWriter`. It is the same Go
  interface, writing rows with `source='billing'`, and nothing implements it
  yet.
- `authz.Capabilities` is implemented by the settings service:
  `On(tenant, key)` = the boolean value in effect for that tenant, one map read.
  `Info.Capability` is renamed `Feature`, and `tenant.data.view` names
  `features.adminView`.

## 3. Key renames and new keys

"Layers" uses: S = site, T = tenant, Sp = space, P = person.
`S+rule` = site layer plus a rule per tenant (`ForTenant`). `S!` = server-wide,
with the reason given in the registry.

### 3.1 From the limits sheet

| Old | New | Kind, layers, default |
|---|---|---|
| `signin.sso` | `features.ownSso` | bool, S+rule, `true` |
| `signin.domains` | `features.domains` | bool, S+rule, `true` |
| `signin.managedAccounts` | `features.managedAccounts` | bool, S+rule, `true` |
| `signin.idpAdmins` | `features.idpAdmins` | bool, S+rule, `false` |
| `signin.socialChoice` | **dropped**: becomes the tenant layer of `signin.social` (3.6 d), held by a per-tenant lock | — |
| `data.adminView` | `features.adminView` | bool, S+rule, `true` |
| `data.auditDownload` | `features.auditDownload` | bool, S+rule, `true` |
| `integrations.smtp` | `mail.ownSender` (owner's name) | bool, S+rule, **`false`** |
| `integrations.captcha` | `features.ownCaptcha` | bool, S+rule, `true` |
| `integrations.connections` | `features.ownConnections` | bool, S+rule, `true` |
| `integrations.daily` | `features.ownDaily` | bool, S+rule, `true` |
| `integrations.tokens` + `machine_access.tokens` | `features.tokens` | bool, S+T, `true` |
| `integrations.mcp` + `machine_access.mcp` | `features.mcp` | bool, S+T, `true` |
| `other.offline` + `offline.copies` | `features.offline` | bool, S+T, `true` (the endpoints answer `403 feature_off`, step (b)) |
| `other.registration` + `registration.open` | `features.registration` | bool, S+T, `false`, site lower lock |
| `sync.assetsRepository` | **dropped**: a per-tenant rule restricting `storage.assets` to `[space]`. `AssetsInput.RepositoryOff` goes | — |
| `storage.modes` | `storage.mode` | choice `server` / `own` / `tenant`, S+T. Restrict = the allowed modes |
| `storage.maxFileBytes` | **dropped**: a per-tenant rule restricting `files.maxBytes` | — |
| `storage.perPerson.*` | **dropped**: per-tenant rules restricting `quota.*PerPerson` | — |
| `storage.quota.spaces`, `.bytes`, `.pages` | `quota.spaces`, `quota.bytes`, `quota.pages` | limit or null, S+rule, null. **No tenant layer** (review round 1): a tenant's own value ranks above the rule's, so its administrators could raise their own totals. Set by a manual, billing or plan rule only |
| `other.people` | `quota.people` | as above (counted and shown, not refused) |
| `storage.pinnedBytes` | `storage.pinnedBytes` | limit, S+rule, `0` |
| `usage.apiPerMinute`, `usage.mcpPerMinute`, `usage.mailPerDay`, `usage.outboundPerMinute` | `rate.apiPerMinute`, `rate.mcpPerMinute`, `rate.mailPerDay`, `rate.outboundPerMinute` | limit or null, S+rule, null |

### 3.2 From the stray records

| Old | New | Kind, layers |
|---|---|---|
| `tenancy.enabled` | `features.tenancy` | bool, S! (whether tenants other than the default exist) |
| `security.login.mode` | `signin.mode` | choice, S! (the sign-in page comes before any tenant; a tenant layer arrives with 3.6 d) |
| `security.login.ssoOnly` | `signin.ssoOnly` | bool, S! (tenant layer with 3.6 d) |
| `security.passkeys.enabled` | `features.passkeys` | bool, S! |
| `security.passkeys.requireRoles` | `signin.passkeyRoles` | list, S! |
| `security.captcha.contexts` | `captcha.contexts` | doc, S!, validated by `auth` |
| `security.captcha.provider` | `captcha.provider` | choice, S! |
| `security.lockout.*` | `signin.lockout.enabled`, `.minutes`, `.scope` | bool / limit / choice, S! |
| `security.limits.perMinute`, `.maxFailures`, `.windowMinutes` | `signin.perMinute`, `signin.maxFailures`, `signin.windowMinutes` | limits, S! |
| `security.limits.sessionIdleDays`, `.sessionMaxDays` | `sessions.idleDays`, `sessions.maxDays` | limits, S! (per-organisation lifetimes come with 3.7) |
| `storage.remotes.require`, `.scope`, `.graceDays` | the same keys | S+T, site lower lock; a tenant may only tighten (the registry's reserved note, `registry.go:62`) |
| `storage.localSpaces.maxBytes` | the same key | limit, S+T, lock |
| `storage.hosts.allowed` | `storage.hosts` | list, S!. Bound to `--remote-host`, read-only when the flag is given |
| `storage.cache.*` | `cache.partial`, `cache.evictAfterDays`, `cache.minIdleHours`, `cache.clonesBytes`, `cache.minFreeBytes`, `cache.assetsBytes` | S! (the server's own disk). `cache.partial` is read-only without Git partial-clone support |
| `daily.allow.own`, `daily.allow.api` | `daily.own`, `daily.api` | unchanged. The site binding goes: the value is a plain row |
| `daily.default` | `daily.default` | unchanged. The binding goes |
| `daily.languages` | `daily.languages` | list, S! |

### 3.3 What is not a setting

These stay as records, in `site_settings` renamed **`site_records`**:

- credentials and observed state: `github_app` (until it moves into the
  providers table in consolidation step 5), the `mail` SMTP sender, the
  captcha site key and sealed secret (in a `captcha` record);
- the daily interfaces and built-in provider state (in a `daily` record);
- `storage.remotes.since`: the time the requirement took its present form. It is
  observed state, written when the effective `storage.remotes.require` changes;
- `oidc_imported`, `passkey_fake_key`, `first_user`.

None of these has layers, and a settings GET must never return a secret (open
question 1).

### 3.4 Reserved for step (b) (shape only, not built here)

- `features.*` is the feature registry. A route entry gains `feature`, and
  `Decide` answers `403 feature_off {"feature": key}`. This fold already renames
  `capability_off` to `feature_off`, so (b) only adds the route field to more
  routes.
- `GET /api/settings` returns the `features` map in effect, with `useFeature(key)`
  in the web. Today `offline.copies` is read the same way
  (`web/src/lib/offline/allowed.ts:18`).
- `serve --profile cn` on an empty database writes ordinary site rows with
  `set_by='profile:cn'`. The `set_by` column carries that.
- `storage.assets.ownStorageExempt` (exists, default on).
- `accounts.multipleEmails` (bool, S+T, default off).
- `users.emailNonAscii` (exists, S!).
- The `storage.assets` default `repository` (exists), with the precedence in 2.2.

## 4. API changes

**Removed** (404, no aliases):

- `GET/PUT /api/admin/capabilities`
- `GET/PUT /api/admin/tenants/{t}/capabilities`
- `GET/PUT /api/admin/tenants/{t}/limits`
- `GET/PUT /api/admin/access` (now `features.tokens` and `features.mcp`)
- `GET/PUT /api/admin/tenancy` (now `features.tenancy`; the tenant count moves
  onto `GET /api/admin/tenants`)

**Changed:**

- `GET /api/admin/tenants/{t}/settings`: each entry's `siteRule` becomes `rules`:
  `[{source, value, lower, from, until, inEffect, setBy}]` (at most three), plus
  `next` (RFC 3339, or absent). `PUT …/settings/{key}` takes
  `siteRule: {value, lower, from, until}`. The API always writes `source:
  manual`; billing writes only through the Go interface. `siteLock` stays as
  the short form.
  - `400 invalid`: `until` not after `from`.
  - `400 setting_above_max`: the value is above the rule's own max.
  - The answer carries the `undated_value_replaced` warning when it applies.
- `GET/POST/PATCH/DELETE /api/admin/plans…`: `values` becomes
  `rules: {key: {value?, lower?}}`, checked as a per-tenant rule. The codes
  `plan_in_use`, `plan_limit` and `affected` stay. `PUT /api/admin/tenants/{t}/plan`
  is unchanged (it answers `cleared`). Each moves from `withSheet`
  (`503 unavailable`) to plain routes.
- `GET/PUT /api/admin/security`: keeps only the captcha credentials, written to
  the `captcha` record. Everything else is under `/api/admin/settings`.
- `GET/PUT /api/admin/storage`: keeps the measured disk and cache state and
  `since`. The policy is under `/api/admin/settings`.
- `GET/PUT /api/admin/daily`: keeps interfaces, providers and lines. `default`,
  `allow` and `languages` are under `/api/admin/settings`.
- The tenant object's `quota` stays read-only, from `quota.*`.
- Error code `capability_off` becomes **`feature_off`** (field `feature`).

**Audit kinds:**

- `capability_changed`, `tenant_limit_changed`, `capabilities_default_changed`
  and `security_changed` become the existing `setting_changed`, with `source`,
  `from` and `until` added to its detail;
- new: `setting_rule_started` and `setting_rule_ended` (no actor);
- `plan_created`, `plan_changed` and `plan_deleted` stay;
- `tenant_limit_exceeded` becomes `quota_exceeded`, which already exists.

## 5. Web changes

- **Delete:**
  - `components/admin/TenantLimits.tsx` and its test, and the block in
    `TenantsPanel.tsx:378`;
  - `adminApi.tenantLimits` and `putTenantLimits` and their hooks
    (`api/admin.ts:224-228`, `:590-603`);
  - the `TenantLimits` and `LimitWrite` types (`api/types.ts:1961`);
  - `api/mock/limitsBook.ts` and its wiring in `tenantBook.ts:7`;
  - `adminApi.access` and `tenancy` and their hooks (`api/admin.ts:208`, `:310`).
- **Change:**
  - `SettingsPanel.tsx`, the tenant settings page: show the rules per source with
    their window and `next`, and edit the manual rule (value, lower, from, until).
    One editor serves every key, quotas and features included;
  - `AccessPanel.tsx`, `AdminSecurityPanel.tsx` with `securityDraft.ts`,
    `StoragePanel.tsx` with `StorageRules.tsx`, `DailyPanel.tsx`, and the tenancy
    switch in `TenantsPanel.tsx:53`: read and write `/api/admin/settings` keys.
    Each panel keeps its page; only the data source changes;
  - a plans page (list, and edit a plan's rules with the same editor);
  - the i18n key labels for the new names (`i18n/tenants/*`, `i18n/storage/*`),
    and `capability_off` becomes `feature_off` (`i18n/apiErrors/*`,
    `i18n/manage/*`, `api/types.ts:815`);
  - `lib/offline/allowed.ts` reads `features.offline`;
  - the mock server rules for the moved keys (`api/mock/server.ts`,
    `tenantBook.ts`, `manageBook.ts`).
- **Check:** every changed panel at 375 px and 768 px, with no horizontal scroll.
  The rule editor becomes a dialog, a bottom sheet on phones.

## 6. PR split (three PRs, each green on its own)

**PR 1: rules with source and window; the limits sheet removed.** Server and
the web code that removing it needs.

- `store`:
  - `schema.sql`: the `layered_settings` columns and indexes, `settings_rev`, no
    `tenant_limits`, `plans` without `settings`;
  - `layered.go`;
  - `tenants.go` (`PlanID` in `TenantRef`);
  - delete `limits.go` and its test.
- `settings`:
  - `tenant.go`: `ruleOf`, sources, windows, `next`;
  - `service.go`: plan snapshot, `planGen`, `next` check, `settings_rev` check,
    boundary timer, `BillingWriter`;
  - `registry.go`: the keys of 3.1, except those that need the stray records;
  - `assets.go`: `RepositoryOff` removed;
  - a new `plans.go`.
- `authz`: `Feature`, `feature_off` (`authz.go`, `subject.go`, `keys.go`).
- `quota`: `quota.go` reads `quota.*` from settings.
- `api`:
  - delete `limitsapi.go`; add `plans.go` (plans on rules);
  - `routes.go`, `api.go`, `settings.go` (rules in the tenant view),
    `assets.go`, `assetspolicy.go`, `tenants.go`, `authz.go`, `filelimit.go`.
- `internal/limits`: deleted.
- Web:
  - `TenantLimits*` deleted;
  - `api/admin.ts`, `api/types.ts`, the mock (`limitsBook` deleted);
  - `SettingsPanel.tsx`: show the rules read-only, plus a manual rule with a
    window;
  - the `feature_off` i18n.
- Docs: `docs/api.md` "Capabilities", "Plans" and "The limits sheet" become one
  section, "Rules for a tenant".

**PR 2: the stray site settings fold.**

- `auth`:
  - `security.go`: derived state from settings by generation; the policy part of
    `Security` goes; `ResetSecurity` writes rows;
  - `mailcfg.go`: the record rename;
  - `captcha.go`, `login.go`, `passkeys.go`.
- `tokens`: `service.go` loses `Access` and `SetAccess`; it reads
  `features.tokens` and `features.mcp` per tenant.
- `spaces`: `storage.go` gets its policy from an injected func built from
  settings, and `since` stays in a record.
- `api`:
  - `tenants.go`: tenancy becomes a setting;
  - `dailysettings.go` deleted; `dailyapis.go` and `dailysite.go` trimmed;
  - `signin.go` (security endpoint trimmed), `tokens.go` (`/admin/access` gone),
    `routes.go`.
- `store`: `site_settings` renamed `site_records`, in `daily.go` and `signin.go`.
- `cmd/petalbook/security.go`.
- Web: `AccessPanel`, `AdminSecurityPanel` with `securityDraft`, `StoragePanel`
  with `StorageRules`, `DailyPanel`, `TenantsPanel`, `lib/offline/allowed.ts`,
  the mock and the i18n.

**PR 3: the admin UI for rules and plans, plus contract tests.**

- Web: the rule editor dialog (source badges, window, underlying, next change)
  on the tenant detail page; the plans page; the site settings page grouped by
  prefix (`features`, `quota`, `rate`, `signin`, `storage`, `cache`, …) for
  mobile and tablet.
- `web/src/api/contract.test.ts` for the new shapes.
- `docs/api.md`: the "Security", "Storage" and "Machine access" sections reduced
  to their remaining endpoints.

PR 1 leaves site administrators able to set quotas and features per tenant
through the existing tenant settings editor (#87). PR 3 adds the comfort. PR 2
does not depend on PR 3. PR 1 and PR 2 both touch `registry.go` and
`routes.go`, so they run in sequence, not in parallel.

## 7. Test plan

Light, targeted. The full suite runs once per PR. The store and settings tests
run on SQLite and PostgreSQL (`PETALBOOK_TEST_POSTGRES`).

- **`settings` unit tests** (table-driven, injected clock):
  - source precedence: manual > billing > plan > none;
  - a window that has not begun, is active, or has ended;
  - `underlying` and `next`;
  - the site's lock beating a manual rule;
  - the clamp under a plan restriction ("max 10 means max 10");
  - a plan rule's value above its own max refused;
  - a read after `next` recomputes one tenant without a timer;
  - a plan write recomputing only through `planGen`;
  - a `settings_rev` bump from a second `Service` on the same database being
    seen after `RevCheck`.
- **`store` tests:**
  - the primary key (one row per source);
  - the `ends > starts` check;
  - the min-boundary queries using the partial indexes (an EXPLAIN test as in
    `storage_explain_test.go`);
  - cascade on tenant delete: rows with `layer_id` = tenant are deleted in
    `DeleteTenant`, because there is no foreign key on a shared column.
- **`api` tests** (a few):
  - a manual quota rule with a window: the tenant answers 409/403 inside it and
    not after it;
  - a plan with a restriction, a tenant on it, a manual override, then a plan
    change that clears it;
  - `features.adminView` off gives `403 feature_off`;
  - `mail.ownSender` defaults off;
  - the removed routes answer 404;
  - the boundary timer writes `setting_rule_ended` once;
  - for PR 2: `security reset` from the CLI seen by a running server, and
    `features.tokens` off for one tenant refusing a token there only.
- **Web** (vitest): the rule editor (window validation, source display); the
  panels of PR 2 against the mock. Check the changed pages at 375 px and 768 px.
- **Benchmarks:** run `BenchmarkFor` warm before and after on korn. It must stay
  allocation-free, and the `now < next` check must add nanoseconds only.

## 8. Open questions (product or security)

1. **Credentials stay out of the settings.** The GitHub App key, the SMTP
   password and the captcha secret would sit in `site_records`. Settings only
   hold policy, so no settings read can ever return a secret.
   Recommended: yes.
2. **Changing a tenant's plan clears its manual rules.** Decided (owner,
   2026-11-26): every manual rule goes, undated and dated alike; billing's
   rules stay; the answer lists `cleared` only. (The recommendation had been
   to keep the dated grants; it was not taken.)

## 9. Review round 1 (2026-11-26)

- **Allowances have no tenant layer.** A tenant's own value ranks above the
  site's rule for it (section 2.2), so a setting whose value *is* the tenant's
  allowance must have no tenant layer, or its administrators raise it. Checked:
  `quota.people`, `.spaces`, `.bytes`, `.pages` had one and lost it (S+rule);
  `features.*` (3.1), `mail.ownSender`, `storage.pinnedBytes`, `rate.*`,
  `storage.assets.ownStorageExempt` and `exports.days` were S+rule already.
  Kept with a tenant layer, because the allowance there is the site's
  *restriction* (a tenant value outside it is `setting_restricted`, and a
  tenant's own lower may only tighten): `storage.mode` (the allowed modes),
  `files.maxBytes` (`--max-upload` and per-tenant restrictions),
  `quota.*PerPerson` (the tenant's choice for its people, under the site's
  restriction), `features.registration`, `features.offline`, `registration.*`,
  `daily.*`, `legal.*`, `storage.assets`. For these a site rule that should hold
  must be a lock or a restriction; a value alone is a default the tenant may
  change, by design ("default or forced").
- **A plan or billing caps.** For a Limit with a tenant layer (`quota.*PerPerson`,
  `files.maxBytes`, `registration.perHour`), a plan's or billing's rule that
  gives a value must also give a maximum or a lock (`400
  setting_rule_needs_max`), as `storage.maxFileBytes` and `storage.perPerson.*`
  were hard ceilings on main. Manual rules keep the default-or-forced model.
- **Who set a rule** (`setBy`, a site administrator's id) is shown to site
  administrators only.
- **The boundary timer** tries again after `settings.BoundaryRetry` (1 minute)
  when the boundaries cannot be read, instead of arming nothing.
