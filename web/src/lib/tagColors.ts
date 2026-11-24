// The ten colours a tag can have (docs/api.md, "Folders and tags"). The server
// stores only the name; how each one looks is decided here, by the `tag-*`
// classes at the end of index.css. Those derive the look from one fixed hue
// per name, with opacity, so every palette and both modes get a readable chip
// without any palette needing a colour per tag.

import type { CSSProperties } from 'react'
import type { TagColor, TagColorName } from '../api/types.ts'

export const TAG_COLORS: readonly TagColorName[] = [
  'gray',
  'red',
  'orange',
  'amber',
  'green',
  'teal',
  'blue',
  'purple',
  'pink',
  'rose',
]

// The hue of each name. Mid-lightness, so it reads as itself on a swatch. The
// chip's text stays the page's own foreground and the hue only tints the
// background, which is what keeps every pair legible (see tagColors.test.ts).
export const TAG_HUES: Record<TagColorName, string> = {
  gray: '#6b7280',
  red: '#ef4444',
  orange: '#f97316',
  amber: '#f59e0b',
  green: '#22c55e',
  teal: '#14b8a6',
  blue: '#3b82f6',
  purple: '#a855f7',
  pink: '#ec4899',
  rose: '#f43f5e',
}

// The share of the hue in the chip's background, in percent; the CSS uses the
// same number and the contrast test reads it from here.
export const TAG_BG_PERCENT = 12

const HEX = /^#[0-9a-f]{6}$/i

// What a colour from the server is: a preset name, a custom `#rrggbb`
// (returned in lower case), or nothing usable, which is drawn as no colour.
export type ParsedTagColor = { kind: 'preset'; name: TagColorName } | { kind: 'custom'; hex: `#${string}` }

export function parseTagColor(v: unknown): ParsedTagColor | null {
  if (typeof v !== 'string') return null
  if ((TAG_COLORS as readonly string[]).includes(v)) return { kind: 'preset', name: v as TagColorName }
  if (HEX.test(v)) return { kind: 'custom', hex: v.toLowerCase() as `#${string}` }
  return null
}

export const isTagColor = (v: unknown): v is TagColor => parseTagColor(v) !== null

// What a person typed in a hex field: `#` optional, any case, six digits.
// Returns the lower-case `#rrggbb`, or null.
export function normalizeHex(input: string): `#${string}` | null {
  const s = input.trim()
  const hex = s.startsWith('#') ? s : `#${s}`
  return HEX.test(hex) ? (hex.toLowerCase() as `#${string}`) : null
}

// A tag with no colour is drawn plain. A custom colour shares the preset rule
// in index.css and brings its hue in `--tag-hue` (see tagColorStyle).
export function tagColorClass(color: TagColor | undefined): string {
  const p = parseTagColor(color)
  return p === null ? '' : p.kind === 'preset' ? `tag-${p.name}` : 'tag-custom'
}

export function tagColorStyle(color: TagColor | undefined): CSSProperties | undefined {
  const p = parseTagColor(color)
  return p?.kind === 'custom' ? ({ '--tag-hue': p.hex } as CSSProperties) : undefined
}
