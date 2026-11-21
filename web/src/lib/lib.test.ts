import { describe, expect, it } from 'vitest'
import { absoluteTime, relativeTime } from './time'
import { ancestorsOf, buildFolderTree, checkFolderPath } from './folders'
import { spaceUrls } from './urls'
import { fragmentToken, loginUrl, safeReturn } from './auth'

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
  it('builds URLs inside a space that keep slashes and escape the rest', () => {
    const u = spaceUrls('01JD3K8Q7Z')
    expect(u.recent()).toBe('/s/01JD3K8Q7Z/recent')
    expect(u.folder('')).toBe('/s/01JD3K8Q7Z/f')
    expect(u.folder('随笔/2026')).toBe(`/s/01JD3K8Q7Z/f/${encodeURIComponent('随笔')}/2026`)
    expect(u.tag('花/栀子 白')).toBe(`/s/01JD3K8Q7Z/t/${encodeURIComponent('花')}/${encodeURIComponent('栀子 白')}`)
    expect(u.page('01JD3K8Q7Z')).toBe('/s/01JD3K8Q7Z/p/01JD3K8Q7Z')
    expect(u.search('栀子 白')).toBe(`/s/01JD3K8Q7Z/search?q=${encodeURIComponent('栀子 白')}`)
    expect(spaceUrls('a/b').recent()).toBe('/s/a%2Fb/recent')
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

describe('sign-in return paths', () => {
  it('accepts only a plain path inside the app', () => {
    expect(safeReturn('/s/01JD3K8Q7Z/p/01JD3K8Q7Z?x=1#y')).toBe('/s/01JD3K8Q7Z/p/01JD3K8Q7Z?x=1#y')
    expect(safeReturn('/')).toBe('/')
    for (const bad of [null, undefined, '', 'relative', '//evil.example', 'https://evil.example', '/a\\b', 'javascript:alert(1)', '/ok\nbad', '/login', '/login?return=%2F', '/setup', '/invite#x']) expect(safeReturn(bad as string | null), String(bad)).toBe('/')
  })
  it('makes the sign-in address with the path to come back to', () => {
    expect(loginUrl('/s/x/p/y')).toBe('/login?return=%2Fs%2Fx%2Fp%2Fy')
    expect(loginUrl('/')).toBe('/login')
    expect(loginUrl('/login')).toBe('/login')
  })
})

describe('tokens in the address fragment', () => {
  const at = (hash: string) => {
    window.history.replaceState(null, '', `/invite${hash}`)
  }
  it('is read once and removed from the address bar, and kept for later reads of the same page load', () => {
    at('#abc123')
    expect(fragmentToken('invite')).toBe('abc123')
    expect(window.location.hash).toBe('')
    expect(window.location.pathname).toBe('/invite')
    // A second look (a remount) still gets it, though the address no longer has it.
    expect(fragmentToken('invite')).toBe('abc123')
  })
  it('never ends up in a query string or storage', () => {
    at('#secret-token')
    fragmentToken('setup')
    expect(window.location.search).toBe('')
    expect(window.location.href).not.toContain('secret-token')
    expect(JSON.stringify({ ...localStorage })).not.toContain('secret-token')
    expect(JSON.stringify({ ...sessionStorage })).not.toContain('secret-token')
  })
})
