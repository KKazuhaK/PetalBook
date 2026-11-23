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
the folder holds (measured: about a millisecond in a folder of 50 000, in
either direction, with 5 options or 500). With filters, or in a small folder
among many pages, it is read like any filtered listing (see "What a filtered
listing costs" below). A field that lists an option twice (a type file edited by hand) has
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
Both `sort` and `filter` answer from the index: nothing reads the page files.

**What a filtered listing costs.** A listing with filters, tags, dates, a
recursive folder or a sort by a property is read in the cheapest of three
ways, each bounded, so that no combination reads a folder page by page.
Measured in a space of 50 000 pages, the median of a page of the listing:

- *From the smallest condition.* When a folder, a tag, or an `in`, `range`,
  `notEmpty` or `notIn` filter keeps at most 2000 pages, the read starts from
  those and checks the rest for each: a folder of 100 among 50 000 in any
  order, a filter few pages match, the "other values" column of a board,
  about 1 ms; 1100 matches put in option order, 6 ms. A filter no page
  matches (`in` with 500 values none of which exists, a column for an option
  nobody uses) answers in under a millisecond.
- *By walking the order.* When every condition keeps many pages, the order is
  walked from the cursor and each page checked, for at most 1000 pages (or 5
  per item asked for): a column of a board for an option in use, a sort with
  a filter half the pages meet, any page of it by cursor, 1 to 3 ms.
- *As sets.* When the walk does not fill the page (few pages match and no
  one condition says so, as with `contains` or `empty`; or the matches come
  last in the order), each condition is read as one range of its index and
  the ranges are intersected, about 6 ms for each 50 000 rows, four at a
  time: `contains` that nothing matches 15 ms, a sort by a property with a
  filter on the same property 20 ms, a search with a sort 25 ms, ten
  `contains` filters at once 50 ms.

The dear case is a request whose every condition keeps most of the pages
while together they keep few: ten tags and ten filters (with lists of 500
values), each on nine pages in ten, 200 items asked for, took 40 to 140 ms.

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
values are returned, the rarest left out, and `truncated` says so. The cost,
when the answer is not a kept one, is one read of the index range of each
condition and one of the rows of the property, no lookup page by page.
Measured in a space of 50 000 pages: 25 ms for a recursive folder or a tag
that holds them all, 30 ms for a list property, 40 to 50 ms for a property
with thousands of distinct values; 15 to 35 ms with a `range`, `contains` or
`notIn` filter (100 or 500 values alike); 10 ms for one folder of 5000; under
a millisecond for a small folder or a tag of a hundred pages; 3 ms for the
"other values" of a board. With ten tags and ten filters that each keep
most of the pages it took 100 to 135 ms: twenty ranges of the size of the
space.
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
- `409 conflict` when `baseRev` is not the file's current rev, unless only the
  server's link rewrites changed the file since (see "Links between pages").
  The response is `{"error": "conflict", "message": "…", "current": {/* page */}}`
  so the client can show both versions.
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
  `stale` is `true` when the file's rev is no longer `baseRev` (and not only
  because of the server's link rewrites, see "Links between pages"): the draft
  is kept, but it cannot be committed as it is.
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

### Links between pages

A page links to another with an ordinary relative Markdown link to the other
page's file, with the target's id in the title position:
`[暮雪](../Tang/暮雪.md#小节 "01JD3K8Q7Z")`. Only links whose destination ends in
`.md` and stays inside the space count; links to the web, images, attachments
and anything in code do not. A title that is exactly an id is the id (the link
then follows the page by id); any other title is kept and the link is read by
its path alone. A link without an id is read by path and gets the id the next
time the page is saved, or when it is rewritten; a link whose id is that of a
page at another path gets that path when the page is saved (only the path
changes). A reference-style link (`[暮雪][r]` with `[r]: ../Tang/暮雪.md`
elsewhere in the page) counts too: its definition holds the path and the id,
and is what is rewritten.

Whenever a page is renamed (a new title in `PUT`), moved (`move`, `batch`, folder
move), the links of the space follow it in the same change, so in the same
commit (and the same push in a space that follows a remote): the files that link
to it have the path of those links rewritten (the text, the `#anchor`, the
other bytes of the file are kept), and so do the links of the moved page itself,
which are relative. If the server stops half way, the next start finishes it.
A page that is moved or renamed outside PetalBook (a pull, an edit in the
folder) has the paths of the links that carry its id put right by the server,
in one commit of its own; a link whose id two files claim, or no page has, is
left as it is, by a rename or move as well. Deleting a page changes no other
file: links to it become broken.

These rewrites are the server's, not a person's, so they never make anyone's
work conflict: a `PUT` page or a draft whose `baseRev` is behind the file only
by such rewrites is accepted as if it were current, with the same rewrites made
to its text (the old path is not written back), and such a draft is not
`stale`; `GET` page returns it with the rewrites made and the file's `rev`
as its `baseRev`. The server remembers the last 20,000 rewrites for this,
across restarts; a draft older than all of them is shown as stale as before.
Reading these needs `read`; the rewrites need nothing of their own (they are part
of the write the caller is allowed).

#### `GET /api/spaces/{space}/pages/{id}/backlinks?cursor=`

The pages that link to this one, the most recently changed first, 20 at a time:

```json
{"items": [{"id": "…", "path": "Notes/引用.md", "title": "引用", "untitled": false,
            "excerpt": "见暮雪 和无"}],
 "next": "", "total": 1}
```

`excerpt` is the paragraph the link is in, as plain text, cut to about 240
characters around the link (`""` when the file no longer has the link). A page
that links several times is listed once. `next` is `""` at the end, else the
`cursor` of the next request; a `cursor` that was not handed out is `400`. `404`
for an unknown page.

#### `GET /api/spaces/{space}/pages/{id}/links`

The links of this page, in the order of the text, for marking broken ones and
for hover text:

```json
{"items": [{"text": "暮雪", "path": "Tang/暮雪.md", "id": "01JD…", "anchor": "小节",
            "broken": false,
            "target": {"id": "01JD…", "path": "Tang/暮雪.md", "title": "暮雪", "untitled": false}},
           {"text": "无", "path": "Notes/无.md", "broken": true}]}
```

`path` is the file the link names, from the root of the space. The target is
found by `id` when the link has one, else by `path`; `title` is the target's
current title (or what stands for it, as in a page summary). `broken` is true,
and there is no `target`, when no page is there.

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
1 MiB the server reads). Whether the settings can follow is found out before
the folder is touched: a move or a delete whose edit of `space.yml` cannot be
made is `400 invalid` with nothing renamed and nothing deleted, and the message
says which it was ("the folder was not moved: …", "the folder was not
deleted: …"). For a delete that is found out for the folders that will be
gone afterwards: a folder that holds files that are not pages stays, and so
does its entry. Should the folders turn out otherwise than was worked out
before the delete (a file came to lie in one meanwhile) and the settings then
cannot be taken out as the folders are, they are taken out as was worked out.
Moving or deleting a folder removes entries of `space.yml`; what
that does to anchors and aliases is under Anchors, aliases and merge keys below.
A move that a crash interrupted and whose
bindings then do not fit in `space.yml` is taken back at the next start: the
folder goes back to its old name, and the error is logged. A `space.yml` that
cannot be used at the next start is restored there as it is by any other
change (see Settings files that cannot be used), and the space opens.
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

#### Settings files that cannot be used

`space.yml`, `tags.yml` and the type files are written by hand too, and come in
with a pull. A type file that cannot be used (not JSON, not an object, its
`properties` not an object, more than 200 fields, too large, not a regular
file) is treated as below, like the two settings files: it is read as its
newest version in the history that can be used (with none, there is no such
type), the next change of the type writes it and keeps the file as it was as
`.petalbook/types/{name}.json.unreadable-{time}`, and so does deleting the
type. A field whose schema
is not an object is kept as it is until a request gives the field a kind;
then the schema is written anew and the file as it was is kept.

A settings file that cannot be used (it is not YAML with keys at the top, has a second
document, its `folders` or `tags` is not a list of entries, an alias is inside
its own anchor, it is over a limit below, larger than 1 MiB, or not a regular
file) is nobody's to fix before the space works again, and no request is
refused because of it:

- **Reading.** The settings are those of the newest version of the file in the
  history of the space that can be used, looked for among the last 50 commits
  that changed the file, in the order of the commits (the clocks of the
  machines that made them are never compared); with none, the space has no
  settings, like one that never had the file. What the server wrote itself
  and has not committed yet counts as newer than any commit, until it is
  committed. Onto that version goes what can still be read of the file as it
  is, entry by entry, the file winning for each entry it has: in a file that
  is YAML but cannot be used, every top-level key and every entry of
  `folders` or `tags` that can be used by itself (with its aliases written
  out); in a file that is not YAML, every top-level key, and every entry of
  `folders` or `tags`, that reads by itself when the text is cut by its
  indentation (a leading byte order mark is not part of the first key, and
  the lines of a quoted value that runs over several lines stay with it,
  wherever they start, so a line `folders:` inside a value never makes up
  entries; a quote never closed is a typo and closes nothing). This takes
  time in proportion to the file, also at the 4 MiB most that is read of
  it. So thirty new bindings and one typo keep the thirty; only the
  entry with the typo is not read (`lost` in the notice), and it is in the
  file and in the copy kept of it. A type file is one JSON document and is
  read as the version restored. Bindings, views and colours are read from
  those settings until the file is put right. What was found is remembered for
  the file as it is and the newest commit that changed it, so the history is
  searched once, not for every request, and not again for commits of other
  files.
- **Repair.** The next change that writes the file (a binding, views, a
  colour, a folder with settings moved or deleted, a type deleted) is made on
  those settings and written in its place. In the same commit the file as it
  was is kept beside it as `.petalbook/space.yml.unreadable-{time}` (or
  `tags.yml.unreadable-{time}`; the time is UTC, `20261122T163159Z`), so
  nothing is lost. A read writes nothing, so it makes no commit and, in a
  space that follows a remote, no push; a change that needs nothing written
  (a folder without settings is moved or deleted) leaves the file as it is.
  A content that is already kept is not kept again: a pull that brings the
  same file back adds no second copy.
- **Folders.** Moving or deleting a folder is never refused because of such a
  file, and never leaves settings behind: they follow the folder, or go with
  it, in the restored settings.
- **After a crash.** The start that finishes an interrupted move or delete
  goes the same way: it restores, writes and keeps the copy, and commits both.
- **A link or a file in the place of a folder.** Nothing is ever read or
  written through a link, or a file, where `.petalbook` or `.petalbook/types`
  belongs (a pull can bring one, or a person put it there), wherever it leads.
  The settings files and types behind it read as restored from the history
  above (the commit that put the link there does not count as deleting them),
  and the types listed are those the folder had. The next change that writes
  there moves what is in the place of the folder aside, inside the space, as
  `.petalbook.unreadable-{time}` (or `.petalbook/types.unreadable-{time}`),
  commits that, makes the folder again with everything it had (the settings
  as they are read, the other files as they were), and makes the change in
  it. Every read and write there is made in the folder as it was opened, so
  a link put in its place while a file is being read or written is not
  followed either. In a space that follows a remote, that repair is pushed
  before the change that needed it; when its push does not go through (the
  remote moved on, or cannot be reached) it is taken back, the clone is as
  the remote again, and the change is made again on the new tip (or fails as
  any change fails without the network), so a repair is never kept as a
  change made here that was not pushed. A local space puts a link where
  `.petalbook` belongs right that way when it starts, before it writes
  `space.yml`.
- **The server's own files are not in `.petalbook`.** `.petalbook` holds the
  settings and the types only: repository content. The index, the journal of
  what is not committed yet, the notices below and, in single-user mode, the
  state database are kept in the Git directory (`.git/petalbook-state/`;
  `--state-dir` puts the state elsewhere), where no pull or checkout writes,
  so a pull that brings a link where `.petalbook` was takes none of it away.
  The Git directory is the one git uses for the space: for a checkout made
  with `git worktree add` its own one (two checkouts of one repository never
  share state), and for a submodule, a `--separate-git-dir` repository or a
  `.git` link, the folder their `.git` names; it may be on another file
  system than the space. A file of the space (a page, an attachment, a
  setting) is written in its own folder under a new hidden name
  (`.petalbook-w-<random>`, made exclusively, never through a link) and then
  renamed into place, so a save works wherever the Git directory is and
  whatever a pull put anywhere else. Those names are not pages, are kept out
  of the repository by `info/exclude` and are never committed; one a crash
  left is removed when the space starts, unless the repository has a file of
  that name. A space last opened by an earlier version, which kept its state
  in `.petalbook/cache/`, has the server's own files moved there once when it
  starts: the index, the journal, the notices and the state database, and
  only those the repository does not have. A file of `.petalbook/cache/` that
  is committed is the repository's: it is neither moved nor used, and neither
  is the log or shared memory file SQLite keeps beside a database the
  repository has, or beside none. A link there is neither followed nor moved.
  When the state directory is in use already (an earlier version ran after
  this one), the earlier journal is replayed, its state database is kept
  beside the current one as `state-earlier-<UTC>.db` (copied with its log, the
  database last, so a crash never leaves one without the other; what a crash
  left of a copy is removed at the next start, which copies it again from the
  originals) and its drafts are imported where
  the current database has none for that page and person; one that differs
  from the current draft stays in that file, held, for `petalbook draft list
  --database <file>`.

What happened is told in `settingsNotices` of `GET /api/spaces/{space}/types`,
to callers who have `manage` in the space (in single-user mode, always); for
anyone else the key is absent. Nobody is asked to do anything:

```json
{"settingsNotices": [
  {"file": ".petalbook/space.yml", "unreadable": true,
   "since": {"commit": "9f2c…", "time": "2026-11-22T16:20:11Z"},
   "restoredFrom": {"commit": "41ab…", "time": "2026-11-20T09:02:40Z"}},
  {"file": ".petalbook/tags.yml", "unreadable": false,
   "since": {"time": "2026-11-21T08:00:00Z"},
   "restoredFrom": {"commit": "77d0…", "time": "2026-11-19T12:00:00Z"},
   "copy": ".petalbook/tags.yml.unreadable-20261121T081502Z",
   "repairedAt": "2026-11-21T08:15:02Z"}
]}
```

`unreadable` is `true` while the file in the space is still the one that cannot
be used (the server reads the restored settings, and the next change puts the
file right), and `false` for a file that was put right. `since` is the oldest
version in a row that could not be used (without `commit`: the file was changed
in the folder and is in no commit; `time` is then when it was changed).
`restoredFrom` is the version the settings were taken from; it is absent when
the history has none that can be used and the settings started empty, and has
no `commit` when they are what the server itself wrote last and had not
committed yet. `lost` lists the entries of the file as it is (was) that could
not be read even one by one, so they are not in the settings in use and are
only in the file and its copy (`"*"`: nothing of it could be read, as for a
file more than four times as large as the server reads); it is absent when
everything that could be read was kept. `copy` is where the file as it was is
kept. A link or a file that was in the place of `.petalbook` or
`.petalbook/types` has a notice with that `file`, `entry`
`"not a folder"` and its `copy`. A notice of a
repair stays for as long as its copy is in the space: deleting the copy puts
the notice away. `since`, `restoredFrom` and `repairedAt` of a repair are kept
by the server that made it; a copy that came with a pull from another server
is listed with `file` and `copy` only. The list is empty, or absent, when
there is nothing to tell.

One entry of a file that reads can be unusable by itself: an entry that is
not a list of settings (`Tang: yes`), `views` that is not a list, a `type` or
`color` that is not text. It reads as not there, everything else in the file
works, and the change that puts something in its place (binding the folder,
saving its views, setting the colour) writes over it and keeps the file as it
was, as a copy like the ones above. Such a notice has `entry`: `"Tang"` for the
entry, `"Tang: views"` or `"Tang: type"` for a setting of it (`"properties: {key}"`
for a field of a type), with `unreadable` `true` until it is written over and
`false`, with `copy` and `repairedAt`, afterwards.

#### Anchors, aliases and merge keys

`space.yml` and `tags.yml` are written by hand too, and come in with a pull, so
they may hold YAML anchors (`&name`), aliases (`*name`) and merge keys (`<<`).
One rule holds for both files, for everything the server reads from them and
every change it makes to them:

- **Reading.** An alias means what its anchor means, and a mapping with a merge
  key has the keys it merges, wherever the server looks: `folders` and `tags`,
  an entry, its `type` or `color`, its `views`, a view, a filter, a value. With
  `A: {type: &t wenan}` and `B: {type: *t}`, both folders are bound to `wenan`.
- **Cost and limits.** What an anchor means is worked out once, however many
  aliases refer to it, so reading a file costs what is written in it: an entry
  that ten thousand folders share by alias (`Tang: *chapter`) is read as fast
  as one written once, and no number of aliases makes a file unusable. The
  only limits are ones a file that a person keeps does not reach: 100 levels
  deep with the aliases followed, no alias inside its own anchor, and
  4 194 304 keys brought in by merge keys (each mapping with a merge key
  counted by itself: ten thousand folders that each merge a template of four
  hundred keys). A file over one of them cannot be used; see Settings files
  that cannot be used. That is found when the file is loaded, in time that
  grows with its size only.
- **Writing.** A change changes what it targets and nothing else. An entry, a
  list of views or a view that is an alias, carries an anchor or has keys from
  a merge key is first written out in its own place (aliases inside it stay
  aliases), and the change is made there; what else refers to the anchor keeps
  what it had. When the node that carries an anchor is removed or replaced (a
  folder deleted or moved onto, a view removed, a type or colour changed) and
  aliases of it remain, the anchor moves to the first of them and the others
  refer to it there; nothing is copied. Deleting `A` above leaves `B: {type: &t
  wenan}`. The comments of a merge key that is written out stay where it was:
  the one above it above the first key written in its place, the one on its
  line on the line of that key, the one below it below the last of them.
- **Check.** Before the file is written the server checks that every alias
  left in it stands for what it stood for, and that the text reads back as
  what was meant. If not (an anchor name used twice can do that), the change is
  made again with every alias of the file written out. If that does not fit in
  1 MiB the request is `400 invalid` and nothing is changed, on disk or in the
  file; no more is written out to find that than 1 MiB holds.
- **Shared entries stay shared.** When one change takes a setting off many
  entries that are aliases of one anchor (a type is deleted that ten thousand
  folders have through `Tang: *chapter`), the first of them gets what the
  anchor is without that setting, under an anchor of its own (`&chapter-no-type`,
  with a number after it if that name is taken), and the others become aliases
  of that: the file stays as small as it was, and what else refers to the old
  anchor keeps what it had. These are the only aliases the server writes.
  Entries that share through a merge key (`{<<: *chapter}`) are written out
  each for itself, and such a change is `400 invalid` when the result would not
  fit in 1 MiB.

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
from it (`baseRev`, below). A file that cannot be used is the type of its last
version that can, with that version's `rev`. `title` is `""` when the file gives none or gives
something that is not text (show the name or key); a `title` that is not text
stays in the file when a request leaves the title empty. `options` is
there for `select` and `multiselect`. `schema` is the field's schema as the
file has it. A type listed as
`{"name": "…", "title": "", "fields": [], "error": "…"}` is a file beyond the
first 100, or one that cannot be used while the history could not be read for
its earlier version just now.

### `GET /api/spaces/{space}/types`

Needs `read`.

```json
{"types": [/* types, in name order */], "bindings": [{"folder": "Work", "type": "task"}]}
```

`bindings` are all the folders that are bound, in the order of the file.
`settingsNotices` is there when a settings file could not be used (see Settings
files that cannot be used). Only
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

Errors: `400 invalid` for anything above that is out of limits or malformed;
`409 conflict` when `baseRev` is
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
file of a type that cannot be used is kept as a copy when the type is deleted
(see Settings files that cannot be used); with no version that can be used it
is no type, and is deleted with `baseRev` `""`.

### `PUT /api/spaces/{space}/folders/type`

Needs `manage`.

```json
{"folder": "Work", "type": "task"}
```

Binds a folder (and, by inheritance, the folders below it) to a type, or
removes the binding when `type` is `""`. `200` with `{"folder": "Work",
"type": "task"}`, the folder as it is spelled on disk. `404 not_found` when
the folder does not exist; `400 invalid` when the type does not exist, or the
entry of the folder in `space.yml` is not a list of settings. Pages are not changed: their
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
read and written the way bindings and tag colours are (a file that cannot be
used is restored from the history, see Settings files that cannot be used),
and a change is journaled and committed like any other, in a space that
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
write that names such an id changes that view and gives it a new one. Only
the first 20 views of an entry are read (the rest of a longer list written by
hand stays in the file, after the views, and is not in any answer). A value
of a key the server does not know that stands for more than 10 000 YAML nodes
(aliases counted each time they are used) is left out of the answer, and stays
in the file. Aliases and merge keys are read as what they stand for (see
Anchors, aliases and merge keys).

### `GET /api/spaces/{space}/views?folder={path}` or `?tag={tag}`

Needs `read`. Exactly one of `folder` (the root is `folder=`) and `tag`.

```json
{"views": [/* views, the first being the default */], "rev": "6a6d14f7db61e4f5578c38cad4f75dfc5a97a9a6"}
```

`rev` is a digest of the views as the server reads them (a list with none has
one too), so a change to the file by hand is a new rev as well. A folder that
does not exist has no views (it is not looked for on disk). A settings file
that cannot be used gives those of its last version that can. `400 invalid` for neither or both of `folder`
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
`tags.yml` larger than the 1 MiB the server reads (nothing is written, so the
settings stay readable), or an entry that is not a list of settings. A view
that carries an anchor, or is written with an alias or a merge key, can be
changed and removed like any other: what else in the file refers to the anchor
keeps what it means (see Anchors, aliases and merge keys). The body of the request is
at most 20 views at their largest and 512 KiB more (`413` above that), and the
list is refused as soon as it has a 21st entry, before any of them is built.
A view that the request gives back as it is stored is left as it is in the file,
also what the server could not read of it (a scalar where a list
belongs), and what is written as an alias stays one; a key is removed or rewritten only when the request changes it. `[]` removes
the views from every writing of the folder in `space.yml`, so none comes back.
`404 not_found` for a folder that does not exist (a tag need not be in use).
A write that changes nothing writes nothing.

## Home: the daily text and favourites

### The daily text

A space shows one short text a day on its home page. Where it comes from is
decided, first match wins, by:

1. **the space's own source**, written under `daily` in `.petalbook/space.yml`
   (it is part of the space and is read and written like the bindings and the
   views: a file that cannot be used is restored from its history, anchors and
   aliases are kept, a change is journaled and committed like any other);
2. **an external interface the space chose** among those the administrators
   set up (accounts mode);
3. **the server's default**, which administrators set, unless the space opted
   out (accounts mode).

With none of them the answer is `{"page": null, "reason": "no_source"}` and the
block is not shown. A space with a source of its own that meets no page is
`no_match` (it does not fall through: it was asked for). Single-user mode has
only the first.

```yaml
daily:
  folder: 文案        # or tag: … or type: …, one of the three
  filter: [{key: status, op: in, values: [定稿]}]   # optional, as the filter of a listing
```

`folder` means the folder and the folders below it; `type` is the effective
type (the header's, else the folder's, see Page types). **By language:**
`daily: {zh: {folder: 文案}, en: {tag: quotes}}`. A source written beside the
language keys (`daily: {folder: x, zh: {…}}`) is "all languages": the fallback
for the languages that have none of their own; written alone it is the source
of every language. For a language the source is that of the language, else
that of the language without its region (`zh` for `zh-CN`), else "all
languages", else, so that a language nobody wrote a source for still gets a
text, the first by the order of the codes. Moving or renaming a folder carries the `folder` of
every source with it, in the same commit as its bindings (and, after a crash,
at the next start). Deleting a folder leaves the source as it is; it then meets
no page.

#### `GET /api/spaces/{space}/daily/settings`, `PUT …/daily/settings`

`GET` needs `read`, `PUT` needs `manage`.

```json
{"daily": {"folder": "文案", "filter": [{"key": "status", "op": "in", "values": ["定稿"]}]}}
{"daily": {"zh": {"folder": "文案", "filter": []}, "en-us": {"tag": "quotes", "filter": []}}}
{"daily": {"folder": "all", "filter": [], "zh": {"tag": "诗", "filter": []}}}
{"daily": null}
```

`PUT` takes the same object and answers with it as it is now; `null` removes
the source. Language codes are lower-cased. `400 invalid` for none of
`folder`, `tag`, `type` or more than one, an unknown key, a tag that is no tag,
a type that does not exist, a filter that is not valid (as for a listing, at
most 10), a language key that is not a code (`zh`, `en`, `zh-CN`) or given twice, more than
20 languages, or a body without `daily`; `404 not_found` for a folder that is
not there. A source that is already as asked writes nothing.

#### `GET /api/spaces/{space}/daily?date=2026-11-23&skip=0&lang=zh`

Needs `read`. `date` is the day in the browser's time zone (today in UTC when
left out); `skip` (0 up to 1 000) asks for another text than the day's and
only affects what the asker is shown; `lang` is the language of the interface
(`zh`, `en`, `zh-CN`; left out, no language); `tz` is the browser's offset in
minutes east of UTC (`-480` for UTC-8, from -840 to 840).

