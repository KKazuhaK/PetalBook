// Colour palettes. The default is the CSS in index.css as it always was; the
// others are public colour schemes, each in a light and a dark form, applied by
// `data-palette` on <html> (see providers/theme.tsx). Only CSS variables change,
// so no component knows which palette is on.
//
// This table is the single source: `paletteCss()` writes the block at the end of
// index.css (palettes.test.ts fails when the file and the table disagree), and
// the same table feeds the swatches in Settings and the contrast test.
//
// Where a scheme's own published colours cannot give body text 4.5:1 (Solarized
// and the muted text of a few others), the nearest published colour that can is
// used, or a published colour is mixed toward the scheme's own text colour. Those
// are marked `derived` below and in the report.

export const PALETTE_IDS = ['default', 'catppuccin', 'nord', 'solarized', 'gruvbox', 'rose-pine'] as const
export type PaletteId = (typeof PALETTE_IDS)[number]
export const isPaletteId = (v: unknown): v is PaletteId =>
  typeof v === 'string' && (PALETTE_IDS as readonly string[]).includes(v)

// What one palette in one mode says, as `#rrggbb`.
export interface Tones {
  bg: string
  // Sidebar background.
  sidebar: string
  // Cards and popovers.
  card: string
  // Muted, secondary and accent backgrounds (hover, selected, chips).
  surface: string
  // The stronger of the two, for hover on a surface.
  surface2: string
  border: string
  fg: string
  mutedFg: string
  // The petal colour, and the theme colour of the controls: primary buttons, switches and checkboxes
  // when on, progress bars, links, selected rows, the focus ring. `--primary` and `--ring` are this.
  brand: string
  onBrand: string
  destructive: string
  success: string
  onSuccess: string
  warning: string
  charts: [string, string, string, string, string]
}

export interface Palette {
  id: PaletteId
  // Proper names are not translated; "Default" is, through the dictionaries.
  name: string
  light?: Tones
  dark?: Tones
  // Only the default has to say how it looks (its colours are written in oklch
  // in index.css); the others show their own tones.
  swatch?: { light: [string, string, string]; dark: [string, string, string] }
}

// Background, text and accent for a swatch of the palette in a mode.
export function swatchOf(p: Palette, mode: 'light' | 'dark'): [string, string, string] {
  const tones = p[mode]
  if (tones) return [tones.bg, tones.fg, tones.brand]
  return p.swatch?.[mode] ?? ['#ffffff', '#000000', '#888888']
}

