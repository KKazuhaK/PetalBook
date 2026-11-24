import { act } from 'react'
import { describe, expect, it } from 'vitest'
import { createRoot } from 'react-dom/client'
import { NodeSelection } from '@tiptap/pm/state'
import { useEditorState } from '@tiptap/react'
import type { Editor } from '@tiptap/core'
import { ImageFields } from './BubbleToolbar'
import { markdownOf, pageEditor } from './pageTestKit'

const PAGE = '![旧说明](/assets/01HX/x.png)\n'

function selectImage(editor: Editor) {
  editor.view.dispatch(editor.state.tr.setSelection(NodeSelection.create(editor.state.doc, 1)))
}
const attrs = (editor: Editor) =>
  (editor.getJSON().content![0].content![0] as { attrs: { src: string; alt: string | null } }).attrs

// The fields as the popover mounts them: values from the document.
function Fields({ editor }: { editor: Editor }) {
  const { alt, src } = useEditorState({
    editor,
    selector: ({ editor: e }) => ({
      alt: (e.getAttributes('image').alt as string) ?? '',
      src: (e.getAttributes('image').src as string) ?? '',
    }),
  })
  return <ImageFields editor={editor} alt={alt} src={src} />
}

async function open(markdown = PAGE) {
  const editor = pageEditor(markdown)
  selectImage(editor)
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  await act(async () => root.render(<Fields editor={editor} />))
  const input = (label: string) => host.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)!
  // React listens to the native input event; the value has to be set through the prototype setter.
  const type = async (el: HTMLInputElement, value: string) => {
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(el, value)
      el.dispatchEvent(new Event('input', { bubbles: true }))
    })
  }
  const key = (el: HTMLInputElement, k: string) =>
    act(async () => void el.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true })))
  const blur = (el: HTMLInputElement) =>
    act(async () => void el.dispatchEvent(new FocusEvent('focusout', { bubbles: true })))
  return { editor, host, input, type, key, blur, done: () => act(async () => root.unmount()).then(() => host.remove()) }
}

describe('the image popover fields', () => {
  it('show the description and the address written in the page, not the mapped URL', async () => {
    const f = await open()
    expect(f.input('图片说明（替代文字）').value).toBe('旧说明')
    expect(f.input('地址').value).toBe('/assets/01HX/x.png')
    expect(f.host.textContent).toContain('说明')
    expect(f.host.textContent).toContain('地址')
    await f.done()
  })

  it('apply the description on Enter, into the Markdown', async () => {
    const f = await open()
    await f.type(f.input('图片说明（替代文字）'), '新的说明')
    expect(attrs(f.editor).alt).toBe('旧说明')
    await f.key(f.input('图片说明（替代文字）'), 'Enter')
    expect(attrs(f.editor).alt).toBe('新的说明')
    expect(markdownOf(f.editor)).toBe('![新的说明](/assets/01HX/x.png)\n')
    await f.done()
  })

  it('apply the address on leaving the field', async () => {
    const f = await open()
    await f.type(f.input('地址'), '/assets/01HX/y.png')
    await f.blur(f.input('地址'))
    expect(attrs(f.editor).src).toBe('/assets/01HX/y.png')
    expect(markdownOf(f.editor)).toBe('![旧说明](/assets/01HX/y.png)\n')
    await f.done()
  })

  it('refuse an empty address, keep the old one and say so', async () => {
    const f = await open()
    await f.type(f.input('地址'), '   ')
    await f.key(f.input('地址'), 'Enter')
    expect(attrs(f.editor).src).toBe('/assets/01HX/x.png')
    expect(f.input('地址').value).toBe('/assets/01HX/x.png')
    expect(f.host.querySelector('[role="alert"]')?.textContent).toContain('地址不能为空')
    expect(markdownOf(f.editor)).toBe(PAGE)
    await f.blur(f.input('地址'))
    expect(attrs(f.editor).src).toBe('/assets/01HX/x.png')
    await f.done()
  })

  it('empty the description, which writes no alt text', async () => {
    const f = await open()
    await f.type(f.input('图片说明（替代文字）'), '')
    await f.blur(f.input('图片说明（替代文字）'))
    expect(attrs(f.editor).alt).toBeNull()
    expect(markdownOf(f.editor)).toBe('![](/assets/01HX/x.png)\n')
    await f.done()
  })

  it('do not change the page when nothing was edited, and Escape drops the draft', async () => {
    const f = await open()
    let steps = 0
    f.editor.on('update', () => steps++)
    await f.blur(f.input('地址'))
    await f.blur(f.input('图片说明（替代文字）'))
    await f.type(f.input('地址'), '/elsewhere.png')
    await f.key(f.input('地址'), 'Escape')
    expect(f.input('地址').value).toBe('/assets/01HX/x.png')
    expect(steps).toBe(0)
    await f.done()
  })
})
