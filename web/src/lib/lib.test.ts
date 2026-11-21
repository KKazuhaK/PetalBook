import { describe, expect, it } from 'vitest'
import { absoluteTime, relativeTime } from './time'
import { ancestorsOf, buildFolderTree, checkFolderPath, folderUrl, tagUrl } from './folders'

describe('relativeTime', () => {
  const now = Date.parse('2026-11-21T12:00:00Z')
  it('is 刚刚 under a minute, then minutes, hours, days', () => {
    expect(relativeTime('2026-11-21T11:59:40Z', now)).toBe('刚刚')
    expect(relativeTime('2026-11-21T11:55:00Z', now)).toBe('5分钟前')
    expect(relativeTime('2026-11-21T09:00:00Z', now)).toBe('3小时前')
    expect(relativeTime('2026-11-20T12:00:00Z', now)).toBe('昨天')
    expect(relativeTime('2026-11-14T12:00:00Z', now)).toBe('7天前')
  })
  it('falls back to a date after a month, with the year only when it differs', () => {
    expect(relativeTime('2026-08-02T12:00:00Z', now)).toBe('8月2日')
    expect(relativeTime('2025-03-09T12:00:00Z', now)).toBe('2025年3月9日')
  })
  it('survives garbage', () => {
    expect(relativeTime('nope', now)).toBe('')
    expect(absoluteTime('nope')).toBe('')
  })
})

describe('folders', () => {
  it('nests by path and tolerates missing parents', () => {
    const { rootPages, nodes } = buildFolderTree([
      { path: '', pages: 3 },
      { path: '随笔/2026', pages: 4 },
      { path: 'Tang', pages: 95 },
      { path: '随笔', pages: 2 },
    ])
    expect(rootPages).toBe(3)
    expect(nodes.map((n) => n.path).sort()).toEqual(['Tang', '随笔'])
    const essays = nodes.find((n) => n.path === '随笔')!
    expect(essays.pages).toBe(2)
    expect(essays.children).toEqual([{ path: '随笔/2026', name: '2026', pages: 4, children: [] }])
    expect(buildFolderTree([{ path: 'a/b/c', pages: 1 }]).nodes[0].children[0].children[0].path).toBe('a/b/c')
  })
  it('builds URLs that keep slashes and escape the rest', () => {
    expect(folderUrl('')).toBe('/f')
    expect(folderUrl('随笔/2026')).toBe(`/f/${encodeURIComponent('随笔')}/2026`)
    expect(tagUrl('花/栀子 白')).toBe(`/t/${encodeURIComponent('花')}/${encodeURIComponent('栀子 白')}`)
  })
  it('tidies slashes and spaces, and applies the server rules with a message', () => {
    expect(checkFolderPath(' /随笔/ 2027/ ')).toEqual({ path: '随笔/2027' })
    expect(checkFolderPath('a\\b')).toEqual({ path: 'a/b' })
    for (const bad of ['', '  ', '.hidden', 'a/.b', '../x', 'a/..', 'a./b', 'assets', 'assets/x', 'a//b', '长'.repeat(86)]) {
      const r = checkFolderPath(bad)
      expect(r, bad).toHaveProperty('error')
      expect((r as { error: string }).error).toMatch(/[\u4e00-\u9fa5]/)
    }
    expect(checkFolderPath('长'.repeat(85))).toEqual({ path: '长'.repeat(85) })
    expect(checkFolderPath('docs/assets')).toEqual({ path: 'docs/assets' })
    expect(ancestorsOf('a/b/c')).toEqual(['a', 'a/b', 'a/b/c'])
  })
})

describe('normalizeUrl', () => {
  it('adds https to bare domains, keeps real schemes, refuses script schemes', async () => {
    const { normalizeUrl } = await import('./url')
    expect(normalizeUrl(' example.com/a ')).toBe('https://example.com/a')
    expect(normalizeUrl('http://x.y')).toBe('http://x.y')
    expect(normalizeUrl('mailto:a@b.c')).toBe('mailto:a@b.c')
    expect(normalizeUrl('/p/01JD3K8Q7Z')).toBe('/p/01JD3K8Q7Z')
    expect(normalizeUrl('javascript:alert(1)')).toBeNull()
    expect(normalizeUrl('  ')).toBeNull()
  })
})
