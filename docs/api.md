# HTTP API

Status: milestone 1.3 (a space can follow a remote Git repository; page
history). Before it: 1.2 (sign-in, several spaces, drafts kept on the server).
Endpoints and fields will be added in later milestones; what is listed here is
not expected to change shape.

The web app talks to the server only through this API.

## Conventions

- JSON in, JSON out, UTF-8. Request bodies need `Content-Type: application/json`.
- Times are RFC 3339 strings in UTC (`2026-11-21T08:30:00Z`). `created` in a
  page's header is a date (`2026-11-21`).
- Errors: a non-2xx status and `{"error": "<code>", "message": "<for people>"}`.
  Codes: `not_found` (404), `invalid` (400; also 405, 413 and 415),
  `conflict` (409), `exists` (409), `unauthenticated` (401: not signed in, or
  wrong credentials), `forbidden` (403: cross-site requests, see below, and
  actions the signed-in user may not take that are not a matter of a role in a
  space), `no_permission` (403: a member whose role in the space lacks the
  permission the request needs; the message names it), `space_disabled` (403: an
  administrator has disabled the space, see "Administration"), `rate_limited` (429, with a
  `Retry-After` header in seconds), `unavailable` (503: the space follows a
  remote repository that cannot be reached or is paused, see "Remote
  repository"; nothing was changed), `internal` (500).
- **Cross-site requests.** These rules keep other web sites from using a
  browser to change pages, and in loopback-only mode from reading them. They
  apply to every request. With `--insecure-no-auth` a site that controls a DNS
  name can still make a browser read pages (DNS rebinding); do not use that
  flag on a network you do not trust until sign-in exists.
  - Every `POST`, `PUT`, `DELETE` and `PATCH` must send
    `Content-Type: application/json`, even with no body, or the answer is
    `415 invalid`. The exceptions are uploading a file (see "Files") and an
    import (see "Import"), which must send
    `Content-Type: application/octet-stream`; a plain HTML form
    cannot send that type either.
  - A request whose `Sec-Fetch-Site` header is present and is neither
    `same-origin` nor `none` gets `403 forbidden`.
  - A request carrying an `Origin` header gets `403 forbidden` unless that
    origin is accepted. An origin is accepted if the administrator listed it
    with `--allow-origin`. In loopback-only mode (the default: the server
    listens on a loopback address) a plain `http` loopback origin
    (`localhost`, `127.0.0.1`, `[::1]`, `*.localhost`) on the same port as the
    request's `Host` is accepted too. With `--insecure-no-auth` there is no
    implicit own origin: browsers must be listed with `--allow-origin`.
    In accounts mode the origin of `--public-url` is accepted as the server's
    own, on any listen address.
  - In loopback-only mode a request whose `Host` is neither a loopback name
    nor the host of an allowed origin gets `403 forbidden`.
  - Requests with no `Origin` and no `Sec-Fetch-Site`, such as `curl` or
    scripts, are not refused by these checks.
  - One exception: `GET /api/auth/oidc/callback` is reached by a redirect from
    the identity provider, so the `Sec-Fetch-Site` and `Origin` rules do not
    apply to it. It changes nothing unless its `state` matches a sign-in this
    browser started.
- A **space** is one tree of pages backed by one Git repository. Every page URL
  carries the space id. In single-user mode there is exactly one space, with
  the id `default`; in accounts mode a space id has the same form as a page id.
- A **page id** is the `id` from the page's header: 10 characters of Crockford
  base32 (`0-9`, `A-Z` without `I L O U`), e.g. `01JD3K8Q7Z`. It never changes.
- A **rev** identifies the exact bytes of a page file: the Git blob id of the
  file content (40 hex characters). Clients send the rev they started from
  when saving; the server refuses the save if the file has changed since.
- Folder paths are relative to the space root, `/`-separated, no leading or
  trailing slash. The root folder is the empty string.

## Modes

The server runs in one of two modes, chosen at start-up.

- **Single-user** (`serve --space DIR`): one space, the directory given; no
  sign-in. Every request acts as the built-in user. The server listens on a
  loopback address only unless `--insecure-no-auth` is given. The sign-in,
  account, user, invite and space-management endpoints answer `404`.
- **Accounts** (`serve --data DIR`): users sign in. Spaces live in
  `DIR/spaces/<space id>/`. Every endpoint except `GET /api/meta`,
  `GET /api/session` and the ones under "Signing in" needs a session and
  answers `401 unauthenticated` without one. A space the user is not a member
  of answers `404 not_found`, the same as a space that does not exist, unless
  the user is an administrator: an administrator may do everything in every
  space (see "Members"). A member whose role lacks the permission a route needs
  answers `403 no_permission`, a code of its own: it is never confused with the
  cross-site refusal, which is `403 forbidden`.

## Meta

### `GET /api/meta`

No session needed. `spaces` lists the spaces the caller can open (empty when
not signed in), in the shape of `GET /api/spaces`.

```json
{
  "version": "dev",
  "maxUpload": 10485760,
  "maxImport": 104857600,
  "spaces": [{"id": "default", "name": "My notes", "role": "owner", "permissions": ["read", "write", "members", "manage"], "personal": true, "sync": "local", "inbox": "Inbox"}]
}
```

`maxUpload` is the largest file, in bytes, the server accepts as an upload
(`--max-upload`, 10 MiB by default, at most 256 MiB). `maxImport` is the same for an import
(`--max-import`, 100 MiB by default, at most 512 MiB).

## Session

### User

```json
{"id": "01JD3K8Q7Z", "username": "kazuha", "name": "Kazuha", "email": "", "role": "admin", "disabled": false, "created": "2026-11-21T08:30:00Z"}
```

`role` is `admin` or `member`. `username` is 2–32 characters of `a-z`, `0-9`,
`.`, `_`, `-`, starting with a letter or digit; it is stored in lower case and
upper case in a request is folded. `email` may be empty; it is never used to
match an account.

### `GET /api/session`

Never an error; no session needed. `Cache-Control: no-store`.

```json
{
  "mode": "accounts",
  "user": null,
  "setup": false,
  "methods": {"password": true, "oidc": {"name": "Company SSO"}}
}
```

- `mode`: `single` or `accounts`.
- `user`: the signed-in user, or `null`. In single-user mode it is always the
  built-in user: `{"id": "local", "username": "local", "name": "Local user", "role": "admin", …}`.
- `setup`: `true` while there is no user at all; the app then shows the
  first-run page.
- `methods.oidc` is `null` when no provider is configured. `methods` is absent
  in single-user mode.

## Signing in

Accounts mode only. The session is a cookie (`HttpOnly`, `SameSite=Lax`,
`Secure` when `--public-url` is `https`); its value means nothing to the
client. A session ends after 30 days without use and at the latest 90 days
after sign-in. Changing a password ends the user's other sessions; disabling a
user ends all of them.

A password is at least 8 characters and at most 1024 bytes; nothing else is
required of it.

### `POST /api/setup`

Creates the first administrator. Works only while there are no users; after
that `409 exists`.

```json
{"token": "…", "username": "kazuha", "name": "Kazuha", "password": "…"}
```

`token` is the setup token the server prints in its log at start-up while
there are no users (it also prints a link of the form
`<public url>/setup#<token>`). Wrong token: `403 forbidden`. `name` defaults to
the username. `201` with `{"user": {…}}` and a session cookie. The user gets a
personal space.

### `POST /api/login`

```json
{"username": "kazuha", "password": "…"}
```

`200` with `{"user": {…}}` and a session cookie. Wrong username or password,
or a disabled user: `401 unauthenticated`, with the same message in every
case. Too many attempts from one address or against one username:
`429 rate_limited`.

### `POST /api/logout`

`204`; ends the session if there is one.

### `POST /api/auth/oidc/start`

Begins sign-in with the configured OpenID Connect provider. `404` when none is
configured.

```json
{"intent": "login", "token": "", "return": "/s/01JD3K8Q7Z"}
```

- `intent`: `login` (default); `setup` (create the first administrator;
  `token` is the setup token); `invite` (create an account; `token` is the
  invite token); `link` (attach the identity to the signed-in user; needs a
  session).
- `return`: a path inside the app to end up on. Anything that is not a plain
  absolute path (`/…`, not `//…`, no backslash) is replaced by `/`.

`200` with `{"url": "https://idp.example/authorize?…"}` and a short-lived
cookie that ties the attempt to this browser. The client navigates to `url`.

### `GET /api/auth/oidc/callback`

The provider redirects here. The server finishes the sign-in and answers with
a redirect (`303`) into the app, never JSON:

- success: to the `return` path, with a session cookie (for `link`, the
  session stays as it is);
- failure: to `/login?error=<code>` (for `link`: `/settings?error=<code>`).
  Codes: `idp_error` (the provider refused or the answer did not verify),
  `expired` (the attempt is unknown, used, or older than 10 minutes),
  `no_account` (nobody has this identity and accounts are not created
  automatically), `disabled`, `identity_in_use` (`link`: another user has it),
  `invite_invalid`, `setup_invalid`.

An identity is the pair of the provider's issuer and the `sub` claim. With
`intent: "login"`, an identity nobody has creates a member account only when
the server runs with `--oidc-auto-create`. New accounts take their name from
the `name` claim and their username from `preferred_username` (or the part of
`email` before `@`), made valid and unique.

## Account

Accounts mode, signed in.

### `GET /api/account`

```json
{
  "user": {…},
  "hasPassword": true,
  "identities": [{"id": "01JD3M2A4C", "provider": "Company SSO", "label": "kazuha@example.com", "created": "2026-11-21T08:30:00Z"}]
}
```

`label` is the `email` or `preferred_username` claim seen when the identity
was attached, for display only.

### `PATCH /api/account`

`{"name": "…", "email": "…"}`, each optional. `200` with `{"user": {…}}`.

### `POST /api/account/password`

`{"current": "…", "new": "…"}`. `current` is required when the user has a
password and must be left out when they have none (an account made through
single sign-on). Wrong `current`: `403 forbidden`. `204`.

### `DELETE /api/account/identities/{id}`

`204`. Refused with `409 conflict` when it would leave the user with no way to
sign in (no password and no other identity).

## Users and invites

Accounts mode. Everything here needs the `admin` role (`403 forbidden`
otherwise) except the two invite endpoints marked public.

### `GET /api/users`

`{"users": [{…}]}`, ordered by `created`. Without parameters, every user.
`?q={text}` keeps the users whose `username` or `name` contains the text,
ignoring case (as the name filter of the administration's list of spaces does);
`?limit={n}` (1 to 200) returns at most that many, the oldest first. Either may
be sent alone; `400 invalid` for a `limit` outside the range. `?enabled=1`
leaves out the disabled accounts, before the limit counts (`enabled=0`, the
default, keeps them; `400 invalid` for anything else). A picker that looks for
one person sends `q` and `limit`, so it never loads every account.

### `PATCH /api/users/{id}`

`{"role": "member", "disabled": true}`, each optional. `200` with
`{"user": {…}}`. `409 conflict` when the change would leave no enabled
administrator, or when an administrator tries to disable themself.

An administrator cannot open another user's space through the API. Whoever
can read the server's disk can read every file.

### `POST /api/invites`

`{"role": "member", "days": 7}`, both optional (`days` 1–30). `201` with

```json
{"id": "01JD3M9RTE", "token": "…", "role": "member", "created": "…", "expires": "…"}
```

`token` is shown this once; the server keeps only a hash. The link to send is
`<public url>/invite#<token>`. An invite works once.

### `GET /api/invites`

`{"invites": [{"id", "role", "created", "expires", "createdBy"}]}`: the ones
neither used nor expired.

### `DELETE /api/invites/{id}`

`204`.

### `POST /api/invites/check` (public)

`{"token": "…"}`. `200` with `{"role": "member", "expires": "…"}`, or
`404 not_found` for a token that is unknown, used or expired.

### `POST /api/invites/accept` (public)

```json
{"token": "…", "username": "tang", "name": "Tang", "password": "…"}
```

`201` with `{"user": {…}}` and a session cookie; the user gets a personal
space. `404 not_found` for a bad token, `409 exists` when the username is
taken (the invite stays usable).

## Spaces

### Space

```json
{"id": "01JD3K8Q7Z", "name": "My notes", "role": "owner", "permissions": ["read", "write", "members", "manage"], "personal": true, "sync": "local", "inbox": "Inbox", "disabled": false}
```

`role` is the caller's role in the space: `owner`, `editor` or `viewer`, and
empty for an administrator who is not a member. `permissions` is what the
caller may do in the space, so a client shows or hides controls by it and never
by the name of a role:

| Permission | Allows |
|---|---|
| `read` | seeing pages, history, attachments and search |
| `write` | creating, changing, moving and deleting pages and folders; tags, attachments, imports, drafts |
| `members` | adding, changing and removing members |
| `manage` | renaming the space, its inbox, its remote and sync, rebuilding the index |

An `owner` has all four, an `editor` `read` and `write`, a `viewer` `read`.
In single-user mode the one user has all of them.
`personal` marks the space made together with the account. `sync` is `local`
(the repository exists only on the server) or the `state` of its remote (see
"Remote repository"). `inbox` is the folder that quick notes go to: a folder
path, never empty. It is only a name: the folder is made when the first page
is created in it, and changing the setting moves nothing. In single-user mode
it is set with `--inbox` (default `Inbox`). `disabled` is true when an
administrator has switched the space off (see "Administration"): it stays in
its members' lists, with the permissions they would have, but every request
that opens it answers `403 space_disabled`.

### `GET /api/spaces`

`{"spaces": [{…}]}`: the caller's spaces, the personal one first, then by
name.

### `POST /api/spaces`

Accounts mode. `{"name": "Tang", "inbox": "收件箱"}`: `name` is 1–80
characters; `inbox` is optional (default `Inbox`) and must be a valid folder
path. `201` with the space.
The server makes the Git repository. The caller is its owner. `403 forbidden` when
the caller already owns as many spaces as the server allows (`--max-spaces`,
20 by default).

### `PATCH /api/spaces/{space}`

Accounts mode, needs `manage`. `{"name": "…", "inbox": "…"}`, each optional; at
least one must be given. `200` with the space.

## Members

Accounts mode; single-user mode answers `404`. The people who belong to a
space, each with a role (`owner`, `editor` or `viewer`, see "Space"). A space
always keeps at least one owner. A personal space (`personal: true`) belongs to
its person alone and never has other members. An administrator is not a member
of the spaces they open, so they are not listed here and not in `GET /api/spaces`,
but may use these endpoints as if they were an owner.

### Member

```json
{"userId": "01JD3K8Q7Z", "username": "tang", "name": "Tang", "role": "editor", "you": false}
```

`you` is true for the caller.

### `GET /api/spaces/{space}/members`

Needs `read`. `{"members": [{…}]}`: owners first, then by name.

### `POST /api/spaces/{space}/members`

Needs `members`. `{"username": "tang", "role": "editor"}`. `201` with the member.
`404 user_not_found` when no enabled user has that username; `409 exists` when
they are a member already; `409 personal_space` for a personal space;
`400 invalid` for a role that is not one of the three.

### `PATCH /api/spaces/{space}/members/{user}`

Needs `members`. `{"role": "viewer"}`, `{user}` being the member's `userId`.
`200` with the member. `404 not_found` when they are not a member;
`409 last_owner` when it would leave the space without an owner. The change
applies to the member's very next request.

### `DELETE /api/spaces/{space}/members/{user}`

Needs `members`, except that a member may remove themself (leave the space)
with no more than `read`; leaving only touches the membership, so it works in a
space an administrator has switched off too (removing someone else does not: that
goes through the space, which cannot be opened). `204`. `409 last_owner` when no
other owner who can sign in would be left (a disabled user cannot be the one
remaining owner; the same goes for `PATCH`): make someone else an owner first.
The removed member's very next request to the space answers `404 not_found`.

## Remote repository

Accounts mode; needs `manage` (`403 no_permission` for anyone else who can
open the space). A space either keeps its repository on the server only, or
follows a remote: then the remote branch is the truth.

- The server fetches the remote every minute (`--pull-interval`) and moves
  the space forward to it. Changes made on the remote directly (on GitHub,
  from another clone) show up after that, in pages, lists and search.
- Every change to a file made through this API (creating, saving, committing
  a draft, moving, deleting) is made on top of the remote's current state and
  pushed before the request is answered. If the remote moved in between, the
  change is made again on the new state; a save or commit whose page changed
  on the remote answers `409 conflict` exactly as it does for any other
  change to the file. The server never forces a push and never rewrites or
  deletes anything on the remote.
- When the remote cannot be reached, those requests answer `503 unavailable`
  and change nothing. Drafts are not affected: typing keeps being stored, and
  the server commits a draft once the remote is back.
- If the remote's history was rewritten (the branch no longer contains the
  last commit the server saw), the space is paused: reads keep working from
  what the server has, writes answer `503 unavailable`, until the owner
  accepts the remote's new history.
- An empty folder exists only on the server: Git does not carry it.

Only `https://host/path` and SSH addresses (`git@host:path`,
`ssh://[user@]host[:port]/path`) are accepted, without a password in them, and
only for hosts the administrator allows (`--remote-host`, repeatable; by
default `github.com`, `gitlab.com`, `codeberg.org`, `bitbucket.org`; `*`
allows any).

### Remote

```json
{
  "url": "git@github.com:kazuha/notes.git",
  "branch": "main",
  "credential": {"type": "ssh", "publicKey": "ssh-ed25519 AAAA… petalbook-01JD3K8Q7Z", "fingerprint": "SHA256:…"},
  "state": "ok",
  "lastSync": "2026-11-21T08:30:00Z",
  "error": "",
  "hostKey": "SHA256:…",
  "rewritten": null
}
```

- `credential.type`: `ssh` (a key pair the server made for this space; the
  private key never leaves the server and is never returned; `publicKey` is
  what to add to the repository as a deploy key with write access) or `token`
  (`{"type": "token", "username": "x-access-token"}`; the token is never
  returned).
- `state`: `ok`; `offline` (the last attempt could not reach the remote;
  `error` says why, in words fit to show); `rewritten` (paused, see above;
  `rewritten` is then `{"lastGood": "<commit>", "remote": "<commit>"}`);
  `error` (anything else that needs attention: the credential is refused, the
  host key changed, the clone is not in the state the server left it in).
- `lastSync`: when the server last saw the remote (absent before the first).
- `hostKey`: for SSH, the fingerprint of the host key trusted on first
  connection, to compare with what the host publishes.

Secrets are stored encrypted with a key kept in the data directory
(`secret.key`, or `PETALBOOK_SECRET_KEY`), and are passed to Git through its
environment, never on a command line, in a URL, in Git's configuration or in
a log.

### `GET /api/spaces/{space}/remote`

`200` with the remote, or `404 not_found` when the space is local.

### `POST /api/spaces/{space}/remote/key`

Makes (or returns, if there is one already) the SSH key pair of this space,
so the public key can be added to the repository before connecting. `200`
with `{"publicKey": "…", "fingerprint": "…"}`. With `{"rotate": true}` a new
pair replaces the old one (only while the space is local).

### `PUT /api/spaces/{space}/remote`

```json
{"url": "https://github.com/kazuha/notes.git", "branch": "main",
 "credential": {"type": "token", "username": "x-access-token", "token": "…"}}
```

`branch` defaults to `main`. For `ssh` the credential is `{"type": "ssh"}`
(the key from `…/remote/key`). Leaving `credential.token` out while the space
already has a token keeps it. The server connects before answering:

- the remote has no such branch (a new, empty repository): the space's
  history is pushed to it;
- the space is pristine (no page and no folder was ever made in it): the
  space takes the remote's content and history;
- one side's history contains the other's: the one behind is moved forward;
- otherwise `409 conflict`: both have content of their own, and the server
  will not merge two histories. Make a new space for that repository.

`200` with the remote. `400 invalid` for an address that is not accepted;
`403 forbidden` for a host that is not allowed; `503 unavailable` when the
remote cannot be reached or refuses the credential (nothing is changed, and
`message` says which).

### `DELETE /api/spaces/{space}/remote`

`204`. The space becomes local, with everything it has; the credential is
deleted. Nothing is changed on the remote.

### `POST /api/spaces/{space}/remote/sync`

Fetches now. `200` with the remote.

### `POST /api/spaces/{space}/remote/accept`

`{"commit": "<the remote commit shown in rewritten.remote>"}`. Accepts a
rewritten history: the space moves to that commit. `200` with the remote;
`409 conflict` if the remote is no longer at that commit. Drafts are kept;
those whose page changed are stale and are shown as such.

## Pages

### Page summary

Used in listings. `title`, `subtitle` and `source` are absent when the page
has none. `source` says where the text comes from (a person, a book, an
address); it is free text from the header field of the same name.
`label` is always present: the title, or for an untitled page the first line
of the body cut to about 30 characters (`untitled` says which). It is an
empty string for an untitled page with an empty body. `created` is absent
when the header has none. `updated` is when the file was last written.
`excerpt` keeps the line breaks of the text as single `\n`; blank lines
between paragraphs are not kept.

`type` is what the header says (`""` when it says nothing). `effectiveType` is
the type the page has: the header's, else the type of the nearest folder
above the page that is bound to one (see Page types), else `""`. It names a
type; whether a type of that name exists is not looked at.
`props` and `rev` are there only when the listing was asked for them with
`props=1` (the table view does): `props` is an object of the header's keys
that are not built in (everything except `id`, `title`, `subtitle`, `source`,
`tags`, `created` and `type`), in header order, `{}` when there are none, and
`rev` is the page's rev, which a change of a property needs. Only values a
table cell can show are in it: text (cut to 300 characters), numbers, true
and false, and lists of those (the first 100 elements). A nested object, a
null, blank text and an empty list are left out, as is anything inside a list
that is not a plain value. Without `props=1` the summary is as small as it
was.

`body` is there only when the listing was asked for it with `body=1` (the
continuous reading does): the page's Markdown as the file has it now, without
the header, `""` for a page with no body. See `GET …/pages`.

`partialProps` comes with `props` and lists, in header order, the keys whose
value in `props` is not exactly what the file holds: text that was cut, a list
that was cut or lost an element, a value that is an object (it has no entry in
`props` at all), a number JSON cannot say, a key beyond the first 200, and any
value that would not come back the same through JSON and the browser: an
integer beyond 2^53, a number not written the way the server writes it
(`0x10`, `1.0`, `+5`, `1e3`, `.5`), a value with a tag of its own (`!custom x`),
a bool not written `true` or `false`. A text, a bool or a number written the
canonical way, and a date written `YYYY-MM-DD` (alone or in a list; a list of
dates is written back as dates), are whole. One such element
makes the key of a list partial. It is
left out when there are none. Such a key must not be written back from the
copy: a client shows it as it is and leaves it to be changed by opening the
page. A key whose value is null, blank text or an empty list has nothing to
lose and is not listed.

```json
{
  "id": "01JD3K8Q7Z",
  "path": "Tang/暮雪.md",
  "folder": "Tang",
  "title": "暮雪",
  "subtitle": "…",
  "source": "…",
  "label": "暮雪",
  "untitled": false,
  "tags": ["玉兰", "黄昏"],
  "type": "",
  "effectiveType": "文案",
  "created": "2025-12-03",
  "updated": "2026-11-21T08:30:00Z",
  "excerpt": "first 400 characters of the body as plain text",
  "props": {"status": "draft", "points": 3, "labels": ["a", "b"]},
  "rev": "3b18e512dba79e4c8300dd08aeb37f8e728b8dad"
}
```

### Page

Returned by get, create, save and commit. `meta` is the header as an object,
in file order, including fields the server does not know about; `id` is in it.
`body` is the Markdown after the header. `header` is the text of the header
exactly as it is in the file (the lines between the two `---` lines, each
ending in `\n`), for showing the file as it was written; it is always the
file's, and what a save writes is decided by `meta` alone. `GET` adds `draft` when the caller
has one for the page (see Drafts); `meta` and `body` are always the file's.

```json
{
  "id": "01JD3K8Q7Z",
  "path": "Tang/暮雪.md",
  "folder": "Tang",
  "rev": "3b18e512dba79e4c8300dd08aeb37f8e728b8dad",
  "updated": "2026-11-21T08:30:00Z",
  "meta": {"id": "01JD3K8Q7Z", "title": "暮雪", "tags": ["玉兰"], "created": "2025-12-03"},
  "header": "id: 01JD3K8Q7Z\ntitle: 暮雪\ntags: [玉兰]\ncreated: 2025-12-03\n",
  "body": "…\n",
  "effectiveType": "文案"
}
```

`effectiveType` is the type the page has: the header's `type`, else that of
the nearest folder above it bound to a type, else `""` (see Page types). A
new page is not given a `type` in its header; it takes its folder's.

`partialProps` is there when some keys of `meta` are held only in part by a
listing: the same list, judged by the server from the file, as a summary's
(see Page summary), in every response that returns a page (get, create, save,
`PATCH …/props`, move, and the `current` of a 409), and left out when there
are none. A client takes it from the response and never works it out from
`meta`: `0x10` and `16` look the same there.

### `GET /api/spaces/{space}/pages`

Query parameters, all optional:

| name | meaning |
|---|---|
| `folder` | only pages directly in this folder (empty string = root) |
| `recursive` | `true` to include subfolders of `folder` |
| `tag` | only pages with this tag (repeatable: all must match; at most 10 different tags, repeats count once, more is `400 invalid`) |
| `q` | full-text search over title, subtitle, source and body |
| `in` | with `q`: `title` searches titles only |
| `updatedFrom`, `updatedTo` | only pages last written on or after / on or before this day (`2026-11-01`, UTC) |
| `createdFrom`, `createdTo` | the same for the `created` date of the header; a page without one never matches |

A date that is not a day written `YYYY-MM-DD`, or lies outside the years 1970
to 2261, is `400 invalid`.
| `sort` | `updated` (default), `created`, `title`, or `prop:{key}` for a property of the header; prefix `-` for descending. Default is `-updated`. With `q` and no `sort`, results are ordered by relevance |
| `filter` | a JSON array of conditions on properties, all of which must hold (see below), at most 11 (a saved view has at most 10: a board's column adds its own condition), at most 128 KiB (room for a board's "other values" column on a field with the most options a type may give) |
| `props` | `1` to put `props` and `rev` in the summaries (see Page summary); default `0` |
| `body` | `1` to put the Markdown `body` of each page in the summaries (see below); default `0` |
| `limit` | 1–200, default 50; with `body=1` at most 20, default 20 |
| `cursor` | the `next` value of the previous response |

```json
{"items": [/* page summaries */], "next": "opaque-or-empty"}
```

`next` is an empty string when there are no more results. Sorting by title
puts untitled pages last in both directions, ordered by `created`; titles
compare by code point, case-insensitively. Search terms separated by spaces
must all match; a term matches as a run of adjacent characters and does not
match across punctuation or a line break.

**Sorting by a property.** `sort=prop:status` orders by the value of the
header key `status`; `sort=-prop:status` reverses that. A page that has no
value for the key (the key is missing, null, blank text or an empty list)
comes after all that have one, in both directions, in the order the server
holds the pages in; ties among the others are broken the same way, following
the direction of the sort. Text sorts like titles do (by code point,
case-insensitively), numbers as numbers, dates (`2026-11-22`) as text, true
after false. A list sorts by its first element. The key is any key; it need
not belong to a type. The cursor stays valid while pages change: it names the
position, not an offset, and a cursor made for one sort is refused for
another. Every page of such a listing is read from an index, however many
pages the folder holds.

**Sorting by a select field.** When the listing has a `folder`, and the
folder's effective type (the type of the folder, or of the nearest folder above
it that is bound; see Page types) has a `select` or `multiselect` field with
the key sorted by and at least one option, `sort=prop:{key}` follows the order
of the field's options instead of the order of the text: the options in turn,
then the values that are not options (by text, as above), then the pages
without a value. `sort=-prop:{key}` is the reverse of the first two groups
(the other values first, then the options from the last) and still puts the
pages without a value last. A multiselect counts by its first value. Within an
option the pages are in the order the server holds them, reversed for `-`. The
listing is read from the index one option at a time, with a cursor that names
the option and the place in it, so a page costs the same however many pages
the folder holds (measured: well under a millisecond in a folder of 50 000).
A listing of at most 2000 pages (after the filters) in a folder is read whole,
in one query, and put in the order here: a small folder among 50 000 pages, or
the few pages a selective filter keeps of a big folder (the read starts from
the rows of an `in` or `range` filter in the index, so it costs the pages that
match and not the folder), with any number of options, takes about a
millisecond. A listing of more pages reads each group from the index of the
property. A field that lists an option twice (a type file edited by hand) has
it once, the first writing counting, and at most 500 options count.
The cursor also says how many options there were: a cursor made for another
list of options, or from a listing that was not in option order (the type was
changed, or the folder has none), is `400 invalid` and the client starts over.
Pages of subfolders listed with `recursive=true` follow the order of the
options of the folder asked for, whatever type they have. Without `folder`,
or for any other field, the order is that of the text.

