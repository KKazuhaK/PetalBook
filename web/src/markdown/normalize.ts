import type { DocJSON } from './types'

// Things an editor document can hold that Markdown cannot, and that nobody can
// see: spaces and line breaks at the very end of a paragraph, heading or table
// cell, and empty paragraphs at the end of the document (where the cursor
// rests). It also applies two structural rules of Markdown: a table has exactly
// one header row, and a heading is a single line (a line break in it becomes a
// space). They are dropped on save, so compare documents after this step.
// Full-width spaces are left alone: they are visible indentation in Chinese.
export function normalizeDoc(json: DocJSON): DocJSON {
  const visit = (node: DocJSON): DocJSON => {
    if (node.type === 'codeBlock' || !node.content) return node
    let content = node.content.map(visit)
    // A Markdown heading is a single line.
    if (node.type === 'heading') {
      content = content.map((child) =>
        child.type === 'hardBreak'
          ? { type: 'text', text: ' ', ...(child.marks ? { marks: child.marks } : {}) }
          : child,
      )
    }
    if (node.type === 'paragraph' || node.type === 'heading') {
      content = [...content]
      for (;;) {
        const last = content[content.length - 1]
        if (!last) break
        if (last.type === 'hardBreak') content.pop()
        else if (
          last.type === 'text' &&
          !(last.marks ?? []).some((m) => m.type === 'code') &&
          /[ \t]$/.test(last.text ?? '')
        ) {
          const text = (last.text ?? '').replace(/[ \t]+$/, '')
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
        if (
          first?.type !== 'text' ||
          (first.marks ?? []).some((m) => m.type === 'code') ||
          !/^[ \t]/.test(first.text ?? '')
        )
          return para
        const text = (first.text ?? '').replace(/^[ \t]+/, '')
        const inner = text ? [{ ...first, text }, ...para.content!.slice(1)] : para.content!.slice(1)
        // eslint-disable-next-line @typescript-eslint/no-unused-vars
        const { content: _c, ...p } = para
        return inner.length ? { ...p, content: inner } : p
      })
    }
    // eslint-disable-next-line @typescript-eslint/no-unused-vars
    const { content: _dropped, ...rest } = node
    return content.length ? { ...rest, content } : rest
  }
  const doc = visit(json)
  const blocks = [...(doc.content ?? [])]
  while (blocks.length && blocks[blocks.length - 1].type === 'paragraph' && !blocks[blocks.length - 1].content?.length)
    blocks.pop()
  return { ...doc, content: blocks }
}
