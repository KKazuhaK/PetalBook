// The source view: the page's file as text, in a code editor with Markdown
// and YAML highlighting. It owns its text; the page editor reads changes the
// person makes and may replace the text from outside (which is not reported
// back as a change).

import { useEffect, useImperativeHandle, useRef, type Ref } from 'react'
import { Annotation, Compartment, EditorState, StateEffect, StateField } from '@codemirror/state'
import { Decoration, EditorView, drawSelection, highlightSpecialChars, keymap, type DecorationSet } from '@codemirror/view'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { markdown } from '@codemirror/lang-markdown'
import { yamlFrontmatter } from '@codemirror/lang-yaml'
import { tags as tg } from '@lezer/highlight'
import { minimalChange } from './fileText'

export interface SourceHandle {
  getText(): string
  // Replace the text from outside. Only what differs is touched, so the view
  // stays where it is.
  setText(text: string): void
  focus(): void
  hasFocus(): boolean
  // The (fractional, 1-based) line at the top of the view, and back.
  topLine(): number
  scrollToLine(line: number): void
  lineCount(): number
  scroller(): HTMLElement | null
}

interface Props {
  ref?: Ref<SourceHandle>
  // Read once, when the editor is built.
  getInitialText: () => string
  // The person changed the text.
  onChange: (text: string) => void
  onScroll?: () => void
  // A 1-based line to mark as wrong, or null.
  problemLine?: number | null
  label: string
  className?: string
  // The text can be looked at and selected, not changed.
  readOnly?: boolean
}

// Whether the text can be changed: it follows the person's permission while the view is open.
const access = (readOnly: boolean) => [
  // Both: no change can be made, and no caret or keyboard comes up on a phone.
  EditorState.readOnly.of(readOnly),
  EditorView.editable.of(!readOnly),
]

const fromOutside = Annotation.define<boolean>()
const setProblem = StateEffect.define<number | null>()
const problemMark = Decoration.line({ class: 'cm-pb-problem' })
const problemField = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update(deco, tr) {
    deco = deco.map(tr.changes)
    for (const e of tr.effects) {
      if (!e.is(setProblem)) continue
      const n = e.value
      deco = n !== null && n >= 1 && n <= tr.state.doc.lines ? Decoration.set([problemMark.range(tr.state.doc.line(n).from)]) : Decoration.none
    }
    return deco
  },
  provide: (f) => EditorView.decorations.from(f),
})

// Colours come from the app's own variables, so both themes work.
const theme = EditorView.theme({
  '&': { height: '100%', color: 'var(--foreground)', backgroundColor: 'transparent', fontSize: '0.875rem' },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': { fontFamily: 'var(--font-mono)', lineHeight: '1.75', overflow: 'auto' },
  '.cm-content': { padding: '1.5rem 1.25rem 6rem', caretColor: 'var(--foreground)', maxWidth: '50rem', margin: '0 auto' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--foreground)' },
  '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': { backgroundColor: 'color-mix(in oklch, var(--brand) 22%, transparent)' },
  '.cm-pb-problem': { backgroundColor: 'color-mix(in oklch, var(--destructive) 14%, transparent)' },
})
const highlight = HighlightStyle.define([
  { tag: tg.heading, fontWeight: '650' },
  { tag: tg.strong, fontWeight: '650' },
  { tag: tg.emphasis, fontStyle: 'italic' },
  { tag: tg.strikethrough, textDecoration: 'line-through' },
  { tag: [tg.link, tg.url], color: 'var(--brand)' },
  { tag: [tg.monospace], color: 'var(--muted-foreground)' },
  { tag: [tg.processingInstruction, tg.meta, tg.punctuation, tg.contentSeparator, tg.quote], color: 'var(--muted-foreground)' },
  { tag: [tg.propertyName, tg.definition(tg.propertyName)], color: 'var(--brand)' },
  { tag: tg.comment, color: 'var(--muted-foreground)', fontStyle: 'italic' },
])

export default function SourceEditor({ ref, getInitialText, onChange, onScroll, problemLine = null, label, className, readOnly = false }: Props) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const accessSlot = useRef(new Compartment())
  // The latest callbacks, without rebuilding the editor when they change.
  const cb = useRef({ onChange, onScroll })
  useEffect(() => {
    cb.current = { onChange, onScroll }
  })

  useEffect(() => {
    const v = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: getInitialText(),
        extensions: [
          history(),
          drawSelection(),
          highlightSpecialChars(),
          EditorView.lineWrapping,
          accessSlot.current.of(access(readOnly)),
          keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
          yamlFrontmatter({ content: markdown() }),
          syntaxHighlighting(highlight),
          problemField,
          theme,
          EditorView.contentAttributes.of({ 'aria-label': label, spellcheck: 'false', autocapitalize: 'off' }),
          EditorView.updateListener.of((u) => {
            if (u.docChanged && !u.transactions.some((t) => t.annotation(fromOutside))) cb.current.onChange(u.state.doc.toString())
          }),
        ],
      }),
    })
    const onScrollEvent = () => cb.current.onScroll?.()
    v.scrollDOM.addEventListener('scroll', onScrollEvent, { passive: true })
    view.current = v
    return () => {
      v.scrollDOM.removeEventListener('scroll', onScrollEvent)
      v.destroy()
      view.current = null
    }
    // The editor is built once; text from outside arrives through setText.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Not a change of the text: nothing is reported, nothing is marked unsaved.
  useEffect(() => {
    view.current?.dispatch({ effects: accessSlot.current.reconfigure(access(readOnly)) })
  }, [readOnly])

  useEffect(() => {
    view.current?.dispatch({ effects: setProblem.of(problemLine) })
  }, [problemLine])

  useImperativeHandle(
    ref,
    (): SourceHandle => ({
      getText: () => view.current?.state.doc.toString() ?? '',
      setText(text) {
        const v = view.current
        if (!v) return
        const change = minimalChange(v.state.doc.toString(), text)
        if (change) v.dispatch({ changes: change, annotations: fromOutside.of(true) })
      },
      focus: () => view.current?.focus(),
      hasFocus: () => view.current?.hasFocus ?? false,
      topLine() {
        const v = view.current
        if (!v) return 1
        const block = v.lineBlockAtHeight(v.scrollDOM.scrollTop)
        const n = v.state.doc.lineAt(block.from).number
        return n + (block.height > 0 ? Math.min(1, Math.max(0, (v.scrollDOM.scrollTop - block.top) / block.height)) : 0)
      },
      scrollToLine(line) {
        const v = view.current
        if (!v) return
        const n = Math.min(Math.max(Math.floor(line), 1), v.state.doc.lines)
        const block = v.lineBlockAt(v.state.doc.line(n).from)
        v.scrollDOM.scrollTop = block.top + (line - Math.floor(line)) * block.height
      },
      lineCount: () => view.current?.state.doc.lines ?? 1,
      scroller: () => view.current?.scrollDOM ?? null,
    }),
    [],
  )

  return <div ref={host} className={className} />
}
