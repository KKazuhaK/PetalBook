import { afterEach, describe, expect, it } from 'vitest'
import { fileUrl, spaceApi } from './client'
import { fixtureFetch } from './fixtures/fetch'
import { createFixtureServer } from './fixtures/server'

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

describe('uploadAsset', () => {
  const realFetch = globalThis.fetch
  afterEach(() => {
    globalThis.fetch = realFetch
  })
  it("sends the bytes as application/octet-stream to the page's assets, with the name in the query", async () => {
    const fx = createFixtureServer()
    globalThis.fetch = fixtureFetch(fx)
    const id = fx.data.spaces[0].pages[0].id
    const a = await spaceApi('default').uploadAsset(id, new Blob([new Uint8Array([1, 2, 3])]), '街景 1.png')
    const sent = fx.requests.at(-1)!
    expect(sent).toMatchObject({
      method: 'POST',
      url: `/api/spaces/default/pages/${id}/assets?name=${encodeURIComponent('街景 1.png')}`,
      type: 'application/octet-stream',
    })
    expect(a.size).toBe(3)
  })
})
