# Reserved: public publishing and tables

## 14. Reserved: public publishing

Not being done for now. The goal is to publish chosen pages publicly, with a
choice of publishing targets:

- **Served directly by this service**: read-only pages, no sign-in needed, with
  addresses of the form `/p/<space>/<slug>`.
- **GitHub Pages**: generate static HTML for the selected pages and push it to a
  public repository.

The conventions to be kept from now on for this:

- The header keeps a `publish` field; when it is absent or `false`, the page is
  never made public.
- The header keeps a `slug` field as the public address; when it is missing, it
  is generated from the title.
- Links between pages are a relative path plus the target's ID (for the form see
  the 2.4 contract in [phase-2.md](milestones/phase-2.md)), and on publishing they are
  replaced by public addresses according to the ID. A link to an unpublished page
  degrades to plain text, and does not leak anything beyond the title.
- Attachments are referenced by relative path, and the address carries no token,
  so that on publishing they can be copied unchanged to the public target.
- The rendering of Markdown to HTML needs an implementation that does not depend
  on the editor, shared by the server and static generation.
- `space.yml` keeps a `publish` section, recording the publishing target and the
  theme.
- Publishing is an explicit action of the space owner, and the UI clearly shows
  which pages are currently public.

## 15. Reserved: tables

Not being done for now. The plan is, apart from documents, to make **tables**
another kind of content in a space: a complete spreadsheet (rows, columns,
types, sorting and filtering), not a Markdown table in the body of a document,
and not the view in [editor-and-frontend.md §8.4](editor-and-frontend.md#84-folder-view) that
"lays out the pages of a folder as a table".

Direction (to be refined when the time comes):

- **A table is a file in the space**, and like a page it follows Git: it has
  history, can be compared, and can be viewed directly in the remote repository.
  The data uses a row-based plain text format (CSV is preferred, one record per
  line, so changing one cell touches only one line), and the column definitions
  (name, type, options, width, order) go in an accompanying description file or
  in the file header, not mixed into the data.
- **At the same level as pages**: it appears in the folder tree, lists, search,
  recent, and tags; it has its own ID, its address is located by ID like a page's,
  and renaming and moving do not affect the links pointing to it.
- **Editing goes through the same save flow**: drafts, commits, the `baseRev`
  conflict check, and remote sync. When several people change one table at the
  same time, conflicts are judged per row and per cell, not for the whole file.
- **Pages can reference tables** (embed a view or link to it), and on publishing
  the same rules of [reserved.md §14](reserved.md#14-reserved-public-publishing) apply.

The conventions to be kept from now on for this:

- **"Page" is not the same as "content in a space".** Everywhere in the API and
  the index that lists content (page summaries, search, folder counts), it must
  be possible to add a kind field later; it is not added now, but the frontend
  must not assume that every item in a list is a Markdown page. The `type` that
  already exists in the page summary is the page type
  ([data-model.md §3.3](data-model.md#33-organization)), and is not used to express the kind.
- **The format and allocation of IDs are common to all kinds**, and an ID in one
  space is unique across all kinds.
- **Addresses go by ID, not by kind**: `/s/<space>/p/<id>` can later open any
  kind; no separate set of addresses is created for tables.
- **Files other than `.md` are for now only attachments** (`GET …/files/…` gives
  them as they are). When some extension becomes a table in the future, it will be
  read and written through a dedicated interface like `.md`; so attachments are
  placed only under `assets/`, and it is discouraged to scatter data files in
  folders and rely on them being treated as attachments.
- **The layer of commits, log, undo and sync knows only paths, not content** (the
  `txn` of 1.3 is already like this, and the attachments of 1.4 also go through
  it). A new kind only needs to add its own read/write and index on top of it.
- **The parts of the save flow that depend on the body format must be kept to the
  side**: a draft stores "header + body", which is the shape of a page; the state
  machine itself (version number, `prev`, commit, conflict) does not depend on
  it, and should be reusable when content of another shape comes along.
- The name `tables/` under `.petalbook/` is reserved.
