// Images and attachments in the page editor: uploading them, showing them, and
// resolving the addresses they are written with.
//
// Uploads start from a paste of file data, a drop of files on the editor, or the
// toolbar. Each file shows a placeholder at the place it will go while it
// uploads. The placeholder is a decoration, not a node: nothing half-done is in
// the document, so autosave never writes one. When the upload is done the image
// (`![alt](path)`) or the link (`[original name](path)`) is inserted there, in
// the order the files were given; when it fails the placeholder goes away and
// the person is told.
//
// What counts as an image is what a browser can show (PNG, JPEG, GIF, WebP,
// AVIF, SVG, BMP, ICO). A HEIC or TIFF photo is a file: a link, not a broken
// picture.

import { Extension, type Editor } from '@tiptap/core'
import { Fragment, type Node as PMNode } from '@tiptap/pm/model'
import { Plugin, PluginKey } from '@tiptap/pm/state'
import { Decoration, DecorationSet } from '@tiptap/pm/view'
import type { UploadedAsset } from '@/api/types'
import { addressToUrl } from '@/lib/fileAddress'
import { failureText } from '@/lib/failure'
import { formatBytes } from '@/lib/bytes'
import { browserCodec, prepareImage, type ImageCodec } from '@/lib/imageCompress'
import { t } from '@/i18n'

export interface PageFilesOptions {
  space: string
  // Uploads belong to a page. Without an id (a page that is not created yet) there is nowhere to put them.
  pageId: string | null
  // The folder of the page now; it changes when the page is moved.
  getFolder: () => string
  // The server's limit in bytes, once known.
  getMaxUpload: () => number | undefined
  upload: (file: Blob, name: string) => Promise<UploadedAsset>
  // The "upload originals" preference, read for every file.
  getOriginal: () => boolean
  notify: (message: string) => void
  // Called right before an uploaded file is put into the document, so the
  // document can first be brought up to date with text typed elsewhere (the
  // source view): the file must land in that text, not replace it.
  beforeInsert?: () => void
  // Called right after, with the Markdown the file is written as.
  afterInsert?: (markdown: string) => void
  codec?: ImageCodec
}

// How the toolbar and the drop say what to make of a file:
//   auto   a displayable image is an image, anything else a link (paste, drop)
//   image  the same: the image button
//   file   always a link, and never recompressed: "attach file"
export type UploadMode = 'auto' | 'image' | 'file'

interface Pending {
  id: number
  pos: number
  name: string
  seq: number
}

interface Store {
  options: PageFilesOptions
  resolveUrl: (address: string) => string
  // Uploads of one editor run one after another.
  tail: Promise<void>
  counter: number
}

const key = new PluginKey<Pending[]>('pageFilesUploads')

const DISPLAYABLE = /^image\/(png|jpe?g|gif|webp|avif|svg\+xml|bmp|x-icon|vnd\.microsoft\.icon)$/i
const DISPLAYABLE_EXT = /\.(png|jpe?g|gif|webp|avif|svg|bmp|ico)$/i

export function isImageFile(file: { type: string; name: string }): boolean {
  return file.type ? DISPLAYABLE.test(file.type) : DISPLAYABLE_EXT.test(file.name)
}

// The clipboard carries files. Copying a picture in a browser or taking a
// screenshot is only that (or that with an `<img>` and no text): upload it.
// Copying from a spreadsheet or a document also puts a picture of the cells or
// pages on the clipboard next to rich text: then the text is what was meant.
export function clipboardMeansFiles(c: { html: string; text: string; fileCount: number }): boolean {
  if (c.fileCount === 0) return false
  if (c.html === '') return true
  return /<img\b/i.test(c.html) && c.text.trim() === ''
}

export const stemOf = (name: string) => name.replace(/\.[^./\\]+$/, '') || name

const store = (editor: Editor) => (editor.storage as unknown as Record<string, Store>).pageFiles

// Where an address written in this page leads, for the browser.
// What the "open" button of a link goes to. An address with a scheme is opened
// only if it is one that opens a page or a program to write to someone; anything
// else (`javascript:`, `data:` ...) goes nowhere.
const OPENABLE = /^(https?|mailto|tel):/i
export function resolveAddressIn(editor: Editor, address: string): string {
  const url = (editor.storage as unknown as Record<string, Store | undefined>).pageFiles?.resolveUrl(address) ?? address
  return /^[a-z][a-z0-9+.-]*:/i.test(url) && !OPENABLE.test(url) ? '#' : url
}

function placeholder(name: string): HTMLElement {
  const el = document.createElement('span')
  el.className = 'pb-upload-placeholder'
  el.contentEditable = 'false'
  el.setAttribute('role', 'status')
  el.textContent = t.files.uploading(name)
  return el
}

export function uploadFiles(editor: Editor, files: File[], opts: { mode?: UploadMode; pos?: number; replaceSelection?: boolean } = {}): void {
  const st = store(editor)
  const o = st.options
  if (!o.pageId) {
    o.notify(t.files.noPage)
    return
  }
  // Refused here, before anything is sent, with the limit in the message.
  const max = o.getMaxUpload()
  const accepted: File[] = []
  for (const f of files) {
    if (max !== undefined && f.size > max) o.notify(t.files.tooLarge(f.name, formatBytes(f.size), formatBytes(max)))
    else accepted.push(f)
  }
  if (!accepted.length || editor.isDestroyed) return

  const tr = editor.state.tr
  let at: number
  if (opts.pos !== undefined) at = opts.pos
  else if (opts.replaceSelection) {
    tr.deleteSelection()
    at = tr.selection.from
  } else at = editor.state.selection.to
  // Insert into the document as it will be after the selection is gone.
  at = Math.max(0, Math.min(at, tr.doc.content.size))
  const $at = tr.doc.resolve(at)
  if ($at.parent.type.spec.code) at = $at.after()

  const base = st.counter
  st.counter += accepted.length
  const items: Pending[] = accepted.map((f, i) => ({ id: base + i + 1, pos: at, name: f.name, seq: base + i + 1 }))
  editor.view.dispatch(tr.setMeta(key, { add: items }))

  const mode = opts.mode ?? 'auto'
  st.tail = st.tail.then(() => runBatch(editor, accepted, items, mode))
}

