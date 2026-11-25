# Data model

## 3. Data model

### 3.1 Directory structure of a space

```
<space>/
  .petalbook/
    space.yml            # space config: page types bound to folders, default views
    types/
      文案.json          # page type: JSON Schema for the extra properties
  Tang/
    暮雪.md
    残阳.md
  随笔/
    2026/
      某篇长文.md
  收件箱/
    2026-11-21-k3f9.md
  assets/
    01JD…/               # one directory per page ID
      photo.jpg
```

### 3.2 Page files

```markdown
---
id: 01JD3K8Q7Z
title: 暮雪
subtitle: 下一个路口吧 等水洼把天空还回来
source: 《某本书》第三章
tags: [玉兰, 栀子, 黄昏]
created: 2025-12-03
series: 冬季
status: 定稿
---
你听得见今天屋檐滴水的声音么？……
```

- **Built-in fields**: `id`, `title`, `subtitle`, `source`, `tags`, `created`.
  All except `id` may be empty. A loose, quick note may have no title.
  `source` is the origin (where it came from): a person, a book, a URL, filled
  in freely, and included in search.
- **Type fields**: things like `series` and `status` are defined by the page
  type's schema.
- **Body**: GFM Markdown.
- **Updated time** is not written into the file. The index derives it from the
  Git commit time.
- **File name** is taken from the title, with characters the file system does
  not allow removed. With no title, it uses the date plus a short ID. A
  duplicate name gets a suffix. Changing the title renames the file; `id` does
  not change.
- **`《title》｜subtitle`** is only the display template of the daily text type.
  It is not written into the text.

#### Pages without a title

A piece of text that is only a sentence or a short paragraph does not need a
title. This is normal, not a broken page:

- The front matter has no `title` field (not an empty string), and the file
  name uses the date plus a short ID, for example `2026-11-21-k3f9.md`.
- In lists, search results, links and browser tabs, the first line of the body
  (cut to about 30 characters) is shown in place of the title, in a lighter
  color to tell it apart from a real title.
- Card view and reading-in-sequence view show the body directly, leave no room
  for a title, and do not show the `《》｜` template.
- When editing, the title bar is a single light placeholder line. If left
  empty, it does not exist. The cursor lands in the body by default. Quick
  capture creates exactly this kind of page.
- When a title is added later, the file is renamed to the title and `id` does
  not change, so links pointing to it are unaffected.
- When sorting by title, pages without a title go last, ordered among
  themselves by creation time.
- The "duplicate title in the same folder" validation ignores pages without a
  title.

### 3.3 Organization

- **Folders**: just directories, nestable without limit. A page lives in
  exactly one place.
- **Tags**: several per page, across folders. Hierarchical names such as
  `花/栀子` are allowed. They are written in each page's front matter. The
  properties of a tag itself (color, description, order) go in
  `.petalbook/tags.yml`. Renaming a tag changes every file that uses it in a
  single commit.
- **Page types**: a folder can bind a type in `space.yml`, and the pages under
  it inherit it. The type decides which fields appear in the properties panel
  (the form is generated from the JSON Schema, reusing Rackbook's rjsf usage)
  and which views the folder has.
- **Sorting**: by name, creation time, or update time. Manual drag-to-sort is
  not done in the first version.

### 3.4 Attachments

- Uploaded to `assets/<page ID>/`, and referenced in the body by relative
  path.
- The default limit is 10 MB per file, configurable.
- Storage goes through an interface, and each space chooses one backend:
  1. **Inside the space repository** (default): the `assets/` directory, the
     simplest.
  2. **A separate attachments repository**: another Git repository, which keeps
     the text repository small and fast. It uses the same credential method as
     the space. Before a page is committed, make sure the attachments it
     references have been pushed.
  3. **S3-compatible storage** (later).
- Whatever the backend, attachments of private content are read out by this
  service and returned after a permission check. No public repository or CDN
  link is handed out directly. Public links are used only for published pages
  (see [reserved.md §14](reserved.md#14-reserved-public-publishing)).
- Before upload, the browser can optionally compress and convert to WebP, and
  file names get a content hash for de-duplication.
- When a page is deleted or a reference is removed, orphaned attachments are
  handled by a cleanup task and not deleted immediately.

### 3.5 Validation

Following the idea of Rackbook's `check`, each space is checked for:

- `id` missing, duplicated, or badly formatted.
- The header is not valid YAML, or a type field does not match the schema.
- Links to pages that do not exist, and attachment references to files that do
  not exist.
- Duplicate titles in the same folder (a warning, does not block).

Pages with problems are marked in the UI for people to fix. The whole space is
not refused loading.