The answer carries `Cache-Control: private, max-age=N` and an `ETag`, and a
request with a matching `If-None-Match` gets `304`. `N` is how long it stays the same:
until the end of `date` where the browser is (from `tz`; `300` when `tz` is
left out), until the end of the refresh period for an interface that
refreshes by the hour or the minute, at least 60 and at most 86 400 seconds, and
60 for an answer with nothing to show or a text that is `stale`.

From the space's own source (and that is also how a page of this space is
returned):

```json
{"page": {"id": "…", "path": "文案/暮雪.md", "title": "暮雪", "label": "暮雪", "…": "(a page summary)",
          "body": "《暮雪》｜…\n", "bodyTruncated": false},
 "count": 12, "origin": "space"}
```

`page` is a page summary (see Page summary) with the Markdown `body` (only
that one file is read) cut to 2 000 characters (`bodyTruncated` says it was).
`count` is how many pages meet the source. Which one is picked: a number is
made from the space and the day (the first 8 bytes of a SHA-256) plus `skip`,
and the page is the one at that number modulo `count` among the pages that meet
the source, in the order of the index; both are read from the index (the
cost is the pages that meet the condition, tens of milliseconds for 50 000
pages). What a day showed is remembered in memory (per source, day and `skip`),
so pages that are added or deleted during the day do not change it; a page that
is gone is replaced by another pick. The picks everybody sees (`skip` 0) and the
others are remembered apart, 512 of each in a space; when either is full, the
half whose days are farthest from today is forgotten, so asking for other texts
or other days never changes today's.

