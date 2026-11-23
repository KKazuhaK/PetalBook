// A page link survives the editor: the title that carries the target's id and the
// anchor are written back as they were, and the links found in the text are the same
// after a pass through the document. Also the helpers that write a link.

import { describe, expect, it } from 'vitest'
import fixtures from './__fixtures__/links.json'
import { linksOf } from './linkCases'
import { roundtrip } from './testHarness'
import { encodeDest, linkDest, relativePath, resolveHref } from '@/lib/pageLinks'

const norm = (md: string) => md.replace(/\n+$/, '') + '\n'

// A link has no text to hold it in a document: nothing of it is left.
const textless = new Set(['empty text'])

describe('links through the editor, shared cases', () => {
  for (const c of fixtures.cases) {
    if (textless.has(c.name)) {
      it(`${c.name}: a link with no text is not kept`, () => expect(linksOf(c.src, roundtrip(c.markdown))).toEqual([]))
      continue
    }
    // What the editor writes back is the same text for the parser: the same links, in the same order.
    it(`${c.name}: the links are the same after a round trip`, () => {
      const once = roundtrip(c.markdown)
      expect(linksOf(c.src, once)).toEqual(c.links)
      expect(norm(roundtrip(once))).toBe(norm(once))
    })
  }
})

describe('the title with the id', () => {
  const exact = [
    '见[暮雪](../Tang/暮雪.md "01JD3K8Q7Z")。',
    '[b](b.md#小标题 "01JD3K8Q7Z")',
    '[My page](My%20Page.md "01JD3K8Q7Z") and [other](x.md "a title that is not an id")',
    '[100%](100%25.md "01JD3K8Q7Z")',
  ]
  for (const md of exact) {
    it(`comes back as written: ${md}`, () => {
      expect(norm(roundtrip(md))).toBe(norm(md))
    })
  }

  it('keeps the id on a link inside formatting', () => {
    const out = roundtrip('**见[暮雪](Tang/暮雪.md "01JD3K8Q7Z")**')
    expect(linksOf('a.md', out)).toEqual([{ text: '暮雪', path: 'Tang/暮雪.md', id: '01JD3K8Q7Z', anchor: '' }])
  })

  it('keeps a link without an id without adding one', () => {
    expect(norm(roundtrip('[b](b.md)'))).toBe('[b](b.md)\n')
  })
})

describe('writing a link', () => {
  it('goes up and down from the folder of the page', () => {
    expect(relativePath('Notes', 'Tang/暮雪.md')).toBe('../Tang/暮雪.md')
    expect(relativePath('Notes', 'Notes/b.md')).toBe('b.md')
    expect(relativePath('Notes', 'Notes/sub/b.md')).toBe('sub/b.md')
    expect(relativePath('', 'Notes/b.md')).toBe('Notes/b.md')
    expect(relativePath('Notes/sub', 'x.md')).toBe('../../x.md')
    expect(relativePath('A/B', 'A/C/d.md')).toBe('../C/d.md')
    expect(relativePath('A', 'AB/c.md')).toBe('../AB/c.md')
  })

  it('encodes what a destination cannot hold, and leaves Chinese as it is', () => {
    expect(encodeDest('My Page.md')).toBe('My%20Page.md')
    expect(encodeDest('100%.md')).toBe('100%25.md')
    expect(encodeDest('C#.md')).toBe('C%23.md')
    expect(encodeDest('a (1) [2].md')).toBe('a%20%281%29%20%5B2%5D.md')
    expect(encodeDest('唐诗/暮雪.md')).toBe('唐诗/暮雪.md')
  })

  it('names the same file when read back, whatever the file is called', () => {
    const names = ['Tang/暮雪.md', 'My Page.md', 'a/100%.md', 'a/C#.md', 'x/a (1) [2].md', 'q/"quoted" `tick`.md', 'back\\slash.md']
    for (const from of ['', 'Notes', 'a/b/c']) {
      for (const to of names) {
        const dest = linkDest(from, to)
        expect(resolveHref(from, dest)).toEqual({ path: to, anchor: '' })
        // and the Markdown parser reads it the same way, inside a full link
        const md = `[x](${dest} "01JD3K8Q7Z")`
        const found = linksOf(from ? `${from}/p.md` : 'p.md', md)
        expect(found).toEqual([{ text: 'x', path: to, id: '01JD3K8Q7Z', anchor: '' }])
      }
    }
  })

  it('reads a link the way the server does', () => {
    expect(resolveHref('Notes', '../x.md')).toEqual({ path: 'x.md', anchor: '' })
    expect(resolveHref('Notes', '../../x.md')).toBeNull()
    expect(resolveHref('Notes', 'https://a.example/b.md')).toBeNull()
    expect(resolveHref('Notes', '#s')).toBeNull()
    expect(resolveHref('Notes', 'a.png')).toBeNull()
    expect(resolveHref('Notes', 'b.md#小节')).toEqual({ path: 'Notes/b.md', anchor: '小节' })
  })
})
