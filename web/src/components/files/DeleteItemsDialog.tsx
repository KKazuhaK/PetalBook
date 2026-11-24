import { useState } from 'react'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { itemName, type FileItem } from '@/lib/folders'
import { t } from '@/i18n'

// `pages` is how many pages the selected folders hold (subfolders included):
// they are deleted with the folder, so the person is told how many.
export function DeleteItemsDialog({
  items,
  pages,
  onDelete,
  onClose,
}: {
  items: FileItem[]
  pages: number
  onDelete: () => Promise<void>
  onClose: () => void
}) {
  const [busy, setBusy] = useState(false)
  const single = items.length === 1 ? items[0] : null
  const title = single
    ? t.manage.deleteTitleOne(itemName(single) || t.list.blankPage)
    : t.manage.deleteTitleMany(items.length)
  const hasFolder = items.some((i) => i.kind === 'folder')
  const holds = !hasFolder
    ? null
    : single
      ? pages === 0
        ? t.manage.deleteFolderEmpty
        : t.manage.deleteFolderPages(pages)
      : pages > 0
        ? t.manage.deleteFoldersHold(pages)
        : null

  return (
    <AlertDialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>
            {holds && <span className="block">{holds}</span>}
            <span className="block">{t.manage.deleteKept}</span>
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>{t.manage.cancel}</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={busy}
            onClick={() => {
              setBusy(true)
              void onDelete().finally(onClose)
            }}
          >
            {t.manage.deleteConfirm}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
