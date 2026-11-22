import { Fragment } from 'react'
import { Link } from 'react-router'
import { ChevronRight, MoreHorizontal } from 'lucide-react'
import { useUrls } from '@/api/hooks'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { useIsMobile } from '@/hooks/use-mobile'
import { collapseCrumbs, crumbsOf, type Crumb } from '@/lib/folders'
import { t } from '@/i18n'
import { useFileOps } from './FileOps'
import { useDropTarget } from './useDropTarget'

// Space › folder › subfolder, each a link and each a place to drop files onto.
// A long trail folds its middle into "…"; on a phone only the last two stay.
export function Breadcrumbs({ folder, currentLast = true }: { folder: string; currentLast?: boolean }) {
  const ops = useFileOps()
  const mobile = useIsMobile()
  const spaceName = ops?.spaceName ?? t.nav.rootFolder
  const all = crumbsOf(folder, spaceName)
  const { start, hidden, end } = mobile ? collapseCrumbs(all, 0, 2, 1) : collapseCrumbs(all, 1, 2)
  const last = all[all.length - 1].path
  const show = (c: Crumb) => <CrumbLink key={c.path} crumb={c} current={currentLast && c.path === last} />

  return (
    <nav aria-label={t.manage.trail} className="min-w-0">
      <ol className="flex min-w-0 items-center gap-0.5 text-sm">
        {start.map((c, i) => (
          <Fragment key={c.path}>
            {i > 0 && <Sep />}
            {show(c)}
          </Fragment>
        ))}
        {hidden.length > 0 && (
          <>
            {start.length > 0 && <Sep />}
            <li className="shrink-0">
              <DropdownMenu>
                <DropdownMenuTrigger render={<button type="button" aria-label={t.manage.trailMore} className="flex size-6 items-center justify-center rounded-md text-muted-foreground outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50" />}>
                  <MoreHorizontal className="size-4" />
                </DropdownMenuTrigger>
                <DropdownMenuContent className="w-auto min-w-40">
                  {hidden.map((c) => (
                    <DropdownMenuItem key={c.path} render={<HiddenLink crumb={c} />} />
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
            </li>
          </>
        )}
        {end.map((c) => (
          <Fragment key={c.path}>
            <Sep />
            {show(c)}
          </Fragment>
        ))}
      </ol>
    </nav>
  )
}

const Sep = () => (
  <li aria-hidden className="shrink-0 text-muted-foreground/60">
    <ChevronRight className="size-3.5" />
  </li>
)

function HiddenLink({ crumb, ...props }: { crumb: Crumb }) {
  const urls = useUrls()
  return (
    <Link to={urls.folder(crumb.path)} {...props}>
      {crumb.name}
    </Link>
  )
}

function CrumbLink({ crumb, current }: { crumb: Crumb; current: boolean }) {
  const urls = useUrls()
  const ops = useFileOps()
  const drop = useDropTarget(crumb.path, ops?.index, (items, target) => void ops?.moveTo(items, target))
  return (
    <li className={`min-w-0 ${current ? 'shrink-0' : 'shrink'}`} {...drop.props}>
      <Link
        to={urls.folder(crumb.path)}
        aria-current={current ? 'page' : undefined}
        className={`block max-w-40 truncate rounded-md px-1.5 py-0.5 outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 sm:max-w-56 ${drop.over ? 'bg-primary/15 text-primary ring-2 ring-primary/40' : 'hover:bg-muted'} ${current ? 'font-medium text-foreground' : 'text-muted-foreground hover:text-foreground'}`}
      >
        {crumb.name}
      </Link>
    </li>
  )
}
