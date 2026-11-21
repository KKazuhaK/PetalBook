// Autosave for one open page, independent of React so it can be tested with
// fake timers.
//
// Rules this class enforces:
//  - Edits only mark the page dirty. The expensive work (turning the editor
//    document into Markdown) happens when a save actually runs, through
//    `read()`, never per keystroke.
//  - A save runs 1.5 s after the last edit; further edits restart the wait.
//  - Never two requests in flight for one page. An edit that arrives while one
//    is out is queued; the queued save reads the latest content when it starts
//    and uses the rev the previous response returned.
//  - Nothing is sent when the content equals what the server has. The body is
//    only replaced if the person actually changed it: otherwise the original
//    string goes back untouched, so merely opening and leaving a page, or
//    editing only the title, never rewrites the file's Markdown.
//  - On 409 the controller stops and keeps the person's text; the UI decides.
//  - Header values are never cleaned, coerced or validated here: a field the
//    person did not change is sent back exactly as the server served it, and
//    the server alone decides key order.
//  - Everything typed and not yet confirmed saved is also kept in a local
//    draft (draftStore.ts): shortly after each change, synchronously when the
//    page is hidden or closed, and when the page is left. It is removed when a
//    save of exactly that state succeeds. It is what survives a closed tab, an
//    oversized `keepalive` body, or a save that fails after the page is gone.

import { ApiError } from '@/api/client'
import type { Page, PageMeta, SavePageInput } from '@/api/types'
import type { DocJSON } from '@/markdown'
import type { DraftContent, DraftStore } from './draftStore'

export type SaveState = 'saved' | 'dirty' | 'saving' | 'error' | 'conflict'

export interface Fields {
  title: string
  subtitle: string
  tags: string[]
}
// `body` is undefined while the body has not been edited since it was loaded.
export interface Draft extends Fields {
  body?: string
}

export interface SaveSnapshot {
  state: SaveState
  error?: Error
}

export interface SaveControllerOptions {
  page: Page
  read: () => Draft
  save: (id: string, input: SavePageInput, opts: { keepalive: boolean }) => Promise<Page>
  // Markdown -> the Markdown the editor would write for it. Used so that
  // typing something and deleting it again does not count as a change when the
  // file was written in another style (`*` bullets, say).
  normalizeBody?: (markdown: string) => string
  debounceMs?: number
  // Local draft. `readDraft` must be cheap and synchronous (no Markdown
  // conversion); it returns nothing for the body unless the body was edited.
  drafts?: DraftStore
  readDraft?: () => { fields: Fields; bodyJson?: DocJSON }
  draftDebounceMs?: number
  // The token of a draft this page was opened with (restored from an earlier
  // visit), so that saving it can remove it.
  draftToken?: string
  // Another tab saved this page and this tab has nothing unsaved: reload it.
  onExternalSave?: (rev: string) => void
  now?: () => number
  onSaved?: (page: Page) => void
  onConflict?: (current: Page, mine: Draft) => void
}

export const DEFAULT_DEBOUNCE_MS = 1500
// The local draft is written sooner than the server save is sent.
export const DEFAULT_DRAFT_DEBOUNCE_MS = 700

const FIELD_KEYS = ['title', 'subtitle', 'tags'] as const
type FieldKey = (typeof FIELD_KEYS)[number]

// --- header fields ---------------------------------------------------------

// The header fields as shown for editing, derived faithfully from what the
// server served: a scalar title or subtitle (number, boolean) is shown as its
// text; `tags` written as a single string is one tag, and in a list the string
// entries are the tags. Anything else is shown as nothing, and is only ever
// replaced if the person edits that very field (see applyFields).
export function fieldsOf(meta: PageMeta): Fields {
  const text = (v: unknown) => (typeof v === 'string' ? v : typeof v === 'number' || typeof v === 'boolean' ? String(v) : '')
  let tags: string[] = []
  if (typeof meta.tags === 'string') tags = meta.tags === '' ? [] : [meta.tags]
  else if (Array.isArray(meta.tags)) tags = meta.tags.filter((t): t is string => typeof t === 'string')
  return { title: text(meta.title), subtitle: text(meta.subtitle), tags }
}

// What a field becomes when the person writes it: one line, trimmed, tags
// de-duplicated. Applied to what they typed, never to what the server served.
export function cleanFields(f: Fields): Fields {
  const line = (s: string) => s.replace(/[\r\n]+/g, ' ').trim()
  const tags: string[] = []
  for (const raw of f.tags) {
    const t = line(raw)
    if (t && !tags.includes(t)) tags.push(t)
  }
  return { title: line(f.title), subtitle: line(f.subtitle), tags }
}

