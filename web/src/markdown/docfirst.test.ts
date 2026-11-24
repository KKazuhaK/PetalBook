// Document-first round trip: the case a person who never sees Markdown hits.
//
// Each case is a document as the editor would hold it after UI editing.
//   doc -> markdown -> doc'
// "identical" means doc' deep-equals doc, after dropping what is invisible
// (trailing spaces and breaks; see normalizeDoc). Anything else means the
// person saved, reloaded and found their page changed.

import { describe, expect, it } from 'vitest'
import type { DocJSON } from './types'
import { fromMarkdown, knownDifferences, normalize, toMarkdown } from './testHarness'

// --- tiny builders -------------------------------------------------------
type Child = string | DocJSON
type MarkArg = string | { type: string; attrs?: Record<string, unknown> }
const t = (text: string, ...marks: MarkArg[]): DocJSON =>
  marks.length
    ? { type: 'text', text, marks: marks.map((m) => (typeof m === 'string' ? { type: m } : m)) }
    : { type: 'text', text }
const link = (href: string, title: string | null = null) => ({ type: 'link', attrs: { href, title } })
const br = (): DocJSON => ({ type: 'hardBreak' })
const kids = (c: Child[]) => c.map((x) => (typeof x === 'string' ? t(x) : x))
const p = (...c: Child[]): DocJSON => ({ type: 'paragraph', content: kids(c) })
const empty = (): DocJSON => ({ type: 'paragraph' })
const h = (level: number, ...c: Child[]): DocJSON => ({ type: 'heading', attrs: { level }, content: kids(c) })
const li = (...c: DocJSON[]): DocJSON => ({ type: 'listItem', content: c })
const ul = (...items: DocJSON[]): DocJSON => ({ type: 'bulletList', content: items })
const ol = (start: number, ...items: DocJSON[]): DocJSON => ({ type: 'orderedList', attrs: { start }, content: items })
const task = (checked: boolean, ...c: DocJSON[]): DocJSON => ({ type: 'taskItem', attrs: { checked }, content: c })
const tasks = (...items: DocJSON[]): DocJSON => ({ type: 'taskList', content: items })
const quote = (...c: DocJSON[]): DocJSON => ({ type: 'blockquote', content: c })
const code = (language: string | null, text: string): DocJSON => ({
  type: 'codeBlock',
  attrs: { language },
  content: text ? [t(text)] : undefined,
})
const hr = (): DocJSON => ({ type: 'horizontalRule' })
const img = (src: string, alt: string | null = null, title: string | null = null): DocJSON => ({
  type: 'image',
  attrs: { src, alt, title },
})
const cell = (type: string, ...c: DocJSON[]): DocJSON => ({ type, content: c })
const row = (...cells: DocJSON[]): DocJSON => ({ type: 'tableRow', content: cells })
const table = (...rows: DocJSON[]): DocJSON => ({ type: 'table', content: rows })
const th = (...c: DocJSON[]) => cell('tableHeader', ...c)
const td = (...c: DocJSON[]) => cell('tableCell', ...c)
const doc = (...c: DocJSON[]): DocJSON => ({ type: 'doc', content: c })