**Filtering by properties.** `filter` is a JSON array, sent as the value of
the parameter (percent-encoded), each item `{"key", "op", "values"}`:

| `op` | `values` | keeps pages whose property |
|---|---|---|
| `in` | up to 500 texts, numbers or booleans | equals one of them (a list: holds one of them) |
| `notIn` | up to 500 texts (as many as a select field has options), numbers or booleans | has a value and none of its values (a list has several) is one of them: not empty, and not an option; the "other values" column of a board |
| `range` | `[from, to]`, both numbers or both texts (dates), either end `null` or `""`, not both; ends are included | has a value in the range (a list: an element is in it) |
| `empty` | none | has no value |
| `notEmpty` | none | has a value (`false` and `0` are values) |
| `contains` | one text | has a value that contains it; letters outside ASCII match in the case they are written |

Several items must all hold. An unknown `op`, a missing key, the wrong number
or kind of `values`, or text that is not that JSON is `400 invalid`. The
filter works together with every other parameter (`folder`, `tag`, `q`, the
sort and the cursor). A text is compared as the first 300 characters of it.
Both `sort` and `filter` answer from the index: nothing reads the page files,
so a folder of 50 000 pages answers in milliseconds. (A filter that few pages
match, in a folder where many do not, is the slow case: a column of a board for
an option no page has takes about 40 ms in a folder of 50 000. `…/pages/groups`
says which columns are empty.)

