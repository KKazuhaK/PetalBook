// Links to other pages as the page editor shows them (docs/design.md, 2.4): a link
// whose destination is a page file of the space is drawn as a page link (a click with
// Ctrl/⌘, or any click where the text cannot be edited, opens it in the app), one
// whose target is gone is drawn differently, and hovering shows the target's current
// title. What the server knows of the links of the page (`GET …/links`) is handed in;
// the document is not touched.

import { Extension, type Editor } from '@tiptap/core'
import type { Mark as PMMark, Node as PMNode } from '@tiptap/pm/model'
import { Plugin, PluginKey, type Transaction } from '@tiptap/pm/state'
import { AddMarkStep } from '@tiptap/pm/transform'
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

export const pageLinksOf = (editor: Editor): PageLinksStorage | undefined =>
  (editor.storage as unknown as { pageLinks?: PageLinksStorage }).pageLinks

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
  return indexOf(links).find(id, path)
}

// The report looked up by id and by path, built once per report: a page can hold thousands of links, and the
// decorations ask about each of them.
export class LinkIndex {
  private byId = new Map<string, PageLink>()
  private byPathNoId = new Map<string, PageLink>()
  private byPath = new Map<string, PageLink>()
  // The id of the page a path names, where the report knows it (a target that is there).
  private pages = new Map<string, string>()
  constructor(links: PageLink[]) {
    for (const l of links) {
      if (l.id && !this.byId.has(l.id)) this.byId.set(l.id, l)
      if (!l.id && !this.byPathNoId.has(l.path)) this.byPathNoId.set(l.path, l)
      if (!this.byPath.has(l.path)) this.byPath.set(l.path, l)
      if (l.target && !l.broken) {
        if (!this.pages.has(l.target.path)) this.pages.set(l.target.path, l.target.id)
        if (!this.pages.has(l.path)) this.pages.set(l.path, l.target.id)
      }
    }
  }
  find(id: string | undefined, path: string): PageLink | undefined {
    return (id ? this.byId.get(id) : undefined) ?? this.byPathNoId.get(path) ?? this.byPath.get(path)
  }
  pageAt(path: string): string | undefined {
    return this.pages.get(path)
  }
}

const indexes = new WeakMap<PageLink[], LinkIndex>()
// The index of a report; one report is indexed once.
export function indexOf(links: PageLink[]): LinkIndex {
  let index = indexes.get(links)
  if (!index) indexes.set(links, (index = new LinkIndex(links)))
  return index
}

// What the title of a link becomes when its address changes from `oldHref` to `newHref`. The id in the title
// decides where a link leads (the server goes by it, and rewrites the path to follow it), so an id kept from the
// old address would undo the change: it becomes the id of the page the new address names, where the report knows
// it, and is dropped otherwise. Any other title stays, and so does the id while the address names the same file
// (another anchor, another way of writing the path).
export function retitle(
  folder: string,
  links: PageLink[] | undefined,
  oldHref: string,
  newHref: string,
  title: string | null,
): string | null {
  if (!isPageId(title) || oldHref === newHref) return title
  const after = resolveHref(folder, newHref)
  if (!after) return null
  if (resolveHref(folder, oldHref)?.path === after.path) return title
  return (links ? indexOf(links).pageAt(after.path) : undefined) ?? null
}