export const cases: Record<string, DocJSON> = {
  // --- emphasis next to CJK punctuation: the everyday Chinese case -------
  'cjk/粗体以引号结尾后接句号': doc(p('她说', t('“别急着回答，先把话听完”', 'bold'), '。')),
  'cjk/粗体以冒号结尾后接汉字': doc(p(t('注意：', 'bold'), '这里是正文')),
  'cjk/粗体以括号开头前接汉字': doc(p('然后', t('（栀子花）', 'bold'), '开了')),
  'cjk/粗体以书名号包裹': doc(p('读', t('《暮雪》', 'bold'), '的时候')),
  'cjk/斜体以句号结尾后接汉字': doc(p('他说', t('就这样吧。', 'italic'), '然后走了')),
  'cjk/删除线以叹号结尾': doc(p('这是', t('不对的！', 'strike'), '对的')),
  'cjk/粗体紧贴汉字无标点': doc(p('夏天的', t('栀子', 'bold'), '是最好的')),
  'cjk/粗体内含逗号': doc(p(t('别急着回答，先把话听完', 'bold'))),
  'cjk/粗体以省略号结尾': doc(p('她说', t('算了……', 'bold'), '就没有然后了')),

  // --- marks: boundaries and overlap -------------------------------------
  'mark/粗体尾部带空格': doc(p('前', t('粗体 ', 'bold'), '后')),
  'mark/粗体头部带空格': doc(p('前', t(' 粗体', 'bold'), '后')),
  'mark/粗斜体相邻': doc(p(t('粗', 'bold'), t('斜', 'italic'), '正')),
  'mark/交叠': doc(p(t('甲', 'bold'), t('乙', 'bold', 'italic'), t('丙', 'italic'))),
  'mark/粗体包住链接': doc(p(t('看', 'bold'), t('这里', 'bold', link('https://example.com')), t('吧', 'bold'))),
  'mark/粗体跨硬换行': doc(p(t('第一行', 'bold'), { ...br(), marks: [{ type: 'bold' }] }, t('第二行', 'bold'))),
  'mark/行内代码含反引号': doc(p('写 ', t('a ` b', 'code'), ' 吧')),
  'mark/行内代码首尾空格': doc(p(t(' x ', 'code'))),
  'mark/行内代码含星号': doc(p(t('**不是粗体**', 'code'))),
  'mark/英文单词内粗体': doc(p('un', t('believ', 'bold'), 'able')),

  // --- literal characters a person may simply type -----------------------
  'lit/星号下划线': doc(p('2 * 3 * 4，snake_case_name，*未闭合，**也未闭合')),
  'lit/波浪号范围': doc(p('约~10 个，价格 5~8 元，~~不是删除~~')),
  'lit/方括号圆括号': doc(p('数组 a[0]，[不是链接](不是地址)，![不是图片](x)')),
  'lit/尖括号与and': doc(p('a < b 且 c > d，A&B，&amp;，<div>，<不是标签>')),
  'lit/反斜杠': doc(p('路径 C:\\Users\\me，结尾反斜杠\\')),
  'lit/反引号': doc(p('一个 ` 和三个 ``` 反引号')),
  'lit/美元符号': doc(p('价格 $5 到 $8，$E=mc^2$')),
  'lit/网址': doc(p('访问 https://example.com/a_b?x=1&y=2 看看')),
  'lit/脚注样式': doc(p('这不是脚注[^1]')),
  'lit/行首井号': doc(p('# 这不是标题')),
  'lit/行首减号': doc(p('- 这不是列表')),
  'lit/行首加号': doc(p('+ 这也不是列表')),
  'lit/行首数字点': doc(p('1. 这不是有序列表')),
  'lit/行首大于号': doc(p('> 这不是引用')),
  'lit/行首三减号': doc(p('---')),
  'lit/行首三反引号': doc(p('```js')),
  'lit/行首竖线': doc(p('| 不是 | 表格 |')),
  'lit/行首方括号待办': doc(p('[ ] 这不是待办')),
  'lit/等号行': doc(p('标题样子', br(), '====')),
  'lit/首尾空格': doc(p('  前后有空格  ')),
  'lit/全角空格缩进': doc(p('　　首行缩进两个全角空格')),
  'lit/段内四空格': doc(p('    四个空格开头不是代码块')),

  // --- paragraphs and breaks ----------------------------------------------
  'para/硬换行': doc(p('第一行', br(), '第二行', br(), '第三行')),
  'para/连续两个硬换行': doc(p('第一行', br(), br(), '第三行')),
  'para/段尾硬换行': doc(p('第一行', br())),
  'para/空段落夹中间': doc(p('上'), empty(), p('下')),
  'para/多个空段落': doc(p('上'), empty(), empty(), empty(), p('下')),
  'para/开头空段落': doc(empty(), p('正文')),
  'para/结尾空段落': doc(p('正文'), empty()),
  'para/空文档': doc(empty()),
  'para/尾部空格': doc(p('打完空格还没打下一个字 ')),
  'para/尾部全角空格': doc(p('全角空格要留着　')),

  // --- headings -----------------------------------------------------------
  'head/各级': doc(h(1, '一'), h(2, '二'), h(3, '三'), h(4, '四'), h(5, '五'), h(6, '六')),
  'head/含格式': doc(h(2, '关于', t('栀子', 'bold'), '和', t('code', 'code'))),
  'head/以井号结尾': doc(h(2, 'C#')),
  'head/标题里换行': doc(h(3, '甲', br(), '乙')),
  'head/空标题': doc({ type: 'heading', attrs: { level: 2 } }, p('正文')),

  // --- lists --------------------------------------------------------------
  'list/无序': doc(ul(li(p('栀子')), li(p('玫瑰')))),
  'list/有序从5开始': doc(ol(5, li(p('第五')), li(p('第六')))),
  'list/三层嵌套': doc(ul(li(p('花'), ul(li(p('栀子'), ul(li(p('白栀')))))), li(p('树')))),
  'list/无序套有序': doc(ul(li(p('树'), ol(1, li(p('玉兰')), li(p('山茶')))))),
  'list/项内两段': doc(ul(li(p('第一段'), p('第二段')), li(p('下一项')))),
  'list/项内代码块': doc(ul(li(p('看代码'), code('js', 'let a = 1')))),
  'list/项内引用': doc(ul(li(p('看引用'), quote(p('引用内容'))))),
  'list/两个相邻无序列表': doc(ul(li(p('甲'))), ul(li(p('乙')))),
  'list/无序接有序': doc(ul(li(p('甲'))), ol(1, li(p('乙')))),
  'list/列表后接段落': doc(ul(li(p('甲'))), p('段落')),
  'list/空列表项': doc(ul(li(empty()), li(p('有字')))),
  'list/待办': doc(tasks(task(false, p('没做')), task(true, p('做完')))),
  'list/待办嵌套': doc(tasks(task(false, p('父'), tasks(task(true, p('子')))))),
  'list/待办接无序': doc(tasks(task(false, p('待办'))), ul(li(p('普通')))),
  'list/项内先空一行再写字': doc(ul(li(empty(), p('二')))),
  'list/项以引用开头': doc(ul(li(empty(), quote(p('引用'))))),
  'list/项以代码块开头': doc(ul(li(empty(), code('js', 'a')))),
  'list/项内硬换行': doc(ul(li(p('第一行', br(), '第二行')))),

  // --- quotes, code, rules -----------------------------------------------
  'quote/两段': doc(quote(p('第一段'), p('第二段'))),
  'quote/嵌套': doc(quote(p('外'), quote(p('内')))),
  'quote/含列表': doc(quote(ul(li(p('甲')), li(p('乙'))))),
  'quote/含代码块': doc(quote(code('go', 'x := 1'))),
  'quote/相邻两个引用': doc(quote(p('甲')), quote(p('乙'))),
  'code/带语言': doc(code('go', 'func main() {\n\tfmt.Println("*hi*")\n}')),
  'code/无语言': doc(code(null, '纯文本\n  缩进')),
  'code/含三反引号': doc(code('markdown', '```js\nconsole.log(1)\n```')),
  'code/含空行': doc(code('text', '第一行\n\n\n第四行')),
  'code/首尾空行': doc(code('text', '\n中间\n')),
  'code/空代码块': doc(code('js', '')),
  'code/带附加信息': doc(code('js title="a.js"', 'let a')),
  'code/相邻两个': doc(code('js', 'a'), code('js', 'b')),
  'rule/段间': doc(p('上'), hr(), p('下')),
  'rule/连续两条': doc(p('上'), hr(), hr(), p('下')),

  // --- links and images ----------------------------------------------------
  'link/普通': doc(p(t('链接', link('https://example.com')))),
  'link/带标题': doc(p(t('链接', link('https://example.com', '标题 "含引号"')))),
  'link/地址含括号': doc(p(t('维基', link('https://zh.wikipedia.org/wiki/栀子_(植物)')))),
  'link/地址含空格': doc(p(t('文件', link('assets/my file.pdf')))),
  'link/文字含方括号': doc(p(t('看 [这里]', link('https://example.com')))),
  'link/文字即地址': doc(p(t('https://example.com/a_b', link('https://example.com/a_b')))),
  'img/独占一段': doc(p(img('assets/01JD/a.jpg', '栀子花'))),
  'img/带标题': doc(p(img('assets/01JD/a.jpg', '栀子花', '盛夏'))),
  'img/路径含空格': doc(p(img('assets/01JD/my photo.jpg', '图'))),
  'img/路径含括号': doc(p(img('assets/01JD/photo (1).jpg', '图'))),
  'img/无说明': doc(p(img('assets/01JD/a.jpg'))),
  'img/说明含方括号': doc(p(img('assets/a.jpg', '图 [1]'))),
  'img/带链接的图片': doc(p({ ...img('cat.png', '猫'), marks: [link('https://example.com')] })),
  'img/粗体里的图片': doc(p(t('看', 'bold'), { ...img('a.png', '图'), marks: [{ type: 'bold' }] }, t('这', 'bold'))),
  'img/段内': doc(p('前', img('assets/a.png', '图'), '后')),
  'img/中文文件名': doc(p(img('assets/01JD/栀子花.jpg', '图'))),

  // --- tables ---------------------------------------------------------------
  'table/基本': doc(table(row(th(p('花')), th(p('季节'))), row(td(p('栀子')), td(p('夏'))))),
  'table/含格式': doc(
    table(
      row(th(p('名')), th(p('说明'))),
      row(td(p(t('粗', 'bold'))), td(p('含', t('a|b', 'code'), '和', t('链接', link('https://example.com'))))),
    ),
  ),
  'table/含竖线文字': doc(table(row(th(p('a'))), row(td(p('x | y'))))),
  'table/空单元格': doc(table(row(th(p('a')), th(p('b'))), row(td(empty()), td(p('有'))))),
  'table/单元格内硬换行': doc(table(row(th(p('a'))), row(td(p('第一行', br(), '第二行'))))),
  'table/单元格尾部空格': doc(table(row(th(p('nihao asd ')), th(empty())), row(td(empty()), td(empty())))),
  'table/单元格首部空格': doc(table(row(th(p(' 前面有空格'))), row(td(p('b'))))),
  'table/表头行被删掉': doc(table(row(td(p('1')), td(p('2'))), row(td(p('3')), td(p('4'))))),
  'table/表头出现在第二行': doc(table(row(th(p('a'))), row(th(p('b'))))),
  'table/表格前后有段落': doc(p('上'), table(row(th(p('a'))), row(td(p('b')))), p('下')),

  // --- a realistic page -----------------------------------------------------
  'mix/随笔': doc(
    h(1, '关于栀子'),
    p(
      '夏天的',
      t('栀子', 'bold'),
      '是最好的。它不如玫瑰',
      t('妖娆', 'strike'),
      '热烈，但',
      t('香。', 'bold'),
      '真的。',
    ),
    h(2, '为什么是白色'),
    ol(1, li(p('白色不抢眼')), li(p('白色', t('耐看', 'italic')), ul(li(p('信纸是白的')), li(p('雪也是白的'))))),
    quote(p('可是，信纸为什么是白的')),
    p(img('assets/01JD/a.jpg', '栀子')),
    table(row(th(p('花')), th(p('颜色'))), row(td(p('栀子')), td(p('白')))),
    code('text', '花开花落'),
    hr(),
    tasks(task(true, p('写完')), task(false, p('发布'))),
    p('参见', t('另一篇', link('https://example.com/x')), '。'),
  ),
}

