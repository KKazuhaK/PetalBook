// What the person has typed so far, readable at the moment a save runs.
//
// It is a plain object rather than React state because it must be current
// when the debounce fires (not as of the last render), and because changing
// it must not re-render anything. The body is only converted to Markdown when
// read, and at most once per distinct state of the document.

import type { DocJSON } from '@/markdown'
import type { Draft, Fields } from './saveController'

// The slice of the Tiptap editor this needs.
export interface EditorLike {
  isDestroyed: boolean
  getJSON(): DocJSON
}

export class LiveDraft {
  fields: Fields
  private editor: EditorLike | null = null
  private bodySeq = 0
  private cache: { seq: number; md: string } | null = null
  private jsonCache: { seq: number; doc: DocJSON } | null = null
  private readonly toMarkdown: (doc: DocJSON) => string

  constructor(fields: Fields, toMarkdown: (doc: DocJSON) => string) {
    this.fields = fields
    this.toMarkdown = toMarkdown
  }

  attachEditor(editor: EditorLike) {
    this.editor = editor
  }

  setField(key: 'title' | 'subtitle', value: string): Fields {
    return (this.fields = { ...this.fields, [key]: value })
  }
  setTags(tags: string[]): Fields {
    return (this.fields = { ...this.fields, tags })
  }
  // Replace everything with what the server has (the other side of a conflict).
  reset(fields: Fields) {
    this.fields = fields
    this.bodySeq = 0
    this.cache = null
    this.jsonCache = null
  }

  bodyChanged() {
    this.bodySeq++
  }

  // The editor document for the latest body edit. Cheap (no Markdown), and
  // kept per edit state, so reading it twice costs once.
  private json(): DocJSON | undefined {
    if (this.bodySeq === 0) return undefined
    const seq = this.bodySeq
    // A destroyed editor (the page is being left) keeps its last reading.
    if (this.jsonCache?.seq !== seq && this.editor && !this.editor.isDestroyed) this.jsonCache = { seq, doc: this.editor.getJSON() }
    // Edits exist but cannot be read: say so rather than let a save or a draft
    // quietly go out without them.
    if (!this.jsonCache || this.jsonCache.seq !== seq) throw new Error('the editor is no longer available')
    return this.jsonCache.doc
  }

  // For the local draft: header fields plus the document, no conversion.
  readStorable = (): { fields: Fields; bodyJson?: DocJSON } => ({ fields: this.fields, bodyJson: this.json() })

  // For a save: header fields plus the body as Markdown (converted once per edit state).
  read = (): Draft => {
    let body: string | undefined
    const doc = this.json()
    if (doc) {
      const seq = this.bodySeq
      if (this.cache?.seq !== seq) this.cache = { seq, md: this.toMarkdown(doc) }
      body = this.cache.md
    }
    return { ...this.fields, body }
  }
}
