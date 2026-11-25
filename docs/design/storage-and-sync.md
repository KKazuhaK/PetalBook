# Storage and sync

## 4. Storage and sync

### 4.1 Two modes (chosen per space)

1. **Local only**: the repository exists only on the server.
2. **Remote is authoritative**: copied from Rackbook. The server holds one
   clone, fetches on a schedule and fast-forwards; each write is fetch, reset
   to the remote, commit, push, and retry if rejected. Never force push.
   If it detects that the remote history was rewritten, it switches to
   read-only and waits for an admin to confirm. If the local clone is lost, it
   can be pulled again.

An optional second remote is only a "push only, never pull" mirror and takes no
part in conflict decisions.

### 4.2 Creating a space

- The server runs `git init` itself and writes `.petalbook/space.yml`; no
  template repository is needed.
- When a remote is chosen, it automatically creates the repository according to
  the credential type, or uses the address the user gave.

### 4.3 Remote credentials (chosen per space)

| Method | Used for | Source |
|---|---|---|
| SSH deploy key | Any Git service | Already in Rackbook |
| GitHub App | Creating repositories automatically under an organization | Already in Rackbook, needs to be extended to create repositories; permissions below |
| HTTPS access token | GitHub, Gitea, GitLab | New, reuses the way the GitHub App passes tokens |
| GitHub user authorization | Creating repositories under a personal account | New |

The permissions needed to create repositories (checked in GitHub's official
documentation on 2026-11-21):

- Under an organization, creating a repository is `POST /orgs/{org}/repos`;
  under a personal account it is `POST /user/repos`. The permissions table lists
  both at the write level of Administration, and notes that both installation
  tokens and user tokens can be used.
- A user token is obtained through web authorization or the device code flow. By
  default it expires in 8 hours, and the refresh token expires in 6 months, so
  the refresh token must be stored and renewed automatically. The permissions of
  a user token are the intersection of "what the user has" and "what the App
  has".
- Two points that the documentation does not state were tested on real GitHub on
  2026-11-23, with the following conclusions (these were measured, not derived
  from the documentation):
  - **Administration as a repository-level permission is enough**: if the App is
    given only the repository-level Administration write permission, an
    installation token with `POST /orgs/{org}/repos` can create a repository
    under the organization; the organization-level permission is not needed.
  - **When the installation scope is "all repositories", a new repository
    created by the App is accessible immediately**.
  - **When the installation scope is "selected repositories only", a new
    repository created by the App is not accessible**, because GitHub does not
    add it to the installation automatically. The server uses the user token to
    call `PUT /user/installations/{id}/repositories/{repoId}` to add it, and
    after that it is accessible; without a user token it answers
    `409 repo_not_in_installation` and leaves an "empty repository reminder",
    consistent with the design.
  - **User web authorization with PKCE works**.
  - **Creating a repository under a personal account fails if the App is not
    installed on that personal account**: GitHub answers
    `403 Resource not accessible by integration`. The user-to-server token can
    only be used on accounts the App is already installed on, and the
    permission is the intersection of three things: "what the user has", "what
    the App has" and "the account installed on". So authorizing without
    installing cannot create a repository under a personal account. To make
    personal accounts usable, the App must be installable on any account ("Any
    account", that is, public), and the user must install it once on their own
    account.
- The approach decided from this (2.7 patch):
  - "Connect GitHub" takes the person to the App's installation page and ticks
    "request user authorization during installation"
    (`request_oauth_on_install`), so installation and authorization finish in
    one flow on GitHub; the callback handles both `setup_action=install` and
    plain authorization.
  - When the user has authorized but the App is not installed on their own
    account, creating a repository under the personal account answers
    `409 app_not_installed` (with the installation address), and no longer
    passes GitHub's 403 straight on to the person.
  - The admin page must show a notice that "an App can only be installed on one
    account", because the personal account flow needs "Any account"; the admin
    page can also create such an App in one click with the App Manifest flow.
  - The admin page has a "Create the App on GitHub" button, which goes through
    GitHub's App Manifest flow: the server gives out the manifest (name, `url`
    as the public address, `callback_urls` as the authorization callback,
    `redirect_url` and `setup_url` as the manifest return address, `public` true,
    `request_oauth_on_install` true, permissions administration write /
    contents write / metadata read, no webhook), the browser POSTs the manifest
    to `https://github.com/settings/apps/new` (when an organization is given, to
    `/organizations/{org}/settings/apps/new`), GitHub returns with a `code` to
    `/api/connections/github/manifest/callback`, and the server exchanges it with
    `POST /app-manifests/{code}/conversions` for the App's ID, slug, client id,
    client secret, private key and webhook secret, and saves them encrypted just
    like a hand-filled App. `state` is bound to the admin's session and browser,
    can be used only once, and expires in 10 minutes. Filling in by hand is still
    kept.
  - The permissions on the admin page are no longer just a snapshot from when
    they were saved: a "Check again" button (which can also be triggered
    automatically by a rejected token request, at most once a minute) reads the
    App's and each installation's permissions from GitHub again, and lists the
    installations that have not yet accepted the new permissions.
  - When a call to GitHub fails, the log records the status code, GitHub's own
    words and the request type, and does not record tokens, private keys or
    authorization codes.
  - An App missing a permission and a repository not being in the installation
    scope are two different things, with separate error codes
    (`app_missing_permission` with the name of the missing permission, versus
    `repo_not_in_installation`).

