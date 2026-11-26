# Owner decisions of 2026-11-24

## 17. Owner decisions of 2026-11-24 (collected, placed by milestone)

**Direction of 3.10 (UI restructuring)**

- **The admin console is a separate page**: the left sidebar has the major items
  (Users, Groups, Spaces, Storage & sync, Sign-in & security, Mail, Daily text,
  Settings by layer, Tenants, Usage, Audit, Server status, ...), and inside each
  item top tabs divide the sub-pages; at the top left is "← Back to ⟨space
  name⟩". There will be more and more content later, and this skeleton has to be
  able to hold it.
- **Every list is a table**, designed for hundreds or thousands of rows: one row
  per person (space, group), the columns shown are chosen by the admin and the
  column widths can be dragged, and these live only in the browser; the far right
  holds only the one or two common actions; the rest are in the **separate page**
  opened by "View/Edit" (no dialogs: things like audit are very long), and the
  detail page is divided into tabs (profile, security and sign-in, sessions,
  spaces and groups, usage, audit). Rows per page is 50 by default, with 25/100/200
  as options, paginated on the server; search, filter, sort, and batch actions
  after ticking.
- **Notifications** (general, reserved now): a bell with a red dot, and a
  notification list; first in-app and web notifications (browser push, after the
  user agrees), with email and phone push added later. A space's "validation"
  problems are one kind of these, and **validation no longer sits permanently in
  the sidebar**; later to-do reminders, invitations and mentions all go through
  it.
- **Favourites more visible**: in the list that browses files, when the mouse
  rests on a row, a favourite button fades in on the right (with a transition, not
  abrupt). **Pinning in a folder**: someone with edit permission pins a page at
  the very top of the folder list, and everyone sees the same (a personal pin is a
  favourite).
- **The settings dialog**: the close button at the top right is placed at the
  corner of the whole dialog, not in the title row of the right column.
- **Errors are translated by code**: the web shows Chinese and English messages by
  error code, and falls back to the server's original text only for codes that are
  not in the table; the server's original text is also changed to start with a
  capital letter and end with a full stop; a test checks that every error code has
  a translation. "Checks" is renamed.
- **Phones and tablets**: the page never scrolls horizontally (tables scroll in
  their own box); a toolbar that does not fit folds into "More"; buttons are at
  least 40 pixels; the sidebar is a drawer on phones; dialogs are full screen on
  phones. Every milestone that changes the UI is actually checked at widths 375
  and 768.
- **New user onboarding**: a basic tutorial at first sign-in, which can be skipped
  and can be opened again from help; when the whole site or an organization
  "recommends" or "requires" connecting your own repository, it explains step by
  step why, where to click, what to authorize, and who to ask when stuck.
- **The page header only changes values**: source mode shows only the body; the
  header is edited with a properties panel, where the keys come from the type and
  only values can be changed, and a new property is chosen from those the type
  allows; system fields such as `id` and `created` are read-only; renaming a
  property is a type-level operation that changes all pages at once; raw YAML is
  kept as an advanced mode for admins.

**3.10 part two: admin console (done)**

- The address is `/admin/<major item>/<page>`, and a user has their own page
  `/admin/user/<id>/<tab>`; the old `/admin/manage`, `recycle`, `access` and
  `usage` redirect to the new places. After a GitHub App is created, it returns to
  `/admin/storage/connections`.
- `DataTable` is the table for all long lists: cursor pagination plus rows per
  page; the column visibility/order/width (stored as proportions, not pixels, with
  a minimum width), row height and rows per page are stored only in the browser's
  localStorage (reads and writes are wrapped in try/catch); on a narrow screen the
  secondary columns are folded first by priority, and the rest scroll in the
  table's own box; clicking the header sorts (for columns the server supports);
  ticking plus batch actions; the right end has only "View" and "⋯". The user list
  and the people and space lists of storage management all use it.
