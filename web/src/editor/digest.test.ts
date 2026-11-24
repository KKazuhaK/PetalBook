import { describe, expect, it } from 'vitest'
import { sha256Hex } from './digest'

describe('sha256Hex', () => {
  it('matches the standard test vectors, including multi-block and non-ASCII input', () => {
    expect(sha256Hex('')).toBe('e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855')
    expect(sha256Hex('abc')).toBe('ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad')
    expect(sha256Hex('abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq')).toBe(
      '248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1',
    )
    expect(sha256Hex('a'.repeat(1000))).toBe('41edece42d63e8d9bf515a9ba6932e1c20cbc9f5a5d134645adb5db1b9737ea3')
    expect(sha256Hex('暮雪')).toBe('0bd24d7d4e147e5455e7fe8725d71dbab7e049b262ac09a8c28da53c56b608e7')
  })
  it('tells different contents apart', () => {
    expect(sha256Hex('a\u0000b')).not.toBe(sha256Hex('ab'))
  })
})
