# Milestones: phase 3, storage management (3.5)

**The 3.5 contract** (2026-11-23): the endpoints and error codes are written in
the new "Storage management" section of `docs/api.md`. The design was written
after 2.7 and 3.1 were both merged.

Decided:

- **Read only stored measurements**: the lists and totals for people and spaces
  read the 1.7 measurements, the 3.1 `cloneBytes` / `indexBytes` / `lastUsed`,
  the 3.3 `usage` row, and the attachment-cache total held in memory. A request
  never walks the disk or a repository. A list is one query per page, and the
  cursor is "sort value:id", not an offset.
- **New measurements**: `users.last_active` (written when a session or token is
  used, at most once an hour) and `users.first_write` (the first time the person
  changes anything in a space, written once). On the `usage` row, these are
  maintained by deltas: bytes held on the server, number of spaces that follow a
  remote, spaces in the grace period and overdue, purged, archived, pinned, and
  whether an account is connected (any 2.7b connection). The daily
  reconciliation corrects them all together. "Registered but never connected and
  never used" is a stored `dormant` flag, and the filter is a single index.
- **One item 3.1 left open, to be settled here** (added at merge, 2026-11-23): when
  a space with an attachment repository is measured, the working tree does not
  include the attachments, so `usage_bytes` and the quota undercount them (see
  the "known issues" of "3.1 merged into main" in [phase-3.md](phase-3.md)). Before part 4 of the 3.5
  measurements starts, decide whether the size of the attachment repository
  counts toward the person's usage, and how to measure it (not by walking in a
  request). Until it is decided, follow 3.1: only the text repository counts.
  **Decided (the owner, 2026-11-23): usage is counted by "where it lives"**; the
  quota limits what takes our resources:
  - Everything stored with us counts: spaces only on the server (the bytes of the
    local directory), and repositories in a site-wide storage organization (both
    the text repository and the attachment repository);
  - What lives on the person's or the tenant's own remote (`own`, `tenant`) is
    **not counted toward quota**, only shown. The cache the server keeps for them
    (partial clones, attachment cache) is managed by the storage pressure
    controller and does not enter a personal or tenant quota;
  - How to measure: for local spaces, as before, the background measurement writes
    `usage_bytes`. For repositories in a site-wide storage organization, ask the
    provider's API (the `size` of a GitHub repository, the statistics of GitLab,
    from 3.8), in the background, one at a time, at most once every few hours per
    repository, and **never download a repository just to measure its size**. The
    attachment repository is asked the same way.
  - Bytes are real byte counts (MB / GB), with the number of pages and spaces
    recorded separately; usage is not counted by number of files.
- **Recycle bin**: every deletion of a space or a person goes to the recycle bin
  and stays for `recycle.days` (site-wide, default 30 days). After that, an hourly
  job purges one item at a time. The space's records are stored whole as one JSON
  document and deleted from the live tables (so no query sees a space in the
  recycle bin, and no condition has to be added everywhere). The directory is
  moved to `DATA/recycle/`. On restore, the records are put back with
  `INSERT … SELECT` under the original id and in the original tenant. Remote
  repositories and attachment repositories are **never touched**. A space in the
  recycle bin does not count toward quota, and a restore is never refused because
  of quota. From 3.5 on, deletion only goes to the recycle bin and no longer uses
  the 1.7 `trash/`.
- **Deleting a person**: first deactivate (sessions and tokens stop working at
  once), then transfer, then leave, then recycle. Each step is its own
  transaction, so stopping halfway only leaves "deactivated, fewer spaces" and
  never a space without an owner. The rules: personal spaces, and spaces where he
  is the only owner and there are no other members, go to the recycle bin. Spaces
  where he is the only owner and there are other members are transferred. The
  heir is, in order: the one named per space in the request, the earliest direct
  member who is an editor, or the `heir` in the request. Read-only members and
  group members are never promoted to owner automatically, and if there is
  nobody to choose, the whole request is refused (`409 heir_required`). A space
  synced with his own token is left in "credentials invalid" after the transfer,
  waiting for the new owner to reconnect, and nothing is lost.
- **When a person's record is really deleted**: on entering the recycle bin only
  `deleted_at` is set, and the username is still taken. On purge, the personal
  data is deleted (password, passkey, identities, email, name, preferences,
  drafts, sessions), and the row stays as a tombstone (id, tenant, deletion time,
  username changed to `deleted-<id>`). It is kept as long as the audit log, or a
  group or invitation he created, still refers to it. A daily job deletes
  tombstones that nobody refers to. Git history is never rewritten.
- **After a purge, the username and email become free** (decided by the owner,
  2026-11-23): they stay taken during the 30 days in the recycle bin. At purge,
  the old username is recorded in `retired_usernames` (in its case-folded form),
  and the email is deleted with the personal data. The site-wide setting
  `users.reuseUsernames` is `after_purge` (default; others may use it after the
  purge) or `never` (a recorded username is never issued again). It is recorded
  either way, so changing to `never` later still protects the names purged
  before. The cost is stated: an `@mention` written by username in a body, and old
  links, point to the new person once the name is reused. References stored by id
  (members, author records, audit) are not affected.
