// A bare editor wired to the Markdown layer, for checking in a real browser
// what the headless tests cannot: typing, shortcuts, paste, input methods.
//
// The right pane shows the Markdown that would be written to the file. After
// every change the page also reads that Markdown back and compares the result
// with what is in the editor; "往返一致" means saving and reopening would give
// the same document.

import { Editor } from '@tiptap/core'
import { extensions, docToMarkdown, markdownToDoc, normalizeDoc } from '../lib/markdown.mjs'

const $ = (id) => document.getElementById(id)

const SAMPLE = `# 关于栀子

夏天的**栀子**是最好的。她说**“别急着回答，先把话听完”**。然后就没有然后了。

## 为什么是白色

1. 白色不抢眼
2. 白色*耐看*
   - 信纸是白的
   - 雪也是白的

> 可是，信纸为什么是白的

| 花 | 季节 |
| --- | --- |
| 栀子 | 夏 |
| 玉兰 | 冬 |

- [x] 写完
- [ ] 发布

约~10 个，价格 5~8 元。参见 [另一篇](https://example.com/x)。
`

const editor = new Editor({
  element: $('editor'),
  extensions,
  content: markdownToDoc(SAMPLE),
  onUpdate: refresh,
  onSelectionUpdate: paintToolbar,
})

// Normalise through the schema so defaults are filled in before comparing.
const scratch = new Editor({ element: document.createElement('div'), extensions })
function canonical(json) {
  scratch.commands.setContent(json)
  return scratch.getJSON()
}

function check() {
  // Trailing spaces and breaks cannot be seen and are not saved, so they are
  // not a difference: without this the status flickers while typing.
  const before = canonical(normalizeDoc(editor.getJSON()))
  const markdown = docToMarkdown(before)
  const after = canonical(markdownToDoc(markdown))
  const again = docToMarkdown(after)
  return { markdown, same: JSON.stringify(before) === JSON.stringify(after), stable: again === markdown, before, after }
}

function refresh() {
  let result
  try {
    result = check()
  } catch (error) {
    $('status').textContent = '转换出错'
    $('status').className = 'bad'
    $('diff').textContent = String(error?.stack ?? error)
    return
  }
  // While the person is typing in the source pane it is the one in charge;
  // rewriting it under the cursor would fight them.
  if (document.activeElement !== $('markdown')) $('markdown').value = result.markdown
  if (result.same) {
    $('status').textContent = '往返一致'
    $('status').className = 'ok'
    $('diff').textContent = ''
  } else {
    $('status').textContent = result.stable ? '重新打开后有细微变化' : '往返不一致'
    $('status').className = 'bad'
    $('diff').textContent = '编辑器里：' + JSON.stringify(result.before.content) + '\n\n重新打开后：' + JSON.stringify(result.after.content)
  }
  paintToolbar()
}

const tools = [
  ['B', '粗体', (c) => c.toggleBold(), 'bold'],
  ['I', '斜体', (c) => c.toggleItalic(), 'italic'],
  ['S', '删除线', (c) => c.toggleStrike(), 'strike'],
  ['<>', '行内代码', (c) => c.toggleCode(), 'code'],
  ['H1', '一级标题', (c) => c.toggleHeading({ level: 1 }), ['heading', { level: 1 }]],
  ['H2', '二级标题', (c) => c.toggleHeading({ level: 2 }), ['heading', { level: 2 }]],
  ['•', '无序列表', (c) => c.toggleBulletList(), 'bulletList'],
  ['1.', '有序列表', (c) => c.toggleOrderedList(), 'orderedList'],
  ['☑', '待办', (c) => c.toggleTaskList(), 'taskList'],
  ['❝', '引用', (c) => c.toggleBlockquote(), 'blockquote'],
  ['{ }', '代码块', (c) => c.toggleCodeBlock(), 'codeBlock'],
  ['—', '分割线', (c) => c.setHorizontalRule()],
  ['链接', '加链接、改链接；留空则去掉链接', editLink, 'link'],
  null,
  ['表格', '插入表格', (c) => c.insertTable({ rows: 2, cols: 2, withHeaderRow: true })],
  ['+行', '在下方加一行', (c) => c.addRowAfter()],
  ['+列', '在右侧加一列', (c) => c.addColumnAfter()],
  ['−行', '删除当前行', (c) => c.deleteRow()],
  ['−列', '删除当前列', (c) => c.deleteColumn()],
  ['删表', '删除整个表格', (c) => c.deleteTable()],
]

// A link is edited as a whole wherever the cursor is inside it.
function editLink(chain) {
  const current = editor.getAttributes('link').href ?? ''
  const href = window.prompt('链接地址（留空则去掉链接）', current)
  if (href === null) return chain
  chain = chain.extendMarkRange('link')
  return href.trim() ? chain.setLink({ href: href.trim() }) : chain.unsetLink()
}

for (const tool of tools) {
  if (!tool) {
    $('toolbar').append(Object.assign(document.createElement('span'), { className: 'sep' }))
    continue
  }
  const [text, title, run, active] = tool
  const button = document.createElement('button')
  button.textContent = text
  button.title = title
  button.dataset.active = JSON.stringify(active ?? null)
  // Keep the selection in the editor when a button is pressed.
  button.addEventListener('mousedown', (e) => e.preventDefault())
  button.addEventListener('click', () => run(editor.chain().focus()).run())
  $('toolbar').append(button)
}

function paintToolbar() {
  for (const button of $('toolbar').querySelectorAll('button')) {
    const active = JSON.parse(button.dataset.active)
    button.classList.toggle('on', active ? (Array.isArray(active) ? editor.isActive(...active) : editor.isActive(active)) : false)
  }
}

function load(markdown) {
  editor.commands.setContent(markdownToDoc(markdown), { emitUpdate: true })
  refresh()
}

$('reload').addEventListener('click', () => load($('markdown').value))

// Editing the source: the text is read into the editor as it is typed. The
// text itself is left exactly as written until the document is next changed
// from the visual side, at which point the file is rewritten in the standard
// style (same content, e.g. "*" bullets become "-").
let sourceTimer
$('markdown').addEventListener('input', () => {
  clearTimeout(sourceTimer)
  sourceTimer = setTimeout(() => {
    try {
      editor.commands.setContent(markdownToDoc($('markdown').value), { emitUpdate: false })
      $('status').textContent = '已从原文读入'
      $('status').className = 'ok'
      $('diff').textContent = ''
    } catch (error) {
      $('status').textContent = '原文解析出错'
      $('status').className = 'bad'
      $('diff').textContent = String(error?.stack ?? error)
    }
  }, 150)
})

function setMode(mode) {
  document.querySelector('main').dataset.mode = mode
  for (const button of document.querySelectorAll('.modes button')) button.classList.toggle('on', button.dataset.mode === mode)
  if (mode !== 'source') $('markdown').blur()
  refresh()
}
for (const button of document.querySelectorAll('.modes button')) button.addEventListener('click', () => setMode(button.dataset.mode))
setMode('wysiwyg')
$('sample').addEventListener('click', () => load(SAMPLE))
$('clear').addEventListener('click', () => load(''))

// For inspection from the console and from automated checks.
window.petalbook = { editor, check, load, setMode, markdownToDoc, docToMarkdown }

refresh()