**Bodies.** With `body=1` each summary has `body`, the page's Markdown, read
from its file. That is a file read per item, so `limit` is at most 20 (also
the default) and a larger one is `400 invalid`; the other parameters work as
usual, so the reading of a folder, a tag or a set of filters goes on with the
`next` cursor. A page whose file is not that page any more (it changed outside
the server since the index looked) has `""`.

### `GET /api/spaces/{space}/pages/groups`

Needs `read`. How many pages of a folder or a tag have each value of a
property, for the heads of the columns of a board. Parameters: `key` (required,
the property), `folder` and `recursive`, `tag` (repeatable, at most 10 different), `filter` (as for a
listing); a folder or a tag is required.

```json
{"groups": [{"value": "草稿", "count": 12}, {"value": "定稿", "count": 7}, {"value": "已废弃", "count": 1}],
 "empty": 4, "truncated": false}
```

`groups` are the values the pages have, most pages first, then by the text of
the value (numbers and booleans as the index holds them: `"3"`, `"true"`); a
list property counts a page once for each distinct value it holds. `empty` is
the number of pages in the scope that have no value for the key (the same pages
as the filter `empty`). Both come from the index, no file is read; the answer
is kept for as long as the index is not written to, so a board opened again
costs nothing. At most 1000
values are returned, the rarest left out, and `truncated` says so. The cost
follows the scope: the pages of the folder, or the pages of the (first) tag
when there is no folder (about 60 ms for 50 000 pages in one folder, 100 ms for
a list; under a millisecond for a small folder, or for a tag of a hundred
pages, among them).
`400 invalid` for a missing `key`, no folder and no tag, more than 10 different tags, or a bad `filter`.

