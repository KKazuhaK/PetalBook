import { act } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRoot } from 'react-dom/client'
import { cleanup, click, markdownOf, mount, pageEditor } from '../pageTestKit'
import { useDiagram } from './useDiagram'

// The drawing itself is not run here (it needs a real browser); the renderer is replaced.
const renderDiagram = vi.fn<(text: string, dark: boolean) => Promise<string>>()
vi.mock('./mermaidRender', () => ({ renderDiagram: (text: string, dark: boolean) => renderDiagram(text, dark) }))

const diagram = (code: string) => '```mermaid\n' + code + '\n```\n'
const flush = () => act(async () => void (await Promise.resolve()))

beforeEach(() => {
  renderDiagram.mockReset()
  renderDiagram.mockImplementation(async (text) => `data:image/svg+xml,${encodeURIComponent(`<svg>${text}</svg>`)}`)
})
afterEach(async () => {
  vi.useRealTimers()
  await cleanup()
})

describe('a mermaid block in the editor', () => {
  it('is drawn as the picture, with the code behind a switch', async () => {
    const editor = pageEditor(diagram('graph TD\nA-->B'))
    const view = await mount(editor)
    await flush()
    // vi.waitFor has its own one-second default, which the poll setting in
    // vite.config.ts does not reach.
    await vi.waitFor(() => expect(view.host.querySelector('.pb-diagram img')).not.toBeNull(), { timeout: 5_000 })
    const code = view.host.querySelector('pre')!
    expect(code.hidden).toBe(true)
    expect(renderDiagram).toHaveBeenCalledWith('graph TD\nA-->B', false)

    const toggle = view.host.querySelector<HTMLButtonElement>('button[aria-pressed]')!
    expect(toggle.getAttribute('aria-label')).toBe('显示代码')
    await click(toggle)
    expect(view.host.querySelector('pre')!.hidden).toBe(false)
    expect(toggle.getAttribute('aria-pressed')).toBe('true')
    await click(toggle)
    expect(view.host.querySelector('pre')!.hidden).toBe(true)
    await view.unmount()
  })

  it('stays an ordinary code block in the document', async () => {
    const source = diagram('graph TD\nA-->B')
    const editor = pageEditor(source)
    const view = await mount(editor)
    expect(editor.getJSON().content?.[0]).toMatchObject({ type: 'codeBlock', attrs: { language: 'mermaid' } })
    expect(markdownOf(editor)).toBe(source)
    await view.unmount()
  })

  it('shows the code of an empty diagram, so there is somewhere to write', async () => {
    const editor = pageEditor('```mermaid\n```\n')
    const view = await mount(editor)
    expect(view.host.querySelector('pre')!.hidden).toBe(false)
    expect(renderDiagram).not.toHaveBeenCalled()
    await view.unmount()
  })

  it('shows the code again when the cursor goes into it', async () => {
    const editor = pageEditor('开头\n\n' + diagram('graph TD\nA-->B'))
    const view = await mount(editor)
    expect(view.host.querySelector('pre')!.hidden).toBe(true)
    await act(async () => void editor.commands.setTextSelection(editor.state.doc.content.size - 3))
    expect(view.host.querySelector('pre')!.hidden).toBe(false)
    await view.unmount()
  })

  it('has no language picker or only the label when it cannot be edited, but still copies', async () => {
    const editor = pageEditor(diagram('graph TD\nA-->B'))
    editor.setEditable(false)
    const view = await mount(editor)
    expect(view.host.querySelector('[aria-label^="代码语言"]')).toBeNull()
    expect(view.host.textContent).toContain('Mermaid')
    expect(view.host.querySelector('[aria-label="复制代码"]')).not.toBeNull()
    await view.unmount()
  })
})

// A component that shows what the hook returns.
function Probe({ text, dark = false }: { text: string; dark?: boolean }) {
  const d = useDiagram(text, dark, 500)
  return <p data-url={d.url ?? ''} data-error={d.error ?? ''} data-busy={String(d.busy)} />
}

async function probe(text: string) {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  const set = (next: string, dark = false) => act(async () => root.render(<Probe text={next} dark={dark} />))
  await set(text)
  const read = () => host.querySelector('p')!.dataset
  return { read, set }
}

describe('keeping a diagram up to date', () => {
  it('draws the first text at once and later edits after a pause', async () => {
    vi.useFakeTimers()
    const p = await probe('A')
    await act(async () => void (await vi.advanceTimersByTimeAsync(0)))
    expect(renderDiagram).toHaveBeenCalledTimes(1)
    expect(p.read().url).toContain('A')

    await p.set('AB')
    await p.set('ABC')
    await act(async () => void (await vi.advanceTimersByTimeAsync(499)))
    expect(renderDiagram).toHaveBeenCalledTimes(1)
    await act(async () => void (await vi.advanceTimersByTimeAsync(2)))
    // One redraw for the two keystrokes.
    expect(renderDiagram).toHaveBeenCalledTimes(2)
    expect(renderDiagram).toHaveBeenLastCalledWith('ABC', false)
    expect(p.read().url).toContain('ABC')
  })

  it('shows the error and keeps the last good picture when the text does not draw', async () => {
    vi.useFakeTimers()
    const p = await probe('good')
    await act(async () => void (await vi.advanceTimersByTimeAsync(0)))
    const good = p.read().url
    expect(good).toContain('good')

    renderDiagram.mockRejectedValueOnce(new Error('Parse error on line 2'))
    await p.set('bad ->')
    await act(async () => void (await vi.advanceTimersByTimeAsync(600)))
    expect(p.read().error).toBe('Parse error on line 2')
    expect(p.read().url).toBe(good)

    await p.set('good again')
    await act(async () => void (await vi.advanceTimersByTimeAsync(600)))
    expect(p.read().error).toBe('')
    expect(p.read().url).toContain('good%20again')
  })

  it('does not let a slow, older drawing replace a newer one', async () => {
    vi.useFakeTimers()
    let release!: (url: string) => void
    renderDiagram.mockImplementationOnce(() => new Promise<string>((r) => (release = r)))
    const p = await probe('old')
    await act(async () => void (await vi.advanceTimersByTimeAsync(0)))
    await p.set('new')
    await act(async () => void (await vi.advanceTimersByTimeAsync(600)))
    expect(p.read().url).toContain('new')
    await act(async () => release('data:old'))
    expect(p.read().url).toContain('new')
  })

  it('draws nothing for an empty text, and redraws when the theme changes', async () => {
    vi.useFakeTimers()
    const p = await probe('  ')
    await act(async () => void (await vi.advanceTimersByTimeAsync(1000)))
    expect(renderDiagram).not.toHaveBeenCalled()
    expect(p.read().url).toBe('')

    await p.set('A')
    await act(async () => void (await vi.advanceTimersByTimeAsync(0)))
    await p.set('A', true)
    await act(async () => void (await vi.advanceTimersByTimeAsync(600)))
    expect(renderDiagram).toHaveBeenLastCalledWith('A', true)
  })
})
