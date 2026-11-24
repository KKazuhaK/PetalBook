import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it, vi } from 'vitest'
import { markdownToDoc } from '@/markdown'
import { DiffView } from './DiffView'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// Diagrams are not drawn under jsdom.
vi.mock('@/components/editor/code/mermaidRender', () => ({ renderDiagram: () => new Promise<string>(() => undefined) }))

describe('DiffView images', () => {
  it('draws images from the mapped address, relative ones from the folder of the page', async () => {
    const doc = markdownToDoc('![a](/assets/01X/a.png)\n\n![b](b.png)\n\n![c](https://example.com/c.png)\n')
    const host = document.createElement('div')
    document.body.appendChild(host)
    const root = createRoot(host)
    await act(async () => {
      root.render(<DiffView doc={doc} label="x" onRendered={() => undefined} space="default" folder="notes" />)
    })
    const srcs = Array.from(host.querySelectorAll('img:not(.ProseMirror-separator)')).map((i) => i.getAttribute('src'))
    expect(srcs).toEqual([
      '/api/spaces/default/files/assets/01X/a.png',
      '/api/spaces/default/files/notes/b.png',
      'https://example.com/c.png',
    ])
    await act(async () => root.unmount())
    host.remove()
  })
})

describe('DiffView code blocks', () => {
  it('draw colours, the language and a diagram like the page does, and mark a code block that is new', async () => {
    const { diffVersions } = await import('@/diff')
    const { loadHighlighter } = await import('@/components/editor/code/highlighter')
    await loadHighlighter()
    const result = diffVersions(
      { meta: {}, body: '正文\n' },
      { meta: {}, body: '正文\n\n```js\nconst a = 1\n```\n\n```mermaid\ngraph TD\nA-->B\n```\n' },
    )
    const host = document.createElement('div')
    document.body.appendChild(host)
    const root = createRoot(host)
    await act(async () => {
      root.render(<DiffView doc={result.doc} label="x" onRendered={() => undefined} />)
    })
    const blocks = Array.from(host.querySelectorAll('.pb-code'))
    expect(blocks).toHaveLength(2)
    // The change marks of the block are on the box that draws it.
    expect(blocks.every((b) => b.getAttribute('data-diff') === 'ins')).toBe(true)
    expect(blocks[0].textContent).toContain('JavaScript')
    expect(blocks[0].querySelector('.hljs-keyword')?.textContent).toBe('const')
    // Read-only: a label, not a picker; copy and the diagram switch remain.
    expect(host.querySelector('[aria-label^="代码语言"]')).toBeNull()
    expect(blocks[1].querySelector('.pb-diagram')).not.toBeNull()
    expect(blocks[1].querySelector('[aria-label="复制代码"]')).not.toBeNull()
    await act(async () => root.unmount())
    host.remove()
  })
})
