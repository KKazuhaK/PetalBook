import { describe, expect, it } from 'vitest'
import { common, createLowlight } from 'lowlight'
import { isDiagramLanguage } from './diagrams'
import { filterLanguages, highlightWord, LANGUAGES, languageLabel, languageWord, PICKER_LANGUAGES, resolveLanguage, withLanguage } from './languages'

describe('the language list', () => {
  const lowlight = createLowlight(common)

  it('only promises colours the highlighter has: every id and alias is registered', () => {
    for (const lang of LANGUAGES) {
      for (const word of [lang.id, ...(lang.aliases ?? [])]) expect(lowlight.registered(word), `${lang.id}: ${word}`).toBe(true)
    }
  })

  it('has no word that means two languages, and is sorted by name in the picker', () => {
    const seen = new Set<string>()
    for (const lang of PICKER_LANGUAGES) {
      for (const word of [lang.id, ...(lang.aliases ?? [])]) {
        expect(seen.has(word), word).toBe(false)
        seen.add(word)
      }
    }
    const names = PICKER_LANGUAGES.map((l) => l.name)
    expect(names).toEqual([...names].sort((a, b) => a.localeCompare(b, 'en')))
  })
})

describe('reading the language attribute', () => {
  it('takes the first word of the info string', () => {
    expect(languageWord('js title=a.js')).toBe('js')
    expect(languageWord('  python  ')).toBe('python')
    expect(languageWord('')).toBeNull()
    expect(languageWord(null)).toBeNull()
    expect(languageWord(3)).toBeNull()
  })

  it('knows ids and aliases, in any case, and nothing else', () => {
    expect(resolveLanguage('TS')?.id).toBe('typescript')
    expect(resolveLanguage('c++ foo')?.id).toBe('cpp')
    expect(resolveLanguage('klingon')).toBeNull()
    expect(resolveLanguage(null)).toBeNull()
  })

  it('hands the highlighter a word only for languages it can colour', () => {
    expect(highlightWord('js')).toBe('js')
    expect(highlightWord('Python')).toBe('python')
    expect(highlightWord('mermaid')).toBeNull()
    expect(highlightWord('klingon')).toBeNull()
    expect(highlightWord(null)).toBeNull()
  })

  it('labels a block by its language name, an unknown word as written, plain text as nothing', () => {
    expect(languageLabel('js')).toBe('JavaScript')
    expect(languageLabel('klingon')).toBe('klingon')
    expect(languageLabel(null)).toBeNull()
  })
})

describe('choosing a language', () => {
  it('writes the id', () => {
    expect(withLanguage(null, 'python')).toBe('python')
    expect(withLanguage('js', 'python')).toBe('python')
  })
  it('keeps what follows the language and a spelling that already means the same', () => {
    expect(withLanguage('js title=a.js', 'python')).toBe('python title=a.js')
    expect(withLanguage('ts', 'typescript')).toBe('ts')
    expect(withLanguage('klingon x', 'go')).toBe('go x')
  })
  it('plain text removes the info string', () => {
    expect(withLanguage('js title=a.js', null)).toBeNull()
  })
})

describe('searching the list', () => {
  it('finds by name, id and alias, ignoring case', () => {
    expect(filterLanguages('type').map((l) => l.id)).toContain('typescript')
    expect(filterLanguages('GOLANG').map((l) => l.id)).toEqual(['go'])
    expect(filterLanguages('c#').map((l) => l.id)).toEqual(['csharp'])
    expect(filterLanguages('zzzz')).toEqual([])
  })
  it('puts names that start with the text first', () => {
    const ids = filterLanguages('java').map((l) => l.id)
    expect(ids.slice(0, 2).sort()).toEqual(['java', 'javascript'])
  })
  it('lists everything for an empty search', () => {
    expect(filterLanguages('  ')).toHaveLength(PICKER_LANGUAGES.length)
  })
})

describe('which code blocks are diagrams', () => {
  it('those in the language mermaid, however it is written', () => {
    expect(isDiagramLanguage('mermaid')).toBe(true)
    expect(isDiagramLanguage('Mermaid')).toBe(true)
    expect(isDiagramLanguage('mmd')).toBe(true)
    expect(isDiagramLanguage('mermaid theme=dark')).toBe(true)
  })
  it('and no others', () => {
    expect(isDiagramLanguage('js')).toBe(false)
    expect(isDiagramLanguage('mermaids')).toBe(false)
    expect(isDiagramLanguage(null)).toBe(false)
    expect(isDiagramLanguage(undefined)).toBe(false)
  })
})
