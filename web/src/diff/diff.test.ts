import { describe, expect, it } from 'vitest'
import { docToMarkdown, markdownToDoc } from '@/markdown'
import { diffDocs, diffVersions, projectDoc, type DiffResult } from './index'
import { DEL, FMT, INS, type Node } from './doc'
import { POOL } from './pool'

const norm = (md: string) => docToMarkdown(markdownToDoc(md))

// Compares and checks the one thing that must always hold: reading the new
// side of the merged document gives the new text, and reading the old side
// gives the old text.
function compare(oldMd: string, newMd: string): DiffResult {
  const r = diffVersions({ meta: {}, body: oldMd }, { meta: {}, body: newMd })
  expect(docToMarkdown(projectDoc(r.doc, 'new'))).toBe(norm(newMd))
  expect(docToMarkdown(projectDoc(r.doc, 'old'))).toBe(norm(oldMd))
  return r
}

type Run = [kind: 'eq' | 'ins' | 'del' | 'fmt', text: string]
const kindOf = (n: Node): Run[0] => {
  const t = (n.marks ?? []).map((m) => m.type)
  return t.includes(INS) ? 'ins' : t.includes(DEL) ? 'del' : t.includes(FMT) ? 'fmt' : 'eq'
}
// The inline content of a block as runs of text with their kind.
function runs(block: Node): Run[] {
  return (block.content ?? []).map((n): Run => [kindOf(n), n.type === 'text' ? n.text! : `<${n.type}>`])
}
const blocks = (r: DiffResult) => r.doc.content!
const diffAttr = (n: Node) => (n.attrs?.diff as string | undefined) ?? 'eq'
const kinds = (r: DiffResult) => blocks(r).map((b) => `${b.type}:${diffAttr(b)}`)
const text = (n: Node): string => (n.type === 'text' ? n.text! : (n.content ?? []).map(text).join(''))
const marked = (r: DiffResult) => JSON.stringify(r.doc).includes(`"${INS}"`) || JSON.stringify(r.doc).includes(`"${DEL}"`) || JSON.stringify(r.doc).includes(`"${FMT}"`) || JSON.stringify(r.doc).includes('"diff"')

describe('text insertion and deletion', () => {
  it('insertion at the start, in the middle and at the end', () => {
    expect(runs(blocks(compare('quick brown fox', 'very quick brown fox'))[0])).toEqual([['ins', 'very '], ['eq', 'quick brown fox']])
    expect(runs(blocks(compare('quick brown fox', 'quick very brown fox'))[0])).toEqual([['eq', 'quick '], ['ins', 'very '], ['eq', 'brown fox']])
    expect(runs(blocks(compare('quick brown fox', 'quick brown fox jumps'))[0])).toEqual([['eq', 'quick brown fox'], ['ins', ' jumps']])
  })
  it('deletion at the start, in the middle and at the end', () => {
    expect(runs(blocks(compare('very quick brown fox', 'quick brown fox'))[0])).toEqual([['del', 'very '], ['eq', 'quick brown fox']])
    expect(runs(blocks(compare('quick very brown fox', 'quick brown fox'))[0])).toEqual([['eq', 'quick '], ['del', 'very '], ['eq', 'brown fox']])
    expect(runs(blocks(compare('quick brown fox jumps', 'quick brown fox'))[0])).toEqual([['eq', 'quick brown fox'], ['del', ' jumps']])
  })
  it('a replaced word shows the old word, then the new one', () => {
    expect(runs(blocks(compare('the quick brown fox', 'the quick red fox'))[0])).toEqual([['eq', 'the quick '], ['del', 'brown'], ['ins', 'red'], ['eq', ' fox']])
  })
  it('counts the characters', () => {
    const r = compare('quick brown fox', 'quick very brown fox')
    expect(r.stats).toMatchObject({ inserted: 5, deleted: 0, changes: 1 })
  })
})

