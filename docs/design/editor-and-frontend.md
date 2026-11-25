# Editor and frontend

## 7. Editor

- WYSIWYG, with a `/` command menu, a formatting toolbar that pops up when text
  is selected, and drag handles to reorder blocks.
- Supported: headings, bold, italic, strikethrough, inline code, links,
  bulleted/numbered/task lists, quotes, code blocks (with language
  highlighting), tables, images, attachments, horizontal rules, footnotes.
- Images: paste, drag in, or click to upload; alt text can be filled in.
- People who know Markdown can type shortcuts such as `#`, `-` and `>` directly.
- **Links**: when the cursor is in a link, a small popover appears, where you can
  change the address, change the text, open it, or remove the link.
- **Tables**: add row, add column, delete row, delete column, delete table, set
  column alignment, all from the menu next to the cell. A Markdown table must
  have exactly one header row, the first row, so the header row cannot be turned
  off; after the header row is deleted, the next row automatically becomes the
  header. Merged cells are not supported.

### 7.0 Three editing modes

Each document can be switched at its top right; the default is WYSIWYG, and the
choice is remembered per user:

1. **WYSIWYG**: the default, and no need to know Markdown.
2. **Markdown source**: edit the text of the whole file directly, including the
   header fields. This is the fallback: content that the editor cannot handle or
   displays wrongly can always be fixed here. It uses a code editor with
   Markdown highlighting (CodeMirror).
3. **Side by side**: source on the left, WYSIWYG on the right, both editable,
   synchronized with each other, with linked scrolling.

Rules:

- What is typed in the source is kept as is, and what is saved is the text you
  wrote.
- Once a change is made from the WYSIWYG side, the body is rewritten into a
  uniform style: the content does not change, but, for example, a `*` list
  becomes `-`, and the separator row of a table is normalized. This happens once
  and is stable afterwards.
- When the header in the source is broken (not valid YAML, `id` deleted), you
  cannot switch back to WYSIWYG; the UI points out which line, and you switch
  after fixing it.
- For large documents in side-by-side mode, synchronization must be debounced (a
  50,000-character document takes about half a second to convert once).
- Features that Markdown cannot express are not provided, to guarantee a
  lossless round trip of the file.
- Phase 2: `[[link]]` to other pages, stored by ID, displaying the title.

### 7.1 Conclusions of the round-trip validation (2026-11-21)

The validation code and full results are in `spikes/markdown-roundtrip/`.

- The editor uses Tiptap, but reading and writing Markdown does not use the
  official extension; we implement "editor document ↔ mdast ↔ Markdown text"
  ourselves on top of remark.
- Of the 116 cases, the official extension passed only 75: bold next to Chinese
  punctuation is lost, a `#` or `-` at the start of a line is not escaped, and an
  image address that contains spaces is written wrongly. The in-house conversion
  layer passed 113. Of the remaining 3, 2 are spaces at the edge of bold being
  moved outside the bold, which the eye cannot see; 1 is a bare URL in the body
  that becomes a link when reopened, which is a visible change, and must be fixed
  by writing bare URLs as `<URL>`.
- The conversion layer handles only the body. The file header has to be cut off
  first and parsed separately, and that part is not written yet.
- Syntax that has no corresponding form in the editor is rewritten once on read:
  a reference-style link becomes an inline link, and a task item inside an
  ordered list becomes unordered.
- Emphasis next to Chinese punctuation uses `remark-cjk-friendly`. This is not
  standard CommonMark: viewing the original file directly on the GitHub web page
  will show the asterisks. Every place in this project that renders Markdown
  (editor, server, public publishing) must use the same rules.
- Content it does not understand is not lost: raw HTML is kept as is as a
  non-editable block; footnotes have a dedicated node.
- On save, things that are invisible and cannot be stored are dropped: spaces and
  line breaks at the end of a paragraph, heading or cell, spaces at the start of a
  cell, and empty paragraphs at the end of the document. Full-width spaces are
  kept.
- Conventional forms: a hard line break uses a trailing backslash, an empty
  paragraph is written as a `<br>` on its own line, tables get no column
  alignment padding, a single `~` is not strikethrough, and underline is not
  provided.
- Performance: 5,000 characters take 74 ms to read in and 20 ms to write out;
  50,000 characters take about 0.5 seconds to read in. Autosave must be debounced.

### 7.2 Conclusions of the browser measurements (2026-11-21)

Tested in the browser with real keyboard events and paste
(`spikes/markdown-roundtrip/browser/`): typing shortcut syntax, keyboard
shortcuts and rich-text paste all work, and at every step "write out and read
back" is identical. The real implementation has to add three things:

- Write our own input rule for emphasis. Tiptap's built-in one requires a space
  or the start of the line before `**`, and does not fire when a Chinese
  character follows right after.
- A custom plain-text paste: a single line break becomes a line break within the
  paragraph, and only a blank line starts a new paragraph. The default behavior
  turns every line into a paragraph, and lines of a poem or text split into lines
  get broken apart.