- `GET /api/users` was changed to keyset pagination (`cursor`/`next`), with
  filters for search, role, status and last active and three sort orders, each
  with an index (`users_live*`); the counts of passkeys and connected accounts are
  looked up only for the ids on this page. Added `GET /api/users/{id}` and
  `…/memberships`.
- A session records the browser's name and the sign-in method
  (`sessions.user_agent`, `method`). **Site admins** can see the sessions on a
  user's page, end one or all of them (recorded as `signed_out_elsewhere`, audit
  `sessions_ended`), and see and disconnect connected accounts; an organization
  admin always gets 404 on these routes, as if the person did not exist. The
  server does not know where a sign-in came from, so a session has no
  "approximate location".
- Not there yet: the "sign-up approval" page (there is no corresponding UI yet);
  "View as this user" is 3.6, and the button is disabled for now.

**3.10 part three: folders and pages (done, what is missing is at the end)**

- **A folder is a table**: the default view of a folder (the `list` layout in the
  saved views) is a table like a Finder list, reusing `DataTable`: title (bold,
  with a line of light-colored body excerpt below it), subtitle, tags, source,
  created, updated, plus the property columns of the type; clicking the header
  sorts (source sorts by the first source), subfolders are sorted first by default
  and can be switched to be mixed in by name. The visibility, order and width (as
  proportions) of the columns are chosen by the person, and when narrow they fold
  by priority. The filters (tags, source, the choice fields in the type, date
  ranges of created and updated) are behind a "Filter" button (ListFilter icon),
  and active ones are shown as removable tags. When hovering over a row, the star
  at the right end fades in over about 150ms; on touch screens it is always there
  but faint. On a phone it is not a table but a list of the title plus one line of
  small text (time, tags, source), and sorting and filtering are merged into one
  button. Cards, board and reading in sequence are still switched above as before;
  the sort dropdown in the top bar is still there on desktop, so the sorting and
  filtering of the 2.x views can always be found.
- **Where personal preferences go**: the columns, widths, order, and whether
  folders are mixed in are read and written through a `PrefsStore` interface, and
  **for now only the browser's localStorage is implemented**, not in the database;
  the shared defaults are the saved views already in the space repository
  (`hidden` is the default when there is no personal choice). Writing to the
  person's own personal space `.petalbook/prefs.yml` (batched, quiet commits, not
  appearing in page history, falling back to localStorage when there is no personal
  space or not signed in) was done in part four (see below).
- **Icons and colors**: for a folder they are in `folders: {path: {icon, color}}`
  of `space.yml`, and move with the folder in the same commit (`rebind` moves the
  whole entry); for a page they are `icon` and `color` in the page header (system
  fields, not properties of the type); a type can give pages a default icon
  (`x-icon` and `x-color` at the top level of the type file). An icon is a Lucide
  name or an emoji; there are eight colors (red, orange, amber, green, cyan, blue,
  purple, pink), each with a tuned light and dark version, and icons use
  `currentColor`. The picker searches by name, by Lucide's tags, and by the Chinese
  aliases of a few hundred common icons, with recently used first and then the
  categories; **the whole Lucide set and its tags are one separate chunk**, fetched
  from this server only when the picker is opened or an uncommon icon has to be
  drawn, and never from the internet. The sidebar, lists, tables and page headers
  all show them; the context menu of folders and pages has "Icon and colour".
- **Source is a list**: like tags (when read, a single string counts as one value,
  because people write YAML by hand). In the index it is a row of the `props` table
  (key `source`), so filtering, sorting (`prop:source`) and the board's columns
  (`key=source`) need no extra code; `pages.source` stores a JSON list for list
  reads; sources that have been used are given by `GET …/sources`. The presets are
  in `sources:` of `space.yml`, and when not written the default of the UI language
  is used (Original, Internet, Book, Film, Music, Conversation); the admin changes
  them with the button next to the source input. The input box and tags are the
  same `ChipInput`: used ones first, then presets, Tab completes the first one,
  arrow keys or the mouse choose, Enter adds, Backspace in an empty box removes the
  last one, Esc closes; on a phone the list is below the input box. The credit line
  of a daily text joins multiple sources with "、" (Chinese) or ", " (English).