const sameTags = (a: string[], b: string[]) => a.length === b.length && a.every((t, i) => t === b[i])

export type FieldChanges = Record<FieldKey, boolean>

// Which fields the person actually changed. A field is unchanged if what is in
// the box equals what the server served, or would be tidied into it (a space
// typed after the title is not a change).
export function changedFields(served: Fields, typed: Fields): FieldChanges {
  const clean = cleanFields(typed)
  return {
    title: typed.title !== served.title && clean.title !== served.title,
    subtitle: typed.subtitle !== served.subtitle && clean.subtitle !== served.subtitle,
    tags: !sameTags(typed.tags, served.tags) && !sameTags(clean.tags, served.tags),
  }
}

// A copy of the header with only the changed fields written. Everything else,
// including fields this app does not know, goes back exactly as received. An
// emptied title is no `title` key at all, never `title: ""`. Where a key sits
// in the file is the server's business.
export function applyFields(meta: PageMeta, typed: Fields, changed: FieldChanges): PageMeta {
  const out: PageMeta = { ...meta }
  const clean = cleanFields(typed)
  for (const key of FIELD_KEYS) {
    if (!changed[key]) continue
    if (key === 'tags') {
      // Entries of a list that are not text (a bare `~`, say) are not the
      // person's tags to drop: they stay, after the edited ones.
      const kept = Array.isArray(meta.tags) ? meta.tags.filter((t) => typeof t !== 'string') : []
      const list = [...clean.tags, ...kept]
      if (list.length) out.tags = list
      else delete out.tags
    } else if (clean[key] !== '') out[key] = clean[key]
    else delete out[key]
  }
  return out
}

// What to send for `draft` on top of `base`, or null when it equals what the
// server has. The body is replaced only if the person really changed it; any
// other save sends the original body string untouched.
export function buildSave(base: Page, draft: Draft, normalizedBase: () => string): SavePageInput | null {
  const changed = changedFields(fieldsOf(base.meta), draft)
  const bodyChanged = draft.body !== undefined && draft.body !== base.body && draft.body !== normalizedBase()
  if (!bodyChanged && !changed.title && !changed.subtitle && !changed.tags) return null
  return {
    baseRev: base.rev,
    meta: applyFields(base.meta, draft, changed),
    body: bodyChanged ? draft.body! : base.body,
  }
}

// --- controller --------------------------------------------------------------

// Saves still on their way, by page id, so a page that is opened again right
// after leaving it waits for them instead of reading a stale rev.
const inflightById = new Map<string, Promise<void>>()
export const waitForSaves = (id: string) => inflightById.get(id) ?? Promise.resolve()

export class SaveController {
  private base: Page
  private snapshot: SaveSnapshot = { state: 'saved' }
  private timer: ReturnType<typeof setTimeout> | undefined
  private inflight: Promise<void> | null = null
  private pending = false
  private editSeq = 0
  private savedSeq = 0
  private frozen: Draft | undefined
  private closed = false
  private detached = false
  private discarded = false
  private draftTimer: ReturnType<typeof setTimeout> | undefined
  private draftToken: string | undefined
  private tokenSeq = 0
  private normalizedBase: { of: string; value: string } | undefined
  private listeners = new Set<() => void>()
  private readonly opts: SaveControllerOptions

  constructor(opts: SaveControllerOptions) {
    this.opts = opts
    this.base = opts.page
    this.draftToken = opts.draftToken
  }

  // --- observation (useSyncExternalStore) ---
  subscribe = (fn: () => void) => {
    this.listeners.add(fn)
    return () => void this.listeners.delete(fn)
  }
  getSnapshot = () => this.snapshot
  get state(): SaveState {
    return this.snapshot.state
  }
  get page(): Page {
    return this.base
  }
  hasUnsaved(): boolean {
    return this.editSeq > this.savedSeq
  }

  private set(state: SaveState, error?: Error) {
    if (this.snapshot.state === state && this.snapshot.error === error) return
    this.snapshot = { state, error }
    this.listeners.forEach((l) => l())
  }

  // --- input ---
  // Something changed. Cheap: no serialisation happens here.
  markDirty() {
    if (this.closed) return
    this.editSeq++
    this.scheduleDraft()
    // Keep the text, stop talking to the server until the person decides.
    if (this.state === 'conflict') return
    if (this.state !== 'saving') this.set('dirty')
    this.clearTimer()
    this.timer = setTimeout(() => void this.flush(), this.opts.debounceMs ?? DEFAULT_DEBOUNCE_MS)
  }

