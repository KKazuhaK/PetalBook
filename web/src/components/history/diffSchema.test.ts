// The merged document of a comparison has to be a document the editor can
// hold: every block where its parent allows it. A merge that looks right as
// JSON but breaks the schema would be silently reshaped (or lost) by the
// editor, so this builds the editor's schema and checks.

import { describe, expect, it } from 'vitest'
import { getSchema } from '@tiptap/core'
import { createExtensions } from '@/markdown'
import { diffVersions, projectDoc } from '@/diff'
import { POOL } from '@/diff/pool'
import { describeBlock, describeMarks, diffExtensions } from './diffExtension'

const schema = getSchema([...createExtensions({ linkOpensOnClick: false }), ...diffExtensions])

function valid(oldMd: string, newMd: string) {
  const r = diffVersions({ meta: {}, body: oldMd }, { meta: {}, body: newMd })
  // `check` throws on a node whose content its type does not allow, or an unknown mark.
  schema.nodeFromJSON(r.doc).check()
  // And both sides, read back, are valid documents too.
  schema.nodeFromJSON(projectDoc(r.doc, 'old')).check()
  schema.nodeFromJSON(projectDoc(r.doc, 'new')).check()
  return r
}

function rng(seed: number) {
  let s = seed
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0
    return s / 2 ** 32
  }
}

describe("a merged document is valid in the editor's schema", () => {
  it('for the cases that reshape containers', () => {
    valid('- 一\n- 二\n- 三\n', '- [ ] 一\n- [ ] 三\n')
    valid('- [x] 一\n- [ ] 二\n', '1. 一\n2. 三\n')
    valid('| a | b |\n| - | - |\n| 1 | 2 |\n', '| a | b |\n| - | - |\n| 1 | 3 |\n| 4 | 5 |\n')
    valid('> 引用一段话，足够长。\n', '- 引用一段话，足够长。\n')
    valid('', '# 标题\n\n正文\n')
    valid('# 标题\n\n正文\n', '')
    valid('甲[^1]\n\n[^1]: 注\n', '乙[^1]\n\n[^1]: 另一个注\n')
    valid('- 水果\n  - 苹果\n  - 香蕉\n- 蔬菜\n', '- 水果\n  1. 苹果\n  2. 橙子\n- [ ] 蔬菜\n')
  })

  it('for random pairs of pages', () => {
    const r = rng(7)
    const pick = (n: number) => Array.from({ length: n }, () => POOL[Math.floor(r() * POOL.length)]).filter(Boolean)
    for (let n = 0; n < 300; n++) {
      const a = pick(1 + Math.floor(r() * 6)).join('\n\n')
      const b = pick(1 + Math.floor(r() * 6)).join('\n\n')
      try {
        valid(a, b)
      } catch (e) {
        throw new Error(`pair ${n}:\n--- old\n${a}\n--- new\n${b}`, { cause: e })
      }
    }
  })

  it('and the document round-trips through the schema without losing a change', () => {
    const r = valid('这是一段**普通**的好话。\n', '这是一段普通的新话。\n')
    const back = schema.nodeFromJSON(r.doc).toJSON()
    expect(JSON.stringify(back)).toContain('diffIns')
    expect(JSON.stringify(back)).toContain('diffDel')
    expect(JSON.stringify(back)).toContain('diffFmt')
  })
})

describe('what the hover text says', () => {
  it('names the old formatting', () => {
    expect(
      describeMarks(JSON.stringify([{ type: 'bold' }, { type: 'link', attrs: { href: 'https://a.example' } }])),
    ).toBe('加粗、链接 https://a.example')
    expect(describeMarks('[]')).toBe('没有格式')
    expect(describeMarks('nonsense')).toBe('没有格式')
  })
  it('names the old block', () => {
    expect(describeBlock(JSON.stringify({ type: 'heading', attrs: { level: 2 } }))).toBe('二级标题')
    expect(describeBlock(JSON.stringify({ type: 'taskItem', attrs: { checked: true } }))).toBe('已完成的待办项')
    expect(describeBlock(JSON.stringify({ type: 'bulletList' }))).toBe('无序列表')
    expect(describeBlock('nonsense')).toBe('')
  })
})

describe('what is drawn', () => {
  it('writes insertions, deletions and format changes as elements with a label for screen readers, and blocks with data attributes', async () => {
    const { Editor } = await import('@tiptap/core')
    const r = diffVersions(
      { meta: {}, body: '这是**一段**普通的好话。\n\n## 小节\n\n- 一\n- 二\n' },
      { meta: {}, body: '这是一段普通的新话。\n\n### 小节\n\n- 一\n- 二\n- 三\n' },
    )
    const editor = new Editor({
      extensions: [...createExtensions({ linkOpensOnClick: false }), ...diffExtensions],
      content: r.doc,
      editable: false,
    })
    const html = editor.getHTML()
    expect(html).toMatch(/<ins [^>]*class="pb-diff-ins"[^>]*data-sr="新增：">新<\/ins>/)
    expect(html).toMatch(/<del [^>]*class="pb-diff-del"[^>]*data-sr="删除：">好<\/del>/)
    expect(html).toMatch(/<span [^>]*class="pb-diff-fmt"[^>]*title="格式变化（原来：加粗）"/)
    expect(html).toMatch(/<h3 [^>]*data-diff="fmt"[^>]*title="格式变化（原来是二级标题）"/)
    expect(html).toMatch(/<li [^>]*data-diff="ins"[^>]*>/)
    editor.destroy()
  })

  it('the editor can take a version in place of its content and put the old content back', async () => {
    const { Editor } = await import('@tiptap/core')
    const { docToMarkdown, markdownToDoc } = await import('@/markdown')
    const editor = new Editor({ extensions: createExtensions(), content: markdownToDoc('现在的内容\n') })
    editor.commands.setContent(markdownToDoc('旧版本的内容\n'), { emitUpdate: false })
    expect(docToMarkdown(editor.getJSON())).toBe('旧版本的内容\n')
    // Whether Ctrl+Z also takes it back is the editor's doing; record what it does.
    editor.commands.undo()
    console.info('undo after setContent gives:', JSON.stringify(docToMarkdown(editor.getJSON())))
    editor.destroy()
  })
})
