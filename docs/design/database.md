# Database

## 5. Database

### 5.1 State database (SQLite or PostgreSQL)

Users, external identities (provider plus subject), passwords and passkeys,
groups and members, spaces and members, sessions, API tokens, OAuth grants,
remote credentials (encrypted), drafts, invitations.

- SQLite uses a pure Go driver, to keep a single executable.
- Both databases run tests in CI from the first phase, to avoid patching in
  compatibility afterwards.
- The benefit of connecting PostgreSQL is managed backups and ready-made
  operations, not horizontal scaling.

### 5.2 Index database (always a local SQLite)

Page metadata, full-text index, tags, link relations. It can be deleted at any
time and rebuilt from the files.

- Chinese full-text search uses "per-character splitting": before storing, a
  space is added on both sides of every Chinese character, so that SQLite's
  built-in tokenizer treats each character as one word, and at query time the
  query is turned into a phrase. A trigram tokenizer cannot find two-character
  words, and was ruled out.
- Global search covers all spaces visible to the current user, and the results
  are filtered by space permissions.
- Lists, the folder tree, tags and search all query the index; they do not walk
  the disk or read files one by one.
- The index records the commit it corresponds to. After every fetch
  fast-forward, it compares which files changed between the old and the new
  commit, and rebuilds the index only for those pages: added ones are added,
  deleted ones are removed, and renamed ones are recognized as the same page by
  their `id`. When someone else edits directly on GitHub, this is the path it
  takes, with a delay of at most one fetch cycle (60 seconds by default); when a
  webhook is configured, it can be triggered immediately.
- A full rebuild happens only when the index is missing, the version does not
  match, or the recorded commit is no longer in the history.
- Redis is not introduced: the service is a single instance, sessions are in the
  state database, and hot data can simply go into an in-process cache.
- Measured (50,000 pages, 62.2 million characters, see `spikes/search-scale/`):
  folder and tag lists 0.1 to 15 ms; searching a rare word 0.2 ms, searching a
  common word that hits over ten thousand pages about 200 ms; updating the index
  after saving one page 0.7 ms; full rebuild 29 seconds; the index size is 87% of
  the text.
- Known to be improved: per-character splitting matches across punctuation, so
  punctuation has to be replaced by a placeholder word when storing.

### 5.3 Database baseline (2026-11-24)

After 3.5 was merged, at the owner's request (see
[decisions-2026-11-24.md §17](decisions-2026-11-24.md#17-owner-decisions-of-2026-11-24-collected-placed-by-milestone), "Database"), the state
database got one overall review: measured on a constructed database (200
tenants, 30,000 users, 60,000 spaces, 1 million audit records, one index
database of 50,000 pages), and the query plans of both databases were checked.
This section records what was changed, why, and the numbers measured. **Both the
development database and the demo database must be deleted and rebuilt**: an old
database is rejected at startup and is not converted.

- **One baseline, no migrations.** 0001 to 0020 were merged into one file,
  `internal/store/schema.sql`, shared by SQLite and PostgreSQL; a statement that
  runs on only one kind of database is preceded by a line
  `-- dialect: postgres` (or `sqlite`). `tenant_id` is now a real foreign key
  (users, spaces, groups, invitations, sign-ups, sign-in providers, usage, GitHub
  installation sharing, trash, exports, bulk jobs); the audit log's `tenant_id`
  is deliberately not a foreign key, because the records outlive the tenant.
  When a tenant is deleted: if anything is still in the trash, it is refused (the
  API document already said this, and the code had missed it); the tombstones of
  purged people, the exports and the bulk jobs are moved to the default tenant,
  and the exports and jobs become site-admin ones (`site_only`), expiring and
  being cleaned up as usual. The single-row `settings` table is merged into
  `site_settings` (key `first_user`). The Go steps of 7 and 18, the "turn off
  foreign keys and rebuild the table" machinery, `tenants_migrate.go` and the
  tests that simulate upgrading an old database were all deleted; the default
  tenant is created together with the database.
- **Text columns are all `COLLATE "C"`.** Before, PostgreSQL ordering and cursor
  comparison relied on adding `COLLATE "C"` temporarily in each query
  (`bytewise()`), and the index on the column itself used the default collation,
  so it could not be used: paging by username over 30,000 users took 14-18 ms (a
  full table scan and then a sort), and after the column was declared
  `COLLATE "C"` it took 0.04 ms; listing spaces ordered by `name_key` in an
  en_US database also gave a different order from SQLite. Now every TEXT column
  is declared `COLLATE "C"` (written as `COLLATE BINARY` on SQLite, which is its
  default), both sides sort by bytes and cursors mean the same, and `bytewise()`
  was deleted. At startup it reads the locale from `pg_database`: if it is not C,
  C.UTF-8 or a builtin provider, it warns in the log (it does not refuse, since
  the columns are already C); the README gives the create-database statement
  `CREATE DATABASE … LOCALE_PROVIDER builtin BUILTIN_LOCALE 'C.UTF-8'`.
- **Finding people by email uses an index.** `UsersByEmail` (used by sign-in,
  password reset, and SSO merging by email) used to lowercase the address of
  every person in the tenant and then compare, taking 88 ms for 30,000 people.
  Now `users` has `email_key` (Go's `EmailKey`; for a non-ASCII address it is the
  empty string and matches nobody) and `name_key` (`FoldName`), and each write of
  a name or address writes the key in the same statement (a test reads the SQL of
  the whole package to check this); the lookup uses the partial index
  `users_email (tenant_id, email_key) WHERE email_key <> ''`. Searching for
  people (`SearchUsers`, the people list on the storage page, group members,
  people visible to a space) folds the query term in Go and then compares it with
  `username` and `name_key`, and no longer uses PostgreSQL's `LOWER()` (in a C
  locale it does not fold non-ASCII), so `STRASSE` finds `Straße`, and both
  databases give the same result; the API document got a sentence to match.