export const palettes: Palette[] = [
  {
    id: 'default',
    name: 'Default',
    swatch: { light: ['#ffffff', '#0a0a0a', '#c4285f'], dark: ['#0a0a0a', '#fafafa', '#ef8aa6'] },
  },
  {
    // https://catppuccin.com/palette (Latte, Mocha)
    id: 'catppuccin',
    name: 'Catppuccin',
    light: {
      bg: '#eff1f5', // base
      sidebar: '#e6e9ef', // mantle
      card: '#eff1f5',
      surface: '#e6e9ef',
      surface2: '#dce0e8', // crust
      border: '#ccd0da', // surface0
      fg: '#4c4f69', // text
      mutedFg: '#5c5f77', // subtext1 (derived: subtext0 is under 4.5:1 on the muted surfaces)
      brand: '#d20f39', // red, the closest to a petal that reads on Latte
      onBrand: '#ffffff',
      destructive: '#d20f39',
      success: '#2d7a1c', // derived: green darkened for text on it
      onSuccess: '#ffffff',
      warning: '#df8e1d',
      charts: ['#1e66f5', '#8839ef', '#ea76cb', '#fe640b', '#179299'],
    },
    dark: {
      bg: '#1e1e2e', // base
      sidebar: '#181825', // mantle
      card: '#313244', // surface0
      surface: '#313244',
      surface2: '#45475a', // surface1
      border: '#45475a',
      fg: '#cdd6f4', // text
      mutedFg: '#bac2de', // subtext1
      brand: '#f5c2e7', // pink
      onBrand: '#11111b',
      destructive: '#f38ba8', // red
      success: '#a6e3a1',
      onSuccess: '#11111b',
      warning: '#f9e2af',
      charts: ['#89b4fa', '#cba6f7', '#f5c2e7', '#fab387', '#94e2d5'],
    },
  },
  {
    // https://www.nordtheme.com/docs/colors-and-palettes. Nord has no light
    // scheme of its own; the light form is built from its Snow Storm colours.
    id: 'nord',
    name: 'Nord',
    light: {
      bg: '#eceff4', // nord6
      sidebar: '#e5e9f0', // nord5
      card: '#eceff4',
      surface: '#e5e9f0',
      surface2: '#d8dee9', // nord4
      border: '#d8dee9',
      fg: '#2e3440', // nord0
      mutedFg: '#4c566a', // nord3
      brand: '#a8505a', // derived: nord11 darkened
      onBrand: '#eceff4',
      destructive: '#a8505a',
      success: '#4a7336', // derived: nord14 darkened
      onSuccess: '#eceff4',
      warning: '#ebcb8b', // nord13
      charts: ['#5e81ac', '#81a1c1', '#8fbcbb', '#a3be8c', '#b48ead'],
    },
    dark: {
      bg: '#2e3440', // nord0
      sidebar: '#2a303b', // derived: nord0 a step darker
      card: '#3b4252', // nord1
      surface: '#3b4252',
      surface2: '#434c5e', // nord2
      border: '#4c566a', // nord3
      fg: '#eceff4', // nord6
      mutedFg: '#bcc5d4', // derived: nord4 toward nord0
      brand: '#c8a8c2', // derived: nord15 lightened
      onBrand: '#2e3440',
      destructive: '#e08e95', // derived: nord11 lightened
      success: '#a3be8c', // nord14
      onSuccess: '#2e3440',
      warning: '#ebcb8b',
      charts: ['#88c0d0', '#81a1c1', '#8fbcbb', '#a3be8c', '#b48ead'],
    },
  },
  {
    // https://ethanschoonover.com/solarized/
    id: 'solarized',
    name: 'Solarized',
    light: {
      bg: '#fdf6e3', // base3
      sidebar: '#eee8d5', // base2
      card: '#fdf6e3',
      surface: '#eee8d5',
      surface2: '#e4ddc6', // derived: base2 a step darker
      border: '#e0d9c1', // derived
      fg: '#073642', // base02 (derived choice: base00 is 4.1:1 on base3)
      mutedFg: '#4f656c', // derived: base01 darkened
      brand: '#c4313f', // derived: between red and magenta, darkened
      onBrand: '#fdf6e3',
      destructive: '#c02a28', // derived: red darkened
      success: '#5f7000', // derived: green darkened
      onSuccess: '#fdf6e3',
      warning: '#b58900', // yellow
      charts: ['#268bd2', '#2aa198', '#859900', '#b58900', '#d33682'],
    },
    dark: {
      bg: '#002b36', // base03
      sidebar: '#00252f', // derived: base03 a step darker
      card: '#073642', // base02
      surface: '#073642',
      surface2: '#0c3e4b', // derived
      border: '#1a4b58', // derived
      fg: '#eee8d5', // base2
      mutedFg: '#93a1a1', // base1
      brand: '#e86a9d', // derived: magenta lightened
      onBrand: '#001f27',
      destructive: '#f26a64', // derived: red lightened
      success: '#9fb800', // derived: green lightened
      onSuccess: '#001f27',
      warning: '#b58900',
      charts: ['#268bd2', '#2aa198', '#859900', '#b58900', '#d33682'],
    },
  },
  {
    // https://github.com/morhetz/gruvbox (the contrast and faded colours)
    id: 'gruvbox',
    name: 'Gruvbox',
    light: {
      bg: '#fbf1c7', // bg0
      sidebar: '#f2e5bc', // bg0_s
      card: '#fbf1c7',
      surface: '#f2e5bc',
      surface2: '#ebdbb2', // bg1
      border: '#d5c4a1', // bg2
      fg: '#3c3836', // fg1
      mutedFg: '#665c54', // fg3
      brand: '#9d0006', // red (faded)
      onBrand: '#fbf1c7',
      destructive: '#9d0006',
      success: '#6f6a0d', // derived: green (faded) darkened
      onSuccess: '#fbf1c7',
      warning: '#b57614', // yellow (faded)
      charts: ['#076678', '#427b58', '#79740e', '#b57614', '#8f3f71'],
    },
    dark: {
      bg: '#282828', // bg0
      sidebar: '#1d2021', // bg0_h
      card: '#3c3836', // bg1
      surface: '#32302f', // bg0_s
      surface2: '#3c3836', // bg1
      border: '#504945', // bg2
      fg: '#ebdbb2', // fg1
      mutedFg: '#bdae93', // fg3
      brand: '#fd5a44', // derived: red lightened
      onBrand: '#1d2021',
      destructive: '#fd5a44', // derived: red lightened
      success: '#b8bb26', // green
      onSuccess: '#1d2021',
      warning: '#fabd2f', // yellow
      charts: ['#83a598', '#8ec07c', '#b8bb26', '#fabd2f', '#d3869b'],
    },
  },
  {
    // https://rosepinetheme.com/palette (Dawn, main)
    id: 'rose-pine',
    name: 'Rosé Pine',
    light: {
      bg: '#faf4ed', // base
      sidebar: '#f2e9e1', // overlay
      card: '#fffaf3', // surface
      surface: '#f4ede8', // highlight low
      surface2: '#dfdad9', // highlight med
      border: '#dfdad9',
      fg: '#575279', // text
      mutedFg: '#625d7e', // derived: subtle darkened for 4.5:1 on the surfaces
      brand: '#a1566c', // derived: love darkened
      onBrand: '#faf4ed',
      destructive: '#a1566c', // derived: love darkened
      success: '#286983',
      onSuccess: '#faf4ed',
      warning: '#ea9d34', // gold
      charts: ['#286983', '#56949f', '#907aa9', '#d7827e', '#ea9d34'],
    },
    dark: {
      bg: '#191724', // base
      sidebar: '#16141f', // derived: base a step darker
      card: '#1f1d2e', // surface
      surface: '#21202e', // highlight low
      surface2: '#26233a', // overlay
      border: '#403d52',
      fg: '#e0def4', // text
      mutedFg: '#908caa', // subtle
      brand: '#ebbcba', // rose
      onBrand: '#191724',
      destructive: '#eb6f92', // love
      success: '#9ccfd8', // foam
      onSuccess: '#191724',
      warning: '#f6c177', // gold
      charts: ['#9ccfd8', '#c4a7e7', '#ebbcba', '#f6c177', '#31748f'],
    },
  },
]