- **Deleted people are invisible**: they are not in lists, pickers, members or
  groups, and apart from the recycle bin and the audit, his id gives `404`
  everywhere. When a person is restored, the state before deactivation, the group
  memberships and the spaces that went to the recycle bin with him are restored.
  Transfers and leaves are not undone (the heir may have changed things already),
  and the response lists them. Revoked tokens are not restored.
- **See clearly before deleting**: a single deletion has a preview endpoint (what
  will happen, how big, the remote is untouched, whether it can be exported).
  Bulk operations and emptying early need a second confirmation: first
  `POST /api/admin/confirm` to get a token bound to the operator, the action, the
  set of ids and the plan hash (10 minutes, single use). The UI asks the operator
  to type the count, and then the operation runs with the token. If the plan
  changed, the answer is `409 confirm_stale`. A bulk operation runs in the
  background one item at a time, each item following the rules of the single
  operation. Failures are listed and the rest continue. One at a time per tenant
  and two at a time site-wide. After a restart, anything unfinished is marked
  "interrupted".
- **Admin view and export** (decided by the owner, 2026-11-23): an administrator
  may view and export any space, not only those being deleted. Viewing means
  first opening a "view" (with a stated reason), and for the next hour that
  administrator reads only that space through the ordinary read endpoints (never
  writes). Starting a view writes an audit entry, and so does each page viewed
  (at most one per person per page per hour). Creating and downloading an export
  are both audited, and the owner of the space is notified. A site administrator
  sees everything. A tenant administrator sees only the organization spaces of
  their own tenant, and from 3.6 also needs the tenant to have the capability
  `data.adminView` (from 3.7 organization administrators cannot see members'
  personal spaces, see the 3.7 contract in
  [phase-3-autonomy-and-providers.md](phase-3-autonomy-and-providers.md)). Refinements by region or compliance
  (whether to notify first, whether a second person must approve, how long the
  audit is kept) come later and are recorded in 5.14.
- **Export**: a zip with one folder per space: the files of the current commit,
  everyone's drafts, and the remote address (without credentials). The files of
  the attachment repository are fetched in too (a purged space is restored
  first). Including Git history (`git bundle`) is for site administrators only.
  Exports are kept for 7 days, with a site-wide total of 20 GiB.
- **Size of a single file**: `files.maxBytes`, two layers, site and tenant. The
  site value is `--max-upload`, and is also the ceiling. It is checked before
  writing: uploads, each file in an import, saving a page and a draft, MCP page
  writes, the 4.2 offline replay, and commits to the attachment repository.
  Existing files and files that come from a remote are not checked (failing to
  pull them in is worse than refusing). The error is `413 file_too_large`, with
  `limit`, `size`, and `name`. `maxUpload` in `GET /api/meta` becomes the
  effective value for the caller's tenant. From 3.6 each tenant's ceiling is its
  capability `storage.maxFileBytes` (not above `--max-upload`).
- **Tenant administrators**: the same lists, deletion, restore, bulk operations
  and export, limited to their own tenant. They cannot see the free disk space
  and cannot empty the recycle bin early. All the new routes go into the 3.3
  isolation table.
- **Audit**: deletion, restore, purge (spaces and people), administrator views
  (the start and the pages seen), the creation and download of exports, and the
  start and end of bulk operations are all recorded.
- **Migration** `0020_recycle.sql` (0019 is left for phase 4), tested on both
  SQLite and PostgreSQL. Only databases used for development and demos can be
  opened, new columns start empty (`last_active` and `first_write` are written
  the next time they are used), and no data-preserving upgrade is promised.

Not doing (reasons in parentheses):

- A person deleting their own account, and a person exporting their own data
  (administrators first; the personal flow needs email confirmation and a
  cooling-off period, and gets its own contract).
- Undoing a transfer from the recycle bin (the heir may have changed things
  already).
- Deleting remote repositories (we never delete remotes, see 3.1).

Decided by the owner on 2026-11-23:

1. After a person is purged, the username may be reused by others (it stays taken
   during the 30 days in the recycle bin). This is the site-wide setting
   `users.reuseUsernames`, which site administrators can change to "never reuse".
2. When a person is deleted, the default heir of a shared space is as written
   above: the earliest direct editor. If there is none, an administrator must
   name one, and read-only members are not promoted to owner automatically.
3. Administrators may view and export everything, not only the spaces being
   deleted, and every view and export is audited. Refinements by region or
   compliance come later (5.14). The 1.7 rule "an administrator cannot read other
   people's pages through the API" thereby becomes "can read, but always leaves
   an audit".

**3.5 addition: archive, site storage budget, storage pressure controller,
automatic partial sync** (decided by the owner on 2026-11-23; the endpoints are
written in the newly added subsections of "Storage management" in `docs/api.md`).
The same day the owner first decided on a set of independent cleanup thresholds
(idle days for the index, a total index budget, a minimum free disk space, and
three lines at 80 / 90 / 100%), and then changed it to **one storage pressure
controller** that replaces them. Only the controller is described below; those
older items are folded into its defaults and its "Advanced" section.

