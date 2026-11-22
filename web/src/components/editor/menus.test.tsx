import { act } from 'react'
import { afterEach, describe, expect, it } from 'vitest'
import type { Editor } from '@tiptap/core'
import { EditorToolbar } from './EditorToolbar'
import { TableControls } from './table/TableControls'
import { cellAtSelection } from './table/tableOps'
import { cleanup, click, cursorIn, markdownOf, mount, pageEditor, settle } from './pageTestKit'

// Menus are only built when they are opened, so a mistake in how one is put
// together shows up only then. Each of the new ones is opened here and used.

const SOURCE = '| 甲 | 乙 |\n| - | - |\n| 1 | 2 |\n'
const text = () => document.body.textContent ?? ''
const q = <T extends Element = HTMLElement>(sel: string, root: ParentNode = document) => root.querySelector<T>(sel)
const all = (sel: string, root: ParentNode = document) => Array.from(root.querySelectorAll<HTMLElement>(sel))
const byLabel = (label: string) => q(`[aria-label="${label}"]`)!
const item = (label: string) => all('[role^="menuitem"]').find((el) => el.textContent?.includes(label))!
const key = (el: Element, k: string) => act(async () => void el.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true })))

afterEach(cleanup)

// A closed menu stays in the page until its closing animation ends.
const closed = () => expect.poll(() => all('[role^="menuitem"]').length).toBe(0)

async function toolbar(markdown: string, setup?: (e: Editor) => void) {
  const editor = pageEditor(markdown)
  setup?.(editor)
  const view = await mount(editor, <EditorToolbar editor={editor} />)
  return { editor, view }
}

describe('the heading menu', () => {
  it('offers text and headings 1 to 4 and sets the one chosen', async () => {
    const { editor, view } = await toolbar('正文\n')
    cursorIn(editor, '正')
    await settle()
    const trigger = q('[aria-label^="段落样式"]')!
    expect(trigger.textContent).toContain('正文')
    await click(trigger)
    const labels = all('[role^="menuitem"]').map((el) => el.textContent)
    expect(labels).toEqual(['正文', '标题 1', '标题 2', '标题 3', '标题 4'])
    await click(item('标题 2'))
    expect(markdownOf(editor)).toBe('## 正文\n')
    expect(q('[aria-label^="段落样式"]')!.textContent).toContain('标题 2')
    await view.unmount()
  })

  it('goes back to text', async () => {
    const { editor, view } = await toolbar('### 标题\n')
    cursorIn(editor, '标')
    await click(q('[aria-label^="段落样式"]')!)
    await click(item('正文'))
    expect(markdownOf(editor)).toBe('标题\n')
    await view.unmount()
  })

  it('says 标题 5 for a page that has one, does not offer it, and keeps it', async () => {
    const { editor, view } = await toolbar('##### 五级\n\n###### 六级\n')
    cursorIn(editor, '五')
    expect(q('[aria-label^="段落样式"]')!.textContent).toContain('标题 5')
    cursorIn(editor, '六')
    await settle()
    expect(q('[aria-label^="段落样式"]')!.textContent).toContain('标题 6')
    await click(q('[aria-label^="段落样式"]')!)
    expect(all('[role^="menuitem"]').map((el) => el.textContent)).not.toContain('标题 5')
    await key(document.activeElement ?? document.body, 'Escape')
    expect(markdownOf(editor)).toBe('##### 五级\n\n###### 六级\n')
    await view.unmount()
  })

  it('is off inside a table cell, where a heading cannot be written', async () => {
    const { editor, view } = await toolbar(SOURCE)
    cursorIn(editor, '1')
    await settle()
    expect(q<HTMLButtonElement>('[aria-label^="段落样式"]')!.disabled).toBe(true)
    await view.unmount()
  })
})