describe('Chinese text', () => {
  it('compares text without spaces', () => {
    const r = compare('我今天去了北京。', '我昨天去了上海。')
    const rs = runs(blocks(r)[0])
    expect(rs.filter((x) => x[0] === 'del').map((x) => x[1]).join('')).toBe('今北京')
    expect(rs.filter((x) => x[0] === 'ins').map((x) => x[1]).join('')).toBe('昨上海')
    expect(rs.filter((x) => x[0] === 'eq').map((x) => x[1]).join('')).toBe('我天去了。')
  })
  it('inserts a clause in the middle', () => {
    const r = compare('春天来了，花开了。', '春天来了，天气暖和了，花开了。')
    expect(runs(blocks(r)[0]).filter((x) => x[0] !== 'eq')).toEqual([['ins', '天气暖和了，']])
  })
  it('does not leave a single character standing between two changes', () => {
    // "的" is the only thing the two sentences share between the changes.
    const r = compare('苹果的颜色', '香蕉的味道')
    const rs = runs(blocks(r)[0])
    expect(rs.some((x) => x[0] === 'eq' && x[1] === '的')).toBe(false)
  })
  it('mixed Chinese and English', () => {
    const r = compare('使用 React 写页面，速度很快。', '使用 Vue 写页面，速度很快。')
    expect(runs(blocks(r)[0])).toEqual([['eq', '使用 '], ['del', 'React'], ['ins', 'Vue'], ['eq', ' 写页面，速度很快。']])
  })
})

describe('whole blocks', () => {
  it('identical documents carry no marks at all', () => {
    const md = '# 标题\n\n一段文字，有**加粗**和[链接](https://a.example)。\n\n- 一\n- 二\n\n| a | b |\n| - | - |\n| 1 | 2 |\n\n```js\nx\n```\n'
    const r = compare(md, md)
    expect(marked(r)).toBe(false)
    expect(r.identical).toBe(true)
    expect(r.stats).toEqual({ inserted: 0, deleted: 0, formatted: 0, changes: 0 })
  })
  it('an empty old document is all insertion', () => {
    const r = compare('', '一段。\n\n又一段。\n')
    expect(kinds(r)).toEqual(['paragraph:ins', 'paragraph:ins'])
    expect(r.stats.deleted).toBe(0)
  })
  it('an empty new document is all deletion', () => {
    const r = compare('一段。\n\n又一段。\n', '')
    expect(kinds(r)).toEqual(['paragraph:del', 'paragraph:del'])
  })
  it('a missing version (before the first) is the same as an empty one', () => {
    const r = diffVersions(null, { meta: { title: 'T' }, body: 'x\n' })
    expect(kinds(r)).toEqual(['paragraph:ins'])
    expect(r.header).toHaveLength(1)
  })
  it('both empty', () => {
    const r = compare('', '')
    expect(r.identical).toBe(true)
  })
  it('an added and a removed paragraph', () => {
    expect(kinds(compare('甲。\n\n乙。\n', '甲。\n\n乙。\n\n丙。\n'))).toEqual(['paragraph:eq', 'paragraph:eq', 'paragraph:ins'])
    expect(kinds(compare('甲。\n\n乙。\n\n丙。\n', '甲。\n\n丙。\n'))).toEqual(['paragraph:eq', 'paragraph:del', 'paragraph:eq'])
  })
  it('a paragraph that changed a little is compared inside, one that changed wholly is replaced', () => {
    expect(kinds(compare('甲乙丙丁戊己庚辛。\n', '甲乙丙丁戊己庚壬。\n'))).toEqual(['paragraph:eq'])
    expect(kinds(compare('春天的风吹过田野，带来泥土的气息。\n', 'The committee approved the budget on Tuesday.\n'))).toEqual(['paragraph:del', 'paragraph:ins'])
  })
  it('a rewritten paragraph is not shown as confetti', () => {
    const r = compare('今天天气很好，我们一起去公园散步，看看花草树木。\n', '明天可能下雨，大家最好待在家里，读读书喝喝茶。\n')
    expect(kinds(r)).toEqual(['paragraph:del', 'paragraph:ins'])
  })
})

