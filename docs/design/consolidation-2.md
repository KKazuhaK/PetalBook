# Consolidation round 2: real-server contract, one jobs runner, one remotes table, one providers model

Status: plan, 2026-11-26, on main bd9a278. Nothing here is built.

Items 1 (notification kinds, one limiter) and 2 (settings fold) are done. This
plan covers items 3, 4 and 5 of the approved consolidation order. Item 6, the
`docs/api.md` rewrite, waits until 3.6 is finished.

| # | What | Must land before |
|---|---|---|
| 3 | Contract tests against the real server only; the web mock becomes fixtures; sign-in answers carry permissions | 4 and 5 (so their web changes touch fixtures, not mock rules) |
| 4 | One `jobs` table and one runner (bulk, exports, attachment moves, recycle actions); one `remotes` table keyed by (space, role) | 3.6 storage targets |
| 5 | One `providers` table and one `linked_accounts` table (sign-in, SSO, GitHub, GitLab/Gitea) | 3.6 (d) |

Rules that apply to every PR here:

- No migration or compatibility code. The baseline schema is edited in place,
  and each PR that changes it says "recreate dev databases".
- Both SQLite and PostgreSQL are first-class. Every `internal/store` change
  passes `make test-postgres` for the touched packages.
- Efficiency first. Each PR body states its cost at 50k pages and many users.
- Tests stay light: targeted runs only, no fixed sleeps, and each PR body says
  how long its new tests take.

---

## 3. Contract against the real server, mock reduced to fixtures, sign-in returns permissions

### 3.1 Current state

**The mock re-implements the server.**

- `web/src/api/mock/` holds about 16,400 lines of non-test code plus 1,244
  lines of tests of the mock itself.
  - `server.ts` alone is 5,747 lines. Its books (`manageBook.ts` 1,325,
    `tenantBook.ts` 1,374, `signInBook.ts` 865, `githubBook.ts` 694,
    `mailBook.ts` 556, `groupBook.ts` 535, `store.ts` 1,512, …) restate
    server rules: permission checks (22 `403` paths in `server.ts`, more in
    `manageBook.ts` and `tenantBook.ts`), validation, quotas, and the rate
    limiter (`server.ts:1024`, its own 429 with `Retry-After`).
  - It also restates clock-driven draft commits, remote states, the audit
    registry (`auditKinds.ts`) and the permission registry with its built-in
    role grants (`permissions.ts`, a hand copy of `internal/authz`).
- These mock self-tests assert server behaviour against the mock: `mock.test.ts`,
  `quota.test.ts`, `mail.test.ts`, `dailyCodes.test.ts`,
  `auditCategories.test.ts` and `notifyBook.test.ts`.

**The contract suite mostly checks the mock.**

- `web/src/api/contract.test.ts` has 8,956 lines and 352 `it`.
  - Without `PETALBOOK_API` it runs against the mock twice, as single-user and
    as accounts (`contract.test.ts:68-80`).
  - `npm test` includes it (the include in `vite.config.ts`), so every web run
    checks the mock.
- 16 cases or blocks are "mock only" and skipped on a real server:
  - OAuth consent: `:636`, `:689`, `:719`, `:749`;
  - GitHub: `:3070`;
  - attachment move: `:3677`;
  - created range: `:5597`;
  - ids: `:6746`;
  - draft clock: `:7498`, `:7526`, `:7543`;
  - remote states: `:8040`;
  - SSO and rate limits: `:8297`;
  - providers, passkeys, captcha and audit: `:8512`.
- `scripts/contract.sh` runs the real binary single-user, then accounts.
  `scripts/contract-postgres.sh` runs the accounts half on PostgreSQL.
  - In CI both run only in the `postgres` job (`ci.yml:256-257`), which a PR
    gets only when it changes Go.
  - A web-only PR that changes `contract.test.ts` or `api/types.ts` is never
    checked against the server.

**Who uses the mock.** 54 web test files use it through `src/testing/harness.tsx`
(`mount` builds `createMockServer`), `src/lib/offline/testkit.ts`,
`src/testing/listKit.tsx` and `src/editor/modelHarness.ts`. They use the server
object for:

- seeding and peeking: `server.mail` 27 uses, `server.peek` 26, `server.manage`
  11, `seedHandWrittenViews` 7, `userId`, `spaceIds`, `ids`;
- a few behaviours: `startAuthorization`, `captcha`, `sweep`, `push`,
  `damageType`, `forceKind`.

`web/dev/mock.ts` serves the same mock to `npm run dev:mock`, in the modes
single, accounts and setup.

**Sign-in does not say what the person may do.**

- `GET /api/session` returns `user.permissions`, built at
  `internal/api/accounts.go:151` from `permissionsOf` (`authz.go:116`).
- The sign-in answers return a bare `userResp`, without permissions:
  - `login` (`accounts.go:243`);
  - `setup` (`:227`);
  - `acceptInvite` (`:286`);
  - `passkeyFinish` (`signin.go:269`);
  - `registerConfirm` (`mailapi.go:245`).
- So the web guesses from the role name with a built-in role table,
  `builtIn()` in `web/src/lib/access.ts:13`, used by `can` (`:19`) and
  `administers` (`:27`).
- `applySignIn` (`web/src/api/hooks.ts:385`) then makes a second round trip,
  `GET /api/session`, to get the real keys. A custom role is wrong in the UI
  until that answer arrives.

### 3.2 Target

**(a) The contract suite runs only against the real server.**

- `contract.test.ts` loses its mock targets.
- `npm test` excludes it. `npm run test:contract` needs `PETALBOOK_API`, and
  without it the suite fails with "run make contract".
- Each mock-only case is either deleted, where a Go test already covers it, or
  ported to a Go `httptest` test in the package that owns the rule:

| Mock-only area | Go home |
|---|---|
| OAuth consent | `internal/api/oauth_test.go` and `internal/oauth` |
| GitHub | `internal/api/signin_github_test.go` and `internal/github` |
| Attachment move | `internal/api/assets_test.go` |
| Draft clock | `internal/api/drafts_test.go` and `internal/drafts` (hooks, no sleeps) |
| Remote states | `internal/api/remote_test.go` |
| SSO, limits, providers, passkeys, captcha | `signin_*_test.go` |

  PR 3c lists each case with its Go test, and a case with no Go test is ported
  before it is deleted.

**(b) Fixture shapes come from the real server.**

- `PETALBOOK_RECORD=1 make contract` writes the JSON **shape** of each response
  the suite reads to `web/src/api/fixtures/recorded/<route>.json`. A shape is
  the keys and JSON types, recursively, with arrays reduced to one element. No
  values are recorded, so nothing personal or random is checked in.
- A normal contract run compares every response with its recorded shape. A new
  key, a missing key or a changed type fails with "re-record". The recorded
  shapes are the single source the fixtures are checked against (see (c)).