From the administrators' settings the answer has no page but a `quote`:

```json
{"page": null, "quote": {"text": "…", "source": "…", "author": "…", "link": "https://…",
                         "fetched": "2026-11-23T08:00:00Z", "stale": false}, "origin": "external"}
{"page": null, "quote": {"text": "…", "source": "…"}, "origin": "site"}
```

`origin` is `external` for an interface, `site` for the default (a line the
administrators pasted, or a page of the space they named: its body as `text`,
its title as `source`; shown to everybody, members of that space or not).

Nothing to show: `{"page": null, "reason": "no_source" | "no_match" | "unavailable" | "out_of_range"}`;
`unavailable` is an interface that did not answer or a default space that is gone;
`out_of_range` is a `date` an interface is not asked about: only today,
yesterday and tomorrow are (today where the browser is, by `tz`; in UTC
without it), since each other day would be a request and a row kept. A client
hides the block for any of them. `429 rate_limited` (with `Retry-After`) when
another text from an interface is asked for too often. `400 invalid` for a
`date` that is not a day (`YYYY-MM-DD`) from 1970 to 2261, a `skip` that is not
a number from 0 up, or a `lang` that cannot be; `400 skip_too_large` for a
`skip` above 1 000 (see "Daily text refusals").

#### `GET /api/spaces/{space}/daily/prefs`, `PUT …/daily/prefs`

