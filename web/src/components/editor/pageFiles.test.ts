import { afterEach, describe, expect, it } from 'vitest'
import type { Editor } from '@tiptap/core'
import { makeEditor, paste } from '@/markdown/testTyping'
import { docToMarkdown, markdownToDoc } from '@/markdown'
import type { UploadedAsset } from '@/api/types'
import { ApiError } from '@/api/client'
import { createPageExtensions } from './pageExtensions'
import { clipboardMeansFiles, isImageFile, resolveAddressIn, stemOf, uploadFiles, type PageFilesOptions } from './pageFiles'

let editor: Editor
afterEach(() => editor?.destroy())

const file = (name: string, type: string, size = 10) => new File([new Uint8Array(size)], name, { type })

interface Harness {
  uploads: { name: string; size: number }[]
  errors: string[]
  finish: (i: number, asset?: UploadedAsset) => void
  fail: (i: number, e?: unknown) => void
}
// Uploads that wait until the test says they are done, so the placeholder can be looked at.
function setup(over: Partial<PageFilesOptions> = {}, content?: Parameters<typeof makeEditor>[0]): Harness {
  const h: Harness = { uploads: [], errors: [], finish: () => undefined, fail: () => undefined }
  const waiting: { resolve: (a: UploadedAsset) => void; reject: (e: unknown) => void }[] = []
  h.finish = (i, asset) => waiting[i].resolve(asset ?? { path: `/assets/01TESTPAGE/${h.uploads[i].name}`, name: h.uploads[i].name, size: h.uploads[i].size })
  h.fail = (i, e = new Error('boom')) => waiting[i].reject(e)
  const options: PageFilesOptions = {
    space: 'default',
    pageId: '01TESTPAGE',
    getFolder: () => 'notes',
    getMaxUpload: () => 1000,
    getOriginal: () => true,
    notify: (m) => h.errors.push(m),
    upload: (blob, name) =>
      new Promise((resolve, reject) => {
        h.uploads.push({ name, size: blob.size })
        waiting.push({ resolve, reject })
      }),
    ...over,
  }
  editor = makeEditor(content, createPageExtensions(options))
  editor.commands.focus('end')
  return h
}
const tick = () => new Promise((r) => setTimeout(r, 0))
const placeholders = () => Array.from(editor.view.dom.querySelectorAll('.pb-upload-placeholder')).map((e) => e.textContent)
const md = () => docToMarkdown(editor.getJSON()).trim()
const p = (...content: object[]) => ({ type: 'paragraph', content })
const t0 = (text: string) => ({ type: 'text', text })

describe('what is an image', () => {
  it('what a browser can show', () => {
    expect(isImageFile(file('a.png', 'image/png'))).toBe(true)
    expect(isImageFile(file('a.svg', 'image/svg+xml'))).toBe(true)
    expect(isImageFile(file('a.jpg', ''))).toBe(true)
    expect(isImageFile(file('a.heic', 'image/heic'))).toBe(false)
    expect(isImageFile(file('a.pdf', 'application/pdf'))).toBe(false)
    expect(isImageFile(file('a.txt', ''))).toBe(false)
  })
  it('alt text from the name', () => {
    expect(stemOf('街景.final.jpg')).toBe('街景.final')
    expect(stemOf('noext')).toBe('noext')
    expect(stemOf('.hidden')).toBe('.hidden')
  })
  it('which clipboards are uploads', () => {
    expect(clipboardMeansFiles({ html: '', text: '', fileCount: 1 })).toBe(true)
    expect(clipboardMeansFiles({ html: '<img src="x">', text: '', fileCount: 1 })).toBe(true)
    expect(clipboardMeansFiles({ html: '<table><tr><td>1</td></tr></table>', text: '1', fileCount: 1 })).toBe(false)
    expect(clipboardMeansFiles({ html: '<img src="x"><p>caption</p>', text: 'caption', fileCount: 1 })).toBe(false)
    expect(clipboardMeansFiles({ html: '', text: '', fileCount: 0 })).toBe(false)
  })
})