### `GET /api/spaces/{space}/pages/{id}`

The page, or `404 not_found`. With the caller's draft, if any:

```json
{"id": "…", "rev": "3b18e5…", "meta": {…}, "body": "…", "…": "…",
 "draft": {"version": "k3f9…", "baseRev": "3b18e5…", "meta": {…}, "body": "…", "updated": "2026-11-21T08:31:10Z", "stale": false}}
```

### `POST /api/spaces/{space}/pages`

```json
{"folder": "Tang", "meta": {"title": "暮雪", "tags": ["玉兰"]}, "body": "…"}
```

All fields optional. The server assigns `id` and `created`, picks the file
name (from the title, or from the date and id when there is no title), creates
missing folders, and returns `201` with the page. A client-supplied `id` or
`created` in `meta` is ignored.

### `PUT /api/spaces/{space}/pages/{id}`

```json
{"baseRev": "3b18e5…", "meta": {"id": "01JD3K8Q7Z", "title": "暮雪"}, "body": "…"}
```

Replaces header and body; both `meta` and `body` are required, as is
`baseRev`. `meta.id` must be absent or equal to `{id}`; `created` is kept
from the file when left out.
Header fields that are already in the file keep the file's order, and fields
new in the request are appended in the order they appear in it; fields the
request leaves out are removed. Unknown fields are written back as given.
Changing the title renames the file; the id does not change.

Saving this way leaves the caller's draft for the page alone, unless the draft
says exactly what was saved (then it is removed). A draft holds text that may
exist nowhere else: the server never discards one as a side effect. After a
save the draft is usually stale, and the app shows both versions when the page
is opened. A save that would write the bytes the file already has changes
nothing and returns the same `rev`.

- `200` with the page (new `rev`, possibly new `path`).
- `409 conflict` when `baseRev` is not the file's current rev. The response
  is `{"error": "conflict", "message": "…", "current": {/* page */}}` so the
  client can show both versions.
- `400 invalid` for a body that is not valid UTF-8, an `id` mismatch, or a
  request without `baseRev`, `meta` or `body`.

### `PATCH /api/spaces/{space}/pages/{id}/props`

Needs `write`. Changes properties of the header and nothing else: the body,
the built-in fields and the other properties stay exactly as the file has
them, and the body does not travel. The table view's cells use it.

```json
{"baseRev": "3b18e5…", "set": {"status": "done", "points": 3, "labels": ["a", "b"]}, "unset": ["old"]}
```

`baseRev` is required, and `set` or `unset` must name something. A value in
`set` is any JSON value a header can hold (text, number, `true`/`false`, a
list, an object). A value that is `null`, blank text or an empty list removes
the key, as `unset` does; `false` and `0` are values. A key that is new goes
at the end of the header; a key that is in the file keeps its place. A date
written `2026-11-22` (a real date, exactly `YYYY-MM-DD`) is written to the file
as a YAML date; text that only looks like one (`2026-13-45`, `2026-1-5`) is
written as a quoted string, and the file never gets an explicit `!!` tag. The keys are any
that are not built in (`id`, `title`, `subtitle`, `source`, `tags`,
`created`, `type`: save the page to change those) and not reserved (`publish`,
`slug`); at most 200 keys at once, none both set and unset. A change that
would leave the file as it is writes nothing and returns the same `rev`.