**(c) The mock becomes a fixture layer** (`web/src/api/fixtures/`, about 2–3k
lines, down from 16k):

- `data/`: declared, typed seed data (from today's `seed.ts`), one file per
  area, typed by `api/types.ts`.
- `routes/<area>.ts`: a route table (method plus path pattern) that maps to
  generic operations on in-memory collections: list (with the same page/cursor
  envelope), get, create, update and delete. There are **no rules** in it: no
  permission checks, no validation beyond JSON parsing, no quotas, no rate
  limits, no audit, no clocks. The caller is the fixture's user, with the
  permissions the fixture declares.
  - **The protocol rules the fixtures keep on purpose.** The client's own
    concurrency and sync logic depends on these answers, so a fixture that
    left them out would let that logic go untested or break it:
    - draft versioning and its 409 (`routes/drafts.ts`): the editor's
      autosave and conflict handling read the version it is told;
    - a page save or property change with a stale rev answers 409
      (`routes/files.ts:374`, `:387`): the "changed elsewhere" flow;
    - a views write with a stale rev answers 409 with the current list
      (`routes/views.ts:28`): the same merge flow for saved views;
    - the sync feed: an expired cursor is 410 `changes_expired` and a bad one
      400 (`routes/sync.ts:54`, `:117`): the offline copy's resync;
    - an unknown username on sign-in is 401 (`routes/auth.ts:59`): the sign-in
      form's error state.
    - a fix of a problem with a stale `baseRev` is 409 (`routes/check.ts:45`):
      the check page's "changed elsewhere" state.
    Everything else the real server refuses stays out.
  - **Derived answers the fixtures keep on purpose.** Each is worked out from
    the data in `routes/admin.ts` because a test reads the result of its own
    write or of a scene it set up, so a seeded constant would make that test
    prove nothing (all five were tried as seeded values and 9 tests failed):
    - a tenant setting's `effective` (`:144`): the tenant rules and the forced
      values are tested by saving a rule and reading what is in force;
    - `deletionPlan` (`:162`): the heir flow (a shared space needs one, an
      owned space goes to the bin) follows the spaces the person owns;
    - the usage limits fallback to the site's quota settings (`:637`): the
      usage page shows numbers against the limits a setting gives;
    - `effectiveMode` (`:1309`): the security page's notice that a mode falls
      back to the password when no provider is enabled;
    - `mail.hasPassword` (`:1124`): the mail and secrets pages save a password
      and expect it to be reported as set, and never shown.
- `server.ts`: one `createFixtureServer({ data, user, overrides })`, with:
  - `fx.fail(method, path, status, {code, …})` and `fx.reply(method, path, body)`
    for a test that needs an error or an exact answer;
  - `fx.requests` for asserting what was sent;
  - `fx.sent.mail` and the like as plain arrays a route appends to.
  The error codes come from the generated code list (see (e)), so a test cannot
  invent one.
- A unit test checks every fixture collection against `recorded/` shapes. This
  is cheap: it walks a few hundred objects, with no server.
- `web/dev/mock.ts` serves the same fixture server. `dev:mock`,
  `dev:mock:accounts` and `dev:mock:setup` stay for UI work: pages render,
  lists page, and forms save and read back. Behaviour (refusals, drafts that
  commit themselves, quotas) is the real server's, reached with `npm run dev`
  against `serve`, using the demo's admin/admin123.

**(d) Component tests assert UI, not server rules.**

- A test that today relies on the mock refusing something declares the refusal
  with `fx.fail`.
- A test that peeks server state (`server.peek`, `server.manage`) asserts the
  request sent (`fx.requests`) or the collection it changed.

**(e) Registries are generated, not copied.**

- A Go test in `internal/api` writes `web/src/api/generated/registries.json`
  (with `-update`) and fails when it is out of date. It contains:
  - permission keys with scope and feature (from `authz.All()`);
  - audit kinds and categories;
  - error codes;
  - the setting keys.
- `mock/permissions.ts` and `mock/auditKinds.ts` are deleted.
- The web's own key type (`PermissionKey`) is derived from the generated file,
  so a renamed key fails typecheck.

**(f) Sign-in answers carry the session.**

- One builder, `s.sessionView(c, u, held)`, gives the same `user` object
  (with `permissions`) and `hold` as `GET /api/session`.
- Every sign-in answer uses it:
  - `login`, `setup` and `acceptInvite`;
  - `passkeyFinish`;
  - `registerConfirm` (`mailapi.go`);
  - the mail-link sign-in.
- Redirect flows (OIDC, SAML, GitHub; `finishOutcome`, `signin.go:217`) are
  unchanged: the page loads and reads `GET /api/session` as it does now.

### 3.3 API changes

- The sign-in answers return
  `{"user": {…userResp, "permissions": [...]}, "hold": null|"passkey"|"password"}`.
  This is the shape of `GET /api/session`'s `user` and `hold`.
  - `passkeyFinish` keeps `return`.
  - `registerConfirm` keeps `tenant`.
- The permissions are the keys held in the person's home tenant, which is the
  new session's `current_tenant`. The acting scope (#101) does not apply to a
  sign-in.
- Nothing else changes in the API.

**Cost.**

- The keys come from `auth.cachedEntry(userID)` (`internal/auth/session.go:181`),
  the same cache entry the next request reads. Filling it at sign-in moves one
  load earlier; it adds none.
  - The load reads `role_grants` by subject only while a custom role exists
    (`Store.CustomRoles`).
  - The names come from a 128-bit set (`Set.Names()`), well under a
    microsecond.
- The web drops the extra `GET /api/session` from `applySignIn`. Each sign-in
  saves one HTTP round trip and one session lookup.

### 3.4 Web changes

- `api/client.ts` and `api/types.ts`: the sign-in calls return `SessionUser`
  and `hold`.
- `api/hooks.ts` `applySignIn` sets the `session` query data from the answer
  instead of fetching it.
- `lib/access.ts`:
  - `builtIn()` is deleted;
  - `can` and `administers` read `permissions` only, and no permissions means
    no access;
  - `access.test.ts` keeps its source scan and adds one rule: no role-name
    comparison outside `adminKindOf`, which describes another account.
- `src/testing/harness.tsx`, `lib/offline/testkit.ts`, `testing/listKit.tsx`
  and `editor/modelHarness.ts` build a fixture server.
- The 54 test files move over by area (section 3.5). Then `src/api/mock/`, its
  self-tests and `contract.test.ts`'s mock path are deleted.
- `vite.config.ts` excludes `src/api/contract.test.ts` from `test.include`.

### 3.5 PR split

| PR | Content | Files | After |
|---|---|---|---|
| 3a | Sign-in answers carry `permissions` and `hold`; web uses them; `builtIn()` deleted | `internal/api/accounts.go`, `signin.go`, `mailapi.go`, a new `session_view.go`; `web/src/api/{client,types,hooks}.ts`, `lib/access.ts`(+test), `api/mock/signInBook.ts` (the answer shape only) | — |
| 3b | Generated registries (Go golden test) plus the web key type; `mock/permissions.ts` and `auditKinds.ts` read the JSON | `internal/api/registries_test.go`, `web/src/api/generated/`, `web/src/api/types.ts`, the two mock files | — (parallel with 3a: no shared files) |
| 3c | Contract suite real-only; mock-only cases ported or deleted with the list; shape recording and the shape check; CI runs the `postgres` job when `web/src/api/**` or `scripts/contract*.sh` change (the `changes` job's filter, no new job) | `web/src/api/contract.test.ts`, `web/vite.config.ts`, `scripts/contract.sh`, `.github/workflows/ci.yml` (filter only), `internal/api/*_test.go` (ports), `web/src/api/fixtures/recorded/` | 3a, 3b |
| 3d | The fixture layer; harnesses switch with an `options.fixtures` path; `dev/mock.ts` on fixtures; first area moved: auth pages, files, editor, views, offline (about 18 files) | `web/src/api/fixtures/{server,data/*,routes/{auth,files,views}}.ts`, `src/testing/*`, `lib/offline/testkit.ts`, `editor/modelHarness.ts`, `web/dev/mock.ts`, those tests | 3c |
| 3e | Area: admin console (users, tenants, manage, groups, security, storage, usage, recycle, plans, mail, access; about 20 files) | `fixtures/routes/admin.ts`, `fixtures/data/admin.ts`, `components/admin/*.test.tsx` | 3d |
| 3f | Area: settings, people, notifications, GitHub, onboarding, App (about 16 files) | `fixtures/routes/{settings,people,notify,connections}.ts`, those tests | 3d (parallel with 3e: disjoint route and test files) |
| 3g | Delete `web/src/api/mock/` (books, self-tests, `sha1`/`sha256` helpers if unused) and the harness's mock path | `web/src/api/mock/**`, `src/testing/harness.tsx` | 3e, 3f |

3e and 3f never edit `mock/server.ts`: the old mock is only deleted, once, in
3g. That is what lets them run side by side.

### 3.6 Test plan

- **3a.** A Go test signs in through each of the five JSON endpoints and checks
  that `permissions` equals `GET /api/session`'s, for:
  - a member;
  - a tenant admin;
  - a site admin;
  - a person with a custom role through a group (from `authz_part2_test.go`).
  It runs on both DBs, under 1 s. A web test checks that `applySignIn` makes no
  second request.
- **3b.** One golden test, about 50 ms.
- **3c.**
  - Speed: `make contract` (SQLite) and `make contract-postgres` now carry
    every contract case; measure both in the PR. The target is under 90 s
    each on a runner. The suite already parallelises nothing across files and
    uses run-unique names, so it needs no fixed waits beyond the existing
    "wait for the version".
  - The ported Go tests use the packages' hooks and `t.Parallel`, each under
    1 s.
  - `npm test` gets faster: 352 contract cases times 2 mock modes leave it.
- **3d–3f.** The moved component tests must pass unchanged in what they assert
  about the UI. The fixture server costs less per request than the mock (no
  hashing, no rule checks), so the web suite's time should drop; record before
  and after in each PR.

---

## 4. One jobs table and runner; one remotes table

### 4.1 Current state

**Four background-work systems, each with its own concurrency rule and restart
rule.**

| Work | Table | Concurrency | On restart | Code |
|---|---|---|---|---|
| Bulk actions | `bulk_jobs` (`schema.sql:1018`) | `jobRunner`: `sync.Cond`, 1 per tenant, 2 per server (`api/bulk.go:27-90`) | marked `interrupted` (`bulk.go:210`, called at `api.go:285`) | `api/bulk.go:301 runJob` |
| Exports | `exports` (`schema.sql:991`) | channel semaphores: 1 per tenant, `maxExportsAtOnce`=2 (`spaces/export.go:42`, `:80`, `:131`) | marked `failed`, temp zip removed (`export.go:536`) | `export.go:158 runExport`; expiry in `ExpireExports` (`:520`) from `recycleRound` |
| Attachment moves | columns `asset_remotes.move_*` (`schema.sql:356-393`) | `as.moving` map, no limit (`spaces/assets_move.go:118`) | **resumed** (`ResumeMoves`, `spaces.go:414`), plus a "switch landed" fixup (`assets_move.go:100-116`) | `assets_move.go:142 runMove` |
| Recycle actions | `recycle_items.state` / `state_at` (`schema.sql:961`) | `holdItem`/`letItem` per item | **finished or undone** per state (`recycle.go:722 recoverRecycle`, `:763 recoverItem`); purges run in a goroutine and the hourly `recycleRound` (`:703`) finishes stragglers (`:550-565`) | `spaces/recycle.go` |

- There are four startup recovery calls (`spaces/spaces.go:405-414`, plus
  `api.go:285`).
- Progress writes follow two patterns: `writeJob` retries 3 times
  (`bulk.go:114`), and `SetAssetMove` writes per step.
- Pruning: bulk jobs are pruned by the daily pass (`bulk_jobs_updated`), and
  exports by `expires`.
- Evictions (`spaces.cache_state='evicting'`, `evict.go:216`) and
  `SweepStaged` are **not jobs**. They are crash marks on a filesystem step,
  with no progress, owner or listing. They stay as they are.

**Two remote tables, twenty columns repeated.**

- `remotes` (`schema.sql:328`) and `asset_remotes` (`:356`) share 20 columns.
  `asset_remotes` adds `used` and eight `move_*` columns.
- Two sets of store functions:
  - `store/remotes.go`: `GetRemote` :70, `PutRemote` :77,
    `UpdateRemoteStatus` :118, `DeleteRemote` :145;
  - `store/assets.go`: `GetAssetRemote` :65, `PutAssetRemote` :71,
    `UpdateAssetRemoteStatus` :102, `DeleteAssetRemote` :119.
- Every "spaces using a connection" query is a `UNION ALL` of both:
  `store/github.go:251,260,267,283` and `store/providers.go:375,382`.
- Deletes, recycle records and fingerprints name both: `admin.go:253-254`,
  `recycle.go:296,454-455,746-750,799`, `fingerprint.go:112-114`,
  `lists.go:431-433`, `storage.go:46`.
- The `gh_*` columns (`gh_via`, `gh_installation_id`, `gh_repo_id`,
  `gh_user_id`) are reused by GitLab/Gitea remotes (`forge/repos.go`), so the
  names are wrong for half their rows.
- **A defect found in passing.** `store/userdelete.go:167` lists the spaces
  synced by a person's own connection from `remotes` only. An attachment
  repository that goes through that person's connection is missed when the
  deletion screen lists what will stop syncing. The merged table fixes this by
  construction. PR 4c adds a test.

### 4.2 Target data shape

```sql
-- Background work: one row per job, whatever its kind.
CREATE TABLE jobs (
  id           TEXT COLLATE "C" PRIMARY KEY,
  tenant_id    TEXT COLLATE "C" NOT NULL REFERENCES tenants(id),
  kind         TEXT COLLATE "C" NOT NULL,         -- 'bulk', 'export', 'assets.move', 'recycle.move', 'recycle.restore', 'recycle.purge'; reserved below
  subject      TEXT COLLATE "C" NOT NULL DEFAULT '', -- the space, recycle item or '' for many
  lock_key     TEXT COLLATE "C" NOT NULL DEFAULT '', -- e.g. 'space:<id>', 'recycle:<id>'; one active job per key
  made_by      TEXT COLLATE "C" NOT NULL DEFAULT '', -- '' for the server's own jobs
  made_by_name TEXT COLLATE "C" NOT NULL DEFAULT '',
  capacity     TEXT COLLATE "C" NOT NULL DEFAULT '', -- 'site_admin' | 'tenant_admin' | '' (the acting scope of #101; the job acts in it)
  site_only    BIGINT NOT NULL DEFAULT 0,
  params       TEXT COLLATE "C" NOT NULL DEFAULT '{}', -- the request: bulk action and ids, export spaces/history, move target
  state        TEXT COLLATE "C" NOT NULL DEFAULT 'queued', -- queued, running, done, failed, interrupted, cancelled
  total        BIGINT NOT NULL DEFAULT 0,
  done         BIGINT NOT NULL DEFAULT 0,
  bytes_total  BIGINT NOT NULL DEFAULT 0,
  bytes_done   BIGINT NOT NULL DEFAULT 0,
  failed       TEXT COLLATE "C" NOT NULL DEFAULT '[]', -- per-item failures {id, code, message}
  error        TEXT COLLATE "C" NOT NULL DEFAULT '',
  result       TEXT COLLATE "C" NOT NULL DEFAULT '{}', -- export: {path, bytes}
  attempt      BIGINT NOT NULL DEFAULT 0,
  runner       TEXT COLLATE "C" NOT NULL DEFAULT '', -- reserved: the process holding it (standby server, phase 5)
  lease_until  BIGINT NOT NULL DEFAULT 0,             -- reserved: with runner
  created      BIGINT NOT NULL,
  started      BIGINT NOT NULL DEFAULT 0,
  updated      BIGINT NOT NULL,
  expires      BIGINT NOT NULL DEFAULT 0              -- export files; 0 = kept until pruned
);
CREATE INDEX jobs_active ON jobs(state) WHERE state IN ('queued', 'running');
CREATE UNIQUE INDEX jobs_lock ON jobs(lock_key) WHERE lock_key <> '' AND state IN ('queued', 'running');
CREATE INDEX jobs_tenant ON jobs(tenant_id, kind, created DESC, id DESC);
CREATE INDEX jobs_expires ON jobs(expires) WHERE expires > 0;
CREATE INDEX jobs_updated ON jobs(updated);
-- dialect: postgres
ALTER TABLE jobs SET (fillfactor = 85);   -- progress updates touch no indexed column: HOT
```

Reserved kinds, with their shape fixed now and their handlers written later:

- `remote.move`: a space moves to or from a storage target (3.6); lock
  `space:<id>`;
- `space.rebuild`: rebuild from the storage org (3.6);
- `tenant.export`;
- `invites.send`: the bulk-add CSV with rate-limited mail;
- `groups.sync`: IdP group sync (3.6 org part).

The lock key `space:<id>` is shared by `assets.move` and `remote.move`, so the
two can never run on one space at once.

`bulk_jobs` and `exports` are dropped, along with `asset_remotes.move_*`.

`recycle_items` loses `state` and `state_at`, and gains
`job_id TEXT NOT NULL DEFAULT ''`. An item is at rest when `job_id = ''`. The
`recycle_items_busy` index becomes `WHERE job_id <> ''`.

**One runner (`internal/jobs`).**

- Each kind registers a `Handler`:
  - `Run(ctx, *Job, Progress) error`;
  - `Recover(ctx, *Job) Outcome`, where the outcome is resume, fail or done.
    The defaults are: bulk marks itself `interrupted`, an export `failed`,
    `assets.move` resumes, recycle move and purge resume, and recycle restore
    undoes.
  - A `Class`, which decides the slots.
- Slots per class keep today's numbers:
  - `bulk`: 1 per tenant, 2 per server;
  - `io` (exports, moves): 1 per tenant, 2 per server;
  - `recycle`: 1 per item through `lock_key`, paced by the handler.
- Enqueueing inserts the row and signals a channel. There is no polling. At
  start the runner reads `jobs_active` once and calls `Recover` per row. That
  replaces the four calls in `spaces.go:405-414` and `api.go:285`.
- Progress:
  - `Progress.Step(n, bytes)` writes at most once a second or on every 100
    items, whichever comes first;
  - the end state is always written, with the 3 retries `writeJob` has today;
  - in-memory progress is read by `GET` while the job runs, so polling the
    API does not read the DB.
- The end of a job goes through item 1's audit-and-notify helper for the
  kinds that tell someone (export ready, bulk finished).
- The daily pass deletes ended jobs older than 30 days via `jobs_updated`, and
  export files past `expires` via `jobs_expires`.
- `runner` and `lease_until` are written (process id, plus 0) but not yet used
  for leasing. A standby server (phase 5) claims with
  `UPDATE … WHERE lease_until < now`. No schema change is needed then.

**One remotes table.**

```sql
CREATE TABLE remotes (
  space_id        TEXT COLLATE "C" NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  role            TEXT COLLATE "C" NOT NULL CHECK (role IN ('content', 'assets')), -- reserved: 'mirror'
  url, branch, cred_type, username, token_enc, privkey_enc, pubkey, last_good,
  state, error_kind, last_error, remote_tip, last_sync, updated,      -- as today
  provider        TEXT COLLATE "C" NOT NULL DEFAULT 'git', -- 'git' or a providers.id (item 5)
  via             TEXT COLLATE "C" NOT NULL DEFAULT '',    -- 'app' | 'user' (was gh_via)
  installation_id BIGINT NOT NULL DEFAULT 0,               -- was gh_installation_id
  repo_id         TEXT COLLATE "C" NOT NULL DEFAULT '',    -- the provider's immutable repo id, as text (GitLab/Gitea ids too)
  cred_user_id    TEXT COLLATE "C" NOT NULL DEFAULT '',    -- via 'user': whose link syncs it (was gh_user_id)
  target_id       TEXT COLLATE "C" NOT NULL DEFAULT '',    -- reserved: the 3.6 storage target that made the repo
  used            BIGINT NOT NULL DEFAULT 0,               -- 'assets': an attachment has gone to it
  PRIMARY KEY (space_id, role)
);
CREATE INDEX remotes_installation ON remotes(installation_id) WHERE installation_id <> 0;
CREATE INDEX remotes_cred_user ON remotes(cred_user_id) WHERE cred_user_id <> '';
-- dialect: postgres
ALTER TABLE remotes SET (fillfactor = 85);
```

- `cred_user_id` keeps its meaning after item 5. A link is unique per
  (provider, user) (section 5.2), so the pair `(provider, cred_user_id)` names
  the link. Item 5 therefore does not touch this table again.
- One set of store functions takes `role`:
  `GetRemote(ctx, q, spaceID, role)`, `PutRemote`, `UpdateRemoteStatus`,
  `DeleteRemote`.
- Each `UNION ALL` becomes one scan of one table.
- The recycle record keeps the rows of one table.

### 4.3 API changes

- `GET /api/admin/jobs?kind=&state=&cursor=`: a new paged list, scoped like
  every admin list (the acting scope; `site_only` hidden from tenant admins).
- `GET /api/admin/jobs/{id}` answers one job shape for every kind:
  `{id, kind, subject, state, total, done, bytesTotal, bytesDone, failed, error, created, updated, madeBy}`.
- `POST /api/admin/bulk` is unchanged and answers `{job}`.
- Exports:
  - `POST /api/admin/exports` answers `{job}`;
  - `GET /api/admin/exports` is the same list as `jobs?kind=export`, with
    `bytes` and `expires`;
  - `GET …/{id}/download` and `DELETE …/{id}` stay (export-specific);
  - `GET /api/admin/exports/{id}` is removed: use `jobs/{id}`.
- Attachment moves:
  - `POST /api/spaces/{space}/remote/assets/move` answers `{job}`;
  - `GET …/remote/assets` carries `move: job|null` instead of the `move_*`
    fields.
- Recycle items carry `state` derived from the job (`ready`, `moving`,
  `restoring`, `purging`) and `job` (an id or null).
- A cancel endpoint is reserved, not built: `DELETE /api/admin/jobs/{id}`. The
  state `cancelled` exists.

### 4.4 Web changes

- `api/admin.ts` and `api/types.ts` gain one `Job` type.
- The bulk progress UI, the exports page, the recycle page and the attachment
  move progress (remote settings) read `Job`.
- One `useJob(id)` hook polls every 2 s while the job is active and stops at
  the end. It replaces the per-page pollers.
- The fixture routes and data for jobs, which exist after 3e, are updated.

### 4.5 Cost

| | Old | New |
|---|---|---|
| Active-job scan at start | four tables | one partial index (a handful of rows) |
| A progress write | per item | at most 1/s, a HOT update on PostgreSQL |
| `GET jobs/{id}` while running | — | memory, no DB read |
| "Spaces using connection X" | two scans plus `UNION ALL` | one index scan (`remotes_installation` or `remotes_cred_user`) |
| Space lists' `has_assets` and `provider` subselects (`lists.go:431-433`) | two subselects | primary-key lookups `(space_id, 'content')` and `(space_id, 'assets')` on one index |

At 50k spaces nothing new is per page or per request.

### 4.6 PR split

| PR | Content | Files | After |
|---|---|---|---|
| 4a | `jobs` table, `internal/jobs` runner, bulk moved on (`bulk_jobs` dropped), `GET /api/admin/jobs`, web `Job` type and `useJob` for bulk | `internal/store/schema.sql`, `store/jobs.go` (new), `store/bulk.go` (gone), `internal/jobs/` (new), `api/bulk.go`, `api/api.go`, routes; `web/src/api/{admin,types,hooks}.ts`, bulk UI | 3a (can run alongside 3c–3f: server plus a few web files the fixture PRs don't touch; fixture data for jobs is added in whichever of 4a/3e merges second) |
| 4b | Exports and recycle actions on jobs (`exports` dropped; `recycle_items.job_id`); `recycleRound` keeps only the purge check and tombstones; startup recovery via the runner | `spaces/export.go`, `spaces/recycle.go`, `spaces/spaces.go:405-414`, `store/exports.go`, `store/recycle.go`, `api/exports*.go`, `api/recycle*.go`; web exports and recycle pages | 4a; its web part after 3e (admin tests on fixtures) |
| 4c | One `remotes` table keyed (space, role), generic credential columns, attachment moves as `assets.move` jobs (`asset_remotes` dropped), the `userdelete.go:167` fix with a test | `store/{remotes,assets,github,providers,storage,lists,fingerprint,recycle,admin,userdelete,spacefacts}.go`, `spaces/{remote,assets,assets_move,sync}.go`, `github/token.go`, `forge/repos.go`, `api/remote*.go`, `api/assets.go`; web remote settings | 4a and 3f (parallel with 4b; both touch `spaces/spaces.go` startup and `store/recycle.go`, in different hunks: the second to merge syncs main first) |

### 4.7 Test plan

- **Store, both DBs.**
  - `jobs_lock`: two concurrent enqueues with one key, exactly one wins
    (PostgreSQL unique-index race, SQLite serialised).
  - Active scan, prune and expiry.
  - Remotes: the role primary key and cascade on space delete.
  - The connection-count query covers both roles.
- **Runner.**
  - Slots per class and per tenant, using channels and hooks, no sleeps.
  - Restart: rows left `running` are passed to `Recover`, and each default
    outcome is asserted.
  - Progress throttle with a fake clock.
  - The end state is written despite one failed write.
- **Kinds.** The existing bulk, export, recycle and move tests keep their
  behaviour assertions. Only their setup changes, through the runner's test
  hooks (`SetBulkJobHook` becomes `jobs.SetHook(kind, …)`).
- **Contract (real server).** The job shape for bulk and export, and
  `remote/assets` with `move`.
- Each PR's new tests stay under 1 s each. Run `go test -race` for the touched
  packages, plus `make test-postgres` for `internal/store` and the touched
  packages.

---

## 5. One providers table and one linked-accounts table

### 5.1 Current state

**Five places model "an outside account linked to a person".**

| What | Provider config | Per-person link | Leftovers | Code |
|---|---|---|---|---|
| Sign-in (OIDC, SAML, GitHub sign-in) | `sign_in_providers` (`schema.sql:662`), site-owned, `tenant_id` = where JIT puts accounts | `identities` (`:142`), `UNIQUE (issuer, subject)` | — | `auth/providers.go` (kinds `:38-40`), `auth/sso.go` (`FindIdentity` `store/users.go:319`; SAML NameID `sso.go:606-654`), `auth/emailtrust.go:57` |
| GitHub repositories | `site_records['github_app']` (`github/github.go:169`) | `github_authorizations` (`:617`), one per user | `github_leftovers` (`:646`) | `internal/github` (renew `auth.go:497,605,645`) |
| GitHub App installations | — | `github_installations` (`:632`) plus `github_installation_tenants` (`:639`) | — | `github/admin.go` |
| GitLab and Gitea | `providers` (`:901`), owner site or tenant | `provider_authorizations` (`:923`), one per (provider, user) | `provider_leftovers` (`:943`) | `internal/forge` (renew `connect.go:307,414`, `manager.go:513`) |

- The store code is duplicated function by function:
  - `ReplaceGitHubTokens` (`store/github.go:121`) and `ReplaceProviderTokens`
    (`store/providers.go:231`);
  - `DueGitHubRenewals` (`github.go:219`) and `DueProviderRenewals`
    (`providers.go:314`).
- There are two refresh loops (`github.RunRenew` and `forge.RunRenew`), two
  token caches with generations, and two leftovers tables.
- GitHub is two unrelated things: a sign-in provider row (kind `github`, its
  own OAuth app) and the App record. A person who signs in with GitHub and
  syncs with GitHub has two links that nothing ties together.
- Ownership is represented two ways:
  - `sign_in_providers.tenant_id` means "where new accounts go";
  - `providers.owner` means "who owns the config";
  - org SSO (3.6 d) needs the second.
- Web:
  - admin: `components/settings/SecurityPanel.tsx` (sign-in providers),
    `components/admin/ForgeCards.tsx` (GitLab/Gitea plus the GitHub card);
  - account: `components/settings/ConnectionsPanel.tsx` plus the identities
    list;
  - per person: `admin/users/PersonSecurity.tsx`;
  - API: `/api/admin/auth/providers*`, `/api/admin/connections*`,
    `/api/connections*`, `/api/account/identities/{id}` and
    `/api/users/{id}/identities*`.

### 5.2 Target data shape

```sql
-- Every outside system a person can sign in with or connect repositories to.
CREATE TABLE providers (
  id                TEXT COLLATE "C" PRIMARY KEY,
  owner             TEXT COLLATE "C" NOT NULL DEFAULT 'site',  -- 'site' or a tenant id (org SSO, a tenant's own GitLab/App)
  slug              TEXT COLLATE "C" NOT NULL,                 -- URL name, unique per owner
  kind              TEXT COLLATE "C" NOT NULL CHECK (kind IN ('oidc', 'saml', 'github', 'gitlab', 'gitea', 'google', 'microsoft', 'apple')),
  name              TEXT COLLATE "C" NOT NULL,
  icon              TEXT COLLATE "C" NOT NULL DEFAULT '',
  signin            BIGINT NOT NULL DEFAULT 0,  -- may be used to sign in
  repos             BIGINT NOT NULL DEFAULT 0,  -- may be used to connect repositories (GitHub, GitLab, Gitea)
  enabled           BIGINT NOT NULL DEFAULT 0,
  position          BIGINT NOT NULL DEFAULT 0,
  issuer            TEXT COLLATE "C" NOT NULL DEFAULT '',  -- OIDC iss, SAML entity id, host URL; for display and discovery
  host              TEXT COLLATE "C" NOT NULL DEFAULT '',
  scheme            TEXT COLLATE "C" NOT NULL DEFAULT 'https',
  client_id         TEXT COLLATE "C" NOT NULL DEFAULT '',
  secret_enc        TEXT COLLATE "C" NOT NULL DEFAULT '',  -- sealed JSON: client secret, SAML key, App private key and webhook secret
  config            TEXT COLLATE "C" NOT NULL DEFAULT '{}', -- kind-specific, nothing secret: scopes; SAML metadata URL,
                                                             --   emailAttribute (default 'mail'), nameIdFormat; groupsClaim;
                                                             --   GitHub appId and appSlug
  jit_tenant        TEXT COLLATE "C" NOT NULL DEFAULT '',  -- site providers: where autoCreate puts accounts (was sign_in_providers.tenant_id)
  auto_create       BIGINT NOT NULL DEFAULT 0,
  trust_email_optin BIGINT NOT NULL DEFAULT 0,  -- custom OIDC/SAML of the site only; default off
  legacy_callback   BIGINT NOT NULL DEFAULT 0,
  cache             TEXT COLLATE "C" NOT NULL DEFAULT '',  -- discovery document or IdP metadata
  status_ok         BIGINT NOT NULL DEFAULT 0,
  status_checked    BIGINT NOT NULL DEFAULT 0,
  status_message    TEXT COLLATE "C" NOT NULL DEFAULT '',
  platform_version  TEXT COLLATE "C" NOT NULL DEFAULT '',
  partial_sync      TEXT COLLATE "C" NOT NULL DEFAULT 'unknown',
  partial_checked   BIGINT NOT NULL DEFAULT 0,
  groups_version    BIGINT NOT NULL DEFAULT 0,
  updated_by        TEXT COLLATE "C" NOT NULL DEFAULT '',
  created           BIGINT NOT NULL,
  updated           BIGINT NOT NULL,
  UNIQUE (owner, slug)
);
CREATE UNIQUE INDEX providers_owner_host ON providers(owner, kind, host) WHERE host <> '';
CREATE INDEX providers_issuer ON providers(issuer) WHERE issuer <> '';

-- A person's account at a provider. One per (provider, person) and one person per (provider, subject).
CREATE TABLE linked_accounts (
  id              TEXT COLLATE "C" PRIMARY KEY,
  provider_id     TEXT COLLATE "C" NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  user_id         TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  subject         TEXT COLLATE "C" NOT NULL,  -- OIDC sub; SAML NameID (persistent); GitHub/GitLab/Gitea numeric id
  login           TEXT COLLATE "C" NOT NULL DEFAULT '',
  email           TEXT COLLATE "C" NOT NULL DEFAULT '',  -- what the provider asserted at the last sign-in
  email_trusted   BIGINT NOT NULL DEFAULT 0,             -- it passed the trust rules below
  signin          BIGINT NOT NULL DEFAULT 0,             -- this link may sign in
  access_enc      TEXT COLLATE "C" NOT NULL DEFAULT '',  -- tokens: repository links only
  refresh_enc     TEXT COLLATE "C" NOT NULL DEFAULT '',
  access_expires  BIGINT NOT NULL DEFAULT 0,
  refresh_expires BIGINT NOT NULL DEFAULT 0,
  scopes          TEXT COLLATE "C" NOT NULL DEFAULT '',
  renew_at        BIGINT NOT NULL DEFAULT 0,             -- when the one renewal loop renews the refresh token (built in 5b)
  state           TEXT COLLATE "C" NOT NULL DEFAULT 'ok',  -- ok, expired (the person authorises again), revoked
  version         BIGINT NOT NULL DEFAULT 1,
  is_admin        BIGINT NOT NULL DEFAULT 0,             -- an admin on a GitLab/Gitea host
  label           TEXT COLLATE "C" NOT NULL DEFAULT '',
  created         BIGINT NOT NULL,
  last_used       BIGINT NOT NULL DEFAULT 0,
  updated         BIGINT NOT NULL,
  UNIQUE (provider_id, subject),
  UNIQUE (provider_id, user_id)
);
CREATE INDEX linked_accounts_user ON linked_accounts(user_id);
CREATE INDEX linked_accounts_renew ON linked_accounts(renew_at) WHERE refresh_enc <> '' AND state = 'ok';  -- 5b: "the next one due" and "all due now"

-- Installations of an App-kind provider (GitHub today), and the tenants each is shared with.
CREATE TABLE provider_installations (
  provider_id  TEXT COLLATE "C" NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  id           BIGINT NOT NULL,
  account      TEXT COLLATE "C" NOT NULL,
  account_type TEXT COLLATE "C" NOT NULL DEFAULT 'Organization',
  updated      BIGINT NOT NULL,
  PRIMARY KEY (provider_id, id)
);
CREATE TABLE provider_shares (           -- a site provider or installation shared with a tenant
  provider_id     TEXT COLLATE "C" NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  installation_id BIGINT NOT NULL DEFAULT 0,  -- 0: the provider itself
  tenant_id       TEXT COLLATE "C" NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  PRIMARY KEY (provider_id, installation_id, tenant_id)
);
CREATE INDEX provider_shares_tenant ON provider_shares(tenant_id);

CREATE TABLE repo_leftovers (            -- was github_leftovers plus provider_leftovers
  provider_id TEXT COLLATE "C" NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  user_id     TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  repo_id     TEXT COLLATE "C" NOT NULL,
  full_name, html_url, space_name, created,
  PRIMARY KEY (provider_id, user_id, repo_id)
);
CREATE INDEX repo_leftovers_user ON repo_leftovers(user_id);

-- Reserved for 3.6, shape only; built empty here.
CREATE TABLE storage_targets (           -- where spaces of an owner get repositories made automatically
  id              TEXT COLLATE "C" PRIMARY KEY,
  owner           TEXT COLLATE "C" NOT NULL,      -- 'site' (the storage org) or a tenant id
  provider_id     TEXT COLLATE "C" NOT NULL REFERENCES providers(id),
  installation_id BIGINT NOT NULL DEFAULT 0,
  namespace       TEXT COLLATE "C" NOT NULL,      -- org or group path
  namespace_id    TEXT COLLATE "C" NOT NULL DEFAULT '', -- immutable id
  created         BIGINT NOT NULL,
  updated         BIGINT NOT NULL
);
CREATE TABLE group_mappings (            -- IdP group claim value → PetalBook group (3.6 org part)
  provider_id TEXT COLLATE "C" NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  value       TEXT COLLATE "C" NOT NULL,
  group_id    TEXT COLLATE "C" NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  PRIMARY KEY (provider_id, value, group_id)
);
```

Gone: `sign_in_providers`, `identities`, `github_authorizations`,
`github_installations`, `github_installation_tenants`, `github_leftovers`,
`provider_authorizations`, `provider_leftovers`, the old `providers`, and the
record `site_records['github_app']`. `group_members.provider` now holds a
provider id.

**Rules the shape carries.**

- **GitHub is one row.** It is a `kind='github'` row with `signin` and/or
  `repos`, holding the App's client and keys. Signing in with it makes or
  finds the link with `signin=1` and **no tokens**. "Connect repositories"
  re-authorises and stores tokens on the **same** link. Signing in never
  grants repository access silently.
- **Linking.** A link is found by `(provider_id, subject)`.
  - For SAML the subject is the NameID; `persistent` is recommended. The email
    (the UPN) comes from `config.emailAttribute`, `mail` by default. It falls
    back to the NameID only when the NameID's format is `emailAddress`.
    Otherwise the sign-in is refused with `saml_no_email`, which tells the
    admin to map an attribute.
  - Existing rule: an address verified on another account is never linked
    automatically.
- **Trust** is one function in `auth/emailtrust.go`, given
  (provider kind, owner, claims) and producing `email_trusted`:
  - Google and Apple: `email_verified=true`;
  - GitHub: an address its API lists as verified;
  - Microsoft: only when `xms_edov` or a verified domain proves it;
  - org SSO (`owner` is a tenant): only addresses in that tenant's verified
    domains;
  - custom OIDC/SAML of the site: only with `trust_email_optin`.
- **Org SSO needs a verified domain.** Enabling a `signin` provider whose owner
  is a tenant is refused with `domain_required` unless that tenant has a
  verified domain. The domains table itself comes with 3.6 (d); this
  consolidation adds the check as a hook that returns false until then, so
  tenant-owned sign-in cannot be enabled early.
- **3.7.**
  - A link belongs to the account, not to a tenant.
  - Leaving an org deletes the links whose provider's `owner` is that org: one
    indexed join on `linked_accounts_user`.
  - A per-org SSO proof stays in `sessions.proofs`.
- **Passkeys** stay in `passkeys`. They are credentials of the account, not
  links to a provider.

**One token service (`internal/connections`).**

- One `Renew` loop over `linked_accounts_renew`, dispatching to a per-kind
  driver (`github`, `gitlab`, `gitea`).
- One token cache keyed by link id, with the existing generation scheme.
- One `ReplaceTokens(link, fromVersion, …)`.
- The two loops and both caches go.

### 5.3 API changes

Admin: one resource replaces `/api/admin/auth/providers*` and
`/api/admin/connections*`.

- `GET/POST /api/admin/providers` and `GET/PUT/PATCH/DELETE /api/admin/providers/{id}`,
  with `kind`, `signin`, `repos` and kind-specific `config`.
- `POST …/{id}/check` (was refresh/check), plus the GitHub-only
  `POST …/{id}/manifest[/retry]` and `GET/PUT …/{id}/installations[/{iid}]`.
- They are scoped by the acting scope (#101): a site admin sees `owner=site`,
  and in a tenant scope the tenant's rows plus the site rows shared with it.
  Tenant-owned sign-in is gated as above.

Account: one resource replaces `/api/account/identities/{id}`,
`/api/connections`, `…/{provider}/authorization*` and `…/leftovers/*`.

- `GET /api/account/links` lists the person's links, with provider, login,
  `signin`, whether repositories are connected, and state.
- `POST /api/account/links/{provider}/start` takes
  `{purpose: "signin"|"repos"}` and answers a redirect.
- `DELETE /api/account/links/{id}` refuses to remove the person's last
  sign-in method (`last_method`).
- `DELETE /api/account/links/{id}/repos` drops the tokens and keeps sign-in.
- `GET /api/account/links/{id}/repos` and `…/owners` (were
  `/api/connections/{provider}/repos|owners`).
- `DELETE /api/account/leftovers/{provider}/{repoId}`.

Admin per person: `GET /api/users/{id}/links` and
`DELETE /api/users/{id}/links/{link}`.

The public sign-in list and start (`/api/auth/{slug}/start`, the callbacks)
keep their paths. Slugs now resolve within an owner: site slugs as today, and
tenant slugs under the 3.6 (d) domain routing.

### 5.4 Web changes

- One admin page, **Providers**, replaces the sign-in providers part of
  `SecurityPanel.tsx`, `ForgeCards.tsx` and the GitHub card. It has filter
  chips "Sign-in" and "Repositories", one form per kind, and installations
  under GitHub.
- One account section, **Linked accounts**, replaces `ConnectionsPanel.tsx`
  and the identities list. Each row offers "Use to sign in" and "Connect
  repositories" where the provider allows them.
- `PersonSecurity.tsx` reads `/api/users/{id}/links`.
- The fixture routes and data for these are rewritten. After item 3 that is
  data, not rules.

### 5.5 Cost

- A sign-in is one lookup on `UNIQUE (provider_id, subject)`, as
  `identities (issuer, subject)` is today.
- The provider config is cached in memory as today (one snapshot), so no
  request reads `providers`.
- Renewal is one partial-index range scan for all kinds, instead of two
  scans. It runs on the existing cadence.
- "Spaces using installation X / user Y's link" uses the remotes indexes from
  4c.

### 5.6 PR split

| PR | Content | Files | After |
|---|---|---|---|
| 5a | New tables (`providers`, `linked_accounts`, `provider_shares`, `repo_leftovers`, plus the reserved `storage_targets` and `group_mappings`, empty); sign-in moved on (`sign_in_providers` and `identities` dropped); `/api/admin/providers` and `/api/account/links` for sign-in kinds; SAML email attribute and `saml_no_email`; the trust function; the domain hook; web Providers page and Linked accounts (sign-in) | `store/schema.sql`, `store/{signin,users,providers}.go`, `auth/{providers,sso,emailtrust,account}.go`, `api/signin*.go`, routes; `web/src/api/{admin,client,types}.ts`, `components/settings/SecurityPanel.tsx`, a new `components/admin/Providers*.tsx`, `components/admin/users/PersonSecurity.tsx`, fixtures | 3a, 3g; parallel with 4b/4c (no shared files) |
| 5b | GitLab/Gitea moved onto `providers` and `linked_accounts`; `repo_leftovers`; the one renew loop and token cache in `internal/connections`; their API under `/api/admin/providers` and `/api/account/links` | `internal/forge/*`, `internal/connections/*`, `store/providers.go`, `api/connections*.go`; `components/admin/ForgeCards.tsx` (gone into Providers), `components/settings/ConnectionsPanel.tsx` | 5a, 4c |
| 5c | GitHub as one provider row (App record out of `site_records`; installations and shares; authorisations into links; sign-in and repos on one link); `github_*` tables dropped; its renew moved onto 5b's loop | `internal/github/*`, `internal/githubapp/*` (only where it reads the record), `store/github.go`, `api/github*.go`; web GitHub pieces | 5b |

5b and 5c share the renew loop and `store/providers.go`, so they run in
sequence. 5a can run alongside 4b and 4c.

### 5.7 Test plan

- **Store, both DBs.**
  - Both uniques on `linked_accounts` (a race of two links for one subject).
  - Cascade on user and provider delete.
  - `providers_owner_host` with `host=''` repeating.
  - The renew index range.
- **Auth.**
  - The trust table: one case per row above, including Microsoft without
    `xms_edov`, which is not trusted.
  - SAML: persistent NameID plus `mail`; `emailAddress` NameID fallback; no
    email gives `saml_no_email`; transient NameID is refused (existing).
  - Org-owned sign-in cannot be enabled without a domain.
  - Signing in with GitHub stores no token.
  - The last-method guard.
- **Connections.**
  - One loop renews a GitHub and a GitLab link due in the same pass, with the
    fakes `forgetest` and `githubapptest`.
  - Version conflict on concurrent renew (existing tests, re-pointed).
- **Contract (real server).**
  - Providers CRUD for an OIDC row.
  - `GET /api/account/links` shape.
  - Delete-last-method refused.
  - The flows with outside fakes stay in Go.
- Each test is under 1 s. Run targeted packages, plus `make test-postgres` for
  `internal/store`, `internal/auth`, `internal/forge` and `internal/github`.

---

## 6. Order and parallel work (all items)

```
3a ─┐
3b ─┴─ 3c ── 3d ──┬── 3e ──┐
                  └── 3f ──┴── 3g ── 5a ──┐
3a ── 4a ──┬── 4b (web part after 3e)     ├── 5b ── 5c
           └── 4c (after 3f) ─────────────┘
```

- At most two heavy agents at once.
- The natural pairs are (3c, 4a), (3e, 3f), (4b, 4c) and (5a, 4b or 4c).
- Each PR syncs main right before it merges.
- Schema-changing PRs (4a, 4b, 4c, 5a, 5b, 5c) each say "recreate dev
  databases", and the demo is rebuilt after each.

## 7. Open questions (product or security)

1. **The key that links an outside account: per provider row, or per issuer
   across the site?**
   - Earlier guidance was "link by (issuer, NameID)". Org SSO makes the issuer
     string a security question: a tenant admin types it in, and anyone can
     put any entity ID in their own IdP's metadata. Keyed by issuer site-wide,
     org B could configure an IdP that claims org A's issuer and sign in to
     org A's linked accounts.
   - Recommended: link by `(provider_id, subject)`. An issuer already used by
     another owner's provider is refused, unless a site admin shares one site
     provider with several tenants (`provider_shares`).
   - The cost: a person whose two orgs use the same IdP through two separate
     provider rows gets two links to the same account. That is harmless.
2. **One linked account per provider per person?**
   - Today `identities` lets one person link two Google (or two OIDC) accounts.
     GitHub and GitLab connections already allow one.
   - Recommended: one per (provider, person). It is what makes "which token
     syncs this space" unambiguous (`remotes.cred_user_id`), and it keeps the
     account page simple.
   - Someone with two Google accounts picks one, or uses another sign-in
     method for the second.

Everything else in this plan is decided as written above. That covers:

- interrupted bulk jobs stay `interrupted`, not resumed;
- evictions stay outside jobs;
- a sign-in link holds no repository token;
- the export endpoints keep `download`;
- the dev mock replays data with no rules.
