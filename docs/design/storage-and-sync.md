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
166 MB. So the text and the index always stay complete locally, and the rest
can be fetched on demand:

- **Attachments**: when a separate attachment repository or an S3 backend is
  used, only a cache with a size limit is kept locally; recently used ones are
  kept, and the others are fetched when the page is opened. The attachment
  repository is implemented with Git's partial clone (file contents are not
  downloaded in advance).
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