describe('the formatting buttons', () => {
  it('toggle bold, italic, strike and code, and show their shortcuts', async () => {
    const { editor, view } = await toolbar('一二三\n')
    editor.commands.setTextSelection({ from: 1, to: 3 })
    await settle()
    expect(byLabel('粗体').title).toMatch(/粗体 \((Ctrl\+B|⌘B)\)/)
    expect(byLabel('删除线').title).toMatch(/(Ctrl\+Shift\+S|⌘⇧S)/)
    for (const label of ['粗体', '斜体', '删除线', '行内代码']) await click(byLabel(label))
    expect(['粗体', '斜体', '删除线', '行内代码'].every((l) => byLabel(l).getAttribute('aria-pressed') === 'true')).toBe(true)
    expect(editor.isActive('bold') && editor.isActive('italic') && editor.isActive('strike') && editor.isActive('code')).toBe(true)
    await view.unmount()
  })

  it('are off in a code block', async () => {
    const { editor, view } = await toolbar('```\ncode\n```\n')
    editor.commands.setTextSelection(3)
    await settle()
    expect((byLabel('粗体') as HTMLButtonElement).disabled).toBe(true)
    await view.unmount()
  })
})

describe('the link button', () => {
  it('is off without a selection, opens a box for the address on a selection, and links the text', async () => {
    const { editor, view } = await toolbar('一二三\n')
    expect((byLabel('链接') as HTMLButtonElement).disabled).toBe(true)
    editor.commands.setTextSelection({ from: 1, to: 3 })
    await settle()
    const button = byLabel('链接') as HTMLButtonElement
    expect(button.disabled).toBe(false)
    expect(button.title).toMatch(/(Ctrl\+K|⌘K)/)
    await click(button)
    const input = q<HTMLInputElement>('input[aria-label="修改网址"]')!
    expect(input).not.toBeNull()
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'https://example.com')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await key(input, 'Enter')
    expect(markdownOf(editor)).toBe('[一二](https://example.com/)三\n'.replace('example.com/', 'example.com'))
    await view.unmount()
  })

  it('opens with Ctrl+K from the text', async () => {
    const { editor, view } = await toolbar('一二三\n')
    editor.commands.setTextSelection({ from: 1, to: 3 })
    await settle()
    await act(async () => void editor.view.dom.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', ctrlKey: true, bubbles: true })))
    expect(q('input[aria-label="修改网址"]')).not.toBeNull()
    await view.unmount()
  })
})

describe('the footnote button', () => {
  it('inserts a reference and its definition', async () => {
    const { editor, view } = await toolbar('正文\n')
    cursorIn(editor, '正')
    await settle()
    await click(byLabel('脚注'))
    expect(markdownOf(editor)).toMatch(/正\[\^1\]文\n\n\[\^1\]: /)
    await view.unmount()
  })
})