- `200` with the page (new `rev`).
- `409 conflict` when `baseRev` is not the file's current rev, as for `PUT`:
  `{"error": "conflict", "message": "…", "current": {/* page */}}`.
- `400 invalid` for a request without `baseRev` or without anything to change,
  a key that may not be changed this way, or a value that cannot be written.
- `403 no_permission` without `write`; `404 not_found` for a page that is not
  there.

The caller's draft of the page is handled as `PUT` does: kept, unless it says
exactly what the file now says. After a change it is usually stale, and the
app shows both versions when the page is opened. The change is journaled and
committed like a save, and in a space that follows a remote it is pushed
before it is answered.

### Drafts

While someone edits, the app does not rewrite the page file on every pause. It
stores a draft: one per page and user, kept in the server's database, not in
the space. Nobody else sees it, and lists and search keep showing the file.
A draft becomes the file's content, and a Git commit, when it is committed:

- the app asks for it (the user leaves the page or chooses to save a version);
- or the server does it by itself once the draft has not changed for
  2 minutes, or 10 minutes after the draft was first stored
  (`--draft-idle`, `--draft-max`). The server does it only while the author may
  still write in the space: a draft of someone who was removed, made a viewer or
  disabled in the meantime is not applied and not deleted. It stays in the
  database (`petalbook draft list|export` shows it), is looked at again after 10
  minutes, and is applied then if the person has the write permission again.

#### `PUT /api/spaces/{space}/pages/{id}/draft`

```json
{"prev": "k3f9…", "baseRev": "3b18e5…", "meta": {"id": "01JD3K8Q7Z", "title": "暮雪"}, "body": "…"}
```

Stores or replaces the caller's draft. `baseRev`, `meta` and `body` mean what
they mean in `PUT …/pages/{id}` and get the same checks, but the file is not
touched. `baseRev` is the rev of the file the edit started from.

Every stored draft has a `version`: an opaque string that is new each time the
draft is stored. `prev` is the version this store replaces: the one the client
last stored or loaded, or `""` (or absent) when it knows of no draft. The
store is refused when that is not what the server has, so the same person
typing on two devices cannot silently replace one draft with the other:

- `200` with `{"version": "p81c…", "baseRev": "3b18e5…", "updated": "2026-11-21T08:31:10Z", "stale": false}`.
  `stale` is `true` when the file's rev is no longer `baseRev`: the draft is
  kept, but it cannot be committed as it is.
- `409 conflict` with `{"error": "conflict", "message": "…", "draft": {…} or null, "current": {/* page */}}`
  when the caller's draft on the server is not version `prev`: there is
  another one (`draft`, in the shape `GET` gives), or there is none any more
  (`draft` is `null`: it was committed or deleted in the meantime, and
  `current` shows what the file holds now). Nothing is stored.
- `404 not_found` when the page does not exist.

#### `DELETE /api/spaces/{space}/pages/{id}/draft`