- **Archive** (a level lower than eviction):
  - The cache states of a space, from highest to lowest: `full`, `partial`,
    `evicted` (3.1: only the index is left), `archived` (new). For an archived
    space, the server keeps only the row in the state database and small
    metadata (name, the small copy of the 3.9 icon, the page count and size
    measurements), and **the index is deleted too**. Members, permissions,
    favourites, remotes and drafts are all in the state database and are not
    affected.
  - **Only spaces that follow a remote can be archived**; a local-only space is
    never archived (and never evicted either), and a manual archive answers
    `409 archive_needs_remote`. The checks before archiving are the same as for
    3.1 eviction (remote `ok`, no pending commits or rescued commits, and a
    fetch on the spot to confirm the remote is at `HEAD`). If any one fails, the
    space is not archived.
  - **Restore** = fetch the body (a partial clone) and rebuild the index. While
    archived: it is not in search (global search skips it, and a search within
    the space starts a restore and answers `503 restoring`), it is not used to
    produce the daily text, and backlinks that involve it are not shown. A
    request that opens it starts the restore, and the UI says "Restoring, about
    N seconds" (N estimated from the stored size and the recently measured fetch
    speed).
  - **Restore in steps**: archived spaces and spaces with more than 50 MB of body
    are restored in two steps. First fetch the directory and the beginning of
    each page, and build a light index (title, folder, tags, properties), so
    that lists and titles work first. The body and the full-text index are filled
    in in the background. If someone opens a certain page in the meantime, that
    page is fetched first. After the restore is complete, all bodies stay local
    as before (decided earlier by the owner: bodies always stay local, step 4 of
    3.1 is not done). The steps are only the order during the restore.
- **Smart restore on sign-in**:
  - When a person signs in (or opens the app with a valid session, counted at most
    once every 6 hours per person), the server queues, in the background, the
    evicted or archived spaces he can reach into a prefetch queue, in this order:
    personal space, then favourites, then used in the last 30 days, then the rest
    (at most 20 of the rest each time).
  - A restore that someone has opened always goes ahead of prefetch. Prefetch runs
    only in the "normal" and "elevated" pressure levels and pauses at "high" and
    above. At most 2 prefetches run at once server-wide, and prefetch cannot use
    the emergency reserve (see below).
  - Administrators can turn prefetch off, or prefetch only for people active in
    the last N days (layered, see below).
- **Site storage budget** `storage.siteBudget` ("Site storage budget"): the total
  space the server's owner gives to PetalBook. **The administrator sets only this
  one value** and everything else is derived from it. If it is not set, 80% of the
  total size of the disk holding the data directory is used. When the actual free
  space on the disk is less than what is "left in the budget", the disk wins.
  - The "Storage & sync" page (site administrators) shows: budget / allocated (the
    sum of the storage totals in the tenants' limits tables; it may be
    oversubscribed, with a warning when it is) / used (the copies of spaces,
    indexes, attachment cache, recycle bin, exports, temporary files).