// The positions of the new document a transaction touched, each widened to the textblocks it is in (a link's
// decorations never leave its textblock). Mark steps move nothing, so their own range is taken too.
function changedRanges(tr: Transaction): [number, number][] {
  const out: [number, number][] = []
  const doc = tr.doc
  tr.steps.forEach((step, i) => {
    const after = tr.mapping.slice(i + 1)
    const add = (from: number, to: number) => out.push([after.map(from, -1), after.map(to, 1)])
    const map = step.getMap()
    map.forEach((_a, _b, from, to) => add(from, to))
    const s = step as unknown as { from?: unknown; to?: unknown }
    if (typeof s.from === 'number' && typeof s.to === 'number') add(map.map(s.from, -1), map.map(s.to, 1))
  })
  const size = doc.content.size
  return out.map(([a, b]) => {
    const from = Math.max(0, Math.min(a, b, size))
    const to = Math.min(size, Math.max(a, b, 0))
    const $from = doc.resolve(from)
    const $to = doc.resolve(to)
    return [$from.parent.isTextblock ? $from.start() : from, $to.parent.isTextblock ? $to.end() : to]
  })
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
    // The decorations of the links of the text in [from, to] (the whole document when not given).
    const decorate = (doc: PMNode, from = 0, to = doc.content.size): Decoration[] => {
      const folder = options.getFolder()
      const links = options.getLinks()
      const index = links ? indexOf(links) : undefined
      const out: Decoration[] = []
      doc.nodesBetween(from, to, (node, pos) => {
        if (!node.isText) return
        if (pos < from || pos + node.nodeSize > to) return
        const mark = node.marks.find((m) => m.type.name === 'link')
        if (!mark) return
        const r = resolveHref(folder, String(mark.attrs.href ?? ''))
        if (!r) return
        const title = mark.attrs.title as string | null
        const found = index?.find(isPageId(title) ? title : undefined, r.path)
        const broken = found?.broken === true
        out.push(
          Decoration.inline(pos, pos + node.nodeSize, {
            class: broken ? 'pb-page-link pb-page-link-broken' : 'pb-page-link',
            'data-page-link': broken ? 'broken' : '',
            // The native tooltip: what the link leads to now, or that it leads nowhere.
            ...(broken
              ? { title: t.pageLinks.broken }
              : found?.target
                ? { title: t.pageLinks.hover(found.target.title || t.editor.untitled) }
                : {}),
          }),
        )
      })
      return out
    }
    const build = (doc: PMNode) => DecorationSet.create(doc, decorate(doc))
    // After a change of the text the decorations are moved along with it, and only the textblocks the change
    // touched are looked at again: a keystroke costs the same in a page with two links or two thousand.
    const update = (tr: Transaction, set: DecorationSet): DecorationSet => {
      let next = set.map(tr.mapping, tr.doc)
      for (const [from, to] of changedRanges(tr)) {
        const stale = next.find(from, to).filter((d) => d.from >= from && d.to <= to)
        next = next.remove(stale).add(tr.doc, decorate(tr.doc, from, to))
      }
      return next
    }
    return [
      new Plugin<DecorationSet>({
        key,
        state: {
          init: (_, state) => build(state.doc),
          // Drawn again when the server's report changes, brought along when the text does; otherwise as it was.
          apply: (tr, set, _old, state) => (tr.getMeta(key) ? build(state.doc) : tr.docChanged ? update(tr, set) : set),
        },
        // A new address for a link that carries a page's id (from the link's popover, or a URL pasted over it) is
        // given the id of the page it now names, or loses the id (see `retitle`).
        appendTransaction: (trs, _old, state) => {
          const fixes: { from: number; to: number; mark: PMMark; title: string | null }[] = []
          trs.forEach((tr, k) => {
            tr.steps.forEach((step, i) => {
              if (
                !(step instanceof AddMarkStep) ||
                step.mark.type.name !== 'link' ||
                !isPageId(step.mark.attrs.title as string | null)
              )
                return
              const newHref = String(step.mark.attrs.href ?? '')
              // Compared with the link as it was before the transaction: setting a mark can take more than one
              // step over the same text (tiptap first sets it with the new attributes alone, then merged).
              const back = tr.mapping.slice(0, i).invert()
              const start = back.map(step.from, 1)
              const end = back.map(step.to, -1)
              if (start >= end) return
              tr.before.nodesBetween(start, end, (node, pos) => {
                const old = node.isText ? node.marks.find((m) => m.type.name === 'link') : undefined
                if (!old || old.attrs.title !== step.mark.attrs.title || old.attrs.href === newHref) return
                const title = retitle(
                  options.getFolder(),
                  options.getLinks(),
                  String(old.attrs.href ?? ''),
                  newHref,
                  step.mark.attrs.title as string,
                )
                if (title === step.mark.attrs.title) return
                let mapping = tr.mapping
                let from = mapping.map(Math.max(pos, start), 1)
                let to = mapping.map(Math.min(pos + node.nodeSize, end), -1)
                for (const later of trs.slice(k + 1)) {
                  mapping = later.mapping
                  from = mapping.map(from, 1)
                  to = mapping.map(to, -1)
                }
                if (from < to) fixes.push({ from, to, mark: step.mark, title })
                return false
              })
            })
          })
          if (fixes.length === 0) return null
          const tr = state.tr
          for (const f of fixes) tr.addMark(f.from, f.to, f.mark.type.create({ ...f.mark.attrs, title: f.title }))
          return tr
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