  private clearTimer() {
    if (this.timer !== undefined) clearTimeout(this.timer)
    this.timer = undefined
  }

  // --- local draft ---
  private scheduleDraft() {
    if (!this.opts.drafts) return
    if (this.draftTimer !== undefined) clearTimeout(this.draftTimer)
    this.draftTimer = setTimeout(() => this.persistNow(), this.opts.draftDebounceMs ?? DEFAULT_DRAFT_DEBOUNCE_MS)
  }

  // Write the unsaved content to the local draft, synchronously. Called for the
  // debounce, and straight from `pagehide`, `visibilitychange`, `beforeunload`
  // and unmount, where nothing asynchronous can be relied on.
  persistNow() {
    if (this.draftTimer !== undefined) clearTimeout(this.draftTimer)
    this.draftTimer = undefined
    const { drafts, readDraft } = this.opts
    if (!drafts || !readDraft || this.discarded || !this.hasUnsaved()) return
    try {
      const { fields, bodyJson } = readDraft()
      const token = `${(this.opts.now ?? Date.now)()}-${++this.tokenSeq}-${Math.random().toString(36).slice(2, 8)}`
      drafts.write({ baseRev: this.base.rev, fields, bodyJson, savedAt: (this.opts.now ?? Date.now)(), token } satisfies DraftContent)
      this.draftToken = token
    } catch {
      // The draft is a safety net; failing to take it must not break typing.
    }
  }

  private clearDraft() {
    if (this.draftTimer !== undefined) clearTimeout(this.draftTimer)
    this.draftTimer = undefined
    // Only a draft this tab wrote (or opened with) is this tab's to remove.
    if (this.draftToken === undefined) return
    try {
      this.opts.drafts?.clearIf(this.draftToken)
    } catch {
      // see persistNow
    }
  }

  // Unconditionally: the page is gone.
  private forgetDraft() {
    if (this.draftTimer !== undefined) clearTimeout(this.draftTimer)
    this.draftTimer = undefined
    try {
      this.opts.drafts?.clear()
    } catch {
      // see persistNow
    }
  }

  // Another tab saved this page (its new rev arrives via the storage event).
  // Nothing unsaved here: reload. Something unsaved: leave the base alone and
  // try now, so the person meets the ordinary conflict dialog straight away
  // instead of at some later save.
  noticeExternalSave(rev: string) {
    if (rev === this.base.rev || this.closed) return
    if (this.state === 'saving' || this.state === 'conflict') return
    if (!this.hasUnsaved()) this.opts.onExternalSave?.(rev)
    else void this.flush()
  }

  // Save now (or queue behind the request that is out). Resolves when the
  // queue is empty.
  flush(): Promise<void> {
    this.clearTimer()
    if (this.state === 'conflict') return this.inflight ?? Promise.resolve()
    if (this.inflight) {
      this.pending = true
      return this.inflight
    }
    return this.run(false)
  }

  retry(): Promise<void> {
    return this.flush()
  }

  // The tab is being hidden or closed: send what is unsaved with `keepalive`
  // so the browser lets the request finish. A request already out cannot be
  // doubled (it would collide on rev), so that case just queues.
  flushKeepalive() {
    // The draft first: it is what survives if the request below does not.
    this.persistNow()
    this.clearTimer()
    if (this.closed || this.state === 'conflict') return
    if (this.inflight) {
      this.pending = true
      return
    }
    if (this.hasUnsaved()) void this.run(true)
  }

  // The page is being left. Capture the content now, while the editor still
  // exists, then let the save finish on its own.
  detach(): Promise<void> {
    this.clearTimer()
    if (this.discarded) return Promise.resolve()
    // Capture the content while the editor still exists: as a local draft, and
    // (below) as the exact text of the save that follows.
    this.persistNow()
    if (this.state !== 'conflict' && this.hasUnsaved()) {
      try {
        this.frozen = this.opts.read()
      } catch {
        // The draft above is all there is; it is offered when the page opens next.
      }
    }
    this.closed = true
    this.detached = true
    if (this.state === 'conflict' || (!this.frozen && !this.inflight)) return Promise.resolve()
    if (this.inflight) {
      this.pending = this.pending || this.hasUnsaved()
      return this.inflight
    }
    return this.run(false)
  }

