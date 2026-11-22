import { describe, expect, it, vi } from 'vitest'
import { Editor } from '@tiptap/core'
import { extensions, markdownToDoc, docToMarkdown } from '@/markdown'
import { replaceDoc } from './replaceDoc'

const make = (md: string, onUpdate = vi.fn()) => new Editor({ extensions, content: markdownToDoc(md), onUpdate })

describe('replaceDoc', () => {
  it('ends with the new document, whatever changed', () => {
    const cases: [string, string][] = [
      ['一\n\n二\n\n三\n', '一\n\n二改\n\n三\n'],
      ['一\n\n二\n\n三\n', '一\n\n三\n'],
      ['一\n', '零\n\n一\n\n二\n'],
      ['一\n\n二\n', '# 全换\n\n- a\n- b\n'],
      ['一\n\n一\n\n一\n', '一\n\n一\n'],
      ['一\n', '一\n'],
    ]
    for (const [from, to] of cases) {
      const ed = make(from)
      replaceDoc(ed, markdownToDoc(to))
      expect(docToMarkdown(ed.getJSON()), `${from} -> ${to}`).toBe(docToMarkdown(markdownToDoc(to)))
      ed.destroy()
    }
  })
  it('reports no update and keeps a position in an unchanged block where it was', () => {
    const onUpdate = vi.fn()
    const ed = make('第一段\n\n第二段\n\n第三段\n', onUpdate)
    // A position inside the third paragraph.
    const before = ed.state.doc.content.size - 2
    let mapped = before
    ed.on('transaction', ({ transaction }) => {
      mapped = transaction.mapping.map(mapped)
    })
    replaceDoc(ed, markdownToDoc('第一段，加长了很多字\n\n第二段\n\n第三段\n'))
    expect(onUpdate).not.toHaveBeenCalled()
    expect(ed.state.doc.textBetween(mapped, mapped + 1)).toBe('段')
    expect(mapped).toBe(ed.state.doc.content.size - 2)
    ed.destroy()
  })
})
