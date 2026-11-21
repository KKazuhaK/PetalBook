// The Markdown layer: which editor features exist, and how an editor document
// maps to and from Markdown text.
//
//   editor document (ProseMirror JSON)  <->  mdast  <->  Markdown text
//
// This handles a page's body only. The header block of a file (id, title,
// tags, ...) is split off by the server and arrives as `meta`; passing a whole
// file through would read the header as a rule and a heading.

import { parser, printer } from './processors'
import { blocksToMdast } from './toMdast'
import { blocksFromMdast, collectDefinitions } from './fromMdast'
import { normalizeDoc } from './normalize'
import type { DocJSON, MdNode } from './types'

export { extensions, createExtensions } from './extensions'
export { normalizeDoc } from './normalize'
export type { DocJSON } from './types'

export function docToMarkdown(json: DocJSON): string {
  const blocks = normalizeDoc(json).content ?? []
  const tree: MdNode = { type: 'root', children: blocksToMdast(blocks) }
  return printer.stringify(tree as any)
}

export function markdownToDoc(markdown: string): DocJSON {
  const tree = parser.runSync(parser.parse(markdown)) as MdNode
  const content = blocksFromMdast(tree.children, collectDefinitions(tree))
  return { type: 'doc', content: content.length ? content : [{ type: 'paragraph' }] }
}