- **Index additions and removals.** Added the child-table indexes needed by the
  cascade when a person is deleted: `drafts_user`, `invites_created_by`,
  `provider_leftovers_user`. Removed the duplicates: `spaces_last_used`
  (`spaces_used` already covers it), `spaces_usage_user` (a prefix of
  `spaces_owner`), `users_tenant` (`UsersWithQuota` with `deleted_at IS NULL`
  uses `users_live`; the tenant conditions of the token and grant lists were
  changed to look up the user row by primary key, row by row);
  `asset_remotes_move` was made a partial index. The 12 list indexes of the usage
  table include only people's rows (`WHERE kind = 'user'`): the per-tenant ones
  dropped the leading `kind`; the cross-tenant ones keep `kind` first, because
  when there is no equality condition on the leading column, SQLite's planner
  costs a partial index like a full table scan, and prefers to use the primary key
  and then sort.
- **PostgreSQL fillfactor.** `usage`, `spaces`, `users`, `sessions` and `remotes`
  are set to `fillfactor = 85`: an update that touches no indexed column can be
  a HOT update. Measured: at the default 100, 5000 such updates gave 0 HOT; at 80,
  4891.
- **Lock the row, do not write the row.** `LockSpace`, `LockGroup` and
  `LockFavorites` used `UPDATE … SET id = id`: on SQLite each one took 262 µs
  (rewriting 16 index entries, checking foreign keys), and on PostgreSQL each one
  left a dead tuple; 5000 times took 689 ms versus 65 ms. Now PostgreSQL uses
  `SELECT … FOR NO KEY UPDATE` (a lock at the same level as that UPDATE: the
  mutual exclusion is unchanged, and it does not block the foreign key checks
  that reference the row, which `FOR UPDATE` would), and SQLite only checks that
  the row exists (a write transaction has held the only write lock since it
  began). With one transaction each locking 5000 times: SQLite 176 µs → 23 µs per
  lock, PostgreSQL 156 µs → 136 µs (mostly the round trip), and no more dead
  tuples.
- **The background measurement queue is its own table.** Measurement walks every
  space again and again, and used to write the indexed `spaces.size_at` every
  time, and also stamp the usage rows of the owner and the tenant, even when
  nothing had changed: on PostgreSQL 10,000 updates gave 0 HOT, and the index on
  the spaces table grew from 57 MB to 62 MB. Now the queue is
  `space_measure (space_id, tenant_id, size_at)`, with the index
  `(size_at, space_id)` for "who to measure next" and `(tenant_id, size_at)` for
  "the tenant's latest measurement"; `spaces.size_at` and its index were deleted.
  `spaces` and the usage rows are written only when the byte count or page count
  changed; a person's row is stamped when it changes, and also once the first time
  his space is measured; the tenant's row is no longer stamped, and `measuredAt`
  takes the latest time of its spaces in the queue (one index lookup). Every path
  that creates a space enqueues it, a restored space is enqueued again, and the
  daily recount fills in any that were missed. On 2000 spaces with 5000 "nothing
  changed" measurements: PostgreSQL 636 µs → 422 µs each, the spaces table index
  1.68 MB → 2.04 MB becomes no growth, and the new versions of space and usage
  rows go from 10,600 to 0; SQLite 389 µs → 222 µs each.
