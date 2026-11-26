import { Link, Navigate, useLocation } from 'react-router'
import { useRef } from 'react'
import { scrollFadeClass, useScrollFade } from '@/components/ui/scroll-fade'
import { useSession } from '@/api/hooks'
import { useDocumentTitle } from '@/lib/useDocumentTitle'
import { adminPath } from '@/lib/urls'
import { t } from '@/i18n'
import { tc } from '@/i18n/console'
import { AdminLayout } from './AdminLayout'
import { AdminPageHeader } from './AdminPageHeader'
import { navFor } from './adminNav'
import { PersonPage } from './users/PersonPage'

// The administration: the sidebar's sections, each with the pages along its top, and a page of its own for each person.
// It is a route of its own, loaded only when an administrator goes there (see the gate in App.tsx), and everything on
// it is an endpoint only administrators may call. The address is `/admin/<section>/<tab>` or `/admin/user/<id>/<tab>`;
// the older `/admin/manage`, `/admin/recycle`, `/admin/access` and `/admin/usage` open in their new place.
export default function AdminRoute() {
  const fadeRef = useRef<HTMLElement>(null)
  useScrollFade(fadeRef)
  const { pathname } = useLocation()
  const [first, second, third] = pathname
    .split('/')
    .filter(Boolean)
    .slice(1)
    .map((s) => decodeURIComponent(s))
  const site = useSession().data?.user?.siteAdmin !== false
  const sections = navFor(site)
  const person = first === 'user' && second !== undefined
  const section = person ? sections.find((s) => s.key === 'users') : sections.find((s) => s.key === first)
  const tabs = section?.tabs ?? []
  const tab = person ? undefined : (tabs.find((x) => x.key === second) ?? tabs[0])
  useDocumentTitle(
    `${(person ? section?.label : tab && tabs.length > 1 ? `${tab.label} · ${section?.label}` : section?.label) ?? tc.title} · ${tc.title} · ${t.appName}`,
  )

  if (!section) return <Navigate to={adminPath('users')} replace />
  if (!person && second !== undefined && tab?.key !== second) return <Navigate to={adminPath(section.key)} replace />
  if (!person && third !== undefined) return <Navigate to={adminPath(section.key, tab?.key)} replace />

  return (
    <AdminLayout sections={sections} active={section.key}>
      {person ? (
        <PersonPage id={second} tab={third} />
      ) : (
        <AdminPageHeader
          title={section.label}
          description={(tc.pageHelp as Record<string, string>)[`${section.key}/${tab?.key}`]}
        >
          {tabs.length > 1 && (
            <nav ref={fadeRef} aria-label={tc.tabsLabel} className={`${scrollFadeClass} -mx-4 px-4 md:mx-0 md:px-0`}>
              <ul className="flex min-w-max gap-1 border-b">
                {tabs.map((x) => (
                  <li key={x.key}>
                    <Link
                      to={adminPath(section.key, x.key)}
                      aria-current={x === tab ? 'page' : undefined}
                      className={`relative flex h-11 items-center px-3 text-sm whitespace-nowrap outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 ${x === tab ? 'font-medium text-foreground after:absolute after:inset-x-2 after:bottom-0 after:h-0.5 after:rounded-full after:bg-brand' : 'text-muted-foreground hover:text-foreground'}`}
                    >
                      {x.label}
                    </Link>
                  </li>
                ))}
              </ul>
            </nav>
          )}
          <div className="min-w-0 animate-in duration-150 fade-in-0" key={`${section.key}/${tab?.key}`}>
            {tab?.render()}
          </div>
        </AdminPageHeader>
      )}
    </AdminLayout>
  )
}
