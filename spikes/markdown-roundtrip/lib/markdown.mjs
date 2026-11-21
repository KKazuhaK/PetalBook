// The Markdown layer: which editor features exist, and how an editor document
// maps to and from Markdown text. Runs unchanged in Node and in the browser.
//
//   editor document (ProseMirror JSON)  <->  mdast  <->  Markdown text
//
// This handles a page's body only. The header block of a file (id, title,
// tags, ...) is split off and parsed separately before the body gets here;
// passing a whole file through would read the header as a rule and a heading.
//
// Constructs the editor has no form for are rewritten on load, once:
//   - reference-style links and images become inline ones, and the
//     definitions they pointed to are dropped;
//   - a task item in a numbered list becomes a task item in a bullet list.

import { Node } from '@tiptap/core'
import StarterKit from '@tiptap/starter-kit'
import { TableKit } from '@tiptap/extension-table'
import { TaskList, TaskItem } from '@tiptap/extension-list'
import Image from '@tiptap/extension-image'
import Code from '@tiptap/extension-code'
import { gfmTableToMarkdown } from 'mdast-util-gfm-table'
import { unified } from 'unified'
import remarkParse from 'remark-parse'
import remarkStringify from 'remark-stringify'
import remarkGfm from 'remark-gfm'
import remarkCjkFriendly from 'remark-cjk-friendly'
import remarkCjkFriendlyStrike from 'remark-cjk-friendly-gfm-strikethrough'
import { gfmTaskListItemToMarkdown } from 'mdast-util-gfm-task-list-item'

// --- nodes for Markdown the UI does not edit but must never lose ----------

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