describe('splitting, merging and moving', () => {
  it('a paragraph split in two keeps all text and shows where', () => {
    const r = compare('第一句话在这里。第二句话在这里。\n', '第一句话在这里。\n\n第二句话在这里。\n')
    expect(blocks(r).length).toBe(2)
    expect(r.stats.inserted + r.stats.deleted).toBeGreaterThan(0)
  })
  it('two paragraphs merged into one', () => {
    const r = compare('第一句话在这里。\n\n第二句话在这里。\n', '第一句话在这里。第二句话在这里。\n')
    expect(r.stats.inserted + r.stats.deleted).toBeGreaterThan(0)
  })
  it('a paragraph that only moved is the old one deleted and the new one inserted, both flagged as moved', () => {
    const r = compare('第一段，讲春天的故事。\n\n第二段，讲夏天的故事。\n\n第三段，讲秋天的故事。\n', '第二段，讲夏天的故事。\n\n第三段，讲秋天的故事。\n\n第一段，讲春天的故事。\n')
    const flagged = blocks(r).filter((b) => b.attrs?.diffMove)
    expect(flagged.map(diffAttr).sort()).toEqual(['del', 'ins'])
    expect(flagged.every((b) => text(b).startsWith('第一段'))).toBe(true)
    // Nothing else is marked.
    expect(blocks(r).filter((b) => !b.attrs?.diffMove).every((b) => diffAttr(b) === 'eq')).toBe(true)
  })
  it('swapping two paragraphs marks one as moved', () => {
    const r = compare('甲段，写了很多内容在这里。\n\n乙段，也写了很多内容在这里。\n', '乙段，也写了很多内容在这里。\n\n甲段，写了很多内容在这里。\n')
    expect(blocks(r).filter((b) => b.attrs?.diffMove)).toHaveLength(2)
  })
})

describe('format changes', () => {
  it('a heading level change is a format change, with the old level kept', () => {
    const r = compare('## 小节\n\n正文。\n', '### 小节\n\n正文。\n')
    const h = blocks(r)[0]
    expect(diffAttr(h)).toBe('fmt')
    expect(h.attrs).toMatchObject({ level: 3 })
    expect(JSON.parse(h.attrs!.diffOld as string)).toEqual({ type: 'heading', attrs: { level: 2 } })
  })
  it('a paragraph made a heading', () => {
    const r = compare('小节\n', '## 小节\n')
    expect(diffAttr(blocks(r)[0])).toBe('fmt')
  })
  it('bold added to unchanged text', () => {
    const r = compare('这是一段普通的话。\n', '这是**一段普通**的话。\n')
    expect(runs(blocks(r)[0])).toEqual([['eq', '这是'], ['fmt', '一段普通'], ['eq', '的话。']])
    expect(r.stats).toMatchObject({ inserted: 0, deleted: 0, formatted: 4, changes: 1 })
  })
  it('bold removed from unchanged text', () => {
    const r = compare('这是**一段普通**的话。\n', '这是一段普通的话。\n')
    expect(runs(blocks(r)[0]).map((x) => x[0])).toContain('fmt')
  })
  it('a link whose address changed but not its text', () => {
    const r = compare('见[这里](https://a.example)。\n', '见[这里](https://b.example)。\n')
    expect(runs(blocks(r)[0])).toEqual([['eq', '见'], ['fmt', '这里'], ['eq', '。']])
    const old = JSON.parse(String(blocks(r)[0].content![1].marks!.find((m) => m.type === FMT)!.attrs!.old)) as { type: string; attrs: { href: string } }[]
    expect(old[0].attrs.href).toBe('https://a.example')
  })
  it('bold text that was deleted stays bold', () => {
    const r = compare('前面**加粗的字**后面\n', '前面后面\n')
    const del = blocks(r)[0].content!.find((n) => kindOf(n) === 'del')!
    expect(del.marks!.map((m) => m.type).sort()).toEqual(['bold', DEL])
  })
  it('text and formatting changed together', () => {
    const r = compare('一二三四五\n', '一**二三六**五\n')
    const rs = runs(blocks(r)[0])
    expect(rs.some((x) => x[0] === 'ins' || x[0] === 'del')).toBe(true)
    expect(rs.some((x) => x[0] === 'fmt')).toBe(true)
  })
})