describe('upload flow', () => {
  it('an image: placeholder while uploading, then the image with the stored path and the file name as alt', async () => {
    const h = setup({}, { type: 'doc', content: [p(t0('前后'))] })
    editor.commands.setTextSelection(2)
    uploadFiles(editor, [file('街景.png', 'image/png')])
    expect(placeholders()).toEqual(['正在上传 街景.png…'])
    // Nothing half-done is in the document.
    expect(md()).toBe('前后')
    await tick()
    h.finish(0, { path: '/assets/01TESTPAGE/街景-3f9a1c2b.png', name: '街景-3f9a1c2b.png', size: 10 })
    await tick()
    expect(placeholders()).toEqual([])
    expect(md()).toBe('前![街景](/assets/01TESTPAGE/街景-3f9a1c2b.png)后')
  })
  it('another file: a link whose text is the original name', async () => {
    const h = setup()
    uploadFiles(editor, [file('合同 终版.pdf', 'application/pdf')])
    await tick()
    h.finish(0, { path: '/assets/01TESTPAGE/合同 终版-91be02aa.pdf', name: '合同 终版-91be02aa.pdf', size: 10 })
    await tick()
    expect(editor.getJSON().content![0].content).toEqual([{ type: 'text', text: '合同 终版.pdf', marks: [{ type: 'link', attrs: expect.objectContaining({ href: '/assets/01TESTPAGE/合同 终版-91be02aa.pdf' }) }] }])
    expect(md()).toContain('[合同 终版.pdf](</assets/01TESTPAGE/合同 终版-91be02aa.pdf>)')
  })
  it('several files go up one after another and are inserted in order, one per line', async () => {
    const h = setup()
    uploadFiles(editor, [file('a.png', 'image/png'), file('b.png', 'image/png'), file('c.pdf', 'application/pdf')])
    expect(placeholders()).toEqual(['正在上传 a.png…', '正在上传 b.png…', '正在上传 c.pdf…'])
    await tick()
    expect(h.uploads.map((u) => u.name)).toEqual(['a.png'])
    h.finish(0)
    await tick()
    // The second is only sent when the first is done; the first is already in place.
    expect(h.uploads.map((u) => u.name)).toEqual(['a.png', 'b.png'])
    expect(placeholders()).toEqual(['正在上传 b.png…', '正在上传 c.pdf…'])
    expect(md()).toBe('![a](/assets/01TESTPAGE/a.png)')
    h.finish(1)
    await tick()
    h.finish(2)
    await tick()
    expect(placeholders()).toEqual([])
    expect(md()).toBe('![a](/assets/01TESTPAGE/a.png)\\\n![b](/assets/01TESTPAGE/b.png)\\\n[c.pdf](/assets/01TESTPAGE/c.pdf)')
  })
  it('a failure removes its placeholder, says why, and the others carry on', async () => {
    const h = setup()
    uploadFiles(editor, [file('a.png', 'image/png'), file('b.png', 'image/png')])
    await tick()
    h.fail(0, new ApiError(500, 'internal', '服务器出错了'))
    await tick()
    expect(h.errors).toEqual(['「a.png」上传失败：服务器出错了'])
    expect(placeholders()).toEqual(['正在上传 b.png…'])
    h.finish(1)
    await tick()
    expect(md()).toBe('![b](/assets/01TESTPAGE/b.png)')
  })
  it('the placeholder follows the text around it', async () => {
    const h = setup({}, { type: 'doc', content: [p(t0('后面'))] })
    editor.commands.setTextSelection(1)
    uploadFiles(editor, [file('a.png', 'image/png')])
    editor.commands.insertContentAt(1, '前面的字')
    await tick()
    h.finish(0)
    await tick()
    // The image went where the cursor was, after the text typed in front of it.
    expect(md()).toBe('前面的字![a](/assets/01TESTPAGE/a.png)后面')
  })
  it('refuses a file over the limit before sending, naming the limit', async () => {
    const h = setup({ getMaxUpload: () => 5 * 1024 * 1024 })
    uploadFiles(editor, [file('big.png', 'image/png', 6 * 1024 * 1024)])
    expect(h.errors).toEqual(['「big.png」有 6 MB，超过了 5 MB 的上传上限，没有上传。'])
    expect(placeholders()).toEqual([])
    await tick()
    expect(h.uploads).toEqual([])
  })
  it('only the files over the limit are refused', async () => {
    const h = setup({ getMaxUpload: () => 100 })
    uploadFiles(editor, [file('big.png', 'image/png', 101), file('ok.png', 'image/png', 100)])
    expect(h.errors).toHaveLength(1)
    await tick()
    expect(h.uploads.map((u) => u.name)).toEqual(['ok.png'])
  })
  it('no page id, no upload', () => {
    const h = setup({ pageId: null })
    uploadFiles(editor, [file('a.png', 'image/png')])
    expect(h.errors).toEqual(['这个页面还不能上传文件。'])
    expect(placeholders()).toEqual([])
  })
  it('"file" mode makes a link even of a picture, and sends it untouched', async () => {
    const h = setup()
    uploadFiles(editor, [file('a.png', 'image/png')], { mode: 'file' })
    await tick()
    h.finish(0)
    await tick()
    expect(md()).toBe('[a.png](/assets/01TESTPAGE/a.png)')
  })
  it('in a code block the file goes after the block', async () => {
    const h = setup({}, { type: 'doc', content: [{ type: 'codeBlock', content: [t0('code')] }] })
    editor.commands.focus('end')
    uploadFiles(editor, [file('a.png', 'image/png')])
    await tick()
    h.finish(0)
    await tick()
    expect(editor.getJSON().content!.map((n) => n.type)).toEqual(['codeBlock', 'paragraph'])
    expect(md()).toContain('![a](/assets/01TESTPAGE/a.png)')
  })
  it('a pasted file replaces the selection', async () => {
    const h = setup({}, { type: 'doc', content: [p(t0('abc'))] })
    editor.commands.setTextSelection({ from: 2, to: 3 })
    paste(editor, { files: [file('a.png', 'image/png')] })
    await tick()
    h.finish(0)
    await tick()
    expect(md()).toBe('a![a](/assets/01TESTPAGE/a.png)c')
  })
})