- **The header only changes values**: source mode shows only the body; an admin
  (`manage`) can tick "Edit the header as YAML too (advanced)", and the original
  error protection stays as before. The keys of the properties panel come from the
  type, and only values can be changed; `id` and `created` are shown read-only;
  renaming a property is a type-level action (`POST …/types/{name}/rename-field`,
  `manage` only), which in one commit changes the type file and all pages that use
  the key.
- **Not there yet**: dragging table rows works only on the title cell; when the
  page header icon is saved in the editor and there is also an unsaved draft, it
  goes through one conflict prompt; the board of a folder with no type groups by
  source, but a card cannot be dragged to change the source (the value belongs to
  the page itself).

**3.10 part four: notifications, onboarding, personal preferences (done, what is missing is at the end)**

- **The notification center is a general, reserved-ahead system.** The table
  `notifications (id, user_id, tenant_id, kind, ref, payload, created, read_at)`,
  with the index `(user_id, id DESC)` for the list (one range scan, keyset
  pagination) and the partial index `(user_id) WHERE read_at IS NULL` for the
  unread count (counting up to 100, with the badge showing "99+"); `id` is the
  millisecond time plus a sequence number, ordered by time. The server **does not
  store sentences**: a row has only the kind, the thing it points to (a space id)
  and the facts as JSON, and the web writes the sentence by kind and by the
  person's language (Chinese and English). The kinds are a registry in code
  (`store.NotifyKinds`): `problems` (derived, has no rows), `invite` (added to a
  space), `sync` (the remote of a space you own needs a person to look at it, or
  is fine again; merely being offline recovers by itself and does not notify),
  `mention`, `todo` (only reserved, nothing produces them), and one kind for
  each event of an account or a space that tells a person what someone else did
  (`space_taken_over`, `space_transferred`, `account_changed`, `password_set`,
  `email_released`; there is no catch-all `system` kind with the event in the
  payload, which the filter could not tell apart). Such an event is written by
  `notify.WithAudit`: its audit row and its notifications in one transaction,
  from one definition. The web's list of kinds is `web/src/i18n/notifyKinds.json`,
  written by a Go test, and a web test needs each kind to have its text in both
  languages. The daily cleanup: those older than half a year, and read ones
  older than 30 days, are deleted, and each person keeps at most the latest 500;
  they are deleted along with the person when the person is deleted.
