import { Fragment, memo, useEffect, useMemo, useState, type ReactNode } from 'react'
import { markdownToDoc, type DocJSON } from '@/markdown'
import { addressToUrl } from '@/lib/fileAddress'
import { ts } from '@/i18n/storage'
import { currentHighlighter, loadHighlighter, type HNode, type Root } from '@/components/editor/code/highlighter'
import { isDiagramLanguage } from '@/components/editor/code/diagrams'
import { highlightWord, languageLabel } from '@/components/editor/code/languages'
import { MAX_HIGHLIGHTED_CHARS } from '@/components/editor/code/highlightPlugin'
import { useDarkTheme, useDiagram } from '@/components/editor/code/useDiagram'
import { tb } from '@/i18n/boards'

// A page's text drawn as a read-only layout, for continuous reading. The Markdown is parsed to the document the
// editor would hold (the same parser, so the same reading of every construct) and drawn straight from that, with
// the classes the editor's page has, so a page looks the same here as when it is opened. No editor is mounted:
// an editor per page is what would make hundreds of them heavy.

type Node = DocJSON
type Mark = NonNullable<DocJSON['marks']>[number]

// What a link may point to: the schemes a page is expected to use. Anything else (`javascript:`, `data:`) is shown as text.
const SAFE_LINK = /^(https?:|mailto:|tel:|#|\/(?!\/)|[^:]*$)/i

interface Ctx {
  space: string
  folder: string
}

function text(node: Node, ctx: Ctx, key: number): ReactNode {
  let out: ReactNode = node.text ?? ''
  const marks = node.marks ?? []
  const has = (name: string) => marks.find((m: Mark) => m.type === name)
  if (has('code')) out = <code>{out}</code>
  if (has('bold')) out = <strong>{out}</strong>
  if (has('italic')) out = <em>{out}</em>
  if (has('strike')) out = <s>{out}</s>
  const link = has('link')
  if (link) {
    const href = String(link.attrs?.href ?? '')
    if (SAFE_LINK.test(href)) {
      // A relative address is a file of the space, as it is in the editor.
      const to = /^[a-z][a-z0-9+.-]*:|^\/\/|^#/i.test(href) ? href : addressToUrl(ctx.space, ctx.folder, href)
      out = (
        <a
          href={to}
          title={(link.attrs?.title as string | null) ?? undefined}
          target="_blank"
          rel="noopener noreferrer"
        >
          {out}
        </a>
      )
    }
  }
  return <Fragment key={key}>{out}</Fragment>
}

function Image({ node, ctx }: { node: Node; ctx: Ctx }) {
  const [broken, setBroken] = useState(false)
  const src = String(node.attrs?.src ?? '')
  return (
    <img
      src={addressToUrl(ctx.space, ctx.folder, src)}
      alt={(node.attrs?.alt as string | null) ?? ''}
      title={broken ? ts.assets.placeholder : ((node.attrs?.title as string | null) ?? undefined)}
      loading="lazy"
      data-broken={broken ? '' : undefined}
      onError={() => setBroken(true)}
    />
  )
}

const kids = (node: Node, ctx: Ctx): ReactNode[] => (node.content ?? []).map((c, i) => render(c, ctx, i))

function hast(nodes: HNode[]): ReactNode[] {
  return nodes.map((n, i) => {
    if (n.type === 'text') return n.value ?? ''
    if (n.type !== 'element') return null
    const cls = n.properties?.className
    return (
      <span key={i} className={Array.isArray(cls) ? cls.map(String).join(' ') : undefined}>
        {hast(n.children ?? [])}
      </span>
    )
  })
}

// The syntax colours of a block, once the highlighter (a chunk of its own) has loaded.
function useHighlighted(code: string, language: unknown): Root | null {
  const word = highlightWord(language)
  const wanted = word !== null && code.length <= MAX_HIGHLIGHTED_CHARS
  const [ready, setReady] = useState(() => currentHighlighter() !== null)
  useEffect(() => {
    if (!wanted || ready) return
    let live = true
    loadHighlighter().then(
      () => live && setReady(true),
      () => undefined,
    )
    return () => {
      live = false
    }
  }, [wanted, ready])
  const hl = ready ? currentHighlighter() : null
  return useMemo(
    () => (wanted && hl && hl.registered(word) ? hl.highlight(word, code) : null),
    [wanted, hl, word, code],
  )
}

function CodeBlock({ node }: { node: Node }) {
  const language = (node.attrs?.language as string | null) ?? null
  const code = (node.content ?? []).map((c) => c.text ?? '').join('')
  const diagram = isDiagramLanguage(language)
  const dark = useDarkTheme()
  const picture = useDiagram(diagram ? code : '', dark)
  const colours = useHighlighted(diagram ? '' : code, language)
  // A diagram that did not draw shows its code, so the error has something to point at.
  const showCode = !diagram || (picture.error !== null && picture.url === null) || code.trim() === ''
  return (
    <div className="pb-code" data-language={languageLabel(language) ?? undefined}>
      <div className="pb-code-bar">
        {languageLabel(language) && (
          <span className="px-1.5 text-xs text-muted-foreground">{languageLabel(language)}</span>
        )}
      </div>
      {showCode && (
        <pre spellCheck={false}>
          <code style={{ whiteSpace: 'pre' }}>{colours ? hast(colours.children) : code}</code>
        </pre>
      )}
      {diagram && (
        <div className="pb-diagram">
          {picture.error && (
            <pre role="alert" className="pb-diagram-error">
              {picture.error}
            </pre>
          )}
          {picture.url ? (
            <img src={picture.url} alt={tb.reading.diagram} draggable={false} />
          ) : (
            !picture.error && picture.busy && <p className="pb-diagram-note">…</p>
          )}
        </div>
      )}
    </div>
  )
}

function render(node: Node, ctx: Ctx, key: number): ReactNode {
  switch (node.type) {
    case 'text':
      return text(node, ctx, key)
    case 'paragraph':
      return <p key={key}>{kids(node, ctx)}</p>
    case 'heading': {
      const H = `h${Math.min(6, Math.max(1, Number(node.attrs?.level) || 1))}` as 'h1'
      return <H key={key}>{kids(node, ctx)}</H>
    }
    case 'bulletList':
      return <ul key={key}>{kids(node, ctx)}</ul>
    case 'orderedList':
      return (
        <ol key={key} start={Number(node.attrs?.start) || 1}>
          {kids(node, ctx)}
        </ol>
      )
    case 'listItem':
      return <li key={key}>{kids(node, ctx)}</li>
    case 'taskList':
      return (
        <ul key={key} data-type="taskList">
          {kids(node, ctx)}
        </ul>
      )
    case 'taskItem': {
      const checked = node.attrs?.checked === true
      return (
        <li key={key} data-type="taskItem" data-checked={checked}>
          <label>
            <input type="checkbox" checked={checked} disabled readOnly />
          </label>
          <div>{kids(node, ctx)}</div>
        </li>
      )
    }
    case 'blockquote':
      return <blockquote key={key}>{kids(node, ctx)}</blockquote>
    case 'codeBlock':
      return <CodeBlock key={key} node={node} />
    case 'horizontalRule':
      return <hr key={key} />
    case 'hardBreak':
      return <br key={key} />
    case 'image':
      return <Image key={key} node={node} ctx={ctx} />
    case 'table':
      return (
        <div key={key} className="tableWrapper">
          <table>
            <tbody>{kids(node, ctx)}</tbody>
          </table>
        </div>
      )
    case 'tableRow':
      return <tr key={key}>{kids(node, ctx)}</tr>
    case 'tableHeader':
    case 'tableCell': {
      const Cell = node.type === 'tableHeader' ? 'th' : 'td'
      const align = node.attrs?.align as string | null | undefined
      return (
        <Cell key={key} style={align ? { textAlign: align as 'left' } : undefined}>
          {kids(node, ctx)}
        </Cell>
      )
    }
    case 'rawBlock':
      return (
        <pre key={key} data-raw-block="">
          {String(node.attrs?.value ?? '')}
        </pre>
      )
    case 'rawInline':
      return (
        <code key={key} data-raw-inline="">
          {String(node.attrs?.value ?? '')}
        </code>
      )
    case 'footnoteRef':
      return (
        <sup key={key} data-footnote-ref={String(node.attrs?.id ?? '')}>
          {String(node.attrs?.id ?? '')}
        </sup>
      )
    case 'footnoteDef':
      return (
        <aside key={key} data-footnote-def={String(node.attrs?.id ?? '')}>
          {kids(node, ctx)}
        </aside>
      )
    default:
      return null
  }
}

// `folder` is where the page lives: the addresses of its images and links are relative to it.
export const StaticBody = memo(function StaticBody({
  markdown,
  space,
  folder,
}: {
  markdown: string
  space: string
  folder: string
}) {
  const doc = useMemo(() => {
    try {
      return markdownToDoc(markdown)
    } catch {
      return null
    }
  }, [markdown])
  return (
    <div className="pb-prose" data-slot="static-body">
      {doc ? kids(doc, { space, folder }) : <p>{markdown}</p>}
    </div>
  )
})
