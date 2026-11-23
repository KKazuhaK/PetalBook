// The extensions of the page editor: the Markdown layer's own, with what only
// the page needs on top. The Markdown tests and the history view do not get
// these: they have no uploads and no pasting.

import type { Extensions } from '@tiptap/core'
import { Placeholder } from '@tiptap/extensions'
import { createExtensions } from '@/markdown'
import { addressToUrl } from '@/lib/fileAddress'
import { t } from '@/i18n'
import { PlainTextPaste } from './paste'
import { CodeBlockWithView } from './code/codeBlock'
import { PageFiles, type PageFilesOptions } from './pageFiles'
import { TableTools } from './table/TableTools'
import { PageLinks, type PageLinksOptions } from './pageLinks'
import { PageLinkPicker } from './pageLinkPicker'

export function createPageExtensions(files: PageFilesOptions, links?: PageLinksOptions): Extensions {
  return [
    // A link click edits instead of navigating; images are drawn from the
    // address the page keeps, mapped to where the browser can load it.
    ...createExtensions({ linkOpensOnClick: false, resolveImageSrc: (src) => addressToUrl(files.space, files.getFolder(), src), codeBlock: CodeBlockWithView }),
    // Keeps tables writable as Markdown while they are edited (header row, column alignment).
    TableTools,
    PlainTextPaste,
    PageFiles.configure(files),
    ...(links ? [PageLinks.configure(links), PageLinkPicker] : []),
    // The empty-body hint (display only).
    Placeholder.configure({ placeholder: t.editor.bodyPlaceholder }),
  ]
}