- **Storage pressure controller**:
  - **Pressure** = used ÷ min(budget, actual free disk space + used). It is
    computed once a minute. Used comes from stored measurements and incremental
    accounting (added or subtracted when writing files, fetching, evicting,
    recycling), and the free disk space is one `statfs`. **A request never walks
    the disk.**
  - **Four levels** (defaults, decided by the owner 2026-11-23):
    - Normal (below 70%): only painless cleanup: expired exports, temporary
      files, and the part of the attachment cache over its limit. Spaces idle for
      a long time (nobody opened them for 30 days) are evicted at the normal pace.
    - Elevated (70-85%): eviction is faster (evict when nobody opened it for 14
      days). The attachment cache shrinks to half of its limit.
    - High (85-95%): archive the coldest spaces (180 days normally, 90 days at
      this level). Pause sign-in prefetch, and restore only spaces that someone is
      opening.
    - Critical (above 95%): pause new attachments and new spaces (answer ordinary
      people and tenant administrators with a neutral
      `503 temporarily_unavailable`, see "What is said when critical" below).
      **Saving text always succeeds.** Notify the site administrators.
    - Hysteresis: a level starts when its upper edge is reached and stops only
      after dropping about 10 percentage points lower, so it does not flip back
      and forth at the edge.
  - **Reclaim order** (least impact on people first): 1. expired exports and
    temporary files; 2. the attachment cache, least recently read first; 3. old
    version content fetched in spaces that follow a remote; 4. evict idle spaces
    (keep the index); 5. archive the coldest spaces (delete the index). Within
    one step, rank by score: score = bytes freed ÷ (estimated restore cost ×
    likelihood of being needed soon). The likelihood looks at the last open time,
    the number of opens in 30 days, whether it is a favourite, and whether the
    members have signed in recently. The restore cost is computed from the size
    of the space.
  - **Hard rules**:
    - A space being opened or edited, and a space restored within 24 hours, is
      never touched;
    - Pinned (`pin`), exempt, and local-only spaces are neither evicted nor
      archived;
    - **Emergency reserve**: by default max(5% of the budget, 2 GB). Only saving
      text and restores that someone actively opened can use it. Prefetch and
      background jobs cannot;
    - A restore that someone opened goes ahead of prefetch. Prefetch runs only at
      the normal and elevated levels, with a concurrency limit;
    - **Fair per tenant**: reclaim first from tenants that exceed their own storage
      allocation (the storage total in the limits table), and then rank by score
      inside each tenant.
  - **Other defaults**: attachment cache limit = 10% of the budget (replaces the
    3.1 `assetsBytes`); evict at 30 days, 14 days when elevated; archive at 180
    days, 90 days when high; protection for 24 hours after a restore (replaces the
    3.1 `minIdleHours`); sign-in prefetch 2 server-wide, paused at "high" and
    above.
  - **Only one knob is exposed**: the admin page has only the budget. The presets
    (conservative / balanced (default) / aggressive) and every individual value are
    in an "Advanced" section that is collapsed and hidden by default. A preset
    changes the percentages of the four levels and the days for eviction and
    archive; balanced is the defaults above. The interface is `storage.siteBudget`
    (optional) plus an optional `storage.advanced` object. "Advanced" also has:
    automatic or manual (in manual mode the controller only lists suggestions and
    the administrator clicks; the refusals at the critical level still apply), and
    the minimum index size worth archiving (default 1 MiB; archiving anything
    smaller saves nothing).
  - **Layers**: the budget, the four levels, hysteresis, reserve and attachment
    cache limit exist only site-wide. The eviction and archive days for the
    tenant's own spaces, the prefetch switch, and "people active in the last N
    days" have a tenant layer, and a tenant can only adjust within the site range
    (`tighten`: days can only be shorter, prefetch can only be less). The
    exclusions stay as before: pinned, exempt, and those kept by tenant policy (a
    tenant administrator can make some of the tenant's spaces not archive, and
    their indexes count toward the tenant's storage allocation).
  - **Visible** (site administrators): the "Storage & sync" page shows the current
    level, recent reclaims (what was reclaimed, how much was freed), and the
    spaces most likely to be reclaimed next. It also records how long people
    waited: the share of opens in 30 days that needed a restore, and the p95 of
    the restore wait. When waits grow (the share needing restore is above 5%, or
    p95 is above 30 seconds, for one hour), the controller automatically slows
    reclaiming (the days for eviction and archive double, and nothing is archived
    below "high"), and reminds the administrator to raise the budget.
  - The budget, allocated, used, level and waits all come from stored measurements
    and numbers in memory, in one query.
  - **Server-level storage pressure is shown only to site administrators**
    (decided by the owner, 2026-11-23): budget, used, pressure, level, free disk
    space, and the reclaim records do not appear in anyone else's responses or
    pages. A tenant administrator sees on the "Storage & sync" page only the
    tenant's usage and allocation, the tenant's wait statistics, and the tenant's
    next candidates.
  - **What is said when critical** (decided by the owner, 2026-11-23):
    - Ordinary people and tenant administrators get a neutral
      `503 temporarily_unavailable` with `Retry-After`. The response has no words
      about storage, budget or capacity, so one cannot tell from the response that
      the server is full.
    - UI text, shown only when the person tries to do the thing, with no permanent
      banner:
      - Upload attachment: "Can't upload attachments right now. Please try again
        later. Everything you write is still saved; you can insert an image link
        instead."
      - New space: "Can't create a space right now. Please try again later. Your
        existing spaces work as usual."
    - A tenant administrator sees the same neutral text, with one more sentence
      after it: "The site administrators have been notified."
    - Only site administrators see the real reason: a red notice on the "Storage &
      sync" page ("Storage is N% used. New attachments and new spaces are paused
      and idle spaces are being reclaimed. Raise the site storage budget or add
      disk." with the tenants and spaces that use the most), an audit entry, and a
      reminder email (at most one per 24 hours for the same state).
    - **Quotas are still stated clearly**, because they are this person's or this
      tenant's own limit: the personal quota is used up (used / limit), the tenant
      quota is used up ("Please contact the administrator of your organization"),
      and the file is too large (the limit is given). These are `quota_exceeded`
      (`level` says whether it is the personal or the tenant level, and `quota`
      says which item) and `file_too_large`. An "upgrade plan" action is reserved
      for future billing (`upgrade_plan` is reserved in `actions` in the response
      and never appears now).
- **Partial sync is automatic** (decided by the owner, 2026-11-23):
  - Remove the 3.1 `cache.partial`; there is no on, off or force switch. The first
    time a remote host is connected (for self-hosted, by host and port), the
    server detects whether it supports fetching by condition (the Git protocol v2
    `filter`) and records it in the state database. If it is supported, a partial
    clone is used, and if not, a full clone. When the remote rejects a partial
    fetch, detection is redone, and it is also rechecked every 30 days. With a
    server Git older than 2.44, a full clone is always used.
  - Pinned spaces are still full.
  - The "Storage & sync" page shows "Partial sync supported: yes / no" per
    platform and host.
  - The only policy, "Remotes that cannot do partial sync: allow (full clone) /
    refuse", is in 3.6 (layered, default allow).
  - Bodies always stay local (decided earlier by the owner).

Decided again by the owner on 2026-11-23 (following the three above):

4. Archive is a level lower than eviction: only a row in the state database and
   small metadata are kept, and the index is deleted too. Only spaces that follow
   a remote can be archived. Restore means fetching the body and rebuilding the
   index. On sign-in, restore in the background in the order personal, then
   favourites, then recently used, then the rest, with a concurrency limit. Very
   old ones are restored in steps. Administrators can turn prefetch off, or
   prefetch only for recently active people. While archived, a space cannot be
   searched, gives no daily text, and shows no backlinks, and the UI says
   "Restoring, about N seconds". Favourites, members and permissions are not
   affected.
5. Server-level storage pressure is shown only to site administrators. When
   critical, ordinary people and tenant administrators get only the neutral
   `503 temporarily_unavailable` and the text above, and quotas and file size are
   still stated clearly.
6. One storage pressure controller replaces the independent cleanup thresholds:
   four levels (70 / 85 / 95%) and 10 percentage points of hysteresis, least
   impact on people reclaimed first and ranked by score within a step, hard rules
   and the emergency reserve, fairness per tenant, visible reclaims and waits,
   and automatic slowing when waits grow. The administrator sets only the site
   storage budget, and the rest is in the hidden "Advanced". A deterministic
   simulator is built in 3.5 to tune the defaults (see below).
7. Partial sync is detected automatically and recorded per remote host. There is
   no switch. Pinned spaces are full. Bodies stay local.

**Simulator** (an implementation requirement of 3.5): a deterministic simulator
(fixed random seed; synthetic people, spaces, and patterns of sign-in and
opening, with the size distribution following the 3.1 estimate) that drives the
real controller code (with a fake clock and a fake disk), used to tune the
defaults. Its tests assert: apart from the emergency reserve, used never exceeds
the budget; prefetch and background jobs never use the reserve; under the
reference load, the balanced preset has a restore wait p95 below 30 seconds and a
share of opens needing restore below 5%. These tests run in plain `go test` and
finish in a few seconds.

**3.5 as built (2026-11-24, parts 1 to 4, backend only)**:

- **Part 1, single-file size `files.maxBytes`**: done for the write entry points
  listed in the contract: upload, each page and attachment in an import, `PUT` /
  `POST` of pages, saving and committing drafts, MCP `page_create` and
  `page_update`. Commits to the attachment repository come only from uploads and
  imports, so they follow them. The check is on the size of the page file "as it
  would be written" (header plus body), and the error is `413 file_too_large` (in
  MCP it is a `code` of the same name, with the details in `details`). The site
  value is `--max-upload`, read-only. A tenant value is also capped by it: after a
  site administrator relaxes `lower`, the effective value is still no more than
  `--max-upload` (cut once more when the setting is read).
  - Not written in the contract, decided by the safest reading: **a page that is
    already over the limit may be changed back to a size that is not larger**
    (after the limit is lowered, such a page can still be read and made smaller;
    only writes that create a new one or make it larger are refused). Otherwise
    lowering the limit would stop people from editing their own old oversized
    pages. A draft is checked by the same rule when it is put in, and again when
    it is committed (the limit may have been lowered in between). An import is
    checked as a whole first, the first page or attachment over the limit is
    named, and no file is written.
  - The 4.2 offline replay does not exist yet. It will go through the same page
    write.
  - Added after review (2026-11-23): writes of templates and types are also checked
    against `files.maxBytes` (the same rule as pages). The background commit of
    drafts (the sweeper, where no request carries a limit) is checked against the
    limit of the tenant of the draft's owner. Anything over the limit is not
    written, and the draft is kept and marked held, so nothing is lost.