Credentials are stored in the database, encrypted with the server key. Tokens
are passed to `git` only through environment variables, and do not appear on the
command line, in the remote address, in `.git/config` or in logs.

### 4.4 Drafts and commits

- While editing, the content is continuously saved as a draft (in the database,
  per page and user). The draft records the version it is based on.
- When it is merged into a commit: 2 minutes after the writer stops, when the
  user leaves the page, when the user clicks "Save version", or when more than
  10 minutes have passed since the last commit.
- The commit's author is the editor, and the committer is the service.

### 4.5 When local space is not enough: partial cache

What takes up space is mainly attachments and Git history, not text: the text of
50,000 pages and 62.2 million characters is only 192 MB, and the index is
166 MB to 305 MB (the full-text index is most of it). So the text and the index
always stay complete locally, and the rest can be fetched on demand. The index
is large, but what every open reads of it is not: the table a sync scans holds no
excerpts and has a covering index of its own (see database.md §5.2):

- **Attachments**: when a separate attachment repository or an S3 backend is
  used, only a cache with a size limit is kept locally; recently used ones are
  kept, and the others are fetched when the page is opened. The attachment
  repository is implemented with Git's partial clone (file contents are not
  downloaded in advance). A separate attachment repository is the default for
  a new space on a provider (the layered setting `storage.assets`: site lock >
  site lock for one tenant > site default > tenant default > the space's
  owner); a space with no remote keeps its attachments in the space, and a
  default that cannot get a second repository falls back to the space without
  an error. See "Where attachments go" in milestones/phase-3-tenants.md.
- **Git history**: a space repository can keep only the recent history, and
  earlier versions are fetched from the remote when the history is viewed.
- **A future offline client**: it can choose which spaces to sync; text is synced
  in full, and attachments are downloaded on demand with a cache limit.

Limits: a local-only space has no other copy, so it cannot use a partial cache;
when the remote cannot be reached, attachments and old versions that were not
cached cannot be viewed, and the UI must say so clearly. Partial clone requires
loosening the commands the restricted git executor allows; this is pending
verification (to be verified when the attachment cache is built, not in the
scope of phase 0).

The owner stated clearly on 2026-11-22: once there are many users, local disk
will certainly not be enough, so **we cannot require every space to have a full
clone locally**. In order of benefit, from largest to smallest, in four steps
(all of them hold only for spaces that follow a remote, because the remote is
the real copy; a local-only space is the only copy, and can rely only on quotas
or on putting attachments in object storage):

1. **An idle space is deleted from local disk entirely** and fetched back the
   next time it is opened. Now idle only means closed, and the clone is still
   kept. For a space that follows a remote, every change has already been
   pushed, so there is nothing unique locally and deleting is safe; the index
   can be kept (it is small), so the list and search do not wait for the fetch.
2. **Fetch only the recent history** (shallow clone), and when history is viewed
   ask the remote for earlier commits on demand.
