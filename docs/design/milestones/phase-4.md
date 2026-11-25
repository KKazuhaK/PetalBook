# Milestones: phase 4, offline and clients

## Phase 4: offline and clients

PWA local storage and sync; the desktop version (the same Go program in a shell,
running against a local repository); the mobile version.

**One codebase for all platforms** (decided by the owner, 2026-11-23): the UI is
the same React, and the engine is the same Go (the packages in section 10, "The
boundary between the engine and the server", see
[overview.md §10](../overview.md#10-reuse)), with a thin shell around each: desktop uses the
system webview (Wails or similar), and mobile uses a webview, with Go compiled in
by gomobile. Which shell to use is decided before 4.4 starts, in one spike
(desktop and mobile compared together: package size, startup time, how Go and the
web page communicate, gomobile limits on iOS, whether CI can produce the
packages). The web version (PWA) stays as before, and the offline abilities of
4.1-4.3 are shared by all three.

### Phase 4 milestones (plan, 2026-11-23)

The basis of the order: first make "read" work offline (local storage and the
change stream). It changes no write rule, has the least risk, and is the base of
every later offline feature. Then "write" (offline changes, merging after coming
back online, and conflict copies), which must change the save rules of 1.x and so
is reviewed on its own. The Service Worker only makes the app itself open offline,
has nothing to do with data, and can run in parallel with 4.1. Desktop and mobile
both wrap a layer on top of these three: the desktop "connect to a server" mode
and mobile both use the offline ability of the web version directly, and no
second sync is written. One PR per milestone, and it still runs in single-user
mode as before (see 1.7).

| Milestone | Content | Depends on | In parallel |
|---|---|---|---|
| 4.1 | Local storage and the change stream: the server's change stream, the space catalog, fetching pages in batches by id; the browser uses IndexedDB to store the catalog of selected spaces and the bodies within scope; offline you can browse, read, and search by title and in the local bodies. Read-only while offline | None (based on `main`; adds only one column and one table to the index) | In parallel with 4.3 and 4.4; unrelated to the 3.x of phase 3 |
| 4.2 | Offline editing and sync: a queue of offline changes (edit body and properties, create, quick capture, move, delete, attachments), replayed in order after coming back online; three-way merge on the server, and what cannot be merged is saved as a conflict copy (the item of section 4.6, [storage-and-sync.md §4.6](../storage-and-sync.md#46-external-changes-and-conflicts), that was never done); idempotency keys for create and replay stored in the state database | 4.1 | In parallel with the shell part of 4.3 and with 4.4 |
| 4.3 | Service Worker: the app shell starts offline, a new-version prompt, on-demand attachment cache (with a limit), and in browsers that support it, background sync triggers the 4.2 replay | The shell and the attachment cache do not depend on 4.1; the small background-sync piece waits for 4.2 | In parallel with 4.1 |
| 4.4 | Desktop version: the same Go program (single-user form) plus a system webview window, running against the local repository, able to follow a Git remote; also a "connect to a server" mode, where the window is the web version and offline relies on 4.1-4.3. CI produces unsigned installers | The single-user form (1.7); offline in "connect to a server" needs 4.2 and 4.3 | The local repository part runs in parallel with 4.1-4.3 |
| 4.5 | Mobile: offline for the web version on phones (persistent storage, iOS limits, a toolbar above the keyboard, offline quick capture, sharing from other apps into the inbox). Not in the app stores | 4.2, 4.3 | In parallel with 4.4 |

Mobile apps in the app stores, and code signing and auto-update for the desktop
version, all need the owner to decide first (accounts, certificates, release
channel). They are placed in phase 5 (see below), not in phase 4.

**How offline relates to the save rules** (4.1 sets the direction, 4.2 writes the
contract):

- **Offline does not set up its own rules; it is only a "late save"**. All the 1.x
  rules stay as before: `rev` is the blob id of the file content; a save carries
  `baseRev`, and a mismatch is a conflict; the server never silently discards a
  draft; the service rewriting links is not a conflict (2.4). An offline change
  records on the device the `rev` it started from (`baseRev`) and the server draft
  version it last knew (`prev`). When back online, it is handed to the server as
  one write carrying these two values, and all the judging is done on the server,
  and the device does not guess.
- **A page has only one pending content on the device** (a snapshot of the whole
  page: header fields plus body). Repeated edits while offline overwrite it, and no
  operation log is kept. The unit of replay is "one page, once", not "one
  keystroke, once", so even hundreds of pages of offline changes are only hundreds
  of requests.
- **When back online**, for each pending content, the server does the following:
  1. The file is still at `baseRev`: write and commit as usual (like the 1.2
     automatic write; the person stopped writing long ago, so it no longer waits
     2 minutes).
  2. The file changed: do a three-way merge. The base is the `baseRev` version
     (fetched from the Git object store by blob id), one side is the one on the
     device, and the other is the file now. The header merges by field (a field
     changed on only one side takes that side, and a field changed to different
     values on both sides cannot be merged), and the body merges by line. If it can
     be merged, the merged content is written and the response says "merged". If
     it cannot, **it is saved as a conflict copy** (the same folder, a new id, the
     title followed by "(conflict copy)", and the header records the id of the
     original page), the original page is not changed by a single character, and
     neither side is lost.
  3. The base version cannot be fetched (a version in a local space that was
     overwritten a few seconds after it was written and never reached a commit;
     objects cleaned up by Git): save directly as a conflict copy. To make this
     rarer, each time the server writes a page, it also writes the content into the
     Git object store (`hash-object -w`, tens of microseconds), so every `rev` it
     hands out can serve as a merge base until Git itself cleans up unreferenced
     objects (two weeks by default).
- **Relation to the server draft**: if the device saved a server draft (version V)
  before going offline and kept writing offline, the replay carries `prev: V`. If
  the server draft is still V, the offline content is its continuation: after the
  write succeeds, that draft is deleted too. If the server draft is no longer V
  (another device kept writing), that draft is not touched, and after the write it
  becomes "behind" by the 1.2 rule, and the person chooses on the next open. So
  "the same person writing the same page on two devices" does not overwrite each
  other offline either.
- **New pages**: the device needs an id while offline (so it can be linked and
  opened locally), so the device generates it in the page id format. On replay, the
  server accepts an id that has a valid format and that nobody in the space uses.
  If it collides, another one is issued and the mapping is given in the response,
  and the device changes its local references. Creates and every replay carry an
  idempotency key, stored in the state database for 7 days, so resending after a
  lost response does not create two pages (this also fixes the 1.4 issue "quick
  capture may save two pages").
- **Move and delete**: move works as usual (a name collision gets a number, and
  links follow by 2.4). Delete carries `baseRev`, and if the page has already been
  changed on the server it is not deleted and the user is told, and the decision
  is not made for him.
- **Attachments**: a file uploaded offline is first stored on the device (each at
  most `maxUpload`, with a total limit). The body first uses a local placeholder
  address. On replay, the attachment is uploaded first, the real address is
  obtained, the body is fixed, and then the page is written.
- **The offline scope** (the key to efficiency; a space of tens of thousands of
  pages is not moved to the device whole): each space has three levels, default
  "not offline"; "catalog only" (a light summary of all pages: id, path, title,
  tags, `rev`, update time, about 2 MB compressed for 50,000 pages, so the folder
  tree, lists, and finding by title all work offline); "catalog plus bodies"
  (bodies only within scope: favourites, the inbox, the 200 most recently opened
  pages, and folders and tags chosen by hand, with a total limit; when it is
  exceeded, the "recent" ones not opened for the longest time go first, and pending
  changes are never dropped). After that, the change stream fetches only what
  changed, and the cost of one poll depends on "how much changed", not on how big
  the space is.
- **The 1.1 local draft** ("input not yet confirmed" in `localStorage`) moves into
  the same IndexedDB store in 4.2 and becomes one kind of pending content, so there
  are no longer two sets.

**Direction of the 4.3 Service Worker**:

- It caches only the app shell (the built files with hashes, and `index.html`) and
  **never caches `/api/*` responses**: data is always stored in IndexedDB by the app
  code per user and space, and the SW holds no data that differs per person. The only
  exception is attachments (`GET …/files/…`), which are put by address into a cache
  with a limit (default 500 MB, least recently used dropped first), and online they
  are still checked with the server as before.
- A navigation request falls back to `index.html` when offline, but `/api/`,
  `/oauth/`, `/mcp` and `/.well-known/` always go to the network: the single
  sign-on callback and the OAuth authorization page must not be caught by the SW.
- After a new SW version is installed, it waits, and the UI says "A new version is
  available, refresh". Before replaying offline changes, check that the shell and
  the server's API version match (the `version` of `GET /api/meta`), and if they do
  not, refresh first, so an old client does not write under an old contract.
- On sign-out, this account's attachment cache and the data in IndexedDB are
  cleared (ask first if there are pending changes).

**Direction of the 4.4 desktop version**:

- The same Go program, in the single-user form (`serve --space`), listens on a
  random loopback port on the machine, with a native window around it that uses the
  system webview (WKWebView on macOS, WebView2 on Windows, WebKitGTK on Linux), no
  Chromium bundled, so the installer is small. The Go binding is chosen between
  Wails and webview_go (or similar) in the spike before 4.4 starts, together with
  the mobile shell (see "One codebase for all platforms" above).
- The desktop version has three ways of use, matching section 1
  ([overview.md §1](../overview.md#1-goals-and-non-goals)): local mode without signing in (the repository on
  this machine, no backup); connecting to one's own Git repository (several devices
  sync; for the rules see section 4.7, [storage-and-sync.md §4.7](../storage-and-sync.md#47-two-devices-using-the-same-repository-of-their-own-owner-2026-11-23));
  and signing in to the official server. **The official build has no "server
  address" setting**: "connect to a server" connects only to the official server,
  and someone who wants to connect to their own server builds the open-source client
  themselves.
- The local service generates a random password at each start, and only the window
  can get it. A request without the password is always refused, so other programs
  on the same machine and web pages in a browser cannot use it (today's loopback
  mode relies only on the `Host` and `Origin` checks).
- The single-user form today has just one space. The desktop version must be able
  to open several repositories, so the single-user form gets "several spaces, no
  accounts": one directory per space, still without the layer of members and
  permissions.
- Following a Git remote follows 1.3 (SSH keys, HTTPS tokens). GitHub device-code
  sign-in needs a public OAuth app (the owner's account), which goes in phase 5.
- "Connect to a server" mode: the window opens the server's address, and it is the
  web version. Offline relies on 4.1-4.3. Whether the Service Worker works in
  WKWebView must be tested for real. If it does not, this mode has only the offline
  of 4.1.

**Direction of 4.5 mobile**:

- After being installed to the home screen, iOS does not clear a site's storage
  because of 7 days of no use (it does that only for sites that are not installed),
  so the offline instructions say "please install to the home screen". On Android,
  turning on offline requests persistent storage (`navigator.storage.persist()`).
- iOS has no background sync, so sync happens only when coming back to the
  foreground and when the network returns.
- The format toolbar sticks above the keyboard (`visualViewport`); quick capture
  works offline; on Android, Web Share Target puts text shared from other apps into
  the inbox.
- **The later mobile app** (listed in the stores in 5.15 of phase 5): the same React
  UI in the system webview, with the Go engine compiled into the app by gomobile.
  Like the desktop version, it has three ways of use: local mode, connecting to
  one's own repository, and signing in to the official server, and the official build
  likewise has no server address setting. What 4.5 builds on the web (keyboard
  toolbar, offline quick capture, sharing into the inbox) is used inside it as it
  is. The shell is decided with the desktop in the spike before 4.4.

**The 4.1 contract** (2026-11-23): the endpoints are written in "Offline sync" in
`docs/api.md`, and the frontend and backend follow it.

Decided:

- **The server does not remember devices**: no device registration, no per-device
  state. The cursor of the change stream is in the client's hands, and the server
  only answers "what changed after this cursor". So ten thousand devices take
  nothing on the server, and a lost device needs no cleanup.
- **The change sequence is in the index**: the `pages` table gets a column `seq`.
  Every time the index writes a page (added, content changed, renamed or moved,
  link rewritten by the service), it gives it a new sequence number larger than all
  before. When a page is deleted, a row is left in the new table `gone(id, seq)`.
  Querying "after a sequence" is one range read on the `seq` index, and when there
  are no changes it reads no rows. A page changed many times appears only once
  (only the latest sequence is kept), so after a week offline, what is fetched is
  only "the number of pages changed".
- **Cursor** = the index epoch plus the sequence. When the index is rebuilt
  (upgrade, manual rebuild, files not matching), the epoch changes, and an old
  cursor answers `410 changes_expired`. The client walks the catalog again,
  compares `rev`, and fetches only the bodies whose `rev` changed. Deletion records
  are kept for the latest 100,000 entries or 180 days, and an older cursor likewise
  answers 410.
- **Catalog**: `GET …/sync/catalog` gives a light summary in batches (at most 5000
  entries per batch) in the order of the pages in the index. The first batch also
  gives the change cursor from before the walk started, and after the walk, changes
  continue from it, so pages changed during the walk are not missed. It can walk
  only some folders (including subfolders).
- **Fetch bodies**: `GET …/sync/pages?ids=` takes at most 100 pages at a time,
  carrying the caller's drafts (one query), and one response is at most 8 MiB, with
  what does not fit listed in `rest` to be fetched again.
- **Asking about many spaces at once**: `GET /api/sync/heads?space=…` takes at most
  100 spaces, and gives only the current cursor of each space. The client fetches
  changes only for spaces that changed. The response comes from memory, and **does
  not open or fetch** closed spaces (the same for spaces cleared by 3.1), so polling
  by an offline device does not keep idle spaces open. A space that follows a remote
  but is closed gets the remote's changes only when someone opens it on the web.
- **Folder tree and settings**: the change stream's response carries two marks:
  `tree` (changes when the folders or the page count in a folder change) and
  `settings` (changes when the settings, tags, types or templates under
  `.petalbook/` change). When they change, the client fetches the existing
  endpoints `…/tree`, `…/tags`, `…/types`, `…/views`, and they do not go into the
  change stream.
- **Permissions**: all need `read`. When the space can no longer be read (the person
  was removed, or the space was deleted), the answer is `404`, and the client
  deletes this space's data on the device. An API token can be used (read-only), and
  an OAuth access token can still be used only on `/mcp`. Single-user mode has it too,
  with the space `default`.
- **Client**: data is stored in IndexedDB, with a database per "user, space", so
  signing in with another account cannot see another person's data. When online and
  the page is visible, it asks `heads` every 60 seconds, and asks at once when
  returning to the foreground or when the network returns. While offline, the editor
  is read-only, and the UI says "Offline, read-only"; the 1.1 local draft protection
  is unchanged. Local search looks only in the bodies present on this machine, and
  the UI says "Offline, only downloaded content is searched".
- **Cost targets** (tested on a synthetic space of 50,000 pages): one poll with no
  changes within 1 millisecond; a batch of 2000 catalog entries within 20
  milliseconds; 100 page bodies within 100 milliseconds (SSD); the index gets one
  more column and one small table, with a size increase of under 1%. The index
  version goes up by one, and the first start rebuilds once in full (about 30
  seconds for 50,000 pages), and after that every client walks the catalog once
  (comparing only `rev`).

Not doing (reasons in parentheses):

- **Server push** (SSE, WebSocket): it would take a connection per online device,
  and for the same reason the 2.6 MCP does not open SSE. Polling `heads` is a few
  memory lookups once, and once every 60 seconds is enough.
- **Property queries and table and board views while offline**: they need `props` to
  be moved to the device too; the offline of 4.1 has only the list, cards and
  reading.
- **Offline attachments**: done in 4.3.
- **A per-device sync status page** ("when did this device last sync", shown to
  administrators): the server would have to remember devices, which conflicts with
  the first item.
- **Offline for encrypted spaces**: encrypted spaces do not exist yet.

To be decided by the owner (build by the default in parentheses first):

1. The server does not remember devices, and the cursor is only in the client (yes).
2. With "catalog only", fetch the light summary of the whole space, even for tens of
   thousands of pages (yes; about 2 MB compressed for 50,000 pages,
   it can be changed to fetch only the selected folders).
3. The default scope of "catalog plus bodies": favourites, the inbox, the 200 most
   recently opened pages, plus folders and tags chosen by hand; the total limit of
   bodies on the machine is 200 MB (counted over these).
4. Poll `heads` once every 60 seconds, only while the page is visible; no server
   push (yes).
5. `heads` does not open or fetch closed spaces, and a remote's changes arrive only
   after someone opens the space (yes; otherwise offline devices would keep every
   synced space open and fetching all the time).
6. Deletion records are kept for 100,000 entries or 180 days (yes); a device that
   returns after longer walks the catalog again.
7. On sign-out, clear this account's offline data on the machine (clear; after 4.2,
   ask first if there are pending changes).
8. Offline data is not encrypted on the device (not encrypted, like the data of other
   sites in the browser, and the instructions say so; if the device is lost, revoke
   the session).
9. In 4.1 the editor is read-only while offline, and offline editing waits for 4.2
   (yes).

**4.1 as built (2026-11-23)**: everything in the contract above was done, and the
owner questions were handled by the defaults in parentheses. For the endpoints see
"Offline sync" in `docs/api.md`. What was settled during implementation:

- **The switch and encryption of offline copies (the owner's decision, following
  common practice)**: offline data on the device is **not encrypted at the
  application layer**, and relies on the storage isolation of the system and the
  browser; it is **cleared on sign-out** (done). Administrators can allow or forbid
  offline copies: the layered setting `offline.copies` (two layers, site and tenant,
  no space or personal layer, default allow). A tenant can turn it off for itself.
  The site value is the default for tenants that have not taken a position, and the
  site can lock it (a locked `false` is off for all tenants) or limit it to
  turn-off-only. When it is off, the four sync endpoints answer `403 offline_off`
  (after the checks of sign-in and the token itself, before the space is opened: a
  refused request does not restore an evicted space; the answer is unrelated to the
  space, and the spaces of other tenants and nonexistent spaces also get it, so
  nothing is leaked). The web version reads the effective value from
  `GET /api/settings`: when it is off, it no longer syncs or polls, and at the next
  start or poll it deletes this account's offline copies. The "Offline" settings
  area shows as unavailable with one sentence of explanation. Copies already on the
  device remain until the client's next start, and the server neither will nor can
  delete data on the device by itself.
- **Index version 10** (mainline is 9): `pages` gets a `seq` column and a `pages_seq`
  index, and a new table `gone(id, seq, at)`; the first start rebuilds once in full.
  If another branch also changes the index version, the numbers must be reassigned at
  merge (a branch is stuck on 9).
- **With the 3.1 eviction and restore, and the index template of #26 (when merging
  mainline c856f5a)**:
  - The mainline index version was still 9 at merge, and the 10 here is unchanged.
  - The index template (`index.ReuseSchema()`, used only in tests) carries no `meta`
    rows. The `meta` of a new 4.1 index (`epoch`, `seq`, `tree`, `gone_n`,
    `gone_floor`) goes into `insertMeta`, and a newly created index and a copy of the
    template take the same code, and after that both go through `markOpen` (without
    `clean`, the epoch is changed once more and `clean=0` is written). The template
    itself runs neither `markOpen` nor `loadHead`, and `Close` does not write `clean`
    for it. `template_test.go` tests that two copies have different epochs and the
    rest of `meta` is the same as a new one, and the copy's `clean`, `StoredHead`,
    that a clean reopen keeps the epoch, and that an unclean reopen changes the epoch.
  - The index of an evicted space is kept and closed: `heads` still reads the closed
    index (reading only `meta`), without opening or restoring. `Evict` records the
    head when it closes the space (the same as `closeIdle`); while a read-only index
    handle is open (`clean=0` in the index), the handle's head is used.
    `sync/catalog` and `changes` were added to the routes of the read-only index. The
    `settings` mark is stored in the index's `meta` (`note_settings`, in the same
    write as `clean`) when the space with its files closes, and an evicted space
    answers with that. `sync/pages` needs to read files, so like other body reads it
    restores first, and if it is not ready within 10 seconds it answers
    `503 restoring`.
  - Restore keeps the index: `restore` calls `index.NoteRestored` after closing the
    read-only handle and before moving the clone into place, and writes `restored=1`
    only if the index was closed cleanly. The next open (`markOpen`) reads and
    deletes it, and the first `Sync`, when it finds the files different, does not
    change the epoch (these are changes of the remote during the eviction, and get
    new sequence numbers as ordinary writes). If it cannot be written, or the index
    was not closed cleanly, the epoch is changed as before, the client walks the
    catalog again, and that is safe. For an index that has to be rebuilt (missing,
    broken, wrong version), `insertMeta` gives a new epoch.
  - The 3.1 fetch and restore both write the index through `Refresh` / `Sync`, and
    the sequence numbers are issued by the same `writer`; there is no other write
    path.
  - **When the index of an evicted space cannot be used, restore it; do not rebuild
    it as empty (review after the merge)**: the read-only index handle
    (`space.OpenIndexOnly`) now uses `index.OpenExisting`: with a read-only connection
    it first looks at whether the file exists, whether the version is 10, and whether
    `root` is right, and if not it answers `index.ErrUnusable`, deleting and creating
    nothing. `acquireIndex`, on meeting it (`space.ErrIndexUnusable`), takes the
    normal restore (`acquire` → `ensureRestored`; if not ready within 10 seconds, it
    answers `503 restoring`). Before, `index.Open` would delete a version 9 index and
    rebuild it as empty, so the list, tree, search, catalog and changes all answered
    empty, and the restore could not read the timestamp either. The old index is kept
    until the restore has put the files in place, and then the full space's
    `index.Open` rebuilds it from the real files (a new epoch).
  - **A race between restore and the read-only handle**: `restoreOp.indexClosed` is
    set after the timestamp is stamped and before `closeIndexOnly` (under `ev.mu`),
    and after that `acquireIndex` no longer hands out new read-only handles and
    answers `restoring`. A read-only handle being opened first takes its place in
    `ev.idx` (the `ready` channel), so `closeIndexOnly` waits for it and does not miss
    it. In addition, writing `clean=1` when a read-only handle closes has a guard
    (`Index.SetCloseGuard`): inside the write transaction, while holding SQLite's write
    lock, it looks at the space directory, and if the directory is already there (a
    full space owns the index) it does not commit, so an old handle that timed out
    does not write `clean=1` at its last close while the full space has the index
    open.
- **With the 3.3 tenants**: the four sync endpoints have the same permissions as
  reading. `heads` looks only at the person's own memberships, and memberships do not
  cross tenants, so spaces of another tenant appear only in `missing`. People and
  tokens of a deactivated tenant are refused at the sign-in and token layer (401), and
  nobody can open its spaces (403 `space_disabled`), and they are not in `heads` either.
  All four endpoints are in the route table of the isolation test (`isolation_test.go`),
  tested with another tenant's id, and `heads` requires that a space of another tenant
  and a made-up id get exactly the same answer. The `store.UserDrafts` used by
  `sync/pages` to read drafts queries only the `drafts` table, by the space that is
  already open and the caller's own id, and does not touch the tenant's five tables, so
  it needs no Scope and is not on the allowlist. The `settings` mark is computed per
  space, and the offline database is split by account id and space id, whose ids are
  unique on the whole server, so no change is needed. When 3.3 was merged, the mainline
  index version was still 9, and the 10 here is unchanged.
- **The sequence and the epoch are stored in the index's `meta` table** (`epoch`,
  `seq`, `tree`, `gone_n`, `gone_floor`). They are read once when first needed inside a
  write transaction and written back together in `finish`, and the in-memory cursor is
  updated only after the commit succeeds (`commitW`, inside an odd `gen`), so the head
  in memory and the snapshot that is read never drift apart. Every `upsert` gives the
  page row a new sequence number; `deleteRow` leaves a row in `gone` (when the same id
  is written again, this row is deleted, so "deleted and created again" is a catalog
  entry, not a deletion). A full rebuild (`clearAll`) changes the epoch and clears
  `gone`. Folders added or removed, and pages entering or leaving a folder, set the
  `tree` mark.
- **The limits of `gone`**: after every deletion it is trimmed by count (100,000) and
  by time (180 days), and the highest sequence trimmed is recorded in `gone_floor`. A
  cursor below it answers 410, without scanning a table. The `Sync` at startup also
  trims once by time. `gone_n` is only a count: when it drops to 0 it stays at 0 (and
  the table is counted again in this write); when trimming by count finds no row at the
  OFFSET, it also counts the table again and does not raise an error. So a wrong count
  cannot make a write or a startup fail.
- **New epoch**: the index uses `synchronous(NORMAL)`, and a power cut may lose the last
  few commits while the epoch stays the same, so sequence numbers would be issued again.
  So `meta` records `clean`: on open it writes `0` (this one with `synchronous(FULL)`,
  flushed to disk before any other write), and the last write of `Close` is `1` (holding
  the write lock, with no writes afterward). If it is not `1` on open, the epoch is
  changed and `gone` is cleared. The first `Sync` after opening also changes the epoch
  if it finds the files differ from what the index recorded (deleted ones, ones whose
  size or time changed, new ones; problem files that must be looked at again every time
  do not count). When the client gets 410 it walks the catalog again.
- **A folder move is one index write**: `Index.Move` first forgets the old path and the
  old folder in one transaction, and then reads the new one (it used to be two commits,
  `Forget` and `Refresh`, and a poll in between would treat the moved page as deleted).
- **The `next` of `changes`**: the head of the snapshot that was read (when there is
  more, the sequence of the last entry); it returns `since` unchanged only when there
  are no changes and `since` is already the head. So after the client catches up, its
  cursor and the head given by `heads` are the same string, and later polls only
  compare strings. This goes one step beyond the contract's "with no changes, next is
  since"; see `docs/api.md`.
- **`heads`**: `Manager.Heads` reads only the member table (cached; one query only on
  a miss) and memory. An open space answers its own head (one atomic read), and a closed
  space answers the head recorded when it closed (recorded by `closeIdle` and `Close`,
  with a limit of 200,000 entries). For what is not in memory after a server restart, it
  uses a read-only connection, outside the lock, to read the `meta` of the closed index
  (`epoch`, `seq`, `clean`, about 1 millisecond, without walking, fetching, or opening
  the space, and then remembers it in memory), so after a restart the client does not
  open every synced space. If there is no index file, the index was not closed cleanly
  (the epoch must change at the next open), or the index version is wrong, it answers
  `""`, the client therefore goes to fetch changes, and that fetch opens it. The
  read-only connection takes locks by SQLite's WAL rules and may leave an empty `-wal`
  and `-shm`, which do not affect later opens. An administrator who is not a member is in
  `missing` (the state database is not queried). A token looks only at the spaces it
  names.
- **The `settings` mark** is not in the index: the index cannot see `.petalbook/`. It is
  a digest of the names, sizes and modification times of the files in the three
  directories `.petalbook/`, `types/` and `templates/`, with three directory listings,
  and does not change with the number of pages.
- **`sync/pages`**: reads the files page by page (`Space.Get`), takes drafts in one query
  (`store.UserDrafts`, an `IN` list; tested on both SQLite and PostgreSQL), and what
  exceeds 8 MiB goes into `rest` from the next page on, and the first page always fits.
- **Client**: `web/src/lib/offline/`. One IndexedDB database per account per space
  (`petalbook-offline:<user>:<space>`, with the tables `meta`, `catalog`, `pages`,
  `textmeta`, `opened`, `walk`), and a registry in `localStorage` (the byte count of each
  database; the device total limit of 200 MB is counted from it, and it is also how the
  databases to clear are found; if it is lost, the prefixes are scanned with
  `indexedDB.databases()`). The catalog and the cursor are written in one transaction, so
  sync can continue from wherever it broke. While walking the catalog, every batch and
  "where the walk is" (the change cursor of the first batch, the cursor of the next batch,
  and the ids seen, recorded in the `walk` table) are written in one transaction. After a
  break the next time it continues from the next batch, and after the walk finishes, the
  ones not seen are deleted; it walks from the beginning only if the batch cursor is no
  longer accepted (410, 400). On a cursor 410, it walks the catalog again and fetches
  only the bodies whose `rev` changed. When a page is deleted from the catalog, its
  record in `opened` stays (it has its own limit of 200 entries, and the scope counts
  only what is in the catalog), so a page that moves back still counts as recently
  opened. The bodies in scope are fetched with at most 4 requests at a time, 100 pages
  per request, and `rest` is put back in the queue. When over the limit, first drop those
  "kept only because recently opened", the longest unopened first. Favourites, the inbox,
  and selected folders and tags are kept only if they fit.
- **Offline reading**: when a GET cannot reach the server, `request()` asks `readOffline`:
  the page list (folder, tag, date, sort, title and search within the saved bodies, with
  an offset as the paging cursor), a single page (with its draft), and the tree and tags
  (what the server last gave, or counted from the catalog if none). It does not answer
  requests for properties, bodies, boards, history, and types, which report a network
  error as before. When a whole page has no stored body it reports `not_offline`. The
  offline flag is the "device offline" event, the heartbeat (`heads`) failing to connect,
  or a read being answered by local data; any successful response clears it. While
  offline the editor is read-only (the top bar says "Offline, read-only" and the notice
  bar says so too), and half-typed content is still kept by the 1.1 browser draft. A page
  opened offline is restored from the server draft and the browser draft as usual
  (offline only makes the editor read-only and sends no request). When the network
  returns, this session is replaced by a new session read again from the server, and
  input is possible only after the draft is restored. An editor that was already open
  before the network went down is not replaced, unsaved content is kept, and saving
  continues after it recovers.
- **Polling**: `startPolling` asks `heads` once every 60 seconds, only while the page is
  visible (one request, at most 100 spaces), and asks at once when returning to the
  foreground and when the network recovers. It fetches changes only for spaces whose head
  and cursor differ. For a space that is not in `heads`: fetch changes for it once, and on
  404 delete the local copy, and on 403 `space_disabled` keep it, mark it read-only and
  stop asking until it appears in `heads` again. A space whose sync fails (not for lack of
  network) backs off per space: wait one poll period, and then double each time, up to 30
  minutes; one success returns it to normal, and a manual "sync now" does not wait.
  Sign-out (`api.logout`) clears all databases of this account and leaves other accounts'
  alone.
- **Settings**: a new "Offline" section was added to the settings (present in both
  single-user and account modes, for anyone who can read this space): three levels, the
  body scope (three switches for favourites, the inbox and the 200 most recent pages,
  plus selected folders and tags), the usage display (this space and the whole device
  against 200 MB), sync now, and clear. The page states that it is not encrypted and is
  cleared on sign-out.
- **Mock server** (`web/src/api/mock/feedBook.ts`): a page that differs from last time
  gets a new sequence number, and the behavior is the same as the real server (cursor,
  410, `rest`, `heads`). The contract test ("offline sync" in `contract.test.ts`) was run
  against both the mock server and the real single-user server.

**Cost** (a space of 50,000 pages, `PETALBOOK_TIMING=1 go test -run
TestChangeFeedInALargeSpace -v ./internal/space/`, SSD, one run on this machine):

| Item | Target | Measured |
|---|---|---|
| One `changes` with no changes | within 1 millisecond | about 0.08 millisecond (median) |
| `changes` with 20 changes | by the number of changes | about 0.3-0.4 millisecond |
| A batch of 2000 catalog entries | within 20 milliseconds | 15-19 milliseconds (whole space), 31 milliseconds by folder |
| 100 page bodies (read one by one) | within 100 milliseconds | about 20 milliseconds |
| `heads` (the head of one space) | a few microseconds | about 0.15 microsecond, no disk |
| `tree` mark / `settings` mark | | 0.12 microsecond / about 57 microseconds (three directory listings) |
| Index size | an increase of under 1% | +0.63% (87.1 MB to 87.7 MB, after tidying) |
| The first full rebuild | about 30 seconds | about 23-24 seconds |

Not done, left for later:

- After a settings change, only the tree and tags are fetched again, and not `types`,
  `views`, `templates` (offline in 4.1 does not use them); they are fetched when 4.2
  offline creation of pages needs templates and types.
- While offline, the home page (daily text, favourites), the property table, the board
  and history cannot be opened: these requests have no local data to answer with, and
  still report an error. The app shell itself opening offline waits for the 4.3 Service
  Worker; 4.1 only covers losing the network while the app is already open.
- The catalog has no excerpt, subtitle, or source: a page that has only the body stored
  has an excerpt in the list, and the others are empty.
- Search uses substring matching (no tokenization), so the result is not exactly the same
  as the server's; the UI says "Offline, only downloaded content is searched".

Known issues recorded in the second round of review:

- If someone changes a file directly on disk while the server is stopped, the stored head
  may happen to equal the client's cursor, and the client sees the new epoch only after
  someone opens the space; it is the same kind of delay as "a closed space is seen a bit
  later" in `docs/api.md`.
- An index from before the upgrade has no `clean` key: the first time each space is
  opened, the epoch changes once, and before that its head is empty; all offline clients
  will walk the catalog of every space once.
