import { useRef } from 'react'
import { Link } from 'react-router'
import { scrollFadeClass, useScrollFade } from '@/components/ui/scroll-fade'
import { tc } from '@/i18n/console'

// The pages of a section (or of a tenant) along the top of an administration page: a horizontal strip that scrolls
// without a scrollbar and fades at the edges that have more.
export function AdminTabs({
  tabs,
  active,
  label = tc.tabsLabel,
}: {
  tabs: { key: string; label: string; to: string }[]
  active: string | undefined
  label?: string
}) {
  const fadeRef = useRef<HTMLElement>(null)
  useScrollFade(fadeRef)
  return (
    <nav ref={fadeRef} aria-label={label} className={`${scrollFadeClass} -mx-4 px-4 md:mx-0 md:px-0`}>
      <ul className="flex min-w-max gap-1 border-b">
        {tabs.map((x) => (
          <li key={x.key}>
            <Link
              to={x.to}
              aria-current={x.key === active ? 'page' : undefined}
              className={`relative flex h-11 items-center px-3 text-sm whitespace-nowrap outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 ${x.key === active ? 'font-medium text-foreground after:absolute after:inset-x-2 after:bottom-0 after:h-0.5 after:rounded-full after:bg-brand' : 'text-muted-foreground hover:text-foreground'}`}
            >
              {x.label}
            </Link>
          </li>
        ))}
      </ul>
    </nav>
  )
}