Accounts mode only (single-user mode answers `404`). `GET` needs `read`, `PUT`
needs `manage`: the owner of a space chooses what it takes of the
administrators' settings. Owners never enter addresses; they choose among the
interfaces that are set up.

```json
{"inherit": true, "apis": ["hitokoto"], "available": [{"id": "hitokoto", "name": "一言", "lang": "zh"}],
 "languages": ["zh", "en"], "allowed": {"own": true, "api": true}, "default": {"kind": "list"}}
```

`inherit` is whether the space shows the server's default when it has nothing
else; `apis` are the ids of the interfaces it uses (for a language, the one
made for it, else one that takes any language, else any); `available` are all
that are set up (never their addresses); `default` is `null` when the
administrators set none. `languages` are the language codes the settings pages
have a tab for (the administrators' `languages`, below; nothing is limited to
them). `allowed` is what the administrators let spaces do (below): a field that
is `false` is set for the whole server, and what the space has there is saved
when asked but has no effect until it is `true` again. `PUT` takes `{"inherit": true, "apis": []}` (both are
needed) and answers as `GET`; `400 invalid` for an id that is not in
`available`; it saves `apis` even when `allowed.api` is `false`.

### The administrators' settings of the daily text

All `admin` only, accounts mode only. They are in the state database, not in
any space. The server is the only one that ever asks an external interface:
browsers never do (a key stays on the server, an interface is asked once a
period however many people look, everybody sees the same text, and the
protection of the network is in one place).

