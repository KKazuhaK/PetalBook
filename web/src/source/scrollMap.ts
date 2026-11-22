// Which line of the Markdown body each top-level block of the editor starts
// on, so the source view and the editor can be scrolled together.
//
// The editor's blocks are not quite the Markdown ones (see fromMdast.ts): a
// list that mixes tasks and plain items becomes several lists, and a link
// definition shows nothing. This follows the same rules, so the n-th line here
// belongs to the n-th child of the editor's document.

import { parser } from '@/markdown/processors'

interface Node {
  type: string
  ordered?: boolean
  checked?: boolean | null
  children?: Node[]
  position?: { start: { line: number }; end: { line: number } }
}

export function blockLines(markdown: string): number[] {
  const tree = parser.parse(markdown) as unknown as Node
  const out: number[] = []
  for (const node of tree.children ?? []) {
    const line = node.position?.start.line ?? 1
    if (node.type === 'definition') continue
    if (node.type !== 'list') {
      out.push(line)
      continue
    }
    let kind = ''
    for (const item of node.children ?? []) {
      const k = typeof item.checked === 'boolean' ? 'task' : node.ordered ? 'ordered' : 'bullet'
      if (k !== kind) {
        kind = k
        out.push(item.position?.start.line ?? line)
      }
    }
  }
  return out
}

// A place in the body: block `index`, and how far through it (0..1).
export interface BlockPos {
  index: number
  frac: number
}

// `line` may be fractional; `total` is the number of lines of the body.
export function lineToBlock(lines: number[], line: number, total: number): BlockPos {
  if (lines.length === 0) return { index: 0, frac: 0 }
  let i = 0
  while (i + 1 < lines.length && lines[i + 1] <= line) i++
  const start = lines[i]
  const end = i + 1 < lines.length ? lines[i + 1] : Math.max(total + 1, start + 1)
  return { index: i, frac: Math.min(1, Math.max(0, (line - start) / (end - start))) }
}

export function blockToLine(lines: number[], pos: BlockPos, total: number): number {
  if (lines.length === 0) return 1
  const i = Math.min(Math.max(pos.index, 0), lines.length - 1)
  const start = lines[i]
  const end = i + 1 < lines.length ? lines[i + 1] : Math.max(total + 1, start + 1)
  return start + pos.frac * (end - start)
}