describe('the table size picker', () => {
  it('opens a 10 by 8 grid, labels the size under the pointer, and inserts that size', async () => {
    const { editor, view } = await toolbar('正文\n')
    cursorIn(editor, '正')
    await click(byLabel('表格'))
    const cells = all('[role="group"] button')
    expect(cells).toHaveLength(80)
    expect(q('[role="status"]')!.textContent).toBe('3 × 3')
    // The cell in column 3, row 4.
    await act(async () => void cells[3 * 10 + 2].dispatchEvent(new MouseEvent('mouseover', { bubbles: true })))
    expect(q('[role="status"]')!.textContent).toBe('3 × 4')
    await click(cells[3 * 10 + 2])
    const table = editor.state.doc.child(1)
    expect(table.type.name).toBe('table')
    expect([table.childCount, table.firstChild!.childCount]).toEqual([4, 3])
    expect(q('[role="status"]')).toBeNull()
    await view.unmount()
  })

  it('is used from the keyboard: arrows pick the size, Enter inserts', async () => {
    const { editor, view } = await toolbar('正文\n')
    cursorIn(editor, '正')
    await click(byLabel('表格'))
    const grid = q('[role="group"][tabindex]')!
    await key(grid, 'ArrowRight')
    await key(grid, 'ArrowDown')
    await key(grid, 'ArrowDown')
    expect(q('[role="status"]')!.textContent).toBe('4 × 5')
    await key(grid, 'Enter')
    const table = editor.state.doc.child(1)
    expect([table.childCount, table.firstChild!.childCount]).toEqual([5, 4])
    await view.unmount()
  })

  it('becomes the table menu inside a table, and aligns a column from it', async () => {
    const { editor, view } = await toolbar(SOURCE)
    cursorIn(editor, '2')
    await click(byLabel('表格'))
    expect(all('[role^="menuitem"]').map((el) => el.textContent)).toEqual(
      expect.arrayContaining(['在上方插入一行', '在下方插入一行', '在左侧插入一列', '在右侧插入一列', '左对齐', '居中', '右对齐', '默认对齐', '删除这一行', '删除这一列', '删除整个表格']),
    )
    await click(item('居中'))
    expect(markdownOf(editor)).toBe('| 甲 | 乙 |\n| --- | :---: |\n| 1 | 2 |\n')
    await view.unmount()
  })

  it('says what deleting the header row does, and cannot insert above it', async () => {
    const { editor, view } = await toolbar(SOURCE)
    cursorIn(editor, '甲')
    await click(byLabel('表格'))
    expect(item('删除表头行').textContent).toContain('下一行成为表头')
    expect(item('在上方插入一行').getAttribute('aria-disabled')).toBe('true')
    await click(item('删除表头行'))
    expect(markdownOf(editor)).toBe('| 1 | 2 |\n| --- | --- |\n')
    await view.unmount()
  })
})

describe('the handles at the edges of a table', () => {
  async function hover(editor: Editor, text: string) {
    cursorIn(editor, text)
    editor.commands.blur()
    const cell = Array.from(editor.view.dom.querySelectorAll('td, th')).find((c) => c.textContent === text)!
    await act(async () => void cell.dispatchEvent(new MouseEvent('mousemove', { bubbles: true })))
    // The controls are laid out on the next animation frame, once for each of
    // the cursor move, the blur and the pointer. Until those have run, the
    // handles of the last hover may still be up and go away a moment later:
    // let them run, then look.
    await act(() => new Promise<void>((done) => requestAnimationFrame(() => requestAnimationFrame(() => done()))))
    await expect.poll(() => q('[aria-label="这一列"]')).not.toBeNull()
  }
  async function page(markdown = SOURCE) {
    const editor = pageEditor(markdown)
    const view = await mount(editor, <TableControls editor={editor} />)
    return { editor, view }
  }

  it('appear over a cell: a handle for its column, its row, and the table, and a + on two edges', async () => {
    const { editor, view } = await page()
    await hover(editor, '2')
    for (const label of ['这一列', '这一行', '整个表格', '在末尾添加一行', '在末尾添加一列']) expect(byLabel(label), label).not.toBeNull()
    await view.unmount()
  })

  it('have + buttons that add a row and a column at the end', async () => {
    const { editor, view } = await page()
    await hover(editor, '1')
    await click(byLabel('在末尾添加一行'))
    expect(editor.state.doc.firstChild!.childCount).toBe(3)
    await click(byLabel('在末尾添加一列'))
    expect(editor.state.doc.firstChild!.firstChild!.childCount).toBe(3)
    await view.unmount()
  })

  it('have a column menu that inserts, aligns and deletes', async () => {
    const { editor, view } = await page()
    await hover(editor, '2')
    await click(byLabel('这一列'))
    expect(all('[role^="menuitem"]').map((el) => el.textContent)).toEqual(expect.arrayContaining(['在左侧插入一列', '在右侧插入一列', '左对齐', '居中', '右对齐', '删除这一列']))
    await click(item('右对齐'))
    expect(markdownOf(editor)).toBe('| 甲 | 乙 |\n| --- | ---: |\n| 1 | 2 |\n')
    await closed()
    await hover(editor, '2')
    await click(byLabel('这一列'))
    await click(item('在左侧插入一列'))
    expect(editor.state.doc.firstChild!.firstChild!.childCount).toBe(3)
    await closed()
    await hover(editor, '1')
    await click(byLabel('这一列'))
    await click(item('删除这一列'))
    expect(editor.state.doc.firstChild!.firstChild!.childCount).toBe(2)
    await view.unmount()
  })

  it('have a row menu, where the header row can be deleted', async () => {
    const { editor, view } = await page()
    await hover(editor, '甲')
    await click(byLabel('这一行'))
    expect(item('删除表头行')).toBeTruthy()
    await click(item('删除表头行'))
    expect(markdownOf(editor)).toBe('| 1 | 2 |\n| --- | --- |\n')
    await view.unmount()
  })

  it('have a table menu that deletes the table', async () => {
    const { editor, view } = await page('前\n\n' + SOURCE)
    await hover(editor, '1')
    await click(byLabel('整个表格'))
    await click(item('删除整个表格'))
    expect(markdownOf(editor)).toBe('前\n')
    expect(cellAtSelection(editor.state)).toBeNull()
    await view.unmount()
  })

  it('go away with the pointer, after a moment', async () => {
    const { editor, view } = await page()
    await hover(editor, '1')
    await act(async () => void editor.view.dom.dispatchEvent(new MouseEvent('mouseleave')))
    await expect.poll(() => q('[aria-label="这一列"]')).toBeNull()
    await view.unmount()
  })
})

