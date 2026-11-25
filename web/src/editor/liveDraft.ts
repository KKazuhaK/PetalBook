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
  // The body as text typed in the source view, for the edit state it was typed in.
  // An edit in the editor after it moves `bodySeq` on, and the document counts again.
  private text: { seq: number; value: string } | null = null
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
  setSource(source: string[]): Fields {
    return (this.fields = { ...this.fields, source })
  }
  // The header keys that are not the named fields (the properties of a type, and
  // whatever else the header holds), as the person has them now.
  setOthers(others: Fields['others']): Fields {
    return (this.fields = { ...this.fields, others })
  }
  // The whole header as parsed from the source view.
  setFields(fields: Fields): Fields {
    return (this.fields = fields)
  }
  // Replace everything with what the server has (the other side of a conflict).
  reset(fields: Fields) {
    this.fields = fields
    this.bodySeq = 0
    this.cache = null
    this.jsonCache = null
    this.text = null
  }

  // The body was edited in the editor: the document is the body.
  bodyChanged() {
    this.bodySeq++
  }
  // The body was typed as Markdown: that text is the body, exactly as typed,
  // until the editor is edited again.
  setBodyText(value: string) {
    this.bodySeq++
    this.text = { seq: this.bodySeq, value }
  }
  // 'none': not edited since it was loaded; 'text': typed as Markdown; 'doc': edited in the editor.
  bodyKind(): 'none' | 'text' | 'doc' {
    if (this.bodySeq === 0) return 'none'
    return this.text?.seq === this.bodySeq ? 'text' : 'doc'
  }
  private typedText(): string | undefined {
    return this.text?.seq === this.bodySeq ? this.text.value : undefined
  }

  // The editor document for the latest body edit. Cheap (no Markdown), and
  // kept per edit state, so reading it twice costs once.
  private json(): DocJSON | undefined {
    if (this.bodySeq === 0) return undefined
    const seq = this.bodySeq
    // A destroyed editor (the page is being left) keeps its last reading.
    if (this.jsonCache?.seq !== seq && this.editor && !this.editor.isDestroyed)
      this.jsonCache = { seq, doc: this.editor.getJSON() }
    // Edits exist but cannot be read: say so rather than let a save or a draft
    // quietly go out without them.
    if (!this.jsonCache || this.jsonCache.seq !== seq) throw new Error('the editor is no longer available')
    return this.jsonCache.doc
  }

  // For the local draft: header fields plus the document, no conversion.
  readStorable = (): { fields: Fields; bodyJson?: DocJSON; bodyText?: string } => {
    const bodyText = this.typedText()
    return bodyText !== undefined ? { fields: this.fields, bodyText } : { fields: this.fields, bodyJson: this.json() }
  }

  // For a save: header fields plus the body as Markdown (converted once per edit state).
  read = (): Draft => {
    const typed = this.typedText()
    let body = typed
    const doc = body === undefined ? this.json() : undefined
    if (doc) {
      const seq = this.bodySeq
      if (this.cache?.seq !== seq) this.cache = { seq, md: this.toMarkdown(doc) }
      body = this.cache.md
    }
    return typed === undefined ? { ...this.fields, body } : { ...this.fields, body, bodyTyped: true }
  }
}