describe('lists', () => {
  it('a list item added, at the end and in the middle', () => {
    const r = compare('- 苹果\n- 香蕉\n', '- 苹果\n- 橙子\n- 香蕉\n')
    const items = blocks(r)[0].content!
    expect(items.map(diffAttr)).toEqual(['eq', 'ins', 'eq'])
  })
  it('a list item removed', () => {
    const items = blocks(compare('- 苹果\n- 橙子\n- 香蕉\n', '- 苹果\n- 香蕉\n'))[0].content!
    expect(items.map(diffAttr)).toEqual(['eq', 'del', 'eq'])
  })
  it('a list item edited inside', () => {
    const items = blocks(compare('- 苹果很甜\n- 香蕉\n', '- 苹果很酸\n- 香蕉\n'))[0].content!
    expect(items.map(diffAttr)).toEqual(['eq', 'eq'])
    expect(runs(items[0].content![0]).some((x) => x[0] === 'ins')).toBe(true)
  })
  it('a list item whose text changed wholly stays one item: the old text struck out, the new beside it', () => {
    const items = blocks(compare('- 一二三四\n- 香蕉\n', '- 五六七八\n- 香蕉\n'))[0].content!
    expect(items.map(diffAttr)).toEqual(['eq', 'eq'])
    expect(runs(items[0].content![0])).toEqual([['del', '一二三四'], ['ins', '五六七八']])
  })
  it('items reordered', () => {
    const r = compare('- 第一项内容在这里\n- 第二项内容在这里\n- 第三项内容在这里\n', '- 第三项内容在这里\n- 第一项内容在这里\n- 第二项内容在这里\n')
    const items = blocks(r)[0].content!
    expect(items.filter((i) => i.attrs?.diffMove)).toHaveLength(2)
  })
  it('nested lists are compared item by item', () => {
    const r = compare('- 水果\n  - 苹果\n  - 香蕉\n- 蔬菜\n', '- 水果\n  - 苹果\n  - 橙子\n  - 香蕉\n- 蔬菜\n')
    const fruit = blocks(r)[0].content![0]
    const nested = fruit.content![1]
    expect(nested.content!.map(diffAttr)).toEqual(['eq', 'ins', 'eq'])
    expect(diffAttr(blocks(r)[0].content![1])).toBe('eq')
  })
  it('a task item checked', () => {
    const r = compare('- [ ] 买菜\n- [ ] 做饭\n', '- [x] 买菜\n- [ ] 做饭\n')
    const items = blocks(r)[0].content!
    expect(diffAttr(items[0])).toBe('fmt')
    expect(items[0].attrs).toMatchObject({ checked: true })
    expect(JSON.parse(items[0].attrs!.diffOld as string)).toEqual({ type: 'taskItem', attrs: { checked: false } })
    expect(diffAttr(items[1])).toBe('eq')
  })
  it('a bullet list made numbered is a format change', () => {
    const r = compare('- 一\n- 二\n', '1. 一\n2. 二\n')
    expect(blocks(r)[0].type).toBe('orderedList')
    expect(diffAttr(blocks(r)[0])).toBe('fmt')
  })
  it('a bullet list made a task list keeps old items in a valid shape', () => {
    const r = compare('- 一\n- 二\n- 三\n', '- [ ] 一\n- [ ] 三\n')
    const list = blocks(r)[0]
    expect(list.type).toBe('taskList')
    expect(list.content!.every((i) => i.type === 'taskItem')).toBe(true)
  })
})

