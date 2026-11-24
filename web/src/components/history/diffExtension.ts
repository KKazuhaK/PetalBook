// What the read-only view of a comparison adds to the editor's own extensions:
// three marks for inline changes and three attributes for blocks. The names
// are the ones src/diff writes (see src/diff/doc.ts).
//
// Insertions and deletions are drawn with <ins> and <del>, which browsers and
// screen readers already know the meaning of; colour is never the only signal
// (see index.css: underline, strike-through, border style), and a hidden label
// in `data-sr` is read out where the change starts.

import { Extension, Mark } from '@tiptap/core'
import { t } from '@/i18n'

const markName = (type: string): string => {
  const d = t.history.describe
  switch (type) {
    case 'bold':
      return d.bold
    case 'italic':
      return d.italic
    case 'strike':
      return d.strike
    case 'code':
      return d.code
    case 'link':
      return d.link
    default:
      return type
  }
}

// "bold, link https://…" for the old marks of a text whose formatting changed.
export function describeMarks(json: unknown): string {
  let marks: { type: string; attrs?: { href?: string } }[] = []
  try {
    marks = JSON.parse(String(json ?? '[]'))
  } catch {
    // Unreadable: say nothing about it.
  }
  const parts = marks.map((m) =>
    m.type === 'link' && m.attrs?.href ? t.history.describe.linkTo(m.attrs.href) : markName(m.type),
  )
  return parts.length ? t.fmt.list(parts) : t.history.describe.noFormat
}

const blockName = (type: string): string | undefined => {
  const d = t.history.describe
  switch (type) {
    case 'paragraph':
      return d.paragraph
    case 'bulletList':
      return d.bulletList
    case 'orderedList':
      return d.orderedList
    case 'taskList':
      return d.taskList
    case 'listItem':
      return d.listItem
    case 'blockquote':
      return d.blockquote
    default:
      return undefined
  }
}

// What a block used to be, in words.
export function describeBlock(json: unknown): string {
  const d = t.history.describe
  try {
    const old = JSON.parse(String(json)) as { type: string; attrs?: Record<string, unknown> }
    if (old.type === 'heading') return d.heading(Number(old.attrs?.level))
    if (old.type === 'taskItem') return old.attrs?.checked ? d.taskDone : d.taskOpen
    if (old.type === 'orderedList' && old.attrs?.start && old.attrs.start !== 1)
      return d.orderedFrom(Number(old.attrs.start))
    return blockName(old.type) ?? old.type
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
      const attrs: Record<string, string> = {
        class: cls,
        'data-diff': tag === 'ins' ? 'ins' : tag === 'del' ? 'del' : 'fmt',
        'data-sr': sr,
      }
      if (name === 'diffFmt') attrs.title = t.history.formattedWas(describeMarks(mark.attrs.old))
      return [tag, attrs, 0]
    },
  })

export const DiffIns = inlineMark('diffIns', 'ins', 'pb-diff-ins', t.history.srAdded)
export const DiffDel = inlineMark('diffDel', 'del', 'pb-diff-del', t.history.srRemoved)
export const DiffFmt = inlineMark('diffFmt', 'span', 'pb-diff-fmt', t.history.srFormat)

const BLOCKS = [
  'paragraph',
  'heading',
  'blockquote',
  'bulletList',
  'orderedList',
  'taskList',
  'listItem',
  'taskItem',
  'codeBlock',
  'horizontalRule',
  'table',
  'tableRow',
  'tableCell',
  'tableHeader',
  'rawBlock',
  'footnoteDef',
]

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
            const sr = moved
              ? a.diff === 'del'
                ? t.history.srMovedAway
                : t.history.srMovedHere
              : a.diff === 'ins'
                ? t.history.srBlockAdded
                : a.diff === 'del'
                  ? t.history.srBlockRemoved
                  : t.history.srFormatWas(describeBlock(a.diffOld))
            const title = moved
              ? a.diff === 'del'
                ? t.history.movedFrom
                : t.history.movedTo
              : a.diff === 'fmt'
                ? t.history.formattedWasBlock(describeBlock(a.diffOld))
                : undefined
            return {
              'data-diff': a.diff as string,
              'data-sr': sr,
              ...(moved ? { 'data-diff-move': '' } : {}),
              ...(title ? { title } : {}),
            }
          },
        },
        diffOld: { default: null, parseHTML: () => null, renderHTML: () => ({}) },
        diffMove: { default: null, parseHTML: () => null, renderHTML: () => ({}) },
      },
    },
  ],
})

export const diffExtensions = [DiffIns, DiffDel, DiffFmt, DiffBlocks]