3. **Do not fetch the contents of large files** (Git's partial clone,
   `--filter=blob:none` or by size); attachments are fetched only when someone
   opens the page, and locally only a cache with a limit is kept.
4. **Even the body text is fetched on demand**: locally there is only the index
   and the recently opened pages.

Steps 3 and 4 need the remote to support partial clone (GitHub and GitLab do),
and also need the commands allowed by the restricted git executor to be loosened;
neither has been verified. Verify them once on their own before doing it.
Opening a deleted space has to wait for one network round trip, and when the
remote cannot be reached this space cannot be opened; the UI has to say so
clearly.

The 3.1 trade-offs (do steps 1 and 3, do not do steps 2 and 4; use
`--filter=blob:none` rather than a shallow clone; the attachment cache only for
a separate attachment repository) are in the 3.1 contract in
[phase-3.md](milestones/phase-3.md).

### 4.6 External changes and conflicts

- After a fetch brings new commits, rebuild the index of the affected pages and
  run validation.
- When a draft is committed and the version it is based on is no longer the
  latest: first do a three-way text merge; if it cannot be merged, save the
  draft as a new "conflict copy", so neither side is lost.
- The same rules are used for: a direct edit on GitHub, another browser tab, and
  a future offline client.

### 4.7 Two devices using the same repository of their own (owner, 2026-11-23)