describe('document-first round trip', () => {
  const names = Object.keys(cases)
  const results = new Map<string, { same: boolean; stable: boolean }>()

  const run = (name: string) => {
    let r = results.get(name)
    if (!r) {
      const input = cases[name]
      const before = normalize(input)
      const markdown = toMarkdown(input)
      const after = fromMarkdown(markdown)
      r = { same: JSON.stringify(before) === JSON.stringify(after), stable: toMarkdown(after) === markdown }
      results.set(name, r)
    }
    return r
  }

  it('has the same case count as the spike (116)', () => {
    expect(names).toHaveLength(116)
  })

  for (const name of names) {
    if (name in knownDifferences) {
      it(`${name} comes back different, as documented (${knownDifferences[name]})`, () => {
        const r = run(name)
        // Fails when the difference is gone too, so the list cannot go stale.
        expect(r.same).toBe(false)
      })
    } else {
      it(`${name} comes back identical and stable`, () => {
        const r = run(name)
        expect(r.same).toBe(true)
        expect(r.stable).toBe(true)
      })
    }
  }

  it('113 cases are identical and 3 are known differences', () => {
    const different = names.filter((n) => !run(n).same)
    expect(names.length - different.length).toBe(113)
    expect(different.sort()).toEqual(Object.keys(knownDifferences).sort())
  })
})
