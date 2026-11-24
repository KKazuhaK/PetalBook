// mdast -> editor document (ProseMirror JSON).
//
// Constructs the editor has no form for are rewritten on load, once:
//   - reference-style links and images become inline ones, and the
//     definitions they pointed to are dropped;
//   - a task item in a numbered list becomes a task item in a bullet list.

import type { DocJSON, MdNode } from './types'
import { EMPTY_PARAGRAPH } from './toMdast'

// Link definitions of the document being loaded, by normalised identifier.
type Definitions = Map<string, MdNode>
type Mark = NonNullable<DocJSON['marks']>[number]

export function collectDefinitions(node: MdNode, into: Definitions = new Map()): Definitions {
  if (node.type === 'definition' && !into.has(node.identifier)) into.set(node.identifier, node)
  for (const child of node.children ?? []) collectDefinitions(child, into)
  return into
}

function inlineFromMdast(
  nodes: MdNode[] = [],
  defs: Definitions,
  marks: Mark[] = [],
  { inCell = false } = {},
): DocJSON[] {
  const out: DocJSON[] = []
  const push = (node: DocJSON) => out.push(marks.length ? { ...node, marks: [...(node.marks ?? []), ...marks] } : node)
  const nested = (children: MdNode[] | undefined, extra: Mark[]) =>
    out.push(...inlineFromMdast(children, defs, [...marks, ...extra], { inCell }))
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
        nested(node.children, [{ type: 'bold' }])
        break
      case 'emphasis':
        nested(node.children, [{ type: 'italic' }])
        break
      case 'delete':
        nested(node.children, [{ type: 'strike' }])
        break
      case 'link':
        nested(node.children, [{ type: 'link', attrs: { href: node.url, title: node.title ?? null } }])
        break
      case 'image':
        // Keeps the marks it sits in, so an image inside a link stays linked.
        push({ type: 'image', attrs: { src: node.url, alt: node.alt || null, title: node.title ?? null } })
        break
      case 'linkReference': {
        const def = defs.get(node.identifier)
        if (def) nested(node.children, [{ type: 'link', attrs: { href: def.url, title: def.title ?? null } }])
        else {
          // No such definition: Markdown shows the brackets as written.
          push({ type: 'text', text: '[' })
          nested(node.children, [])
          push({
            type: 'text',
            text: node.referenceType === 'full' ? `][${node.label}]` : node.referenceType === 'collapsed' ? '][]' : ']',
          })
        }
        break
      }
      case 'imageReference': {
        const def = defs.get(node.identifier)
        if (def) push({ type: 'image', attrs: { src: def.url, alt: node.alt || null, title: def.title ?? null } })
        else
          push({
            type: 'text',
            text: `![${node.alt ?? ''}]${node.referenceType === 'full' ? `[${node.label}]` : node.referenceType === 'collapsed' ? '[]' : ''}`,
          })
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

const withContent = (node: DocJSON, content: DocJSON[]): DocJSON => (content.length ? { ...node, content } : node)

export function blocksFromMdast(nodes: MdNode[] = [], defs: Definitions): DocJSON[] {
  const out: DocJSON[] = []
  for (const node of nodes) {
    switch (node.type) {
      case 'paragraph':
        out.push(withContent({ type: 'paragraph' }, inlineFromMdast(node.children, defs)))
        break
      case 'heading':
        out.push(withContent({ type: 'heading', attrs: { level: node.depth } }, inlineFromMdast(node.children, defs)))
        break
      case 'list': {
        // The editor has separate task and bullet lists; a Markdown list may
        // mix both, so split it into runs.
        let run: DocJSON | null = null
        for (const item of node.children!) {
          const isTask = typeof item.checked === 'boolean'
          const kind = isTask ? 'taskList' : node.ordered ? 'orderedList' : 'bulletList'
          if (!run || run.type !== kind) {
            run = { type: kind, content: [] }
            if (kind === 'orderedList') run.attrs = { start: node.start ?? 1 }
            out.push(run)
          }
          let content = blocksFromMdast(item.children, defs)
          if (!content.length || content[0].type !== 'paragraph') content = [{ type: 'paragraph' }, ...content]
          run.content!.push(
            isTask ? { type: 'taskItem', attrs: { checked: item.checked }, content } : { type: 'listItem', content },
          )
        }
        break
      }
      case 'blockquote':
        out.push({ type: 'blockquote', content: blocksFromMdast(node.children, defs) })
        break
      case 'code':
        out.push(
          withContent(
            { type: 'codeBlock', attrs: { language: [node.lang, node.meta].filter(Boolean).join(' ') || null } },
            node.value ? [{ type: 'text', text: node.value }] : [],
          ),
        )
        break
      case 'thematicBreak':
        out.push({ type: 'horizontalRule' })
        break
      case 'table': {
        // Markdown lets rows differ in length; the editor's table is a grid.
        // Every row is filled up at its end to the longest one here, so that no
        // cell changes column when the table is first edited (the table
        // extension would otherwise pad some rows at their start), and a cell
        // beyond the header's width is shown instead of silently dropped.
        const width = Math.max(...node.children!.map((row) => row.children!.length))
        out.push({
          type: 'table',
          content: node.children!.map((row, r) => ({
            type: 'tableRow',
            content: Array.from({ length: width }, (_, c) => {
              const cell = row.children![c]
              return {
                type: r === 0 ? 'tableHeader' : 'tableCell',
                ...(node.align?.[c] ? { attrs: { align: node.align[c] } } : {}),
                content: [
                  withContent(
                    { type: 'paragraph' },
                    cell ? inlineFromMdast(cell.children, defs, [], { inCell: true }) : [],
                  ),
                ],
              }
            }),
          })),
        })
        break
      }
      case 'html':
        if (node.value === EMPTY_PARAGRAPH) out.push({ type: 'paragraph' })
        else out.push({ type: 'rawBlock', attrs: { value: node.value } })
        break
      case 'footnoteDefinition':
        out.push({ type: 'footnoteDef', attrs: { id: node.identifier }, content: blocksFromMdast(node.children, defs) })
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
