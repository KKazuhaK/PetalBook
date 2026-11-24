// Node's own module: the web app has no Node typings, and the test runs in Node.
// @ts-expect-error TS2591
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { contrast, mix } from './contrast'
import { PALETTE_CSS_BEGIN, PALETTE_CSS_END, PALETTE_IDS, palettes, paletteCss, varsOf, type Tones } from './palettes'
import { TAG_BG_PERCENT, TAG_HUES } from './tagColors'

const AA = 4.5
// index.css as written: the bundler would hand back the compiled stylesheet.
const css: string = readFileSync('src/index.css', 'utf8')

// The default palette is the CSS written in oklch, so it is read from the file
// and converted, not copied here.
function oklchToHex(l: number, c: number, hDeg: number): string {
  const h = (hDeg * Math.PI) / 180
  const a = c * Math.cos(h)
  const b = c * Math.sin(h)
  const l_ = (l + 0.3963377774 * a + 0.2158037573 * b) ** 3
  const m_ = (l - 0.1055613458 * a - 0.0638541728 * b) ** 3
  const s_ = (l - 0.0894841775 * a - 1.291485548 * b) ** 3
  const lin = [
    4.0767416621 * l_ - 3.3077115913 * m_ + 0.2309699292 * s_,
    -1.2684380046 * l_ + 2.6097574011 * m_ - 0.3413193965 * s_,
    -0.0041960863 * l_ - 0.7034186147 * m_ + 1.707614701 * s_,
  ]
  const enc = (v: number) => {
    const x = Math.min(1, Math.max(0, v))
    return Math.round(255 * (x <= 0.0031308 ? 12.92 * x : 1.055 * x ** (1 / 2.4) - 0.055))
  }
  return `#${lin.map((v) => enc(v).toString(16).padStart(2, '0')).join('')}`
}

function block(selector: string): Record<string, string> {
  const start = css.indexOf(`\n${selector} {`)
  expect(start, selector).toBeGreaterThan(-1)
  const body = css.slice(start, css.indexOf('}', start))
  const out: Record<string, string> = {}
  for (const m of body.matchAll(/--([\w-]+):\s*oklch\(([\d.]+)\s+([\d.]+)\s+([\d.]+)(?:\s*\/\s*[\d.]+%)?\)/g))
    out[m[1]] = oklchToHex(+m[2], +m[3], +m[4])
  return out
}

interface Case {
  palette: string
  mode: 'light' | 'dark'
  vars: Record<string, string>
}
const cases: Case[] = [
  { palette: 'default', mode: 'light', vars: block(':root') },
  { palette: 'default', mode: 'dark', vars: block('.dark') },
  ...palettes.flatMap((p) =>
    (['light', 'dark'] as const)
      .filter((m) => p[m])
      .map((m) => ({ palette: p.id, mode: m, vars: varsOf(p[m] as Tones) })),
  ),
]

