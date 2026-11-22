// The extensions of the page editor: the Markdown layer's own, with what only
// the page needs on top. The Markdown tests and the history view do not get
// these: they have no uploads and no pasting.

import type { Extensions } from '@tiptap/core'
import { Placeholder } from '@tiptap/extensions'
import { createExtensions } from '@/markdown'
import { addressToUrl } from '@/lib/fileAddress'
import { t } from '@/i18n'
import { PlainTextPaste } from './paste'
import { PageFiles, type PageFilesOptions } from './pageFiles'

export function createPageExtensions(files: PageFilesOptions): Extensions {
  return [
    // A link click edits instead of navigating; images are drawn from the
    // address the page keeps, mapped to where the browser can load it.
    ...createExtensions({ linkOpensOnClick: false, resolveImageSrc: (src) => addressToUrl(files.space, files.getFolder(), src) }),
    PlainTextPaste,
    PageFiles.configure(files),
    // The empty-body hint (display only).
    Placeholder.configure({ placeholder: t.editor.bodyPlaceholder }),
  ]
}
