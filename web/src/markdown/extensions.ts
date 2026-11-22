// The editor features that exist. Each one has a Markdown form in toMdast.ts
// and fromMdast.ts; adding one here without a form there loses content.

import { Node } from '@tiptap/core'
import StarterKit from '@tiptap/starter-kit'
import { TableKit } from '@tiptap/extension-table'
import { TaskList, TaskItem } from '@tiptap/extension-list'
import { MappedImage, type ResolveSrc } from './imageView'
import { CjkBold, CjkCode, CjkItalic, CjkStrike } from './inputRules'

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
  // Maps the address of an image, as written in the page, to what the browser
  // loads. Only drawing is affected; the document keeps the address.
  resolveImageSrc?: ResolveSrc
}

export function createExtensions({ linkOpensOnClick, resolveImageSrc }: ExtensionOptions = {}) {
  return [
    // Underline has no Markdown form; trailingNode is a UI convenience that
    // would otherwise show up as an empty paragraph in every document.
    StarterKit.configure({
      underline: false,
      trailingNode: false,
      // Replaced below by our own, whose typing shortcuts also work after a
      // Chinese character (see inputRules.ts).
      bold: false,
      italic: false,
      strike: false,
      code: false,
      ...(linkOpensOnClick === undefined ? {} : { link: { openOnClick: linkOpensOnClick } }),
    }),
    CjkBold,
    CjkItalic,
    CjkStrike,
    CjkCode,
    TableKit,
    TaskList,
    TaskItem.configure({ nested: true }),
    MappedImage.configure({ inline: true, resolveSrc: resolveImageSrc }),
    RawBlock,
    RawInline,
    FootnoteRef,
    FootnoteDef,
  ]
}

// Exactly the set the round-trip tests run against.
export const extensions = createExtensions()