describe('palettes', () => {
  it('has the default and five schemes, each with a light and a dark form', () => {
    expect(palettes.map((p) => p.id)).toEqual([...PALETTE_IDS])
    expect(PALETTE_IDS).toEqual(['default', 'catppuccin', 'nord', 'solarized', 'gruvbox', 'rose-pine'])
    for (const p of palettes.filter((p) => p.id !== 'default')) {
      expect(p.light, p.id).toBeDefined()
      expect(p.dark, p.id).toBeDefined()
    }
    expect(cases).toHaveLength(12)
  })

  for (const { palette, mode, vars } of cases) {
    const name = `${palette} ${mode}`
    it(`${name}: body text, muted text and primary buttons reach WCAG AA (4.5:1)`, () => {
      expect(contrast(vars.foreground, vars.background), 'foreground on background').toBeGreaterThanOrEqual(AA)
      expect(
        contrast(vars['muted-foreground'], vars.background),
        'muted-foreground on background',
      ).toBeGreaterThanOrEqual(AA)
      expect(
        contrast(vars['primary-foreground'], vars.primary),
        'primary-foreground on primary',
      ).toBeGreaterThanOrEqual(AA)
    })

    // The default is the app's colours as they were and is not altered here (its
    // muted text on the muted surface is 4.3:1); the schemes are held to more.
    it.skipIf(palette === 'default')(`${name}: the same text holds on the other surfaces it is drawn on`, () => {
      for (const surface of ['card', 'popover', 'muted', 'secondary', 'sidebar']) {
        expect(
          contrast(vars['muted-foreground'], vars[surface]),
          `muted-foreground on ${surface}`,
        ).toBeGreaterThanOrEqual(AA)
        expect(contrast(vars.foreground, vars[surface]), `foreground on ${surface}`).toBeGreaterThanOrEqual(AA)
      }
      expect(contrast(vars['accent-foreground'], vars.accent), 'accent-foreground on accent').toBeGreaterThanOrEqual(AA)
      expect(
        contrast(vars['sidebar-accent-foreground'], vars['sidebar-accent']),
        'sidebar-accent-foreground on sidebar-accent',
      ).toBeGreaterThanOrEqual(AA)
      // Hover and selected rows keep muted text readable enough (large or secondary text).
      expect(contrast(vars['muted-foreground'], vars.accent), 'muted-foreground on accent').toBeGreaterThanOrEqual(3)
    })

    it(`${name}: the petal colour, the error colour and the success pair are readable`, () => {
      // Used as text and icons on the page background.
      expect(contrast(vars.brand, vars.background), 'brand on background').toBeGreaterThanOrEqual(3)
      expect(contrast(vars.destructive, vars.background), 'destructive on background').toBeGreaterThanOrEqual(3)
      if (palette !== 'default') {
        expect(contrast(vars.brand, vars.background), 'brand on background').toBeGreaterThanOrEqual(AA)
        expect(contrast(vars.destructive, vars.background), 'destructive on background').toBeGreaterThanOrEqual(AA)
        expect(
          contrast(vars['sidebar-primary-foreground'], vars['sidebar-primary']),
          'sidebar-primary-foreground on sidebar-primary',
        ).toBeGreaterThanOrEqual(AA)
        expect(
          contrast(vars['success-foreground'], vars.success),
          'success-foreground on success',
        ).toBeGreaterThanOrEqual(AA)
      }
    })
  }

  it('sets every variable the default sets, so no palette leaves a stray default colour behind', () => {
    const wanted = Object.keys(block(':root'))
    for (const c of cases.filter((x) => x.palette !== 'default')) {
      for (const key of wanted) expect(c.vars[key], `${c.palette} ${c.mode} --${key}`).toBeDefined()
    }
  })

  it('index.css carries exactly the block that the table writes', () => {
    const start = css.indexOf(PALETTE_CSS_BEGIN)
    const end = css.indexOf(PALETTE_CSS_END)
    expect(start).toBeGreaterThan(-1)
    expect(css.slice(start, end + PALETTE_CSS_END.length)).toBe(paletteCss())
  })

  it('the dark selector is more specific than the light one, so dark wins whatever the order', () => {
    expect(paletteCss()).toContain('html[data-palette="nord"].dark {')
    expect(paletteCss()).toContain('html[data-palette="nord"] {')
  })
})

describe('tag colour chips', () => {
  // The chip tints its background with a fixed hue and keeps the foreground
  // for the text; check that on every surface a chip can sit on.
  for (const { palette, mode, vars } of cases) {
    it(`${palette} ${mode}: the text of every colour is readable on its chip`, () => {
      for (const surface of ['background', 'card', 'sidebar', 'accent']) {
        for (const [color, hue] of Object.entries(TAG_HUES)) {
          const chip = mix(hue, TAG_BG_PERCENT, vars[surface])
          expect(contrast(vars.foreground, chip), `${color} on ${surface}`).toBeGreaterThanOrEqual(AA)
        }
      }
    })
  }
})
