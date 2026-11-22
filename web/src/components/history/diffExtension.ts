// What the read-only view of a comparison adds to the editor's own extensions:
// three marks for inline changes and three attributes for blocks. The names
// are the ones src/diff writes (see src/diff/doc.ts).
//
// Insertions and deletions are drawn with <ins> and <del>, which browsers and
// screen readers already know the meaning of; colour is never the only signal
// (see index.css: underline, strike-through, border style), and a hidden label
// in `data-sr` is read out where the change starts.

import { Extension, Mark } from '@tiptap/core'
import { t } from '@/i18n/zh-CN'

const MARK_NAMES: Record<string, string> = { bold: '加粗', italic: '斜体', strike: '删除线', code: '行内代码', link: '链接' }

// "加粗、链接 https://…" for the old marks of a text whose formatting changed.
export function describeMarks(json: unknown): string {
  let marks: { type: string; attrs?: { href?: string } }[] = []
  try {
    marks = JSON.parse(String(json ?? '[]'))
  } catch {
    // Unreadable: say nothing about it.
  }
  const parts = marks.map((m) => (m.type === 'link' && m.attrs?.href ? `链接 ${m.attrs.href}` : (MARK_NAMES[m.type] ?? m.type)))
  return parts.length ? parts.join('、') : '没有格式'
}

const BLOCK_NAMES: Record<string, string> = {
  paragraph: '正文',
  bulletList: '无序列表',
  orderedList: '有序列表',
  taskList: '待办列表',
  listItem: '列表项',
  blockquote: '引用',
}
const HEADING = ['', '一级标题', '二级标题', '三级标题', '四级标题', '五级标题', '六级标题']

// What a block used to be, in words.
export function describeBlock(json: unknown): string {
  try {
    const old = JSON.parse(String(json)) as { type: string; attrs?: Record<string, unknown> }
    if (old.type === 'heading') return HEADING[Number(old.attrs?.level)] ?? '标题'
    if (old.type === 'taskItem') return old.attrs?.checked ? '已完成的待办项' : '未完成的待办项'
    if (old.type === 'orderedList' && old.attrs?.start && old.attrs.start !== 1) return `从 ${String(old.attrs.start)} 开始的有序列表`
    return BLOCK_NAMES[old.type] ?? old.type
  } catch {
    return ''
  }
}

const inlineMark = (name: string, tag: 'ins' | 'del' | 'span', cls: string, sr: string) =>
  Mark.create({
    name,
    inclusive: false,
    // A change can be on text that has other marks.
    excludes: '',
    addAttributes: () => ({ old: { default: null, rendered: false } }),
    renderHTML: ({ mark }) => {
      const attrs: Record<string, string> = { class: cls, 'data-diff': tag === 'ins' ? 'ins' : tag === 'del' ? 'del' : 'fmt', 'data-sr': sr }
      if (name === 'diffFmt') attrs.title = `${t.history.formatted}（原来：${describeMarks(mark.attrs.old)}）`
      return [tag, attrs, 0]
    },
  })

export const DiffIns = inlineMark('diffIns', 'ins', 'pb-diff-ins', t.history.srAdded)
export const DiffDel = inlineMark('diffDel', 'del', 'pb-diff-del', t.history.srRemoved)
export const DiffFmt = inlineMark('diffFmt', 'span', 'pb-diff-fmt', t.history.srFormat)

const BLOCKS = ['paragraph', 'heading', 'blockquote', 'bulletList', 'orderedList', 'taskList', 'listItem', 'taskItem', 'codeBlock', 'horizontalRule', 'table', 'tableRow', 'tableCell', 'tableHeader', 'rawBlock', 'footnoteDef']

export const DiffBlocks = Extension.create({
  name: 'diffBlocks',
  addGlobalAttributes: () => [
    {
      types: BLOCKS,
      attributes: {
        diff: {
          default: null,
          parseHTML: () => null,
          renderHTML: (a) => {
            if (!a.diff) return {}
            const moved = Boolean(a.diffMove)
            const sr = moved ? (a.diff === 'del' ? t.history.srMovedAway : t.history.srMovedHere) : a.diff === 'ins' ? t.history.srBlockAdded : a.diff === 'del' ? t.history.srBlockRemoved : `${t.history.srBlockFormat}原来是${describeBlock(a.diffOld)}，`
            const title = moved ? (a.diff === 'del' ? t.history.movedFrom : t.history.movedTo) : a.diff === 'fmt' ? `${t.history.formatted}（原来是${describeBlock(a.diffOld)}）` : undefined
            return { 'data-diff': a.diff as string, 'data-sr': sr, ...(moved ? { 'data-diff-move': '' } : {}), ...(title ? { title } : {}) }
          },
        },
        diffOld: { default: null, parseHTML: () => null, renderHTML: () => ({}) },
        diffMove: { default: null, parseHTML: () => null, renderHTML: () => ({}) },
      },
    },
  ],
})

export const diffExtensions = [DiffIns, DiffDel, DiffFmt, DiffBlocks]