- **Audit log is deleted in batches by id range.** The id is a 12-digit hex
  millisecond time plus a sequence number, so "older than some time" is a range
  of the primary key: `audit_log_at` was deleted, and `TrimAudit` deletes 5000
  rows per batch, one statement per batch (each is its own transaction when not
  inside a transaction). Before, one statement deleting 676,000 rows took 8.2 s
  and held SQLite's only write connection the whole time; now 270,000 rows take
  1.8 s on SQLite (54 batches, about 33 ms each) and 0.66 s on PostgreSQL. Added
  `audit_log_tenant_kind (tenant_id, kind, id)`; before, filtering a tenant's
  audit by kind had to read the tenant's whole log (414 ms at 1 million rows).
- **Connections.** PostgreSQL `SetMaxIdleConns(20)` (Go keeps only 2 idle by
  default, so after a burst of requests the connections just opened are closed,
  and reopened for the next burst). SQLite gets a read-only pool of 4
  connections (`query_only`): a SELECT on `Store.Q()` that is not inside a
  transaction goes to it, and all other statements (including PRAGMA and those
  with RETURNING) go to the only write connection. The rule (written in the
  comment of `Store.Q`): under WAL, a read sees all writes committed before it
  began, so writing on `Q` and then reading on `Q` reads your own write; a read
  that must be in the same state as a certain write (read, decide, write; or no
  other write allowed between the write and the read) goes into the same `Tx`,
  which is on the write connection, and the reads are inside it; inside a `Tx`,
  use only the `Q` it gives. Before, SQLite had only one connection, and using the
  outer `Q` inside a `Tx` would hang, so no existing code does this.
- **`bulk_jobs` periodic cleanup.** It was never deleted before. A daily task
  deletes jobs that ended (done, failed, interrupted) more than 30 days ago,
  using the new index `bulk_jobs_updated`; queued and running ones are never
  deleted.