When signing in is not used and your own repository is connected directly
([overview.md §1](overview.md#1-goals-and-non-goals)), one repository can be used on several devices:

- **Each device is a clone**. Sync is fetch, merge, push: once each when the app
  is opened, on a timer, after an edit, and when the user taps it by hand.
- **Three-way merge per page**, with pages recognized by the id in the header,
  not by the path: it still lines up when one side renames and the other side
  changes content. The merge rules are the same as in 4.2 (header by field, body
  by line, and the base is the version in the last commit that both sides share).
- **What cannot be merged is saved as a conflict copy**, so neither side is lost.
  **A change beats a deletion**: if one side deleted and the other changed, the
  changed page is kept.
- **When a push is rejected**, fetch again, merge, and retry; **never force
  push** (the same as "remote is authoritative" in [storage-and-sync.md §4.1](storage-and-sync.md#41-two-modes-chosen-per-space)).
- This is not real-time collaboration: the two devices see each other's changes
  only after a while. To have several people edit together in real time, use the
  official server.
- The merge code is the same as the server's in 4.2 (in the engine packages, see
  the boundary in [overview.md §10](overview.md#10-reuse)); two copies are not written.

### 4.8 Packing Git objects (2026-11-24)

Commits are built with plumbing (`commitChanges`), which never makes Git pack
anything, so a clone only ever grew in loose objects: measured at 50k pages, about
90 KB per commit on average and about 250 KB in a 10,000-file folder. `.git` counts
toward quotas, so a local-only space grew without limit.

- **What runs.** `Repo.Maintain` runs `repack -d -q --geometric=2`, then
  `prune-packed -q`, then `commit-graph write --reachable`. The repack packs the loose
  objects into a new pack and merges small packs so that each is at least twice the
  size of the next: the pack count stays logarithmic in the history instead of one per
  run, and every object of a merged pack is kept. There is no `-a`, so nothing
  unreachable is dropped. `prune-packed` deletes only loose objects a pack already
  holds; the commit graph is a cache. `safegit.go` allows exactly these three forms:
  `gc`, `prune` and `repack -a/-A/-k/--cruft` stay refused, because they could remove an
  object that a commit in flight has written but not yet referenced.
- **Local spaces.** The committer counts commits it makes. After 50
  (`DefaultMaintainEvery`) its loop goroutine runs `Maintain`, after the commit is done:
  a save, `Flush` and `SwitchTo` never wait for it, except that a commit asked for while
  it runs waits for its end. `Close` runs it only if at least 10 commits were made since
  the last run, so a space that is evicted and reopened often does not leave a pack each
  time. A failure is a warning in the log; the commits it follows are fine.
  Lock: `committer.runMu`, which every committer commit holds, so packing never
  overlaps one. Other writers (restore, settings moves) do not take it, but cannot be
  hurt by it: nothing unreachable is ever removed.
- **Remote spaces.** Their fetch, commit and push run under the space lock (`s.mu`, with
  `fetchMu` for fetches), not `runMu`, and their commits do not go through the
  committer. So the committer's packing is switched off while a space follows a remote
  (`Connect`, `initRemote`; `Disconnect` switches it on). Instead, `mutate` packs after
  every 50th pushed change, after the push, inside the same space lock and under
  `fetchMu`, so it is never alongside a fetch or a push of that clone. That change's
  answer waits for it, which is the price of not needing a second lock. Fetch keeps Git's
  own `gc --auto`; we do not pass `gc.auto=0`, since it is harmless next to ours and
  stays the fallback if ours fails.
- **Cost.** Measured: 100 commits, 10 MB loose, a repack takes 354 ms and adds 1.3 MB
  packed. At the default threshold that is about 0.2 s of git work per 50 commits and a
  space ends about 7x smaller. The repack scales with the loose objects since the last
  run (plus the small packs it merges), not with the space. Known issue: `commit-graph
  write --reachable` rewrites the whole graph each run, about 1 s at 30k commits, so at
  that size every 50th change (remote) or every 50th commit (local, in the background)
  pays it; a split graph would make it incremental if that matters. Many users: spaces
  pack independently, one git process at a time per space, only after writing.
- **Known issues (round 1).**
  - On a remote space the pack runs under the space lock, so a slow first repack (up to
    the 10-minute timeout) holds back the writes that follow it. Packing after the lock
    is released would remove this.
  - No test covers a geometric repack in a partial clone (promisor packs). Git releases
    before about 2.49 may fail there; that is a logged warning, not data loss.

### 4.9 The write journal and how commits are cut (approved 2026-11-24)

A local space commits in the background (4.4), so every change is first written to a
journal in the state directory (`pending.journal`, `internal/space/journal.go`) and
fsynced before the files are touched. A crash between the write and its commit is then
recovered at the next start. This section is the redesign the owner approved on
2026-11-24. It ships as seven PRs (round 2), and each part below names the PR that
delivers it. On-disk formats change freely before the first release: there is no
compatibility or migration code for older journals.

**Where it stood before round 2.** The journal recorded intents: `w path rev` (a file
written with this content), `l from to` (a move whose links must follow), `b`/`bd`,
`u`/`ud` (bindings in `space.yml`), `f`/`fp`/`fd` (a type field rename), `m`, `r`, `t`,
`d`. Recovery re-derived the work from whatever was on disk. The committer staged the
working tree when its timer fired (`git add -A` on the pending paths) and, after a
commit, rewrote the journal from its in-memory map of pending paths instead of by
position in the journal.

**Known issues this fixes.**

1. Redoing `l` after a crash is not idempotent. `finishLinkRecs` runs `relink` again on
   content that may already be rewritten, so relative links without an `id` can become
   wrong (`A/B/A/B/y.md`). A remote `moveIntent` records `l` for every name it tries,
   and the undo paths never close them.
2. A background commit can split one rename in two: `tx.touch` comes before the write
   loop of `writeEdits`, and the commit's `add -A` stages whatever is on disk when its
   timer fires.
3. The commit log has three narrow windows:
   - (a) a save appends `w` but hands its path to the committer only later, in
     `afterWrite`; a commit in between rewrote the journal from the pending map and
     dropped the record;
   - (b) a failed or partial append at runtime was not cut back, so the next record was
     glued onto the fragment and both became unreadable;
   - (c) a file the server created and that was changed outside PetalBook before
     recovery is skipped (its `w` no longer matches), so it is never committed.
4. "Still typing during a save": when a save rewrites links, an editor that kept typing
   still holds the old text, and its next save writes the old paths back.

**The design.**

- **Framed records** (PR 1). Each record is one frame, one per line:
  `LLLLLLLL CCCCCCCC SSSSSSSSSSSSSSSS {json}\n`. L is the length of the JSON body, C
  the CRC-32C of the sequence number (8 bytes, big-endian) followed by the body, and S
  a sequence number that rises by one per frame. All three are fixed-width hex, so the
  file stays readable with `less` and `grep`. An append that fails (a short write or a
  failed fsync) is truncated back to the last good offset; if that truncation fails
  too, the next append retries it first. At startup the journal is read up to the first
  frame that is cut short, whose sum or end does not match, or whose number does not
  rise. The rest is logged (a warning for an interrupted append, including a block of
  zeros a file system left, an error for damage) and cut off. Nothing is refused, and a
  sound frame whose body is not a record is reported and passed over. Numbers continue
  across trims while the server runs, and from the last frame after a restart.
- **Trim by position** (PR 1). When a commit takes the pending paths it also takes a
  journal position S. That is the last frame number, or, if a change is running (it has
  journaled but not yet handed its paths over: `committer.begin`, called by `mutate`),
  the number just before that change began. After the commit the journal keeps every
  frame after S byte for byte, the frames of records still open (`b`, `u`, `l`, `f`
  until their `…d`), and a fresh `t` record for each path whose commit failed. This
  fixes 3a.
- **Record types** (PRs 2 to 6). These replace the intent records:
  - `x tx author`: a transaction begins;
  - `p tx path oldHash newHash [moveFrom]`: one per file. Content up to 64 KB is inline;
    larger content goes into one staged file per transaction, `tx-<n>.blob`;
  - `a tx`: applied. It is not fsynced, because replay is idempotent;
  - `c tx`: the transaction is in Git;
  - `k tx`: aborted.
- **Prepare, then apply** (PR 2 for save and `writeEdits`; PR 4 for moves and relinks).
  Under the space lock the whole change is planned first: the save, the moves, every
  link rewrite. Then `x` and the `p` entries are written with one fsync, and only then
  is the change applied.
- **Redo is a pure replay** (PR 2). A file already at newHash is skipped. A file at
  oldHash gets the new content. A file at any other hash was changed outside PetalBook
  and is left alone. Nothing is parsed again, and `l`/`ld` go away (PR 4). This fixes
  1 for local spaces.
- **A consistent commit snapshot** (PR 3). The committer takes only the applied
  transactions up to S, builds the tree from their recorded blob hashes with
  `update-index --index-info` instead of `add -A`, and after the commit keeps the
  records after S. Touch moves after apply. This fixes 2 and 3c.
- **Owner decision 1.** When a file was changed outside PetalBook before recovery, Git
  records the server's version. The outside edit stays in the working tree as an
  uncommitted change and is never lost.
- **Remote spaces** (PR 5) use the same unit. Undo writes `k`, and old contents come
  from the parent commit. This fixes 1 for remote spaces.
- **Owner decision 2.** A remote change that a crash cut short before its push is rolled
  back at startup, unless HEAD is already on the remote. The user never got a success
  reply, because remote mutations answer only after the push.
- **Fold-in** (PR 6). `b`/`u`/`f` become extra `p` entries for `space.yml` and the type
  files.
- **Issue 4** (PR 7, API and web). The save response adds `textRev`: the rev of the file
  as the client's text produced it, before this save's link rewrites. If its text
  changed since it sent the save, the editor sends `baseRev = textRev`, and the server
  rebases through the step it already logged.

**The seven PRs.** 1: framing, sequence numbers and trim by position (3b, 3a).
2: prepare/apply/replay for save and `writeEdits` (1, local). 3: the committer cuts by
transaction from blob hashes, with touch after apply (2, 3c). 4: moves and relinks as
one transaction, removing `l`/`ld`. 5: the remote path, with `k` and the rollback at
startup (1, remote). 6: fold in `b`/`u`/`f`. 7: issue 4, API and web.

**Cost (PR 1).** One save is still one append: one `write` and one fsync. The frame adds
35 bytes of header to the JSON body (about 120 bytes for a `w` record with an author),
plus a CRC-32C over it (hardware-accelerated). `begin` and its end are two short lock
pairs per change, with no I/O. The trim after a commit reads the journal once. The
journal then holds only what came after the last commit, plus open records. Before
round 2 the trim rewrote it from memory; both write it once and fsync.

**What PR 2 does.** In a local space a save and `writeEdits` (link rewrites, field
renames) journal one transaction before writing: `x`, then one `p` per file with the
blob ids before and after and the new content. A save whose write fails writes `k`, so
it is not replayed; after the writes comes `a`, without an fsync. `x` counts the frames
of its transaction, and a transaction that is not all there (its append was cut short,
so none of its files was written) is dropped whole. Recovery replays each `p` of a
transaction that has neither `k` nor `a`, by hash and in journal order among the older
records, so replaying twice is replaying once. A transaction with `a` is not written
again: its files may have changed since, even back to the old bytes. Its paths are only
committed where the file is still at the new hash. The `a` record is unsynced, but the
next synced append makes it durable, so only the last transaction before a crash can
lack it while applied, and its files are then at the new hash or untouched. Content
that cannot be read back is logged with the path and the transaction. Inline content
is bounded per file (64 KB) and per transaction (1 MB); the rest is staged, and neither
startup nor the trim reads staged files.
`writeEdits` also closes the change's open `l` records in the same append (an `ld` per
record). Without that, a crash after the rewrite was journaled would leave `l` open, and
the next start would plan the links again from files that are already rewritten: issue
1 again. The `l`/`ld` records themselves go in PR 4. `finishLinkRecs` now closes its
record through the same transaction. A space that follows a remote keeps the `w`
records until PR 5. Staged `tx-<n>.blob` files go when the trim drops their
transaction, and at startup when no record names them.

**Cost (PR 2).** A save up to 64 KB still costs one journal fsync, plus the file's own,
as before. The journal append now carries the content (its size plus about 250 bytes),
and there is one more unsynced write for `a`. Content over 64 KB costs two fsyncs more
(the staged file and its folder). A move that rewrites 1,000 files of about 1.5 KB each
was measured three times each way. On disk it took 0.94 to 1.13 s before and 1.00 to
1.18 s after, which is noise. On tmpfs, where an fsync costs nothing, it took 423 ms
before and 489 ms after. The journal for that change grows from 116 KB to 1.7 MB, read
once by the trim after the commit.

**What PR 3 does.**
- **Holding a change.** What a running change touches or applies is held until the
  change ends (`committer.begin`), so a commit never takes part of one: a move with all
  its link rewrites is one commit (issue 2). The one change that needs a commit of its
  own in the middle, a repair of the way to a folder, uses `FlushChange`.
- **Touch after apply.** A transaction's files are handed to the commit by `Applied`,
  after they are written, with the blob id each was written with. `writeEdits` no
  longer touches before writing. `afterWrite` skips a path the transaction already
  covers with the same content.
- **Commits from recorded blobs.** Such paths are committed from the recorded blob, not
  the work tree (`gitstore.CommitPathsAs`). `hash-object -w --no-filters --stdin-paths`
  writes the blobs in one process where the file still has that content. Where it does
  not, the content comes from the journal (`hash-object --stdin`, one process per such
  file). The index gets it with `update-index --index-info`. An edit made outside
  PetalBook therefore stays in the work tree as an uncommitted change (owner decision
  1). Paths of the older record types, and any path a later record of another type
  names, are staged from the work tree as before.
- **Recovery.** Recovery does the same: a transaction's file is committed as the server
  wrote it when the replay leaves it at the new content, or when the transaction was
  applied. A transaction that never was applied and whose file is at neither content is
  given up (`k`): what it would have written never was on disk.
- **Edge cases.** A path a transaction wrote keeps its Git mode (an executable page stays
  one). When one author's commit fails and another's is made, the trim journals the
  failed path again as an applied transaction of its own, with its content, so the retry
  still commits what the server wrote. A path with recorded content is committed without
  looking at the work tree, so a change that deletes or moves such a file must touch the
  path (`Touch` says so).
- **Pages made by the server** (`createNamed`) are now transactions: applied once the
  name is taken, given up (`k`) when another file holds it. A page made and then
  changed outside before the next start is committed as the server wrote it (3c), and a
  name another file held is still never committed.
- **Cost.** Measured three times each way on disk, a commit of a move rewriting 1,000
  files of about 1.5 KB took 136 to 232 ms before and 184 to 234 ms after. The extra
  part is a stat per file and an `update-index` process. A save adds no fsync and no
  git process.
- **Remote spaces** are unchanged (PR 5).

**What PR 4 does.**
- **Page moves.** A page move (`Move`, and a save that renames) first takes the new name
  as a second name of the file (a hard link, `moveNamed` with reserve), journaled as `mr
  From To`. It then plans the links and journals one transaction. That transaction holds
  the move as a `p` record of the new name with `From` set, the page's content (its own
  links rewritten when they change), and every other rewritten file. After that the
  files are written, the old name is removed, and `a` follows.
- **Recovery of page moves.** Replay writes the new name if it is still at the old
  content (or makes it from the old name's content if the second name was lost). It
  then removes the old name if that is still the same file or the old content. An `mr`
  that no transaction (one not given up) completes is taken back: the new name goes if
  it is still a second name of the same file. Nobody was told that move was made.
- **Folder moves.** A folder move journals `md From To`, then renames the folder. It
  plans the links and journals the transaction, with a `p` record of the folder (`Dir`)
  and the rewritten files, before `space.yml` is edited, and writes the files after.
  Recovery finishes the rename forwards only for a transaction not journaled applied,
  and only if the folder is still at its old place and nothing is at the new one;
  anything else is left alone and logged. An applied move is never made again, so a new
  folder under the old name, or a folder renamed by hand since, stays where it is.
  Without a transaction, the folder goes back (`mdd` closes an `md` whose rename failed), so the bindings, edited
  only after the transaction, never point at a folder that went back.
- **No more `l`/`ld` in local spaces.** Nothing is planned again at recovery.
- **Commits.** The moved page's new name is committed from its recorded blob, and its
  old name as gone (a known path with no blob id is committed as a deletion, without
  looking at the work tree).
- **Failures.** A move that fails after its transaction was journaled fails the change,
  and the next start finishes it. One that fails before has its name taken back. An
  append that fails and cannot be cut off again may still be on disk: the transaction is
  given up (`k`) at once, and if even that fails, it is logged as a change that may be
  made at the next start.
- **Remote spaces** still journal `m` and `l` and finish them forwards, until PR 5 rolls
  their changes back instead (done in PR 5).
- **Known issue.** The files of a moved folder that are not rewritten are still staged
  from the work tree (their `t` records). Reading every file to record its blob would
  cost as much as the folder is large.

**What PR 5 does.**
- **Same transactions.** A space that follows a remote uses the same transactions as a
  local one, for saves, rewrites, created pages, and page and folder moves (`mr`/`md`
  and their transaction). `Applied` journals `a` there but hands nothing to the
  background committer, because the change commits and pushes itself. `l`, `ld` and
  `m` are gone everywhere, with `finishLinkRecs`, `pendingLinkRecs` and `finishMove`.
- **The parent commit.** Each remote change journals the commit it starts from (`rp`),
  and journals it again after a repair of the way to a folder has been pushed.
- **Undo** gives the change's transactions up (`k`) in one append before the files are
  put back from that commit.
- **Startup (owner decision 2).** A remote space does not replay its journal. The
  journal holds at most the one change that was not finished: it is cleared once a
  change is pushed or undone. If HEAD has moved past the parent and is on the remote as
  the clone last saw it, the push went through and the change stays. Otherwise the
  folders it renamed go back, and `Discard` puts HEAD and every file the records name
  (with `space.yml` when bindings or a field rename were involved) back to the parent
  commit. Nothing reaches the network. A commit whose push went through unseen comes
  back with the next sync. When the change stays, its files are still put back to HEAD,
  so nothing half written remains of a step after the push (a crash between a pushed
  repair of the way to a folder and its `rp`). Folders a record names (`md`, a folder
  `p`, a folder respelling `r`) are renamed back, and never passed to `Discard`, which
  gets only files. `l`, `ld` and `m` left by an earlier build are logged and passed
  over. A rollback that cannot be done never stops the start: the space opens and
  refuses changes, keeps the journal, and takes the change back when the notice is
  cleared (or at the next start) before it lets changes in again. Folders are looked for
  by their exact names in the listing of their parent. A rename to another spelling of
  the same name goes through a third name, because a case-folding Linux file system
  takes a direct one for nothing.
- **Known issue (PR 5).** When the change stays (its commit is on the remote), its
  files are put back to HEAD. An edit made outside PetalBook to one of them while the
  server was down, between the push and the clearing of the journal, is overwritten.
- **Answers after the push**, as before.
- A commit made outside the journal (the clone ahead of the remote) is still pushed by
  the first sync or set aside, as before.

**What PR 6 does.**
- **Every write is a transaction.** Pages, settings (`space.yml`, tags, preferences,
  templates, types), attachments, created folders, deletions, the copies a repair keeps
  aside, and the files a repair of the way to a folder puts back are all journaled as
  transactions. A change journals what it is about to write (`txn.journalFiles`, with
  `writeFileIn`/`removeFileIn` for the common case), then writes, then journals `a`. A
  transaction that a failed change never got to is given up (`k`) when the change ends.
  A removal is a `p` record with no new content; replay removes the file if it is still
  at the old content.
- **Folder moves** are one transaction:
  - the folder (`Dir`);
  - an entry for each file that moves with it (`Ref`: its content is not in the
    journal, and its blob id comes from the content a change recorded for it, else
    Git's index, else the file itself);
  - the rewritten links;
  - `space.yml` with the bindings moved, written first so that a failure takes the move
    back.
- **Folder deletes** journal one transaction up front with the pages, the placeholders
  and `space.yml` without the bindings of what goes.
- **Field renames** are one transaction with the type file, every page and the views.
  It is put together page by page (`txBuild`): content past the inline bounds goes to
  the staged file through a buffered writer as it comes, the append is written a
  megabyte at a time, and the pages are written back reading the staged file through
  one handle.
- **Only `space.yml` that has to be repaired first** (unusable, or the way to it not a
  folder) is still edited on its own right after, as its own transaction.
- **A rename through a third name (`r`)** is finished forwards only when the move's
  transaction is there, and otherwise taken back, with the `mr`/`md` take-backs run
  after it.
- **Removed:** `t`, `d`, `w`, `b`/`bd`, `u`/`ud` and `f`/`fd`/`fp`, with
  `recoverBindings`, `recoverFieldRename`, the committer's open records (`Done`) and
  `txn.bind`. The journal now holds only `x`, `p`, `a`, `k`, `mr`, `md`, `mdd`, `rp` and
  `r`.
- **What still falls back to the work tree:** the committer stages a path from the work
  tree only where no blob can be recorded. That means a thing moved aside that is no
  file, a file stuck under a third name after a failed rename, and repositories with
  SHA-256 object names.
- **Cost.** A field rename over 50,000 pages of about 400 bytes each, on a seeded space
  on disk, was measured before and after:

  | | Before | After |
  |---|---|---|
  | Rename | 22.9 s | 22.5 s |
  | Commit | 46.7 s | 44.7 s |
  | Journal | 14.8 MB | 10.5 MB |
  | Sampled peak heap in use (includes garbage not yet collected) | 233 MB | 274 MB |

  The page contents are no longer held in memory; what remains are the records (path
  and blob ids, about 400 bytes a page) and the index's own work.

**Known issues (PR 3).**
- `contentAt` read and scanned the whole journal once per path whose file was changed
  outside PetalBook. Fixed in PR 4: the journal keeps where each frame is, and reads
  only that frame.
- In a repository using SHA-256 object names no recorded blob id (SHA-1, `page.Rev`)
  matches, so every path falls back to the work tree, and owner decision 1 does not hold
  there.

**Known issue (PR 2, accepted).** The `a` record is not synced. After a power loss (a
process crash does not lose it), the last transaction may be replayed although it was
applied. If an edit made outside PetalBook restored that file's exact old bytes in the
same window, the replay writes the new bytes over that edit. It takes both events
together, so it is accepted.

**Notes for the later PRs.**

- Frame numbers are not stable across restarts once the journal has been trimmed empty
  (they start again from 0). A transaction id that names a file, such as
  `tx-<n>.blob`, must either be removed when its transaction is trimmed, or be cleaned
  up at startup if no frame names it.
- `append` always fsyncs. The unsynced `a` record needs a variant that does not.
- The "change still running" mark of PR 1 becomes "the first transaction not yet
  applied" in PR 3.
