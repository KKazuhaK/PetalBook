import { afterEach, describe, expect, it } from 'vitest'
import { fileUrl, spaceApi } from './client'
import { mockFetch } from './mock/fetch'
import { createMockServer } from './mock/server'
import { sha256Hex } from './mock/sha256'
import { storedName } from './mock/files'

describe('fileUrl', () => {
  it('encodes each segment and keeps the slashes', () => {
    expect(fileUrl('default', '/assets/01JD3K8Q7Z/street-3f9a1c2b.jpg')).toBe(
      '/api/spaces/default/files/assets/01JD3K8Q7Z/street-3f9a1c2b.jpg',
    )
    expect(fileUrl('default', 'assets/a/我的 文件#1?.pdf')).toBe(
      '/api/spaces/default/files/assets/a/%E6%88%91%E7%9A%84%20%E6%96%87%E4%BB%B6%231%3F.pdf',
    )
    expect(fileUrl('a/b', '/x')).toBe('/api/spaces/a%2Fb/files/x')
  })
})

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

describe('uploadAsset', () => {
  const realFetch = globalThis.fetch
  afterEach(() => {
    globalThis.fetch = realFetch
  })
  it("sends the bytes as application/octet-stream to the page's assets, with the name in the query", async () => {
    const server = createMockServer()
    const seen: { method: string; url: string; type: string | null }[] = []
    const f = mockFetch(server, {
      onRequest: (method, url, _b, init) =>
        seen.push({ method, url, type: new Headers(init?.headers).get('content-type') }),
    })
    globalThis.fetch = f
    const id = server.ids()[0]
    const a = await spaceApi('default').uploadAsset(id, new Blob([new Uint8Array([1, 2, 3])]), '街景 1.png')
    expect(seen).toEqual([
      {
        method: 'POST',
        url: `/api/spaces/default/pages/${id}/assets?name=${encodeURIComponent('街景 1.png')}`,
        type: 'application/octet-stream',
      },
    ])
    expect(a.size).toBe(3)
    expect(a.path).toMatch(new RegExp(`^/assets/${id}/街景 1-[0-9a-f]{8}\\.png$`))
  })
})
