// editor document (ProseMirror JSON) -> mdast.

import type { DocJSON, MdNode } from './types'

// An empty paragraph has no Markdown form of its own; a line holding only this
// HTML tag stands for it, and fromMdast.ts turns it back.
export const EMPTY_PARAGRAPH = '<br>'
const WRAPPING: Record<string, string> = { bold: 'strong', italic: 'emphasis', strike: 'delete' }

type Mark = NonNullable<DocJSON['marks']>[number]
interface Item {
  node: DocJSON
  marks: Mark[]
}

const sameMark = (a: Mark, b: Mark) =>
  a.type === b.type && JSON.stringify(a.attrs ?? {}) === JSON.stringify(b.attrs ?? {})

function inlineToMdast(nodes: DocJSON[] = []): MdNode[] {
  // Flat runs with mark sets -> nested tree. At each step wrap the longest
  // run that shares one mark.
  const items = nodes.map((n) => ({ node: n, marks: (n.marks ?? []).filter((m) => m.type !== 'code') }))
  return nest(items)
}

function nest(items: Item[]): MdNode[] {
  const out: MdNode[] = []
  let i = 0
  while (i < items.length) {
    const item = items[i]
    if (item.marks.length === 0) {
      out.push(leaf(item.node))
      i++
      continue
    }
    let best: { mark: Mark; end: number } | null = null
    for (const mark of item.marks) {
      let j = i
      while (j < items.length && items[j].marks.some((m) => sameMark(m, mark))) j++
      // Links go outermost when tied so a link never gets split by emphasis.
      if (!best || j > best.end || (j === best.end && mark.type === 'link')) best = { mark, end: j }
    }
    const chosen = best!
    const inner = items
      .slice(i, chosen.end)
      .map((it) => ({ node: it.node, marks: it.marks.filter((m) => !sameMark(m, chosen.mark)) }))
    out.push(...wrap(chosen.mark, nest(inner)))
    i = chosen.end
  }
  return out
}

function wrap(mark: Mark, children: MdNode[]): MdNode[] {
  if (mark.type === 'link') return [{ type: 'link', url: mark.attrs!.href, title: mark.attrs!.title ?? null, children }]
  const type = WRAPPING[mark.type]
  if (!type) return children
  // Markdown cannot open or close emphasis next to whitespace. Whitespace at
  // the edge of a bold/italic run looks the same outside it, so move it out.
  const before: MdNode[] = []
  const after: MdNode[] = []
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

function leaf(node: DocJSON): MdNode {
  switch (node.type) {
    case 'text':
      return (node.marks ?? []).some((m) => m.type === 'code')
        ? { type: 'inlineCode', value: node.text }
        : { type: 'text', value: node.text }
    case 'hardBreak':
      return { type: 'break' }
    case 'image':
      return { type: 'image', url: node.attrs!.src, alt: node.attrs!.alt ?? '', title: node.attrs!.title ?? null }
    case 'rawInline':
      return { type: 'html', value: node.attrs!.value }
    case 'footnoteRef':
      return { type: 'footnoteReference', identifier: node.attrs!.id, label: node.attrs!.id }
    default:
      throw new Error(`no Markdown form for inline node "${node.type}"`)
  }
}

// A hard break at the very end of a block has no Markdown form and no visible
// effect; drop it.
function trimTrailingBreaks(children: MdNode[]): MdNode[] {
  while (children.length && children[children.length - 1].type === 'break') children.pop()
  return children
}

export function blocksToMdast(nodes: DocJSON[] = [], { inItem = false } = {}): MdNode[] {
  const out: MdNode[] = []
  for (const [index, node] of nodes.entries()) {
    switch (node.type) {
      case 'paragraph': {
        const children = trimTrailingBreaks(inlineToMdast(node.content))
        if (children.length) out.push({ type: 'paragraph', children })
        // The editor requires a list item to start with a paragraph. When
        // that paragraph is empty and a non-paragraph follows, the item really
        // starts with that block ("- > quote"); alone, it is an empty item.
        // Before another paragraph it is a visible blank line and is kept.
        else if (!(inItem && index === 0 && nodes[1]?.type !== 'paragraph'))
          out.push({ type: 'html', value: EMPTY_PARAGRAPH })
        break
      }
      case 'heading':
        out.push({
          type: 'heading',
          depth: node.attrs!.level,
          children: trimTrailingBreaks(inlineToMdast(node.content)),
        })
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
        out.push({ type: 'html', value: node.attrs!.value })
        break
      case 'footnoteDef':
        out.push({
          type: 'footnoteDefinition',
          identifier: node.attrs!.id,
          label: node.attrs!.id,
          children: blocksToMdast(node.content),
        })
        break
      default:
        throw new Error(`no Markdown form for block node "${node.type}"`)
    }
  }
  return out
}

// A GFM table cell is one line of inline content. Paragraphs inside a cell are
// joined; line breaks become <br>, the only form a table row can carry.
function cellToMdast(cell: DocJSON): MdNode[] {
  const out: MdNode[] = []
  ;(cell.content ?? []).forEach((para, i) => {
    if (i > 0) out.push({ type: 'html', value: '<br>' })
    for (const child of trimTrailingBreaks(inlineToMdast(para.content))) {
      out.push(child.type === 'break' ? { type: 'html', value: '<br>' } : child)
    }
  })
  return out
}
