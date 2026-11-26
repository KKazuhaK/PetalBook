import { describe, expect, it } from 'vitest'
import { sha256Hex } from './sha256'
import { storedName } from './files'

describe("the mock server's side of uploads", () => {
  it('sha256 matches the published test vectors', () => {
    const hex = (s: string) => sha256Hex(new TextEncoder().encode(s))
    expect(hex('')).toBe('e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855')
    expect(hex('abc')).toBe('ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad')
    expect(hex('abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq')).toBe(
      '248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1',
    )
    expect(hex('a'.repeat(1_000_000))).toBe('cdc76e5c9914fb9281a1c7e284d73e67f1809a48a497200e046d39ccc7112cd0')
  })
  it('stored names', () => {
    expect(storedName('Photo.JPG', 'abcd1234')).toBe('Photo-abcd1234.jpg')
    expect(storedName('a/b\\c.txt', 'abcd1234')).toBe('c-abcd1234.txt')
    expect(storedName('a:b*c?.png', 'abcd1234')).toBe('a_b_c_-abcd1234.png')
    expect(storedName('no extension', 'abcd1234')).toBe('no extension-abcd1234')
    expect(storedName('.gitignore', 'abcd1234')).toBe('gitignore-abcd1234')
    expect(storedName('', 'abcd1234')).toBe('file-abcd1234')
    expect(Array.from(storedName('长'.repeat(200) + '.png', 'abcd1234')).length).toBeLessThan(110)
  })
})
