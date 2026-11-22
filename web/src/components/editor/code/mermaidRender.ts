// Draws a Mermaid diagram. The library is large, so it is loaded the first
// time a page shows a diagram and never before.
//
// Safety: a page is untrusted text. Mermaid runs with `securityLevel: 'strict'`
// (no HTML in labels, no click handlers), labels are plain SVG text, and the
// result is shown as an image from a data URL, in which a browser runs no
// script and loads nothing from outside. The picture is never inserted into
// the page's own DOM.

export interface MermaidApi {
  initialize(config: Record<string, unknown>): void
  render(id: string, text: string): Promise<{ svg: string }>
}

let api: Promise<MermaidApi> | null = null
let initializedFor: boolean | null = null
// mermaid keeps one global configuration and one queue; renders go one at a time.
let queue: Promise<unknown> = Promise.resolve()
let counter = 0

const load = () => (api ??= import('mermaid').then((m) => m.default as unknown as MermaidApi))

// Mermaid writes `width="100%"`, which an image has no container to be a
// percentage of: it would be stretched to the full line. The natural size is in
// the viewBox; stating it makes the picture its own size, and the page's
// `max-width: 100%` shrinks it where the line is narrower.
export function withNaturalSize(svg: string): string {
  try {
    const doc = new DOMParser().parseFromString(svg, 'image/svg+xml')
    const root = doc.documentElement
    const box = root.getAttribute('viewBox')?.trim().split(/[\s,]+/).map(Number)
    if (root.localName !== 'svg' || !box || box.length !== 4 || box.some((n) => !Number.isFinite(n)) || box[2] <= 0 || box[3] <= 0) return svg
    root.setAttribute('width', String(Math.ceil(box[2])))
    root.setAttribute('height', String(Math.ceil(box[3])))
    root.removeAttribute('style')
    return new XMLSerializer().serializeToString(root)
  } catch {
    return svg
  }
}

export function svgToDataUrl(svg: string): string {
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`
}

// The picture of `text` as an image address. Rejects with the library's own
// message when the text is not a diagram.
export function renderDiagram(text: string, dark: boolean): Promise<string> {
  const run = async () => {
    const mermaid = await load()
    if (initializedFor !== dark) {
      mermaid.initialize({
        startOnLoad: false,
        securityLevel: 'strict',
        theme: dark ? 'dark' : 'default',
        // Plain SVG text instead of HTML inside foreignObject, which an image
        // cannot show in every browser.
        htmlLabels: false,
        flowchart: { htmlLabels: false },
        // An error is reported to the caller, not drawn into the page.
        suppressErrorRendering: true,
      })
      initializedFor = dark
    }
    const id = `pb-diagram-${++counter}`
    try {
      const { svg } = await mermaid.render(id, text)
      return svgToDataUrl(withNaturalSize(svg))
    } finally {
      // A failed render can leave its scratch element behind.
      document.getElementById(id)?.remove()
      document.getElementById(`d${id}`)?.remove()
    }
  }
  const result = queue.then(run, run)
  queue = result.catch(() => undefined)
  return result
}
