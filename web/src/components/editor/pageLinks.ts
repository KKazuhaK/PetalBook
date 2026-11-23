// Links to other pages as the page editor shows them (docs/design.md, 2.4): a link
// whose destination is a page file of the space is drawn as a page link (a click with
// Ctrl/⌘, or any click where the text cannot be edited, opens it in the app), one
// whose target is gone is drawn differently, and hovering shows the target's current
// title. What the server knows of the links of the page (`GET …/links`) is handed in;
// the document is not touched.

import { Extension, type Editor } from '@tiptap/core'
import { Plugin, PluginKey } from '@tiptap/pm/state'
import { Decoration, DecorationSet } from '@tiptap/pm/view'
import type { PageLink } from '@/api/types'
import { isPageId, resolveHref } from '@/lib/pageLinks'
import { t } from '@/i18n'

export interface PageLinkTarget {
  // The id in the link's title, when it has one.
  id?: string
  // The file the link names, from the root of the space.
  path: string
  anchor: string
}

export interface PageLinksOptions {
  // The folder of the page now; it changes when the page is moved.
  getFolder: () => string
  // The links the server reports for the page, once known.
  getLinks: () => PageLink[] | undefined
  // Opens the page a link names, in the app.
  open: (target: PageLinkTarget) => void
}

export interface PageLinksStorage {
  // Draws the links again (the server's report changed).
  refresh: () => void
  // Opens the page a link in the text names; false (and nothing happens) when it is not a page link.
  openHref: (href: string, title: string | null) => boolean
  // Whether a destination names a page file of the space.
  isPage: (href: string) => boolean
}

export const pageLinksOf = (editor: Editor): PageLinksStorage | undefined => (editor.storage as unknown as { pageLinks?: PageLinksStorage }).pageLinks

// What the page's links are known to be, handed over after the editor exists (the editor is made before the
// query for them has an answer).
export class LinkReport {
  private items: PageLink[] | undefined
  set(items: PageLink[] | undefined) {
    this.items = items
  }
  get = () => this.items
}

const key = new PluginKey<DecorationSet>('pageLinks')

// The server's report on one link of the text: by the id it carries, else by the file it names.
export function findLink(links: PageLink[], id: string | undefined, path: string): PageLink | undefined {
  return (id ? links.find((l) => l.id === id) : undefined) ?? links.find((l) => l.path === path && !l.id) ?? links.find((l) => l.path === path)
}

export const PageLinks = Extension.create<PageLinksOptions, PageLinksStorage>({
  name: 'pageLinks',

  addOptions() {
    return { getFolder: () => '', getLinks: () => undefined, open: () => undefined }
  },

  addStorage() {
    return { refresh: () => {}, openHref: () => false, isPage: () => false }
  },

  onCreate() {
    this.storage.isPage = (href) => resolveHref(this.options.getFolder(), href) !== null
    this.storage.openHref = (href, title) => {
      const r = resolveHref(this.options.getFolder(), href)
      if (!r) return false
      this.options.open({ id: isPageId(title) ? title : undefined, path: r.path, anchor: r.anchor })
      return true
    }
    this.storage.refresh = () => {
      if (!this.editor.isDestroyed) this.editor.view.dispatch(this.editor.state.tr.setMeta(key, 'refresh'))
    }
  },

  addProseMirrorPlugins() {
    const options = this.options
    const build = (doc: import('@tiptap/pm/model').Node): DecorationSet => {
      const folder = options.getFolder()
      const links = options.getLinks()
      const out: Decoration[] = []
      doc.descendants((node, pos) => {
        if (!node.isText) return
        const mark = node.marks.find((m) => m.type.name === 'link')
        if (!mark) return
        const r = resolveHref(folder, String(mark.attrs.href ?? ''))
        if (!r) return
        const title = mark.attrs.title as string | null
        const found = links ? findLink(links, isPageId(title) ? title : undefined, r.path) : undefined
        const broken = found?.broken === true
        out.push(
          Decoration.inline(pos, pos + node.nodeSize, {
            class: broken ? 'pb-page-link pb-page-link-broken' : 'pb-page-link',
            'data-page-link': broken ? 'broken' : '',
            // The native tooltip: what the link leads to now, or that it leads nowhere.
            ...(broken ? { title: t.pageLinks.broken } : found?.target ? { title: t.pageLinks.hover(found.target.title || t.editor.untitled) } : {}),
          }),
        )
      })
      return DecorationSet.create(doc, out)
    }
    return [
      new Plugin<DecorationSet>({
        key,
        state: {
          init: (_, state) => build(state.doc),
          // Drawn again when the text changes or the server's report does; otherwise as it was.
          apply: (tr, set, _old, state) => (tr.docChanged || tr.getMeta(key) ? build(state.doc) : set),
        },
        props: {
          decorations: (state) => key.getState(state),
          handleDOMEvents: {
            click: (view, event) => {
              const a = (event.target as HTMLElement | null)?.closest?.('a')
              if (!a || !view.dom.contains(a)) return false
              const r = resolveHref(options.getFolder(), a.getAttribute('href') ?? '')
              if (!r) return false
              // Where the text can be written a click puts the cursor in it (and the link's popover shows);
              // Ctrl/⌘ opens. Where it cannot, a click opens.
              if (view.editable && !event.ctrlKey && !event.metaKey) return false
              event.preventDefault()
              const title = a.getAttribute('title')
              options.open({ id: isPageId(title) ? title : undefined, path: r.path, anchor: r.anchor })
              return true
            },
          },
        },
      }),
    ]
  },
})