- **A space's problems are not copied to every member.** When the list and the
  unread count are asked for, they are computed on the spot from the indexes of the
  spaces this person can write to (at most 50, disabled ones not counted): if the
  space is open, its index is read (two grouping queries, reused for 20 seconds);
  if it is not open, what it said the last time it was open or was closed is used
  (in memory; after a restart it waits until the space is opened); **a space is
  never opened for this**. Each person has one read cursor per space
  (`notification_seen`, which stores the signature of "what the problems looked
  like when it was read"): if the problems change it becomes unread again, and when
  all are fixed it is gone.
- **API**: `GET /api/notifications` (paginated, by kind, unread only),
  `GET /api/notifications/unread`, `POST …/read`, `POST …/read-all`, all "your
  own": looked up by the person and tenant in the session, and no route accepts
  someone else's id; tokens and the admin's "view as" cannot get in. All are in the
  isolation table (`TestNotificationsAreTheCallersOwnAcrossTenants`).
- **UI**: a bell at the right end of every page's top bar, with a red dot (with a
  transition) when there is something unread. On desktop and tablet clicking it
  opens a small panel (the latest 5: title, body, time, unread dot, "Mark all as
  read" and "See all notifications →"); on a phone tapping the bell goes straight
  to the full-screen notifications page (`/notifications`: filter by type, that
  is all, unread, problems, invitations, mentions of me, sync, and it can scroll
  back to earlier ones). Clicking an item jumps to where it points and marks it
  read. The bell asks for the unread count once a minute, and only when the window
  is in front.
- **Browser notifications**: a switch in the preferences; before it is turned on,
  it first explains what it will do, and only after the person clicks "Continue"
  does the browser show its own authorization box; there are explanations for being
  refused, for the browser not supporting it, and for not being on https. An open
  tab that finds the unread count has gone up while nobody is looking at it pops up
  one (the Notifications API). The choice is stored in this browser (the
  authorization belongs to the browser anyway), not in the personal preferences.
  **Real push (Web Push, VAPID) has only its name reserved and is not done**: it
  needs the server's keys and a subscription table, and is only useful when all the
  tabs are closed.
- **Onboarding**: a few steps that appear automatically at first sign-in (new
  page, the editor, favourites, search; admins get one more step: the daily text is
  off by default, and where to turn it on), which can be skipped at any time and
  then opened again from "Help" at the bottom of the sidebar; full screen on a
  phone. "Done" or "Skipped" is remembered per person in
  `pb.onboarding.<user id>`, going through PrefsStore (in the personal space if
  there is one, otherwise in the browser), and not in the database. When the whole
  site or an organization requires your own repository (`storage.remotes.require`
  of 3.1), the onboarding has one more step, "Connect your own repository": why
  (before the recommended due date, or already required and the space is
  read-only), what to click, what to authorize (only this one repository), and who
  to ask when stuck, listed step by step; when it is required, the notice at the top
  of the space says not only "read-only" but also "Still to do: …" and "Step by
  step", and after the account is connected the first step is ticked.
- **Personal preferences are in the personal space**: `GET`/`PATCH /api/prefs`,
  and the file is `.petalbook/prefs.yml` in the person's own personal space (the
  table's columns, order and widths, whether folders are mixed in, the onboarding
  state, ...). The route carries neither a space nor a person, and is always the
  caller's own; tokens and the admin's "view as" cannot get in; this file is not a
  page, not an attachment, and not the space's settings: no route can read it (a
  path with a dot is always 404), and the index, the offline sync catalog and
  `SettingsStamp` all ignore it, and an admin exporting a personal space does not
  include it. A write first goes into the server's memory (a read sees it at once),
  and **3 minutes after the first write of each burst everything is written
  together**, as one small commit "Update prefs.yml", signed with the person's own
  name, and not appearing in the history of any page; when the server shuts down,
  what has not been written is written. The web side reads once and caches: it
  fetches once after signing in and keeps it in the browser, and all later reads
  read this copy synchronously; a write first lands in the browser, and then after
  2 seconds of quiet (at most 15 seconds) it is sent to the server in a batch; with
  no personal space, not signed in, or offline, only the browser is used, and it is
  sent later when back online; when a different person signs in, the previous
  person's copy is cleared, and the first sign-in merges the columns and widths
  that an old version left in the browser into the file.
- **Not there yet**: (1) A space's problems are known only when the space is open
  or was just closed, and after a restart it waits until it is opened, so a space
  nobody has opened yet will not remind; for full coverage, a per-space summary
  would have to be written when the index is updated. (2) "Invited to join an
  organization" has no corresponding kind: today an invitation creates an account,
  and the recipient has no account yet; add it when "invite someone who already
  has an account" exists. (3) Mentions, to-dos and system notifications have only
  their kinds reserved. (4) Web Push, email and phone push are not done. (5) The
  pages of the admin console have no bell at the top. (6) Personal sorting (the
  kind clicked on the header) is still in the address and the saved view, and does
  not go into the preferences. (7) When an admin exports a personal space with
  "history" ticked, the repository's history bundle contains the commits of
  `prefs.yml`: this is the old way in which a site admin reads any file, and no
  scrubbing is done. (8) "Who to ask when stuck" is for now a generic sentence, and
  there is no organization admin contact to show yet. (9) A server crash loses at
  most the last 3 minutes of preferences not yet written to the file (the copy in
  the browser is still there).