- **Part 2, recycle bin, deleting spaces, administrator view and export**:
  - Deleting a space stores all its records (the space row, members, groups,
    records of remotes and attachment remotes, favourites, drafts, daily-text
    settings, folder-move records) as one JSON document in
    `recycle_items.records`, and deletes them from the live tables in the same
    transaction. The directories are moved to `DATA/recycle/<item>/space` and
    `/cache`. The steps and what to do after a crash are written at the top of
    `internal/spaces/recycle.go`: after the transaction commits, the item is
    `moving`, and only when the directories are moved is it `ready`. On startup,
    `moving` ones continue moving, `restoring` ones are moved back, and `purging`
    ones continue purging. The hourly job also retries what could not be moved.
    Restore and purge touch only `ready` items, so the two never run at the same
    time. Restore puts the records back with `INSERT … SELECT`, linked by tenant
    to people and groups (people and groups that do not exist or are deleted are
    listed in `skipped`). If there is no owner, an `owner` is required (same
    tenant, exists, not deleted, not deactivated). Restore is never refused
    because of quota.
  - Migration `0020_recycle.sql` holds `recycle_items`, `exports`,
    `users.deleted_at` and `users.recycle_item` (the columns of part 4 are added
    to the same file later). A tenant cannot be deleted while it still has items in
    the recycle bin (`tenant_not_empty` gets an extra `recycle`), so the tenant is
    always there on restore.
  - **Not written in the contract, decided by the safest reading**:
    1. The contract says "in 1.7 an administrator cannot read other people's
       pages through the API", but in the code an administrator (session) who
       opens a space they are not a member of gets all permissions, and can read
       and write. That makes the "view" meaningless, so it was changed: an
       administrator who is not a member has only `members` and `manage` (manage
       members, remotes, settings, rename, transfer). Reading first needs a view
       with a stated reason, and a view adds only `read` and can never write. The
       two endpoints that list members (they need `read`) remain open to
       administrators, because managing members needs them.
    2. The contract says "from 3.7 organization administrators cannot see personal
       spaces", while the task said "personal spaces are never shown to
       organization administrators". The latter was followed: for a tenant
       administrator, viewing, exporting and opening a personal space always
       answer "does not exist". A site administrator can still see them (each time
       is audited).
    3. A personal space cannot be restored alone (`409 conflict`). It must come
       with its person (part 3), because restoring it alone would give a
       "personal" space belonging to a deleted person.
    4. The 3.1 grace-period deadline is not a stored value. It is computed from the
       creation time and the current policy, so a restored space naturally is
       "recomputed under the current policy" and need not be stored separately.
    5. In the preview, `attachments.inSpace` has no stored number and answers `null`
       (the preview reads only stored values).
    6. The export's "notify the owner of the space" is not done: there is no
       in-account notification channel to put it in right now. The audit row
       records the export and its space. The files of the attachment repository are
       fetched into the zip one by one, and any that cannot be fetched are written
       to `NOT-INCLUDED.txt` and `warnings`, so the export as a whole does not
       fail. A space in the recycle bin that was already "evicted" at deletion has
       no files to export, and the export has only the drafts and the remote
       address. Objects missing from a partial clone are not fetched from the
       remote automatically (Git's lazy fetch is off), and the export fails with
       an explanation.
  - The confirmation token (`POST /api/admin/confirm`) is done in this part for
    `delete_spaces`, `purge`, `restore` and `export`. The other actions and the
    bulk jobs are in part 3.

- **Part 3, deleting people, confirmation tokens and bulk jobs**:
  - Deleting a person follows the contract's order: first deactivate (through the
    same entry as `PATCH /api/users/{id}`, so "the last administrator" and
    "yourself" are still refused), then transfer, leave, recycle. The last
    transaction does the person's recycle item, marks the row, takes out the group
    memberships, and attaches the spaces recycled with him under the item. A
    failure halfway leaves "deactivated, fewer spaces", never a space without an
    owner. Requesting again continues, using the mark `parent = u:<user id>` on the
    space items to recognize the spaces recycled earlier. The preview and the
    execution both compute from records, with a constant number of queries, no
    matter how many spaces he has.
  - **Not written in the contract, decided by the safest reading**:
    1. After a transfer he is an editor in that space, so deleting the person must
       also remove this membership (recorded in `left`). Otherwise a deleted
       person would stay in a live space.
    2. A personal space in principle is restored with the person. But if the person
       is already there (deletion stopped halfway, or restore stopped halfway), the
       personal space can be restored on its own back to him. Otherwise such a
       personal space would be stuck in the recycle bin forever.
    3. When a person is restored, a space "that went to the recycle bin with him"
       that cannot be restored (for example a space with the same id has appeared
       again) stays in the recycle bin as an independent item and is listed in
       `skipped`, so the whole restore does not fail.
    4. The person's item records "whether he was already deactivated before". After
       review, this was changed: before step 1, "whether he was already
       deactivated before" is recorded on his row (`users.delete_pending`), and
       every transfer and every leave is recorded in it afterward, so when
       requesting again after a failure, what is recorded is the state at the first
       request and all the transfers. On restore he is enabled as he was and all
       transfers are listed. After the second round of review: an administrator
       re-enabling the person clears this record. If the person was not deactivated
       when the deletion started, an old record is also voided and re-recorded, so
       later deletions do not reuse an old "already deactivated".
    5. `users.reuseUsernames` governs invitations (with a username), registration,
       accepting an invitation, and `autoCreate`. People who sign in by username
       cannot find a deleted person, and his username is held by the row itself
       (the unique key).
    6. The tenant's number of people (`usage.people`) is decremented on deleting a
       person and incremented on restore, and the daily reconciliation does not
       count deleted people.
    7. In the count that blocks tenant deletion: rows of deleted people
       (tombstones) do not count, and items in the recycle bin count only if there
       are any (`recycle`).
    8. In bulk operations, `evict`, `pin` and `unpin` are for site administrators
       only, and a tenant administrator gets `403` on every item. `export` is one
       export for the whole set, not one per item. The bulk token is always
       required (the contract says "actions on more than one item and any purge"
       need a token, but the bulk endpoint accepts only the token and has no other
       way to receive ids, so it is always required).
    9. A job does not stop when the request ends. The job row is stored in
       `bulk_jobs`, and after a restart an unfinished job reads as `interrupted`.
  - Isolation table: `GET /api/admin/users/{user}/deletion`,
    `DELETE /api/users/{id}` and `GET /api/admin/jobs/{id}` get one entry each
    (B's id is treated as not existing). `POST /api/admin/bulk` is marked exempt,
    because it accepts only the token, and the ids in the token were already
    checked against the caller's scope when the token was requested (B's id gives
    `404` there). One entry was added to the store's scope test: every function
    that reads `users` must carry `deleted_at`, and one that does not must have a
    reason in the list.

