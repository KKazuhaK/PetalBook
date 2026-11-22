// @vitest-environment jsdom
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { act, createRef } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import SourceEditor, { type SourceHandle } from './SourceEditor'

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  if (!Range.prototype.getClientRects) {
    Range.prototype.getClientRects = () => [] as unknown as DOMRectList
    Range.prototype.getBoundingClientRect = () => new DOMRect()
  }
})

let root: Root | undefined
let host: HTMLElement | undefined
afterEach(async () => {
  await act(async () => root?.unmount())
  host?.remove()
  root = undefined
})

async function show(readOnly: boolean) {
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  const ref = createRef<SourceHandle>()
  const onChange = vi.fn()
  const render = (ro: boolean) => act(async () => root!.render(<SourceEditor ref={ref} getInitialText={() => 'hello'} onChange={onChange} label="source" readOnly={ro} />))
  await render(readOnly)
  const content = () => host!.querySelector<HTMLElement>('.cm-content')!
  return { render, content, onChange, ref }
}

describe('the source view follows the permission while it is open', () => {
  it('a viewer who gains write can type, and the switch itself reports nothing', async () => {
    const s = await show(true)
    expect(s.content().getAttribute('contenteditable')).toBe('false')
    await s.render(false)
    expect(s.content().getAttribute('contenteditable')).toBe('true')
    expect(s.onChange).not.toHaveBeenCalled()
    expect(s.ref.current!.getText()).toBe('hello')
  })

  it('an editor who loses write can no longer type, and the switch itself reports nothing', async () => {
    const s = await show(false)
    expect(s.content().getAttribute('contenteditable')).toBe('true')
    await s.render(true)
    expect(s.content().getAttribute('contenteditable')).toBe('false')
    expect(s.onChange).not.toHaveBeenCalled()
  })
})