- **Page cache of the index database.** Each connection had a 64 MB page cache,
  an index has 15 connections, and up to 64 spaces can be open at once, so the
  worst case was about 61 GB. Now the write connection keeps 64 MB, each read
  connection gets 8 MB, and files are read through a 256 MB memory map
  (`mmap_size`, supported by modernc's SQLite, with an upper limit of 2 GB); the
  shared pages are stored only once, in the operating system cache; the worst
  case of private cache is 176 MB per index, and about 11 GB for 64. The cost:
  when an I/O error happens while reading a mapped file, the process receives
  SIGBUS; the index database is on local disk, so this is acceptable.
  (Superseded on 2026-11-24 by 5.5: the writer keeps 8 MB, 64 MB only during a big sync.)
- **The time index of `session_ends`** (the table added in #39) already has
  `session_ends_ended`, and the daily cleanup `DELETE … WHERE ended < ?` uses it;
  a test confirms this with the query plan.
- **Tests**: every behavior change has a test; each performance item has an
  EXPLAIN test, run on both databases, which fails if it regresses: finding people
  by email, byte-order paging on PostgreSQL, audit by tenant and kind, the
  measurement queue, the child-table indexes used by cascades, `session_ends`
  cleanup, bulk job cleanup (`internal/store/db_explain_test.go` and others).

Done together with their own features (3.6 and later): the audit categories and
the `activity_log` for high-frequency events; `set_by`, `site_lock` and the
`(layer, key)` index of `layered_settings`; the `tenant_members` sort index of
3.7, and the list index for personal spaces not entering the default tenant; the
notification tables (3.10 part four: `notifications` and `notification_seen`)
and the custom roles table; and 3.10 paginating `/admin/users`.

### 5.4 Rule for schema changes: no migrations before the official release

A rule the owner set on 2026-11-24.

- **Before the official release, not one line of migration code is written.**
  The schema has a single baseline, `internal/store/schema.sql`; a change is made
  directly in it, with no numbered incremental files and no Go upgrade steps.
  When the database is created, the hash of the baseline statements (without
  comments) is recorded in the single row of `schema_baseline`; at startup, if the
  hash in the database differs from this code's (or the database was made by an
  old version that still used numbered migrations, and has a `schema_migrations`
  table), startup is refused with the message "this database was made by an
  earlier development version, please recreate it", and **conversion is never
  attempted**. Tests that simulate old databases or simulate upgrades are never
  wanted.
- **After the official release, upgrade code is kept apart from the schema.** The
  place is already set aside: `internal/store/upgrade/` (an empty package for
  now, with only a comment explaining the rule). The upgrade between each pair of
  releases goes in one file each there; the baseline always describes a fresh
  database of the current version, and an upgrade step is only responsible for
  bringing an old database up to it.
- **Other upgrade and compatibility code is the same** (the owner, 2026-11-24:
  「其他升级代码也是，要能在合适的时候丢掉」 [Other upgrade code too; it must be
  possible to drop it at the right time]). Before release, when our own data,
  settings, interfaces and client storage change format, we change them directly
  and do not read the old format. What was deleted under this rule: the upgrade of
  the daily text settings and `notices`, the `hosts` that `PUT /api/admin/daily`
  ignored, rows stored by old code, reading a single string as a list (the default
  `apis` and `sources`), the space selection stored as a plain id list; the state
  that was kept in `.petalbook/cache` before 2.2 and the moving of
  `.petalbook/tmp`, the import of drafts from the old state database; on the web
  side the old addresses of 1.1, the old settings addresses, the way of reading
  local drafts that lack `source`, the plain numbers in the home page records, the
  version upgrade of the offline database, and the accommodation of old servers.
  After release, compatibility code goes behind a marked boundary, and is deleted
  after one major version.
- **Only upgrading from the previous major version is supported.** When a new
  major version is entered, the new baseline already contains everything, and the
  upgrade steps of the previous major version can be deleted entirely.

### 5.5 Memory budget of open spaces and the rebuild limit (2026-11-24)

The open-space cache used to count spaces (at most 64 open), whatever they cost, and every index kept a
64 MB writer cache and up to 14 read connections. Nothing bounded rebuilds: after a schema change or a crash
every large space someone opened rebuilt at once. The owner approved a redesign in two parts.

**What an open space costs** (measured on a generated 50 000-page space, index file 92 MB, and a 200-page one;
anonymous memory of a fresh process after a GC that gives the Go heap back, so SQLite's caches; the memory map
of the index is file-backed and is not counted; `internal/space/measure_memory_test.go`):

| | before | after |
|---|---|---|
| open an indexed 50 000-page space | +54 MB | +12 MB |
| the same after 16 parallel searches | +59 MB | +15 MB |
| the same after 40 s idle | +59 MB | +15 MB (read connections closed: about 0.5 MB each) |
| open an indexed 200-page space | | +1.3 MB (+4.8 MB after 16 parallel searches) |
| a full rebuild of 50 000 pages | 18–36 s, 1.7–2.2 cores, peak +150 MB | 27 s, 1.7 cores, peak +150 MB |
| one search at a time (50 000 pages, common words) | p50 59–79 ms | p50 24–34 ms |
| one folder listing at a time | p50 2.3–5 ms | p50 1.9–2.6 ms |
| 16 searches at a time in one space, 400 in all | 5.2–7.2 s | 6.2–10.5 s |

The timings are from a loaded 8-core development machine and vary by ±50 % between runs; the memory numbers are
stable. A rebuild leaves about 75 MB that SQLite's allocator (modernc's libc) keeps for the process after the
space is closed: it is reused by later work, not lost, and it is one reason rebuilds are bounded (below).

**Per index** (`internal/index`):

- The writer's page cache is 8 MB. A *big sync* — a rebuild (`Sync` with `full`), or a sync that reads more than
  1000 files (the first open of a large space with no index, a pull that changed most of a space) — raises it to
  64 MB with `PRAGMA cache_size` for its duration, and lowers it after (`cache_size` back to 8 MB, then
  `PRAGMA shrink_memory`). Every sync scans the `pages` table (`loadKnown`), which is what filled the 64 MB
  cache of an idle space before.
- 4 read connections and 2 aux connections (were 8 and 6), closed after 30 s unused (`SetConnMaxIdleTime`).
  Readers keep an 8 MB cache and map the file (`mmap_size` 256 MB); with the map, their caches stay nearly
  empty, so a reader costs about 0.5 MB. One search at a time is as fast as before or faster; 16 concurrent
  searches in one space lose up to a third of their throughput on 8 cores, and about nothing on the 4-core target,
  where 4 readers already keep the cores busy.
