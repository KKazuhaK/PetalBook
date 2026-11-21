// The editor features that exist. Each one has a Markdown form in toMdast.ts
// and fromMdast.ts; adding one here without a form there loses content.

import { Node } from '@tiptap/core'
import StarterKit from '@tiptap/starter-kit'
import { TableKit } from '@tiptap/extension-table'
import { TaskList, TaskItem } from '@tiptap/extension-list'
import Image from '@tiptap/extension-image'
import Code from '@tiptap/extension-code'

// Raw HTML is kept verbatim and shown as an opaque chip/block.
const RawBlock = Node.create({
  name: 'rawBlock',
  group: 'block',
  atom: true,
  addAttributes: () => ({ value: { default: '' } }),
  parseHTML: () => [{ tag: 'pre[data-raw-block]' }],
  renderHTML: ({ node }) => ['pre', { 'data-raw-block': '' }, node.attrs.value],
})
const RawInline = Node.create({
  name: 'rawInline',
  group: 'inline',
  inline: true,
  atom: true,
  addAttributes: () => ({ value: { default: '' } }),
  parseHTML: () => [{ tag: 'code[data-raw-inline]' }],
  renderHTML: ({ node }) => ['code', { 'data-raw-inline': '' }, node.attrs.value],
})
const FootnoteRef = Node.create({
  name: 'footnoteRef',
  group: 'inline',
  inline: true,
  atom: true,
  addAttributes: () => ({ id: { default: '' } }),
  parseHTML: () => [{ tag: 'sup[data-footnote-ref]' }],
  renderHTML: ({ node }) => ['sup', { 'data-footnote-ref': node.attrs.id }, node.attrs.id],
})
const FootnoteDef = Node.create({
  name: 'footnoteDef',
  group: 'block',
  content: 'block+',
  defining: true,
  addAttributes: () => ({ id: { default: '' } }),
  parseHTML: () => [{ tag: 'aside[data-footnote-def]' }],
  renderHTML: ({ node }) => ['aside', { 'data-footnote-def': node.attrs.id }, 0],
})

export interface ExtensionOptions {
  // The page UI edits links in a popover, so a click on a link must not
  // navigate away. The Markdown tests keep the library default.
  linkOpensOnClick?: boolean
}

export function createExtensions({ linkOpensOnClick }: ExtensionOptions = {}) {
  return [
    // Underline has no Markdown form; trailingNode is a UI convenience that
    // would otherwise show up as an empty paragraph in every document.
    StarterKit.configure({
      underline: false,
      trailingNode: false,
      code: false,
      ...(linkOpensOnClick === undefined ? {} : { link: { openOnClick: linkOpensOnClick } }),
    }),
    // By default inline code excludes every other mark, which would split a
    // link or bold run that contains code. Markdown allows the combination.
    Code.extend({ excludes: '' }),
    TableKit,
    TaskList,
    TaskItem.configure({ nested: true }),
    Image.configure({ inline: true }),
    RawBlock,
    RawInline,
    FootnoteRef,
    FootnoteDef,
  ]
}

// Exactly the set the round-trip tests run against.
export const extensions = createExtensions()
