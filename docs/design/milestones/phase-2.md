# Milestones: phase 2, structure and AI (2.1 to 2.6)

## Phase 2: structure and AI

Page types and the table view; `[[links]]` and backlinks; templates; the
validation page; API tokens; MCP; GitHub App and user authorization, automatic
repository creation; passkey; SAML.

### Phase 2 milestones (planned, 2026-11-22)

The ordering: first what the owner needs right away when writing every day
(types, views, home, links), then machine access (MCP), and last repository
creation and sign-in methods, which are only needed when "used a different way".
Each milestone is one PR, and each must first work in single-user mode, then
add permissions (see 1.7).

| Milestone | Content |
|---|---|
| 2.1 | Page types, the properties panel, the table view |
| 2.2 | Saved views, the board, reading in sequence |
| 2.3 | Space home, favourites, daily text |
| 2.4 | Page links and backlinks |
| 2.5 | Templates and the validation page |
| 2.6 | API tokens and MCP |
| 2.7 | GitHub App, GitHub user authorization, automatic repository creation |
| 2.7b | Generic connections (one provider interface, GitHub is the first implementation); merge the naming of "storage and sync" with the management page (added by the owner on 2026-11-23, done after the 2.7 fixes and before the 2.7 merge, see the 2.7b contract in [phase-2-connections.md](phase-2-connections.md)) |
| 2.8 | passkey, SAML, several sign-in providers configured at once, sign-in verification code |

**2.1 Page types, the properties panel, the table view**

- A type is `.petalbook/types/<name>.json`, standard JSON Schema, using only a
  subset: string, long text, single select (`enum`), multi select (`array` +
  `enum`), date, number, checkbox, URL. Keywords outside the subset are still
  kept in the file; the UI shows and edits that field as raw YAML, with nothing
  lost and no error.
- A folder is bound to a type in `space.yml`; subfolders inherit it and can be
  re-bound on their own; if the page header has a `type`, the header wins.
- The properties panel is generated from the schema and sits below the subtitle
  and the tags. rjsf is not introduced: it is designed for generic admin forms,
  is large, and here only the controls above are needed; we write a small
  renderer of our own, loaded on demand separately, and not loaded for pages
  without a type.
- Space settings have a "Types" section: create, change fields, delete fields.
  Renaming or deleting a field does not change the headers of existing pages
  (changing tens of thousands at once is both slow and one huge commit); the old
  field shows in the properties panel as "not in the type", and can be cleared
  from a single page with one click.
- **Index**: add a new table `props(page, key, value)`, with indexes on
  `(key, value)` and on `page`. Single select, date and number are stored in a
  sortable form, and a multi select has one row per value. The table view's
  filtering and sorting are both done on this table, without reading files. With
  tens of thousands of pages, first measure the time of filter plus sort once
  with a synthetic set of 50,000 pages before deciding on the approach; the goal
  is, as with the current list, within tens of milliseconds.
- **Table view**: available for folders bound to a type. The columns are the
  type's fields plus title, tags, time; sort by column, filter by field; single
  select, multi select, date and checkbox are edited directly in the cell (what
  changes is the page header, through the normal draft and commit, one page at a
  time). With many rows, load by page with virtual scrolling, and do not fetch
  everything at once.
- Single-user mode and read-only members (cells not editable) must both be
  tested.

The 2.1 implementation plan (2026-11-22):

- **Type file**: `.petalbook/types/<name>.json`; the file name (without `.json`)
  is the value of the page header's `type`, and is not renamed once created
  (renaming would touch every page); the display name is the schema's `title`,
  which can be changed. The top level is `{"title", "properties": {…}}`, and the
  field order is the key order of `properties` in the file (order is kept on
  read, not going through a Go map).
- The **field kinds** and their schema:
  | Kind | schema |
  |---|---|
  | Text | `{"type":"string"}` |
  | Long text | `{"type":"string","x-multiline":true}` |
  | Single select | `{"type":"string","enum":[…]}` |
  | Multi select | `{"type":"array","items":{"type":"string","enum":[…]},"uniqueItems":true}` |
  | Date | `{"type":"string","format":"date"}` |
  | Number | `{"type":"number"}` |
  | Checkbox | `{"type":"boolean"}` |
  | URL | `{"type":"string","format":"uri"}` |
  Each field can have a `title` (display name). A field kind that is not
  recognized is "other", and the properties panel shows and edits it as raw
  YAML. When the server changes a type it only changes the keys it knows; the
  other keys in the field object are left as they are.
- **Field keys**: cannot be a built-in field (`id`, `title`, `subtitle`,
  `source`, `tags`, `created`, `type`) or the reserved `publish` and `slug`;
  non-empty, at most 64 bytes, no control characters and no `:`. It can be
  Chinese.
- The **binding** is written in `space.yml`:
  ```yaml
  folders:
    Tang:
      type: 文案
  ```
  A subfolder inherits the nearest binding; if the page header has a `type`, the
  header wins. When a page is created, `type` is **not** written into the
  header, and inheritance is used; the page response and the summary get one
  more field, `effectiveType`. Bindings are recorded by path, so when a folder
  is renamed or moved, `space.yml` is changed in the same commit.
- **Index**: the `pages` table gets one more column `props` (the keys in the
  header other than the built-in fields, JSON, with overly long strings cut to
  300 characters); a new table `props(page, key, pos, val, skey)`: one row for a
  scalar, one row per element for an array (`pos` is the position), objects are
  not included. `skey` is the key used for sorting: a number is encoded as text
  that sorts by its literal form, a date is kept as is, text follows the same
  rule as `title_key`, a checkbox is 0/1. Indexes on `(key, skey, page)`,
  `(key, val, page)`, `(page)`. All non-built-in keys are indexed, regardless of
  which type they belong to, so changing a type or a binding never needs an
  index rebuild. The index version is bumped by one, and the first start does one
  full rebuild (about 30 seconds for 50,000 pages).
- **List endpoint**: `GET …/pages` gets:
  - `props=1`: the summary carries `props` (the column above) and `rev`, for the
    table view; without it the summary is unchanged, and the traffic for the
    list and the cards does not grow.
  - `sort=prop:<key>` / `-prop:<key>`: sort by field; pages without this field
    go last, then by `rowid`; a multi select uses the first value. The cursor is
    keyset pagination as before.
  - `filter`: a JSON array, each item `{"key","op","values"}`, where `op` is `in`
    (equal to one of them; for a multi select, contains one of them), `range`
    (`values` is `[from, to]`, either end may be empty, for numbers and dates),
    `empty`, `notEmpty`, `contains` (text contains, a substring match on `val`,
    looking only in the rows of this key). Several items are combined with
    "and".
- **Properties-only endpoint**: `PATCH …/pages/{id}/props`, `{"baseRev", "set":
  {…}, "unset": [...]}`, which only changes the header; the body does not go over
  the network; a wrong `baseRev` answers 409, same as PUT. Needs `write`. Edits
  in the table cells go through it. Drafts are handled the same as for PUT (no
  draft is lost).
- **Type endpoints** (reading needs `read`, writing needs `manage`):
  - `GET …/types`: for each type, the name, the display name, the fields in
    order (key, display name, kind, options, raw schema), plus an error message
    when the file cannot be read; plus all bindings.
  - `PUT …/types/{name}`: create or change, with a field list; the server merges
    it into the original file, keeping the keys it does not recognize.
  - `DELETE …/types/{name}`: delete the file, and remove the bindings pointing to
    it in the same commit. Page headers are not touched.
  - `PUT …/folders/type`: `{"folder", "type"}`; an empty `type` removes the
    binding.
  If a type file is broken (edited by hand), do what is done for `tags.yml`: do
  not overwrite, answer 400 saying it must be fixed by hand. A space has at most
  100 types, and each file at most 256 KiB and 200 fields.
- **Value validation**: does not block saving. A value that does not match the
  schema is saved anyway, and the properties panel marks it red; the 2.5
  validation page lists them.
- **Frontend**:
  - The properties panel is below the subtitle and the tags, with a control per
    field kind; value changes go into the draft, like the title. The panel is a
    separate load-on-demand chunk, not loaded when the page has no type. Fields
    not in the type are listed in a separate group, and each can be cleared.
  - Space settings get one more "Types" section (changing needs `manage`):
    create a type, add a field, change a display name, change options, reorder,
    delete a field, delete a type; the folder's context menu has "Set type".
  - The folder view gets one more "Table", which only appears for folders with a
    type. The columns are the title plus the type's fields, then tags and
    update time; click a column header to sort; filter by field; single select,
    multi select, date, checkbox and number are edited in the cell (read-only
    members cannot edit). Load in batches by cursor, fetch the next batch when
    scrolled to the bottom, with virtual scrolling for rows, so thousands of rows
    do not lag.
  - The chosen view (list, cards, table) is remembered per folder in the
    browser; 2.2 changes it to be stored in `space.yml`.

**2.1 as built (2026-11-22)**: everything in the plan above was built; see
`docs/api.md` for the endpoints. Decided during implementation and not written in
the plan:

- Measured (50,000 pages in one folder): sorting by a field and taking one page
  takes about 0.3 ms, the same when paging to page 400; filter plus sort takes 1
  to 2 ms; the slowest is a range filter that hits only a few pages, about 40 to
  60 ms.
- At most 200 fields are indexed per page, and at most 100 items in a list; empty
  text and an empty list count as not having the field, and go last when sorting
  by the field. When a value stored in the index is incomplete (truncated, an
  object, over the limit), the summary's `partialProps` lists those keys, and
  those cells in the table are read-only; open the page to edit.
