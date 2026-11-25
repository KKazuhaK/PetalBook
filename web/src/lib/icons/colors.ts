import type { IconColor } from '@/api/types'

// The eight colours of an icon, by name. How each looks is in index.css (`--icon-*`, a light and a dark shade).
export const ICON_COLORS: readonly IconColor[] = ['red', 'orange', 'amber', 'green', 'teal', 'blue', 'purple', 'pink']
export const isIconColor = (v: unknown): v is IconColor =>
  typeof v === 'string' && (ICON_COLORS as readonly string[]).includes(v)

// The CSS colour of a name; nothing for what is not one (the icon then has the colour of the text).
export const iconColorVar = (c: unknown): string | undefined => (isIconColor(c) ? `var(--icon-${c})` : undefined)
