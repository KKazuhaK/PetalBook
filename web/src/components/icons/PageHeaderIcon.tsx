import { SmilePlus } from 'lucide-react'
import { useSpacePermissions } from '@/api/hooks'
import { useFileOps } from '@/components/files/FileOps'
import { usePageStyle } from '@/lib/icons/useStyles'
import { t } from '@/i18n'
import { PageIcon } from './PageIcon'

// The icon at the head of a page: the page's own, else its type's. Where the person may write, it opens the picker, and
// a page with no icon has a quiet button that appears when the pointer is near (always on a touch screen).
export function PageHeaderIcon({
  id,
  folder,
  label,
  icon,
  color,
  effectiveType,
}: {
  id: string
  folder: string
  label: string
  icon?: string
  color?: string
  effectiveType?: string
}) {
  const ops = useFileOps()
  const { write } = useSpacePermissions()
  const style = usePageStyle()({ icon, color, effectiveType })
  const open = () => ops?.setStyle({ kind: 'page', id, folder, label })
  if (style.icon) {
    const glyph = <PageIcon icon={style.icon} color={style.color} className="size-9 text-3xl" />
    return write && ops ? (
      <button
        type="button"
        onClick={open}
        aria-label={t.icons.menu}
        title={t.icons.menu}
        className="mb-2 flex size-12 items-center justify-center rounded-lg outline-none transition-colors hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        {glyph}
      </button>
    ) : (
      <div className="mb-2 flex size-12 items-center justify-center">{glyph}</div>
    )
  }
  if (!write || !ops) return null
  return (
    <button
      type="button"
      onClick={open}
      className="mb-1 inline-flex h-8 items-center gap-1.5 rounded-md px-2 text-sm text-muted-foreground opacity-0 transition-opacity outline-none hover:bg-muted hover:text-foreground focus-visible:opacity-100 pointer-coarse:opacity-70 [.group\/title:hover_&]:opacity-100"
    >
      <SmilePlus className="size-4" aria-hidden />
      {t.icons.menu}
    </button>
  )
}
