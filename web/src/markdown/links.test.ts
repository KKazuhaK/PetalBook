// The links of a page, as the editor's Markdown pipeline sees them, against the
// cases the server's parser is tested with (internal/links): the two must find
// the same links, with the same text, target path, id and anchor.

import { describe, expect, it } from 'vitest'
import { parser } from './processors'
import fixtures from './__fixtures__/links.json'

type Node = { type: string; url?: string; title?: string | null; value?: string; alt?: string; children?: Node[]; identifier?: string }
type Found = { text: string; path: string; id: string; anchor: string }

const ID = /^[0-9A-HJKMNP-TV-Z]{10}$/

function textOf(n: Node): string {
  if (n.type === 'image') return ''
  if (n.type === 'text' || n.type === 'inlineCode') return n.value ?? ''
  return (n.children ?? []).map(textOf).join('')
}

function resolve(src: string, dest: string): { path: string; anchor: string } | null {
  let anchor = ''
  const h = dest.indexOf('#')
  if (h >= 0) [dest, anchor] = [dest.slice(0, h), dest.slice(h + 1)]
  const q = dest.indexOf('?')
  if (q >= 0) dest = dest.slice(0, q)
  if (!dest || /^[A-Za-z][A-Za-z0-9+.-]*:/.test(dest) || dest.startsWith('//')) return null
  try {
    dest = decodeURIComponent(dest)
  } catch {
    // keep as written
  }
  if (!/\.md$/i.test(dest)) return null
  const parts: string[] = dest.startsWith('/') ? [] : src.split('/').slice(0, -1)
  for (const seg of dest.split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') {
      if (parts.length === 0) return null
      parts.pop()
    } else parts.push(seg)
  }
  return { path: parts.join('/'), anchor }
}

function linksOf(src: string, markdown: string): Found[] {
  const tree = parser.parse(markdown) as unknown as Node
  const defs = new Map<string, Node>()
  const walk = (n: Node, f: (n: Node) => void) => {
    f(n)
    n.children?.forEach((c) => walk(c, f))
  }
  walk(tree, (n) => {
    if (n.type === 'definition' && n.identifier) defs.set(n.identifier.toLowerCase(), n)
  })
  const out: Found[] = []
  walk(tree, (n) => {
    if (n.type !== 'link' && n.type !== 'linkReference') return
    const def = n.type === 'linkReference' ? defs.get((n.identifier ?? '').toLowerCase()) : n
    if (!def?.url) return
    const r = resolve(src, def.url)
    if (!r) return
    const title = def.title ?? ''
    out.push({ text: textOf(n).replace(/\s+/g, ' ').trim(), path: r.path, id: ID.test(title) ? title : '', anchor: r.anchor })
  })
  return out
}

describe('page links, shared cases', () => {
  for (const c of fixtures.cases) {
    it(c.name, () => {
      expect(linksOf(c.src, c.markdown)).toEqual(c.links)
    })
  }
})