// Everything the editor can hold. Each entry has a Markdown form below.
export const extensions = [
  // Underline has no Markdown form; trailingNode is a UI convenience that
  // would otherwise show up as an empty paragraph in every document.
  StarterKit.configure({ underline: false, trailingNode: false, code: false }),
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

// --- Markdown text <-> mdast ---------------------------------------------

const parser = unified().use(remarkParse).use(remarkGfm, { singleTilde: false }).use(remarkCjkFriendly).use(remarkCjkFriendlyStrike, { singleTilde: false })
const printer = unified()
  .use(remarkGfm, { singleTilde: false, tablePipeAlign: false })
  .use(remarkCjkFriendly)
  .use(remarkCjkFriendlyStrike, { singleTilde: false })
  // The CJK plugins replace handlers that the task-list serializer builds on,
  // which silently drops "[ ]" / "[x]". Registering it again afterwards
  // restores the checkboxes.
  .use(function taskListLast() {
    ;(this.data().toMarkdownExtensions ??= []).push(gfmTaskListItemToMarkdown())
  })
  // Unpadded tables keep diffs to the edited row, but the serializer then
  // writes one-dash delimiter cells. Widen them to the conventional three.
  .use(function tableDelimiters() {
    const base = gfmTableToMarkdown({ tablePipeAlign: false }).handlers.table
    ;(this.data().toMarkdownExtensions ??= []).push({
      handlers: {
        table(node, parent, state, info) {
          const lines = base(node, parent, state, info).split('\n')
          if (lines.length > 1) lines[1] = lines[1].replace(/(:?)-+(:?)/g, '$1---$2')
          return lines.join('\n')
        },
      },
    })
  })
  .use(remarkStringify, { bullet: '-', emphasis: '*', strong: '*', rule: '-', fences: true, listItemIndent: 'one', incrementListMarker: true })

// --- editor document -> mdast --------------------------------------------

const EMPTY_PARAGRAPH = '<br>'
const WRAPPING = { bold: 'strong', italic: 'emphasis', strike: 'delete' }

function inlineToMdast(nodes = []) {
  // Flat runs with mark sets -> nested tree. At each step wrap the longest
  // run that shares one mark.
  const items = nodes.map((n) => ({ node: n, marks: (n.marks ?? []).filter((m) => m.type !== 'code') }))
  return nest(items)
}

function nest(items) {
  const out = []
  let i = 0
  while (i < items.length) {
    const item = items[i]
    if (item.marks.length === 0) {
      out.push(leaf(item.node))
      i++
      continue
    }
    let best = null
    for (const mark of item.marks) {
      let j = i
      while (j < items.length && items[j].marks.some((m) => sameMark(m, mark))) j++
      // Links go outermost when tied so a link never gets split by emphasis.
      if (!best || j > best.end || (j === best.end && mark.type === 'link')) best = { mark, end: j }
    }
    const inner = items.slice(i, best.end).map((it) => ({ node: it.node, marks: it.marks.filter((m) => !sameMark(m, best.mark)) }))
    out.push(...wrap(best.mark, nest(inner)))
    i = best.end
  }
  return out
}

const sameMark = (a, b) => a.type === b.type && JSON.stringify(a.attrs ?? {}) === JSON.stringify(b.attrs ?? {})

function wrap(mark, children) {
  if (mark.type === 'link') return [{ type: 'link', url: mark.attrs.href, title: mark.attrs.title ?? null, children }]
  const type = WRAPPING[mark.type]
  if (!type) return children
  // Markdown cannot open or close emphasis next to whitespace. Whitespace at
  // the edge of a bold/italic run looks the same outside it, so move it out.
  const before = []
  const after = []
  const first = children[0]
  if (first?.type === 'text') {
    const m = first.value.match(/^\s+/)
    if (m) {
      before.push({ type: 'text', value: m[0] })
      first.value = first.value.slice(m[0].length)
    }
  }
  const last = children[children.length - 1]
  if (last?.type === 'text') {
    const m = last.value.match(/\s+$/)
    if (m) {
      after.push({ type: 'text', value: m[0] })
      last.value = last.value.slice(0, -m[0].length)
    }
  }
  const kept = children.filter((c) => c.type !== 'text' || c.value !== '')
  return kept.length ? [...before, { type, children: kept }, ...after] : [...before, ...after]
}

function leaf(node) {
  switch (node.type) {
    case 'text':
      return (node.marks ?? []).some((m) => m.type === 'code') ? { type: 'inlineCode', value: node.text } : { type: 'text', value: node.text }
    case 'hardBreak':
      return { type: 'break' }
    case 'image':
      return { type: 'image', url: node.attrs.src, alt: node.attrs.alt ?? '', title: node.attrs.title ?? null }
    case 'rawInline':
      return { type: 'html', value: node.attrs.value }
    case 'footnoteRef':
      return { type: 'footnoteReference', identifier: node.attrs.id, label: node.attrs.id }
    default:
      throw new Error(`no Markdown form for inline node "${node.type}"`)
  }
}

// A hard break at the very end of a block has no Markdown form and no visible
// effect; drop it.
function trimTrailingBreaks(children) {
  while (children.length && children[children.length - 1].type === 'break') children.pop()
  return children
}

function blocksToMdast(nodes = [], { inItem = false } = {}) {
  const out = []
  for (const [index, node] of nodes.entries()) {
    switch (node.type) {
      case 'paragraph': {
        const children = trimTrailingBreaks(inlineToMdast(node.content))
        if (children.length) out.push({ type: 'paragraph', children })
        // The editor requires a list item to start with a paragraph. When
        // that paragraph is empty and a non-paragraph follows, the item really
        // starts with that block ("- > quote"); alone, it is an empty item.
        // Before another paragraph it is a visible blank line and is kept.
        else if (!(inItem && index === 0 && nodes[1]?.type !== 'paragraph')) out.push({ type: 'html', value: EMPTY_PARAGRAPH })
        break
      }
      case 'heading':
        out.push({ type: 'heading', depth: node.attrs.level, children: trimTrailingBreaks(inlineToMdast(node.content)) })
        break
      case 'bulletList':
      case 'orderedList':
      case 'taskList': {
        const items = (node.content ?? []).map((item) => {
          const children = blocksToMdast(item.content, { inItem: true })
          return {
            type: 'listItem',
            checked: node.type === 'taskList' ? Boolean(item.attrs?.checked) : null,
            spread: children.filter((c) => c.type !== 'list').length > 1,
            children,
          }
        })
        out.push({
          type: 'list',
          ordered: node.type === 'orderedList',
          start: node.type === 'orderedList' ? (node.attrs?.start ?? 1) : null,
          spread: items.some((it) => it.spread),
          children: items,
        })
        break
      }
      case 'blockquote':
        out.push({ type: 'blockquote', children: blocksToMdast(node.content) })
        break
      case 'codeBlock': {
        // The attribute holds the whole info string ("js title=a.js"); Markdown
        // splits it into the language and the rest.
        const [, lang = null, meta = null] = (node.attrs?.language ?? '').match(/^(\S+)(?:\s+(.*))?$/) ?? []
        out.push({ type: 'code', lang, meta: meta || null, value: (node.content ?? []).map((n) => n.text).join('') })
        break
      }
      case 'horizontalRule':
        out.push({ type: 'thematicBreak' })
        break
      case 'table':
        out.push({
          type: 'table',
          align: (node.content?.[0]?.content ?? []).map((cell) => cell.attrs?.align ?? null),
          children: (node.content ?? []).map((row) => ({
            type: 'tableRow',
            children: (row.content ?? []).map((cell) => ({ type: 'tableCell', children: cellToMdast(cell) })),
          })),
        })
        break
      case 'rawBlock':
        out.push({ type: 'html', value: node.attrs.value })
        break
      case 'footnoteDef':
        out.push({ type: 'footnoteDefinition', identifier: node.attrs.id, label: node.attrs.id, children: blocksToMdast(node.content) })
        break
      default:
        throw new Error(`no Markdown form for block node "${node.type}"`)
    }
  }
  return out
}

// A GFM table cell is one line of inline content. Paragraphs inside a cell are
// joined; line breaks become <br>, the only form a table row can carry.
function cellToMdast(cell) {
  const out = []
  ;(cell.content ?? []).forEach((para, i) => {
    if (i > 0) out.push({ type: 'html', value: '<br>' })
    for (const child of trimTrailingBreaks(inlineToMdast(para.content))) {
      out.push(child.type === 'break' ? { type: 'html', value: '<br>' } : child)
    }
  })
  return out
}

// --- mdast -> editor document --------------------------------------------

function inlineFromMdast(nodes = [], marks = [], { inCell = false } = {}) {
  const out = []
  const push = (node) => out.push(marks.length ? { ...node, marks: [...(node.marks ?? []), ...marks] } : node)
  for (const node of nodes) {
    switch (node.type) {
      case 'text':
        if (node.value) push({ type: 'text', text: node.value })
        break
      case 'inlineCode':
        out.push({ type: 'text', text: node.value, marks: [{ type: 'code' }, ...marks] })
        break
      case 'break':
        push({ type: 'hardBreak' })
        break
      case 'strong':
        out.push(...inlineFromMdast(node.children, [...marks, { type: 'bold' }], { inCell }))
        break
      case 'emphasis':
        out.push(...inlineFromMdast(node.children, [...marks, { type: 'italic' }], { inCell }))
        break
      case 'delete':
        out.push(...inlineFromMdast(node.children, [...marks, { type: 'strike' }], { inCell }))
        break
      case 'link':
        out.push(...inlineFromMdast(node.children, [...marks, { type: 'link', attrs: { href: node.url, title: node.title ?? null } }], { inCell }))
        break
      case 'image':
        // Keeps the marks it sits in, so an image inside a link stays linked.
        push({ type: 'image', attrs: { src: node.url, alt: node.alt || null, title: node.title ?? null } })
        break
      case 'linkReference': {
        const def = definitions.get(node.identifier)
        if (def) out.push(...inlineFromMdast(node.children, [...marks, { type: 'link', attrs: { href: def.url, title: def.title ?? null } }], { inCell }))
        else {
          // No such definition: Markdown shows the brackets as written.
          push({ type: 'text', text: '[' })
          out.push(...inlineFromMdast(node.children, marks, { inCell }))
          push({ type: 'text', text: node.referenceType === 'full' ? `][${node.label}]` : node.referenceType === 'collapsed' ? '][]' : ']' })
        }
        break
      }
      case 'imageReference': {
        const def = definitions.get(node.identifier)
        if (def) push({ type: 'image', attrs: { src: def.url, alt: node.alt || null, title: def.title ?? null } })
        else push({ type: 'text', text: `![${node.alt ?? ''}]${node.referenceType === 'full' ? `[${node.label}]` : node.referenceType === 'collapsed' ? '[]' : ''}` })
        break
      }
      case 'html':
        if (inCell && /^<br\s*\/?>$/i.test(node.value)) out.push({ type: 'hardBreak' })
        else out.push({ type: 'rawInline', attrs: { value: node.value } })
        break
      case 'footnoteReference':
        out.push({ type: 'footnoteRef', attrs: { id: node.identifier } })
        break
      default:
        throw new Error(`unhandled inline mdast node "${node.type}"`)
    }
  }
  return out
}

// Link definitions of the document being loaded, by normalised identifier.
let definitions = new Map()

function collectDefinitions(node, into = new Map()) {
  if (node.type === 'definition' && !into.has(node.identifier)) into.set(node.identifier, node)
  for (const child of node.children ?? []) collectDefinitions(child, into)
  return into
}

const withContent = (node, content) => (content.length ? { ...node, content } : node)

function blocksFromMdast(nodes = []) {
  const out = []
  for (const node of nodes) {
    switch (node.type) {
      case 'paragraph':
        out.push(withContent({ type: 'paragraph' }, inlineFromMdast(node.children)))
        break
      case 'heading':
        out.push(withContent({ type: 'heading', attrs: { level: node.depth } }, inlineFromMdast(node.children)))
        break
      case 'list': {
        // The editor has separate task and bullet lists; a Markdown list may
        // mix both, so split it into runs.
        let run = null
        for (const item of node.children) {
          const isTask = typeof item.checked === 'boolean'
          const kind = isTask ? 'taskList' : node.ordered ? 'orderedList' : 'bulletList'
          if (!run || run.type !== kind) {
            run = { type: kind, content: [] }
            if (kind === 'orderedList') run.attrs = { start: node.start ?? 1 }
            out.push(run)
          }
          let content = blocksFromMdast(item.children)
          if (!content.length || content[0].type !== 'paragraph') content = [{ type: 'paragraph' }, ...content]
          run.content.push(isTask ? { type: 'taskItem', attrs: { checked: item.checked }, content } : { type: 'listItem', content })
        }
        break
      }
      case 'blockquote':
        out.push({ type: 'blockquote', content: blocksFromMdast(node.children) })
        break
      case 'code':
        out.push(withContent({ type: 'codeBlock', attrs: { language: [node.lang, node.meta].filter(Boolean).join(' ') || null } }, node.value ? [{ type: 'text', text: node.value }] : []))
        break
      case 'thematicBreak':
        out.push({ type: 'horizontalRule' })
        break
      case 'table':
        out.push({
          type: 'table',
          content: node.children.map((row, r) => ({
            type: 'tableRow',
            content: row.children.map((cell, c) => ({
              type: r === 0 ? 'tableHeader' : 'tableCell',
              ...(node.align?.[c] ? { attrs: { align: node.align[c] } } : {}),
              content: [withContent({ type: 'paragraph' }, inlineFromMdast(cell.children, [], { inCell: true }))],
            })),
          })),
        })
        break
      case 'html':
        if (node.value === EMPTY_PARAGRAPH) out.push({ type: 'paragraph' })
        else out.push({ type: 'rawBlock', attrs: { value: node.value } })
        break
      case 'footnoteDefinition':
        out.push({ type: 'footnoteDef', attrs: { id: node.identifier }, content: blocksFromMdast(node.children) })
        break
      case 'definition':
        // Its links were made inline above; on its own it shows nothing.
        break
      default:
        throw new Error(`unhandled block mdast node "${node.type}"`)
    }
  }
  return out
}

// --- public surface ---------------------------------------------------------

// Things an editor document can hold that Markdown cannot, and that nobody can
// see: spaces and line breaks at the very end of a paragraph, heading or table
// cell, and empty paragraphs at the end of the document (where the cursor
// rests). It also applies two structural rules of Markdown: a table has exactly
// one header row, and a heading is a single line (a line break in it becomes a
// space). They are dropped on save, so compare documents after this step.
// Full-width spaces are left alone: they are visible indentation in Chinese.
export function normalizeDoc(json) {
  const visit = (node) => {
    if (node.type === 'codeBlock' || !node.content) return node
    let content = node.content.map(visit)
    // A Markdown heading is a single line.
    if (node.type === 'heading') {
      content = content.map((child) => (child.type === 'hardBreak' ? { type: 'text', text: ' ', ...(child.marks ? { marks: child.marks } : {}) } : child))
    }
    if (node.type === 'paragraph' || node.type === 'heading') {
      content = [...content]
      for (;;) {
        const last = content[content.length - 1]
        if (!last) break
        if (last.type === 'hardBreak') content.pop()
        else if (last.type === 'text' && !(last.marks ?? []).some((m) => m.type === 'code') && /[ \t]$/.test(last.text)) {
          const text = last.text.replace(/[ \t]+$/, '')
          if (text) {
            content[content.length - 1] = { ...last, text }
            break
          }
          content.pop()
        } else break
      }
    }
    // A Markdown table always has exactly one header row, the first. If the
    // header row was deleted in the editor, the next row takes its place.
    if (node.type === 'table') {
      content = content.map((row, r) => ({
        ...row,
        content: (row.content ?? []).map((cell) => ({ ...cell, type: r === 0 ? 'tableHeader' : 'tableCell' })),
      }))
    }
    // A table cell is trimmed on both sides by Markdown itself.
    if ((node.type === 'tableCell' || node.type === 'tableHeader') && content.length) {
      content = content.map((para) => {
        const first = para.content?.[0]
        if (first?.type !== 'text' || (first.marks ?? []).some((m) => m.type === 'code') || !/^[ \t]/.test(first.text)) return para
        const text = first.text.replace(/^[ \t]+/, '')
        const inner = text ? [{ ...first, text }, ...para.content.slice(1)] : para.content.slice(1)
        const { content: _c, ...p } = para
        return inner.length ? { ...p, content: inner } : p
      })
    }
    const { content: _dropped, ...rest } = node
    return content.length ? { ...rest, content } : rest
  }
  const doc = visit(json)
  const blocks = [...(doc.content ?? [])]
  while (blocks.length && blocks[blocks.length - 1].type === 'paragraph' && !blocks[blocks.length - 1].content?.length) blocks.pop()
  return { ...doc, content: blocks }
}

export function docToMarkdown(json) {
  const blocks = normalizeDoc(json).content ?? []
  const tree = { type: 'root', children: blocksToMdast(blocks) }
  return printer.stringify(tree)
}

export function markdownToDoc(markdown) {
  const tree = parser.runSync(parser.parse(markdown))
  definitions = collectDefinitions(tree)
  const content = blocksFromMdast(tree.children)
  return { type: 'doc', content: content.length ? content : [{ type: 'paragraph' }] }
}