- A type carries a `rev`; changing and deleting both need the `rev` that was read,
  and if someone else changed it first the answer is 409, so changes do not
  overwrite each other. An existing field is kept even if it does not match the
  new field naming rule.
- When a folder is moved or deleted, changing the directory and changing the
  binding in `space.yml` are recorded as one journal entry, and finished together
  on crash recovery; in local mode, if the binding cannot be written, the move is
  undone. When `space.yml` has only comments, writing a binding does not lose the
  comments; when two bindings are written for the same folder, the last one wins.
- Text in the header that really is a date (`YYYY-MM-DD`) is written without
  quotes, other text that looks like a date is quoted, and a `!!` tag is never
  written, so other YAML tools can read it.
- Number and text cells in the table are saved only on blur or on Enter, and Esc
  cancels; select-type cells are saved as soon as they are clicked.
- Verified in the browser: sorting in the table, and that the file and the commit
  are right after editing a value in a cell; the various controls of the
  properties panel, a value not in the options being marked red and kept, the
  "not in this type" group; type editing in settings and folder binding.

Known issues, carried forward:

- Sorting by a single select field sorts by text, not by option order ("In
  revision, Final, Draft" rather than "Draft, In revision, Final"). Sorting by option order would either
  sort fresh on every query (slow with tens of thousands of pages) or recompute
  the sort keys of all related pages when the option order changes; to be decided
  in 2.2 together with saved views.
- Changing a field's options (renaming, deleting an option) does not change the
  values in existing pages; the old values are marked red in the properties panel
  and the table; bulk value changes come later.
- The text "contains" filter is case-insensitive only for ASCII letters.

**2.2 Saved views, the board, reading in sequence**

- A view = a combination of filter, sort, grouping and display mode, stored in the
  `views` of `space.yml`, recorded per folder (or tag); there can be several, with
  one default.
- **Board**: columns by a single select field, and dragging a card changes the
  value; each column is loaded by page, and not all cards are pulled at once.
- **Reading in sequence**: a folder or a set of filter results is laid out in order
  as one long text to read, with the body fetched in batches as you scroll down;
  read-only, and clicking a page enters editing.
- List, cards, table, board and reading in sequence are also usable on tag pages
  and on search results.

2.2 implementation plan (2026-11-22):

- **Where views are stored**: a folder's views are written in `space.yml` under that
  folder's entry, together with the binding, and go with the entry when the folder
  is renamed, moved or deleted (reusing the 2.1 journal and recovery):
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
  A tag's views are written in `tags.yml` under that tag's entry (together with the
  color). The key for the root directory is `""`.
- **A view**: `id` (issued by the server, a short random string, unique within the
  space), `name`, `layout` (`list`, `cards`, `table`, `board`, `read`), `sort`,
  `filter` (written as in the list endpoint), `group` (the board's column field),
  `hidden` (columns hidden in the table). The first view is the default view. Keys
  that are not recognized are kept as they are. At most 20 views per folder or tag.
- **Endpoints**: `GET …/views?folder=` or `?tag=`, returning the view list and
  `rev`; `PUT …/views` with `{folder or tag, baseRev, views}`, replacing the whole
  list, and a wrong `rev` answers 409 (with `current`). Changing views needs
  `write`: views are an arrangement shared by everyone, and whoever can write pages
  can change them. People without write permission can switch views, and can also
  change the sort and filter temporarily, but cannot save them back.
- **Frontend**: a row of view tabs at the top of the folder page and the tag page;
  create, rename, duplicate, delete, drag to reorder. When the sort, filter, column
  grouping or hidden columns are changed in a view, "Modified" appears on the tab,
  and only clicking "Save" writes it back (not a commit for every click); "Revert"
  drops the changes. The last view used in each folder is remembered in the
  browser. A folder with no saved view is the same as now (one implicit default
  view). The choice of list, cards or table that 2.1 stored in the browser is the
  starting point of the implicit view.
- **Single select sorted by option order** (left over from 2.1): when sorting by a
  field and the list carries `folder`, if this field is a single select in the
  folder's type (or a multi select, by the first value), the sort order is: the
  options in turn, then values not in the options (by text), and last those without
  this field. In the implementation it is an index scan by group (one group per
  option, using the `(key, val, page)` index), the cursor remembers which group it
  is in, and there is no full-table sort. As in 2.1, test with a synthetic set of
  50,000 pages, with the goal within tens of milliseconds.
- **Board**: `group` is a single select field. The columns are, in turn, the
  options, "Other values" (values not in the options, shown only if present), and
  "Empty".
  - Each column is paged separately: the list endpoint gets the filter `notIn` (has
    a value and is not in the given options), and the columns are the three filters
    `in [option]`, `notIn [all options]` and `empty`, 20 cards each time, fetching
    more when scrolled to the bottom.
  - The count in a column header: a new endpoint
    `GET …/pages/groups?folder=&key=&filter=`, returning `[{value, count}]` plus the
    number of empty values; grouped and counted by value on the `props` table,
    without reading files.
  - A card shows the title (the beginning of the body when there is no title), a
    two-line excerpt, tags, and up to 3 fields chosen in the view. Dragging to
    another column changes this field (dragging to "Empty" removes it), through the
    2.1 `PATCH props`, with `rev`, and on conflict the card is refreshed with a
    prompt. The order within a column follows the view's sort and cannot be set by
    hand (the first version has no manual ordering). Read-only members cannot drag.
- **Reading in sequence**: following the view's sort and filter, pages are laid out
  one after another as a long text.
  - The list endpoint gets `body=1`: the summary carries the body (Markdown), and
    then `limit` is at most 20. Since that many files must be read, it is used only
    for reading in sequence.
  - The frontend renders the body as a read-only layout, and does **not** mount an
    editor for each page; it uses the existing Markdown parser to get the document
    and then draws it as static HTML (images, tables, code and mermaid look the same
    as in the editor). It is a separate load-on-demand chunk.
  - Each page shows the title and the subtitle (a text of the "text" type looks like
    `《标题》｜副标题`), and a page without a title is just the body; clicking the title
    enters editing. As you scroll down, pages are fetched in batches, and pages far
    from the viewport are unmounted from the page, so even hundreds of pages do not
    lag.
- **Tag pages** can use all display modes except the table (the pages under a tag
  are not necessarily of the same type); the board on a tag page is available only
  when a column field has been chosen in the view. Search is now a dialog, and has
  no views.
- Fixed along the way, found while working on 2.1: when a table's menu is open the
  whole table is replaced, and changing the alignment reports an error
  (`columnAlign` in `tableOps.ts`).

**2.2 as built (2026-11-23)**: everything in the plan above was built; see
`docs/api.md` for the endpoints. After eleven rounds of adversarial review,
decided during implementation and not written in the plan:

- **Principle (set by the owner)**: problems at the system level are not handed to
  the user to fix. When a settings file (`space.yml`, `tags.yml`, a type file)
  cannot be read, recover from the most recent readable version in the Git
  history, rescue entries one by one where possible, fix it on the next write and
  keep the original as `<file>.unreadable-<time>`, and give only the managers one
  notification; when `.petalbook` or `.petalbook/types` has been replaced by a
  link, move it aside and rebuild, and never follow it.
- **YAML anchors and aliases**: parsed as usual on read (each anchor is parsed
  only once, so the cost depends only on how much the file writes), and on write
  only the part to be changed is copied, and after the change the meaning of the
  other aliases is checked to be unchanged; the server itself writes an alias in
  only one case (when removing the type from an entry shared by many folders).
- **The server's own state** (index, journal, notifications, the single-user
  mode draft store) lives in the Git directory (`.git/petalbook-state`; a
  worktree uses its own Git directory); `.petalbook/` holds only repository
  content. The migration code from when it lived in `.petalbook/cache` was
  deleted under the §5.4 rule. When writing a file, the temporary file is placed
  next to the target (`.petalbook-w-*`).
- **Index queries**: every condition (range, tag, filter) can be looked up by
  page or taken by index range, and each time the cheapest of the three methods
  is picked; one request reads only one snapshot (a read transaction, plus a
  version number to tell whether parallel queries are at the same moment); writes
  have their own connection; when the WAL exceeds 16 MB it is merged between
  reads, so it does not grow without bound. The lock order is written at the top
  of `internal/index/read.go`.
- **Measured** (50,000 pages): the combinations the API allows are mostly within
  100 ms; sorting by option order, each board column, and the board counts are
  mostly a few ms to tens of ms.
- **A view's "unsaved changes"**: one working copy, with the sort inside it too;
  changes land only on the view they belong to, and when the change is not the
  user's own action it is not silently dropped (a prompt is shown); during the
  moment the user saves the view the view controls are locked, and after saving
  the list is read again, and if that read fails the saved reply is used first.
- Board cards can use the keyboard "Move to…"; managers see the settings-file
  recovery notification in space settings and in the top banner.
- The contract tests are also run on PostgreSQL (`make contract-postgres`, in the
  CI of `check-full` and `main`).

Known issues, carried forward:

- With 10 tags and 10 filters that hardly drop any pages in use at the same time,
  queries and counts take about 95-150 ms; when writes keep going without a break
  it falls back to reading in order, and may come close to 200 ms.
- With many reads (20 or more, non-stop), the slowest read takes about 250-330
  ms, mainly queuing for the 8 read connections.
- When 10,000 folders share one template through a merge key (`<<: *template`),
  removing their type is rejected if what would be written exceeds 1 MB.
- When a file has a syntax error, an entry that refers to an anchor elsewhere
  counts as lost (listed in the notification) and is not rescued.
- One full set of checks (`make check-full`) takes about 7-8 minutes, which is too
  slow; to be sped up separately after 2.2 is merged.

**2.3 Space home, favourites, daily text** (proposed by the owner on 2026-11-22)

- **One home per space** (decided by the owner; no cross-space master home), the
  page opened by default on entering a space, at the address `/s/<space>/`. From
  top to bottom:
  1. **Greeting and daily text** (the owner, 2026-11-23: the daily text goes at
     the very top, together with the greeting): by browser time it says "Good
     morning / Good afternoon / Good evening, <name>", and right below is
     today's text (a text of the "text" type looks like `《标题》｜副标题`), with "Another", and clicking it opens it
     for editing; when no source is set there is only the greeting.
  2. **Quick note**: write a note right on the home page, and when done it goes
     into this space's inbox.
  3. **Continue writing**: the 5 to 8 most recently updated pages.
  4. **Inbox**: the number of items not yet sorted and the first few, with "go
     and sort".
  5. **Favourites**: the pages, folders and views you have favourited yourself.
- **Daily text**:
  - Where to draw from is chosen in space settings: a folder, a tag or a type
    (a filter may be added, for example drawing only the "Final" ones). When none
    is chosen this block does not appear. The setting is stored in `space.yml`,
    and everyone sees the same one.
  - On the same day, in the same space, the same page is drawn: the date (in the
    browser's time zone, passed to the server as a parameter) and the space ID
    give a sequence number, and the page at that number is taken among the pages
    that qualify. Both the count and the fetch go through the index (one count
    plus one fetch by offset), without reading other files.
  - "Another" draws another page at random, only in your own browser, and does
    not change what others see.
- **Favourites** are per person, stored in the state database (not in the
  repository, not committed), split by space; pages, folders and views can be
  favourited, and they can be reordered by dragging. After a page is deleted or
  moved out of the space, it shows in the favourites as stale, and can be
  cleared. Single-user mode has them too (the state database is there anyway).
  The sidebar also has a "Favourites" group.
- Each block on the home page takes only the first few items, by querying the
  index; the number of requests the home page makes does not grow with the number
  of pages.
- **The greeting should have some charm** (the owner, 2026-11-23): not just good
  morning / good afternoon / good evening. It is computed entirely in the
  browser, with no request, and picked from a set of sentences according to the
  UI language (Chinese and English, and each language added later has its own
  set):
  - Finer by time of day: early morning, morning, early afternoon, evening, late
    night (such as "Night owl, still writing?"), before dawn ("Dawn is near,
    remember to get some sleep").
  - By day: Monday, Friday, the weekend; holidays and solar terms (the lunar
    calendar and solar terms for New Year's Day, Spring Festival, Qingming,
    Dragon Boat, Mid-Autumn, Winter Solstice and so on use a small table
    precomputed by year, covering the next ten years); the user's sign-up
    anniversary.
  - By situation: first open today, away for a long time ("Long time no see"),
    back on the home page right after finishing a page, already written several
    pages today.
  - One situation has several sentences, rotated by date, so they do not jump
    around within the same day; the flourishes can also be turned off in
    preferences, using only plain greetings.
  - The sentences are written in the i18n files, easy to change and add, and not
    hard-coded in components.
- **The daily text source, three layers** (the owner, 2026-11-23: 「都要」 [All
  of them]):
  1. The space's own (the `daily` above).
  2. **A site-wide default configured by the administrator**: the admin console
     "Daily text" page, where one can pick a folder, tag or type in a space, or
     paste/upload a batch directly (one per line, or a Markdown file, stored in
     the state database). It is used when a space has no setting; the owner can
     turn it off in the space. When a space is chosen as the site-wide source, a
     clear warning says "this content will be shown to everyone".
  3. **A custom external interface**: the administrator fills in a URL and a
     field mapping (the JSON paths for text, source and author), and can fill in
     different interfaces by language (`{lang}` may be used in the URL), for
     example Hitokoto for Chinese and FavQs for English. The server
     fetches it, each interface once a day, and caches it, and everyone sees the
     same one; "Another" fetches again, with a frequency limit. To keep the
     server from being used to reach the internal network: only hosts the
     administrator lists are allowed, internal and local addresses are not
     connected to (checked after resolving, to stop DNS rebinding), timeout 5
     seconds, response at most 64 KB, only JSON accepted; when nothing can be
     fetched this block does not appear and the home page is not affected. A
     space owner can choose an interface the administrator has configured, but
     cannot fill in a URL themselves.
  - Priority: the space's own > the external interface the space chose >
    the site-wide default. When there is none of them only the greeting is shown.
  - **Scope** (confirmed by the owner, 2026-11-23): the site-wide default the
    administrator configures (pasted texts, a space chosen as the source) and the
    external interfaces are shared by the whole server, the same for all users
    and all spaces; external interfaces are cached by (interface, language,
    refresh period), so people using the same interface and the same language
    within the same period see the same one. A space's own source is valid only
    in that space, and its content is shown only to that space's members. When
    multi-tenant and open registration come later, a layer "each tenant's
    default" is added under the site-wide default, with a priority between the
    space and the site.
  - **Multiple languages**: the text follows the UI language. A space source can
    be split by language (for example `daily: {zh: {folder: 文案}, en: {tag:
    quotes}}`; if only one is written, all languages use it); a batch of
    site-wide texts can be tagged with a language; external interfaces are
    configured one per language. If a language has none, nothing is shown
    (matching follows BCP 47, Simplified and Traditional Chinese match each
    other, plus the "all other languages" entry; text in another language is not
    substituted, and the greeting is still shown).
  - **Configured by language, supported in both code and UI**: the languages are
    a table, and Chinese and English are not hard-coded; in the UI the "Home"
    section of space settings and the admin console "Daily text" page are both
    split into tabs by language (one "All languages" page plus one page per
    language), and each page can pick a source, paste texts, and pick external
    interfaces; when the UI gains one more language later, one more page appears
    here automatically.
  - **Built-in external interface presets** (the documentation of each was
    checked on 2026-11-23): shipped with the program, **not enabled** by default
    (a self-hosted service should not connect to the outside network by itself
    without the administrator's consent); the administrator enables one with a
    click, and the host is added to the allow list automatically:
    - Chinese: Hitokoto international line `https://international.v1.hitokoto.cn/`
      (the docs say QPS 20, including a 2-second cache; the ordinary line
      `v1.hitokoto.cn` only has 2). Free, no key needed, a public-interest
      service; a backlink `https://hitokoto.cn?uuid=<uuid>` is earnestly
      requested, so a link "from Hitokoto" is shown on display. The default
      `max_length` is 30, raised to 80 in the preset. Text `hitokoto`, source
      `from`, author `from_who`. The preset's rate limit is set to 20.
    - English: ZenQuotes daily quote `https://zenquotes.io/api/today` (free, no
      key needed; **its terms require a backlink, and the home page does not
      show external links, so it is in no default**, and the administrator
      decides whether to turn it on; 5 times per 30 seconds per IP, so the rate
      limit is set to 0.1). Text `[0].q`, author `[0].a`. It gives only one a
      day, so new ones are not skipped (`skipNew` off).
    - English: FavQs daily quote `https://favqs.com/api/qotd` (free, no key
      needed). Text `quote.body`, author `quote.author`, backlink
      `https://favqs.com/quotes/{quote.author_permalink}/{quote.id}`. The rate
      limit is set to 1, refreshed daily, new ones not skipped.
    - Quotable (`api.quotable.io`) can no longer be used: the domain's DNSSEC
      validation fails and no address can be resolved, so it is no longer built
      in.
    - Hitokoto can choose a category (all, single select, multi select): a
      built-in interface can declare "options", and the chosen values are stored
      together with the choice (stored separately for space, tenant and site-wide
      default), and empty means all; the values are added after the URL
      (`&c=d&c=i`), different categories are different cache entries, and the rate
      limit is still counted per interface.
    - One language can pick several sources (external interfaces, our texts, the
      pages of a space), and mixing them is fine: they rotate by day, and when
      one errors or has no content the next one is used automatically, with only
      one shown at a time: the starting point of the rotation is decided by the
      day and the scope (space, tenant, site-wide), so people in the same scope
      see the same one; "Another" moves to the next.
    - Other built-in interfaces: Animechan (anime quotes, 100 a day, so at most 1
      per minute), DummyJSON (about 1,400 entries, for developer testing, uneven
      quality), Jinri Shici (classical Chinese poetry, an old interface, a
      category can be chosen, using a path option), and Shanbay daily sentence and
      Kingsoft iCiba daily sentence (English plus Chinese; one side is taken
      according to the reader's language, only one shown at a time, never both
      together). **Shanbay uses its mini-program's own interface, not a public
      API, and it may change at any time**; iCiba labels its JSON as text/html, so
      the Content-Type check is relaxed only for this built-in interface (the
      content must still be strictly JSON). None of them is in any default.
    - **Author and source**: every text, wherever it comes from, has the same
      fields (text, author, source, link), shown as a smaller grey line below the
      text: in Chinese `—— 作者《出处》`, in
      English `— Author, Source`, showing only the parts that exist, with no link
      drawn. Our own texts: when pasting, a line-ending ` —— 作者《出处》` (` — `
      and ` -- ` are also recognized; only the last one is split off, what follows
      must look like a name, and what is before the dash must be the end of a
      sentence (。！？…」』” . ! ? " ') or what follows has a 《出处》, so a dash
      in the middle of a sentence is not split) is split off before saving, and the
      administrator can see and change it in the preview; CSV with three columns
      (text, author, source; the header row optional). Pages of a space: the author
      is taken from the page's `author`/`作者` property, and the source from the page's `source` (the
      built-in "Source" field) or `出处` property, and when there is none it is not shown; when there is no source
      the title is used.
    - A space can also list this itself: one ordered list of sources per language
      (external interfaces, the administrator's texts, this space's own pages),
      with the same rules as the administrator's; when the administrator locks
      "use external interfaces", the interfaces in the list are skipped, and when
      "use its own sources" is locked, the space's own pages are skipped, and the
      old list of interface ids is still read as before. A tenant's default can
      use the same list (interfaces, the tenant's own spaces); for now it can be
      changed only through the settings endpoint, and the editing UI is left to
      3.6.
    - An interface a space chose that the administrator later disables is not
      dropped on save: the settings page shows it greyed out with the reason, and
      it is skipped on read; when the interface no longer exists (such as the old
      Quotable), the space shows no text (reason `unavailable`), does not switch to
      the tenant or site-wide default, and the settings page shows a prompt.
      Shanbay and iCiba serve only `zh-Hans` and `en` (`zh-Hant` falls under
      `zh-Hans` by the existing matching rules), and readers of other languages
      skip them and use the next source in the list. "Another" continues from the
      source that actually gave today's text.
    - **The home page shows no external links**: links the interface gives
      (Hitokoto's uuid link, ZenQuotes' backlink) may be stored, but are never
      drawn as links, and the home page does not send people off the site; author
      and source are plain text `—— 作者《出处》`. English defaults to FavQs (which
      does not require a backlink). Hitokoto is Chinese only, is not used for
      English readers, and cannot be put under the English entry; when put under
      "all other languages" it takes effect only for Chinese readers.
    - To be completely unrestricted: QuoteSlate is MIT open source, and you can
      deploy your own copy and fill in the address yourself in the console.
    - When changing a built-in interface, run `make test-live-daily`: it uses
      `Probe` to really request every built-in interface, and fails if one is
      unreachable or a path gets no value. It needs the network, and is not in CI
      or in `make check`.
    - The server fetches and caches in one place, with one request per period, far
      below these limits in normal use.
    - **Backlink**: an interface can be configured with a "source link" template
      (such as Hitokoto's `uuid`), shown below the text; filled in in the preset.
  - **The administrator configures interfaces completely themselves** (stressed by
    the owner, 2026-11-23): the presets are only a starting point, and the
    administrator can create, change and delete any number of interfaces, each
    with:
    - name, language (or "all languages"), URL (`GET`, `{lang}` and `{date}` can be
      used);
    - request headers (such as `X-Api-Key: …`), values stored encrypted in the
      database, and the UI shows only "saved · replace";
    - the JSON paths for text, source and author (such as `hitokoto`, `[0].q`,
      `data.content`), text required;
    - **refresh frequency** (the owner, 2026-11-23): how often the one everyone
      sees changes: every day (default), every hour, every N minutes; whether
      "Another" asks the other side for a new one each time;
    - **rate limit on outgoing requests**: at most how many per second (a token
      bucket, for example the Hitokoto international interface can be set to 20),
      with all users' requests together not exceeding it; when exceeded the one
      in the cache is used, with no queuing;
    - a "Try it" button: request once immediately, and show the raw response
      (truncated) and the result taken out by the paths, so a wrong path is seen
      on the spot;
    - enable / disable.
    On save, the URL's host is added to the allow list; internal and local
    addresses are never allowed.
  - **The server fetches in one place, redistributes, and caches** (the owner,
    2026-11-23): browsers never request external interfaces directly. Reasons:
    keys do not leave the server; each interface is requested only once a day, so
    it is not rate-limited by the other side because there are many people;
    everyone sees the same one; cross-origin settings of the other side do not
    matter; and the internal network protection only has to be done in one place.
    The cache has three layers:
    1. **Server memory**: results are remembered by (interface, language, refresh
       period, skip), and used directly within the period.
    2. **State database**: the same results are written to the state database
       (only the most recent 7 days are kept), so after a service restart there
       is no need to request again; also shared by several processes or, later,
       several instances.
    3. **Browser**: `GET …/daily` carries `Cache-Control` (until the end of the
       day, computed from the date in the request), so opening the home page
       again and again on the same day does not request the server again; the
       result of "Another" is cached only in the page.
    When an external interface fails, the most recent successful result is used
    (marked with which day it is from), and only if it fails again is nothing
    shown; after a failure it retries with exponential backoff, and does not knock
    on the other side every time the home page is opened. The results of space
    sources and site-wide texts also use caches 1 and 3 (they only query the index
    anyway, which is cheap).

2.3 implementation plan (2026-11-23):

- **Home page**: the address `/s/<space>/`; entering a space (including after
  sign-in and after switching spaces) opens it by default, and the old "Recent"
  stays in the sidebar. The home page only assembles a few existing and new small
  endpoints, no more than 5 requests in all:
  - Quick note: reuse the existing quick-note endpoint; on the home page it is
    directly an input box, which is cleared after writing, with the prompt "Put
    in the inbox", and the inbox block refreshes right away.
  - Continue writing: `GET …/pages?sort=-updated&limit=8` (existing).
  - Inbox: the count comes from the count on the inbox row in the folder tree
    (existing, O(number of folders)), and the first 5 use
    `GET …/pages?folder=<inbox>&limit=5`.
  - Daily text and favourites: see below.
  - The home page a read-only member sees has no quick note. Single-user mode has
    a home page too.
- **The daily text source** is written at the top level of `space.yml`:
  ```yaml
  daily:
    folder: 文案        # or tag: … or type: …, pick one of the three
    filter: [{key: status, op: in, values: [定稿]}]   # optional, same as the list endpoint
  ```
  It follows the 2.2 settings-file rules (self-healing, anchors, link guards are
  all the same). Changing it needs `manage`, and it is chosen in the newly added
  "Home" section of space settings.
- **Daily text endpoint**: `GET …/daily?date=2026-11-23&skip=0` (needs `read`).
  - The server computes a sequence number n from `hash(space ID, date)` plus
    `skip`, and takes the n-th page among the pages that qualify (`n mod total`):
    one count with the condition plus one read ordered by `rowid` with offset n,
    both through the index; measured with a synthetic set of 50,000 pages, the
    goal is within tens of milliseconds.
  - The result of the same day is remembered in memory by `(space, date, skip)`,
    so pages added or deleted during the day do not make the one everyone sees
    change back and forth; it is fetched again only when that page has been
    deleted.
  - It returns the page summary plus the body (reading only this one file), and
    when the body is over 2000 characters it is truncated and marked as such.
  - When no source is set, or no page qualifies, it returns `{"page": null}` and
    that block of the home page does not appear (when it is set but nothing was
    drawn, the managers get a one-line hint).
  - "Another" is the browser adding one to `skip`, and affects only the user.
- **Favourites**: a new state database table `favorites(user_id, space_id, kind,
  target, position, created)`, where `kind` is `page` (target is the page id),
  `folder` (the path), `view` (`folder:<path>#<view id>` or
  `tag:<tag>#<view id>`); primary key `(user_id, space_id, kind, target)`, and
  deleted together when a space or a user is deleted. One migration each for
  SQLite and PostgreSQL, and both must be tested.
  - Endpoints (all need `read`, and only touch your own): `GET …/favorites`
    returns the ordered list, each item with a display name and `missing` (the
    page is gone, the folder is gone, the view is gone); `POST …/favorites` adds
    one (not duplicated if already there); `DELETE …/favorites` removes one;
    `PUT …/favorites/order` gives the new order. At most 200 items per person per
    space.
  - The display name and `missing` are decided from the index and the settings
    files, looked up in a batch by id within one request, without reading files
    one by one.
  - When a folder is renamed or moved, the paths in the favourites change with it
    (they go with the folder like bindings, but the favourites are in the state
    database, so after a successful move the server updates this item for
    everyone in the space).
  - For a person who has been removed from the space, the favourites stay in the
    database but are not returned; they are still there after rejoining.
  - Entry points: a star in the page editor's top bar, "Favourite" in the folder
    context menu, "Favourite this view" in the view tab menu; a "Favourites" group
    in the sidebar (collapsible), the same data as the home page.
- **Also fix the PostgreSQL space-name search** (a known issue from 1.7): the
  `spaces` table gets a `name_key` column, storing the result of the Unicode case
  folding done in Go (`cases.Fold`), and search compares only this column;
  backfilled at migration. The two databases behave the same, and tests cover
  Chinese, German ß, Turkish İ and so on.
- **Frontend**: the home page is a load-on-demand chunk; the daily text block
  makes no request when there is no source; drag-and-drop ordering of favourites
  follows what the view tabs do, and also has keyboard "Move up / Move down".
  Text is complete in Chinese and English.
- **Tests**: backend unit tests (determinism of the daily text, across days,
  `skip`, refetch after a page is deleted, filters; favourites add, delete,
  ordering, `missing`, following a folder move, permissions, the limit; both
  databases), endpoint contract tests, frontend component tests, and the timing
  of the daily text at 50,000 pages.

**2.3 as built (2026-11-23)**: everything in the plan above was built; see
`docs/api.md` for the endpoints. After three rounds of adversarial review,
decided during implementation and not written in the plan:

- The **key of an external interface** is bound only to the request in which it
  was entered (the interface, the header name, and a hash of the full URL
  template); if any part of the URL is changed it must be entered again, so an
  administrator who does not have the key cannot lead it elsewhere. A link
  template cannot refer to a request header; every value shown goes through
  "take the value → decode → check (case-insensitive, any 8 consecutive
  characters of the key) → truncate → escape", and the raw response of "Try it" is
  checked value by value, and if one place looks like the key the whole section is
  not shown. What remains is only a length side channel (against a service that
  echoes, the key length can be inferred).
- **Outgoing requests**: the address is checked only at connect time (against a
  domain being repointed), and internal, local, CGNAT (including tailscale's
  100.x) and metadata addresses are all refused; redirects are not followed, 5
  seconds, 64 KB, JSON only; a reader disconnecting does not count as a failure;
  the language is normalized per interface and the date accepts only the day
  before and after, so a reader cannot make the server request over and over;
  memory and cache rows both have limits.
- **Error codes** are in the `error` field of the error body (`host_not_allowed`,
  `header_reenter`, `path_reveals_secret` and so on), and the frontend shows them
  in Chinese and English by code.
- **Favourites follow the folder**: the move is recorded before it happens
  (migration 0010), and then, regardless of success or order, settled together the
  next time favourites are read; a same-named folder newly created at the old
  location does not take over the old favourites. A known gap: if a crash happens
  exactly between recording the move and actually moving, and the target folder
  already exists, it is treated as moved.
- **Daily text**: when a space source matches nothing it does not fall back to the
  site-wide default (the owner agreed); when the folder used as the source is
  deleted the setting stays and this block is not shown; the one everyone sees that
  day and the "Another" ones are cached separately, with "Another" at most 1000
  times.
- **The greeting** is computed in the browser, and that day's sentence is
  remembered together with the conditions it was based on, and recomputed only
  when the conditions change; "just finished writing" and "wrote several pages
  today" count only pages this browser itself saved (deduplicated by page), and
  imported ones do not count.
- **Administrator locks**: two items, "whether spaces may use their own sources"
  and "whether they may choose external interfaces"; when locked, the space's
  value stays where it is and does not take effect.
- Drawing the daily text at 50,000 pages: about 8 ms for the whole space, about
  14 ms for a folder, about 2 ms for a tag.

**Daily text additions after 2.3 (2026-11-23, proposed by the owner)**:

- The site-wide default can also be an external interface, and one can be chosen
  per language (`""` is the fallback for other languages).
- Language codes always use the canonical BCP 47 form (`zh-Hans`, `pt-BR`),
  checked against the IANA registry and matched by the BCP 47 rules; the old
  values `zh` and `pt-br` are converted to the canonical form on read.
- Built in are Hitokoto (the international interface, Simplified and Traditional
  Chinese, at most 20 per second), ZenQuotes (English, daily quote, at most 5 per
  30 seconds) and FavQs (English, daily quote, 1 per second), off by default and
  usable once the administrator turns them on, and their URLs cannot be changed.
- There is no longer a "trusted URLs" list: each interface can connect only to
  the host and port in its own URL, and `host_not_allowed` is removed.
- Required fields in the form are marked `*`, and those that may be absent, such
  as author, source and link, are marked "optional", and an interface not
  returning them is not an error. The admin console is no longer squeezed at phone
  width.
- The upgrade of settings stored by 2.3 (presets replaced by built-in interfaces,
  renaming `<id>-custom`, dropping non-conforming language codes, the `notices`
  hints, rows stored by the old code) was deleted on 2026-11-24 under the rule "no
  upgrade code before release" (see [database.md §5.4](../database.md#54-rule-for-schema-changes-no-migrations-before-the-official-release)).

**Layered settings and locking** (a general rule set by the owner on 2026-11-23,
used step by step from 2.3):

- Preference and behavior settings have four layers: **site-wide** (administrator)
  → **tenant** (added when multi-tenant is built later) → **space** (owner) →
  **personal** (each person's own preferences). When the next layer has no setting
  it follows the previous layer, and the default is to follow site-wide.
- For each setting the administrator decides two things: the default value, and
  **whether the layers below can change it**, in three modes: "can change", "can
  only choose among the given options", "locked, cannot change". Later, when there
  are tenants, a tenant administrator applies the same restrictions to spaces and
  individuals within what the site allows.
- When locked or restricted, the value the lower layer already has stays where it
  is (a space's is written in `space.yml`, not deleted and not changed), and just
  does not take effect; in the UI this item shows grey with "set centrally by the
  administrator", and after unlocking the original value takes effect again. So
  locking never loses anyone's settings.
- The storage places do not change: site-wide and tenant are stored in the
  database; a space's is still in repository files (it goes with Git, see 2.2);
  personal is stored in the database. When the server reads settings it merges by
  layer, and the result is cached by the version of the settings files and the
  database, not recomputed each time.
- Settings it applies to (connected step by step): the daily text source and
  external interfaces (2.3), the greeting flourishes, default language and color
  scheme, upload size, whether a space may open registration to join, whether
  boards and views may be saved by editing members, later publishing (see
  [reserved.md §14](../reserved.md#14-reserved-public-publishing)) and so on. Security ones (sign-in methods,
  verification code, lockout, see 2.8) are at the site-wide or tenant layer only,
  and are not delegated to spaces and individuals.
- In 2.3 the daily text is done by this rule first: the administrator can set
  "whether spaces may use their own sources" and "whether spaces may choose
  external interfaces".

**2.4 Page links and backlinks**

- Typing `[[` in the editor pops up a page picker (using the existing search
  endpoint), and after one is chosen a link is inserted; the link is shown as the
  target page's current title, and clicking it opens it.
- **How a link is written in the file** (decided by the owner, 2026-11-22): a
  standard relative-path link, with the target's `id` placed in the link title
  position: `[暮雪](../Tang/暮雪.md "01JD3K8Q7Z")`.
  - It is clickable as usual on GitHub and in other editors, and the `id` shows
    only on hover.
  - When a page is renamed or moved in PetalBook, the paths in all the files that
    link to it are changed in the same commit; those files are found from the
    `links` table, not by scanning the disk.
  - When it is renamed or moved directly outside, after the changes are fetched the
    new location is found by `id`, and the link path is changed back automatically
    (as one commit by the service); the ones that cannot be found are listed as
    broken links on the validation page.
  - A link title is treated as an `id` only if it is exactly one `id`; other title
    text is kept as is, and such a link is recognized by path only.
  - A link with only a path and no `id` (handwritten or imported from elsewhere)
    is still recognized, and resolved by path; the next time PetalBook saves this
    page it adds the `id`.
  - Later, linking to a subheading uses `#anchor`, which does not conflict with
    `id`.
  - The display text in the editor is the title at the time it was written to the
    file; when the target's title changes, the link text does not change
    automatically (changing someone else's body needs care), and the backlinks
    and the hover tip show the new title.
- **Index**: add `links(src, dst)`, indexed in both directions; when a page is
  saved, only the outgoing links of that page are recomputed. Backlinks and broken
  links are both queried from this table, without scanning files.
- The right-column "Backlinks": which pages link to this one, with a context
  excerpt.
- After a page is deleted, links pointing to it are marked as broken in the
  editor, and other files are not changed automatically.

2.4 implementation plan (2026-11-23):

- **Recognizing links**: a Markdown link in the page body whose target is a
  relative path inside the space (ending in `.md`, may carry `#anchor`), or whose
  title is exactly a page `id`. External links, images and attachments do not
  count. Those in code blocks and inline code do not count. Parsing uses the
  server's existing Markdown parser (goldmark), and its results must agree with the
  editor's conversion layer on the same set of cases (added to the round-trip
  tests).
- **Index**: `links(src INTEGER, dst_id TEXT, dst_path TEXT, anchor TEXT, text
  TEXT, pos INTEGER)`; `dst_id` is used if present, and otherwise the path is
  resolved to an id, and one that cannot be resolved has an empty `dst_id` and is
  recorded as a broken link. Indexes `(dst_id)`, `(dst_path)`, `(src)`. When
  building the index and on each save, only that page's outgoing links are
  recomputed. The index version is bumped by one (rebuilt once on first start).
- **Endpoints**:
  - `GET …/pages/{id}/backlinks?cursor=`: the pages that link to this one, with an
    excerpt of the paragraph where the link is (reading only those source files,
    20 per page of results).
  - `GET …/pages/{id}/links`: the outgoing links of this page, each marked with the
    target's current title and whether it is broken (used by the editor to mark
    broken links and for the hover tip).
  - The page picker reuses the existing search endpoint.
- **Changing links on rename and move**: in the `space` package, the write that
  renames or moves a page or folder, in the same commit and the same journal
  transaction, changes the paths in the referring files found in the `links` table
  (only the path part, keeping the link text, the `#anchor` and everything else;
  incidentally adding an `id` where there was none). When there are many
  references (a limit of, say, 500 files) it is still done in one go, and the time
  is measured. A remote space is likewise pushed as one commit. Crash recovery:
  changing the links belongs to the journal of this move, and is finished together
  on recovery.
- **Repair of external renames**: after a pull, when by `id` the target is found
  to have changed location while the referring file still has the old path, the
  service changes it back automatically (as one commit); when one `id` has two
  pages or cannot be found, nothing is changed, and it is listed on the 2.5
  validation page.
- **Adding `id` on save**: when a page is saved, among its outgoing links those
  that can be resolved to a page but carry no `id` get the `id` written into the
  link title position (those that already have other title text are left alone).
- **Frontend**: typing `[[` in the editor pops up a page picker (usable by
  keyboard), and after a choice inserts `[title](relative path "id")`; a link is
  shown as an ordinary link, a broken link in a different style, and hover shows
  the target's current title; clicking opens it within the app. The right column
  gets a new "Backlinks" panel (load on demand).
- **Cost**: saving a page only adds parsing of itself; rename and move change more
  of "the files that link to it", found from the index, without scanning the disk;
  backlinks are paged. Measure the rename time at 50,000 pages with one page
  referenced by 1,000 pages.
- **A service rewrite of links is not a conflict**: for a page that someone else
  has open, or that has a draft stored on the server, if it fell behind only
  because the service rewrote link paths in it (a linked page was renamed or
  moved, or it was moved itself), saves and drafts are accepted as usual, the same
  rewrite is applied to this text, no conflict pops up, and the old path is not
  written back. The rewrite records are stored in the state directory (at most
  20,000, still there after a restart).

**2.4 as built (2026-11-23)**: everything in the plan above was built; see
`docs/api.md` for the endpoints. Reviewed for two rounds under the new review
rule (the first round comprehensive, the second looking only at the fixes), and
decided during implementation:

- Link parsing uses goldmark (the unused autolink extension removed), and was
  compared against the editor's conversion layer on the same set of 53 cases, with
  identical results; reference-style definitions (`[r]: y.md`) are also rewritten.
- A link with only a path and no `id` is resolved by path at read time, so it is
  right after the target is created, moved or deleted; a link with an `id`
  recognizes only the `id`.
- Rewriting the referring files on rename or move is in the same transaction and
  the same commit as the move (one push for a remote space), the journal records
  `w`/`l`, and it is completed at startup after a crash.
- For a page whose links the service rewrote, if someone else has it open or has a
  stored draft, saves and drafts are accepted as usual, and the same rewrite is
  applied to this text (the rewrite records are stored in the state directory, at
  most 20,000), with no conflict popping up.
- On save the missing `id` is added, and when the `id` points elsewhere the path is
  corrected along the way.
- Editor: `[[` / `【【` choose a page (not popping up while an input method is
  composing), when the link address is changed the `id` is swapped or removed along
  with it; writing `Other.md` directly is taken as a page path; a page with 2,000
  links takes about 1 ms per keystroke; the right-side "Backlinks" panel, which at
  narrow widths is a closable overlay.
- Measured (50,000 pages, one page referenced by 1,000 pages): rename 348 ms, move
  332 ms (each rewriting 1,000 files), commit 513 ms, first page of backlinks 5
  ms.

2.4 known issues, carried forward (each needs several uncommon conditions at once):

- **Redoing an `l` record after a crash is not idempotent**: if the process
  crashes after the moved page's own links have been rewritten but before `ld` is
  written, the next start parses the already-rewritten content again against the
  old location, and relative links without an `id` get changed wrongly (such as
  `A/B/A/B/y.md`). In a remote space, for a move that was undone, or a rename
  whose file name was tried and then taken, the `l` record is also left unclosed
  and may change things wrongly at the next start. It appears only when the crash
  happens exactly between these steps.
- **A background commit during a half-done rewrite**: in a local space,
  `writeEdits` first hands files to the committer and then writes them one by one;
  if the idle commit fires right in the middle of writing, one rename is split into
  two commits (no content is lost, only the history is not tidy). It appears only
  when many files must be changed and the commit interval happens to come due.
- **A repair commit's push is rejected**: for a link-repair commit made by the
  service after a pull, if someone pushes a new commit to the remote before the
  push, the next round treats it as a history fork, saves this repair as a
  "rescued commit" and marks the remote as in error, which needs an administrator
  to look. It appears only when both sides push within a few seconds of each
  other.
- **Still typing during a save**: if the user keeps typing after the save request
  is sent, and the server made a link rewrite on this save, the editor still keeps
  its own old text, and the next save writes it back at the new version number,
  no longer applying the rewrite. Links with an `id` are corrected by `id` on
  save; links without an `id` (written outside PetalBook, or with a title the user
  wrote) are written back with the old path and become broken links.
- **`Equivalent` does not check the end of the rewrite chain**: after a rename
  and a rename back, deciding whether a draft is the same as the file applies a
  few more rewrite steps, with the result that a draft that could have been
  discarded is kept; no content is lost.
- **The rewrite-record log file is compacted only at startup**: at most 20,000
  entries in memory, but the records on disk are only appended while running, so
  with a long run and a lot of folder moves it keeps growing, and at startup all of
  it is read.
- **One rewrite over 20,000 files**: when one folder move rewrites more than
  20,000 files, the earliest steps are pushed out of the records, and the drafts
  on those files show as "behind", as before this repair.

**2.5 Templates and the validation page**

- A template is an ordinary page under `.petalbook/templates/`, which may carry
  type fields and a body; the variables support only date and time (`{{date}}`,
  `{{time}}`). A template is chosen when creating a page; a folder can set a
  default template, and a quick note can also specify one.
- **The validation page**: lists the pages in the space that have problems: a
  missing or duplicate `id`, a header that is not valid YAML, a type field that
  does not match the schema, a broken link, an attachment that cannot be found, a
  duplicate title in the same folder. All the data comes from the index
  (`problems`, `props`, `links`), checked only once when the index is built, and
  opening the validation page does not scan the disk. Those that can be fixed
  directly get a button (add an `id`, clear a field not in the type), and the rest
  are opened and fixed by hand.

2.5 implementation plan (2026-11-23): the endpoints and error codes are first
written into the "Templates" and "Checks" sections of `docs/api.md`, and the
frontend and backend are built in parallel from it.

- Done in two parts, each reviewed: templates first (not dependent on 2.4), then
  the validation page; the "broken link" item of the validation page is connected
  after 2.4 is merged into `main` (it has been merged, the index version is 9).
- Templates live in `.petalbook/templates/`, read and written as ordinary page
  files, not in the page list or search; the default template is written in
  `space.yml` (`templates:`), following the 2.2 settings-file rules; when a folder
  is renamed or moved it changes along (like bindings and views).
- The variables are only `{{date}}`, `{{time}}`, `{{datetime}}`, in the client's
  time zone.
- Validation results are recorded when the index is built (extending the existing
  `problems` table, adding kind, page, detail), and opening the validation page
  queries only the index. Two kinds can be fixed directly: adding an `id`, and
  removing a field not in the type.
- Broken links (after 2.4 is merged): the `links` table records one more column
  `line` (the line it is on) for each link, and broken links, like other problems,
  are stored in the `checks` table (`seq` is the link's ordinal within the page,
  at most 20 per page). Whether a link is broken depends on other pages, so when
  writing the index two kinds of "dirty" are recorded: pages whose links changed,
  and ids and paths that appeared or disappeared; before commit the pages linking
  to them are found through the two indexes on `links` and recomputed page by page
  (over 2,000 pages, or on a whole rebuild, one SQL statement recomputes all of
  them, and about 100,000 links for 50,000 pages takes about 40 ms). Saving, the
  rewrite on rename and move, pulling and rebuilding all go through this write
  path, and opening the validation page does not touch the links.
- Each problem carries the file's current `rev` (the `baseRev` of `checks/fix`):
  a file without an `id` is not a page, and the client has no other place to read
  its `rev`.
- Frontend: a template can be chosen when creating a page (preselected when the
  folder has a default template); a "Templates" section in space settings (list,
  edit, set as folder default, quick-note default); the "Checks" page is entered
  from the sidebar or space settings, grouped by kind, with counts and paging, and
  a button for those that can be fixed.

**2.5 as built (2026-11-23)**: everything in the plan above was built; see
"Templates" and "Checks" in `docs/api.md`. Reviewed as it was written: the
template backend, the template UI and the validation page backend each reviewed
once, and the final overall review looked only at how they join up. Decided during
implementation:

- Quick note means "created in the inbox with no template specified"; it first
  uses the quick-note default template, then the inbox folder's default template;
  a folder default template applies only to that folder itself and is not
  inherited by subfolders; the root cannot have a default template. Fields with a
  value in the request override the template, and the template's type is kept.
  When the template a default points to has been deleted, it is skipped on
  creation without an error.
- When `.petalbook/templates` has been replaced by a link or a file, it recovers
  itself like the types directory (moved aside, rebuilt).
- Validation is recorded when the index is built, and on save, move, delete and
  pull only the affected pages are recomputed; broken links use the 2.4 link
  table; every problem carries a `rev`, and one-click fixes (add an `id`, remove a
  field not in the type) go through a normal save and keep drafts. Opening the
  validation page queries only the index: about 2 ms per page of results at 50,000
  pages.
- The validation page is entered from the sidebar and from "General" in space
  settings.

**2.5 known issues**

- Only the fields title, subtitle, source, type, tags and creation date "keep the
  template's value when empty in the request"; other fields (such as `status: ""`)
  overwrite the template's value with an empty value, while the frontend's mock
  server keeps the template's value for all fields, so the two sides disagree.
- A template file over 256 KiB (which can only come from a pull) can be neither
  overwritten (503) nor deleted (404 when no default template refers to it), and
  only that one name is stuck.
- When saving a template edit meets a conflict (409), the template is read in
  again and the prompt "The changes were not saved" is shown for 12 seconds, but
  what was just filled in is not kept; when another administrator saves the same
  template at the same time, the edit box is also refreshed.
- When in a hand-edited template the title or subtitle is not text (such as
  `title: 2026`) or the tags contain a non-text item, the form treats it as empty,
  and saving again drops this field.
- When the case of a folder name differs from the one in `space.yml`, creating a
  page does not preselect this folder's default template (the server matches
  case-insensitively, the frontend matches exactly).
- When an attachment is deleted or comes from a pull, "attachment not found" is
  updated only after the page that references it is saved again or the index is
  rebuilt.
- Template files themselves are not in the index, so when a template carries
  fields outside the type, the validation page does not point at the template, and
  only lists every page generated from it as "field not in the type", and fixing
  one page clears only that one.
- On the validation page "being fixed" marks which row by path; currently only
  "broken link" appears in several rows on the same path, and it has no fix
  button, so there is no effect.

**2.6 API tokens and MCP**

- API tokens follow Rackbook's `internal/tokens`: only the hash is stored, scoped
  to one or several spaces, read-only or read-write, with an expiry time, and
  revocable at any time. A token's permissions never exceed the issuer's
  permissions in that space, and if the issuer is removed or demoted it takes
  effect immediately (through the 1.7 member cache).
- MCP is at `/mcp`, copying Rackbook's `mcpserver`, `oauthserver` and `mcpauth`.
  The tools follow the list in [auth-and-permissions.md §6.3](../auth-and-permissions.md#63-machine-access):
  list spaces and folders, search, read a page, create, change, move, tag, view
  history; plus the query by type fields after 2.1. Lists and search go through the
  index and are paged, the number of items and the body length returned at once
  have limits, and even a space with tens of thousands of pages will not spit out
  everything at once.
- Writes go through drafts and commits, the author is the token's owner, and the
  commit message ends by noting it came from MCP.
- Single-user mode has no accounts: generate a local token with the command line
  (`petalbook token`), and MCP connects with it.
- Access through MCPHub comes after this, to be done after separately confirming
  the token format on the MCPHub side.

The 2.6 contract (2026-11-23): the endpoints, tools and error codes are first
written into the "API tokens", "OAuth for MCP clients" and "MCP" sections of
`docs/api.md` and "Machine access" under "Administration", and the frontend and
backend are built from it. What was decided:

- **Token**: `pbk_` plus 32 random bytes (base62); the state database stores only
  the SHA-256 and the first 12 characters; one token is scoped to 1 to 100 spaces
  (there is no "all spaces"), `read` or `write`, expires in 90 days by default (1
  to 365 days, or never), and each person can have at most 50 valid ones. Revoked
  and expired ones are kept 90 days and then deleted, and in that time answer
  `token_revoked` / `token_expired`.
- **Permissions**: each request takes the owner's current permission in this space
  (the 1.7 member cache, no database query) and intersects it with the token's
  `access`; a token can never get `members` or `manage`, and does not take the
  administrator's route into spaces they are not a member of. A space not in the
  token answers `403 token_scope`, an owner who is no longer a member answers
  `404`, the same as the web; an owner who has been disabled answers
  `401 token_owner_disabled`.
- **Lookup cost**: like sessions, hash first and check memory; a hash not seen
  before does one unique-index query, and a miss is remembered for 30 seconds;
  records in memory are re-read at most every 30 seconds (revoking from the command
  line in another process takes effect within 30 seconds; revoking through the
  service takes effect immediately); the last-used time is written at most once an
  hour. A request that hits the cache does not query the database. Consecutive
  failures are rate-limited by client address (10 per minute).
- **The endpoints a token can use**: routes in a space that need `read` / `write`,
  `GET /api/spaces`, `/api/meta`, `/api/session` (with one more field, `token`) and
  `/mcp`; accounts, users, administration and the management of tokens themselves
  are all off limits, so a token cannot issue another token.
- **Single-user mode**: only the command line `petalbook token create|list|revoke`,
  reading and writing the state database directly, and usable while the service is
  running; `/api` requests without a token are as before, and `/mcp` must carry a
  token. OAuth exists only in account mode and when `--public-url` is given.
- **OAuth**: copies Rackbook's authorization service (OAuth 2.1, PKCE with S256
  only, RFC 8707, RFC 9207, client metadata document first, dynamic registration as
  fallback, public clients), with the clients and the authorization records moved
  from JSON files to the state database (both SQLite and PostgreSQL tested). The
  signing key is also in the state database, sealed with the server key
  (`secret.key`) like other credentials, and not kept in a separate file; if the
  server key is changed it cannot be opened, and the service does not start,
  rather than quietly swapping in a new key that invalidates all authorizations.
  The scopes are only `read` and `write`, and the spaces are ticked on the consent
  page (by default only the personal space is ticked); after one authorization the
  behavior is exactly the same as a token of the same scope. The access token is a
  10-minute ES256 JWT, verified only within this process, and verified ones are
  cached by hash until expiry; the refresh token is rotated every time, a resend
  within 60 seconds gets the same reply, and reusing it after that ends the whole
  authorization; an authorization not refreshed for 90 days becomes invalid. An
  OAuth access token can be used only on `/mcp` (`/api` answers
  `403 token_scope`). When the MCP switch under machine access is turned off, all
  endpoints of the authorization service answer `404`, but the authorization
  records are kept, and the account page and the administration page can still list
  and revoke them. When the owner is disabled, refresh answers `invalid_grant` and
  the authorization is kept, and continues to be valid after the account is
  enabled. Besides `https` and loopback `http`, a callback address also accepts the
  private schemes of native apps (`cursor://` and the like, RFC 8252), which Cursor
  and VS Code need. The same person can have at most 20 authorization requests
  waiting for a decision at once, and the whole server at most 1000; those over the
  limit show `temporarily_unavailable` on the error page (the personal limit
  answers `429`, the whole-server one `503`).
- **An authorization request error no longer redirects back to the client** (changed
  in the review of 2.6 part two, **this changes the original contract above, pending
  confirmation by the owner**): the original contract was that errors in parameters
  other than `client_id` and `redirect_uri` carry `error` and redirect back to the
  client's `redirect_uri`; now, following RFC 9700 section 4.11.2, every error
  before the user consents (a bad `response_type`, `scope`, PKCE or `resource`, too
  many requests) shows only this server's own error page and does not redirect, so
  that a self-registered client or an arbitrary metadata document cannot use this
  server as an open redirect. Only after the `redirect_uri` has been verified and
  the request itself is valid, when the user refuses on the consent page, does it
  redirect back with `access_denied`.
- **MCP transport**: Streamable HTTP, stateless, replying only with JSON (no SSE,
  no MCP session), so an idle client takes no resources. The `Origin` check is as
  before, against DNS rebinding.
- **Tools**: `spaces_list`, `folders_list`, `tags_list`, `types_list`,
  `pages_search`, `pages_query` (the 2.1 filtering and sorting by type field, with
  the same parameters as the REST list), `page_get`, `page_history`,
  `page_version`, `page_create`, `page_update`, `page_move`, `page_tag`. All lists
  are paged and go through the index; search returns at most 50 items each time and
  query at most 200, items carry no body and the excerpt is at most 400
  characters; the body is at most 128 KiB each time, continued by byte offset; one
  result is at most 1 MiB, and over that it answers `too_large` with a suggested
  count. A written body is at most 1 MiB.
- **Changing the body**: `page_update` can replace the whole page, or give a series
  of exact "original text → new text" replacements (each original text must appear
  exactly once), so that changing a long text of hundreds of thousands of
  characters does not mean passing the whole text back and forth.
- **Writes and drafts**: an MCP write is the same as a REST save (`baseRev`
  conflict check, journaled, committed, and for a remote space push first and then
  answer), and **does not go through the owner's draft**: a page has only one draft
  per person, and MCP writing into it would overwrite what the person is typing, so
  following the `PUT` rule it does not touch the draft, and the draft shows as
  behind in the web. The `baseRev` of `page_tag` and `page_move` is optional
  (adding or removing tags and moving do not overwrite anyone's text);
  `page_update` must carry it. `page_create` can carry a `requestId`, and a resend
  within an hour will not create two pages (in memory only).
- **Commit message**: the author is the owner; an MCP change is not merged into one
  commit with changes from other sources (when the source changes it commits first,
  like switching author in 1.7), and the commit message gets the trailer
  `Changed-via: MCP` at the end; a change through `/api` with an API token gets
  `Changed-via: API token`. A history entry gets one more field `via`
  (`mcp` / `token`). The trailer does not contain the token's name (the name is
  whatever the user chose).
- **Administrator**: `/api/admin/access` has two switches (token, MCP; both on by
  default; turning off does not delete data), and lists and revokes everyone's
  tokens and authorizations. Single-user mode turns MCP off with
  `serve --mcp=false`.

Deliberately not done: tools to delete pages and folders, move folders, or change
types / templates / settings / members; MCP resources and prompts; a backlinks
tool; a JWKS endpoint (only this process verifies tokens); request rate limiting
per token (only failures are limited); an "all spaces" token; the token endpoints
and settings page in single-user mode; access through MCPHub (after 2.6).

For the owner to decide (do what is in parentheses first):

- Whether a token may never expire (yes, it must be chosen explicitly; default 90
  days).
- Whether a single-user mode token is read-only or read-write by default (default
  read-only).
- MCP writes do not go through drafts (done as above); or should it first be saved
  as the owner's draft and then committed.
- The trailer wording `Changed-via: MCP` / `Changed-via: API token`.
- Whether to limit the request rate per token (not for now).

Known issues (noted in the review of 2.6 part one, not handled for now):

- On PostgreSQL, when the same person creates two tokens at the same moment, both
  may pass the "at most 50 valid" check, with the result of one or two too many. The
  limit counts first and writes after, without a lock; SQLite has only one writer
  and is not affected. The excess does not affect use, and revoking one is enough.
- Whenever a commit carries a `Changed-via` trailer, the history shows `via`,
  including commits pulled from the remote: a trailer written by someone else (or
  by another PetalBook) also shows here as MCP or API token. `via` only says what
  the commit message says, and is not a source this server has verified.

Known issues (noted in the review of 2.6 part two, the OAuth service, not handled
for now):

- The detection of authorization code replay has a gap: a replay that arrives while
  the first exchange is still saving the authorization record is not detected; and a
  replay arriving 60 seconds after the exchange is no longer recognized as a replay
  (it only answers `invalid_grant` and does not end the authorization). PKCE still
  protects: someone without the `code_verifier` cannot exchange the code for a token
  even if they get it.
- Refresh tokens that have been replaced are remembered only for the most recent 8;
  when an older old token comes again it cannot be recognized as reuse, and only
  answers `invalid_grant`, not ending the whole authorization.
- Refresh uses one lock for all authorizations (only one refresh at a time). SQLite
  has only one writer anyway, so there is no effect; on PostgreSQL the refreshes of
  different authorizations could run in parallel, and this lock limits throughput.
- The same reply obtained by a resend within the 60-second grace period exists only
  in memory. When several processes run on PostgreSQL, a retry that lands on another
  process does not get it and answers `invalid_grant` (the client must authorize
  again).

**2.6 as built (2026-11-23)**: everything in the plan above was built; see "API
tokens", "OAuth for MCP clients", "MCP" and "Machine access" under
"Administration" in `docs/api.md`. Each of three parts was reviewed once (tokens,
the authorization service, MCP). Decided during the implementation of part three
(MCP):

- **Location and SDK**: the tools are in `internal/api/mcp*.go`, sharing with REST
  the same query parsing (`parseListQuery`), error mapping and summaries, so the
  same failure is the same code and the same sentence on both sides (a tool's error
  is the one `fail` writes). The MCP SDK is v1.8.0 (the same version as Rackbook,
  and the latest stable release at the time of writing), stateless and replying only
  with JSON, with GET / DELETE and so on all answering 405.
- **Mounting**: `/mcp` is mounted separately on the mux, not through `handle`.
  `handle` rejects every non-GET for a read-only token (in `routes.go`), while
  `/mcp` is a POST every time. Authentication (API token, OAuth access token) is
  done once in `serveMCP`, and after that each tool decides by itself whether it is
  reading or writing (`mcpCall.open`). Space permission takes the same path as the
  web: `OpenMember` takes the owner's current member permission, intersects it with
  the credential's `access`, and there is never `members` / `manage`.
- **Errors**: a tool failure returns `isError` plus structured content. The SDK's
  own parameter validation failure is also an `isError` result, but without
  structured content; the middleware changes such a result into JSON-RPC `-32602`,
  which is just what the contract asks for. Where the parameter type is right and
  the value is not (`limit` out of range, a bad cursor) it is `invalid`.
- **`tools/list`**: when the credential cannot write in any of its spaces (a `read`
  token, or the owner is only a viewer in all those spaces), the write tools are not
  listed; this step checks the member cache once, without a database query.
- **Limits**: the result is measured once after serialization, and if it exceeds 1
  MiB it answers `too_large` instead, and list kinds attach `suggestedLimit`
  (proportionally another 10% lower, and always smaller than the count returned this
  time, so following it always makes progress). The body is chunked by byte offset
  (128 KiB, cut on a character boundary; when the header is large it is cut smaller
  by the space left after subtracting the rest from 1 MiB, and when the header alone
  leaves no room for any body it answers `too_large`). A written `body` is at most 1
  MiB; `edits` limits only the combined size of `old` and `new`, so a long text of
  hundreds of thousands of characters can also be changed.
- **Changing the body**: a new `space.Update`, which within one hold of the space
  lock reads the file, hands it to the caller to change, and writes it, so `edits`
  always act on the current file, with no gap between "read first, write after";
  the `baseRev` conflict check is the same as `Save` (a difference caused only by the
  service rewriting links is not a conflict). When the whole `body` is replaced it
  uses `space.UpdateBody`: the caller's body was written at `baseRev`, and link
  rewrites the service did later are applied to this body as `Save` does, with the
  same result as a web save. The `baseRev` of `page_move` uses the new
  `space.MoveIf`, and the other fields of `page_create` / `page_update` use
  `space.PatchMeta` (the rules of `PATCH …/props`, except it writes the whole
  header). `drafts.Service.Write` makes such writes queue in the same page's draft
  lock as `PUT`, the owner's draft is left alone and shows as behind in the web; the
  draft is deleted along the way only when what it would write out is exactly the
  same as the file.
- **Commits and source**: the author is the owner, and `Changed-via: MCP` is on the
  last line; MCP changes are not merged with other sources, but several consecutive
  MCP changes (by the same person) are still one commit, like the same person typing
  continuously.
- **`requestId`**: an hour, at most 10,000 entries, in memory only; when the same
  key arrives twice at the same time, the second waits for the first to finish and
  takes the same reply (the wait follows the context of its own request); failed or
  panicked ones are not recorded. Before a replay the permission is checked as
  usual, and an owner who was demoted or removed gets a refusal. The key contains
  the credential's id, so switching to another token will not get someone else's
  reply.
- **Single-user**: `serve --mcp=false` turns off `/mcp` (answers 404); in account
  mode giving this option is an error, because the switch is the administrator's in
  the UI.
- **Tests**: part two's `NewWithMCPStandIn` (which let `/api` accept an OAuth access
  token) was removed, and its permission tests now go through `/mcp`; `/api`
  receiving an access token still answers `403 token_scope`.

Where it deviates from or adds to the contract: the result of `types_list` is
`{"types": [...]}` (the contract did not give the key name, and it has been added to
`docs/api.md`); all non-`POST` methods answer 405 before the credential is looked
at; the `parent` of `folders_list` finds the folder case-insensitively like the web;
the size limit of `page_update`'s `edits` was changed to count only `old` + `new`
(the contract only mentioned `body`). The SDK writes the response header
`Cache-Control` as `no-cache, no-transform`, without `no-store`; there are no keys
in the result, so it was not changed.

2.6 part three (MCP) known issues, not handled for now:

- `tags_list` and `folders_list` re-read the whole table on every page and then slice
  it (the same single query as `GET …/tags` and `…/tree`, counted by the number of
  tags and folders, not by the number of pages). Measured at 50,000 pages and 5,000
  tags / folders, each call takes about 11 ms and 6 ms. Later the paging can be moved
  into SQL.
- The structured content and the text of a result each carry a copy of the JSON; the
  1 MiB limit covers only the structured content, so on the wire it is about twice
  that (the text escaped once more, three times in the worst case), and
  `docs/api.md` states it accordingly.
- On `/oauth/*` the "localhost only" refusal and the 404 / 405 fallbacks still answer
  in the API's error format; the branch where a cross-site request is refused has no
  test.
- The middleware treats every "`isError` without structured content" as a parameter
  error. For now only the SDK's validation does this; if in future the SDK returns
  such a result elsewhere it would be wrongly reported as `-32602`.
- When `page_tag` meets a non-text item in `tags` (a hand-edited file), writing back
  drops these items; `tags: x` (a single string) is written back as a list.
- `requestId` is only in this process's memory: when several processes run on
  PostgreSQL, a retry that lands on another process is not recognized and creates a
  second page.
- `page_get` finds a page by `path` through the index: for a file changed outside the
  service it can be found only after the index is rebuilt (taking by `id` repairs the
  index first, like REST).
- In a remote space one write may be retried several times, so `page_update`'s change
  function is called several times (with the same result, just computed again).

Known issues noted in the second-round review, not handled for now:

- When replaying a `requestId`, what is checked is the space named in the request,
  not the space the recorded reply belongs to. A replay only gives the same credential
  its own reply back, so it is not a security issue.
- When the body is under 1000 bytes, `page_get`'s estimate of the remaining space may
  be off by 3 bytes, so when the header size lands almost exactly on the limit it may
  still answer `too_large`.
- The response to a JSON-RPC batch request goes through the ordinary `json.Marshal`,
  which escapes each of `<>&` into 6 bytes. It does not take this path in normal use.

Noted in the overall review (how the three parts join):

- The frontend's contract tests and mock server cover only the administration of
  tokens, authorization, the consent page and the switches, and have no cases that
  call `/api` with a bearer token (`token_scope`, `tokens_off`, a read-only token) or
  `/mcp`; those are only in the Go tests. The web itself does not use tokens, so it
  stays like this for now.
- When "API tokens" is turned off, connected apps (OAuth) can still use `/mcp`; to
  stop them MCP must be turned off; the documentation and the administration page's
  text have been changed to say so.
- Token failures and OAuth failures are counted separately, and the same address can
  try 10 times a minute for each; written into the documentation.
- A credential is checked only once when the request comes in, and a request in
  progress is still finished after revocation (at most one write); written into the
  documentation.