- `MemoryCost()` estimates what the index holds without a query: 0.5 MB for the writer's connection, its cache
  as configured now (64 MB during a big sync) capped at the size of the file, 0.5 MB per open read connection,
  and 64 bytes per page. `space.MemoryCost()` adds 1 MB for the rest of the space. On the 50 000-page space this
  says 12.7 MB idle and 15.7 MB with every connection open; measured 12 and 15. The memory map is not counted:
  it is file-backed, shared with the operating system's cache, and given back under pressure.

**The budget** (`internal/spaces`):

- Each open space carries its estimated cost, refreshed at every trim outside the manager's lock (a file stat
  and two pool counters per open space: microseconds for hundreds of spaces).
- While the open spaces together cost more than `--open-spaces-memory` (default 1024 MiB), or more than
  `--max-open-spaces` (default 256, now only a safety limit for open files) are open, the least recently used
  *idle* spaces are closed. A space with a request in flight is never closed, however much it costs; the
  budget can be exceeded by spaces in use, and is enforced again as they are released.
- A trim runs in the background after every open (`trimSoon`) and closes only over the budget or the cap; the
  minute trim also closes spaces idle for `--space-idle`. An open during a background trim makes it run once
  more, so a new space's cost is always counted.
- Locks: a trim takes `trimMu`, then `mu` only to list, pick and mark spaces; costs are computed, spaces
  flushed and closed, without `mu`. Opening (and so a big sync's wait for a slot) holds no manager lock. A
  test runs requests, rebuilds and trims that close everything together under `-race`.
- Why a fixed default and not a share of RAM: in a container the RAM the process sees is the host's, not its
  limit, and the default should mean the same everywhere. 1 GiB on the 4-core, 6 GB target leaves room for
  rebuilds, Git, the Go heap of requests and the file cache that serves the mapped indexes. It holds about 80
  idle 50 000-page spaces, or 256 (the cap) small ones.

**Rebuild limit:** at most `--max-rebuilds` (default 2) big syncs run at once, across all spaces of the
process (`index.SyncSlots`). A big sync waits for a slot without holding its index's write lock, so writes and
small syncs of that space go on; once it has the slot it walks the files again. Syncs that read 1000 files or
fewer (the open of an indexed space, a pull of a few changed files, the sync after an edit) never take a slot.
One rebuild of 50 000 pages keeps about 2 cores busy; two leave the 4-core target able to answer, and halve the
time to recover from a schema change compared with one.

**Requests while a space waits for its rebuild** (owner decision, 2026-11-24): they answer as for an evicted
space being restored, through the same code (`RestoringError`, `awaitOpen` in `internal/spaces`). The open of a
space runs in the background; a request waits for it up to `RestoreWait` (10 s), and when the open is still in a
big sync it answers `503 restoring` with `Retry-After: 5` and `phase` `waiting` (no slot yet) or `rebuilding`
(the sync holds a slot). The index tells the phase to the space (`SetSyncNotify`, `space.Options.OnRebuild`),
which tells the manager. The open is not dropped by the answer: it goes on, and a later request finds the space
open. An open that needs no rebuild is waited for with the request's own context, as before. The web app shows
"Waiting to prepare this space…" for `waiting` (also when an evicted space waits for its restore turn) and
"Preparing this space: its index is being built…" for `rebuilding`.

**Cost at 50 000 pages and many users.** Memory of open spaces is bounded by the budget plus the spaces in use:
1 GiB, plus at most 2 × 150 MB for rebuilds under way, plus about 75 MB per rebuild of high-water that the
allocator keeps. Before, 64 such spaces could hold 3.5 GB idle and 6.4–15 GB in use. With many users each having
a large space, about 80 stay open; the 81st opened closes the least recently used idle one, and reopening an
indexed 50 000-page space costs 0.3–0.8 s (a walk and stat of its files, and the scan of its pages table) and
12 MB. After a schema change, N large spaces rebuild 2 at a time: about N × 15 s in all; requests for a space
whose rebuild has not finished answer `503 restoring` after 10 s, and the client tries again.

**Known issues:**

- 16 concurrent searches in one space are up to a third slower than before (fewer
  read connections); one search at a time is not slower.
- About 75 MB stays with the process after a rebuild: SQLite's allocator (modernc's
  libc) keeps what it freed. It is reused, and bounded by the rebuild limit.
- A server that is shutting down waits for the opens queued for a rebuild slot to
  finish (two at a time), as it waited for opens before.