async function runBatch(editor: Editor, files: File[], items: Pending[], mode: UploadMode) {
  const o = store(editor).options
  let inserted = false
  for (let i = 0; i < files.length; i++) {
    if (editor.isDestroyed) return
    const file = files[i]
    const asImage = mode !== 'file' && isImageFile(file)
    try {
      // Only a picture inserted as a picture is recompressed.
      const prepared = asImage && !o.getOriginal() ? await prepareImage(file, o.codec ?? browserCodec) : { blob: file as Blob, name: file.name }
      const asset = await o.upload(prepared.blob, prepared.name)
      if (editor.isDestroyed) return
      insertUploaded(editor, items[i].id, asset, file.name, asImage, inserted)
      inserted = true
    } catch (e) {
      if (editor.isDestroyed) return
      editor.view.dispatch(editor.state.tr.setMeta(key, { remove: items[i].id }))
      o.notify(t.files.failed(file.name, failureText(e)))
    }
  }
}

function insertUploaded(editor: Editor, id: number, asset: UploadedAsset, originalName: string, asImage: boolean, afterAnother: boolean) {
  ;(editor.storage as unknown as { pageFiles?: Store }).pageFiles?.options.beforeInsert?.()
  const { state } = editor
  const item = key.getState(state)?.find((p) => p.id === id)
  if (!item) return
  const { schema } = state
  const inline: PMNode[] = []
  // Files of one batch go on separate lines, not run together.
  if (afterAnother) inline.push(schema.nodes.hardBreak.create())
  inline.push(asImage ? schema.nodes.image.create({ src: asset.path, alt: stemOf(originalName) }) : schema.text(originalName, [schema.marks.link.create({ href: asset.path })]))
  const tr = state.tr
  const $pos = tr.doc.resolve(Math.min(item.pos, tr.doc.content.size))
  const content = $pos.parent.inlineContent ? Fragment.from(inline) : Fragment.from(schema.nodes.paragraph.create(null, afterAnother ? inline.slice(1) : inline))
  tr.insert($pos.pos, content).setMeta(key, { remove: id })
  editor.view.dispatch(tr)
  const label = (asImage ? stemOf(originalName) : originalName).replace(/[\\[\]]/g, '\\$&')
  ;(editor.storage as unknown as { pageFiles?: Store }).pageFiles?.options.afterInsert?.(`${asImage ? '!' : ''}[${label}](<${asset.path}>)`)
}

export const PageFiles = Extension.create<PageFilesOptions>({
  name: 'pageFiles',

  addOptions() {
    return {
      space: '',
      pageId: null,
      getFolder: () => '',
      getMaxUpload: () => undefined,
      upload: () => Promise.reject(new Error('no upload')),
      getOriginal: () => false,
      notify: () => undefined,
    }
  },

  addStorage() {
    const options = this.options
    return {
      options,
      resolveUrl: (address: string) => addressToUrl(options.space, options.getFolder(), address),
      tail: Promise.resolve(),
      counter: 0,
    } satisfies Store
  },

  addProseMirrorPlugins() {
    const editor = this.editor
    return [
      new Plugin<Pending[]>({
        key,
        state: {
          init: () => [],
          apply(tr, items) {
            const meta = tr.getMeta(key) as { add?: Pending[]; remove?: number } | undefined
            let next = tr.docChanged ? items.map((p) => ({ ...p, pos: tr.mapping.map(p.pos, 1) })) : items
            if (meta?.add) next = [...next, ...meta.add]
            if (meta?.remove !== undefined) next = next.filter((p) => p.id !== meta.remove)
            return next
          },
        },
        props: {
          decorations(state) {
            const items = key.getState(state) ?? []
            if (!items.length) return null
            return DecorationSet.create(
              state.doc,
              // `side` keeps several placeholders at one position in the order of their files.
              items.map((p) => Decoration.widget(p.pos, () => placeholder(p.name), { side: p.seq, key: `upload-${p.id}`, ignoreSelection: true })),
            )
          },
          handlePaste(_view, event) {
            const data = event.clipboardData
            const files = data ? Array.from(data.files) : []
            if (!data || !clipboardMeansFiles({ html: data.getData('text/html'), text: data.getData('text/plain'), fileCount: files.length })) return false
            event.preventDefault()
            uploadFiles(editor, files, { replaceSelection: true })
            return true
          },
          handleDrop(view, event, _slice, moved) {
            // Something of the page itself being dragged elsewhere is not an upload.
            if (moved) return false
            const files = event.dataTransfer ? Array.from(event.dataTransfer.files) : []
            if (!files.length) return false
            const at = view.posAtCoords({ left: event.clientX, top: event.clientY })
            event.preventDefault()
            uploadFiles(editor, files, { pos: at?.pos })
            return true
          },
        },
      }),
    ]
  },
})