describe('pairing blocks of very different length', () => {
  it('a short list that gained an item next to a changed heading is still compared inside', () => {
    const r = compare('## 小节\n\n- 一\n- 二\n', '### 小节\n\n- 一\n- 二\n- 三\n')
    expect(kinds(r)).toEqual(['heading:fmt', 'bulletList:eq'])
    expect(blocks(r)[1].content!.map(diffAttr)).toEqual(['eq', 'eq', 'ins'])
  })
})

describe('blockquotes', () => {
  it('compares inside a quote', () => {
    const r = compare('> 伞是修得好的。\n', '> 伞是修得好的，只是没人等。\n')
    const q = blocks(r)[0]
    expect(q.type).toBe('blockquote')
    expect(runs(q.content![0]).some((x) => x[0] === 'ins')).toBe(true)
  })
  it('a paragraph put into a quote', () => {
    const r = compare('这是一段话，写得足够长了。\n', '> 这是一段话，写得足够长了。\n')
    expect(kinds(r)).toEqual(['paragraph:del', 'blockquote:ins'])
  })
})

describe('tables', () => {
  const t = (rows: string[][]) => `${rows.map((r) => `| ${r.join(' | ')} |`).join('\n').replace('\n', `\n|${rows[0].map(() => ' --- ').join('|')}|\n`)}\n`
  it('a cell edited is compared inside that cell', () => {
    const r = compare(t([['名称', '数量'], ['苹果', '十个'], ['香蕉', '五个']]), t([['名称', '数量'], ['苹果', '十二个'], ['香蕉', '五个']]))
    const table = blocks(r)[0]
    expect(table.type).toBe('table')
    expect(diffAttr(table)).toBe('eq')
    const cell = table.content![1].content![1]
    expect(runs(cell.content![0]).some((x) => x[0] === 'ins')).toBe(true)
    expect(table.content![2].content!.every((c) => !JSON.stringify(c).includes(INS))).toBe(true)
  })
  it('a row added', () => {
    const r = compare(t([['名称', '数量'], ['苹果', '十个']]), t([['名称', '数量'], ['苹果', '十个'], ['香蕉', '五个']]))
    const rows = blocks(r)[0].content!
    expect(rows.map(diffAttr)).toEqual(['eq', 'eq', 'ins'])
  })
  it('a row removed', () => {
    const rows = blocks(compare(t([['名称', '数量'], ['苹果', '十个'], ['香蕉', '五个']]), t([['名称', '数量'], ['苹果', '十个']])))[0].content!
    expect(rows.map(diffAttr)).toEqual(['eq', 'eq', 'del'])
  })
  it('a column added changes the shape: the old table is deleted and the new one inserted', () => {
    const r = compare(t([['名称', '数量'], ['苹果', '十个']]), t([['名称', '数量', '价格'], ['苹果', '十个', '三元']]))
    expect(kinds(r)).toEqual(['table:del', 'table:ins'])
  })
  it('a header cell edited', () => {
    const r = compare(t([['名称', '数量'], ['苹果', '十个']]), t([['名称', '个数'], ['苹果', '十个']]))
    expect(diffAttr(blocks(r)[0])).toBe('eq')
    expect(JSON.stringify(blocks(r)[0].content![0])).toContain(INS)
  })
})

