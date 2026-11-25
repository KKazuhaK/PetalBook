# Milestones: phase 3, teams (3.1 to 3.4)

## Phase 3: teams

Groups (IdP mapping and local groups); partial cache for when everything does
not fit locally (the four steps of
[storage-and-sync.md §4.5](../storage-and-sync.md#45-when-local-space-is-not-enough-partial-cache)); preparation for multi-tenancy
and open registration (see 1.7 in [phase-0-1.md](phase-0-1.md)); extracting Git storage into a standalone module and
switching Rackbook over to it. Shared spaces and the admin console were already
done early in 1.7.

**Milestone 3.1: save server space: mandatory remote, partial cache, separate
asset repository** (proposed by the owner on 2026-11-22). It comes at the start
of phase 3 and depends on 2.7 (GitHub App, GitHub user authorization,
automatic repository creation). The owner's requirements:

- An admin can require spaces to follow a remote (no requirement, required for
  new spaces, required for all spaces). When GitHub is configured, a repository
  is created in one click. Existing local spaces get a grace period and become
  read-only after it expires, with nothing deleted or lost. Individual users or
  spaces can be exempted, and the size of local spaces can be limited.
  `--remote-host` moves to the same page.
- Partial cache (the four steps of
  [storage-and-sync.md §4.5](../storage-and-sync.md#45-when-local-space-is-not-enough-partial-cache), only for spaces that follow a remote):
  idle spaces have their local clone deleted and keep only the index; only the
  recent history is fetched; assets are fetched on demand into a capped cache;
  even the body text is fetched on demand (`--filter=blob:none`,
  「只拉取一部分，不用全部拉下来」 [fetch only a part, do not pull everything
  down]). The admin sets a disk budget.
- A separate asset repository (the second backend of
  [data-model.md §3.4](../data-model.md#34-attachments)): one repository for
  text, another for assets, and before a page is committed, the assets it
  references are first made sure to be pushed.
- Goal: for each space the server keeps only the index, the recently opened
  content and a capped asset cache, so disk use is counted by the "active
  part".

**The 3.1 contract (2026-11-23)**: the endpoints and error codes are written in
a new "Server storage" section of `docs/api.md`, plus additions in "Spaces"
(the `storage` field of the Space object, and `remote` and `github.assets` of
`POST /api/spaces`), `DELETE …/remote` under "Remote repository", and "History",
"Files", "Import", "Users" and "Administration". The names that 2.7 reserved
under "GitHub" are used as they are.

- **What is saved** (a corpus of 50,000 pages; text and index are measured,
  the rest is estimated, and before merging it is measured on the
  `spikes/search-scale` corpus and written back here): index 166 MB, workspace
  text 192 MB, text of the current versions in `.git` about 100 MB, commits and
  trees about 60-120 MB (at five versions per page and 250,000 commits), text
  of old versions about 100-400 MB. When assets live in the space repository
  they **take two copies** (one in the workspace, one in `.git`, and images do
  not compress well), so 5 GB of assets is 10 GB plus all the old versions. So
  for such a space: full clone about 11 GB; partial clone plus asset repository
  about 0.6 GB (plus the part of the asset cache that belongs to it); after
  eviction 166 MB. For a common space of 2000 pages and 200 MB of assets: about
  450 MB, about 25 MB, about 7 MB. With 1000 such spaces, 100 of them in use,
  clones drop from about 450 GB to about 2.5 GB, plus 7 GB of index and asset
  cache.
- **The per-request cost of a cached space does not change**: pages, lists,
  search, links, validation and current assets still read the workspace and the
  index. Only reading something the server does not have (old versions, assets
  not in the cache, files of an evicted space) waits for the network.

**Mandatory remote**

- `remotes: {require, scope, graceDays}` of `GET/PUT /api/admin/storage`.
  `require` is `null` (default), `"any"`, or `"github"` (a `github` credential,
  App or user authorization; it cannot be set when no App is configured).
  `scope` is `"new"` (default) or `"all"`. `graceDays` is 0-365, default 30. The
  server records `since`, the time the requirement last changed to its current
  value, and the deadline of an existing space is `since + graceDays`.
- New spaces: when `POST /api/spaces` does not carry what the requirement
  asks for, it answers `409 remote_required` before doing anything. With
  `"any"` it may carry `remote` (same shape as `PUT …/remote`), again "either
  everything succeeds or no space is left behind".
- The **personal space** is created at first sign-in, when the user has not
  connected anything yet, so it is created as a local space as before, and its
  deadline is the creation time plus `graceDays`. The UI offers a one-step way
  to finish (connect GitHub, and create the private repository
  `petalbook-<username>` under their own account).
- During the grace period there is only a notice: the `storage.remote` field of
  the Space object (the requirement, the deadline, whether it has expired),
  visible to all members, and people with `manage` see a one-click connect.
- **Read-only after expiry**: the space is still listed, opened, read and
  searched; drafts are still saved (drafts are in the state database, not in
  the space); requests that change files answer `409 remote_required` with the
  deadline. Connecting a remote is always allowed, and as soon as a remote of
  the required kind is connected the space is writable again and drafts are
  committed as usual. **Nothing is ever deleted**: not spaces, pages, drafts or
  settings.
- While a requirement is in force, `DELETE …/remote` and switching to a
  credential the requirement does not accept both answer `409 remote_required`.
- When the requirement is relaxed, deadlines are cleared at once and expired
  spaces are writable at once. When it is tightened, `since` is counted again,
  so nobody's grace period starts in the past. When `graceDays` is changed,
  deadlines still in their grace period are moved, and one moved to a time that
  has already passed expires at once; `affected` in the answer to `PUT` lets
  the admin see first how many are affected.
- A space whose remote has a problem (unreachable, `rewritten`, any of the
  `errorCode`s of 2.7) still counts as following a remote, satisfies the
  requirement, and is handled by the existing rules.
- Exceptions: `remoteExempt` of `PATCH /api/users/{id}` (none of the spaces
  this person owns are required), and `exempt` of
  `PUT /api/admin/spaces/{space}/storage` (a single space).
- **Size limit of local spaces** `localSpaces.maxBytes` (default unlimited): it
  only rejects uploads and imports (`507 space_full`) and never rejects saving
  text, so what is being written is never lost. The size is the value measured
  in the background (at most 6 hours old), and within 10% of the limit it is
  measured again first.
- `hosts.allowed` is the current `--remote-host`, moved to this page and stored
  in the state database. When the flag is given at startup, the flag wins and
  the page is read-only.
- **Layering and locking**: these are all site-wide settings, and in 3.1 they
  are all locked at the site level and not delegated to spaces: they decide how
  the server uses its own disk and which hosts it trusts, whereas settings at
  the space level are in `space.yml`, which anyone who can push to the remote
  can change. The tenant level is reserved: a tenant admin may only tighten on
  top of the site settings (require a remote, `"github"` is stricter than
  `"any"`, a shorter grace period, a smaller budget) and may not relax them.
  The "pinned" and "exempt" flags of each space are admin records in the state
  database, not space settings.

**Partial cache**

- 3.1 does step 1 of [storage-and-sync.md §4.5](../storage-and-sync.md#45-when-local-space-is-not-enough-partial-cache) (evict idle
  spaces) and step 3 (fetch file contents on demand). **Step 2 (shallow clone) is not done, and step 4 (fetch body text on
  demand) is not done**; the reasons are below.
- **Use `--filter=blob:none`, not a shallow clone**: the server has all commits
  and trees but only the file contents of the current version (still checked
  out into the workspace). Old versions are fetched when opened, and what is
  fetched stays until the space is evicted. All a shallow clone would save in
  addition is commits and trees (about 60-120 MB for 50,000 pages), but page
  history, finding history across renames, detecting that the remote history
  was rewritten, and counting unpushed commits all have to walk commits. With a
  shallow clone every page of history needs a `--deepen`, which also brings
  back all the files of those commits.
- **Git itself never touches the network**: every Git command on the server
  carries `GIT_NO_LAZY_FETCH=1`. A missing object is an error handled by the
  server, not a network request made halfway through a read, without
  credentials and without a deadline. The server fetches missing contents in a
  batch by id (`git fetch --filter=blob:none --stdin`), using the same
  credentials, deadlines and error classification as a pull. The restricted git
  executor allows only these few extra options. Git 2.44 or later is required
  (it has `--no-lazy-fetch`); with an older Git, partial clone is turned off,
  and evicting idle spaces still works (it comes back as a full clone).
- **Pull**: the per-minute pull (still only for spaces that are open) fetches
  new commits and trees; when files changed, there is one more request to fetch
  the new contents of those files, and then it fast-forwards. A pull with
  nothing new costs the same.
- When the remote does not support filtering (Git warns and fetches
  everything), it is a full clone, `cache: "full"`, and nothing else changes.
- An existing full clone is not converted in place (Git cannot drop contents
  from a clone in place); when it is evicted and later fetched back it becomes a
  partial clone. An admin can do this at once with
  `POST /api/admin/spaces/{space}/evict`.
- **History and old versions**: each page of history still lists versions and
  content ids with one `git log`, and fetches the ones that are missing (at most
  one and a half times `limit`) in one request (30 versions of 4 KB are about
  100-200 KB). When following renames to find history, Git's rename detection
  uses only exact renames (which need no contents), and the rest is recognized
  by the page `id`, which is what `Predecessor` does now when Git cannot tell
  (the start of the file deleted in that one commit, fetched in one request).
  The versions listed are the same as in a full clone; at implementation time
  the same history is compared in tests on both kinds of clone.
- While fetching, the request waits for at most 15 seconds. If the remote is
  unreachable, the credential is refused or it times out: `503 not_cached`,
  where `fetching` says whether it is still being fetched, and `Retry-After` is
  5 seconds (still fetching, so probably there next time) or 60 seconds. A page
  of history is either complete or not given; an incomplete list is never
  given. Versions already on the server are read as usual when the remote is
  unreachable. Repairing the settings file from an old version (2.2) looks only
  at the versions on the server when the remote is unreachable, and tries again
  the next time the space is opened.
- **Evicting idle spaces**: delete `DATA/spaces/<id>`, and keep the index and
  the records in the state database (members, remote, drafts). Lists, the folder
  tree, tags, search (within a space and global), links and backlinks, and the
  validation list query the index directly, as fast as for a cached space;
  requests that need to read a file fetch it back first.
  - Only when all of these hold is a space evicted (checked when the space is
    closed, held, and nobody can open it): the remote is `ok`; the committer has
    nothing pending, its log is empty, and a fetch done on the spot confirms
    that the remote branch is at the space's `HEAD` (the remote has every commit
    of the space); there are no rescued commits (`refs/petalbook/unpushed/…`,
    which are the only copy); it is not pinned; and it has been unused for at
    least `minIdleHours`. If any one fails it stays, and the next round looks
    again.
  - When to evict: unused for `evictAfterDays` (default 14 days); or, when the
    clones together exceed `clonesBytes` or the free disk space is below
    `minFreeBytes` (default 10 GiB), the least recently used first, but a space
    used within `minIdleHours` (default 24 hours) is never evicted, so it is not
    fetched back and evicted again and again. "Use" means a request that opens
    the space; a pull does not count. The last-used time is stored in the state
    database, written at most once per hour per space. A round runs every 10
    minutes and evicts one space at a time, each in a few seconds (deleting
    files), using the stored size measurement, in one query. The part of the
    asset cache that belongs to it is evicted together.
  - **Restore**: the first request that needs to read a file starts the restore
    and waits for up to 10 seconds: partial-clone the remote branch → check out
    → for files whose content id matches the index record, set the modification
    time back to the value the index recorded (so the index considers them
    unchanged, and the "by update time" order is the same as before; only files
    changed on the remote in the meantime are read again, as after a pull) →
    open as usual. 2000 pages are about 10 MB and take 1-3 seconds; 50,000 pages
    are about 200 MB and take 20-40 seconds at 100 Mbit/s. At most two restores
    run at the same time and the rest queue. A request that is still not ready
    after 10 seconds answers `503 restoring` (`phase`: `waiting`, `fetching`,
    `checkout`, `indexing`; `Retry-After: 5`), and the restore continues. The
    clone is made in a separate directory and moved into place once complete,
    so a failure leaves no half clone. When the remote is unreachable or the
    credential does not work it fails at once with `503 unavailable` and the
    remote's `errorCode`, and requests that only query the index work as usual.
    Drafts are not affected.
- **Asset cache**: `DATA/assetcache/<space>/<content id>`, one file per copy,
  named by the Git content id, so a file is stored once however many times it
  is referenced. All spaces share one budget `cache.assetsBytes` (default
  5 GiB), and when it is exceeded the least recently read are deleted first. The
  modification time of a file is the time of the last read (changed at most once
  per hour), so the order survives a restart and no database is needed; the
  total is kept in memory and is accurate, because the files in the cache are
  all written by the server itself.
- **Pinned**: `pin` of `PUT /api/admin/spaces/{space}/storage`, which only an
  admin can set. A pinned space is not evicted, the background job fetches all
  old versions so it becomes a full clone, and all its assets stay in the cache
  and are not counted in `assetsBytes`.
- **Admin console**: each item of `GET /api/admin/spaces` gets `storage`
  (`cache`, pinned, exempt, last used, `cloneBytes`, `indexBytes`,
  `assetCacheBytes`, where the assets are, the remote requirement), and can be
  filtered by `cache` and `overdue` (in the same query); `GET /api/admin/status`
  adds the number of spaces in each state, the sum of the three kinds of use,
  the budgets, the number of queued restores, and the number of spaces in their
  grace period and expired. All of these are stored measurements plus numbers
  in memory, in one query.
- **Offline**: a cached space is read as usual, writes answer
  `503 unavailable`, and old versions and assets the server does not have
  answer `503 not_cached`; an evicted space can only be queried through the
  index, and anything that needs to read a file answers `503 unavailable`. An
  expired or missing authorization from 2.7 is the same, with `errorCode`. A
  space in these states is never evicted, so a space in use whose remote has
  gone can keep using its own clone.

**Separate asset repository**

- **An ordinary second Git repository, not Git LFS**: any host a space can use
  works, the credential kinds, the restricted git executor and the GitHub App
  are all the same, and on GitHub it can be browsed like any other file. LFS is
  a separate protocol and service outside Git, with separately billed storage
  and traffic quotas on GitHub, not every host offers it, and it puts pointer
  files in the space repository, so someone without LFS installed sees a piece
  of text.
- **Layout**: the root of the asset repository is the content of `assets/`,
  `<page id>/<file name>`, branch `main`. Clone the space repository, then
  clone the asset repository into its `assets/` directory, and the files are
  exactly as before and links work in any editor. The author and committer of
  commits are the same as for the space, and the push happens before the
  answer.
- **Links do not change**: pages still write `/assets/01JD3K8Q7Z/x.jpg` or a
  relative path to `assets/`, and no page is changed. `GET …/files/assets/…`
  now looks in the asset repository: the server keeps in memory a table of the
  current files of the asset repository (path, content id, size), built when
  the space is opened (20,000 files take about 2 MB and about 50 ms) and updated
  on pull and upload. Once found, the file is served from the asset cache, and
  if it is not in the cache it is first fetched by id, and requests reading the
  same file at the same time share one fetch. The answer carries `ETag` (the
  content id) in place of `Last-Modified` (finding the last commit of a file
  would need walking the history), `If-None-Match` answers `304`, and `Range`
  and the other headers are as before. A path not in the table answers `404`
  directly, without asking the remote.
- Validation (an asset reference pointing at a file that does not exist) uses
  the same table at the same cost; when the asset repository is unreachable it
  uses the last table, so assets are not all reported missing just because
  the repository is unreachable.
- **Unreachable**: text is read and written as usual; assets in the cache are
  served as usual; others answer `503 not_cached`, and the UI shows a
  placeholder where the image would be; an upload answers `503 unavailable` and
  stores nothing. The asset repository has its own status (same as Remote) and
  does not pause the space.
- **Write order**: an upload is pushed to the asset repository before the
  answer, so the assets referenced by a page saved afterwards are certainly on
  the remote already. An import first pushes the assets as one commit to the
  asset repository, then pushes the pages as one commit to the space
  repository; if the second step fails the assets stay on the remote with
  nobody referencing them, and the import still counts as failed (no pages).
  Neither side is ever force-pushed or deleted from.
- Endpoints: `GET/PUT/DELETE /api/spaces/{space}/remote/assets` (the Remote
  object gets one more field, `move`). `PUT` may carry
  `github: {owner, name, private, create}` to create directly on GitHub; the
  space itself must follow a remote, otherwise `409 remote_required`.
  Connecting moves nothing: existing assets stay in the space's `assets/` and
  are read from there as before, and new uploads go to the asset repository.
- **Move over**: `POST …/remote/assets/move` answers `202` and works in the
  background:
  1. Copy: commit and push the files of `assets/` in the current commit to the
     asset repository, at most 500 MB per commit (GitHub rejects pushes over
     2 GB). During this the space is writable as usual, and uploads still go
     into the space's `assets/`.
  2. Switch: briefly hold the space's writes, push also what was added to
     `assets/` since the copy began, then make one commit in the space
     repository that deletes `assets/` and adds `/assets/` to `.gitignore`, and
     push it. From this commit on, assets are read from the asset repository.
  3. The server makes a new partial clone of the space repository, as in a
     restore; the old clone serves as usual until the new one is in place, and
     only then is the disk the assets occupied freed.
  The history of the space and the remote still contain these assets (history
  is never rewritten). If it fails before the switch, nothing has changed for
  the space, what was already pushed to the asset repository stays, and the next
  move continues from there. Only one move can run at a time.
- `DELETE …/remote/assets` is allowed only if no asset has ever entered the
  asset repository; moving back to the space repository is not in 3.1
  (`409 conflict`).
- **Automatic creation on GitHub**: yes. `github.assets` of `POST /api/spaces`
  (when `true` the name is `<name>-assets`) creates the asset repository right
  after the space repository, with the same owner, visibility and credential,
  and step 4 is checked in the same way (an installation limited to "selected
  repositories" is still topped up, and if that fails it answers
  `409 github_repo_not_in_installation`). As soon as one repository has been
  created, a failed answer lists `repo` and `assetsRepo`, and repeating the same
  request with `create: false` can connect both. When the admin's
  `assets.newSpaces` is on, spaces newly created with GitHub get an asset
  repository by default.

**Error codes**: `409 remote_required` (with `require`, and for an existing
space also `deadline`), `503 not_cached` (with `fetching` and `Retry-After`),
`503 restoring` (with `phase` and `Retry-After`), `507 space_full`; reused:
`503 unavailable`, `409 busy` (when evicting, the space is in use or has
something the remote does not), `409 conflict` (a second move, changing the
asset repository during a move or after assets exist, `hosts` set by a flag,
Git too old to enable partial clone), `400 invalid`. The GitHub error codes are
as in 2.7.

**State database** (test on both SQLite and PostgreSQL; the migration uses the
next free number at merge time): `spaces` gets last-used time, cache state,
deadline, `pinned`, `exempt`; `users` gets `remote_exempt`; `remotes` gets a
column to tell the space repository from the asset repository (at most one row
of each per space); one small table for move progress; the policy goes in
`site_settings`.

Not done in 3.1:

- Step 4 "fetch body text on demand too" (see item 1 of the questions for the
  owner) and step 2, the shallow clone.
- An on-demand cache of `assets/` inside the space repository (it would need
  sparse checkout, which touches too much of the current commit, reset and
  fast-forward code): for an asset cache, move to an asset repository.
- S3-compatible storage, Git LFS.
- Moving assets from the asset repository back to the space repository;
  deleting the asset repository of a space that already has assets; cleaning up
  assets nobody references.
- Webhooks and push events in place of the per-minute pull (left over from 2.7);
  it still only pulls spaces that are open.
- The tenant level (only reserved); per-user quotas; a separate clone budget
  per space.
- Email notification for the grace period (there is no email feature yet); only
  a notice in the UI.
- Several processes sharing one cache (the service is still a single instance).
- The offline client of phase 4.

Questions for the owner (the default if there is no reply is in parentheses):

1. **Fetch body text on demand (step 4)**: the owner explicitly asked for this
   on 2026-11-22. It was not done this time, because the text itself is small
   (192 MB for 50,000 pages), and building the index, full-text search,
   rewriting links on rename, and validation all need to read body text, so
   fetching on demand would make requests such as opening an uncached page or
   moving a page wait for the network, against "the per-request cost of a cached
   space does not change"; at scale the big saving is evicting idle spaces (which
   evicts the body text too) and the assets. (Default: not done in 3.1; decide
   after measuring the effect of eviction and the asset repository.)
2. After the grace period, is the space read-only, or does it become unopenable
   as a whole, as when an admin disables it? (Default: read-only, readable and
   searchable, drafts still saved.)
3. Under "must follow a remote", is it fine for the personal space to still be
   created as local first with a grace period, instead of requiring GitHub to be
   connected at sign-in? (Default: yes.)
4. When assets are moved, the space repository deletes `assets/`: the remote
   saves room, but when viewing the space repository on the GitHub web UI the
   images no longer show (the asset repository must be cloned into `assets/`).
   (Default: delete.)
5. Defaults: evict after 14 idle days, never evict a space used within 24 hours,
   evict early when free disk is below 10 GiB, asset cache 5 GiB, grace period
   30 days. (The default is these values.)
6. Only an admin can pin; the owner of a space cannot request it for themselves.
   (Default: admin only.)
7. Spaces newly created with GitHub do not get an asset repository by default.
   (Default: not by default; an admin can turn it on.)
8. When `--remote-host` is given, the flag wins and the page is read-only.
   (Default: so.)
9. Git 2.44 or later is needed for partial clone; with an older one only idle
   spaces are evicted. (Default: so; checked at startup and shown on the
   settings page.)

Relation to the 2.7 measurements: the endpoints do not depend on the measured
results. Automatic creation of the asset repository takes the same path as the
space repository, an installation limited to "selected repositories" is still
topped up, and if that fails it is the existing `409
github_repo_not_in_installation`; the measurements only affect whether a space
needs one more top-up. 3.1 has two more points to verify itself: that fetching
single contents by id (`--filter=blob:none --stdin`) works with both the
installation token and the user token of GitHub; and that restore and fetching
on demand make closed spaces swap tokens too, and the count has to be examined
in a test (the token cache is per repository, at most once every 55 minutes for
a space in use).

**3.1 as built (part one, 2026-11-23)**: the storage policy, partial clone,
fetching old versions on demand, evicting idle spaces and restore, and the
admin console are done; the separate asset repository (part two) is not done
yet. The endpoints follow "Server storage" in `docs/api.md`, and the migration
is `0015_storage.sql`; tests were run on both SQLite and PostgreSQL. Where it
differs from the contract, and details settled during implementation:

- **Which spaces become partial clones**: in this codebase a space is not
  "cloned"; it is first `git init`ed locally and then connected to the remote.
  So there are only two cases: when connecting, the space is still brand new
  (no commits of its own) and the remote has content, in which case it does
  `fetch --filter=blob:none`, fetches the full contents of the current version
  and then checks out; and restoring after eviction. A space with its own
  history is still a full clone when connected, and existing full clones are
  left alone. When `cache.partial` is off or Git is older than 2.44, it is
  always a full clone.
- **Fetching old versions on demand**: `git log --follow` in a partial clone
  has to read contents to compare similarity, and fails when contents are
  missing, so in a partial clone it uses `-M100%` (only exact renames, no
  contents needed) plus `--raw --no-abbrev`. It first takes the content id of
  each version from the commit list, finds the missing ones with
  `cat-file --batch-check`, fetches them back with one
  `fetch --filter=blob:none --stdin`, and then reads as before. A page that was
  renamed and changed in content at the same time is joined up by the page `id`
  (`Predecessor`), and the start of the file deleted in that commit is also
  fetched in one request. The same history lists the same commits, `rev` and
  paths on a full clone and a partial clone, and a test compares them. When
  several requests want the same content id at the same time they share one
  fetch; the request waits at most 15 seconds while fetching and on timeout
  answers `not_cached` (`fetching: true`); when the fetch fails it answers
  `fetching: false`, and within 3 seconds of consecutive failures it answers the
  same failure directly.
- **Pull**: before a fast-forward or a reset to the remote commit (divergence,
  accepting rewritten history, connecting), it first computes with
  `diff --raw` the contents missing from the files to be checked out and fetches
  them in one request; a pull with nothing new costs the same. Git commands all
  carry `GIT_NO_LAZY_FETCH=1`, and the restricted executor allows only these
  extra options: `fetch --filter=blob:none --stdin --no-write-fetch-head`,
  `cat-file --batch-check`, `log/diff --raw --no-abbrev`, `log -M100%`,
  `rev-list --objects --missing=print`, `status --untracked-files=normal`,
  `for-each-ref --format=*`, `version`.
- **Eviction**: first `holdForEviction` (mark the space as closing, new requests
  wait; if a request, a draft sweep or a pull is using it, answer `busy`),
  commit and close, then check: the log is empty, there are no changes or
  uncommitted files, there is no `refs/petalbook/unpushed/…`, the remote is
  `ok`, and after one `fetch` the `HEAD` is in the remote branch and the remote
  history was not rewritten. One more condition than the contract: **if there is
  a draft (anyone's) it is not evicted**. Drafts are in the state database and
  would not be lost, but this way even a failed restore never leaves "something
  half written in a space with no clone". The order is: mark `evicting` → one
  `rename` moves it to `DATA/evicting/` → mark `evicted` → delete the moved
  directory. At startup `recoverEvictions` finishes by the state database and
  the disk (marked `evicting` but the directory is still there: restore it as it
  was; the directory is gone: count it as evicted). Tests "crash" after each
  step (the hook returns an error, and a new manager is swapped in), and the
  space is either complete or in an evicted state that can be restored, and in
  both all pages can be read. Restore likewise finishes in `DATA/restoring/`
  before `rename` into place, so a crash or failure leaves no half clone.
- **Restore stays alert when the remote was rewritten**: after a restore,
  `.git/petalbook-last-good` is written only if the `last_good` in the state
  database is an ancestor of the new clone; otherwise the space, when opened,
  detects "remote rewritten" as usual and waits for the owner to decide.
- **Restore deadline and backoff**: once its turn comes, a restore takes at most
  5 minutes, so a stuck remote does not hold one of the two slots forever. After
  a failure the same space is not tried again at first: `FailFast` (default 3
  seconds), doubling with each consecutive failure up to 2 minutes; requests in
  this time answer `503 unavailable` and `Retry-After` directly.
- **Startup cleanup is done only in `serve`**: `recoverEvictions` (directories
  left by half-done evictions and restores) and `SweepStaged` (half-built
  spaces) run only with `Options.Serve`, and only after taking the exclusive
  lock on `DATA/serve.lock` (`flock`, released when the process exits);
  `petalbook user …` opened against the data directory of a running server
  touches nothing, and a second `serve` that cannot get the lock does not start.
  During cleanup, a space that "follows a remote, has no clone on disk, and is
  not marked `evicted`" is also marked `evicted`; opening such a space while
  running also restores it, instead of answering 500.
- **Recording the clone state does not overwrite an eviction**: after a space is
  opened, "partial / full" is recorded in the background, with
  `WHERE cache_state NOT IN ('evicted','evicting')` when writing, decided by the
  database, so an eviction that started in between is not overwritten to
  `partial`.
- **Requests that only query the index** (lists, the folder tree, tags,
  backlinks, links, the validation list, groups) use `space.OpenIndexOnly` on an
  evicted space and do not restore; `GET …/pages?body=1` has to read files, so
  it restores. Known small gaps: in the list of an evicted space, the "effective
  type" of a page only looks at the `type` in the header (folder bindings are in
  `space.yml` and wait for the restore), and tag colors likewise wait for the
  restore; backlinks have no `excerpt` (a paragraph read from the file) and
  answer `""`. Links and backlinks find pages by the path in the index and do
  not check the files. One test calls every route in `indexOnlyRoutes` on an
  evicted space.
- **Pinned**: once set, the background job fetches all missing old versions
  (`rev-list --objects --missing=print` lists what is missing, fetched in
  batches), and an evicted space is restored first; if the remote is unreachable
  it does not succeed this time and is tried again the next time the space is
  opened, with no separate retry thread. The order of completion is: first
  remove `remote.origin.partialclonefilter` (under the pull lock, so later pulls
  bring contents), then fetch what is missing, and finally, under the lock,
  confirm with `rev-list --all` that nothing is missing, and only then remove
  `remote.origin.promisor` and `extensions.partialclone`, and from then on the
  clone is a full clone. For this the restricted executor allows only
  `config --local --unset-all` on these three keys and `rev-list --all`.
- **The grace period and deadline are not stored**: the deadline is computed as
  `since` (or the creation time of the space) plus `graceDays`, so changing
  `graceDays` moves all deadlines at once and relaxing the requirement clears
  them at once; `?overdue=1` of the list and `affected` are one SQL written from
  the same condition. Requests that write files are checked in one place in
  `space.mutate` (`WriteGate`), so MCP, draft commits and import all answer
  `remote_required` the same way, and the draft itself is still saved. On the
  hot path, with no requirement, it is only one atomic read.
- **`remote` of `POST /api/spaces`** accepts only token credentials: an SSH key
  must first be added to the repository, and that needs a space that already
  exists. Someone who wants SSH when a remote is required must first create the
  space with GitHub or a token, and then change the credential.
- **Size limit of local spaces** uses the stored measurement, and measures again
  first above 90%; import counts the bytes of the unpacked pages plus assets.
- A round every 10 minutes, one space at a time, the candidates from one query
  (the index on `last_used`); `last_used` keeps the latest use in memory and
  writes to the database at most once per hour per space; after an upgrade,
  spaces with no record take the startup time, so they are not all taken as idle
  right after the upgrade.

Measurement (`PETALBOOK_MEASURE=3000 go test ./internal/spaces -run MeasureStorage -v`,
3000 pages, 5 versions per page, each about 4 KB of Chinese text, generated by
`fast-import`, local SSD; the text is repetitive, so the compression ratio is
far better than real data, and the `.git` row is only good for proportions):

| Item | Full clone | Partial clone | After eviction |
|---|---|---|---|
| Workspace text | 8.9 MB | 8.9 MB | 0 |
| `.git` | 2.4 MB | 1.3 MB (the saving is old versions) | 0 |
| Index | 11.1 MB | 11.1 MB | 11.1 MB |
| Total | 11.3 MB (without index) | 10.2 MB (without index) | 11.1 MB (index only) |

Times: partial clone, checkout and build the index at connect 1.3 seconds;
eviction (hold, check the remote, move away, delete) 69 ms; search plus list
after eviction (including opening a space with only the index) 8 ms; restore
(partial clone, checkout, set modification times back, open; the index sees no
change so nothing is re-read) 256 ms. The estimate and measurement for 50,000
pages still have to be done on the `spikes/search-scale` corpus; this only proves
the order of magnitude and the flow, not the 200 MB figure.

Known issue: if the remote is force-pushed (history rewritten) while a space is
evicted, the old history exists only if the remote still keeps it; the server
has nothing but the index, what is restored is the remote as it is now, and the
space shows `rewritten` for the owner to accept. Versions that were rewritten
away and exist only in the old history cannot be found again.

**3.1 as built (part two, 2026-11-23)**: the separate asset repository and the
asset cache are done: `GET/PUT/DELETE /api/spaces/{space}/remote/assets`,
`POST …/remote/assets/move` (background, `202`, continues after a power cut),
`github.assets` (the `github` of `POST /api/spaces` and of
`PUT …/remote/assets`), `cache.assetsBytes` and `assets.newSpaces`, `assets` /
`assetCacheBytes` in Space and the admin console, `ETag`, `503 not_cached`, and
the UI in the settings page, the admin console and new-space creation. Question
4 takes the default: after the move, the space repository no longer has
`assets/`. Where it differs from the contract, and details settled during
implementation:

- **The state database uses a new table `asset_remotes`, not a new column on
  `remotes`**: the primary key of `remotes` is `space_id`, and many queries
  (counts in the admin console, reference counts of GitHub installations,
  `UPDATE … WHERE space_id`) depend on it, and changing the primary key would
  change all those queries in both databases. The new table has the same columns
  plus `used` (an asset has entered it) and a few columns for move progress, and
  the migration is `0017_assets.sql` (0016 is left for the groups of 3.2). It is
  deleted together with the space.
- **The table has no file size**: in a partial clone the size of a file that was
  never fetched cannot be read (`ls-tree -l` gives `BAD` when contents are
  missing), so the table is (path, content id), and the size is known only when
  the content enters the cache (on upload it is known at once). A size of `-1`
  from `Lookup` means unknown; when serving a file the size of the cached file
  itself is used.
- **The asset repository on the server**: `DATA/assetrepos/<space>`, a clone
  with only commits and trees (`--filter=blob:none`) whose workspace is always
  empty. Contents do not enter this clone: uploads and moves make commits with
  low-level Git commands (`hash-object -w`, `ls-tree`, `mktree --missing`,
  `commit-tree`, `update-ref HEAD`) in a temporary repository that borrows its
  objects (`objects/info/alternates`), and delete it after pushing; when reading
  missing contents, they are fetched by id into another temporary repository,
  copied into the cache, and then deleted. So the contents of assets on the
  server exist as one copy only, in the cache, bounded by `cache.assetsBytes`.
  The restricted git executor allows only these extra options:
  `hash-object -w --no-filters --stdin-paths`, `mktree -z --missing` and
  `cat-file blob <id>`. There is no `write-tree`: it reports "invalid object"
  when contents are missing.
- **The table**: built when the space is opened from the tree of the local clone
  (20,000 files: 50 ms, 3.2 MB of memory), updated incrementally on pull (every
  minute) from the differences of `diff --raw`, and fetched again after an
  upload; a request never walks all assets. Validation uses the same table
  (`assetExists` first looks in the table and then at the space's own `assets/`),
  and when the asset repository is unreachable the table is still the last one.
- **Upload**: push before answering (one commit; if the push is rejected, which
  means the remote moved, redo on the new head, at most 4 times). Import pushes
  the assets before the commit of the pages (in `mutate`, after `op` and before
  the commit), and when the pages' push fails the assets stay on the remote with
  nobody referencing them (a test rejects the page push with `pre-receive`).
  **So "make sure assets are pushed before the page is committed" holds by
  order, with no separate queue of things to push**: the asset repository never
  has anything unpushed.
- **Steps of the move and crashes** (`internal/spaces/assets_move.go`, with an
  `AssetsHook` between steps, and tests "crash" after each step, swap in a new
  manager, and the space opens as usual, the move continues, and no asset is
  lost; after each step it is checked that every asset is in at least one
  remote with matching content ids): copy (in chunks, each at most
  `MaxCommitBytes`) → at the switch, hold the space's writes, copy what was
  added, and **after `refresh`, check one by one by content id that the remote
  of the asset repository has these files**, and only then make the commit that
  deletes `assets/` with low-level commands (without touching files) →
  `reset --hard` so the workspace catches up → push. Crash after the commit and
  before the push: the next time the space is opened that commit is pushed as a
  "commit that was never pushed"; crash after the commit and before `reset`:
  `RecoverSwitch` finishes only if it recognizes that HEAD is the switch commit
  (by the message and `assets/` no longer being in the tree). Push failure:
  `reset --hard` back to the original commit, and the files come back as they
  were. Step 3 (a new partial clone) is done by evicting the space: after the
  move it tries one `Evict`, and if the space is in use it is left for a later
  eviction, and the next restore is a partial clone without assets. During the
  move, uploads go into the space's own `assets/` (`Direct()` is false), and
  reads look at the space's own files first without using the network; as soon
  as the switch completes (still holding the space's writes), uploads go directly
  to the repository.
- **Extra checks when evicting a space** (`verifyAssets`): no move in progress,
  the status of the asset repository is `ok`, after a move there are no files in
  the clone's `assets/` (files ignored by `.gitignore` are invisible to the
  ordinary "uncommitted files" check, and would vanish when the clone is
  deleted), no upload being pushed, no temporary repository left by a previous
  upload, and a fetch of the asset repository's remote done on the spot works;
  if it is unreachable it answers `503 unavailable` and does not evict. When
  evicting, this space's files in the cache are evicted together. Test: after a
  crash at each step, all assets can still be read, and both remotes have them.
- **Connecting**: token, SSH (using the key pair of the space itself, and the
  public key must also be added to the asset repository), GitHub (an existing
  repository, or created on the spot with `github: {create: true}`). First a
  fetch in a trial directory (proving the credential works), then the rules for
  a repository that has contents are applied, and only if it passes is it stored
  in the database and moved into place, so if it is unreachable or cannot be
  accepted nothing is changed. **Added beyond the contract**: when a space has an
  asset repository, `DELETE …/remote` (disconnect the space's remote) answers
  `409 conflict`, because after disconnecting a local space cannot read the
  assets.
- **Automatic creation on GitHub**: both the space repository and the asset
  repository are created first, and then the checks of step 4 are done for each,
  so a failed answer lists both `repo` and `assetsRepo` (`github.Error` gained
  `AssetsRepo`), and the same request with `create: false` connects both (the
  test uses the fake GitHub of 2.7, including the case where the top-up of an
  installation limited to "selected repositories" fails). When `github.assets` is
  not given, `assets.newSpaces` decides, and when `false` is given it is not
  created.
- **Pinned**: files in the asset cache that belong to a pinned space are not
  evicted and not counted in `assetsBytes` (the `Pinned` callback of
  `assetcache`), and when pinning, the background fetches all the files in the
  table into the cache.
- **Cache**: one copy per file, named by content id, with the modification time
  being the last read (changed at most once per hour); the total is kept in
  memory and counted once at startup (only then; it also reads every file once
  and deletes those whose size and content id do not match, so it is
  O(cache bytes)); a write first does `fsync` on a temporary file and then
  renames it; when over budget the least recently read are deleted first, down to
  90%, so that once full it does not scan on every added file. When several
  requests want the same file at the same time they share one fetch.

Nothing else changed beyond the endpoints: `503 not_cached` carries `fetching`
and `Retry-After`, `ETag` is the content id (`If-None-Match` answers `304`), and
a path not in the table gets `404` without asking the remote.

Tests: `internal/spaces/assets_test.go` (upload, offline, move, a crash at each
step of the move, a crash at each step of eviction and the various reasons not
to evict, the order of import), `github_test.go` (automatic creation),
`internal/api/assets_test.go`, `internal/assetcache`,
`internal/gitstore/assets_test.go`; both SQLite and PostgreSQL are run
(`scripts/test-postgres.sh`). Frontend: `assets.test.tsx`, `storage.test.tsx`,
`github.test.tsx`, and the contract test (the mock plays the move back step by
step).

Measurement (`PETALBOOK_MEASURE=20000 go test ./internal/assetrepo -run Measure -v`,
20,000 files of 100 KiB in 2000 folders, local SSD, a `file://` remote, with
repetitive content, so the disk sizes of the remote and the clone are not a
useful reference):

| Item | Measured |
|---|---|
| First fetch of commits and trees and building the table | 145 ms |
| Reopening (building the table from the local clone) | 49 ms, the table takes 3.2 MB of memory |
| Looking up one path in the table | 27 ns |
| Reading a file that is in the cache (open) | 11 µs |
| Reading a 100 KiB file not in the cache (fetch by id, including building a temporary repository) | 44 ms |
| Uploading one 100 KiB file (hash, change the tree, commit, push, refresh the table) | 172 ms |

An asset in the cache is read as before from the workspace (one `open`), and the
first read of one not in the cache costs one more network request. The size of a
space has not yet been measured on the 50,000-page corpus of
`spikes/search-scale`, and the time to move 5 GB of assets is still owed.

Known issues, carried forward:

- After the copy has pushed part of the assets, if an asset in the space changes
  again (an outside change, or an eol rule in `.gitattributes` makes the
  checked-out content differ from the committed one), the move stops every time
  at "the asset repository has a different file". Nothing is lost, but it has to
  be handled by hand.
- Every pull does `ls-remote` and then `fetch` on the asset repository, even
  when nothing changed.
- After the move, an outside commit that adds tracked files back under
  `assets/` blocks evicting the space. Nothing is lost.
- Noted in the second round of review:
  - When the branch of the asset repository is gone, what really stops the move
    from deleting `assets/` is "clear the table on a forced refresh"; the new
    tree check in `Missing` alone cannot stop it (the old remote-tracking ref
    still points to the old commit). Whoever changes this later should not rely
    on `Missing` alone.
  - At startup a hash is computed for every file of the asset cache,
    synchronously before the service starts to answer: about 7 seconds for
    5 GiB on an SSD, half a minute to a minute on a cold spinning disk; pinned
    spaces also have to be computed and are not counted in `assetsBytes`, so with
    a large volume it may drag to several minutes. Later change it to a
    background check (serve first, and until the check finishes validate a file
    that was read once more by id).
  - The check for changing the address of the asset repository reads the local
    table; if the push succeeded, the three attempts to record "used" all failed,
    the refresh after the push also failed, and then a restart followed, all
    together, the table is empty and changing the address is let through. Nothing
    on the remote is deleted, but the space would point to the wrong repository.

Left over from 2.7 and part one, and still not done here: the counts in
`GET /api/admin/status` are still stored measurements plus numbers in memory;
`cloneBytes` does not include `DATA/assetrepos` (each is only a few hundred KB to
a few MB of commits and trees); the count of repositories a GitHub installation
is referenced by (the statistics of `github_installations`) does not count the
asset repositories.

**Fixes from the second review of 3.1 part one (2026-11-23)**: `MakeFull` first
does `StopFilter` and only at the end `LeavePartial`, while `IsPartial` used to
look only at `remote.origin.partialclonefilter`: a clone interrupted between the
two (fetch failed, space closed, restart) was missing old versions but was
taken as complete, so after reopening it did not fetch on demand, old versions
could not be read, `MakeFull` returned at once and could never fill them in;
and retrying a disconnect from the remote would succeed with the history
missing, which is data loss. Now `IsPartial` looks at
`remote.origin.promisor` / `extensions.partialclone` (removed only by
`LeavePartial`, after `MissingAll` is empty), and the restricted Git allows only
this one more kind of `git config --local --get-regexp`. Tests:
`TestAnInterruptedMakeFullLeavesAPartialCloneThatIsRepairedLater` (the remote is
disconnected so that the disconnect stops after `StopFilter`, after a restart it
is still a partial clone and old versions can be fetched, and the retried
disconnect becomes a local space only after the history is filled in) and the
gitstore test `TestAClonePartialUntilItLeavesPartialModeNotUntilTheFilterStops`.

**3.1 merged into main (after 2.7b and 3.3, 2026-11-23)**: following the 2.7b
contract (see the 2.7b contract in
[phase-2-connections.md](phase-2-connections.md)) and the hooks of 3.3:

- **Connect**: `github` and `github.assets` of `POST /api/spaces` become
  `storage.sync` and `storage.sync.assets`; the 3.1 `remote` (by address, with a
  token) stays at the top level, and is the "Other (by address)" of "where it is
  stored"; giving it together with `storage.sync` is `400 invalid`. The `github`
  of `PUT …/remote/assets` becomes `sync` (with `provider`), and the credential
  type `connection` carries `provider`; `asset_remotes` also has a `provider`
  column (`0017_assets.sql` changed in place, like 2.7 changing 0013, since it
  has not been released). The error codes are the generic ones (`push_failed`,
  `repo_not_in_installation`, … with `provider`), and `connections.Error` gained
  `AssetsRepo`. After creating a space or asset repository fails, the empty
  repositories left behind are both recorded as reminders; when the asset
  repository is connected its reminder is also cleared. The GitHub counts
  (installation, user, all) and the counts of the `git` provider both count the
  asset repository, once per space.
- **Requiring a remote**: `remotes.require` takes `null`, `"any"` or a provider
  id; an id the server does not have is `400 invalid`, and one it has but is not
  configured is `409 provider_not_configured`; only connections through this
  provider count as satisfying it. The queries decide by
  `cred_type = 'connection' AND provider = ?`.
- **Allowed hosts**: the `hostAllowed` of 2.7b now uses `hostPolicy()` of 3.1,
  that is `hosts.allowed` in the settings (when `--remote-host` is given the flag
  still decides). The 3.1 settings are still one site-wide record and do not go
  into the layered settings table of 3.3; the tenant layer of `require`,
  `graceDays` and `localSpaces.maxBytes` will be added later following the
  reservation of 3.3.
- **Tenants**: `/api/admin/storage`, `PUT /api/admin/spaces/{space}/storage`,
  `POST /api/admin/spaces/{space}/evict` and the user's `remoteExempt` are for
  site admins only (the server's disk, the site-wide requirement); the `storage`
  block of `/api/admin/spaces` follows the scope of 3.3 (a tenant admin sees
  their own tenant). The new storage queries of 3.1 are all site-wide
  background jobs, statistics for site admins, or by an already found space id;
  they went into the scope allowlist with a written reason; the connection of
  the asset repository is checked like the space's own: the person with
  `via: "user"` is in the space's tenant.
- **Usage**: when measuring the size of partial clones and of evicted and
  being-evicted spaces, only the list's `size_bytes` is written
  (`SetSpaceSize`), `RecordMeasurement` is not called, and `usage_bytes` keeps
  the value of the last full measurement; the test is
  `TestMeasuringAPartialCloneLeavesItsUsageAlone`. The workspace measured for a
  space with an asset repository does not include assets and still goes through
  `RecordMeasurement`; this point is left to be decided together with the
  measurements of 3.5.
- **Migrations**: both orders of 0015 and 0017 with main's 0016 and 0018 were
  tested (`storage_migrate_test.go`); the test of the number of migrations
  counts files anyway.
- **Known issue**: for a space with an asset repository, the size measurement
  does not include its assets (they are not in the workspace), so usage and
  quota undercount the assets; left for 3.5 to decide.
- **Development databases must be rebuilt**: a development database created with
  the old feat/storage or a 2.7 build has had its migrations (0013, 0017) changed
  in place, so the old database cannot run and has to be deleted and rebuilt.

### Milestones of phase 3 (plan, 2026-11-23)

The basis for the order: first what teams can use right away and what is a
prerequisite for the later steps (groups), then the preparation for multi-tenancy
and registration (a tenant puts groups, providers and settings under it, so it
comes after groups), and last the Git storage module (it is worth fixing its
shape only after the changes of both 2.7 and 3.1 to `gitstore` are merged and
the interface no longer moves). One PR per milestone, and it still runs in
single-user mode (see 1.7 in [phase-0-1.md](phase-0-1.md)).

| Milestone | Content | Depends on |
|---|---|---|
| 3.1 | Save server space: mandatory remote, partial cache, separate asset repository (see above) | 2.7 |
| 3.2 | Groups: local groups, mapping IdP group claims by provider, groups as space members | 2.8 (providers); unrelated to 3.1, can run in parallel |
| 3.3 | Preparation for multi-tenancy and open registration: a tenant layer in the settings, data isolated by tenant, quota reservation, a registration switch (off by default) | 3.2 (groups go under tenants), 3.1 (quotas and the disk budget share the measurement) |
| 3.4 | Git storage module: first settle the package boundary and interface inside this repository; publishing it as a module under KazuhaHub and switching Rackbook over needs the owner's nod | 2.7, 3.1 (both change `gitstore`) |
| 3.5 | Storage management: usage and status by person and by space (read-only stored measurements), recycle bin, deleting and transferring people, batch actions and second confirmation, admin viewing and export (all audited), a per-tenant single-file limit; **archive** (a tier below eviction) and smart restore at sign-in; **a site storage budget and a storage-pressure controller** (replacing the separate cleanup thresholds); automatic detection of partial sync | 3.1 (cache state, cleanup, `lastUsed`), 2.7b (disconnect connections when deleting a person, record per provider whether partial sync is supported); 3.3 |
| 3.6 | Tenant autonomy, finishing the permission model: each tenant's capabilities and **limit table** (set by hand by the site admin, each value records its source, can have a start and end date; the boundary for plans and billing is reserved, and neither billing nor payment is done); two layered policies, where assets go and "remotes that cannot do partial sync"; all admin items sorted by list into three kinds; layering gets a "minimum requirement" and a reason; the site admin's tenant detail page; storage mode (server, own remote, the tenant's own storage target); a tenant's own SSO, email domains (DNS verification) and the per-domain SSO entry, sender, captcha, daily-text interface, own connections (GitHub App, etc.); social sign-in separate from SSO; several sign-in methods for one account, preset templates for social sign-in and custom OIDC / OAuth 2.0 providers (added by the owner 2026-11-23); email unique site-wide and sign-in mainly by email; four ways to join an organization; organization self-registration | 3.5 (the same admin pages and isolation table, the tenant share of storage pressure), 2.7b (the provider interface, multiple Apps, automatic repository creation), 3.1 (the tenant layer of the storage settings, partial cache); the GitLab / Gitea storage targets need 3.8 |
| 3.7 | One account in several organizations (moved up from phase 5): a personal home and an organization membership table, switching organizations in the UI, an organization admin manages only the organization's things, managed accounts; join-by-domain and self-service organization creation of 3.6 become multi-member here | 3.6 (the ways to join, domains and self-registration are all first done as "one person, one tenant"; capabilities) |
| 3.8 | Connections for GitLab (gitlab.com and self-hosted) and Gitea / Forgejo / Codeberg: one admin-configured OAuth app per host, and the user connects once; create repositories, sync, and serve as a tenant's storage target (added by the owner 2026-11-23) | 2.7b (the provider interface); can run in parallel with 3.5, and goes before the storage-target part of 3.6 |
| 3.9 | Avatars and brand icons: people's avatars, space icons (stored in the space's own repository), organization logos; every upload is re-encoded on the server; the sign-in buttons, connections, remote and storage pages use the official brand icons, bundled with the web app (added by the owner 2026-11-23) | 2.7b (connections, using the connection's avatar), 3.5 (archiving keeps a small copy of the icon), 3.3 (organizations) |
| 3.10 | **UI overhaul** (the owner, 2026-11-24, placed after 3.5 and before 3.6, see "The 3.10 direction" in [decisions-2026-11-24.md](../decisions-2026-11-24.md)): a separate admin console (a left sidebar of major items, tabs at the top for sub-pages, tables, separate detail pages); the skeleton of message notifications; validation moved out of the sidebar; hover buttons for favourites and pinning inside folders; a close button for the settings dialog; errors translated by code; a full check on phone and tablet; onboarding for new users; the page header only changes values | 3.5 (the admin pages, the table template) |

**The order of 3.5, 3.6 and 3.4** (2026-11-23):

- 3.5 and 3.6 both wait until 2.7 and 3.1 are merged into `main` before coding
  starts (the contract is written after both are merged; both were merged on
  2026-11-23, and 4.1 is also merged). 3.5 comes before 3.6: the "updated admin
  table" and isolation table of 3.6 have to include the routes of 3.5, and the
  single-file limit of 3.5 is also one of the keys 3.6 has to layer.
- 3.4 can run in parallel with them: steps 1, 3 and 4 of 3.4 inside this
  repository are not affected; step 2 (settling the module interface) waits until
  the GitHub part of 3.6 is merged, because 3.6 makes `internal/githubapp`
  support several Apps (the App is passed as a parameter), and only after that
  does the interface no longer need to change. So the recommended order is
  3.5 → 3.6, with 3.4 interleaved, and step 2 of 3.4 last.
- **3.5 is reviewed in several parts** (the same PR, each part reviewed once on
  the branch):
  1. The single-file limit `files.maxBytes` (small, touches only a few entry
     points of writing);
  2. The recycle bin and deleting spaces, admin viewing and export
     (`recycle_items`, moving directories, restore, the purge job, the audit of
     viewing);
  3. Deleting people: preview, transfer rules, tombstones, whether the username
     can be reused, restore; confirmation tokens and batch jobs;
  4. The list and summary of storage management (new measurement columns, new
     columns of `usage`, indexes, filtering and sorting) and the UI;
  5. Automatic detection of partial sync (recorded by remote host, and removing
     the `cache.partial` switch of 3.1);
  6. Archive and restore: the new state `archived`, deleting the index, stepwise
     restore, prefetching at sign-in;
  7. The storage-pressure controller: measurement (stored ones, incrementally
     recorded ones plus `statfs`), four tiers with hysteresis, the order and
     scoring of reclaim, hard rules and an emergency reserve, refusing new assets
     and new spaces in a critical state; together with the deterministic
     simulator and its tests;
  8. The budget, current tier, recent reclaims, next candidates and restore-wait
     statistics on the "Storage & sync" page.
  Parts 6 and 7 delete things on the server, and each gets its own review.
- **3.6 is reviewed in several parts** (capabilities come first: every later part
  has to ask first "does this tenant have this capability"):
  1. Capabilities, the limit table and plans (including what section 16
     reserves, "admin interfaces register the permission keys they need, and the
     check goes through one function", see
     [auth-and-permissions.md §16](../auth-and-permissions.md#16-reserved-custom-roles-and-permissions)): the registry of
     capabilities, site defaults, each tenant's limit table (source, start and end
     dates, audit events for changes and for exceeding a limit, usage export),
     plans, the billing boundary (only an internal interface is kept), the
     effective set in memory, `403 capability_off`, the preview and second
     confirmation when a capability is turned off; the site admin's tenant detail
     page and `overview`, values and locks the site sets for a tenant (`setBy`,
     `siteLock`);
  2. Generalizing layering: `tighten`, strictness, explanation and reason,
     registration of all (b) keys (each key says which capability decides whether
     the tenant layer exists); the registry of feature switches `features.*`,
     `403 feature_off`, the region preset `site.profile` (added by the owner
     2026-11-23);
  3. Outbound restrictions (the public-network check, the site cap and tenant
     shares), a tenant's sender, captcha, daily-text interface;
  4. Social sign-in separate from SSO, a tenant's SSO, who owns an identity; the
     account's "sign-in methods" table, preset templates and `kind: oauth2`;
  5. Claiming email domains and DNS verification (TXT, CNAME, a daily recheck, a
     cooldown on "check now"), `discover`, the sign-in page asking for the email
     first;
  6. Email unique site-wide (a unique index) and sign-in by email;
  7. Joining an organization: four ways, join requests and approval, the default
     role and `viewer`, SSO admission by group, mapping IdP groups to organization
     admins;
  8. A tenant's own connection (GitHub App) and sharing an installation per
     tenant (needs the provider interface of 2.7b);
  9. Storage mode and the tenant's storage target: `storage.mode`, automatic
     per-space repository creation (the repository creation flow of 2.7, pointing
     at the tenant's place), the asset repository, moving when the mode changes
     (needs 2.7 and 3.1);
  10. Organization self-registration (3 per person, people on a restricted domain
      create a separate organization, the plan of a new tenant);
  11. Two layered storage policies: where assets go (site → tenant → space owner)
      and "a remote that cannot do partial sync: allow (full) / refuse" (needs the
      automatic detection of 3.5).
  Parts 1, 4, 5, 7 and 9 matter most for security, and each gets its own review.
  In part 9, the GitLab / Gitea storage targets need 3.8 to be already merged;
  if it is not merged, part 9 first does only the GitHub target.
- **3.7 comes after 3.6**: the join-by-domain, join requests, instant SSO join
  and self-service organization creation of 3.6 are all first done following the
  "one person, one tenant" of 3.3 (a new account is created in that tenant); after
  3.7 changes "person and tenant" into a membership table, these become "add an
  organization to the account", and the shape of the interfaces does not change.
  The 3.7 contract is written before 3.6 is merged, and work starts after 3.6 is
  merged (it changes exactly the isolation and sessions that 3.6 just settled, and
  managed accounts also have to check the capabilities of 3.6).
- **3.7 is reviewed in several parts**:
  1. The membership table and the home (the isolation table, the checks of
     `INSERT … SELECT` changed to go by the membership table);
  2. Sessions, tokens, OAuth and MCP carry the current organization, and the
     permission cache adds the tenant;
  3. Usage and quota recorded by (tenant, person); admin pages and audit by
     organization;
  4. Managed accounts (turning on, sign-in requirements, moving away or exporting
     the personal space);
  5. UI: switching organizations, the personal home, organizations that can be
     joined.
  Parts 1, 2 and 4 each get their own review.
- **Where 2.7b, 3.8 and 3.9 go** (added by the owner 2026-11-23):
  - 2.7b is done after the fixes of 2.7, on the branch of 2.7, and merged
    together with 2.7, so when 3.5 and 3.6 start the provider interface is
    already on `main`; the module interface settled in step 2 of 3.4 does not
    include providers (they are in the application).
  - 3.8 needs only 2.7b and can run in parallel with 3.5 (each in its own
    workspace); it comes before part 9 of 3.6, so that a tenant can use GitLab /
    Gitea as a storage target.
  - 3.9 comes after 3.5 (archiving has to keep a small copy of the space icon, and
    the table already has this column), is unrelated to 3.6 and 3.7, and can run
    in parallel. The brand icon part depends on nothing else and can be done
    first.
  - Recommended order: 2.7 + 2.7b → 3.1 → 3.5 (3.8 in parallel) → 3.6 → 3.7; 3.9
    interleaved after 3.5; 3.4 as above.
- **3.8 is reviewed in several parts**:
  1. The GitLab provider: configuring and checking the OAuth app, the user
     connection (PKCE, renewal, disconnect), listing groups and projects,
     creating projects, the credential given to Git;
  2. The Gitea / Forgejo provider (Codeberg is one Forgejo), the same;
  3. Serving as a tenant's storage target (the tenant's group and credential,
     `via: "tenant"`).
  Part 1 gets its own review (the first time an external OAuth app is handed to an
  admin to configure, with outbound requests and credentials all here).
- **3.9 is reviewed in several parts**:
  1. Brand icons: bundling, components, the guidelines of each "Sign in with X"
     button;
  2. Receiving and re-encoding images (size, pixels, format, rejecting SVG), and
     the same checks for images in the repository before they are shown;
  3. People's avatars and organization logos (stored on the server);
  4. Space icons (stored in the space's repository, with a small copy in the
     state database).
  Part 2 gets its own review (decoding untrusted images).
- **No upgrade compatibility in v0.x** (decided by the owner on 2026-11-23):
  PetalBook has not been released, so 3.5, 3.6 and 3.7 write no requirement for
  "existing deployments or data keep working after an upgrade": no "behavior
  unchanged after the upgrade", no moving old settings as they are to the
  site-wide layer, no compatibility with the `legacyCallback` of 2.8 and the old
  `--oidc-*` (the provider split of 3.6 replaces them), no old visibility and
  managed accounts brought by migration in 3.7, and no special handling of
  duplicate emails. The contracts directly state the new rules. Migration files
  still exist (numbers unchanged), only so that the databases used for
  development and demos can be opened, with no promise of an upgrade path that
  preserves data.

Note the numbering: the "second backend of 3.4" written in 3.1 means the second
backend of section 3.4 (assets) in [data-model.md §3.4](../data-model.md#34-attachments), not
milestone 3.4.

**3.3 preparation for multi-tenancy and registration** (only the direction is
listed; for the contract see the 3.3 contract in
[phase-3-tenants.md](phase-3-tenants.md), whose scope is larger than here):

- **The tenant layer**: build the tenant layer reserved in "Layering and locking
  of settings": site → tenant → space → personal, and a tenant admin can only
  tighten within what the site allows. The existing server is one default
  tenant, and after the upgrade nothing changes.
- **Data isolation**: users, spaces, groups, sign-in providers, invitations,
  tokens and the audit log carry the tenant; a request sees only its own tenant's
  things (cache keys carry the tenant too). Admins are split into site admins and
  tenant admins. The strength of isolation (the same process, the same database,
  separated by query conditions) is written down, and no stronger isolation is
  promised.
- **Quota reservation**: per tenant and per person, the number of spaces, disk,
  and number of members, with fields and measurement first (reusing the background
  size measurement of 1.7 and the disk budget of 3.1), warning only and not
  refusing; the rules for refusing are decided later.
- **Registration switch**: one for the site and one for each tenant, off by
  default; 3.3 does not build the registration flow itself (email verification,
  password recovery, abuse prevention, terms), and only wires up the switch, the
  `sso_only_with_registration` check that 2.8 left behind, and the `register`
  context of the captcha.

**3.4 Git storage module**:

- **Done first inside this repository** (needs no separate nod from the owner):
  1. `internal/gitstore` already no longer depends on the other packages of the
     application; the `internal/githubapp` that 2.7 added is also tidied to depend
     on no application code in the same way. Add a test (`go list -deps`) to hold
     the line: these two packages depend only on the standard library and their own
     third-party dependencies, and may import none of the application's packages.
  2. Settle the module interface: the restricted git executor, credentials (SSH
     deploy keys, HTTPS tokens,
     GitHub App installation tokens), fetch / commit only the given paths / push /
     retry, detection of rewritten history, read-only mirrors, and the partial
     clone and fetch on demand of 3.1. Drafts, merging, conflict copies and the
     repository creation flow stay in the `space` package. Write down which
     things are public interface, and do not export the rest.
  3. Move these two packages into a separate Go module directory in this
     repository (its own `go.mod`, with the main module pointing to it by
     `replace`), so that it really can be compiled and tested on its own. Still in
     this repository, not published.
  4. Compare file by file with Rackbook's `internal/gitstore`, list the
     differences on both sides (especially `safegit.go`), and write them into this
     section. Only read Rackbook, do not change it.
- **Waiting for the owner's nod** (outward actions, to be asked about first when
  the time comes):
  1. Create a new repository under the KazuhaHub organization (the name, and the
     license, Apache-2.0 or AGPL-3.0, are for the owner to decide: Rackbook is
     Apache-2.0 and this project is AGPL-3.0, and a module shared by both should
     use Apache-2.0).
  2. Push the code and tag the first version.
  3. This project drops `replace` and depends on the published version.
  4. Change the Rackbook repository: switch to this module, delete its own copy,
     and open a PR.
  Before the first two steps this project is not affected; step 4 is a matter for
  another repository and may come later than this project's switch.

**3.2 Groups** (the direction the owner set in the phase 3 plan):

- Groups have two sources: **local groups** made by hand in the application
  (an admin manages the members), and **synced groups** mapped from the group
  claims of a sign-in provider (members are decided only by the sync at sign-in).
- A member of a shared space can be a user or a group, and groups have roles; a
  person's effective permission is the union of the direct role and the roles of
  all the groups they are in (built-in roles nest one inside the next, so the
  union is taking the highest).
- The permission check is still the per-person cached table of 1.7: groups are
  expanded once on a cache miss and stored into this person's cache; when a group
  or a membership changes, only the affected people are invalidated. Groups are
  never computed at request time.
- The "no more than the owner's current permission" of the tokens and OAuth
  grants of 2.6 now includes groups in the owner's current permission.
- The contract is in "Groups" and the "Groups" of "Administration" in
  `docs/api.md`, and the frontend and backend follow it.

**The 3.2 contract** (2026-11-23):

Settled:

- **Two kinds of group, not mixed**: after a group is created, its source
  (`local` or `synced`) cannot change. The members of a local group can only be
  added or removed by an admin; the members of a synced group are written only by
  the sync at sign-in, and changing them by hand answers `409 group_synced`; a
  mapping can only point to a synced group (`409 group_not_synced`). The reason
  for not mixing: when a person is removed from a group in the IdP, we must be
  able to remove them for sure, without guessing which members were added by
  hand.
- **The mapping is written on the provider**: the provider gets a `groups`: a
  switch, the claim name (`groups` by default for OIDC, the attribute name for
  SAML, and for GitHub fixed to the organizations the person is in), what to do
  when the claim is missing, and a table of "value in the claim → group". The same
  group can be mapped by several providers and several values; within one
  provider a value maps to only one group. The mapping is kept in memory together
  with the provider (the cache of 2.8), and sign-in does not query the database
  for the mapping.
- **Sync at sign-in**: after a successful sign-in through a provider (and linking
  an identity with it, or accepting an invitation), the groups this person should
  be in are computed from this claim, compared with their existing memberships
  **brought by this provider**, extra ones are deleted and missing ones added, and
  those brought by other providers are left alone. One query reads the existing
  ones; it writes only if there is a change, and invalidates this person's cache
  only if there is a change. At most 1000 values are read from the claim, each at
  most 256 bytes, and values with no mapping are ignored.
- **When the claim disappears**:
  - A value is missing from the claim: at the next sign-in this person leaves the
    corresponding group. Between two sign-ins they are still in the group, and
    existing sessions, tokens and grants keep working by the group's permission. To
    take it away at once, an admin disables the person or removes the group from
    the space.
  - The whole claim is missing (the IdP did not send it, or the "overage" case of
    Entra ID where there are too many groups and it sends `_claim_names`
    instead): by default **leave** the existing groups alone and record the audit
    `groups_claim_missing`; the provider can be changed to `clear` (treat it as an
    empty list and remove from all). The claim is present but an empty list:
    remove from all.
  - An entry is deleted from the mapping table (or the group it points to is
    changed): the memberships brought by this entry are deleted on the spot. A
    membership records which provider and which value brought it, so a row of the
    same person entering the same group by another value is not affected.
  - The provider is deleted: all the group memberships it brought are deleted
    together (identities are kept as in 2.8); when a provider is only disabled
    nothing is touched.
- **Groups as space members**: needs the `members` permission, and gives a role
  (`owner`, `editor`, `viewer`) as when adding a person. A personal space cannot
  take a group (`409 personal_space`). **The rule that a space has at least one
  owner counts only direct members**: the people in a group may be cleared by the
  IdP all at once, and that must not leave the space without an owner; likewise
  the default author of commits is still the first direct owner.
- **Effective permission = union**: the permission set of the direct role and the
  permission sets of the group roles are ORed bit by bit (`perm.Set` is one byte,
  so just OR it). Built-in roles nest one inside the next, so the union is
  "take the highest"; later custom roles also use the union, with no need for an
  order. The displayed `role` is the built-in role whose permission set equals the
  union (among the three built-in roles there is always one); `directRole` is given
  in addition, and for a person who got in only through groups it is empty.
- **Cache** (the key to efficiency):
  - It is still the per-person cache of 1.7, with the user id as the key and as
    the value, for each space the person can enter, "space id → permission set,
    direct role". A cache miss is one query (direct members and members through
    groups `UNION ALL`, merged by space) and is stored in the cache; after that
    each request only looks at this one person's small table in memory, without
    touching the database or computing groups, as now.
  - The cache no longer stores the space's name and inbox: these go into a shared
    small table by space id. This way, when an "all staff" group can enter several
    hundred spaces, each person's cache grows only by a few hundred pairs of "id →
    one byte"; renaming a space changes only the shared table, and there is no need
    to invalidate the caches of all members. The cache limit changes from "at most
    10,000 people" to counting by entries (default 2 million pairs, about a few
    dozen MB), and when exceeded the least recently used are dropped first.
  - Invalidation targets only the affected people: when a person enters or leaves
    a local group or the sync result changes, invalidate this person; when a group
    is added to a space, its role changes, it is removed, or the group is deleted,
    invalidate all members of this group (one query by the index on group gets the
    user ids, and they are deleted one by one under the lock, which takes
    milliseconds even for a group of 50,000); when a space is deleted, invalidate
    the direct members and the members of each group. The generation counter of
    1.7 (`listGen`) is kept, to prevent a result read before a change from being
    stored back after the change.
  - As in 1.7, it is in one process only; when several processes run on
    PostgreSQL, the cache of another process is not invalidated. This limit has
    existed since 1.7, and 3.2 does not change it.
- **Tokens, grants and drafts**: the tokens and OAuth grants of 2.6 intersect with
  the owner's current permission, and this permission is from now on the effective
  permission (including groups), so their code does not need to change: a space
  entered through a group can be signed into a token; after leaving the group or
  the group being removed from the space, the next request is `404` (the same as a
  removed member). The automatic writing of drafts and the `tools/list` of MCP also
  look at the same effective permission.
- **Listing members**: `GET …/members` still lists the direct members, with one
  more `groups` array (the group's name, role, and number of people), not
  expanded, and not paginated because the number is capped. Expanding into "every
  person who can enter this space, their effective role, and through what" goes
  through a separate paginated endpoint `GET …/members/effective`: this is a list,
  not a permission check, with one query with a join per page, paged by username.
- **Leaving a space**: a person who got in only through a group cannot "leave" by
  themselves (`409 member_via_group`) and has to ask the group's admin or the
  space's owner; a person who is both a direct member and in a group can still get
  in by the group's role after leaving.
- **Finding groups**: when the owner of a space adds a group they must be able to
  find it by name, so any signed-in person can search groups by name
  (`GET /api/groups?q=`, returning only id, name and source, and not members or
  mappings), at most 20 per call, with `q` required. The members of a group are
  visible only to admins (and the part visible in the `members/effective` of a
  space it is in).
- **Limits**: at most 10,000 groups on the whole server; at most 100,000 people
  per group (a local group takes at most 500 people added at a time); each person
  in at most 1000 groups (extras are ignored in a sync and audited); at most 200
  groups per space; at most 1000 mapping entries per provider. A group name is 1
  to 80 characters, unique without regard to case (following the `name_key` of
  space names, the same on SQLite and PostgreSQL). All lists are paginated, one
  query per page.
- **Audit**: creating, changing and deleting groups; adding and removing people of
  a local group (by whom); one line `groups_synced` when a sign-in sync changes
  something (which were added, which removed, which provider); one line each for a
  missing claim and for exceeding a limit; a group added to a space, its role
  changed, removed. A sync with no change writes nothing.
- **Storage**: the migration `0016_groups.sql` (0013 is 2.7, 0014 is 2.8, 0015 is
  3.1). `groups`, `group_members(group_id, user_id, provider, value)` (the
  `provider` of a local group is empty; one index each on `user_id` and
  `group_id`), `space_groups` (one index on `group_id`); the mapping is stored in
  the configuration of `sign_in_providers`. Both SQLite and PostgreSQL are tested.
- **Single-user mode**: there are no accounts and no groups, and these endpoints
  all answer `404`.

Not done (the reasons are in parentheses):

- **Nested groups** (a group inside a group): expanding needs recursion and cycle
  prevention, and invalidation has to follow the tree to find people; the IdP
  side has generally already expanded nesting (Entra ID and Keycloak can both
  send expanded groups). If done later, it would also be expanded at write time
  into flat memberships, not recursed at request time.
- **SCIM**: the IdP actively pushes users and groups, which solves "not synced
  between two sign-ins", but needs a new set of endpoints and tokens, and is left
  for a separate job later.
- **Automatically creating groups for unmapped values**: Entra ID sends the GUID of
  the group, so the automatically created group name would be a string of GUID;
  and anyone who can change IdP groups could make groups here. Only explicit
  mapping is done.
- **GitHub teams**: it needs reading `/user/teams` with extra pagination, and a
  mapping breaks when a team is renamed; 3.2 uses only the organizations that 2.8
  already reads.
- **Instance roles by group** (people in an IdP group automatically become
  admins): admin permission is too powerful, and is still given only by an admin
  by hand.
- **Group admins** (letting a non-admin manage some local group): for now only
  admins manage.
- **Audit of direct member changes**: since 1.7 adding, changing the role of, and
  removing people in a space has not been audited; 3.2 adds it only for groups,
  and direct members will be filled in together later.
- **Expiry between two sign-ins** (a synced membership becomes invalid if there is
  no sign-in for N days): see question 2 below.

For the owner to decide (do it first by the default in parentheses):

1. Local groups and synced groups are separate and cannot be mixed (separate;
   mixed together, people removed in the IdP cannot be deleted cleanly).
2. Between two sign-ins, should synced group memberships expire? For example,
   removed automatically if there has been no sign-in through this provider for 30
   days (no expiry: rely only on the sync at the next sign-in; otherwise scripts
   that use only tokens and long sessions would suddenly lose permission one
   day).
3. When the whole claim is missing, leave existing groups alone (leave them, audit
   it; can be changed to clearing per provider).
4. A space's at-least-one-owner counts only direct members, and an owner in a
   group does not count (only direct members).
5. Any signed-in person can find group names by name (yes, only names are given,
   `q` required, at most 20 per call; otherwise the owner of a space could not add
   a group by themselves and would have to ask an admin).
6. A person who got in only through a group cannot leave the space by themselves
   (cannot, answers `409 member_via_group`).
7. When a provider is deleted, the group memberships it brought are deleted
   together (delete; identities are still kept).
8. Limits: 10,000 groups, 100,000 people per group, 1000 groups per person, 200
   groups per space (as these).

**3.2 as built (2026-11-23)**: everything in the contract above is done, and the
owner questions follow the defaults in parentheses. For the endpoints see "Groups"
and the "Groups" of "Administration" in `docs/api.md`, and the migration is
`0016_groups.sql` (both SQLite and PostgreSQL are tested). Settled during
implementation:

- **Cache**: each person has a small table sorted by space id, each entry being
  "space id, permission as one byte, the permission of the direct role, whether it
  came through a group", and a request does a binary search on this table; the
  space's name, inbox and whether it is a personal space are in another shared
  table by space id. The limit is counted by entries (default 2 million entries,
  about 24 bytes each, about 50 MB when full), and when exceeded the least
  recently used person is dropped first; when one person's table is bigger than
  the whole limit it is only returned and not stored. `listGen` is kept: a result
  read before a change is not stored back after the change. Renaming a space
  changes only the shared table and invalidates nobody.
- **One miss**: one query does `UNION ALL` of the direct members and the members
  through groups, the half through groups is first grouped by space and role (at
  most three rows per space for one person) and then joined with `spaces`; in Go
  they are merged by space into the union.
- **Invalidation**: a person entering or leaving a local group or a sync change:
  invalidate this person; a group added to a space, its role changed (no
  invalidation if the role is unchanged), removed, or the group deleted: first take
  all the people's ids by the index on group within a transaction, and after the
  commit delete them one by one under one lock; a space deleted: the direct members
  and the people of each group. Invalidating a group of 50,000 once takes
  milliseconds. Before reading the people in a group, the row of this group is
  locked (space first and then group; when a sync locks several groups at once,
  they are sorted by group id), and adding people to a group (by hand or by sync)
  also locks it, so under READ COMMITTED on PostgreSQL nobody can be added between
  "reading the list" and "commit" and stay in the cache with the old permission.
- **Sync**: the existing memberships are read with one query (an index on
  `user_id` plus `provider`) and compared with this claim; a transaction is opened
  to write only if there is a change, and only then this person is invalidated and
  `groups_synced` written. The limit of 1000 groups per person is counted inside
  the writing transaction (delete first, then add), extras are not added, and
  `groups_claim_truncated` is written (`limit` is `group_limit`). The mapping table
  is in the provider object, and sign-in does not query the database for the
  mapping.
- **Changing a mapping**: when a provider is changed, entries that are in the old
  table but not in the new one (or point to another group) have the memberships
  they brought deleted on the spot; when a group is deleted, the entries pointing
  to it in each provider are first removed, and then the group is deleted. When a
  provider is deleted, all the relations it brought are deleted. These deletions
  and the write of the provider's row are in the same transaction, and if it fails
  the whole request fails. Every write of the provider adds one to
  `groups_version`; the sign-in sync first locks the provider's row, and writes only
  if the version is still the one when it read the mapping, otherwise skips (syncs
  again at the next sign-in), so a sign-in holding an old mapping does not add back
  a relation that was just deleted.
- **Different from the contract, or not stated in the contract**:
  - The group claim of OIDC reads only the ID token, and does not go on to read
    userinfo (the OIDC library used does not fetch userinfo; the Entra ID overage is
    "do not read, treat as missing" anyway, so nothing is lost). The sentence "then
    read userinfo" in the contract has been changed.
  - GitHub: when group sync is on, each sign-in reads `/user/orgs` additionally (at
    most 5 pages, 500 organizations); if no organization restriction is set, or the
    organization the restriction needs was already read in the earlier pages, a
    failure to read later pages does not refuse the sign-in, and is treated as a
    missing claim.
  - The Member in the replies of adding a member and changing a role still has the
    original five fields; `viaGroups` and `effectiveRole` are only in the member
    list (old clients and the contract test are not affected).
  - A group in the path that does not exist answers `not_found`, and a group in the
    request body that does not exist answers `group_not_found` (the wording of the
    contract, separated during implementation).
  - Adding people with no change (all "already in") does not write
    `group_member_added`.
- **Tokens, grants, drafts**: their code was not changed, and the tests added prove
  that tokens and OAuth grants gain and lose permission as the owner joins and
  leaves a group, as a group's role changes, as a group leaves a space, and as a
  group is deleted, and the automatic writing of drafts also looks at the union.
- **UI**: administration gets a "Groups" page (list, filter by name and source,
  create, rename, delete, asking first and then forcing when in use, the member
  list, picking people from a search or pasting usernames, at most 500 at a time,
  and the spaces it is in); the provider form gets a "Groups" mapping (the switch,
  the claim name, what to do when missing, the table of value to synced group); the
  members page of space settings gets groups (add, change role, remove, search by
  name) and "Who can enter this space" (paginated, searchable, read only when
  opened); account settings gets "My groups". On narrow screens everything is
  vertical and wrappable, and tables scroll horizontally.
- **Measured** (SQLite, local, `go test -bench`, see
  `internal/spaces/groups_test.go`):
  - One person in 1000 groups, which together are in 200 spaces: one cache miss (one
    query plus building and sorting the table, including the names of 200 spaces)
    takes about **4.7 ms**;
  - A hit (one person in 200 spaces asking about one of them) takes about **96 ns**,
    the same order as one table lookup of 1.7, without touching the database or
    expanding groups.
  - After a group is added to a space, each of its N people reads the database once
    more, each being the single query above.

3.2 known issues, carried forward:

- **Multiple processes**: as in 1.7, the cache is in one process only; when
  several processes run on PostgreSQL, the cache of another process updates only
  when entries are pushed out. Groups make this limit more noticeable (one
  person's access may come from several changes at once). Real multi-process
  needs an invalidation broadcast, left for later.
- **The number of people of each group in the `members` list**: one count by the
  group index per group, at most 200 groups per space and at most 100,000 people
  per group, so in the worst case one list is 200 large counts. If it becomes a
  problem, store a head count in `groups`.
- **The sync does not update between two sign-ins**: tokens and long sessions
  still use the group's permission until the next sign-in; to take permission away
  at once see "When the claim disappears" above. SCIM is left for later.
- **OIDC userinfo and the Entra ID overage expansion** are not done (see above);
  for overage the only recourse is the `missing` setting.
- **The GitHub organization value is the login name**: the mapping and the
  organization restriction both compare the organization's login. After an
  organization is renamed or deleted, the old login can be registered by someone
  else, and the members of the person who registered would be mapped in. Mapping by
  organization id is left for later.
- **Invalidating many people at once**: invalidating 100,000 people in the cache
  takes about 8.7 ms under the cache's lock, and other requests wait during that
  time; after that, each of these people does one more query at their next
  request.
- **An admin in a read-only group**: if an admin is in a space's viewer group, what
  they get in that space is viewer permission, as when an admin was a direct member
  since 1.7 (admin status itself gives no permission in a space).
- **Noted in the second round of review**:
  - When one change of a provider removes two entries mapped to the same group, and
    it happens to forcibly delete this group at the same time, there may be a
    deadlock on the rows of `group_members` on PostgreSQL; the database detects it
    and reports an error, and does not hang, and it existed before the fix.
  - A sign-in sync of the same provider that has changes now has to hold the
    provider's row until the commit, so they go one after another; this is a
    throughput cost, not a deadlock.
  - If `reloadProviders` fails after a provider change is committed, the sync of
    this provider is skipped until it is read again; it is not permanent, and
    nothing is lost.
