# PetalBook design: overview

Status: draft, 2026-11-21. The phase 0 validation is done; the real
implementation has not started. Items still marked "pending verification" were
not resolved in phase 0; each one says when it is carried forward to.

PetalBook is a self-hosted personal writing and notes app. The content is
Markdown files in a Git repository, edited on the web, with no need to know
Markdown. It is a sister project of Rackbook. They share the sign-in protocol
layer (authcore) and the way of doing Git storage, but they are separate
repositories and separate products.

## 1. Goals and non-goals

### Goals

- One model for three kinds of content: series of short texts, scattered quick
  notes, long essays, and ordinary notes.
- A complete web editing experience: WYSIWYG, covering all the commonly used
  Markdown syntax, with image and attachment upload.
- Three ways to organize: folders, tags, and page types.
- The content belongs to the user: plain text files plus Git history, which can
  be taken away at any time.
- One or two people can use it with zero configuration, and a team can use it
  too: local accounts, OIDC, SAML, passkey.
- By default everyone sees only their own content; shared spaces can be created,
  with access granted per person or per group.
- AI can read and write through MCP.
- Eventually usable offline, with desktop and mobile clients.

### Non-goals

- Real-time collaborative editing by several people (seeing other people's
  cursors in the same document).
- Per-page permissions. Permissions go down to the space only.
- Layout that Markdown cannot express: text color, columns, arbitrarily nested
  blocks.
- Horizontal scaling across several instances. The Git working directory is on
  local disk, and the service is a single instance. (The primary/standby
  redundancy of phase 5, 5.17, is one host plus one standby host; still only
  one instance serves requests at a time.)
- A full copy of Notion's databases (formulas, relations, rollups).

### How the app is used, and the official build (owner, 2026-11-23)

- **Local mode without signing in**: one person, one device, no backup. The
  content is the repository on this machine, with no accounts, the same as the
  single-user form (1.7).
- **Signing in to the official server is mainly for collaboration**: several
  people editing and sharing together, multiple spaces, organizations. Sync and
  backup come along with it; they are not the main reason to sign in.