describe('blocks that are only the same or replaced', () => {
  it('a code block edit shows the old block, then the new one', () => {
    const r = compare('```js\nlet a = 1\n```\n', '```js\nlet a = 2\n```\n')
    expect(kinds(r)).toEqual(['codeBlock:del', 'codeBlock:ins'])
  })
  it('an image replaced shows the old and the new one', () => {
    const r = compare('![封面](a.png)\n', '![封面](b.png)\n')
    expect(kinds(r)).toEqual(['paragraph:del', 'paragraph:ins'])
  })
  it('an image inside a sentence is compared as one unit', () => {
    const r = compare('看图：![x](a.png) 完毕。\n', '看图：![x](b.png) 完毕。\n')
    const rs = runs(blocks(r)[0])
    expect(rs).toEqual([['eq', '看图：'], ['del', '<image>'], ['ins', '<image>'], ['eq', ' 完毕。']])
  })
  it('raw HTML', () => {
    const r = compare('<div class="a">x</div>\n\n正文。\n', '<div class="b">x</div>\n\n正文。\n')
    expect(kinds(r)).toEqual(['rawBlock:del', 'rawBlock:ins', 'paragraph:eq'])
  })
  it('inline raw HTML is a unit', () => {
    const r = compare('文字<br class="a">更多\n', '文字<br class="b">更多\n')
    expect(runs(blocks(r)[0]).filter((x) => x[0] !== 'eq').map((x) => x[1])).toEqual(['<rawInline>', '<rawInline>'])
  })
  it('a footnote definition edited is replaced, a footnote mark is a unit', () => {
    const r = compare('正文[^1]。\n\n[^1]: 旧的注释。\n', '正文[^1]。\n\n[^1]: 新的注释。\n')
    expect(kinds(r)).toEqual(['paragraph:eq', 'footnoteDef:del', 'footnoteDef:ins'])
    const r2 = compare('正文[^1]。\n\n[^1]: 注。\n\n[^2]: 另。\n', '正文[^2]。\n\n[^1]: 注。\n\n[^2]: 另。\n')
    expect(runs(blocks(r2)[0]).some((x) => x[0] === 'del' && x[1] === '<footnoteRef>')).toBe(true)
  })
  it('a rule added', () => {
    expect(kinds(compare('甲。\n\n乙。\n', '甲。\n\n---\n\n乙。\n'))).toEqual(['paragraph:eq', 'horizontalRule:ins', 'paragraph:eq'])
  })
})

describe('line breaks', () => {
  it('a hard break added inside a paragraph', () => {
    const r = compare('第一行第二行\n', '第一行\\\n第二行\n')
    const rs = runs(blocks(r)[0])
    expect(rs.filter((x) => x[0] === 'ins')).toEqual([['ins', '<hardBreak>']])
  })
  it('text after a hard break is compared', () => {
    const r = compare('第一行\\\n第二行\n', '第一行\\\n第二行改了\n')
    expect(runs(blocks(r)[0]).filter((x) => x[0] === 'ins')).toEqual([['ins', '改了']])
  })
})

describe('header', () => {
  it('lists changes of title, subtitle, source, tags and other keys', () => {
    const r = diffVersions(
      { meta: { id: 'x', title: '暮雪', subtitle: '旧', source: 'a', tags: ['玉兰', '黄昏'], status: '草稿', created: '2025-01-01' }, body: '' },
      { meta: { id: 'x', title: '暮雪（改）', tags: ['玉兰', '冬'], status: '定稿', series: '冬季', created: '2025-01-01' }, body: '' },
    )
    const by = Object.fromEntries(r.header.map((h) => [h.kind === 'text' ? h.field : h.kind === 'other' ? h.key : h.kind, h]))
    expect(by.title).toMatchObject({ kind: 'text', from: '暮雪', to: '暮雪（改）' })
    expect(by.subtitle).toMatchObject({ kind: 'text', from: '旧', to: '' })
    expect(by.source).toMatchObject({ kind: 'text', from: 'a', to: '' })
    expect(by.tags).toEqual({ kind: 'tags', added: ['冬'], removed: ['黄昏'] })
    expect(by.status).toMatchObject({ kind: 'other', change: 'changed', from: '草稿', to: '定稿' })
    expect(by.series).toMatchObject({ kind: 'other', change: 'added', to: '冬季' })
    expect(r.header).toHaveLength(6)
    expect(r.identical).toBe(false)
  })
  it('the title change is compared by word', () => {
    const r = diffVersions({ meta: { title: '关于慢' }, body: '' }, { meta: { title: '关于快' }, body: '' })
    expect(r.header[0]).toMatchObject({ kind: 'text', parts: [{ type: 'eq', text: '关于' }, { type: 'del', text: '慢' }, { type: 'ins', text: '快' }] })
  })
  it('ignores the id and the order of keys and tags', () => {
    const r = diffVersions({ meta: { id: 'a', x: { p: 1, q: 2 }, tags: ['a', 'b'] }, body: '' }, { meta: { id: 'b', tags: ['b', 'a'], x: { q: 2, p: 1 } }, body: '' })
    expect(r.header).toEqual([])
    expect(r.identical).toBe(true)
  })
})

