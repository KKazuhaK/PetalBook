import type { ReactNode } from 'react'
import { cn } from 'cn'
import { useTagColors } from '@/api/hooks'
import type { TagColor } from '@/api/types'
import { tagColorClass, tagColorStyle } from '@/lib/tagColors'

// The classes of a tag drawn as a pill: tinted with its colour when it has one
// (see the `tag-*` rules at the end of index.css), a quiet outline when not.
// Exported so a link that is a tag (the sidebar's) can wear the same look.
export function tagPillClass(color: TagColor | undefined, extra?: string) {
  const cls = tagColorClass(color)
  return cn('inline-flex max-w-full items-center gap-1 rounded-full px-2 py-0.5 text-xs leading-tight', cls || 'border bg-secondary/60 text-secondary-foreground', extra)
}

// The inline style a custom colour needs next to tagPillClass (its hue); none for presets.
export const tagPillStyle = tagColorStyle

// A tag as a small pill. Without `color` it looks the tag up in the space's
// colours (the same data the sidebar already loaded, so nothing extra is
// asked); pass `color` to say it yourself, or `null` to force no colour. The
// editor's tag input can adopt it as it is.
export function TagChip({ tag, color, count, className, children }: { tag: string; color?: TagColor | null; count?: number; className?: string; children?: ReactNode }) {
  const colors = useTagColors()
  const resolved = color === null ? undefined : (color ?? colors.get(tag))
  return (
    <span className={tagPillClass(resolved, className)} style={tagPillStyle(resolved)} data-tag-color={resolved}>
      <span className="truncate">{tag}</span>
      {count !== undefined && <span className="tabular-nums opacity-70">{count}</span>}
      {children}
    </span>
  )
}
