// Syntax colours for code blocks, as inline decorations: the document is not
// touched, so the Markdown of a page does not know about them.
//
// The highlighter is loaded on demand (see highlighter.ts). Until it is here
// the blocks are drawn plain; when it arrives one transaction paints them all.
// After that an edit only re-highlights the code blocks it touched.

import { getChangedRanges } from '@tiptap/core'
import type { Node as PMNode } from '@tiptap/pm/model'
import { Plugin, PluginKey } from '@tiptap/pm/state'
import { Decoration, DecorationSet } from '@tiptap/pm/view'
import { highlightWord } from './languages'
import { currentHighlighter, loadHighlighter, spansOf, type Highlighter } from './highlighter'

// A block this long is shown plain: highlighting it on every keystroke would
// make typing in it lag, and nobody reads colours in a pasted log file.
export const MAX_HIGHLIGHTED_CHARS = 60_000

interface State {
  decorations: DecorationSet
  // A code block that could be coloured exists, but the highlighter is not loaded yet.
  wanted: boolean
}

const key = new PluginKey<State>('codeHighlight')

function blockDecorations(node: PMNode, pos: number, hl: Highlighter): Decoration[] {
  const word = highlightWord(node.attrs.language)
  if (!word || node.content.size > MAX_HIGHLIGHTED_CHARS || !hl.registered(word)) return []
  // The block holds one run of text, so an offset in the text is an offset in the document.
  return spansOf(hl.highlight(word, node.textContent)).map((s) => Decoration.inline(pos + 1 + s.from, pos + 1 + s.to, { class: s.className }))
}

export function codeHighlightPlugin(typeName: string) {
  const isCode = (node: PMNode) => node.type.name === typeName

  const paintAll = (doc: PMNode, hl: Highlighter): DecorationSet => {
    const out: Decoration[] = []
    doc.descendants((node, pos) => {
      if (!isCode(node)) return true
      out.push(...blockDecorations(node, pos, hl))
      return false
    })
    return DecorationSet.create(doc, out)
  }
  const anyWanted = (doc: PMNode, from = 0, to = doc.content.size) => {
    let found = false
    doc.nodesBetween(from, to, (node) => {
      if (found) return false
      if (isCode(node)) {
        found = highlightWord(node.attrs.language) !== null
        return false
      }
      return true
    })
    return found
  }

  return new Plugin<State>({
    key,
    state: {
      init: (_, { doc }) => {
        const hl = currentHighlighter()
        return hl ? { decorations: paintAll(doc, hl), wanted: false } : { decorations: DecorationSet.empty, wanted: anyWanted(doc) }
      },
      apply: (tr, state, _old, next) => {
        const hl = currentHighlighter()
        if (!hl) return tr.docChanged && !state.wanted ? { ...state, wanted: getChangedRanges(tr).some((r) => anyWanted(tr.doc, r.newRange.from, r.newRange.to)) } : state
        if (tr.getMeta(key) === 'ready') return { decorations: paintAll(next.doc, hl), wanted: false }
        if (!tr.docChanged) return state
        let decorations = state.decorations.map(tr.mapping, tr.doc)
        const touched = new Set<number>()
        for (const { newRange } of getChangedRanges(tr)) {
          tr.doc.nodesBetween(newRange.from, newRange.to, (node, pos) => {
            if (!isCode(node)) return true
            touched.add(pos)
            return false
          })
        }
        for (const pos of touched) {
          const node = tr.doc.nodeAt(pos)!
          decorations = decorations.remove(decorations.find(pos + 1, pos + node.nodeSize - 1)).add(tr.doc, blockDecorations(node, pos, hl))
        }
        return { decorations, wanted: false }
      },
    },
    props: { decorations: (state) => key.getState(state)?.decorations },
    view(view) {
      let destroyed = false
      let loading = false
      const load = () => {
        if (loading || currentHighlighter() || !key.getState(view.state)?.wanted) return
        loading = true
        loadHighlighter()
          .then(() => {
            if (!destroyed) view.dispatch(view.state.tr.setMeta(key, 'ready'))
          })
          // The page stays readable without colours.
          .catch(() => {
            loading = false
          })
      }
      load()
      return {
        update: load,
        destroy: () => {
          destroyed = true
        },
      }
    },
  })
}