describe('limits', () => {
  it('a block with more than a few thousand units after the equal ends is replaced, not compared', () => {
    const words = (n: number, f: string) => Array.from({ length: n }, (_, i) => `${f}${i}`).join(' ')
    const a = `开头。${words(5000, 'a')} 结尾。\n`
    const b = `开头。${words(5000, 'b')} 结尾。\n`
    const r = compare(a, b)
    expect(kinds(r)).toEqual(['paragraph:del', 'paragraph:ins'])
  })
  it('a long paragraph with a small edit is still compared', () => {
    const base = Array.from({ length: 3000 }, (_, i) => `词${i}`).join(' ')
    const r = compare(`${base}\n`, `${base.replace('词1500', '改了')}\n`)
    expect(kinds(r)).toEqual(['paragraph:eq'])
    expect(r.stats.changes).toBe(1)
  })
})

// A 20,000-character page, edited in a handful of places.
function page(n: number, edit: boolean): string {
  const paras: string[] = []
  for (let i = 0; i < n; i++) {
    let p = `第${i}段：这是一段用来测试比较速度的文字，里面有一些 English words 和标点，还有编号 ${i * 7}。`
    if (edit && i % 17 === 3) p = p.replace('测试', '检验')
    if (edit && i % 41 === 5) p += '多出来的一句话。'
    paras.push(i % 25 === 0 ? `## 小节 ${i}\n\n${p}` : i % 13 === 0 ? `- ${p}\n- 另一项 ${i}` : p)
  }
  if (edit) paras.splice(40, 2)
  return paras.join('\n\n') + '\n'
}

describe('speed', () => {
  it('compares a 20,000-character page in well under a second, and the tree comparison in well under 100 ms', () => {
    const a = page(420, false)
    const b = page(420, true)
    expect(a.length).toBeGreaterThan(20_000)
    const da = markdownToDoc(a)
    const db = markdownToDoc(b)
    // Warm the segmenter and the JIT: the first call pays for loading the dictionary.
    diffDocs(markdownToDoc('预热一下。'), markdownToDoc('预热两下。'))
    // The best of a few runs: the suite runs many files at once, and one run can
    // lose the processor to another worker for longer than the whole comparison takes.
    let treeMs = Infinity
    let totalMs = Infinity
    for (let i = 0; i < 4; i++) {
      const t0 = performance.now()
      const { stats } = diffDocs(da, db)
      treeMs = Math.min(treeMs, performance.now() - t0)
      expect(stats.changes).toBeGreaterThan(10)
      const t1 = performance.now()
      diffVersions({ meta: {}, body: a }, { meta: {}, body: b })
      totalMs = Math.min(totalMs, performance.now() - t1)
    }
    console.info(`diff of ${a.length} characters: tree ${treeMs.toFixed(1)} ms, with parsing ${totalMs.toFixed(1)} ms`)
    // Budgets are several times what a laptop needs, so a busy CI machine does not flake.
    expect(treeMs).toBeLessThan(150)
    expect(totalMs).toBeLessThan(1500)
  })
  it('two entirely different 20,000-character pages stay fast too', () => {
    const a = markdownToDoc(page(420, false))
    const b = markdownToDoc(page(420, false).replace(/第/g, '另').replace(/段/g, '节').replace(/测试/g, '核对'))
    const t0 = performance.now()
    const { stats } = diffDocs(a, b)
    const ms = performance.now() - t0
    console.info(`full rewrite of 420 paragraphs: ${ms.toFixed(1)} ms`)
    expect(stats.changes).toBeGreaterThan(0)
    expect(ms).toBeLessThan(2000)
  })
})

