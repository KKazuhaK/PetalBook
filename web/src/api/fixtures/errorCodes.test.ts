// A test that makes the fixture refuse with a code the server never sends tests a page against an answer that cannot
// happen. Every `fx.fail(..., { code })` in the web sources must be a code in the list written from the server's source.

import { describe, expect, it } from 'vitest'
import codes from '../../i18n/errorCodes.json'

const files = import.meta.glob(['/src/**/*.{ts,tsx}', '!/src/api/fixtures/errorCodes.test.ts'], {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

describe('the codes the tests make the fixture refuse with', () => {
  it('are all codes the server sends', () => {
    const used: [string, string][] = []
    for (const [file, text] of Object.entries(files))
      for (const m of text.matchAll(/\bfail\((?:(?!\bfail\()[\s\S]){0,300}?\bcode:\s*'([a-z0-9_]+)'/g))
        used.push([m[1], file])
    expect(used.length).toBeGreaterThan(20)
    expect(used.filter(([code]) => !(codes as string[]).includes(code))).toEqual([])
  })
})
