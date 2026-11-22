import { useRef, useState } from 'react'
import type { Editor } from '@tiptap/core'
import { ImagePlus, Paperclip } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuCheckboxItem, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { getUploadOriginal, setUploadOriginal } from '@/lib/uploadPrefs'
import { t } from '@/i18n'
import { uploadFiles, type UploadMode } from './pageFiles'

// Two toolbar buttons that open a file picker: pictures (with the choice of
// uploading them as they are) and any file. The editor keeps its selection
// while the picker is open, so the files go where the cursor was.
export function UploadButtons({ editor }: { editor: Editor }) {
  const images = useRef<HTMLInputElement>(null)
  const files = useRef<HTMLInputElement>(null)
  const [original, setOriginal] = useState(getUploadOriginal)

  const chosen = (input: HTMLInputElement, mode: UploadMode) => {
    const list = Array.from(input.files ?? [])
    // The same file can be chosen again afterwards.
    input.value = ''
    if (list.length) uploadFiles(editor, list, { mode })
  }

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={<Button type="button" variant="ghost" size="icon-sm" title={t.files.imageMenu} aria-label={t.files.imageMenu} className="text-muted-foreground" onMouseDown={(e) => e.preventDefault()} />}
        >
          <ImagePlus />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="min-w-44">
          <DropdownMenuItem onClick={() => images.current?.click()}>
            <ImagePlus />
            {t.files.chooseImage}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuCheckboxItem
            checked={original}
            onCheckedChange={(on) => {
              setOriginal(on)
              setUploadOriginal(on)
            }}
          >
            {t.files.keepOriginal}
          </DropdownMenuCheckboxItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <Button type="button" variant="ghost" size="icon-sm" title={t.files.attach} aria-label={t.files.attach} className="text-muted-foreground" onMouseDown={(e) => e.preventDefault()} onClick={() => files.current?.click()}>
        <Paperclip />
      </Button>
      <input ref={images} type="file" accept="image/*" multiple hidden tabIndex={-1} aria-hidden onChange={(e) => chosen(e.currentTarget, 'image')} />
      <input ref={files} type="file" multiple hidden tabIndex={-1} aria-hidden onChange={(e) => chosen(e.currentTarget, 'file')} />
    </>
  )
}
