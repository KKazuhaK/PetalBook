import { describe, expect, it } from 'vitest'
import codes from './errorCodes.json'
import { apiErrorsEn } from './apiErrors/en'
import { apiErrorsZhCN } from './apiErrors/zh-CN'
import type { ErrorParams } from './apiErrorsTypes'

// errorCodes.json is written by a Go test (internal/api/errcodes_test.go) from the codes in the server's source, so a
// code added there fails this test until it has a text in both languages.
describe('the texts of the server error codes', () => {
  const params: ErrorParams = {
    limit: '10 MB',
    used: '3',
    size: '12 MB',
    name: 'a.png',
    provider: 'GitHub',
    permission: 'contents',
    header: 'X-Key',
    field: 'text',
    quota: 'spaces',
    spaces: '2',
    accounts: '4',
    wait: '30 s',
  }
  for (const [name, table] of [
    ['zh-CN', apiErrorsZhCN],
    ['en', apiErrorsEn],
  ] as const) {
    it(`${name} has a text for every code the server sends`, () => {
      const missing = codes.filter((c) => !Object.hasOwn(table, c))
      expect(missing).toEqual([])
    })
    it(`${name} texts are not empty, with or without what they are about`, () => {
      for (const [code, text] of Object.entries(table)) {
        for (const p of [params, {}]) {
          const s = typeof text === 'function' ? text(p) : text
          expect(s.trim(), code).not.toBe('')
          expect(s, code).not.toContain('undefined')
        }
      }
    })
  }
  it('has no text for a code the server does not send, except the few client-side ones', () => {
    const extra = Object.keys(apiErrorsEn).filter((c) => !codes.includes(c))
    expect(extra).toEqual(['capability_off'])
    expect(Object.keys(apiErrorsZhCN).filter((c) => !codes.includes(c))).toEqual(extra)
  })
  it('says the limit and what a file was in the file_too_large text', () => {
    const en = apiErrorsEn.file_too_large
    expect(typeof en === 'function' && en(params)).toContain('10 MB')
    const zh = apiErrorsZhCN.file_too_large
    expect(typeof zh === 'function' && zh(params)).toContain('a.png')
  })
})