`204`. Needs only `read`: whoever lost the `write` permission may still discard
their own draft (drafts are per user, so it is always the caller's). See below
for `?version=`.

#### `POST /api/spaces/{space}/pages/{id}/commit`

```json
{"version": "p81c…"}
```

Writes the caller's draft to the file, exactly as `PUT …/pages/{id}` with the
draft's `baseRev`, `meta` and `body` would, and removes the draft. `version`
is required: the draft is committed only if it is that one.

- `200` with the page.
- `409 conflict` with `current` (the page) and `draft` (the caller's draft on
  the server, or `null`), when the draft is not version `version` — including
  when there is none because the server committed it already — or when it is
  stale. Nothing is written and a draft that exists is kept. After a stale
  draft was refused the server stops trying to commit it by itself until it
  is stored again.

A client that gets `409` with `"draft": null` compares `current` with what it
meant to commit: equal means its draft is already the file.

To get past a stale draft the app shows both versions and then either deletes
the draft, or stores it again with the current rev as `baseRev`.

`DELETE …/draft` takes an optional `?version=`; when given and the caller's
draft is another version, or there is none any more (the server committed it
already: `"draft": null`, and `current` shows the file), the answer is
`409 conflict` as above and nothing is deleted. Without `version` the answer
is `204` whether or not there was a draft. With `version`, `&rev=` may be
added: the file rev the client believes the page is at. If the file is at
another rev the answer is the same `409` (with the draft and `current`) and
nothing is deleted, so a client that drops its draft because its text equals
the file finds out when the file is no longer the one it knows.

Deleting a page deletes its drafts.

### History

A version of a page is a commit that changed its file. The history follows
the page through renames and moves.

#### `GET /api/spaces/{space}/pages/{id}/history`

Query: `limit` (1–100, default 30), `cursor`.

```json
{"items": [{"commit": "9fceb02d0ae598e95dc970b74767f19372d61af8", "time": "2026-11-21T08:30:00Z",
            "author": "Kazuha", "path": "Tang/暮雪.md", "rev": "3b18e5…", "change": "edit"}],
 "next": ""}
```

Newest first. `author` is the name on the commit. `rev` is the rev of the
file in that version. `change` is `add`, `edit` (content changed), `move`
(only the path changed) or `delete`. A version in which the page was deleted
has no `rev`.

#### `GET /api/spaces/{space}/pages/{id}/versions/{commit}`

The page as it was in that version: `{"commit", "time", "author", "path",
"rev", "meta", "body"}`. `404 not_found` when that commit is not a version of
this page (it is not possible to read any other file through this endpoint).

Going back to an old version is an ordinary edit: the app loads the old
content into the editor and it is stored and committed like anything typed, as
a new version. History is never rewritten.

### `POST /api/spaces/{space}/pages/{id}/move`

```json
{"folder": "随笔/2026"}
```

Moves the file, creating the folder if needed. Returns the page.

### `DELETE /api/spaces/{space}/pages/{id}`

`204`. The file is removed from the working tree; it remains in Git history.

### `POST /api/spaces/{space}/pages/batch`

Moves or deletes several pages as one change: one commit, and in a space
that follows a remote one push, or nothing at all. In a local space a failure
half way (a full disk) leaves the pages handled so far moved or deleted; what
can be refused beforehand (an unknown id, a bad folder) is refused before
anything changes.

```json
{"op": "move", "ids": ["01JD3K8Q7Z", "01JD3K8Q80"], "folder": "随笔/2026"}
{"op": "delete", "ids": ["01JD3K8Q7Z"]}
```

`ids` holds 1 to 500 page ids, each once. `200` with `{"pages": 2}`: how
many were moved or deleted. A page that is already in `folder` counts and is
left where it is. `404 not_found` when one of the ids is not a page, and
`400 invalid` for a bad `op`, a list that is empty, too long or names an id
twice, a `move` without `folder` (`""` is the top level), or a `folder` that
cannot be one: in both cases nothing is changed. Deleting a page also removes the drafts of it, as deleting one page
does.

## Page types

A page type says which properties the pages of a kind have. It is a file of
the space, `.petalbook/types/{name}.json`, a JSON Schema of which the server
understands a small part. The file name (without `.json`) is the **name** of
the type, the value of `type` in a page's header; it never changes once the
type is made. The `title` in the file is the name people see and can change.
The properties are the keys of `properties`, and their order in the file is
their order everywhere.

Each field has a **kind**, which the server recognises from its schema:

| kind | schema |
|---|---|
| `text` | `{"type": "string"}` |
| `longtext` | `{"type": "string", "x-multiline": true}` |
| `select` | `{"type": "string", "enum": […]}` |
| `multiselect` | `{"type": "array", "items": {"type": "string", "enum": […]}, "uniqueItems": true}` |
| `date` | `{"type": "string", "format": "date"}` |
| `number` | `{"type": "number"}` (`"integer"` is read as a number too) |
| `checkbox` | `{"type": "boolean"}` |
| `url` | `{"type": "string", "format": "uri"}` |
| `other` | anything else: shown and edited as the YAML in the header, never rewritten |

A field may have a `title`. Everything else in a schema (other keywords of a
field, `$schema`, `description`, `required`, …) stays in the file as it was,
and the server changes only the keys it knows when it writes one. A value that
does not fit its field's schema is saved all the same: nothing here checks
page values.

A folder is bound to a type in `.petalbook/space.yml`:

```yaml
folders:
  Tang:
    type: 文案
```

A folder takes the type of the nearest folder above it that is bound (the
root, `""`, may be bound too); a page whose header has a `type` has that.
Renaming or moving a folder changes its bindings, and those of the folders in
it, in the same commit; deleting one removes them (and its views, see Saved
views). Other settings an entry has stay. The move and the change of the bindings are one change: if the server
stops between the two, it finishes the bindings when it starts again, and if
the bindings cannot be written the folder is not moved and the request fails
(also when `space.yml` is so full that the new key would make it larger than the
1 MiB the server reads). Moving or deleting a folder removes entries of
`space.yml`; an alias elsewhere in the file that pointed into a removed entry
(a hand-written `*anchor` of another folder's view) is replaced by a copy of what
it pointed to, so the other folder keeps its views and the removed folder's
settings never stay behind under the old name. Should the file still not be
readable after the edit, the move or delete is refused like a full file (`400
invalid`, fix `space.yml` by hand). A move that a crash interrupted and whose
bindings then do not fit in `space.yml` is taken back at the next start: the
folder goes back to its old name, and the error is logged.
Nothing was at the place a folder is moved to, so every binding at that place
and under it is stale and is dropped (the moved folder takes the bindings it
had, and none it did not); the journal of the move is kept until the bindings
are done, so a crash at any point, also while a failed change in a space that
follows a remote is undone, is finished or taken back with folder and bindings
agreeing. If the move fails and is taken back, the parent folders it made for
the target are removed again. The same place, written twice, is its last entry; a folder written twice in `space.yml` counts as its last entry, and
removing a binding removes all of them. Comments in `space.yml` and `tags.yml`
stay, also in a file that has nothing else.

Limits: a space has at most 100 types; a type file is at most 256 KiB and has
at most 200 fields; a field key is 1 to 64 bytes, has no control character
and no `:`, and is not `id`, `title`, `subtitle`, `source`, `tags`, `created`,
`type`, `publish` or `slug`; a select has 1 to 500 options of at most 200
bytes each, none blank, none twice; titles are one line of at most 200 bytes;
a type name is 1 to 100 bytes, not starting with a dot or ending in a dot or
space, with none of `< > : " / \ | ? *`; two names that differ only in case
are one name. The key rules are for a field that is new: a field the file
already has keeps its key whatever it is (written by hand, say), and a request
may list it as it is.

A type file or `space.yml` that someone damaged by hand (not JSON or YAML, not
an object, too large, a link) is never overwritten: a request that would
write it is `400 invalid` and says it has to be fixed by hand. Reads still
work: such a type is listed with an `error`, and a `space.yml` that cannot be
read binds nothing. Moving or deleting a folder is not stopped by one.

### Type

```json
{
  "name": "task",
  "title": "任务",
  "rev": "9c2f…",
  "fields": [
    {"key": "status", "title": "状态", "kind": "select", "options": ["todo", "doing", "done"],
     "schema": {"title": "状态", "type": "string", "enum": ["todo", "doing", "done"]}},
    {"key": "points", "title": "", "kind": "number", "schema": {"type": "number"}}
  ]
}
```

`rev` is the Git blob id of the type's file, as the `rev` of a page is: it
changes whenever the file does, and a change or a delete of the type is made
from it (`baseRev`, below). A file too large or not a regular file has the
`rev` `"unreadable"`. `title` is `""` when the file gives none or gives
something that is not text (show the name or key); a `title` that is not text
stays in the file when a request leaves the title empty. `options` is
there for `select` and `multiselect`. `schema` is the field's schema as the
file has it. A type whose file cannot be used is
`{"name": "…", "title": "", "fields": [], "error": "…"}` and the error says
how to fix it.

### `GET /api/spaces/{space}/types`

Needs `read`.

```json
{"types": [/* types, in name order */], "bindings": [{"folder": "Work", "type": "task"}]}
```

`bindings` are all the folders that are bound, in the order of the file. Only
the first 100 type files are read; more than that (put there by hand) are
listed with an `error`.

### `PUT /api/spaces/{space}/types/{name}`

Needs `manage`. Makes the type, or changes it. The request is the whole of
what the type is to be:

```json
{"baseRev": "9c2f…", "title": "任务", "fields": [
  {"key": "status", "title": "状态", "kind": "select", "options": ["todo", "doing", "done"]},
  {"key": "points", "kind": "number"}
]}
```

`baseRev` is the `rev` of the type the request was made from, and `""` or
absent to make a type that is not there yet. When the type is not as that says
(changed, made or removed since) nothing is written and the answer is `409
conflict`.

The fields are the type's fields, in this order: one the type has that is not
listed is removed from the type (pages keep the values; the app shows them as
not in the type), and a `title` that is left out or empty removes the title.
The server merges this into the file: a field that exists and keeps its kind
has only its `title` and options changed, a field whose `kind` changes gets
the keys of the new kind and keeps its others, a field that is new is made.
`kind` may be left out for a field that exists (it keeps its kind) and is
required for a new one; `other` cannot be made, and a field cannot be changed
to it. `options` goes with `select` and `multiselect` only. Renaming a field
is removing one and adding another; the pages are not rewritten. `200` with
the type.

Errors: `400 invalid` for anything above that is out of limits or malformed,
and for a file that has to be fixed by hand; `409 conflict` when `baseRev` is
not the file's current rev, or the type is there and `baseRev` is empty, or
the type is not there and `baseRev` is not: `{"error": "conflict", "message":
"…", "current": {/* type */}}`, `current` being the type as it is now (absent
when it is gone); `409 exists` when another name
differs from `{name}` only in case. The change is journaled and committed like
any other.

### `DELETE /api/spaces/{space}/types/{name}?baseRev=…`

Needs `manage`. Removes the file and, in the same commit, every binding to the
type. `baseRev` is the `rev` of the type the request was made from, as for
`PUT`; `409 conflict` (with `current`, as there) when it is not the file's
current rev. For a type whose file is gone and only bindings are left, it is
`""`. The headers of pages that name the type are not touched. `204`;
`404 not_found` when there is no such type and no folder is bound to it. The
file of a type that cannot be read can be deleted too. Deleting a folder, not a
file, in its place is `400 invalid`.

### `PUT /api/spaces/{space}/folders/type`

Needs `manage`.

```json
{"folder": "Work", "type": "task"}
```

Binds a folder (and, by inheritance, the folders below it) to a type, or
removes the binding when `type` is `""`. `200` with `{"folder": "Work",
"type": "task"}`, the folder as it is spelled on disk. `404 not_found` when
the folder does not exist; `400 invalid` when the type does not exist, or
`space.yml` has to be fixed by hand. Pages are not changed: their
`effectiveType` is.

## Saved views

A view is a saved way of looking at the pages of a folder or of a tag: a
layout, a sort, filters, the field a board is grouped by, the hidden columns of
a table. A folder's views are written under its entry in
`.petalbook/space.yml`, next to its type; a tag's under its entry in
`.petalbook/tags.yml`, next to its colour. The root folder is the key `""`.

```yaml
folders:
  文案:
    type: wenan
    views:
      - id: k3f9
        name: 按状态
        layout: board
        group: status
        sort: -prop:written
        filter: [{key: series, op: in, values: [冬季]}]
```

The first view is the default one; a folder or tag with none shows the
implicit default. Moving or renaming a folder carries its views, and those of
the folders in it, in the same commit and the same journal record as its
bindings (a crash in between is finished when the server starts); deleting a
folder removes them (a folder that stays, because it holds files that are not
pages, keeps them). Deleting a type keeps the views of the folders that were
bound to it; unbinding a folder keeps its views. Comments, keys the server does
not know (in a view, in the entry) and other settings stay in the file: a
view that is saved again is changed in its own YAML. The settings files are
read and written the way bindings and tag colours are (a file damaged by hand
is never overwritten: a save is `400 invalid` and says it has to be fixed by
hand), and a change is journaled and committed like any other, in a space that
follows a remote too. There is no tag rename in the API, so nothing carries a
tag's views to another name.

### View

```json
{"id": "k3f9a1", "name": "按状态", "layout": "board", "sort": "-prop:written", "group": "status",
 "filter": [{"key": "series", "op": "in", "values": ["冬季"]}], "hidden": []}
```

Every key is always there when the server answers (`sort` and `group` as `""`,
`filter` and `hidden` as `[]` when empty). `id` is made by the server: six
lower-case letters and digits, unique in the space (folders and tags together).
`name` is one line of 1 to 200 bytes. `layout` is `list`, `cards`, `table`,
`board` or `read`. `sort` is `""` (the default order of the listing), `updated`,
`created`, `title` or `prop:{key}`, each with an optional leading `-`, as for a
listing. `filter` is written as the `filter` of a listing (at most 10, each
checked the same way, `notIn` included). `group` is the field a board is
grouped by, at most 256 bytes; it is kept whatever the layout. `hidden` are the
keys of the columns a table hides, each once, at most 200. A key that is not
one of these is kept as it is (`"color": "teal"` comes back) and is written to
the file; the server does not look at its meaning, but it limits its size: at
most 16 such keys, each value at most 4 KiB as JSON, and a view at most 16 KiB
as JSON (the views a request changes, 128 KiB together). A view that a request
gives back exactly as it is stored is not checked again.

Views written by hand are read so that a client can work with them: a view
whose `id` is missing, malformed or repeated has the id `@{n}`, `n` its
position (from 0) in the folder's list, and a missing or unknown `layout` reads
as `list`; every view in an answer therefore has a unique id and a layout. A
write that names such an id changes that view and gives it a new one. A value
of a key the server does not know that stands for more than 10 000 YAML nodes
(aliases counted each time they are used) is left out of the answer, and stays
in the file.

### `GET /api/spaces/{space}/views?folder={path}` or `?tag={tag}`

Needs `read`. Exactly one of `folder` (the root is `folder=`) and `tag`.

```json
{"views": [/* views, the first being the default */], "rev": "6a6d14f7db61e4f5578c38cad4f75dfc5a97a9a6"}
```

`rev` is a digest of the views as the server reads them (a list with none has
one too), so a change to the file by hand is a new rev as well. A folder that
does not exist has no views (it is not looked for on disk). A settings file
that cannot be read gives none. `400 invalid` for neither or both of `folder`
and `tag`, or a path that cannot be a folder.

### `PUT /api/spaces/{space}/views`

Needs `write`: views are a shared way of arranging pages, and whoever can write
pages can change them. A member who can only read can switch between views and
change the sort and filter for themselves; they cannot save them.

```json
{"folder": "文案", "baseRev": "6a6d…", "views": [/* the whole list */]}
```

`folder` (or, instead, `tag`) says whose views; `baseRev` is the `rev` of the
views the request was made from; `views` is the whole list the folder or tag is
to have, in order (`[]` removes them all, and the entry of the folder when
nothing else is left in it). A view with an `id` must be one of the views the
folder or tag has now (`@{n}` ids included, see above); one without gets a new id. A view that keeps its `id`
keeps what the request does not mention: its comments, and the keys the server
does not know that the request leaves out. A view that is not listed is
removed.

`200` with `{"views": […], "rev": "…"}`, the views as they are now. When the
views are not as `baseRev` says nothing is written and the answer is `409
conflict` with `current` the same object as a `GET` answers:
`{"error": "conflict", "message": "…", "current": {"views": […], "rev": "…"}}`.
`400 invalid` for a missing `baseRev`, `views` or scope, both a folder and a
tag, more than 20 views, any view out of the limits above, an `id` the folder
or tag does not have, an `id` twice, a change that would make `space.yml` or
`tags.yml` larger than the 1 MiB the server reads, or that would leave either
in a form the server cannot read again (an anchor that something else in the
file refers to would be removed; both: nothing is written, so the settings stay
readable), or settings that have to be fixed by hand. The body of the request is
at most 20 views at their largest and 512 KiB more (`413` above that), and the
list is refused as soon as it has a 21st entry, before any of them is built.
A view that the request gives back as it is stored is left as it is in the file,
also what the server could not read of it (an alias, a scalar where a list
belongs); a key is removed or rewritten only when the request changes it. `[]` removes
the views from every writing of the folder in `space.yml`, so none comes back.
`404 not_found` for a folder that does not exist (a tag need not be in use).
A write that changes nothing writes nothing.

## Files

Images and other attachments are ordinary files of the space. The ones the
server stores live in `assets/<page id>/` at the root of the space, and a page
refers to them by a path from the root of the space:

```markdown
![a rainy street](/assets/01JD3K8Q7Z/street-3f9a1c2b.jpg)
[the contract](/assets/01JD3K8Q7Z/contract-91be02aa.pdf)
```

That path stays right when the page is moved or renamed. A client showing a
page resolves the address of an image or link like this: one with a scheme
(`https:`, `data:`, `mailto:` …) or starting with `//` or `#` is left alone;
one starting with `/` is a path from the root of the space; anything else is
relative to the folder of the page. The resulting path is fetched with
`GET …/files/{path}`.

### `POST /api/spaces/{space}/pages/{id}/assets?name={file name}`

Stores a file for the page. The body is the file's bytes, sent with
`Content-Type: application/octet-stream`. `name` is the name the file had;
only its last path segment is used.

`201` with

```json
{"path": "/assets/01JD3K8Q7Z/street-3f9a1c2b.jpg", "name": "street-3f9a1c2b.jpg", "size": 48213}
```

`path` is what goes into the page. The stored name is made by the server:
the given name with characters that file systems refuse replaced, cut to a
reasonable length, the extension in lower case, and the first 8 hexadecimal
digits of the SHA-256 of the content before the extension. Uploading the same
bytes under the same name again answers `200` with the same body and stores
nothing new.

Errors: `404 not_found` when the page does not exist; `400 invalid` for an
empty body or a missing name, and when `assets` or the page's directory in it
is not a plain directory (a symbolic link, a file, or a folder named the same
in another letter case on a file system that ignores case); `409 exists` when
another file already has the stored name (same name, same first 8 digits,
other bytes); `413 invalid` when the file is larger than
`maxUpload` (see `GET /api/meta`); `415 invalid` for another content type;
`503 unavailable` as for every change in a space that follows a remote.

The file is committed like a page is: at once and pushed in a space that
follows a remote, otherwise by the next background commit. It is stored
whether or not the page ends up referring to it; nothing removes files that
no page refers to (a later milestone).

### `GET /api/spaces/{space}/files/{path}`

The bytes of a file of the space. `path` is from the root of the space, each
segment percent-encoded. `404 not_found` for a path that does not exist, is
not a regular file, has a segment starting with a dot, goes through a
symbolic link, or is a page (a `.md` file outside `assets/`): pages are read
through the page endpoints.

The answer carries `X-Content-Type-Options: nosniff`, a
`Content-Security-Policy` that forbids scripts and sandboxes the document,
and `Cache-Control: private, no-cache` with `Last-Modified`, so a browser
revalidates with `If-Modified-Since` and gets `304`. `Range` requests work.
The content type comes from the extension, for a fixed list of image, audio,
video, PDF and plain-text types; those are sent `Content-Disposition:
inline`. A PDF is sent without the policy header, because browsers' PDF
viewers do not start under it. Everything else is `application/octet-stream` and
`Content-Disposition: attachment`.

## Import

### `POST /api/spaces/{space}/import?folder={folder}&name={file name}`

Brings Markdown files into the space as pages. The body is either one
Markdown file or a `.zip` of files and folders (which it is, is told from the
content: a zip starts with its signature), sent with
`Content-Type: application/octet-stream` like an upload. It may be at most
`maxImport` bytes (see `GET /api/meta`; `--max-import`, 100 MiB by default).

| name | meaning |
|---|---|
| `folder` | the folder the pages go into; optional, the root by default. It is made if missing |
| `name` | the name the file had; required for a single Markdown file (it gives the title when the file has none), ignored for a zip |

Every file whose name ends in `.md` or `.markdown` becomes one page; folders
of a zip become folders below `folder` (a file in a folder the server would
not make, see `POST …/folders`, is left out). The server works on one import
at a time; a second one waits its turn. Nothing that exists is replaced: a
page whose file name is taken gets a numbered one, as when creating a page.
For each file:

- A header (YAML between `---` lines at the top) is kept, key by key, except
  `id`: every imported page gets a new id. A `created` that is not a date is
  dropped, and one is assigned as for a new page. A header that does not
  parse is left in the body as text.
- A Markdown file larger than a page may be (8 MiB) is left out of a zip,
  and refused (`400 invalid`) when sent alone.
- The title is the header's `title`; else the first line of the body when it
  is a `# ` heading (which is then taken out of the body); else the file's
  name without its extension.
- An image or link whose address is a relative path to another file in the
  zip (not a page) makes that file an attachment of the page, and the
  address is rewritten to it (see "Files"). A file larger than `maxUpload`
  is left out and the address stays as written. Addresses with a scheme,
  absolute paths, and links to other Markdown files stay as written.

Files that are neither pages nor referred to by one are not imported; hidden
files and folders (a name starting with a dot, `__MACOSX`) are ignored.

`200` with a report:

```json
{
  "pages": 104,
  "files": 3,
  "skipped": [{"path": "notes/draft.docx", "reason": "not a Markdown file and no page refers to it"}]
}
```

`files` counts attachments stored. `skipped` lists what was left out and
why, for a person to check (at most 200 entries; `skippedMore` is how many
further ones there were, absent when none).

Errors: `400 invalid` when the body is empty, is a single file without
`name` or with a name that is not Markdown, is a zip without any Markdown
file, unpacks to more than four times `maxImport`, has more than 20,000
entries, or `folder` is not a valid folder path; `413 invalid` over
`maxImport`; `415 invalid` for another content type; `503 unavailable` as
for every change in a space that follows a remote.

The whole import is one change: one commit, and in a space that follows a
remote one push, with nothing left behind if it fails. In a local space a
failure half way (a full disk) can leave the pages created so far.

## Folders and tags

### `GET /api/spaces/{space}/tree`

Every folder that exists, including empty ones, with the number of pages
directly inside it.

```json
{"folders": [{"path": "", "pages": 3}, {"path": "Tang", "pages": 95}, {"path": "随笔/2026", "pages": 4}]}
```

### `POST /api/spaces/{space}/folders`

```json
{"path": "随笔/2027"}
```

`201` with `{"path": "随笔/2027"}`; `409 exists` if it is already there;
`400 invalid` for a name starting with a dot, `assets` at the root in any
letter case (that name is reserved for attachments), or a name ending in
`.md` (that is how page files are named). The same applies to folders made
on the way by creating or moving a page.

### `POST /api/spaces/{space}/folders/move`

```json
{"from": "随笔/2026", "to": "归档/2026"}
```

Moves a folder with everything in it, which is also how a folder is renamed
(`{"from": "随笔", "to": "散文"}`). Missing parents of `to` are made. `200`
with `{"path": "归档/2026"}`. Pages keep their ids, so links, drafts and
attachments are not affected. It is one change: one commit, and one push in
a space that follows a remote.

Errors: `404 not_found` when `from` is not a folder; `409 exists` when `to`
already exists (folders are not merged), except that `to` may be another
spelling of `from` itself (a change of letter case); `400 invalid` when
`from` is the root, when `to` is `from` or inside it, or when `to` is not a
folder name the server makes (see `POST …/folders`).

### `DELETE /api/spaces/{space}/folders?path={path}`

Removes a folder. `204`. Without `recursive=true` the folder must hold no
pages, in it or below it (`409 conflict` otherwise; empty subfolders go with
it). With `recursive=true` the pages in it and below it are deleted like
`DELETE …/pages/{id}` deletes one, drafts included; they remain in Git
history. Files in the folder that are not pages are left where they are, and
then so is the folder that holds them. `404 not_found` when it is not a
folder, `400 invalid` for the root.

### `GET /api/spaces/{space}/tags`

```json
{"tags": [{"tag": "栀子", "pages": 41, "color": "rose"}]}
```

Ordered by count, descending. `color` is absent when the tag has none.

### `PUT /api/spaces/{space}/tags/{tag}`

```json
{"color": "rose"}
```

Sets the colour of a tag, or removes it with `{"color": null}`. The colour
is either one of the names `gray`, `red`, `orange`, `amber`, `green`, `teal`,
`blue`, `purple`, `pink`, `rose` (how each looks is the client's business,
and follows its theme), or a colour of the person's own as `#rrggbb` (six
hexadecimal digits; stored in lower case). `200`
with `{"tag": "栀子", "color": "rose"}` (`color` absent when removed).
`400 invalid` for another value, for a tag that is empty, longer than 200
bytes or has a line break in it, and when `.petalbook/tags.yml` exists but
cannot be read as YAML (it is then left as it is, for a person to fix). The
tag in the address is percent-encoded, so it may contain `/`. The tag need
not be in use (it is listed, with its colour, once a page uses it). Colours are
kept in `.petalbook/tags.yml` in the space, so they travel with the
repository; a change is committed like any other. The same entry of the file
holds the tag's saved views (see Saved views), which setting or removing the
colour leaves alone.

## Maintenance

### `POST /api/spaces/{space}/reindex`

Rebuilds the index from the files. `200` with `{"pages": 1234, "ms": 850}`.
The index is otherwise kept current by the server's own writes and is rebuilt
automatically at start-up when it is missing or does not match the files.

The server does not watch the file system. Edits, additions, deletions and
moves made outside it show up in listings, search, the tree and tags after a
restart or a reindex. Reaching a page by id repairs the index sooner: a `GET`,
`PUT`, move or `DELETE` of an id whose file is no longer where the index
expects it re-reads the files at once, and an id the index has never seen
triggers a re-read at most once every 2 seconds (in between, the answer is
`404`). Reading, saving, moving and deleting always check the file's own id
before acting, so a request never touches another page's file.

A `.md` file with no header, a header that does not parse, no `id`, or an `id`
another file already uses is not served as a page. The server logs each one
at start-up. Showing and repairing them in the app comes in a later milestone.

## Administration

Accounts mode, administrators only; single-user mode answers `404`, anyone else
`403 forbidden`. These endpoints work on the server's records, not on a space's
pages: none opens a space (so they work on a disabled one), and nothing they do
deletes a file. They are separate from "Users and invites", which is unchanged.

### `GET /api/admin/spaces?q={text}&cursor={cursor}&limit={n}`

Every space on the server, in order of creation, one page at a time:

```json
{"items": [{"id": "01JD3K8Q7Z", "name": "Team", "personal": false, "inbox": "Inbox", "created": "2026-11-21T08:30:00Z",
  "disabled": false, "owner": {"id": "01JD3K7A2M", "username": "tang", "name": "Tang"}, "members": 3,
  "sizeBytes": 18304211, "sizeAt": "2026-11-22T02:10:00Z", "sync": "local"}],
 "next": "01JD3K8Q7Z"}
```

`q` keeps the spaces whose name contains it, ignoring case (on SQLite for all of
Unicode; on PostgreSQL as its `LOWER` does, which in a database with the C
locale folds ASCII letters only). `limit` is 1 to
200 (default 50). `next` is the `cursor` of the following page, `""` after the
last. The list is one database query for the whole page, however many spaces
there are.

`inbox` is the folder quick notes go to (see "Space"): an administrator who opens a
space they are not a member of captures into it. `owner` is the first owner (the one whose name is the Git author of a space's
default commits), `null` if there is none. `members` counts every member.
`sizeBytes` and `sizeAt` are the last stored measurement and are both `null`
until the space has been measured (also when its first measurement failed). Sizes are not measured when the list is asked for:
a space can hold tens of thousands of pages. The server measures them in the
background, one space at a time: it takes the space measured longest ago (or
never), if that was more than 6 hours ago, and adds up the sizes of the files
under its directory, `.git` included (the index cache is not counted); then it
goes straight on to the next stale space, so a first round over thousands of
spaces takes as long as the walks do (a few hundred milliseconds for a space of
tens of thousands of pages), not days. Walks never overlap and pause
briefly after each directory, so the load stays that of one walk. When no space
is stale it looks again every minute. So the number can be several hours old.
`POST .../size` measures one space now.

`sync` is `local` or the state of the remote the space follows (see "Remote
repository"), as the server knows it without opening the space. A space that
has never been opened since the server started has the state it was last left
in; nothing is opened to fill this in.

### `GET /api/admin/spaces/{space}`

One space, the same object as an item of the list (same first owner, same
member count), found by its id with one query; this is how a client finds a
space it was sent to by address without reading the list. `404 not_found` for
an id that is no space.

### `POST /api/admin/spaces/{space}/size`

Measures the space's directory now (waiting its turn if a measurement is
running: they never run in parallel) and stores the result. `200` with
`{"sizeBytes": 18304211, "sizeAt": "2026-11-22T02:10:00Z"}`.

### `POST /api/admin/spaces/{space}/transfer`

`{"userId": "01JD3K7A2M"}`. The user becomes the owner of the space (a member
is promoted, anyone else added) and every other owner becomes an editor. The
Git author of later commits is the new owner, also in a space that is open
now. `204`. `404 user_not_found` when there is no such user, `409 user_disabled`
when the user cannot sign in, `409 personal_space` for a personal space.

### `POST /api/admin/spaces/{space}/disable`

`204`. A disabled space stays in its members' lists (`disabled: true`) but
cannot be opened by anyone, administrators included: every request on it
answers `403 space_disabled`, and the drafts waiting in it are kept and
committed once it is enabled. If it is open it is closed after committing what
it holds; requests that are using it at that moment finish first (the server
waits up to 5 seconds, then leaves it to close as soon as the last one is done).

### `POST /api/admin/spaces/{space}/enable`

`204`. Undoes `disable`.

### `DELETE /api/admin/spaces/{space}`

Closes the space (committing what it holds), deletes its records (members,
drafts, remote and the space itself) and moves its directories into the trash
inside the data directory; no file is deleted. `200`:

```json
{"trashed": "trash/01JD3K8Q7Z-20261122T021000Z", "cacheTrashed": "trash/01JD3K8Q7Z-20261122T021000Z.cache", "warnings": []}
```

`trashed` is the repository, `cacheTrashed` the index and journal (moved too:
the journal can hold a change that never reached Git), both relative to the
data directory. To get a space back, move the directory back and add the
records by hand; to get the room back, delete the trash. Members' drafts, held
ones too, are written first to `trash/<id>-<stamp>.drafts.json` (space, page,
user, base rev, header, body, times) so that the text typed and not saved is not
lost with the records; if that file cannot be written nothing is deleted. If a move fails after
the records are gone, the files are left where they are, `trashed` is `""` and
`warnings` says what happened (the server also logs it as an error). Members'
very next request answers `404 not_found`. `409 busy` when requests still use
the space or its pending changes cannot be committed: nothing was changed, try
again. `409 personal_space` for a personal space, which goes with its person.

### `GET /api/admin/status`

```json
{"version": "0.9.0", "startedAt": "2026-11-22T01:00:00Z", "uptimeSeconds": 4210, "database": "sqlite",
 "spaces": 212, "users": 40, "openSpaces": 7, "diskFreeBytes": 52428800000, "goVersion": "go1.27.4"}
```

`database` is `sqlite` or `postgres`. `openSpaces` is how many spaces are open
in memory now (idle ones are closed, see `--space-idle`). `diskFreeBytes` is
the room left for the server on the disk of the data directory; it is absent
on platforms that cannot say (anything but Linux, macOS and FreeBSD).
