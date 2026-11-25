# Milestones: phases 0 and 1

## Phase 0: Validation (finished 2026-11-21, leftovers below)

| Item | Result | Where |
|---|---|---|
| Lossless Markdown round trip | Tiptap as the editor, with the conversion layer written by us on remark; 113 of 116 cases pass, the other 3 have their reasons recorded | [editor-and-frontend.md §7.1](../editor-and-frontend.md#71-conclusions-of-the-round-trip-validation-2026-11-21), `spikes/markdown-roundtrip/` |
| Editing in the browser | Typing, shortcuts and pasting work; the three editing modes are feasible; three things found that the real implementation must add | [editor-and-frontend.md §7.0](../editor-and-frontend.md#70-three-editing-modes), [editor-and-frontend.md §7.2](../editor-and-frontend.md#72-conclusions-of-the-browser-measurements-2026-11-21) |
| Chinese search and scale | Local SQLite with per-character splitting; 50,000 pages is fine, no Redis needed | [database.md §5.2](../database.md#52-index-database-always-a-local-sqlite), `spikes/search-scale/` |
| Server-side rendering parity | goldmark with a Chinese emphasis extension, 114/116 identical | [editor-and-frontend.md §7.3](../editor-and-frontend.md#73-server-side-rendering-2026-11-21), `spikes/render-parity/` |
| Permission to create GitHub repositories | The docs plus a real test on 2026-11-23 settled it, see [storage-and-sync.md §4.3](../storage-and-sync.md#43-remote-credentials-chosen-per-space) | [storage-and-sync.md §4.3](../storage-and-sync.md#43-remote-credentials-chosen-per-space) |
| Name, license | PetalBook, AGPL-3.0 | [overview.md §13](../overview.md#13-decided-and-open) |

Not covered by phase 0 and still pending verification: composition input with
Chinese input methods; a full Tang round trip; Git partial clone (when the
attachment cache is built).

Carried forward to phase 1:

- Conversion layer: parse and write back the file header; write bare URLs as
  `<URL>`; avoid four asterisks when bold and italic overlap; replace
  punctuation with placeholder words when pages go into the search index.
- Editor: emphasis input rules that do not require a space; plain-text paste
  keeps single line breaks; parse Markdown source when it is pasted.
- Re-run the round-trip tests after all of Tang is imported.

## Phase 1: Usable for daily work myself

Local accounts and OIDC; private spaces; two modes, local-only and remote as
the source of truth (SSH key, HTTPS token); all editor syntax; images and
attachments; folders and tags; list and card views; search; drafts and commits;
version history; quick capture and inbox; Markdown import; Chinese and English;
installable PWA; SQLite and PostgreSQL.

Completion criterion: all of Tang imported, and two weeks of writing only in
it, with no going back to Notion.

Phase 1 is split into these milestones, each of which can run and be accepted
on its own:

| Milestone | Content |
|---|---|
| 1.1 | Single user, local space, no sign-in. Reading and writing page files (including the header), folders and tags, index and search, the HTTP API (`docs/api.md`); the web sidebar, list, editor and autosave |
| 1.2 | Sign-in (local accounts, OIDC), the state database (SQLite and PostgreSQL), private spaces, drafts stored in the database |
| 1.3 | Remote-as-source-of-truth mode (copy and decouple Rackbook's Git storage), SSH keys and HTTPS tokens, version history with diff highlighting |
| 1.4 | Images and attachments, card view, quick capture and inbox, the three editing modes, the three editor improvements |
| 1.5 | Markdown import, Chinese and English UI, installable PWA, build and image for release |

In 1.1, saving writes the file directly, and the server merges the writes into
a commit after the user stops typing; storing drafts in the database comes in
1.2.

### Milestone 1.1 as built (2026-11-21)

Done and passed the pre-commit review. Where the implementation differs from
the text above, or adds to it:

- **Local draft**: about 0.7 seconds after a change, the edited content is
  written to the browser's local storage; it is written once more,
  synchronously, when the page is hidden, reloaded or closed. It is cleared
  only after the server confirms it stored the same content. The next time the
  page is opened it is restored automatically, or, if the server version has
  changed, it leads to the conflict dialog. It expires after 30 days. It
  guards against losing content that has not yet reached the server, and only
  works in the current browser; protection across devices comes from the
  server-side draft in 1.2.
- **Background commits include only files the server itself wrote.** The paths
  waiting to be committed are recorded in a log in the state directory, and
  after a crash the restart carries on committing them. While the repository
  is in the middle of a merge, rebase or similar, committing is put off. Files
  placed in the space by hand are not committed automatically.
- **Cross-site requests** are always rejected; the rules are in `docs/api.md`.
  During development, list the web page's address with `--allow-origin`.
- **The disk is not watched**: changes made outside the server show up in the
  list after a restart or an index rebuild; when a single page is accessed by
  ID, the file is always checked.
- `make contract` runs the front end's API tests against a real server, so
  that the front end's mock server and the real behavior cannot drift apart.

Known issues left by the review, not blocking, carried forward to 1.2:

- The commit log has three narrow windows: a save interleaved with a commit
  snapshot may miss one entry; after a crash interrupts an append, the next
  entry may become invalid (this needs two crashes in a row); a file the
  server created and that was changed externally before recovery is not
  committed automatically. In all three cases the file content is intact; it
  is just temporarily not in Git history.
- When the repository stays in the middle of a rebase for a long time, the
  commit log keeps growing.
- A non-text item in the tag list (such as an empty value) cannot be deleted
  in the UI.
- A `.md` file with no header or a damaged header is only recorded in the
  startup log and cannot be seen in the UI.
- Listening on an external address (`--insecure-no-auth`) is not safe until
  there is sign-in, see `docs/api.md`.

### Milestone 1.2 as built (2026-11-21)

The API is in `docs/api.md`. What was settled in the implementation, which the
text above does not mention or words differently:

- **Two ways to start**: `--space <directory>` is single-user without sign-in
  (that is, 1.1); `--data <directory>` is account mode, with spaces in
  `<directory>/spaces/<space ID>/`, the index in `<directory>/cache/`, and the
  state database by default at `<directory>/petalbook.db`; use
  `PETALBOOK_DATABASE` to switch to PostgreSQL. Single-user mode also has a
  state database (it holds the drafts) and runs the same code as account mode.
- **The first administrator**: when there are no accounts, the startup log
  prints a one-time setup link once; the person can set a password, or create
  the account by signing in directly with OIDC. When nobody can sign in, use
  `petalbook user add|passwd` to change the data directory directly.
- **Adding people** is by invitation link (7 days by default, single use). To
  have OIDC create an account automatically on first sign-in, add
  `--oidc-auto-create`. Accounts are matched only by issuer plus `sub`, never
  by email.
- **Spaces**: each account automatically has one personal space and can create
  more, up to 20 per person by default (`--max-spaces`). Spaces are opened on
  demand; when one has been idle for 15 minutes, or more than 64 are open at
  once, the least recently used is closed. The commit author is the space
  owner and the committer is the service.
- **Drafts** (changes a detail of [storage-and-sync.md §4.4](../storage-and-sync.md#44-drafts-and-commits)):
  each draft has a version number; saving, committing or deleting a draft must
  carry the version the client last saw, and a mismatch is rejected, so the UI
  shows the conflict dialog. One person writing the same page on two devices
  does not overwrite themselves. **The server never discards a draft
  implicitly**: when the page file is saved directly, the draft stays (it
  becomes stale, and the next time the page is opened the person chooses). A
  draft disappears only through a commit, a replacement or deletion that names
  its version, or deletion of the page itself. So the client does not have to
  guess whether what is on the server is its own: with no unsaved content, if
  the server changed it simply shows the server's version; on switching back to
  the tab it takes a look on its own, so content changed on another device
  shows up directly. After 2 minutes without typing, or when the draft has
  existed for 10 minutes, the server writes it into the file itself; within a
  few seconds of the write it is committed to Git (the default of
  `--commit-idle` therefore changed from 2 minutes to 5 seconds). A draft whose
  page file no longer exists is not deleted; get it back with `petalbook draft
  list|export`.
- **The local draft** is stored per user, space and page, so signing in with a
  different account does not show someone else's. After the server confirms it
  stored the draft, the local copy is marked "confirmed" and takes no further
  part in any decision (it will not pop up an old text as a conflict the next
  day); it is used only to recover the text when the page was deleted
  elsewhere, and it is cleared once the text is in the file.
- **Verifying the draft state machine**: every case where "the server differs
  from what I thought" goes through one rule table in the client
  (`web/src/editor/reconcile.ts`). `model.*.test.ts` (sharing
  `modelHarness.ts`) drives the real controller and recovery logic with random
  sequences against a mock server that follows the real server's rules (about
  nine thousand orderings, including lost responses, server auto-writes, a
  second device, offline and reload), checking eight invariants at every step.
- **The state database**: SQLite and PostgreSQL use the same table schema and
  the same tests (`make test-postgres` starts a temporary PostgreSQL to run
  them; CI runs both). Sessions store only the hash of the token, and queries
  go through an in-process cache.

Known issues, carried forward:

- Someone who keeps entering a wrong password for a user name can keep that
  user name from signing in with a password (each time it locks for at most 5
  minutes, but the lock can be renewed). A full fix requires telling trusted
  source addresses apart.
- Changes made to accounts from the command line (changing a password, adding
  a person) are only seen by the running service after up to 5 minutes.
- In account mode, renaming a space changes only the database, not
  `space.yml`.
- OIDC has not been run against a real identity provider, only in automated
  tests with a fake provider.
- An integer in the header larger than 2^53 is rewritten to a value that lost
  precision after any field is edited (present since 1.1).
- When a file is changed outside the application and the editor content equals
  what the editor last knew of the file, this is not noticed until the next
  request.
- The behavior of two tabs open on the same page has only unit tests, and has
  not been tested in a real browser.
- When a page header is larger than 16 KB, history cannot read versions of that
  page (deciding which page a version belongs to reads only this much of the
  start of the file).
- The model test cannot tell when the rule "the server implicitly discards a
  draft" is broken (its second device replaces a draft without looking, so the
  corresponding invariant is relaxed); this rule is guarded by unit tests and
  API tests.
- The command-line and single-user-mode tests run only on SQLite; the
  account-mode tests run on both databases.
- The service is single-instance: when two processes share one PostgreSQL,
  there is no cross-process protection for automatic draft writes or for the
  cap on the number of spaces.
- The commit-log windows left by 1.1 and the damaged-header files that cannot
  be seen in the UI are still not handled.

### Milestone 1.3 as built (backend, 2026-11-21)

The API is in `docs/api.md`. What was settled in the implementation, which the
text above does not mention or words differently:

- **The Git layer** is `internal/gitstore`, with no dependency on the rest of
  the application: a restricted executor (every git command first goes through
  the allowlist in `safegit.go`, and a push is always `push --quiet origin
  HEAD:refs/heads/<branch>`), tokens and SSH keys, fetch / fast-forward /
  commit only the given paths / push / revert, rewrite detection, page history.
  Local-space commits go through it as well, so there is exactly one place in
  the process that starts git. The allowlist entries added relative to Rackbook
  each have their reason written next to them in `safegit.go`.
- **Transport** is only https and ssh (`GIT_ALLOW_PROTOCOL`; address validation
  also rejects other forms). The token is handed to a fixed credential helper
  through an environment variable; the SSH private key exists only for the
  duration of one command, as a 0600 file in a 0700 temporary directory; the
  host key is trusted on first connection, and if it changes afterwards the
  state is `error`. Every network command has a timeout, and when it expires
  the whole process group is killed.
- **Writing to a remote space** does a full round inside the space lock: fetch
  (skipped if fetched within a few seconds), fast-forward, apply, commit only
  the paths changed this time, push; if rejected, revert (one `reset`, one
  `ls-tree`, one `checkout`) and retry on the new state, at most 4 times; for a
  push whose response was lost, first check whether the remote already took
  it. Reads do not wait for the network: reads do not take the space lock.
  When a draft is committed, the draft lock is not held while waiting for the
  network, so typing is still saved as usual when the remote is slow or
  disconnected.
- **Crash recovery** does not touch the network: when a space is opened, the
  uncommitted files recorded in the log are committed; the first sync after
  that (one is queued immediately on every open) pushes them, and if the remote
  has moved ahead, the unpushed commits are kept at
  `refs/petalbook/unpushed/<time>`, the clone is moved to the remote, and the
  state is set to `error`, with the note naming that ref. If the working tree
  contains changes that were not made by the server, writing is refused
  (`error`, and those changes are left alone).
- **Pulling lives in the manager**: one loop that looks only at spaces that are
  currently open and follow a remote, fetching in turn every `--pull-interval`
  plus jitter, at most 4 at a time. Closed spaces are not pulled (nobody is
  looking), and are pulled once immediately the next time they are opened. A
  pull does not count as "use", so a space that is only being pulled is still
  closed for being idle.
- **The index**, after a fast-forward, updates only the files changed between
  the two commits (`index.Refresh`), and falls back to a full sync by
  modification time only when more than 2000 files changed.
- **History** uses `git log --follow` plus a page id check (the id in the file
  header in each version must be this page's); the cursor is "commit plus
  path", and the next page reads backward from that commit rather than walking
  from the start.

Known issues, carried forward:

- When a rename comes with more than half of the content changed, `--follow`
  cannot recognize it as the same file and the history breaks there.
- In a history with merge commits, cursor paging may miss earlier versions that
  were on a side branch and merged in later.
- If the local clone (`.git`) is lost, it is not re-fetched automatically:
  opening reports an error, and the administrator has to deal with it.
- The first fetch of a very large repository is limited by `InitialTimeout` (4
  minutes), which must be shorter than the service's response timeout.
- The GitHub App, automatic repository creation, a second push-only mirror, and
  webhooks are all not done yet.

### Milestone 1.4 as built (2026-11-21)

The API is in `docs/api.md`. What was settled, where it differs from the text
above or the text above did not say:

- **Attachment references are written as paths from the space root**
  (`/assets/<page ID>/<file name>`), not the relative paths that
  [data-model.md §3.4](../data-model.md#34-attachments) describes. A relative path breaks when
  the page is moved, and the body would have to be rewritten, while the body
  may have a draft right then; the form starting from the root is unaffected by
  moves and renames, and GitHub and GitLab web pages can show it directly.
  Existing repositories that write paths relative to the page's folder still
  display; the rules are in "Files" in `docs/api.md`.
- **An attachment is an ordinary file and goes through the same write path as
  a page**: in a local space it goes into the commit log and is committed by
  the background committer; in a space that follows a remote it is committed
  and pushed on the spot, and if the push is rejected it is reverted. The file
  name carries the first 8 characters of the content hash, so uploading the
  same content again creates no new file.
- **Reading an attachment requires sign-in**, and the response carries a policy
  that forbids scripts; images, audio, video, PDF and plain text display
  directly in the browser, and everything else is a download.
- **Compression before upload**: a PNG or JPEG over 300 KB or with a side over
  2560 is redrawn as WebP, and used only if it is more than 20% smaller; it can
  be turned off in the browser ("Upload original pictures").
- **The quick-capture shortcut** is Option/Alt+Shift+N. Browsers keep most
  Ctrl/Cmd+Shift+letter combinations for themselves, and a web page cannot
  intercept them.
- **A file that cannot be staged no longer blocks later commits**: when
  `git add` fails for the whole batch (a file matched by `.gitignore` or the
  user's global ignore rules, or a path replaced by something outside the
  server with a symlink or device), files are staged one by one instead, and
  files the server wrote itself ignore the ignore rules; the one that still
  cannot be staged is left out of the commit. A path that has become a
  directory is skipped in the same way. The server itself does not create
  folders whose names end in `.md`.
- In raw-text mode, after an uploaded image lands in the editor, the editor
  rewrites the body into its uniform style (the same as typing on the WYSIWYG
  side).
- **Attachments nobody references are not cleaned up**, and deleting a page
  does not delete its attachments (older versions may still use them). A
  cleanup task is left for later.
- **The inbox is a folder name**, one setting per space (`inbox`, default
  `Inbox`; when a space is created on the web page, the name in the UI
  language is used). Changing the setting moves nothing.
- **Card view**: the choice is remembered in the browser per "space, list kind,
  folder or tag" and is not written into `space.yml` (the view configuration
  mentioned in [editor-and-frontend.md §8.4](../editor-and-frontend.md#84-folder-view) is done
  together with page types). The excerpt grew from 200 to 400 characters.
- **The three editing modes** ([editor-and-frontend.md §7.0](../editor-and-frontend.md#70-three-editing-modes)):
  - There is only one copy of the content (header fields and body), and both
    sides are ways of editing it. Typing on the raw-text side makes the body
    exactly that text, saved as is; typing on the WYSIWYG side makes the editor
    rewrite the body into its uniform style. Whichever side was typed in last
    is what gets saved.
  - **The header is not saved as is**: the header in raw text is parsed into
    fields and handed to the server, which writes it back to the file. Key
    order and the way unchanged values are written are kept, but comments are
    lost, and the way quotes and lists are written may be normalized. This is
    not fully consistent with "what is typed in raw text is kept as is" in
    [editor-and-frontend.md §7.0](../editor-and-frontend.md#70-three-editing-modes); only the body does
    that. When unchanged, the header shown on the raw-text side is the original
    text in the file (the API's `header`).
  - When the header is written badly (not valid YAML, `id` changed or deleted,
    a `---` with no end), the UI points out which line; meanwhile the body is
    saved as usual, the header stays at the last correct content, switching
    back to WYSIWYG is not possible, and the right side of the side-by-side
    view cannot be edited for the time being. The badly written raw text is
    stored separately in the browser (it is neither content that can enter the
    editor nor something that can be saved to the server), and a prompt appears
    before the page is closed. When the page is opened again, or when the page
    is updated elsewhere while it is open, this raw text does not attach itself
    again (the page or draft may have changed elsewhere); it is set aside, and
    the UI asks whether to keep editing or discard; there can be several set
    aside, and they do not overwrite each other. The banner has a button to
    abandon these changes directly.
  - In the side-by-side view, the other side catches up after typing stops
    (0.15 to 0.6 seconds depending on body length); if you click into the other
    side before it has caught up, it syncs first and then lets you type. Syncing
    replaces only the blocks that changed, and the cursor and the position of
    an image being uploaded do not move. Scrolling is matched block by block,
    and by proportion when blocks cannot be matched.
  - In raw text, changing the body to the editor's own style (for example
    changing a `*` list to `-`) counts as a change and is saved; on the editor
    side, a difference in style alone is not taken as a change.
  - On a narrow screen (under 768 pixels) there is no side-by-side view.
- **The three editor improvements** ([editor-and-frontend.md §7.2](../editor-and-frontend.md#72-conclusions-of-the-browser-measurements-2026-11-21))
  are all done: input rules such as `**bold**` right after a Chinese character;
  when pasting plain text, a single line break is a line break inside the
  paragraph and only a blank line starts a new paragraph; when Markdown source
  is pasted, it is parsed into formatted content.

Known issues left by the 1.4 review, not blocking:

- When the browser's local storage is disabled or full, raw text whose header
  is not yet fixed exists only on screen. Closing the page, navigating within
  the site, or the page being updated elsewhere will first ask or warn; but if
  the sign-in happens to expire at that moment (kicked back to the sign-in
  page), this text is lost. With working storage this does not happen.
- The header on the raw-text side is saved as data, and comments and the way
  quotes are written are not kept (stated above).
- After restoring a history version in raw-text mode, undo puts back
  normalized Markdown.
- When the page content is replaced as a whole (updated elsewhere, choosing the
  other side in a conflict, restoring a history version), the landing position
  of an image being uploaded may end up at the end of the text.
- When a deleted page is recovered from a local draft, other header fields that
  were not changed are not brought back.
- Uploading a file reads the whole of it into memory before writing to disk; the
  limit is at most 256 MiB.
- When quick capture is saved in two tabs at the same time, or the request
  succeeds but the response is lost, two pages may be saved.
- In the side-by-side view, on a narrow window the rightmost button of the
  toolbar is cut off.
- The quick-capture shortcut, IME input and the real file picker have not been
  tested by hand.

### Milestone 1.5 as built (2026-11-21)

- **No Notion-specific import** (the owner's decision of 2026-11-21): it does
  not recognize the file name suffixes or database properties of a Notion
  export package, and does not split the outline inside a page into many pages.
  Only a general Markdown import is done; tables and other content wait for the
  table support of [reserved.md §15](../reserved.md#15-reserved-tables). The way to bring content
  over from Notion is to first tidy it up there into one Markdown file per
  page, then import.
- **Markdown import** ("Import" in `docs/api.md`): one Markdown file, or one
  zip. Each `.md` is one page; an existing file header is kept (except `id`; a
  new one is issued for every page); the title is taken from the header's
  `title`, otherwise from the leading `# heading`, otherwise from the file
  name; images and files in the zip that a page references by relative path
  become that page's attachments. Existing pages are not overwritten. The whole
  import is one change: one commit, one push for a space that follows a remote,
  and if it fails nothing is left behind.

- **Chinese and English UI**: all text is in the two dictionaries under
  `web/src/i18n/`, and types guarantee that the keys on both sides match. The
  language is decided once when the page is opened (a stored choice wins,
  otherwise the browser language is used), and switching language reloads the
  page. Error messages returned by the server are in English and shown as is.
- **One program**: `make release` compiles the web front end into the Go
  program (`internal/webui`), and the same address serves both the UI and the
  API; `make build` is still the API-only program and does not need Node. UI
  responses carry a content security policy that does not allow inline scripts.
- **Installable**: there is an app manifest and icons, but no Service Worker
  (offline comes in phase 4). The icons are drawn from one set of paths by
  `scripts/icons`.
- **Image and release**: the `Dockerfile` makes a small image with git and ssh,
  which by default listens on 8080 in account mode, with data in `/data`;
  `--public-url` (or `PETALBOOK_PUBLIC_URL`) must be given, otherwise the
  browser's requests are rejected by the cross-site rules. When a `v*` tag is
  pushed, the GitHub workflow builds the program for four platforms and pushes
  the image.

Known issues left by 1.5:

- The image and the release workflow have not been run on this machine (no
  docker), and rely on the build job in CI for checking; the release workflow
  will not actually run until the first tag is pushed.
- Import handles one request at a time, and its place is taken before the
  request body is read: someone uploading very slowly makes others' imports
  queue (only imports are affected).
- During import, attachment links in a single line over 64 KiB are not checked,
  and the relative links on that page are left as they are.
- Importing into an existing folder whose name ends in `.md` is not possible.
- When storage is disabled, switching language gives no indication (nothing
  happens).
- The English UI has not been reviewed by a native speaker; content in Notion
  needs to be tidied into Markdown files by oneself first.

### Milestone 1.6: file management and editor polish (proposed by the owner after trying it on 2026-11-21)

Decided after trying phase 1. The interaction follows Notion's approach.

File management:

- The main area works like a file manager: a folder lists its subfolders and
  pages, folders first, with different icons; the top level is the same. The
  top bar is a clickable breadcrumb, and the page editor uses it at its top
  too.
- Creating a folder asks only for a name and creates it in the current folder;
  the person is not asked for a path.
- Pages and folders can both be dragged into another folder (in the list and
  on the sidebar tree); folders can be renamed.
- A right-click menu (open, rename, move, delete, create here); after a bulk
  selection, move or delete.
- Sidebar: at the very top of the tree is a "Top level" row (pages that are
  not in any folder are found here), with the top-level folders indented under
  it, no longer alongside it; clicking an already selected folder again
  expands or collapses it; the inbox appears only once; when there are many
  folders and tags, each scrolls on its own.
- Tags can have colors, stored in `.petalbook/tags.yml`.
- Search: one dialog, with results as you type and a preview; it can be
  filtered by folder, tag, time range, and title only.

Editor:

- Everything Markdown supports is supported. Headings go to level 4 (levels 5
  and 6 are not put in the toolbar, and those already in a file display as
  usual). Bold, italic, strikethrough, inline code and links are also put in
  the top toolbar.
- **No underline** (the owner agreed): Markdown has no such syntax.
- Tables: when inserting, a grid is used to choose the size; when the mouse is
  at a table's edge, add-row and add-column controls appear, and the first
  cell of each row and column has a menu.
- A code block can have its language chosen, and is colored by language.
- A ```` ```mermaid ```` code block is drawn as a diagram in WYSIWYG, and the
  code is edited in raw text.
- The image popup labels "Description" and "Address", and both can be edited.

Settings:

- Settings are merged into one place, made to look like Notion: grouped
  navigation on the left (Account: Profile, Preferences; Space: General, Sync,
  Import; Admin: users and invitations), content on the right. Appearance
  (light/dark, color scheme) and language are in "Preferences", and each item
  is "name + one line of explanation + a control on the right". The entry
  points that are now scattered across the user menu, the bottom of the
  sidebar and a few separate pages are all brought in.

Themes:

- Besides light and dark, the color scheme can be changed: a few public color
  schemes are preset (Catppuccin, Nord, Solarized, Gruvbox, Rosé Pine and so
  on, each with a light and a dark version), plus the current default scheme.
  It is chosen in settings and remembered in the browser. In the implementation
  only CSS variables change, and components are not touched.

### Milestone 1.7 (planned): shared spaces, permissions and the admin console

Direction set by the owner on 2026-11-22, to be done after 1.6:

- **Shared spaces**: a space can have several members; built-in roles owner,
  editor and read-only. Permission checks are written by permission item,
  leaving room for custom roles (see [auth-and-permissions.md §6.2](../auth-and-permissions.md#62-roles)).
- **Administrators can see everything**, and there is a separate admin console
  at `/admin`, apart from personal settings: all users and invitations, all
  spaces (owner, members, size, sync state; transfer ownership, disable,
  delete), and the server's status. The "users and invitations" that 1.6 puts
  in settings moves here.
- **Single-user use and the multi-user server must be separable** (the owner,
  2026-11-22): later there will be desktop and phone apps, which are one person
  using their own repository and do not need accounts, members, roles, an admin
  console, sign-up, or that whole layer. So this layer must be optional:
  - The core (page files, index, Git sync, import, attachments, history) does
    not depend on accounts and permissions; the current single-user mode
    (`serve --space`) is the form that has only the core, and must keep
    running and keep being in the tests.
  - The multi-user parts (accounts, space members, roles, the admin console,
    later sign-up and quotas) are a layer on top of the core, and talk to the
    core only through one interface, "who is making the request and what
    permissions they have on this space". New features first hold in
    single-user mode, then permissions are added.
  - The front end likewise: the admin console, members and account settings
    are parts that can be left out of loading as a whole; in single-user mode
    the UI shows no entry point to them.
- **Several sign-in methods** (the owner, 2026-11-22, later): at present only
  one OIDC provider can be configured. Later it must be possible to configure
  several at once, with one button each on the sign-in page, and linking and
  unlinking one by one under the account's "Security". Google and Microsoft
  (Entra ID) are standard OIDC, so it is mainly changing "one" to "several";
  GitHub is not OIDC (no id_token; its OAuth and user API must be used), so a
  separate adapter has to be written. When the same email signs in from
  different providers, accounts are not merged automatically, unless that
  provider declares the email verified and the administrator has turned on
  this option.
- **Multi-tenancy and public sign-up** are a later stage and presuppose shared
  spaces: a sign-up switch, email (verification and password recovery),
  per-person quotas, abuse prevention, re-evaluating how strong the data
  isolation is, and terms and a privacy notice. Encrypted spaces come after
  that too.

**What 1.7 does and does not do** (set 2026-11-22): it does shared spaces,
permission items, member management, commit authors recorded per person, the
admin console, and the split of the single-user form. Several sign-in methods,
groups, sign-up, quotas and encrypted spaces are not in 1.7.

**Implementation plan** (2026-11-22):

- **Permission items, not comparing role names**. One package defines the
  permission items: `read` (see pages, history, attachments, search), `write`
  (create, change, move and delete pages and folders, tags, attachments,
  import, drafts), `members` (manage members), `manage` (rename, inbox, remote
  and sync, rebuild index). A role is a set of permission items and is defined
  only in this one table: owner = all, editor = `read`+`write`, read-only =
  `read`. A later custom role is just more rows in this table.
- **The API recognizes only "who, and what permissions they have on this
  space"**. Each route declares the permission item it needs, and `handle` in
  `api` is the only place that decides: a non-member (except an administrator)
  always gets 404; a member who lacks the permission gets 403 `no_permission`
  (a separate error code, not mixed with the cross-site rejection's
  `forbidden`). The core (the `space` package) knows nothing about accounts and
  roles, and only receives one extra thing, "whose this change counts as".
  In single-user mode the only local user has all permissions and does not go
  through the member table.
- **Decisions do not query the database**: membership is still cached in memory
  per person, and a permission decision for one request is a lookup in a small
  table; when members change, only the caches of the people involved are
  invalidated.
- **Administrators can see everything**: an administrator has all permissions on
  any space, but is not counted as a member, and other people's spaces do not
  appear in their own space list; to enter someone else's space, click in from
  the admin console.
- **Member management** (needs `members`): add a person by user name with a
  role, change the role, remove; a member can leave on their own. A space keeps
  at least one owner. A personal space can only have its own person and cannot
  be shared.
- **Commits record the author per person**: each change carries the person who
  made it, commits group by author when they are made, and one commit contains
  only one person's changes; the author is that person and the committer is
  still the service. Entries in the log also carry the author, so the commit
  author is not lost after crash recovery. A space that follows a remote
  already makes one commit per change and just uses that person. The cost: when
  two people both changed things in the same idle window, there are two commits
  instead of one.
- **The admin console** (administrators only; in single-user mode these APIs do
  not exist):
  - Users and invitations: the API does not change; the UI moves from settings
    to `/admin`.
  - All spaces: name, whether it is a personal space, owner, member count,
    size, sync state, whether disabled. One query brings out the owner and the
    member count. **The size is not computed when the list is requested**:
    walking a space of tens of thousands of pages each time is too expensive,
    so it is computed slowly in the background and stored in the state database
    (each space at most once every few hours, one at a time), the list only
    reads the stored value, and a single space can be recomputed manually.
  - Transfer ownership: the new owner becomes owner and the original owner is
    lowered to editor. A personal space cannot be transferred.
  - Disable and restore: a disabled space is still in the members' lists (with
    a mark), but cannot be opened or changed; those already open are closed.
  - Delete: close it first, delete the record in the database, and **move the
    directory into `<data directory>/trash/`** instead of deleting the files on
    the spot, so a wrong deletion can still be recovered from disk. A personal
    space cannot be deleted while its person still exists.
  - Server status: version, uptime, database type, number of spaces and users,
    number of spaces currently open, free space on the disk that holds the data
    directory.
- **Front end**: the space information carries the person's own role and
  permission items; a read-only member does not see the entry points for
  create, delete and drag, and the editor is read-only. Space settings gain a
  "Members" section. The admin console is at `/admin`, a separate block loaded
  on demand and loaded only by administrators; in single-user mode the entry
  points for members, accounts and the admin console do not appear.

**1.7 as built (2026-11-22)**: everything in the plan above is done; the API is
in "Members" and "Administration" in `docs/api.md`. What was settled during
implementation and the plan does not mention:

- The permission items are in `internal/perm`, a one-byte bit set; routes
  declare the permission items they need in `internal/api/routes.go`, a missing
  permission is answered with 403, and the error code is `no_permission` (kept
  apart from the cross-site rejection's `forbidden`; the front end tells them
  apart by code).
- The membership cache carries a generation, so a list read before a member
  change is not stored in the cache after the change; a removed member is
  rejected on the very next request. The check for the last owner locks the
  space's row inside a transaction, and counts only owners who can sign in.
- Commits record the author per person: when the author changes, the previous
  person's pending changes are committed first and then this person's changes
  are written, so one commit contains only one person's changes, and with
  interleaved editing there are somewhat more commits (one per change of
  person).
- The automatic write of drafts first checks whether the author can still write
  this space: the drafts of someone who was removed or lowered to read-only are
  kept, not written, and looked at again after 10 minutes; they are written as
  usual once permission is restored; a read-only member can still discard their
  own drafts. When a space is deleted, everyone's drafts are first saved as
  `trash/<id>-<time>.drafts.json`.
- A disabled space is still in the members' list but cannot be opened; members
  can leave a disabled space.
- When an administrator looks at someone else's space, the front end fetches
  just that space by id (`GET /api/admin/spaces/{id}`) and does not page through
  the whole list; the person picker for transferring ownership searches by user
  name or name and lists only usable accounts
  (`GET /api/users?q=&enabled=1`).
- The admin console's UI, text and API calls are in a separate on-demand block,
  which non-administrators do not load.
- Tested in a real browser: a read-only member sees the "Read only" mark, the
  editor is not editable, and there is no create or delete; the three pages of
  the admin console; an administrator opening someone else's space by address
  sends only one request.

Known issues, carried forward:

- When two people alternately save in the same space, every change of person
  waits for one Git commit (about 60 milliseconds), during which other writes to
  this space queue; a stuck Git blocks writes to this space.
- When a deletion fails (`os.Remove` returns an error), earlier changes by other
  people on this path are recorded under the person who deleted; a crash, only
  in a very narrow time window, can make the author of some path recorded
  wrongly. Neither loses content.
- Rewriting the log after a commit may lose records that are "logged but not yet
  handed to the committer" (present before 1.6); after a crash that change
  waits until the next time this file is touched to be committed.
- On PostgreSQL, the case-insensitivity of the space-name search is reliable
  only for ASCII (a database with the C locale).
- After an administrator removes themselves from a space, they can still open
  it by address as an administrator.