- **Connecting directly to your own Git repository**: without signing in, you
  can also connect your own repository in the app (GitHub, GitLab, any Git
  address), and the same repository can be used on several devices (see
  [storage-and-sync.md §4.7](storage-and-sync.md#47-two-devices-using-the-same-repository-of-their-own-owner-2026-11-23)). This is sync, not real-time
  collaboration.
- **The official build in the app stores connects only to the official server**
  (plus the user's own repositories), and **has no "server address" setting**.
  People who want to use their own server modify the open-source client and
  build it themselves. So the "connect to server" mode of
  [storage-and-sync.md §4.4](storage-and-sync.md#44-drafts-and-commits) always points to the official
  server in the official build.
- **Scope of open source**: the client and local mode (including connecting your
  own repository) are open source. The server may later not be fully open source
  (open-core, like RustDesk); during development the repository stays public (CI
  is free).
- **External contributions must sign a CLA**, so that the official build can
  stay on the App Store; the two maintainers (Danika He, Kazuha Mo) sign too, or
  the rights are later assigned to a single entity. Which licenses the server and
  the client use is undecided; see [overview.md §13](overview.md#13-decided-and-open).

## 2. Core decisions

| Decision | Choice | Reason |
|---|---|---|
| Source of truth | Markdown files in a Git repository | Portable, has history, AI friendly, reuses Rackbook's approach |
| Unit of permission | The space; one space is one Git repository | Private content is physically isolated; team features can be added later without changing the model |
| Page identity | A fixed ID in the file header, independent of the path | Rename, move, offline sync and backlinks all rely on it |
| Editor | Tiptap, stores Markdown | The most mature ecosystem; only features that Markdown can express are exposed, to guarantee a lossless round trip |
| Markdown conversion | Written in-house with remark, not `@tiptap/markdown` | Measured on 116 cases: the official extension passed 75, the in-house conversion layer passed 113; see `spikes/markdown-roundtrip` |
| Tags and categories | Written in each file's header; queries go through the index | They travel with the file and do not create merge conflicts on a central file |
| State storage | SQLite (default) or PostgreSQL | Users, groups, sessions and so on are not content; MySQL is not supported, to keep test cost down |
| Search index | Always a local SQLite, split per character | A cache that can be rebuilt from the files; measured to be enough for 50,000 pages, no Redis or search service needed |
| Sign-in protocol | authcore's `oidc`, `passkey`, `saml` | Already exists, Rackbook uses it |
| Accounts, sessions, roles | Implemented by this project itself | authcore explicitly does not include an account model |
| Git storage code | Copy from Rackbook and decouple first; extract into a separate module in phase 3 | Not published until the interface is stable; see [overview.md §10](overview.md#10-reuse) |
| Drafts | Stored in the database; merged into a commit only after the writer pauses | Autosave does not flood the history, and a lost disk does not lose the draft |

## 10. Reuse

| Source | Content | How |
|---|---|---|
| authcore | `oidc`, `passkey`, `saml`, and `captcha` as needed | Direct dependency |
| Rackbook `internal/login` | Sign-in flow orchestration | Copy, then change to database accounts |
| Rackbook `internal/tokens`, `oauthserver`, `mcpauth` | API tokens, OAuth for MCP | Copy |
| Rackbook `internal/gitstore`, `githubapp` | Git read/write | Copy and decouple, see below |
| Rackbook `web/` | Shell, components, i18n, rjsf forms | Copy |

### The Git storage module

1. Copy `gitstore` and `githubapp`, remove the dependencies on Rackbook's
   `config` and `fsutil` and the hard-coded names, and organize them into a
   package that does not depend on application code.
2. Add multiple repositories, HTTPS tokens and repository creation in this
   project, and make the interface stable.
3. In phase 3, publish it as a separate module and switch Rackbook over to it.

In the meantime, the files responsible for security guarantees (mainly
`safegit.go`) are left alone as much as possible; a fix is made on both sides
at the same time. What goes into the module: the restricted git executor,
credentials, fetch/commit/push/retry, history-rewrite detection, and read-only
mirrors. What stays in each project: Rackbook's `Store` and PR mode, and this
project's drafts, merging, conflict copies and repository creation flow. This
module does not go into authcore.

### The boundary between the engine and the server (owner, 2026-11-23)

Keep this from now on: the **engine packages** (`space`, `gitstore`, `index`,
`page`, `links`, and the handling of Markdown: on the Go side it is currently
goldmark inside `links`, on the web side it is `web/src/markdown`) **do not
depend on packages that exist only on the server** (tenants, sign-in and
accounts `auth`, billing, multi-user permissions `perm` and the like). This way
the client (local mode, connecting your own repository, see
[overview.md §1](overview.md#1-goals-and-non-goals)) can be split out on its own later. Checked once
on 2026-11-23: `internal/space` depends only on `gitstore`, `page`, `links`,
`index` and `langtag`, and keeps to this rule. Later, add a test (a `go list
-deps` check on the engine packages) so that CI guards it; when new code needs
something from the server, wrap it in the server layer and do not pull it into
the engine packages.

## 12. Risks

| Risk | Response |
|---|---|
| The Markdown round trip is not clean, and the file has unrelated diffs on every save | Test it first in phase 0; if it fails, change the editor or write the serializer ourselves |
| The editing experience effort is underestimated | In the phase 1 scope it comes first, and the rest can be pushed back |
| The repository is bloated by attachments | A size limit; leave the storage interface ready for object storage |
| An outside edit of the files breaks the header | Validate after fetch, flag the problem pages, do not lose content |
| The two copies of the Git storage code drift apart before the module is extracted | Keep security-related files unchanged, sync the fixes |
| Offline changes overwrite other people's content when they come back, or are lost on the device | All judgment is on the server; keep using `baseRev` and "never implicitly drop a draft"; what cannot be merged is saved as a conflict copy; content waiting to sync is never evicted on the device because of a space cap ([storage-and-sync.md §4.2](storage-and-sync.md#42-creating-a-space)) |
| The browser clears offline data (iOS: 7 days for sites not installed, or storage full) | Install to the home screen, request persistent storage; unsynced changes stay visible in the UI and can be exported ([storage-and-sync.md §4.2, §4.5](storage-and-sync.md#42-creating-a-space)) |
| An old app shell writes to the new server under the old contract | Check the API version before replay, and refresh first if it does not match ([storage-and-sync.md §4.3](storage-and-sync.md#43-remote-credentials-chosen-per-space)) |
| A space of tens of thousands of pages makes offline sync slow | Fetch only the lightweight catalog and the bodies in scope, then afterwards fetch only changed ones via the change feed; the polling cost scales with the number of changes ([storage-and-sync.md §4.1](storage-and-sync.md#41-two-modes-chosen-per-space)) |
| When two processes run at the same time (rolling restart), the cache and counters are inconsistent | A known limitation, written in each milestone; 5.3 shares via PostgreSQL |

## 13. Decided and open

- The project name is decided as PetalBook, with a capital B (the config directory is still the lowercase `.petalbook/`; only name clashes on GitHub were checked, not Google, domains or trademarks).
- License: originally decided as AGPL-3.0 (anyone who changes the code and offers it as a service must publish the changes; Rackbook and
  authcore are Apache-2.0, which is compatible). **Reopened by the owner on 2026-11-23, undecided**: the client and local
  mode are open source, and the server may later be open-core ([overview.md §1](overview.md#1-goals-and-non-goals)), so which license the server and the client each use has to
  be decided again; the official build goes on the App Store, and the license must be compatible with the store's terms (this is also why a CLA is needed).
- The text of the CLA, how it is signed (for example a bot on the PR), and whether the two maintainers each sign or the rights are assigned to
  a single entity: undecided (on 2026-11-23 the owner decided 「要 CLA」 [a CLA is required]).
- Language (decided 2026-11-24): everything in the repository is in English (code comments, commits, pull requests, docs); only the README and the program's own interface are bilingual.
