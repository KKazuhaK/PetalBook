# HTTP API

Status: milestone 1.2 (sign-in, several spaces, drafts kept on the server).
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
  actions the signed-in user may not take), `rate_limited` (429, with a
  `Retry-After` header in seconds), `internal` (500).
- **Cross-site requests.** These rules keep other web sites from using a
  browser to change pages, and in loopback-only mode from reading them. They
  apply to every request. With `--insecure-no-auth` a site that controls a DNS
  name can still make a browser read pages (DNS rebinding); do not use that
  flag on a network you do not trust until sign-in exists.
  - Every `POST`, `PUT`, `DELETE` and `PATCH` must send
    `Content-Type: application/json`, even with no body, or the answer is
    `415 invalid`.
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
  of answers `404 not_found`, the same as a space that does not exist.

## Meta

### `GET /api/meta`

No session needed. `spaces` lists the spaces the caller can open (empty when
not signed in), in the shape of `GET /api/spaces`.

```json
{
  "version": "dev",
  "spaces": [{"id": "default", "name": "My notes", "role": "owner", "personal": true}]
}
```

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

`{"users": [{…}]}`, ordered by `created`.

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
{"id": "01JD3K8Q7Z", "name": "My notes", "role": "owner", "personal": true}
```

`role` is the caller's role in the space; in this milestone always `owner`.
`personal` marks the space made together with the account.

### `GET /api/spaces`

`{"spaces": [{…}]}`: the caller's spaces, the personal one first, then by
name.

### `POST /api/spaces`

Accounts mode. `{"name": "Tang"}` (1–80 characters). `201` with the space.
The server makes the Git repository. The caller is its owner. `403 forbidden` when
the caller already owns as many spaces as the server allows (`--max-spaces`,
20 by default).

### `PATCH /api/spaces/{space}`

Accounts mode. `{"name": "…"}`. `200` with the space.

## Pages

### Page summary

Used in listings. `title`, `subtitle` and `source` are absent when the page
has none. `source` says where the text comes from (a person, a book, an
address); it is free text from the header field of the same name.
`label` is always present: the title, or for an untitled page the first line
of the body cut to about 30 characters (`untitled` says which). It is an
empty string for an untitled page with an empty body. `created` is absent
when the header has none. `updated` is when the file was last written.

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
  "created": "2025-12-03",
  "updated": "2026-11-21T08:30:00Z",
  "excerpt": "first 200 characters of the body as plain text"
}
```

### Page

Returned by get, create, save and commit. `meta` is the header as an object,
in file order, including fields the server does not know about; `id` is in it.
`body` is the Markdown after the header. `GET` adds `draft` when the caller
has one for the page (see Drafts); `meta` and `body` are always the file's.

```json
{
  "id": "01JD3K8Q7Z",
  "path": "Tang/暮雪.md",
  "folder": "Tang",
  "rev": "3b18e512dba79e4c8300dd08aeb37f8e728b8dad",
  "updated": "2026-11-21T08:30:00Z",
  "meta": {"id": "01JD3K8Q7Z", "title": "暮雪", "tags": ["玉兰"], "created": "2025-12-03"},
  "body": "…\n"
}
```

### `GET /api/spaces/{space}/pages`

Query parameters, all optional:

| name | meaning |
|---|---|
| `folder` | only pages directly in this folder (empty string = root) |
| `recursive` | `true` to include subfolders of `folder` |
| `tag` | only pages with this tag (repeatable: all must match) |
| `q` | full-text search over title, subtitle, source and body |
| `sort` | `updated` (default), `created`, `title`; prefix `-` for descending. Default is `-updated`. With `q` and no `sort`, results are ordered by relevance |
| `limit` | 1–200, default 50 |
| `cursor` | the `next` value of the previous response |

```json
{"items": [/* page summaries */], "next": "opaque-or-empty"}
```

`next` is an empty string when there are no more results. Sorting by title
puts untitled pages last in both directions, ordered by `created`; titles
compare by code point, case-insensitively. Search terms separated by spaces
must all match; a term matches as a run of adjacent characters and does not
match across punctuation or a line break.

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

### Drafts

While someone edits, the app does not rewrite the page file on every pause. It
stores a draft: one per page and user, kept in the server's database, not in
the space. Nobody else sees it, and lists and search keep showing the file.
A draft becomes the file's content, and a Git commit, when it is committed:

- the app asks for it (the user leaves the page or chooses to save a version);
- or the server does it by itself once the draft has not changed for
  2 minutes, or 10 minutes after the draft was first stored
  (`--draft-idle`, `--draft-max`).

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

`204`. See below for `?version=`.

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

### `POST /api/spaces/{space}/pages/{id}/move`

```json
{"folder": "随笔/2026"}
```

Moves the file, creating the folder if needed. Returns the page.

### `DELETE /api/spaces/{space}/pages/{id}`

`204`. The file is removed from the working tree; it remains in Git history.

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
`400 invalid` for a name starting with a dot, or `assets` at the root (that
name is reserved for attachments).

### `GET /api/spaces/{space}/tags`

```json
{"tags": [{"tag": "栀子", "pages": 41}]}
```

Ordered by count, descending.

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