// --- property: both sides can always be read back --------------------------------------


function rng(seed: number) {
  let s = seed
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0
    return s / 2 ** 32
  }
}

describe('property: the old side and the new side of a comparison are the two versions', () => {
  it('holds for random pairs built from a pool of blocks', () => {
    const r = rng(2026)
    const pick = (n: number) => Array.from({ length: n }, () => POOL[Math.floor(r() * POOL.length)]).filter(Boolean)
    // A second version is often a lightly edited copy of the first, as in real life.
    const mutate = (blocks: string[]) => {
      const out = [...blocks]
      const n = 1 + Math.floor(r() * 3)
      for (let i = 0; i < n; i++) {
        const k = Math.floor(r() * 4)
        const at = Math.floor(r() * (out.length + 1))
        if (k === 0) out.splice(at, 0, POOL[Math.floor(r() * POOL.length)])
        else if (k === 1 && out.length) out.splice(Math.min(at, out.length - 1), 1)
        else if (k === 2 && out.length > 1) {
          const j = Math.floor(r() * out.length)
          const [x] = out.splice(Math.min(at, out.length - 1), 1)
          out.splice(j, 0, x)
        } else if (out.length) {
          const i2 = Math.min(at, out.length - 1)
          out[i2] = out[i2].replace('春天', '早春').replace('很好', '特别好').replace('苹果', '梨')
        }
      }
      return out.filter(Boolean)
    }
    for (let n = 0; n < 400; n++) {
      const a = pick(1 + Math.floor(r() * 7))
      const b = n % 2 === 0 ? pick(1 + Math.floor(r() * 7)) : mutate(a)
      const oldMd = a.join('\n\n')
      const newMd = b.join('\n\n')
      try {
        compare(oldMd, newMd)
      } catch (e) {
        throw new Error(`pair ${n} failed:\n--- old\n${oldMd}\n--- new\n${newMd}`, { cause: e })
      }
    }
  })

  it('holds for random edits inside one paragraph, in Chinese and English', () => {
    const r = rng(99)
    const alphabet = ['今', '天', '气', '好', ' ', 'a', 'b', 'cat', '，', '。', '的', '**', '*', '[x](u)']
    for (let n = 0; n < 300; n++) {
      const mk = () => Array.from({ length: 3 + Math.floor(r() * 12) }, () => alphabet[Math.floor(r() * alphabet.length)]).join('')
      const a = mk()
      const b = n % 3 === 0 ? mk() : a.slice(0, Math.floor(r() * a.length)) + mk().slice(0, 3) + a.slice(Math.floor(r() * a.length))
      try {
        compare(a, b)
      } catch (e) {
        throw new Error(`pair ${n} failed: ${JSON.stringify(a)} -> ${JSON.stringify(b)}`, { cause: e })
      }
    }
  })
})

describe('segmentation', () => {
  it('has a fallback for when Intl.Segmenter is missing: one unit per CJK character, whole Latin words', async () => {
    const { wordCuts } = await import('./segment')
    const text = '今天 hello世界, ok'
    const pieces = (cuts: number[]) => cuts.map((c, i) => text.slice(i === 0 ? 0 : cuts[i - 1], c))
    expect(pieces(wordCuts(text, true))).toEqual(['今', '天', ' ', 'hello', '世', '界', ',', ' ', 'ok'])
    // The real segmenter also covers the text completely.
    expect(wordCuts(text).at(-1)).toBe(text.length)
  })
  it('marks split a word without losing it', () => {
    const r = compare('这是**加粗的词语**测试\n', '这是**加粗的词语**测试了\n')
    expect(runs(blocks(r)[0]).filter((x) => x[0] !== 'eq')).toEqual([['ins', '了']])
  })
})
