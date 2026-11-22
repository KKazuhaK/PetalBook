// Node's own module: the web app has no Node typings, and the test runs in Node.
// @ts-expect-error TS2591
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { TAG_BG_PERCENT, TAG_COLORS, TAG_HUES, isTagColor, normalizeHex, parseTagColor, tagColorClass, tagColorStyle } from './tagColors'

const css: string = readFileSync('src/index.css', 'utf8')

describe('tag colours', () => {
  it('are the ten names of docs/api.md', () => {
    expect(TAG_COLORS).toEqual(['gray', 'red', 'orange', 'amber', 'green', 'teal', 'blue', 'purple', 'pink', 'rose'])
    expect(isTagColor('rose')).toBe(true)
    expect(isTagColor('magenta')).toBe(false)
    expect(isTagColor(null)).toBe(false)
  })

  it('a custom colour is #rrggbb in any case; anything else is no colour', () => {
    expect(parseTagColor('#A1B2C3')).toEqual({ kind: 'custom', hex: '#a1b2c3' })
    expect(parseTagColor('rose')).toEqual({ kind: 'preset', name: 'rose' })
    for (const bad of ['#abc', '#gggggg', 'abcdef', '#a1b2c3d', '', 7, undefined]) expect(parseTagColor(bad)).toBeNull()
    expect(tagColorClass('#a1b2c3')).toBe('tag-custom')
    expect(tagColorStyle('#A1B2C3')).toEqual({ '--tag-hue': '#a1b2c3' })
    expect(tagColorStyle('rose')).toBeUndefined()
    expect(css).toContain('.tag-custom,')
  })

  it('typed hex: # optional, any case, six digits', () => {
    expect(normalizeHex(' A1B2C3 ')).toBe('#a1b2c3')
    expect(normalizeHex('#a1b2c3')).toBe('#a1b2c3')
    expect(normalizeHex('abc')).toBeNull()
    expect(normalizeHex('##a1b2c3')).toBeNull()
  })

  it('each has a class and a hue in index.css, the same as here', () => {
    for (const c of TAG_COLORS) {
      expect(tagColorClass(c)).toBe(`tag-${c}`)
      expect(css).toContain(`.tag-${c} { --tag-hue: ${TAG_HUES[c]}; }`)
    }
    expect(css).toContain(`color-mix(in srgb, var(--tag-hue) ${TAG_BG_PERCENT}%, transparent)`)
    expect(tagColorClass(undefined)).toBe('')
  })
})
