import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { useAppearance, useSetFolderStyle, useSpaceApi, useSpaceId, invalidateListings } from '@/api/hooks'
import type { IconColor } from '@/api/types'
import { IconPicker } from '@/components/icons/IconPicker'
import { baseName, type FileItem } from '@/lib/folders'
import { announcePageSaved } from '@/lib/pageEvents'
import { t } from '@/i18n'

// "Icon and colour" of one folder (kept in space.yml) or one page (kept in its header).
export default function StyleDialog({ item, onClose }: { item: FileItem; onClose: () => void }) {
  return item.kind === 'folder' ? (
    <FolderStyle path={item.path} onClose={onClose} />
  ) : (
    <PageStyle id={item.id} label={item.label} onClose={onClose} />
  )
}

function FolderStyle({ path, onClose }: { path: string; onClose: () => void }) {
  const appearance = useAppearance()
  const set = useSetFolderStyle()
  const cur = appearance.data?.folders.find((f) => f.folder === path)
  if (appearance.isPending) return null
  return (
    <IconPicker
      open
      onOpenChange={(o) => !o && onClose()}
      description={t.icons.forFolder(baseName(path))}
      icon={cur?.icon ?? ''}
      color={cur?.color ?? ''}
      onSave={(icon, color) => set.mutateAsync({ folder: path, icon, color })}
    />
  )
}

function PageStyle({ id, label, onClose }: { id: string; label: string; onClose: () => void }) {
  const space = useSpaceId()
  const api = useSpaceApi()
  const qc = useQueryClient()
  const page = useQuery({
    queryKey: ['space', space, 'style-page', id],
    queryFn: ({ signal }) => api.getPage(id, signal),
    gcTime: 0,
  })
  if (page.isPending) return null
  if (page.isError) {
    toast.error(t.icons.failed)
    onClose()
    return null
  }
  const meta = page.data.meta
  return (
    <IconPicker
      open
      onOpenChange={(o) => !o && onClose()}
      description={t.icons.forPage(label)}
      icon={typeof meta.icon === 'string' ? meta.icon : ''}
      color={typeof meta.color === 'string' ? meta.color : ''}
      onSave={async (icon: string, color: IconColor | '') => {
        const next = { ...meta }
        if (icon) next.icon = icon
        else delete next.icon
        if (color) next.color = color
        else delete next.color
        const saved = await api.savePage(id, { baseRev: page.data.rev, meta: next, body: page.data.body })
        announcePageSaved(space, saved)
        await invalidateListings(qc, space)
        await qc.invalidateQueries({ queryKey: ['space', space, 'page'] })
      }}
    />
  )
}
