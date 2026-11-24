// Pasting plain text.
//
// The clipboard has text/plain and no text/html (or Shift is held: "paste as
// plain text"). ProseMirror's default turns every line into a paragraph, which
// breaks up poems and copy that is broken into short lines. Here:
//
//   - one line break stays a line break inside the paragraph (hardBreak);
//   - a blank line (also one with only spaces) starts a new paragraph;
//   - in a code block the text stays literal (ProseMirror does that itself,
//     before this code is asked);
//   - in a heading the lines are joined with a space: a heading is one line in
//     Markdown, so a break there would turn into a space on the next save anyway;
//   - text that looks like Markdown (see src/markdown/detect.ts) is parsed with
//     the Markdown layer and pasted as formatted content, with the same rule for
//     the line breaks inside its ordinary paragraphs. Shift+paste never does this,
//     and neither does a paste into a table cell (a cell holds one line of inline
//     content; blocks have nowhere to go).
//
// Pasted HTML is not touched.

import { Extension } from '@tiptap/core'
import { Fragment, Slice, type Node as PMNode, type ResolvedPos, type Schema, type Mark } from '@tiptap/pm/model'
import { Plugin, PluginKey } from '@tiptap/pm/state'
import { detectMarkdown } from '@/markdown/detect'
import { markdownToDoc, type DocJSON } from '@/markdown'

// Paragraphs of lines. Blank (or whitespace-only) lines separate paragraphs, and
// any number of them is one separator.
export function splitPlainText(text: string): string[][] {
  const paragraphs: string[][] = []
  let current: string[] = []
  for (const line of text.replace(/\r\n?/g, '\n').split('\n')) {
    if (line.trim() === '') {
      if (current.length) paragraphs.push(current)
      current = []
    } else current.push(line)
  }
  if (current.length) paragraphs.push(current)
  return paragraphs
}

export type LineJoin = 'break' | 'space'

function inlineNodes(schema: Schema, lines: string[], marks: readonly Mark[], join: LineJoin): PMNode[] {
  if (join === 'space') return [schema.text(lines.map((l) => l.trim()).join(' '), marks)]
  const out: PMNode[] = []
  lines.forEach((line, i) => {
    if (i > 0) out.push(schema.nodes.hardBreak.create(null, null, marks))
    out.push(schema.text(line, marks))
  })
  return out
}

// Null when there is nothing to paste (only blank lines): ProseMirror's own handling then applies.
export function plainTextSlice(
  schema: Schema,
  text: string,
  marks: readonly Mark[] = [],
  join: LineJoin = 'break',
): Slice | null {
  const paragraphs = splitPlainText(text)
  if (!paragraphs.length) return null
  return new Slice(
    Fragment.from(
      paragraphs.map((lines) => schema.nodes.paragraph.create(null, inlineNodes(schema, lines, marks, join))),
    ),
    0,
    0,
  )
}

// Markdown reads a single line break inside a paragraph as a space. Someone who
// pastes lines of text means line breaks, so the breaks the parser kept in text
// become hard breaks (code spans and code blocks keep theirs).
export function hardBreaksInParagraphs(doc: DocJSON): DocJSON {
  const visit = (node: DocJSON): DocJSON => {
    if (!node.content) return node
    if (node.type !== 'paragraph') return { ...node, content: node.content.map(visit) }
    const content: DocJSON[] = []
    for (const child of node.content) {
      const isCode = (child.marks ?? []).some((m) => m.type === 'code')
      if (child.type !== 'text' || isCode || !child.text?.includes('\n')) {
        content.push(child)
        continue
      }
      child.text.split('\n').forEach((part, i) => {
        if (i > 0) content.push({ type: 'hardBreak', ...(child.marks ? { marks: child.marks } : {}) })
        if (part) content.push({ ...child, text: part })
      })
    }
    return { ...node, content }
  }
  return visit(doc)
}

// The blocks of every Markdown paste made here, so the slice can be recognised
// after ProseMirror has opened it up again (see `keepStructure`).
const markdownBlocks = new WeakSet<object>()

// ProseMirror opens a pasted slice as far as it can, so that pasted text joins
// the paragraph it lands in. For one paragraph of Markdown that is right. For a
// heading, a list or several blocks it would merge the first block's text into
// the paragraph at the cursor and lose its structure: those are pasted closed,
// splitting the paragraph if need be.
export function keepStructure(slice: Slice): Slice {
  const first = slice.content.firstChild
  if (!first || !markdownBlocks.has(first)) return slice
  if (slice.content.childCount === 1 && first.type.name === 'paragraph') return slice
  return new Slice(slice.content, 0, 0)
}

// Null when the text does not parse into something this editor can hold.
export function markdownSlice(schema: Schema, text: string): Slice | null {
  try {
    const doc = hardBreaksInParagraphs(markdownToDoc(text))
    const blocks = schema.nodeFromJSON(doc).content
    if (!blocks.childCount) return null
    // Only definitions, say: nothing visible. Paste the text as it is.
    if (blocks.childCount === 1 && blocks.firstChild!.isTextblock && blocks.firstChild!.childCount === 0) return null
    blocks.forEach((node) => markdownBlocks.add(node))
    return new Slice(blocks, 0, 0)
  } catch {
    return null
  }
}

const inTableCell = ($pos: ResolvedPos) => {
  for (let d = $pos.depth; d > 0; d--) {
    const role = $pos.node(d).type.spec.tableRole
    if (role === 'cell' || role === 'header_cell') return true
  }
  return false
}

// What ProseMirror's `clipboardTextParser` asks for: a slice for this text pasted at `$context`.
export function parseClipboardText(schema: Schema, text: string, $context: ResolvedPos, plain: boolean): Slice | null {
  if (!plain && !inTableCell($context) && detectMarkdown(text).markdown) {
    const md = markdownSlice(schema, text)
    if (md) return md
  }
  return plainTextSlice(schema, text, $context.marks(), $context.parent.type.name === 'heading' ? 'space' : 'break')
}

export const PlainTextPaste = Extension.create({
  name: 'plainTextPaste',
  addProseMirrorPlugins() {
    return [
      new Plugin({
        key: new PluginKey('plainTextPaste'),
        props: {
          // ProseMirror falls back to its own parsing when this answers nothing; its types do not say so.
          clipboardTextParser: (text, $context, plain, view) =>
            parseClipboardText(view.state.schema, text, $context, plain) as Slice,
          transformPasted: (slice) => keepStructure(slice),
        },
      }),
    ]
  },
})
