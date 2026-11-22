// Keeps every table of the page expressible in Markdown while it is edited
// (see tableOps.ts). The page editor adds it; the Markdown tests and the
// history view do not need it, since documents they build are already right.

import { Extension, getChangedRanges } from '@tiptap/core'
import { Plugin, PluginKey } from '@tiptap/pm/state'
import { Mapping } from '@tiptap/pm/transform'
import { tableFixes } from './tableOps'

export const TableTools = Extension.create({
  name: 'tableTools',
  addProseMirrorPlugins() {
    return [
      new Plugin({
        key: new PluginKey('tableTools'),
        appendTransaction(transactions, _old, state) {
          if (!transactions.some((tr) => tr.docChanged)) return null
          // Where the edits ended up, in the final document: tables elsewhere are not looked at.
          const spans: [number, number][] = []
          transactions.forEach((tr, i) => {
            if (!tr.docChanged) return
            const later = new Mapping()
            transactions.slice(i + 1).forEach((next) => later.appendMapping(next.mapping))
            for (const { newRange } of getChangedRanges(tr)) spans.push([later.map(newRange.from, -1), later.map(newRange.to, 1)])
          })
          const seen = new Set<number>()
          const tr = state.tr
          for (const [from, to] of spans) {
            state.doc.nodesBetween(Math.max(from, 0), Math.min(to, state.doc.content.size), (node, pos) => {
              if (node.type.name !== 'table') return true
              if (seen.has(pos)) return false
              seen.add(pos)
              for (const fix of tableFixes(node, pos)) {
                const cell = state.doc.nodeAt(fix.pos)
                // Only ever a cell: turning anything else into one would not be a valid document.
                if (!cell || (cell.type.name !== 'tableCell' && cell.type.name !== 'tableHeader')) continue
                tr.setNodeMarkup(fix.pos, state.schema.nodes[fix.type], { ...cell.attrs, align: fix.align })
              }
              return false
            })
          }
          return tr.docChanged || tr.steps.length ? tr : null
        },
      }),
    ]
  },
})
