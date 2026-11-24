import { describe, expect, it } from 'vitest'
import { addressToUrl, resolveAddress } from './fileAddress'

const file = (path: string, suffix = '') => ({ kind: 'file', path, suffix })

describe('resolveAddress', () => {
  it('leaves what has a scheme alone', () => {
    for (const a of [
      'https://example.com/a.png',
      'http://x/y',
      'data:image/png;base64,AAAA',
      'mailto:a@b.c',
      'tel:123',
      'blob:abc',
      'HTTPS://X.COM',
    ]) {
      expect(resolveAddress(a, 'notes')).toEqual({ kind: 'external', url: a })
    }
  })
  it('leaves // and # alone', () => {
    expect(resolveAddress('//cdn.example.com/a.png', 'notes')).toEqual({
      kind: 'external',
      url: '//cdn.example.com/a.png',
    })
    expect(resolveAddress('#section', 'notes')).toEqual({ kind: 'external', url: '#section' })
    expect(resolveAddress('', 'notes')).toEqual({ kind: 'external', url: '' })
  })
  it('a leading / is from the root of the space, whatever the folder', () => {
    expect(resolveAddress('/assets/01JD3K8Q7Z/x.jpg', 'a/b/c')).toEqual(file('/assets/01JD3K8Q7Z/x.jpg'))
    expect(resolveAddress('/assets/x.jpg', '')).toEqual(file('/assets/x.jpg'))
  })
  it('anything else is relative to the folder of the page', () => {
    expect(resolveAddress('pic.png', 'notes/2026')).toEqual(file('/notes/2026/pic.png'))
    expect(resolveAddress('./img/pic.png', 'notes')).toEqual(file('/notes/img/pic.png'))
    expect(resolveAddress('pic.png', '')).toEqual(file('/pic.png'))
  })
  it('resolves .. and never goes above the root', () => {
    expect(resolveAddress('../assets/a.png', 'notes/2026')).toEqual(file('/notes/assets/a.png'))
    expect(resolveAddress('../../a.png', 'notes/2026')).toEqual(file('/a.png'))
    expect(resolveAddress('../../../../a.png', 'notes')).toEqual(file('/a.png'))
    expect(resolveAddress('/../../a.png', 'notes')).toEqual(file('/a.png'))
    expect(resolveAddress('a/../b/./c.png', 'x')).toEqual(file('/x/b/c.png'))
  })
  it('keeps a query or fragment out of the path and puts it back', () => {
    expect(resolveAddress('/assets/a/m.pdf#page=3', '')).toEqual(file('/assets/a/m.pdf', '#page=3'))
    expect(resolveAddress('m.pdf?v=2#x', 'n')).toEqual(file('/n/m.pdf', '?v=2#x'))
  })
  it('decodes percent-encoded segments, and takes broken ones literally', () => {
    expect(resolveAddress('/assets/a/my%20file.png', '')).toEqual(file('/assets/a/my file.png'))
    expect(resolveAddress('/assets/a/%E6%88%91.png', '')).toEqual(file('/assets/a/我.png'))
    expect(resolveAddress('/assets/a/100%.png', '')).toEqual(file('/assets/a/100%.png'))
  })
  it('collapses empty segments', () =>
    expect(resolveAddress('/assets//a///b.png', '')).toEqual(file('/assets/a/b.png')))
})

describe('addressToUrl', () => {
  it('maps a file to the files endpoint, each segment encoded', () => {
    expect(addressToUrl('default', 'notes', '/assets/01JD/街景 (1)-3f9a1c2b.jpg')).toBe(
      '/api/spaces/default/files/assets/01JD/%E8%A1%97%E6%99%AF%20(1)-3f9a1c2b.jpg',
    )
    expect(addressToUrl('s p', '', 'a.png')).toBe('/api/spaces/s%20p/files/a.png')
  })
  it('does not double-encode an address that is already encoded', () =>
    expect(addressToUrl('default', '', '/assets/a/my%20file.png')).toBe(
      '/api/spaces/default/files/assets/a/my%20file.png',
    ))
  it('keeps the fragment', () =>
    expect(addressToUrl('default', '', '/assets/a/m.pdf#page=3')).toBe(
      '/api/spaces/default/files/assets/a/m.pdf#page=3',
    ))
  it('returns an external address unchanged', () => {
    expect(addressToUrl('default', 'n', 'https://example.com/a.png')).toBe('https://example.com/a.png')
    expect(addressToUrl('default', 'n', '#top')).toBe('#top')
  })
})

describe("an address never leads out of the space's files", () => {
  it('resolves encoded dots and slashes like plain ones', () => {
    expect(addressToUrl('S1', 'a/b', '%2e%2e/%2e%2e/%2e%2e/%2e%2e/auth/x')).toBe('/api/spaces/S1/files/auth/x')
    expect(addressToUrl('S1', '', '/assets/..%2F..%2F..%2Fsession')).toBe('/api/spaces/S1/files/session')
    expect(addressToUrl('S1', 'a', '..%5C..%5Cx.png')).toBe('/api/spaces/S1/files/x.png')
    for (const a of ['%2e%2e/%2e%2e/x', '../../../../x', '/..%2f..%2f..%2fx', './%2E%2E/%2E%2E/x']) {
      expect(new URL(addressToUrl('S1', 'f', a), 'http://h').pathname.startsWith('/api/spaces/S1/files/')).toBe(true)
    }
  })
})