#### `GET /api/admin/daily`, `PUT /api/admin/daily`

```json
{"default": {"kind": "space", "space": "01JD3K8Q7Z", "source": {"folder": "文案", "zh": {"tag": "诗"}}},
 "allow": {"own": true, "api": true},
 "languages": ["zh", "en"],
 "apis": [ /* interfaces, below */ ],
 "presets": [ /* interfaces to start a form from, below */ ],
 "lines": {"total": 6, "byLang": {"zh": 3, "en": 2, "": 1}, "max": 5000}}
```

`PUT` takes any of `default`, `languages` and `allow` (what it leaves out stays)
and answers as `GET`; `lines`, `apis` and `providers` are read-only here. A
`hosts` field sent by an older client is ignored.

- `default` is `null`, `{"kind": "list"}` (the lines below),
  `{"kind": "space", "space": id, "source": …}` where `source` is a source or
  sources by language as above, checked in that space (choosing a space shows
  its content to everybody; a client says so), or
  `{"kind": "api", "apis": {"": "quotable", "zh": "hitokoto"}}`: an
  interface id per language, `""` for all languages not listed. Each must be an
  interface of `apis` (`400 invalid` with `error: "api_not_found"` otherwise);
  a disabled or unanswering interface gives the daily reason `unavailable`.
  The interface is used exactly as when a space picks it (same cache, refresh
  and rate limit), and the `allow.api` lock does not apply to it: it limits
  what spaces may choose, not the administrators' own default.