- **Part 4, storage management lists and totals** (2026-11-24):
  - **Usage counted by "where it lives", done**: each space stores a `location`:
    `server` (only on the server, counted in full), `own` (the remote is the
    person's own; only shown and measured, not counted toward quota), `tenant`, and
    `site` (3.6; `site` counts, `tenant` does not). `usage.bytes`, tenant totals
    and all quotas count only `server` and `site`. Spaces that are `own` or
    `tenant` are still measured and still shown, and their partial clones, indexes
    and attachment cache on the server count as `held_bytes`, belong to the storage
    pressure controller, and do not enter quota. The size of a remote repository is
    never measured by downloading. **A seam left for 3.6**: `locationOf` (where a
    space lives, in one place) and `LocationCounts` (whether this location
    counts). When 3.6 adds storage modes, change the former, and for spaces at
    `site` take the size from the provider's API; nothing else needs to change.
    When a remote comes or goes, the bytes counted for the space change with it
    (after a remote is removed, they are counted again from the last measurement).
  - Columns, deltas and reconciliation: `spaces` gets `location`, `remote_via` and
    `remote_state`, `users` gets `last_active` and `first_write`, and `usage` gets
    the columns listed in the contract, plus `partial_spaces` (the summary needs to
    split full and partial) and copies of `created` and `last_active` (the list of
    people goes through a single index). Every change of a space (measurement,
    index size, cache state, pinning, a remote coming or going and its state,
    changing owner, deletion, restore) first locks the space in the same
    transaction, and then adds the delta to two rows: the tenant and "the person it
    is counted under". `dormant` is recomputed when the number of remotes, the
    connections, or the first write changes, and the tenant's number follows. The
    daily reconciliation corrects everything in one pass (and also corrects
    columns on the space such as `location`).
  - **Not written in the contract, decided by the safest reading**:
    1. `grace_spaces` and `overdue_spaces` cannot be maintained by deltas: a grace
       period turns into overdue as time passes, and the policy can change, so
       neither is a single write. Instead, one grouped query (scanning only spaces
       that lack a remote) sets them: once an hour, when the policy changes, and in
       the daily reconciliation. So they may lag by up to one hour. The `deadline`
       filter of the list is computed at request time from the policy and
       `created`, so it does not lag.
    2. The list cursor is opaque and carries the sort and direction
       (`held.desc:<value>:<id>`); a cursor from another sort answers `400`. Those
       with no sort value (never measured, never used) are placed last, ordered by
       id, as the second segment of the same index. Spaces with equal values go by
       id, and the direction follows the index.
    3. `remote=error` also includes "history was rewritten". `exempt=1` means only
       the space's own exemption, not the owner's. `cache=archived` was added (it
       will be written only in part 5, and the reconciliation already recognizes it).
    4. `last_active` is written when a session, an API token, or an OAuth grant is
       used (once per person per hour, decided in memory). `first_write` is
       recorded at "the author", the one place (every write goes through it), so it
       is recorded before the first write, even if that write is rejected later.
    5. When a tenant administrator looks at the list of people and a person's
       entry, **the spaces never include personal spaces** (`AdminScopeOf`). But
       the person's row (number of spaces, bytes, held) is the row that the quota
       reads, and it includes personal spaces and is not split. Splitting would
       need another set of counters, which is left for 3.7 (the organization's share
       and the person's home are separated). The totals a tenant administrator sees
       may be one personal space more than the spaces listed.
    6. `affected` of `GET /api/admin/storage`: for a site administrator it is still
       the 3.1 aggregate. For a tenant administrator it is the stored grace and
       overdue counts (the exempt count is site-wide and not told to him). `PUT`
       carries no summary.
    7. The migration has to be tested on old databases built in order from the
       three branches. 0020 does not touch the 0013 columns, so in the seed of
       `remote_via` every connection is recorded as `github` (the only provider
       then), and the reconciliation corrects it to the real provider. The three
       "merge order" tests treat 0020 as the last one (which it is anyway).
  - Indexes and EXPLAIN: each sort has one index on `spaces` with the tenant and
    one without (the site administrator has no tenant), and likewise for each sort
    of people on `usage`, plus indexes for `dormant`, people with a grace period,
    pinned spaces, exempt spaces and so on.
    `internal/store/storage_explain_test.go`, on a database of 16,000 rows, asks
    both SQLite and PostgreSQL for the execution plan of every list, sort and main
    filter, and fails if a full table scan or a sort of the found rows appears (on
    PostgreSQL, sequential scans and sorts are first marked expensive, to prove
    that the index gives the order and the range). The cursor condition is written
    as "column >= value, and (column > value or id comparison)", not as one OR,
    otherwise SQLite would take two ranges and then sort. **Only `q` (name
    contains) has no index**: it reads names in sort order until the page is full.

- **Corrections after review (2026-11-24, part 4 and the UI)**:
  - `connected` (and `dormant`) counts a connection of any provider:
    `github_authorizations` or `provider_authorizations`. The live check and the
    daily reconciliation both look at the two tables. Connecting a 3.8 account,
    disconnecting, and deleting a provider (with its connections) all recompute
    that person's `connected`. A person who connected only GitLab or Codeberg is no
    longer treated as dormant.
  - When a tenant administrator restores a person, the answer does not include
    personal spaces (neither `skipped` nor `restored.spaces` carries its id and
    name), and only gives a count in `restored.personal`. The audit row is still
    complete.
  - `GET /api/admin/storage`: `hosts.fromFlag`, `cache.minFreeBytes`,
    `git.version` and `spaces.restoring` in the summary (server-wide) are shown
    only to site administrators.
  - The preview of deleting a person, `revoke.connections`, lists all connections
    by provider (including 3.8). Deleting a person also disconnects 3.8
    connections (those that can be revoked are revoked at the other side), and
    purging a person deletes `provider_authorizations` and the reminder rows.
  - The cursor of the space list sorted by name uses the stored `name_key`. The
    reconciliation restores the counted bytes of a space that has returned to the
    server from the last measurement. `AssetBytesOfTenant` finds the tenants of all
    cached spaces in the cold table in one query.
  - UI: in the bulk dialog, replacing or clearing the heir drops the old token and
    fetches the plan again (an answer that arrives early does not overwrite a later
    question). The banner "Administrator view, read only, recorded" is shown only
    when a view is really open.

- **Corrections after review (2026-11-23, parts 1 to 3)**:
  - Export: an export made by a site administrator is shown only to site
    administrators (list, read, download, delete), whatever its tenant field. An
    export that includes history, includes personal spaces, or includes spaces of
    several tenants is likewise only for the site (`exports.site_only`). An export
    made by a tenant administrator goes, as in the contract, to that tenant's
    administrators and the site.
  - Personal spaces: the tenant administrator's management scope
    (`store.AdminScopeOf`) excludes personal spaces in every query by space and by
    recycle item. So deactivating, enabling, measuring, transferring, deleting,
    the deletion preview, viewing, restoring, confirming and exporting a personal
    space always answer "does not exist", and lists (spaces, recycle bin, and
    `with` in a person's item) do not list them. A table was added next to the
    isolation table: every admin route that carries a space or a recycle item (and
    every space route) is requested once with a personal space and once with a
    made-up id, and the answers must be the same. Deleting and restoring a person
    still use `ScopeOf`, and personal spaces go with the person.
  - Recycle bin: if `recycle.days` cannot be read, this round does not purge (it
    does not purge by the default). Every action holds its item inside the process,
    and the hourly check takes over only items left by a crash or a failure, never
    a restore or purge that is still running. At the end of a restore, deleting the
    item must delete exactly one row, otherwise it rolls back and the directory is
    put back. After the hourly check gets an item, it rereads it once by id, and if
    the state differs from the list (or it is already `ready`, or gone), it is not
    touched, so a restore that completed meanwhile is not moved back to the
    recycle bin. A move across file systems became a copy (fsync each file and keep
    permissions, write to a `.partial` beside the target, rename when complete,
    sync `DATA/recycle`) and then deleting the source. Before deleting, the source
    is first renamed to `.moved` in its own file system and the parent directory is
    synced, so when "both sides exist" both sides are complete, a `.moved` is never
    treated as a complete copy, and on restore it is simply deleted. Retrying after
    an interruption does not get stuck in `moving` and does not leave a broken
    space. Before deleting a space, the 3.1 restore currently in progress for that
    space is stopped and waited for, and the 4.1 read-only index handle is closed.
    Early purge (`DELETE /api/admin/recycle/{item}`) became background work, and
    answers `202` and the item (the contract says `204`; this is noted in the As
    built note of api.md), and the audit is written only after the purge finishes.
  - Deleting people: purging a person's item purges only the item's own spaces (it
    does not touch spaces under the mark `u:<id>` that belong to a deletion in
    progress), and purges his data only if `users.recycle_item` is exactly this
    item, so purging a stale item (a restore half done) does not touch a person who
    has already come back or is held by a later deletion. Restoring a person's item
    also no longer claims the spaces under the mark. A deactivated or deleted
    person cannot be added to a space (the statement itself refuses). A deleted
    person does not count as "another owner". The last step also removes
    memberships that slipped in after the plan (listed in `left`), and if removing
    them would leave the space without an owner, this step refuses (`409 conflict`),
    and requesting again brings that space into the plan. The confirmation hash of
    `delete_users` covers what the preview decided: what is recycled, whom each
    transfer goes to, what is left and with which roles, `needsHeir`, and the
    blocking items.
  - Bulk jobs: a panic in a job is caught and the job is marked `failed` (listing
    the items at that moment), and the process is not affected. If progress cannot
    be written, it is retried, and a job that finally cannot write ends as
    `failed`. Every item is done with the initiator's permissions at that moment
    (someone who is no longer an administrator no longer executes). Jobs are queued
    by the tenant they operate on (a site administrator's job on B takes B's slot),
    and the job row is recorded in that tenant. A job started by a site
    administrator is shown only to the site (`bulk_jobs.site_only`).
  - **Questions left for the 3.6 permission keys** (not changed this time): (1) the
    "view" can be bypassed: an administrator who is not a member has `members` and
    `manage`, and can add himself as an owner and then read, or change the remote
    to push the content to his own repository; (2) an administrator with `manage`
    can write templates (templates are files in the space). Both wait until 3.6
    splits the administrator's permissions into permission keys.
