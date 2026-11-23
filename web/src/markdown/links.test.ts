// The links of a page, as the editor's Markdown pipeline sees them, against the
// cases the server's parser is tested with (internal/links): the two must find
// the same links, with the same text, target path, id and anchor.

import { describe, expect, it } from 'vitest'
import fixtures from './__fixtures__/links.json'
import { linksOf } from './linkCases'

describe('page links, shared cases', () => {
  for (const c of fixtures.cases) {
    it(c.name, () => {
      expect(linksOf(c.src, c.markdown)).toEqual(c.links)
    })
  }
})