**The sign-in page after a sign-in expires** (a separate small PR): above the
ordinary sign-in page it explains the reason (expired, signed out on another
device, password changed, deactivated); the account is already filled in, shown
according to this person's last sign-in method (a password box, a passkey button,
"Continue with X" with the email carried over); "Use another account" goes back to
a blank sign-in page; after signing in it returns to the original page. The
remembered account is stored only in the browser, cleared on an active sign-out,
and not shown when deactivated. The server keeps a record of the reason for an
ended session for 30 days.

**3.6 additions**

- **View as a user** (like Canvas's student view): read-only, a banner at the top,
  audited every time; a tenant admin can only see the organization's spaces as a
  member of their own organization, and never sees personal spaces; it does not
  include the person's drafts, tokens, sessions, or connected accounts.
- **Sessions and connected accounts**: a **site admin** can see them, kick them
  offline and disconnect them on the user detail page; an organization admin can
  always manage only the organization's own things, and cannot manage or audit
  other tenants or anyone's personal area.
- **Logs**: the core audit (sign-in and security, admin actions, permissions,
  deletions, exports, views) is always on and no one can turn it off; the optional
  categories (which page was read, downloads, every successful sign-in, API calls)
  are configured by "site default/enforced → tenant". The site admin sets automatic
  cleanup: older than X days, more than Y records, and between the two chooses "or"
  (delete when either is met) or "and" (delete only when both are met); the core
  audit has a minimum retention period (for example 90 days).
- **Enforcing for a single organization** is a general rule of all settings: the
  site default, the site enforcement, and the site admin setting a value directly
  for one organization and locking it (`setBy`, `siteLock`).
- **Defaults**: at most **3** spaces per person by default (it was 20); an
  organization is not allowed to create its own by default; "at most how many
  created per hour on the whole site" is not enabled by default, and the
  configuration is kept.

**Database**: before the official deployment, anything unreasonable (inefficient,
or can be optimized) is changed directly, because now the cost is lowest; after 3.5
is merged, do one overall review (indexes, column types, splitting and merging of
tables, the query plans of both databases at large data volumes), and v0 writes no
migration compatibility.

**Phase 5 addition**: **to-dos** in the sidebar, with in-app, email and phone
reminders (through the notifications above); a calendar to be connected later
(CalDAV, Google Calendar). Not urgent, and not scheduled.


**3.10 known issues** (left by the review, not blocking, 2026-11-24):

- When the editor has a draft stored on the server, changing this page's icon with
  the icon picker pops up one conflict prompt (the icon endpoint's answer carries
  no `draft`); nothing is lost. A reply that lacks `draft` could be counted as
  "the draft did not change", or the page could be fetched again after the icon is
  saved.
- When the `fp` part in the rename log does not match the page count of `f` (only
  when a line in the middle is damaged), log one error and still replay according
  to the pages found; missing pages keep the old key, and no value is lost.
- After a conflict is resolved through `rebaseOnto`, the "Commit" the user clicked
  is not restored, and has to wait for the server to be idle and commit
  automatically.
- The base-rewriting log (`rebase.go`) is still read at 1 MiB per line.
- Other small items left by the review of parts 2-4 (the session device info has
  no location, organization invitations and mentions have only their kinds
  reserved, the admin console has no bell yet, in a table only the title cell can
  be dragged, the board grouped by source cannot drag cards, and when the server
  crashes personal settings lose at most the changes of the last 3 minutes not yet
  written to the file, with a copy still in the browser) are left to the
  corresponding milestone or a cleanup round.