- When pasting Markdown source, parse it into formatted content with the
  conversion layer.

### 7.3 Server-side rendering (2026-11-21)

The server renders Markdown with goldmark plus the `goldmark-cjk-friendly`
extension. Comparing the frontend and Go rendering of the 116 Markdown documents
the editor actually wrote, 114 are identical; without the extension it is 106,
and the mismatches are mainly emphasis next to Chinese punctuation. See
`spikes/render-parity/` for details.

The remaining 2 have to be fixed in the conversion layer: when bold and italic
partly overlap, use `_` for italic, to avoid the ambiguity of four asterisks in a
row; and bare URLs in the body are always written as `<URL>`. This comparison test
has to go into CI.

Not yet verified: a full-volume round trip of real texts (the tests use written
samples; this waits until content is imported); math formulas.
Composition input with Chinese input methods is also not yet verified: automated
tests do not go through an input method, and a real person has to try a pinyin
input method in the lists, tables and inline code of the test page.

## 8. Frontend

### 8.1 Technology

React, Vite, Tailwind, shadcn components, TanStack Query, react-router, cmdk,
consistent with Rackbook; the shell and the i18n mechanism are copied over.
refine is not used (it serves table-style admin back ends, and is of no use
here). The UI supports Chinese and English from the first phase.

### 8.2 Layout

- **Left column**: space switcher, quick capture, search (⌘K),
  inbox/recent/favourites, folder tree, tags. Can be collapsed.
- **Middle**: folder view or the editor.
- **Right column** (expanded on demand): properties, outline, backlinks, version
  history.

### 8.3 Page editing

- At the top is the title, below it a line for the subtitle (shown as a light
  placeholder when empty, and shown by default for the text type), then the tags
  and type properties, then the body.
- The body has a limited width, with line height and letter spacing tuned for
  reading Chinese; two body fonts are offered, sans-serif and serif.
- The top right shows the save state: "Draft saved", "Version saved", "Offline".
- Focus mode: hide the side columns and leave only the text.

### 8.4 Folder view

- **List**: title, subtitle, tags, updated time.
- **Cards**: suited to short texts; they show `《Title》｜Subtitle` and the body
  directly, many entries can be read in one screen, and the effect is close to
  that Notion page now.
- **Table**: available for a folder bound to a type; the columns are the type's
  properties, it can be filtered and sorted, and enums and dates can be edited
  directly in the cell (reusing Rackbook's approach).
- **Reading in sequence** (phase 2): lay a folder or a series out in order as one
  long article, like leafing through a collection.
- **Board** (phase 2): columns by some enum property, for example status or
  series, and dragging changes the value.
- **Timeline/calendar** (later): laid out by creation date, suited to diaries and
  quick notes.
- **Gallery** (later): uses the first image in a page as the cover.

A view is just a combination of "filter, sort, group, display mode"; the same set
of pages can have several saved views, and the configuration is written in
`space.yml`. Tag pages and search results can use these display modes too.

### 8.5 Quick capture

On any page, a shortcut key or a button pops up a small input box; what is
written goes into the inbox, to be sorted later. On a phone it is a permanent
floating button.

### 8.6 Phone

Single column. The left column becomes a drawer, with navigation and quick
capture at the bottom; while editing, the formatting toolbar sticks above the
keyboard. From the first phase it is made an installable PWA (installable only;
offline is in phase 4).

### 8.7 History and diff highlighting

The goal is an experience like Google Docs version history: someone who does not
know Markdown can see at a glance what was changed.

- The **version list** is in the right column, grouped by day, showing the time
  and the editor. A version is one commit. A version can be given a name (for
  example "First draft", "Submission version"), and named versions are shown at
  the top.
- **The diff is marked directly on the formatted body**: added text has a green
  background, deleted text is red with a strikethrough, and stays in place. It is
  not a code-style comparison in two side-by-side columns.
- **Compared by character**. First blocks (paragraphs, headings, list items) are
  aligned, and unchanged blocks are shown as they are; inside a changed block, the
  comparison is by word and character. Chinese word segmentation uses the
  browser's built-in `Intl.Segmenter`.
- **Format changes are marked too**: modifications with no text change, such as
  just adding bold or changing the heading level, are shown with an underline of
  another color.
- **Changes to header fields** (title, subtitle, tags, status) are listed
  separately above the body.
- **Images and tables**: when an image is replaced, the old and the new are shown
  side by side; tables are colored by cell.
- By default it compares with the previous version, and any two versions can also
  be chosen. It can be switched to a line-by-line comparison of the raw Markdown,
  for people used to reading text.
- **Restoring** to a version produces a new commit and does not rewrite history.
- In shared spaces, a different color per editor (phase 3).

In implementation, the Markdown of the two versions is each converted into an
editor document, the comparison is done on the document tree, and the result is
rendered with a read-only editor plus highlight decorations. The comparison is
computed in the browser on demand, only when the history panel is opened and a
version is selected; the server only provides the version list and the file
contents of a given version.
