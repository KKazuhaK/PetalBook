// The links between the pages of a mock space (docs/api.md, "Links between pages"):
// read out of the bodies when asked, not kept, which is fine for a few pages. The
// reading is a simple one (the server's is goldmark's, and the shared cases in
// markdown/__fixtures__/links.json are asserted on the editor's own parser).

import { isPageId, resolveHref } from '../../lib/pageLinks.ts'

export interface FoundLink {
  text: string
  path: string
  id: string
  anchor: string
  // Where the paragraph that holds it starts and ends in the body.
  from: number
  to: number
}

const LINK = /(!?)\[((?:[^[\]\\]|\\.)*)\]\(\s*(<[^>\n]*>|(?:[^\s()\\]|\\.|\([^\s()]*\))*)(?:\s+(?:"([^"]*)"|'([^']*)'))?\s*\)/g

// Code is blanked out (same length) so its brackets are not links.
function withoutCode(body: string): string {
  const blank = (m: string) => m.replace(/[^\n]/g, ' ')
  return body.replace(/^(```|~~~)[^\n]*\n[\s\S]*?(?:\n\1[^\n]*(?=\n|$)|$)/gm, blank).replace(/(`+)[^`\n]+?\1/g, blank)
}

export function linksOf(srcFolder: string, body: string): FoundLink[] {
  const text = withoutCode(body)
  const out: FoundLink[] = []
  for (const m of text.matchAll(LINK)) {
    if (m[1]) continue
    let dest = m[3]
    if (dest.startsWith('<')) dest = dest.slice(1, -1)
    else dest = dest.replace(/\\(.)/g, '$1')
    const r = resolveHref(srcFolder, dest)
    if (!r) continue
    const at = m.index!
    const from = text.lastIndexOf('\n\n', at) + 1
    const end = text.indexOf('\n\n', at)
    const title = m[4] ?? m[5] ?? ''
    out.push({ text: m[2].replace(/\\(.)/g, '$1').replace(/\s+/g, ' ').trim(), path: r.path, id: isPageId(title) ? title : '', anchor: r.anchor, from, to: end < 0 ? body.length : end })
  }
  return out
}

// The paragraph of a link as plain text, about 240 characters around the link.
export function excerptAround(body: string, link: FoundLink): string {
  const plain = body
    .slice(link.from, link.to)
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/[*_`~#>]/g, '')
    .replace(/\s+/g, ' ')
    .trim()
  const chars = Array.from(plain)
  if (chars.length <= 240) return plain
  const at = Math.max(0, Array.from(plain.slice(0, plain.indexOf(link.text))).length - 100)
  return Array.from(plain).slice(at, at + 240).join('')
}
