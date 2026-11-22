// The code block of the page editor and of the history view: the stock node
// (same name, same attributes, so the Markdown is exactly what it was) drawn
// with a language picker, a copy button, diagrams and syntax colours.

import { CodeBlock } from '@tiptap/extension-code-block'
import { ReactNodeViewRenderer } from '@tiptap/react'
import { CodeBlockView } from './CodeBlockView'
import { codeHighlightPlugin } from './highlightPlugin'
import { withLanguage } from './languages'

declare module '@tiptap/core' {
  interface Commands<ReturnType> {
    codeBlockLanguage: {
      // Sets the language of the code block the selection is in (null: plain text).
      setCodeBlockLanguage: (id: string | null) => ReturnType
    }
  }
}

export const CodeBlockWithView = CodeBlock.extend({
  addCommands() {
    return {
      ...this.parent?.(),
      setCodeBlockLanguage:
        (id) =>
        ({ state, tr, dispatch }) => {
          const { $from } = state.selection
          for (let depth = $from.depth; depth > 0; depth--) {
            const node = $from.node(depth)
            if (node.type.name !== this.name) continue
            if (dispatch) dispatch(tr.setNodeMarkup($from.before(depth), undefined, { ...node.attrs, language: withLanguage(node.attrs.language, id) }))
            return true
          }
          return false
        },
    }
  },
  addProseMirrorPlugins() {
    return [...(this.parent?.() ?? []), codeHighlightPlugin(this.name)]
  },
  addNodeView() {
    return ReactNodeViewRenderer(CodeBlockView)
  },
  // Tab indents inside code instead of moving the focus out of the page.
}).configure({ enableTabIndentation: true, tabSize: 2 })