  // Stop autosaving for now (a delete or move is about to happen) ...
  suspend() {
    this.clearTimer()
    this.closed = true
  }
  // ... and start again, for the page that is still open. Also what a mount
  // calls, because React may mount, unmount and mount again in development.
  attach() {
    if (this.discarded) return
    this.closed = false
    this.detached = false
  }
  // Drop whatever is unsaved for good (the page has been deleted).
  discard() {
    this.suspend()
    this.discarded = true
    this.pending = false
    this.frozen = undefined
    this.forgetDraft()
  }

  // --- conflict resolution ---
  // Keep the person's version: save it again on top of the server's current one.
  keepMine(current: Page): Promise<void> {
    this.base = current
    this.set('dirty')
    this.pending = false
    return this.flush()
  }
  // Take the server's version; the caller replaces the editor content.
  useTheirs(current: Page) {
    this.clearTimer()
    this.base = current
    this.editSeq = 0
    this.savedSeq = 0
    this.frozen = undefined
    this.clearDraft()
    this.set('saved')
  }
  // The page opened with a local draft that disagrees with the server: start in
  // the conflict state, with the draft as the person's side.
  enterConflict(current: Page, mine: Draft) {
    this.editSeq = Math.max(this.editSeq, 1)
    this.base = current
    this.clearTimer()
    this.set('conflict')
    this.opts.onConflict?.(current, mine)
  }
  // The page was changed by something other than a save (a move): learn the
  // new rev/path without touching the content the person is editing.
  adopt(page: Page) {
    this.base = { ...this.base, rev: page.rev, path: page.path, folder: page.folder, updated: page.updated }
    this.snapshot = { ...this.snapshot }
    this.listeners.forEach((l) => l())
  }

  // --- the save itself ---
  private run(keepalive: boolean): Promise<void> {
    const p = this.loop(keepalive).finally(() => {
      if (this.inflight === p) this.inflight = null
      if (inflightById.get(this.base.id) === p) inflightById.delete(this.base.id)
    })
    this.inflight = p
    inflightById.set(this.base.id, p)
    return p
  }

  private async loop(keepalive: boolean) {
    do {
      this.pending = false
      await this.once(keepalive)
    } while (this.pending && this.state !== 'conflict')
  }

  private bodyNormalized(): string {
    const body = this.base.body
    if (this.normalizedBase?.of !== body) this.normalizedBase = { of: body, value: this.opts.normalizeBody ? this.opts.normalizeBody(body) : body }
    return this.normalizedBase.value
  }

  private async once(keepalive: boolean) {
    const seq = this.editSeq
    let draft: Draft
    let input: SavePageInput | null
    try {
      draft = this.frozen ?? this.opts.read()
      input = buildSave(this.base, draft, () => this.bodyNormalized())
    } catch (e) {
      // The editor document had no Markdown form, or the editor is gone.
      this.set('error', e instanceof Error ? e : new Error(String(e)))
      return
    }
    if (!input) {
      this.savedSeq = Math.max(this.savedSeq, seq)
      if (this.editSeq === seq) this.clearDraft()
      this.set(this.editSeq > seq ? 'dirty' : 'saved')
      return
    }
    this.set('saving')
    try {
      const page = await this.opts.save(this.base.id, input, { keepalive })
      this.base = page
      this.savedSeq = Math.max(this.savedSeq, seq)
      if (this.frozen && this.editSeq === seq) this.frozen = undefined
      // The draft goes only if this save carried everything typed. Otherwise it
      // is rewritten against the new rev, so it never claims the old one.
      if (this.editSeq === seq) this.clearDraft()
      else if (!this.detached) this.persistNow()
      // Left the page with more queued: the editor cannot be read any more, but
      // the stored draft has all of it. Point it at the new rev, so that if the
      // queued save fails the draft is not mistaken for a conflict with this one.
      else if (this.draftToken !== undefined) this.opts.drafts?.rebase(this.draftToken, page.rev)
      try {
        this.opts.drafts?.announceSaved(page.rev)
      } catch {
        // see persistNow
      }
      this.opts.onSaved?.(page)
      this.set(this.editSeq > seq ? 'dirty' : 'saved')
    } catch (e) {
      if (e instanceof ApiError && e.code === 'conflict' && e.current) {
        this.clearTimer()
        this.set('conflict')
        // After the page is left nobody is listening: the draft stays, and
        // the conflict is raised when the page is opened again.
        if (!this.detached) this.opts.onConflict?.(e.current, { ...draft, body: draft.body ?? this.base.body })
      } else {
        this.set('error', e instanceof Error ? e : new Error(String(e)))
      }
    }
  }
}