- `languages` are the languages the settings pages have a tab for besides
  "all languages", as BCP 47 language tags in canonical form (`zh-Hans`,
  `zh-Hant`, `en`, `pt-BR`; each once, at most 30), `["zh-Hans", "en"]` to begin
  with. Every language tag anywhere in the daily text (this list, per-language
  sources, interfaces, lines, the `lang` of `GET …/daily`) must be a well-formed
  tag whose language subtag is in the IANA registry: `cn`, `english` or
  `zh_CN` are refused with `400 invalid`, `error: "bad_language"`. The server
  writes tags in canonical form (`ZH-cn` is stored as `zh-CN`). A reader's
  language is matched to the configured ones the usual BCP 47 way (`zh-CN` and
  `zh-SG` find `zh-Hans`, `zh-TW` and `zh-HK` find `zh-Hant`, `en-GB` finds
  `en`), then "all languages". `GET /api/admin/daily` also returns
  `knownLanguages`: `[{"tag": "zh-Hans", "name": "中文（简体）", "english": "Chinese (Simplified)"}, …]`,
  the common languages a client offers to pick from (any valid tag may still be
  typed). Values saved before this rule (`zh`, `pt-br`) are read as their
  canonical tags (`zh` → `zh-Hans`, `pt-br` → `pt-BR`) and written back in
  that form on the next save.
- `allow` is what the spaces may do: `own` is whether a space may have a source
  of its own (`daily` in its `space.yml`), `api` whether it may choose an
  external interface. Both are `true` until set; `PUT` takes either or both
  (`{"own": false}` leaves `api` as it is; `400 invalid` for neither, or a value
  that is not a boolean). A lock does not remove anything: the space's `daily`
  stays in `space.yml` and its choice of interfaces stays, they are ignored, and
  the order of `GET …/daily` goes on to the next one (own, interface, default)
  as if they were not set; unlocked, they count again. A space's
  `PUT …/daily/settings` and `PUT …/daily/prefs` still save while locked.
  `GET` and `PUT …/daily/settings` say whether the source is in effect:
  `{"daily": …, "inEffect": false}`. In single-user mode there are no
  administrators and everything is in effect.

**Languages everywhere.** A space's `daily`, the default's `source`, the lines
and the interfaces are all by language, with "all languages" (an unkeyed
source, lines with `lang` `""`, an interface with `lang` `""`) as the fallback
for the languages that have none of their own, as in `GET …/daily/settings`
above.

#### Interfaces: `PUT /api/admin/daily/apis/{id}`, `DELETE …/apis/{id}`

Any number of interfaces (at most 50). An interface in `apis`:

```json
{"id": "hitokoto", "name": "一言", "lang": "zh",
 "url": "https://international.v1.hitokoto.cn/?max_length=80&d={date}",
 "headers": [{"name": "X-Api-Key", "set": true}],
 "text": "hitokoto", "source": "from", "author": "from_who",
 "link": "https://hitokoto.cn?uuid={uuid}",
 "refresh": "daily", "refreshMinutes": 0, "skipNew": true, "rps": 20, "enabled": true}
```

- `id`: 1 to 32 lower-case letters, digits, `-`, `_`; it is the one in the
  address. `name` at most 100 characters. `lang` is the language it answers
  in, `""` for every language.
- `url` is fetched with `GET`. It is `https`, with no user name, and `{lang}`
  (the language asked for, `en` when none) and `{date}` (the day, `2026-11-23`)
  may be in its path or query, not in its scheme, user, host or port (as the
  address is written, in any case); they are filled in only as a checked
  language code and day, escaped, and the request may go only to the host
  (and port) of `url` as written: **there is no separate list of trusted
  hosts** — saving an interface is what trusts its host, and only for that
  interface. An address that is an IP address
  that is not public, `localhost` and its subdomains are refused when it is saved;
  whatever a name resolves to is checked again when the connection is made (below).
- `headers` are request headers, at most 10. A value is **never returned**, and
  never reaches any output (below). It is encrypted with the server's key
  (`DATA/secret.key`, or `PETALBOOK_SECRET_KEY`), bound to the exact request it
  was given for: the interface's `id`, the header's name and the `url` exactly
  as written (scheme, host, port, path, query and placeholders). The interface
  answers with `{"name": …, "set": true}`. In a request
  `{"name": "X-Api-Key", "value": "…"}` sets or replaces a value, and
  `{"name": "X-Api-Key"}` keeps the one it has: `400 header_missing` when it has
  none (another `id` has none), `400 header_reenter` when `url` is not, to the
  character, the one the value was given for. Any change of the address, even
  of its path or query, drops every kept value of the interface: they are
  given again by whoever has them. `text`, `source`, `author` and `link` may
  change freely. A header that is left out is removed. `Host`, `Content-Length`,
  `Content-Type`, `Transfer-Encoding`, `Connection` and the like cannot be set.
- `text` (needed), `source` and `author` are paths in the JSON answer: names
  and positions with dots and brackets (`hitokoto`, `data.0.q`, `[0].content`);
  the value must be text or a number; the text is cut to 2 000 characters, the
  others to 200.
- `link` is an optional template of a link shown with the text (an attribution):
  `{lang}` and `{date}` are those of the request, any other `{path}` the value at
  that path of the answer (URL-escaped); it is `http` or `https`, and is left out
  when a value it needs is missing. It never takes a request header:
  `400 link_uses_header` (`field` `"link"`) for a placeholder any part of whose
  path is `header`, `headers` or the name of one of the interface's headers
  (`{headers.Authorization}`, `{args.X-Api-Key}`), in any case.

