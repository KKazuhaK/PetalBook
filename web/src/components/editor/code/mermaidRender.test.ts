import { beforeEach, describe, expect, it, vi } from 'vitest'

const initialize = vi.fn()
const render = vi.fn<(id: string, text: string) => Promise<{ svg: string }>>()
vi.mock('mermaid', () => ({
  default: { initialize: (c: unknown) => initialize(c), render: (id: string, text: string) => render(id, text) },
}))

const { renderDiagram, svgToDataUrl, withNaturalSize } = await import('./mermaidRender')

beforeEach(() => {
  initialize.mockClear()
  render.mockReset()
  render.mockResolvedValue({ svg: '<svg xmlns="http://www.w3.org/2000/svg"><text>héllo 图</text></svg>' })
})

describe('the size of a picture', () => {
  it('is the viewBox, not a percentage', () => {
    const out = withNaturalSize(
      '<svg xmlns="http://www.w3.org/2000/svg" width="100%" viewBox="0 0 120.5 80" style="max-width: 120.5px;"><g/></svg>',
    )
    expect(out).toContain('width="121"')
    expect(out).toContain('height="80"')
    expect(out).not.toContain('max-width')
  })
  it('is left alone when there is no usable viewBox, or the text is not SVG', () => {
    expect(withNaturalSize('<svg xmlns="http://www.w3.org/2000/svg"/>')).toBe(
      '<svg xmlns="http://www.w3.org/2000/svg"/>',
    )
    expect(withNaturalSize('not xml <')).toBe('not xml <')
  })
})

describe('drawing with mermaid', () => {
  it('runs in strict mode with plain-SVG labels, and in the theme asked for', async () => {
    await renderDiagram('graph TD\nA-->B', false)
    expect(initialize).toHaveBeenCalledWith(
      expect.objectContaining({
        securityLevel: 'strict',
        theme: 'default',
        startOnLoad: false,
        htmlLabels: false,
        suppressErrorRendering: true,
      }),
    )
    await renderDiagram('graph TD\nA-->B', true)
    expect(initialize).toHaveBeenLastCalledWith(expect.objectContaining({ securityLevel: 'strict', theme: 'dark' }))
  })

  it('configures once per theme, not per drawing', async () => {
    await renderDiagram('a', true)
    initialize.mockClear()
    await renderDiagram('b', true)
    await renderDiagram('c', true)
    expect(initialize).not.toHaveBeenCalled()
  })

  it('returns an image address, never markup that would go into the page', async () => {
    const url = await renderDiagram('graph TD\nA-->B', false)
    expect(url.startsWith('data:image/svg+xml;charset=utf-8,')).toBe(true)
    expect(decodeURIComponent(url.split(',')[1])).toContain('héllo 图')
    expect(document.body.querySelector('svg')).toBeNull()
  })

  it('keeps a script in a picture inert: it is only ever the data of an image', () => {
    const url = svgToDataUrl('<svg><script>alert(1)</script></svg>')
    expect(url).not.toContain('<')
  })

  it('rejects with the library message, and does not leave its scratch element behind', async () => {
    render.mockImplementationOnce(async (id) => {
      const stray = document.createElement('div')
      stray.id = `d${id}`
      document.body.appendChild(stray)
      throw new Error('Parse error')
    })
    await expect(renderDiagram('nonsense', false)).rejects.toThrow('Parse error')
    expect(document.body.querySelector('[id^="dpb-diagram"]')).toBeNull()
    // And the next drawing is not affected.
    await expect(renderDiagram('graph TD\nA-->B', false)).resolves.toContain('data:')
  })

  it('draws one at a time', async () => {
    const order: string[] = []
    render.mockImplementation(async (_id, text) => {
      order.push(`start ${text}`)
      await new Promise((r) => setTimeout(r, 5))
      order.push(`end ${text}`)
      return { svg: '<svg/>' }
    })
    await Promise.all([renderDiagram('one', false), renderDiagram('two', false)])
    expect(order).toEqual(['start one', 'end one', 'start two', 'end two'])
  })
})
