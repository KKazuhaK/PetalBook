import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { decideLanguage, languages, LANG_KEY } from './index'
import { en } from './en'
import { zhCN } from './zh-CN'
import { adminEn } from './admin/en'
import { adminZhCN } from './admin/zh-CN'

describe('decideLanguage', () => {
  it('uses a saved choice over the browser', () => {
    expect(decideLanguage('en', ['zh-CN'])).toBe('en')
    expect(decideLanguage('zh-CN', ['en-US'])).toBe('zh-CN')
  })
  it('falls back to the browser when nothing is saved: any Chinese is Chinese, the rest English', () => {
    expect(decideLanguage(null, ['zh-CN'])).toBe('zh-CN')
    expect(decideLanguage(null, ['zh'])).toBe('zh-CN')
    expect(decideLanguage(null, ['zh-TW', 'en'])).toBe('zh-CN')
    expect(decideLanguage(null, ['en-US', 'zh-CN'])).toBe('zh-CN')
    expect(decideLanguage(null, ['en-US', 'fr'])).toBe('en')
    expect(decideLanguage(undefined, [])).toBe('en')
  })
  it('ignores a stored value that is not one of ours', () => {
    expect(decideLanguage('fr', ['en-US'])).toBe('en')
    expect(decideLanguage('fr', ['zh-CN'])).toBe('zh-CN')
    expect(decideLanguage('', ['de'])).toBe('en')
    expect(decideLanguage('EN', ['zh-CN'])).toBe('zh-CN')
  })
})

describe('the language in effect at load', () => {
  beforeEach(() => {
    vi.resetModules()
    localStorage.clear()
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('is Chinese in tests when nothing is saved', async () => {
    const m = await import('./index')
    expect(m.lang).toBe('zh-CN')
    expect(m.t.appName).toBe('PetalBook')
    expect(m.t.nav.recent).toBe('最近')
    expect(document.documentElement.lang).toBe('zh-CN')
  })
  it('follows the saved choice and marks the document', async () => {
    localStorage.setItem(LANG_KEY, 'en')
    const m = await import('./index')
    expect(m.lang).toBe('en')
    expect(m.t.nav.recent).toBe('Recent')
    expect(document.documentElement.lang).toBe('en')
  })
  it('ignores a bad saved value', async () => {
    localStorage.setItem(LANG_KEY, 'klingon')
    expect((await import('./index')).lang).toBe('zh-CN')
  })
  it('survives storage that throws', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    expect((await import('./index')).lang).toBe('zh-CN')
  })
  it('setLanguage saves the choice and reloads; when it cannot be saved nothing happens', async () => {
    const reload = vi.fn()
    vi.stubGlobal('location', { ...window.location, reload })
    const m = await import('./index')
    expect(m.setLanguage('en')).toBe(true)
    expect(localStorage.getItem(LANG_KEY)).toBe('en')
    expect(reload).toHaveBeenCalledTimes(1)
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    // A reload would only bring the same language back, and cost what the page held.
    expect(m.setLanguage('zh-CN')).toBe(false)
    expect(reload).toHaveBeenCalledTimes(1)
    vi.unstubAllGlobals()
  })
  it('names each language in its own language', () => {
    expect(languages.map((l) => l.name)).toEqual(['中文', 'English'])
  })
})

// The compiler already refuses a missing or extra key in en.ts; this walks the
// values too, because a function in one language and text in the other is the
// kind of difference a type cast could hide.
describe('dictionaries', () => {
  const walk = (a: unknown, b: unknown, path: string, problems: string[]) => {
    const kind = (v: unknown) => (typeof v === 'function' ? 'function' : typeof v === 'string' ? 'string' : v !== null && typeof v === 'object' ? 'object' : typeof v)
    if (kind(a) !== kind(b)) return void problems.push(`${path}: ${kind(a)} vs ${kind(b)}`)
    if (kind(a) === 'function') {
      if ((a as () => void).length !== (b as () => void).length) problems.push(`${path}: takes ${(a as () => void).length} vs ${(b as () => void).length} arguments`)
      return
    }
    if (kind(a) !== 'object') return
    const ka = Object.keys(a as object).sort()
    const kb = Object.keys(b as object).sort()
    for (const k of ka) if (!kb.includes(k)) problems.push(`${path}.${k}: missing in en`)
    for (const k of kb) if (!ka.includes(k)) problems.push(`${path}.${k}: extra in en`)
    for (const k of ka) if (kb.includes(k)) walk((a as Record<string, unknown>)[k], (b as Record<string, unknown>)[k], `${path}.${k}`, problems)
  }

  it('have the same keys, with functions where the other has functions', () => {
    const problems: string[] = []
    walk(zhCN, en, 't', problems)
    walk(adminZhCN, adminEn, 'admin', problems)
    expect(problems).toEqual([])
  })
  it('English has no Chinese text in it', () => {
    const cjk = /[一-鿿]/
    const found: string[] = []
    const scan = (v: unknown, path: string) => {
      if (typeof v === 'string' && cjk.test(v)) found.push(path)
      else if (typeof v === 'function') {
        // Called with stand-in arguments; the one that takes a list gets a list.
        const args = Array.from({ length: v.length }, () => (path.endsWith('.list') && path.includes('fmt') ? ['x'] : 'x'))
        const out = (v as (...a: unknown[]) => unknown)(...args)
        if (typeof out === 'string' && cjk.test(out)) found.push(path)
      } else if (v && typeof v === 'object') for (const [k, x] of Object.entries(v)) scan(x, `${path}.${k}`)
    }
    scan(en, 't')
    scan(adminEn, 'admin')
    expect(found).toEqual([])
  })
  it('English plurals read correctly', () => {
    expect(en.list.allLoaded(1)).toBe('1 page')
    expect(en.list.allLoaded(3)).toBe('3 pages')
    expect(en.import.totals(1, 1)).toBe('1 page, 1 attachment')
    expect(en.import.totals(0, 2)).toBe('0 pages, 2 attachments')
    expect(en.auth.wait(1)).toBe('1 second')
    expect(en.auth.wait(120)).toBe('2 minutes')
  })
})