**No header value reaches an output.** Each value the server shows (`text`,
`source`, `author`, each value of `link`, the `raw` of a trial) is taken in
this order: the value at its path, decoded from JSON (escapes such as `\/` and
`A` undone); checked; cut; and, in a link, escaped. A value that contains a
header's value (of 4 characters and more), or any 8 characters in a row of
one, in any case, as it is or with its URL escapes undone (`%2B`, `+` read as a
space), is refused: a trial says so (`path_reveals_secret`), and an answer to
readers with such a value is not used at all (as a failure: the last good
text, or `unavailable`). The old blotting out of values (`•••`) is kept as a
second line only.
- `refresh` is how long the text everybody sees stays the same: `"daily"` (the day
  of the request: the `date` parameter), `"hourly"`, or `"minutes"` with `refreshMinutes`
  1 to 1440 (by the server's clock). `skipNew` says "another" (`skip` above 0)
  asks the interface for a new text each time (at most `skip` 100); without it
  "another" is the same text. `rps` is the most requests a second this server
  sends the interface, 0.01 to 100, 1 when left out: a token bucket (as many
  tokens as `rps`, at least one) shared by everybody who looks and every
  language. With the bucket empty nothing waits: the last text the interface said
  is used. `enabled` (true when a new interface leaves it out) says whether it may
  be chosen by spaces and is used.

`PUT` makes the interface or replaces it, and answers as
`GET /api/admin/daily`. Changes of the settings (these, `PUT /api/admin/daily`
and `DELETE`) are made one at a time, in a transaction that holds the
settings: two administrators saving at once both keep their change. `DELETE` removes it and
answers the same; `404` for an id that is not there. `400 invalid` for anything
above that breaks a rule (the message says which), or one of the codes of
"Daily text refusals".

**Built-in interfaces** (`providers`): services the server knows how to ask,
so an administrator only switches one on — no address, paths or language
codes to fill in. `GET /api/admin/daily` lists them as
`{"id": "hitokoto", "name": "一言", "languages": ["zh-Hans"], "site": "https://hitokoto.cn", "note": "…", "enabled": false, "refresh": "daily", "rps": 20, "skipNew": true}`.
`PUT /api/admin/daily/providers/{id}` takes `{"enabled": true}` and optionally
`refresh`, `minutes`, `rps` (not above the service's own limit) and
`skipNew`; needs admin; answers as `GET /api/admin/daily`. An enabled built-in
interface appears in `apis` with `"builtin": true` and can be chosen by spaces
and as the site default like any other; its address, paths, link and language
mapping cannot be changed (`400 invalid`, `error: "builtin"`). None is enabled
until an administrator enables it, and no service is contacted before that.
Today: `hitokoto` (一言, the international line; `zh-Hans`, `zh-Hant`; link
back to the sentence as the service asks) and `quotable` (Quotable; `en`).

**Language codes in the address**: `{lang}` is filled with the reader's
language as matched (a canonical BCP 47 tag, e.g. `zh-Hans`). Services that
want another form are given a `langMap` on the interface,
`{"zh-Hans": "zh", "en": "en"}`, applied before filling in (built-in
interfaces have theirs set; custom interfaces may set one, keys checked as
language tags, values 1–35 characters of `[A-Za-z0-9_-]`).

#### `POST /api/admin/daily/try`

```json
{"api": {"id": "hitokoto", "name": "…", "url": "…", "headers": [{"name": "X-Api-Key"}], "text": "hitokoto"},
 "lang": "zh", "date": "2026-11-23"}
```

Makes one request now with the interface as it is in the form (saved or not;
a header without a value uses the value saved for that `id` and that exact
`url` only), at the same rules as any request, and shows what came back. It goes only to
the host of the address in the form, under the same public-address checks. It
keeps nothing and is limited to a few a second (`429`). `400 invalid`, or a
code of "Daily text refusals", when the interface itself breaks a rule.

A trial that worked, or failed for a reason that has no code (the service did
not answer, answered with an error status, or said a header's value back
outside every path), is a `200`:

```json
{"ok": true, "status": 200, "url": "https://…?l=zh", "raw": "{\"hitokoto\": …", "rawTruncated": false,
 "quote": {"text": "…", "source": "…", "author": "…", "link": "https://hitokoto.cn?uuid=…"}}
{"ok": false, "message": "status 503", "status": 503, "url": "…"}
{"ok": false, "status": 200, "url": "…", "quote": {"text": "…"},
 "message": "the answer repeats the value of a request header, so it is not shown"}
```

A trial whose answer is refused is a `400` with the code of the refusal, what
it is about beside it, and what the trial showed in `result` (the shape
above). With several path errors, the code and `field` are those of the first:
a `path_reveals_secret` before a `path_error`, in the order text, source,
author, link.

```json
{"error": "path_error", "message": "no text or number at from_x", "field": "source",
 "result": {"ok": false, "status": 200, "url": "…", "raw": "…", "quote": {"text": "…"},
            "pathErrors": [{"field": "source", "code": "path_error", "message": "no text or number at from_x"},
                           {"field": "link", "code": "path_error", "message": "a value the link needs is not in the answer"}]}}
{"error": "host_not_allowed", "message": "the host api.example.org is not on the list of hosts", "host": "api.example.org",
 "result": {"ok": false, "url": "…", "message": "…"}}
```

`raw` is the start of the answer (4 000 characters), shown only when it is
valid JSON and none of its decoded texts, names and numbers repeats a request
header's value (see above); otherwise it is left out, and `message` says why.
`pathErrors` is a list of `{field, code, message}` (`field` one of `text`,
`source`, `author`, `link`; `code` `path_error` or `path_reveals_secret`); the
fields that were found are still in `quote`. `message` says what went wrong
with the request or the answer.

#### Daily text refusals

The code is the `error` of the error body; what the refusal is about is beside
it. All are `400`.

| `error` | beside it | when |
|---|---|---|
| `header_reenter` | `header` | a header without a value whose kept value was given for another `url` |
| `header_missing` | `header` | a header without a value, and none is kept for it (for this `id`) |
| `not_json` | | a trial's answer does not declare JSON, or is not valid JSON |
| `too_large` | | a trial's answer is larger than 64 KB |
| `path_error` | `field` | a trial's path (or a value the link needs) finds no text or number |
| `path_reveals_secret` | `field` | a trial's path (or a value of the link) would show a request header's value |
| `link_uses_header` | `field` (`"link"`) | a link template with a request header in it (saving or trying) |
| `skip_too_large` | | `GET …/daily` with a `skip` above 1 000 |

A `date` that is a day but not today, yesterday or tomorrow, for an external
interface, is not a refusal: `GET …/daily` answers `200` with `"reason":
"out_of_range"`. A `date` that is not a day is `400 invalid`.

**How it is fetched.** Only the interfaces the administrators wrote, only from
`https`, only to the host of the interface's own address. The address a name resolves to is checked
when the connection is made, for every address (so a name that answers
differently the second time is no way round it): loopback, private, link-local
(the cloud metadata address included), unspecified, multicast, carrier-grade
NAT, documentation and benchmarking addresses are refused, an IPv4 address
written as IPv6 being judged as the IPv4 one, always, for every request and every
trial. No redirect is followed, no proxy used. The answer must come within 5
seconds, be `200`, declare JSON, and be at most 64 KB.

**What is kept.** (1) In memory, by interface (as it is asked: its id, address,
paths and header names), language, refresh period and skip: inside a period
nobody asks again, however many look; requests in flight for the same item are
one, and that one request is not cut short (nor counted as a failure) when the
reader who started it goes away. The language is the one the answer can differ
in: none for an address without `{lang}`, the interface's own `lang` when it
has one, else the reader's language or its base when it is one of
`languages`, else the first of them. What is kept by interface and language
(last answers, waits after failures, rate limits) is bounded (1 024 of each;
the oldest go). (2) In the state database, the same: a restart asks for nothing
again (rows older than 7 days are removed, and at most 2 000 rows of an
interface are kept, the newest). (3) In the browser, by `GET …/daily` (below).
When an interface fails (an error, a bad answer, no answer in time) the last
text it said in that language is given, marked `"stale": true` with the time it
was got (`"fetched"`); with none, or none from the last 7 days, the answer is
`unavailable` and the block is hidden. After a failure the interface is not asked
again for 30 seconds, then 1, 2, 4 … minutes up to an hour, never at every
request; a success clears the wait. A space's own source and the default
that is a space or lines are cheap (the index, a row) and use only the memory of
the day and the browser's.

#### `PUT /api/admin/daily/lines`, `DELETE /api/admin/daily/lines?lang=zh`

The lines are a source of the default that is stored here: pasted or uploaded
text, each entry with the language it is in (`""` for any language). `PUT`
replaces the lines of one language:

```json
{"lang": "zh", "text": "山色空蒙雨亦奇\t苏轼\n行到水穷处", "format": "lines"}
```

`format` is `lines` (each line that is not blank is an entry; a tab separates the
text from where it is from) or `markdown` (entries are cut at the lines that
are `---`, or at the blank lines when there are none). Entries are at most 8 000
bytes, their source 200 characters, and the server keeps at most 5 000 in all.
`DELETE` removes the lines of one language. Both answer as `GET /api/admin/daily`.
For a language the lines used are those of the language, else of its
language without region, else those for any language, else the first
language that has some; the line is picked as a page is (from the day and
`skip`, modulo how many there are).

### Favourites

A person's own list of pages, folders and saved views in a space, in the order
they put them. It is kept in the state database, not in the space (nothing is
committed), for a person and a space. At most 200 in a space. It is deleted
with the space or the user; a person who leaves a space keeps theirs in the
database but they are not returned, and are back if the person is added again.
(An administrator who is not a member of the space has none, and cannot add
any: `403 forbidden`.) Everything here needs `read` and works in both modes.

```json
{"items": [
  {"kind": "page", "target": "01JD3K8Q7Z", "name": "暮雪", "path": "Tang/暮雪.md", "missing": false, "created": "2026-11-23T08:30:00Z"},
  {"kind": "folder", "target": "Tang/七言", "name": "七言", "path": "Tang/七言", "missing": false, "created": "…"},
  {"kind": "view", "target": "folder:Tang#k3f9a1", "name": "按作者", "of": "Tang", "ofKind": "folder", "missing": false, "created": "…"}
 ], "limit": 200}
```

`target` is a page id, a folder path as it is on disk, or a saved view as
`folder:<path>#<view id>` or `tag:<tag>#<view id>`. `name` is a page's label, a
folder's last name, a view's name; `missing` is true when the page, the folder
or the view is gone (the name is then `""` for a page or a view and the folder's
name for a folder). The names and `missing` of the whole list are looked up in
the index and the settings in one go: no page file is read.

- `GET /api/spaces/{space}/favorites` answers the list.
- `POST …/favorites` with `{"kind": "page", "target": "…"}` adds one at the end
  (one that is there stays where it is) and answers the list. `404` for a page,
  folder or view that is not there, `400 invalid` for a target that cannot be
  one, the root folder, and a 201st favourite.
- `DELETE …/favorites?kind=page&target=…` takes one off (target URL-encoded;
  also one that is `missing`; one that is not there is not an error) and
  answers the list.
- `PUT …/favorites/order` with `{"items": [{"kind": "page", "target": "…"}, …]}`
  puts the favourites in that order: those named first, the rest after in the
  order they had; names that are not favourites are ignored. Answers the list.

Moving or renaming a folder (see `POST …/folders/move`) changes, once it has
succeeded, the paths of the favourites of every person in the space: of the
folder, of the folders below it, and of the saved views of those folders. A
person who already has a favourite at the new path keeps it, and their old one
goes. The move is recorded in the state database before it is made, and the
record goes with the change of the favourites, whether or not the client is
still there; a record left by a crash or a failure is settled the next time
the favourites of the space are read or a folder of it is moved. All records
of a space are settled together, oldest first, in one transaction (all or
none): a move is taken as made when its folder is gone from the old place,
whatever its age, or a later move put a folder there, or a folder is at both
places (the one at the old place is new: it keeps none of the old one's
favourites); its favourites go to where the folder is, or to its new place
from which a later move carries them on, so moves A to B and B to C take A's
favourites to C, settled in any order. A record of a folder still where it was
and not at the new place is forgotten after ten minutes. A move onto a folder
that is there is refused and not recorded. Deleting a folder leaves the
favourites, now `missing`.

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
bytes or has a line break in it. A `.petalbook/tags.yml` that cannot be used
does not stop the change (see Settings files that cannot be used). The
tag in the address is percent-encoded, so it may contain `/`. The tag need
not be in use (it is listed, with its colour, once a page uses it). Colours are
kept in `.petalbook/tags.yml` in the space, so they travel with the
repository; a change is committed like any other. The same entry of the file
holds the tag's saved views (see Saved views), which setting or removing the
colour leaves alone. A colour written as an alias (`color: *c`) is the colour
of its anchor, and changing or removing the colour of one tag never changes
another's (see Anchors, aliases and merge keys).

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

`q` keeps the spaces whose name contains it, ignoring case the same way on SQLite and
PostgreSQL: the server compares Unicode case foldings of both (a column of the
state database, `name_key`, written with every name), so `STRASSE` finds
`Straße`, `唐诗` finds `唐诗 Tang`, and `İ` is the letter `i` with a dot above it
(it is found by `i`, not by `I`). `limit` is 1 to
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