describe('paste and drop', () => {
  it('pasting image data uploads it and does not paste anything else', async () => {
    const h = setup()
    const event = paste(editor, { files: [file('image.png', 'image/png')] })
    expect(event.defaultPrevented).toBe(true)
    await tick()
    expect(h.uploads.map((u) => u.name)).toEqual(['image.png'])
  })
  it('rich text that comes with a picture of itself is pasted as text', async () => {
    const h = setup()
    paste(editor, { html: '<p>rich</p>', text: 'rich', files: [file('image.png', 'image/png')] })
    await tick()
    expect(h.uploads).toEqual([])
    expect(md()).toBe('rich')
  })
  it('dropping files from outside uploads them at the drop position', async () => {
    const h = setup({}, { type: 'doc', content: [p(t0('一二三'))] })
    editor.view.posAtCoords = () => ({ pos: 3, inside: -1 })
    const event = new Event('drop', { bubbles: true, cancelable: true })
    Object.defineProperty(event, 'dataTransfer', { value: { files: [file('a.png', 'image/png')], getData: () => '', types: ['Files'] } })
    editor.view.dom.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(true)
    await tick()
    h.finish(0)
    await tick()
    expect(md()).toBe('一二![a](/assets/01TESTPAGE/a.png)三')
  })
  it('a drop with no files is left to the editor', () => {
    setup()
    editor.view.posAtCoords = () => ({ pos: 1, inside: -1 })
    const event = new Event('drop', { bubbles: true, cancelable: true })
    Object.defineProperty(event, 'dataTransfer', { value: { files: [], getData: () => '', types: [] } })
    editor.view.dom.dispatchEvent(event)
    expect(placeholders()).toEqual([])
  })
})

describe('addresses', () => {
  it('the page keeps the address as written; only the drawn image is mapped', () => {
    setup({ getFolder: () => 'notes/2026' }, markdownToDoc('![x](/assets/01TESTPAGE/x.jpg)\n\n![y](pic/y.png)\n\n![z](https://example.com/z.png)\n'))
    const srcs = Array.from(editor.view.dom.querySelectorAll('img:not(.ProseMirror-separator)')).map((i) => i.getAttribute('src'))
    expect(srcs).toEqual(['/api/spaces/default/files/assets/01TESTPAGE/x.jpg', '/api/spaces/default/files/notes/2026/pic/y.png', 'https://example.com/z.png'])
    expect(md()).toBe('![x](/assets/01TESTPAGE/x.jpg)\n\n![y](pic/y.png)\n\n![z](https://example.com/z.png)')
    // What a copy would carry is the stored address too.
    expect(editor.getHTML()).toContain('src="/assets/01TESTPAGE/x.jpg"')
  })
  it('resolveAddressIn maps for the link popover', () => {
    setup({ getFolder: () => 'notes' })
    expect(resolveAddressIn(editor, '/assets/a/b.pdf')).toBe('/api/spaces/default/files/assets/a/b.pdf')
    expect(resolveAddressIn(editor, 'https://example.com')).toBe('https://example.com')
  })
  it('an image whose alt or address changes is redrawn', () => {
    setup({}, markdownToDoc('![x](/assets/a/x.jpg)'))
    editor.commands.setNodeSelection(1)
    editor.commands.updateAttributes('image', { alt: '新说明' })
    expect(editor.view.dom.querySelector('img:not(.ProseMirror-separator)')!.getAttribute('alt')).toBe('新说明')
  })
})
