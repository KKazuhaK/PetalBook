# Milestones: phase 5, the rest

## Phase 5: everything else not yet scheduled

This collects what earlier sections marked "later", "left for later",
"late stage", "reserved" or "put off until later", together with the known
issues each milestone "carried forward". They are ordered by benefit and
dependency. Each item is one PR (large ones are split into several parts, with
a contract written for each when the time comes). The last column says whether
the owner must first decide something outward-facing before work starts
(releases, app store accounts, signing certificates, domains, external
services, creating a repository in KazuhaHub, licenses). Items marked "none"
can be finished entirely inside this repository.

| No. | Content (goal) | Depends on | Outward-facing matters the owner must decide first |
|---|---|---|---|
| 5.1 | Known-issue cleanup, starting with those that lose history or write wrong content: 2.4 redoing the `l` record after a crash is not idempotent, and a background commit during a half-finished rewrite; a few narrow windows in the 1.1 / 1.7 commit log; 1.3 re-fetching automatically when the local `.git` is lost, and history broken by `--follow`; 2.4 "still typing while saving" writing back to the old path. The 2.4 issues and the commit-log windows are being fixed by the round 2 journal redesign, [storage-and-sync.md §4.9](../storage-and-sync.md#49-the-write-journal-and-how-commits-are-cut-approved-2026-11-24) | None | None |
| 5.2 | Fetching page bodies on demand ([storage-and-sync.md §4.5](../storage-and-sync.md#45-when-local-space-is-not-enough-partial-cache) step 4, which the owner explicitly asked for on 2026-11-22) and shallow clones (step 2): keep only the index and recently opened pages locally, and stream the bodies when building the index | 3.1 merged, plus its measurements (how much clearing the space and attachment repositories saves) | None (3.1 problem 1 already defaults to "decide after measuring"; show the owner the measured result then) |
| 5.3 | Multi-process safety: the invalidation broadcast for member and group caches, the sign-in rate-limit counts, the MCP `requestId`, and the OAuth 60-second grace reply all become shared between processes through PostgreSQL (`LISTEN/NOTIFY` and tables). The goal is that two processes running at once during a rolling restart cause no errors. It is not horizontal scaling | None | None |
| 5.4 | Quota rejection: going over bytes, pages or people is answered with `507 quota_exceeded` (3.3 only warns); attachments are counted separately and deduplicated per person | 3.3 merged, plus measured usage | The owner decides the rules and defaults (not outward-facing) |
| 5.5 | Attachment wrap-up: a cleanup task for attachments nobody references (1.4, 3.4); S3-compatible storage (the third backend in 3.4, tested in CI against a compatible service started locally); an on-demand cache for `assets/` inside the space repository; moving attachments back from the attachment repository | 3.1 | None |
| 5.6 | Push instead of polling: GitHub App webhooks, and Gitea / GitLab push notifications, which trigger a pull on receipt; a closed space that receives a push only records it, and pulls when it is opened | 2.7, 3.1 | Configure the webhook address and secret on the owner's GitHub App (needs a publicly reachable address) |
| 5.7 | Custom roles ([auth-and-permissions.md §6.2](../auth-and-permissions.md#62-roles)): a role is a set of permission items, and the space owner defines roles besides the three built-in ones. The 1.7 permission checks are already written per permission item, so the work is mainly UI and storage | 3.2 | None |
| 5.8 | View and editor additions: timeline / calendar and gallery ([editor-and-frontend.md §8.4](../editor-and-frontend.md#84-folder-view)); manual drag-to-reorder (3.3); bulk changes to a field's value (2.1); links to subheadings (the `#anchor` of 2.4); coloring diffs by editor in shared spaces ([editor-and-frontend.md §8.7](../editor-and-frontend.md#87-history-and-diff-highlighting)); math formulas ([editor-and-frontend.md §7.3](../editor-and-frontend.md#73-server-side-rendering-2026-11-21) was never verified, so do a round-trip verification first) | None | None |
| 5.9 | Machine access additions: MCP resources and prompts, a backlinks tool, moving the pagination of `tags_list` / `folders_list` into SQL, per-token request rate limits, the audit event `grant_created`; access through MCPHub ([auth-and-permissions.md §6.3](../auth-and-permissions.md#63-machine-access), after 2.6) | 2.6 | For MCPHub, the owner must confirm its token format and how to connect (external service); nothing else |
| 5.10 | Identity additions: SCIM (left over from 3.2), the GitHub device-code flow (left to the client in 2.7), GitHub Enterprise Server and multiple Apps, the App manifest flow, mapping by organization id and GitHub teams (3.2), expanding Entra ID group overage and OIDC userinfo, SAML single logout and signing-key rotation, group administrators, auditing of direct membership changes | 3.2, 3.3 | None for the code; real-world testing needs the owner's accounts: the device code needs a public OAuth / GitHub App, GHES needs an instance, and SCIM needs an Entra ID or Okta tenant |
| 5.11 | Public publishing ([reserved.md §14](../reserved.md#14-reserved-public-publishing)): read-only pages served directly by this service at `/p/<space>/<slug>`; publishing to GitHub Pages | 5.5 (public addresses for attachments) | None for the part this service serves; GitHub Pages needs a public repository under the owner's account to try it, and a custom domain needs the owner's domain |
| 5.12 | Tables ([reserved.md §15](../reserved.md#15-reserved-tables)): a second kind of content in a space, plain text by row, going through the same drafts, commits and conflicts | None (the contract of section 15 is already being kept) | None |
| 5.13 | Encrypted spaces ([auth-and-permissions.md §6.2](../auth-and-permissions.md#62-roles)): encrypt and decrypt in the browser, the server holds only ciphertext | 4.2 (offline and encrypted spaces have to be thought through together), 5.7 | The owner decides the trade-offs (password recovery, server-side search, and history comparison cannot be done); not outward-facing |
| 5.14 | Tenant follow-ups (what 3.3, 3.6 and 3.7 all do not do; tenants' own sign-in providers and senders are already in 3.6, and one account in several organizations was brought forward to 3.7): telling tenants apart by host name, approval after sign-up (3.6 only did applications by domain), re-consent after terms updates, moving people and spaces between tenants; refinements of admin viewing and export (3.5) by region or compliance (notify the owner first, a second person approves, audit retention) | 3.7 | Host names need the owner's domain and DNS; the text of the terms and privacy notice, and compliance requirements, are for the owner to decide |
| 5.15 | Client releases: code signing, notarization and automatic updates for the desktop app; mobile app store listing (the React UI inside the system webview, the Go engine compiled in with gomobile, see the direction in [storage-and-sync.md §4.5](../storage-and-sync.md#45-when-local-space-is-not-enough-partial-cache); official builds connect only to the official server, and outside contributions need a CLA, see [overview.md §1](../overview.md#1-goals-and-non-goals)) | 4.4, 4.5 | All of them: an Apple developer account, a Windows signing certificate, a Google Play account, package names, an update server or release channel, app store privacy statements |
| 5.16 | Outward-facing naming and release: trademark and domain (section 13 only checked GitHub), the language of the public README; publishing the Git storage module to KazuhaHub (the second part of 3.4) | 3.4 | All of them: domain, trademark, creating the repository in KazuhaHub, the module's license (Apache-2.0 or AGPL-3.0) |
| 5.17 | Redundancy (direction set by the owner on 2026-11-23, see below the table): a primary and a standby, not active-active; PostgreSQL streaming replication for the state database; spaces that exist only on the server get a scheduled git push to the standby; health checks plus failover, with the old primary fenced off at failover | PostgreSQL as the state database; 3.1 (spaces that follow a remote are fetched again on the standby) | Needed: two servers, Cloudflare or a floating IP; the owner's existing failover script will be looked at then |

**5.17 redundancy direction** (the owner, 2026-11-23):

- **Primary and standby, not active-active**: one primary and one standby, each
  with 4 cores and 6 GB. Only the primary takes requests at any one time, so
  "no horizontal scaling with multiple instances" in [overview.md §1](../overview.md#1-goals-and-non-goals) still holds.
- **State database**: PostgreSQL streaming replication to the standby (SQLite
  deployments have no such redundancy).
- **Spaces**: spaces that follow a remote are not replicated. After failover
  they are fetched again from the remote on the standby (the partial clone and
  on-demand fetch of 3.1 apply as they are). **Spaces that exist only on the
  server** get a scheduled `git push` to a mirror on the standby. The index is a
  cache, and the standby rebuilds it itself. **The attachment cache is not
  replicated**, and is fetched again on demand after failover.
- **Failover**: a health check (Cloudflare or a floating IP) notices the primary
  is down and switches to the standby. At failover the **old primary is fenced
  off** (fencing: it can no longer write the state database or push), so that
  two machines never act as primary at the same time.
- Spaces that exist only on the server are pushed on a schedule, so a failover
  may lose commits made after the last push. How long the interval is, and how
  far drafts (in the state database, replicated with the stream) and commits can
  differ, will be written into the contract then.
- **State database backups** (the owner, 2026-11-23): back up daily to somewhere
  outside the server. PostgreSQL uses continuous archiving (WAL), which can
  restore to within a few minutes. Accounts, members, permissions and settings
  can only be recovered from it, and content from the repositories
  ("rebuild from the storage organization" in 3.6). Spaces that exist only on
  the server and have no remote rely on the standby's mirror or the daily backup.
- The owner already has a failover script; look at it first when doing this item.

There are also a few verifications that need no code and that the owner has to
do by hand. They will be requested together with the related milestone:
composition input with a Chinese input method ([editor-and-frontend.md §7.3](../editor-and-frontend.md#73-server-side-rendering-2026-11-21));
the Tang full round trip (phase 0); a real OIDC provider, passkeys in a real
browser and third-party captchas (1.2, 2.8); the real GitHub test of 2.7; and
having a native speaker look at the English UI (1.5).

The known issues that the other milestones "carried forward" and that 5.1 does
not list (performance edge cases, UI details, things that appear only when a
few rare conditions occur together) stay as before: "handle them when touching
the related code". They do not get a milestone of their own.

### Signing in again after a sign-in ends (the owner, 2026-11-24)

When a session ends, no dialog pops up. Instead the user lands on the normal
sign-in page, with one line at the top giving the reason: the sign-in expired;
you signed out on another device; the password was changed; the account was
disabled. For this the server keeps a very small record in the `session_ends`
table (the hash of the session cookie, the user, the reason, the time). It is
kept for 30 days and cleaned up once a day. Only a request carrying such a
cookie receives a `reason` in the 401. An unknown or garbage cookie gets
nothing, and someone who clicks sign out themselves leaves no record. The web
client keeps the previous signed-in user's display name, username or email,
avatar address and last sign-in method (password, passkey or a certain single
sign-on) only in this browser's localStorage, and stores no secrets. The sign-in
page uses this to prefill the account and offer only that one method, and hands
the email to providers that support it as `login_hint`. "Use another account"
returns to a blank sign-in page. When the user signs out on purpose, this memory
is cleared. When the session ended passively, it is kept. For "disabled" it is
neither shown nor used. After signing in the user returns to the original page,
and local drafts are left alone. Disabling a tenant, like disabling a user,
ends all sessions under it on the spot and records them as "disabled", so that
idle cookies are not later recorded as "expired". When the user or tenant is
enabled again, these "disabled" records are deleted. When a 401 comes without a
reason, the web client shows the normal sign-in page, without the reason and
without the remembered account. "Use another account" adds `other=1` to the
address, which stays in effect for the rest of the visit. Every clickable
control on the sign-in page is at least 40px high (phone and tablet). The
`session_ends` table is written directly into the initial migration that creates
the sessions table (v0 needs no upgrade path).

### Notes on the follow-up plan

Phase 4 comes before phase 5, because "eventually usable offline, with desktop
and phone clients" is the goal written down in [overview.md §1](../overview.md#1-goals-and-non-goals), while most of phase 5
fills in gaps in existing features. Inside phase 4, reading comes before writing:
4.1 only adds a change stream and changes no saving rule, so at worst a bad
result means seeing old content offline; 4.2 is what touches saving and
conflicts, and is reviewed on its own; 4.3 has nothing to do with data and runs
in parallel with 4.1; desktop and phone both reuse the web client's offline
ability, with no separate sync written. Phase 5 is ordered by "could it lose
anything" first (5.1 cleans up the known issues that lose history or write wrong
content, 5.2 is the space saving the owner explicitly asked for, 5.3 makes
rolling restarts error-free), then the features whose dependencies are all
there and that can be finished inside this repository alone (5.4 to 5.9, 5.12),
and last the ones that need external accounts, domains, certificates or legal
text (the real-world testing of 5.10, GitHub Pages in 5.11, the trade-offs of
5.13, 5.14 to 5.16). 5.1 and 5.3 have no dependency relation with phase 4 and can
be interleaved with it.

What needs no owner involvement and can be finished inside this repository: all
of 4.1, 4.2, 4.3 and 4.5 in phase 4, and the local-repository part and unsigned
installers of 4.4; in phase 5, 5.1, 5.2, 5.3, 5.5, 5.7, 5.8 and 5.12, the part
of 5.9 other than MCPHub, and the code parts of 5.10 and 5.11 (publishing served
directly by this service).

What the owner has to decide first: the quota rules of 5.4; configuring the
webhook on the GitHub App for 5.6; how to connect MCPHub for 5.9; the test
accounts of 5.10 (a public OAuth app, GHES, Entra ID or Okta); GitHub Pages and
the custom domain of 5.11; the trade-offs of 5.13; the domain, the mail-sending
domain, the terms and the compliance requirements of 5.14; the app store
accounts, signing certificates and release channels of 5.15; the trademark,
domain, KazuhaHub repository and license of 5.16; and the two servers,
Cloudflare or floating IP, and existing failover script of 5.17.