export const paletteById = (id: PaletteId): Palette => palettes.find((p) => p.id === id) ?? palettes[0]

// The variables of index.css that a palette sets, from its tones.
export function varsOf(t: Tones): Record<string, string> {
  return {
    background: t.bg,
    foreground: t.fg,
    card: t.card,
    'card-foreground': t.fg,
    popover: t.card,
    'popover-foreground': t.fg,
    primary: t.brand,
    'primary-foreground': t.onBrand,
    secondary: t.surface,
    'secondary-foreground': t.fg,
    muted: t.surface,
    'muted-foreground': t.mutedFg,
    accent: t.surface2,
    'accent-foreground': t.fg,
    destructive: t.destructive,
    border: t.border,
    input: t.border,
    ring: t.brand,
    'chart-1': t.charts[0],
    'chart-2': t.charts[1],
    'chart-3': t.charts[2],
    'chart-4': t.charts[3],
    'chart-5': t.charts[4],
    brand: t.brand,
    success: t.success,
    'success-foreground': t.onSuccess,
    warning: t.warning,
    sidebar: t.sidebar,
    'sidebar-foreground': t.fg,
    'sidebar-primary': t.brand,
    'sidebar-primary-foreground': t.onBrand,
    'sidebar-accent': t.surface2,
    'sidebar-accent-foreground': t.fg,
    'sidebar-border': t.border,
    'sidebar-ring': t.brand,
  }
}

export const PALETTE_CSS_BEGIN =
  '/* ---- colour palettes: generated from src/lib/palettes.ts (palettes.test.ts checks it) ---- */'
export const PALETTE_CSS_END = '/* ---- end of colour palettes ---- */'

// The block for index.css. `html[data-palette]` beats `:root`, and with `.dark`
// it beats the `.dark` block, whatever the order in the file.
export function paletteCss(): string {
  const out: string[] = [PALETTE_CSS_BEGIN]
  for (const p of palettes) {
    for (const mode of ['light', 'dark'] as const) {
      const tones = p[mode]
      if (!tones) continue
      const selector = mode === 'light' ? `html[data-palette="${p.id}"]` : `html[data-palette="${p.id}"].dark`
      out.push(`${selector} {`)
      for (const [k, v] of Object.entries(varsOf(tones))) out.push(`    --${k}: ${v};`)
      out.push('}')
    }
  }
  out.push(PALETTE_CSS_END)
  return out.join('\n')
}