describe('the language picker', () => {
  it('opens a searchable list, narrows it as one types, and sets the language with Enter', async () => {
    const editor = pageEditor('```\nlet a = 1\n```\n')
    const view = await mount(editor)
    await click(q('[aria-label^="代码语言"]')!)
    const options = () => all('[role="option"]').map((el) => el.textContent)
    expect(options()[0]).toBe('纯文本')
    expect(options()).toEqual(expect.arrayContaining(['JavaScript', 'TypeScript', 'Python', 'Mermaid']))
    const search = q<HTMLInputElement>('input[aria-label="搜索语言"]')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(search, 'type')
      search.dispatchEvent(new Event('input', { bubbles: true }))
    })
    expect(options()).toEqual(['TypeScript'])
    await key(search, 'Enter')
    expect(markdownOf(editor)).toBe('```typescript\nlet a = 1\n```\n')
    expect(q('[aria-label^="代码语言"]')!.textContent).toBe('TypeScript')
    await view.unmount()
  })

  it('moves with the arrow keys and says when nothing matches', async () => {
    const editor = pageEditor('```js\nx\n```\n')
    const view = await mount(editor)
    await click(q('[aria-label^="代码语言"]')!)
    const search = q<HTMLInputElement>('input[aria-label="搜索语言"]')!
    await key(search, 'ArrowDown')
    await key(search, 'Enter')
    expect(markdownOf(editor)).toMatch(/^```bash\n/)
    await click(q('[aria-label^="代码语言"]')!)
    const again = q<HTMLInputElement>('input[aria-label="搜索语言"]')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(again, 'zzzz')
      again.dispatchEvent(new Event('input', { bubbles: true }))
    })
    expect(text()).toContain('没有匹配的语言')
    await view.unmount()
  })

  it('chooses plain text, and shows an unknown language as written', async () => {
    const editor = pageEditor('```klingon\nx\n```\n')
    const view = await mount(editor)
    expect(q('[aria-label^="代码语言"]')!.textContent).toBe('klingon')
    await click(q('[aria-label^="代码语言"]')!)
    await click(all('[role="option"]').find((el) => el.textContent === '纯文本')!)
    expect(markdownOf(editor)).toBe('```\nx\n```\n')
    expect(q('[aria-label^="代码语言"]')!.textContent).toBe('纯文本')
    await view.unmount()
  })

  it('has a copy button', async () => {
    const written: string[] = []
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: async (s: string) => void written.push(s) }, configurable: true })
    const editor = pageEditor('```js\nconst a = 1\n```\n')
    const view = await mount(editor)
    await click(byLabel('复制代码'))
    expect(written).toEqual(['const a = 1'])
    expect(byLabel('已复制')).not.toBeNull()
    await view.unmount()
  })
})
