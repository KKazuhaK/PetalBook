// How an image is drawn in the editor, not how it is stored.
//
// The page keeps the address as written (`/assets/<id>/x.jpg`); the browser has
// to be asked for the URL that serves it. A node view maps the address while
// drawing and nothing else: the document, the clipboard HTML (which uses the
// schema's own `renderHTML`) and the Markdown conversion all keep the original.

import Image, { type ImageOptions } from '@tiptap/extension-image'

export type ResolveSrc = (src: string) => string

export const MappedImage = Image.extend<ImageOptions & { resolveSrc?: ResolveSrc }>({
  addNodeView() {
    const resolve = this.options.resolveSrc
    if (!resolve || typeof document === 'undefined') return null
    return ({ node }) => {
      const img = document.createElement('img')
      const set = (name: string, value: unknown) => {
        if (value === null || value === undefined || value === '') img.removeAttribute(name)
        else img.setAttribute(name, String(value))
      }
      const paint = (attrs: Record<string, unknown>) => {
        img.removeAttribute('data-broken')
        set('src', typeof attrs.src === 'string' ? resolve(attrs.src) : null)
        set('alt', attrs.alt)
        set('title', attrs.title)
        set('width', attrs.width)
        set('height', attrs.height)
      }
      // A file that is gone or not an image would show a broken-image icon with
      // nothing to say what was meant.
      img.addEventListener('error', () => img.setAttribute('data-broken', ''))
      paint(node.attrs)
      return {
        dom: img,
        update: (updated) => {
          if (updated.type !== node.type) return false
          paint(updated.attrs)
          return true
        },
      }
    }
  },
})
