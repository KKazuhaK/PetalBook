import { useEffect, useRef, useState } from 'react'
import { NodeViewContent, NodeViewWrapper, useEditorState, type NodeViewProps } from '@tiptap/react'
import { Check, Code2, Copy } from 'lucide-react'
import { t } from '@/i18n'
import { isDiagramLanguage } from './diagrams'
import { LanguagePicker } from './LanguagePicker'
import { languageLabel, withLanguage } from './languages'
import { useDarkTheme, useDiagram, type DiagramState } from './useDiagram'

async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

function CopyButton({ getText }: { getText: () => string }) {
  const [state, setState] = useState<'idle' | 'done' | 'failed'>('idle')
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined)
  useEffect(() => () => clearTimeout(timer.current), [])
  const label = state === 'done' ? t.code.copied : state === 'failed' ? t.code.copyFailed : t.code.copy
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      onMouseDown={(e) => e.preventDefault()}
      onClick={async () => {
        setState((await copyText(getText())) ? 'done' : 'failed')
        clearTimeout(timer.current)
        timer.current = setTimeout(() => setState('idle'), 1600)
      }}
      className="inline-flex size-6 items-center justify-center rounded-md text-muted-foreground outline-none hover:bg-background/60 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
    >
      {state === 'done' ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
    </button>
  )
}

// The picture of a diagram block, and what went wrong when its text does not draw.
function Diagram({ text, state: { url, error, busy } }: { text: string; state: DiagramState }) {
  const empty = text.trim() === ''
  return (
    <div contentEditable={false} className="pb-diagram">
      {error && (
        // Under the code, with the picture of the last text that drew still below it.
        <pre role="alert" className="pb-diagram-error">
          {error}
        </pre>
      )}
      {url ? (
        <img src={url} alt={t.code.diagram} draggable={false} className={error ? 'opacity-60' : undefined} />
      ) : (
        !error && <p className="pb-diagram-note">{empty ? t.code.diagramEmpty : busy ? t.code.diagramRendering : ''}</p>
      )}
    </div>
  )
}

// A code block: language and copy at the top right, the code itself editable
// in place. A block in the language `mermaid` is a diagram with a switch to the code.
export function CodeBlockView({ node, editor, getPos, updateAttributes, HTMLAttributes }: NodeViewProps) {
  const language = node.attrs.language as string | null
  const diagram = isDiagramLanguage(language)
  // Follows the editor when the person's permission changes while the page is open.
  const editable = useEditorState({ editor, selector: ({ editor: e }) => e.isEditable })
  const text = node.textContent
  const dark = useDarkTheme()
  // Nothing is drawn for a block that is not a diagram.
  const picture = useDiagram(diagram ? text : '', dark)
  const [showCode, setShowCode] = useState(() => !diagram || text.trim() === '')

  // A cursor that arrives in hidden code (arrow keys, undo) must be able to see where it is.
  useEffect(() => {
    if (!diagram) return
    const onSelect = () => {
      const pos = typeof getPos === 'function' ? getPos() : undefined
      if (pos === undefined) return
      const { from, to } = editor.state.selection
      if (from > pos && to < pos + node.nodeSize) setShowCode(true)
    }
    editor.on('selectionUpdate', onSelect)
    return () => void editor.off('selectionUpdate', onSelect)
  }, [editor, getPos, node.nodeSize, diagram])

  // The page's attributes for a changed block in the history view (see history/diffExtension.ts).
  const marks = Object.fromEntries(
    Object.entries(HTMLAttributes).filter(([k]) => k.startsWith('data-') || k === 'title'),
  )
  // A text that never drew shows its code, so the error has something to point at.
  const forced = diagram && picture.error !== null && picture.url === null
  const shown = diagram ? showCode || forced : true

  return (
    <NodeViewWrapper className="pb-code" data-language={languageLabel(language) ?? undefined} {...marks}>
      <div contentEditable={false} className="pb-code-bar">
        {editable ? (
          <LanguagePicker
            value={language}
            onPick={(id) => updateAttributes({ language: withLanguage(language, id) })}
            onClosed={() => editor.commands.focus(null, { scrollIntoView: false })}
          />
        ) : (
          languageLabel(language) && (
            <span className="px-1.5 text-xs text-muted-foreground">{languageLabel(language)}</span>
          )
        )}
        {diagram && (
          <button
            type="button"
            title={shown ? t.code.hideCode : t.code.showCode}
            aria-label={shown ? t.code.hideCode : t.code.showCode}
            aria-pressed={shown}
            disabled={forced}
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => setShowCode((v) => !v)}
            className={`inline-flex size-6 items-center justify-center rounded-md outline-none hover:bg-background/60 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-50 ${shown ? 'bg-background/60 text-foreground' : 'text-muted-foreground'}`}
          >
            <Code2 className="size-3.5" />
          </button>
        )}
        <CopyButton getText={() => node.textContent} />
      </div>
      {/* Kept mounted while hidden: it is where the editor keeps the text. */}
      <pre spellCheck={false} hidden={!shown}>
        <NodeViewContent<'code'> as="code" style={{ whiteSpace: 'pre' }} />
      </pre>
      {diagram && <Diagram text={text} state={picture} />}
    </NodeViewWrapper>
  )
}
