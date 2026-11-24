import { describe, expect, it } from 'vitest'
import { canonicalTag, matchTag } from './langTags'

describe('canonicalTag', () => {
  it('writes a well-formed tag in canonical form, a bare zh as zh-Hans', () => {
    expect(canonicalTag('zh')).toBe('zh-Hans')
    expect(canonicalTag('ZH-cn')).toBe('zh-CN')
    expect(canonicalTag('pt-br')).toBe('pt-BR')
    expect(canonicalTag('zh-hant')).toBe('zh-Hant')
    expect(canonicalTag(' en ')).toBe('en')
    expect(canonicalTag('en-GB')).toBe('en-GB')
  })
  it('refuses what is not a tag, or whose language is not a registered one', () => {
    for (const bad of ['', 'cn', 'english', 'zh_CN', 'und', 'x-foo', 'en-', '中文', 'zh Hans', 'a'.repeat(40)])
      expect(canonicalTag(bad), bad).toBeNull()
  })
})

describe('matchTag', () => {
  const have = ['zh-Hans', 'zh-Hant', 'en', 'pt-BR']
  it("matches a reader's language the BCP 47 way", () => {
    expect(matchTag('zh-CN', have)).toBe('zh-Hans')
    expect(matchTag('zh-SG', have)).toBe('zh-Hans')
    expect(matchTag('zh-TW', have)).toBe('zh-Hant')
    expect(matchTag('zh-HK', have)).toBe('zh-Hant')
    expect(matchTag('en-GB', have)).toBe('en')
    expect(matchTag('pt', have)).toBe('pt-BR')
  })
  it('finds another script of the language when there is no better, and none for another language', () => {
    expect(matchTag('zh-TW', ['zh-Hans', 'en'])).toBe('zh-Hans')
    expect(matchTag('fr', have)).toBeNull()
    expect(matchTag('', have)).toBeNull()
    expect(matchTag('en', [])).toBeNull()
  })
})
