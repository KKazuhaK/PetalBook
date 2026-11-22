// The syntax highlighter, loaded the first time a page holds a code block with
// a language it can colour. Nobody else pays for it: the language grammars are
// the largest part of the editor's code.

// The part of lowlight's result (a hast tree) that is read here.
export interface HNode {
  type: string
  value?: string
  properties?: { className?: unknown }
  children?: HNode[]
}
export interface Root {
  children: HNode[]
}

export interface Highlighter {
  registered(word: string): boolean
  highlight(word: string, code: string): Root
}

let loaded: Highlighter | null = null
let pending: Promise<Highlighter> | null = null

// Null until `loadHighlighter` has finished.
export const currentHighlighter = () => loaded

export function loadHighlighter(): Promise<Highlighter> {
  pending ??= import('lowlight').then(({ common, createLowlight }) => (loaded = createLowlight(common)))
  return pending
}

export interface Span {
  from: number
  to: number
  className: string
}

// The coloured stretches of a highlighted text, as offsets into that text.
// Nested elements carry the classes of all their parents, so a keyword inside a
// string template has both.
export function spansOf(root: Root): Span[] {
  const out: Span[] = []
  let at = 0
  const walk = (nodes: HNode[], classes: string[]) => {
    for (const node of nodes) {
      if (node.type === 'text') {
        const length = node.value?.length ?? 0
        if (classes.length && length) out.push({ from: at, to: at + length, className: classes.join(' ') })
        at += length
      } else if (node.type === 'element') {
        const own = node.properties?.className
        walk(node.children ?? [], Array.isArray(own) ? [...classes, ...own.map(String)] : classes)
      }
    }
  }
  walk(root.children, [])
  return out
}
