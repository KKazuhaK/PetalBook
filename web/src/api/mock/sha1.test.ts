import { describe, expect, it } from 'vitest'
import { gitBlobId } from './sha1'

describe('gitBlobId', () => {
  it('matches `git hash-object`', () => {
    expect(gitBlobId('')).toBe('e69de29bb2d1d6434b8b29ae775ad8c2e48c5391')
    expect(gitBlobId('hello\n')).toBe('ce013625030ba8dba906f756967f9e9ca394464a')
  })
})
