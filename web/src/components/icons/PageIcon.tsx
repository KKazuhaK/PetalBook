import type { ReactNode } from 'react'
import { iconColorVar } from '@/lib/icons/colors'
import { CORE_ICONS } from '@/lib/icons/coreIcons'
import type { LucideIcon } from 'lucide-react'
import { isIconName, isTextIcon, useFullIcons } from '@/lib/icons/useIcons'

// The icon of a folder or a page: a Lucide name or an emoji, in one of the eight colours (currentColor otherwise, so it
// follows the theme). `fallback` is drawn when there is no icon, and while the full set loads for an uncommon one. A
// name the set does not have, or text that is not one emoji (a file written by hand), is no icon.
export function PageIcon({
  icon,
  color,
  fallback,
  className = 'size-4',
}: {
  icon?: string
  color?: string
  fallback?: ReactNode
  className?: string
}) {
  const name = icon && isIconName(icon) ? icon : ''
  const full = useFullIcons(name)
  const set: Readonly<Record<string, LucideIcon>> = full ? full.ALL_ICONS : CORE_ICONS
  // Its own keys only: `constructor` is a key of every object.
  const Lucide = name && Object.hasOwn(set, name) ? set[name] : undefined
  const style = { color: iconColorVar(color) }
  if (icon && !name && isTextIcon(icon))
    return (
      <span
        data-icon={icon}
        aria-hidden="true"
        className={`inline-flex shrink-0 items-center justify-center leading-none ${className}`}
        style={{ ...style, fontSize: '1em' }}
      >
        <span className="text-[0.95em]">{icon}</span>
      </span>
    )
  if (Lucide) return <Lucide data-icon={icon} aria-hidden="true" className={`shrink-0 ${className}`} style={style} />
  // Still loading: room for it, so nothing moves when it comes.
  if (name && !full) return <span aria-hidden="true" className={`inline-block shrink-0 ${className}`} />
  if (!fallback) return null
  return (
    <span style={style} className="inline-flex shrink-0">
      {fallback}
    </span>
  )
}
