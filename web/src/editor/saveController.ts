// Autosave for one open page, independent of React so it can be tested with
// fake timers.
//
// What is where. The page file is only rewritten when a draft is committed;
// typing writes drafts.
//   - the browser-local draft (draftStore.ts): everything typed that the server
//     has not confirmed. Written shortly after each change, before every store
//     leaves, and synchronously when the tab is hidden, closed or left. Once the
//     server confirms the text as its draft the copy is marked `confirmed` and
//     decides nothing any more; it is removed when the text is in the file. A
//     refused store (below) confirms nothing, so the copy stays as it is.
//   - the server draft (`PUT .../draft`): stored after a pause in typing, and
//     when the tab is hidden. The page file stays as it is.
//   - the page file: rewritten only by `commit`, which this controller asks for
//     when the editor is left after typing and when the person chooses "save
//     version"; the server also does it by itself once the draft has been quiet.
//
// Every request names the draft it means. A store carries `prev`, the version
// of the draft it replaces; a commit carries the version it commits; a delete
// the version it removes. The server refuses (409) when its draft is another
// one, so another tab or device, or the server's own commit, cannot be
// overwritten or mistaken for ours without this controller noticing.
//
// Rules this class enforces:
//  - Edits only mark the page dirty. The expensive work (turning the editor
//    document into Markdown) happens when a request actually runs, through
//    `read()`, never per keystroke.
//  - A draft is stored 1.5 s after the last edit; further edits restart the wait.
//  - Never two requests in flight for one page. Whatever happens while one is
//    out is picked up by the loop when it returns: it stores again, then
//    commits if a commit was asked for.
//  - Nothing is sent when the content equals what the server shows (its draft
//    if it has one, else the file). Content that equals the file while the
//    server holds a draft deletes that draft, so the server cannot commit text
//    the person has since undone.
//  - Opening a page and leaving without typing sends nothing at all.
//  - What the server shows is never taken as "ours" without checking. Whenever
//    it may differ from what this controller believes (a request refused with
//    409, a store that comes back stale, a store whose answer never came) there
//    is one decision, `reconcile` (reconcile.ts): our own text that the server
//    committed is built on silently; anything else is a conflict, with the
//    person's text kept in the editor and in the local draft.
//  - The server never drops a draft on its own. A draft it confirmed is gone
//    only if it was committed, or replaced or deleted by a request naming its
//    version. So with nothing unsaved, a change seen on the server (after a
//    notice from another tab, a move, or coming back to the tab) is simply shown.
//  - Only one store is ever outstanding without an answer: after one, the next
//    step is to ask the server what it holds.
//  - A notice that another tab changed the page while a request is out is kept
//    and acted on when the loop is idle.
//  - Header values are never cleaned, coerced or validated here: a field the
//    person did not change is sent back exactly as the server served it, and
//    the server alone decides key order.

import { ApiError, isConflict, isUnauthenticated } from '@/api/client'
import type { DraftInput, DraftReceipt, Page, PageDraft, PageMeta, SavePageInput } from '@/api/types'
import type { DocJSON } from '@/markdown'
import { oursOf, reconcile, sameContent, type Ours } from './reconcile'
import type { DraftContent, DraftStore } from './draftStore'

// saved       the file has this content, nothing is waiting
// dirty       typed, not yet on the server (only in the browser's draft)
// storing     a draft is on its way to the server
// drafted     the server holds the draft; the file changes at commit
// committing  the draft is being written to the file
// error       the last request failed (offline, signed out, server error); retry
// conflict    the file and the text disagree; waiting for the person
export type SaveState = 'saved' | 'dirty' | 'storing' | 'drafted' | 'committing' | 'error' | 'conflict'

export interface Fields {
  title: string
  subtitle: string
  tags: string[]
  // Where the text comes from (a person, a book, a film): a list like the tags.
  source: string[]
  // The rest of the header (every key but `id` and the four above), when it is
  // not the file's: rewritten in the source view, or held by a draft the text
  // was loaded from. Absent means "as the file has them", whatever the file
  // has by the time of a save: keys the person never changed are never sent
  // over what someone else wrote.
  others?: PageMeta
}
// `body` is undefined while the body has not been edited since it was loaded.
export interface Draft extends Fields {
  body?: string
  // The body is Markdown the person typed themself (the source view), to be
  // kept as typed: it counts as changed unless it is the file's text exactly.
  // A body written by the editor also counts as unchanged when it is merely
  // the editor's way of writing the file's text.
  bodyTyped?: boolean
}

export interface SaveSnapshot {
  state: SaveState
  error?: Error
}

// What the server holds as the caller's draft, as far as this controller knows.
export interface ServerDraft {
  // Opaque, new on every store: what the server is asked to replace, commit or delete.
  version: string
  baseRev: string
  meta: PageMeta
  body: string
  updated: string
}

export interface ConflictInfo {
  // file:  the file moved on since the draft's base (a stale draft, or a 409
  //        from commit). Keep mine stores the text again on the file's rev; take
  //        theirs deletes the draft.
  // draft: the server holds another draft than the one this text was typed
  //        on top of (another tab or device). Keep mine replaces it; take
  //        theirs shows it.
  kind: 'file' | 'draft'
  // The file; for kind `draft`, the file with the other draft's header and body.
  theirs: Page
  mine: Draft
  // For kind `draft`: the other draft.
  draft?: ServerDraft
  // The text on the left is an older copy kept in this browser: it had been stored on
  // the server as a draft, which is gone since (committed and superseded, or discarded by
  // another device's save), and the page has moved on. `savedAt` is when it was written.
  older?: { savedAt: number }
  // For kind `draft`, when the other draft is itself stale: after this is
  // settled, the chosen text still disagrees with this file, and that is asked
  // next.
  after?: Page
}

// The requests a page needs. The real ones are `spaceApi(space)`'s.
export interface DraftApi {
  putDraft(id: string, input: DraftInput, opts: { keepalive: boolean }): Promise<DraftReceipt>
  // `rev`: the file rev this controller believes the page is at; the server refuses when it is not.
  deleteDraft(id: string, version: string, rev: string): Promise<void>
  commit(id: string, version: string): Promise<Page>
  getPage(id: string): Promise<Page>
}

export interface SaveControllerOptions {
  // The file.
  page: Page
  // The server draft the editor was opened on, if the page had one. The editor
  // shows its content, so it is what the content is compared with.
  draft?: PageDraft
  // What the controller starts out knowing, from the restore plan (draftRestore.ts).
  seed?: { lineage: string; ours: Ours[] }
  read: () => Draft
  api: DraftApi
  // Space id, so that a page opened again right after leaving waits for the
  // commit still on its way.
  scope?: string
  // Markdown -> the Markdown the editor would write for it. Used so that
  // typing something and deleting it again does not count as a change when the
  // file was written in another style (`*` bullets, say).
  normalizeBody?: (markdown: string) => string
  debounceMs?: number
  // Local draft. `readDraft` must be cheap and synchronous (no Markdown
  // conversion); it returns nothing for the body unless the body was edited.
  drafts?: DraftStore
  readDraft?: () => { fields: Fields; bodyJson?: DocJSON; bodyText?: string }
  draftDebounceMs?: number
  // The token of a draft this page was opened with (restored from an earlier
  // visit), so that storing it can remove it.
  draftToken?: string
  // Another tab or device changed the server's copy and nothing is unsaved
  // here: the page as the server has it now.
  onExternalChange?: (page: Page) => void
  now?: () => number
  // The file was written (by us or, as far as we can tell, by the server).
  onCommitted?: (page: Page) => void
  // A draft of the page was stored on the server: the person has written something that outlives this tab.
  onStored?: (id: string) => void
  onConflict?: (info: ConflictInfo) => void
}

export const DEFAULT_DEBOUNCE_MS = 1500
// The local draft is written sooner than the server draft is stored.
export const DEFAULT_DRAFT_DEBOUNCE_MS = 700

const FIELD_KEYS = ['title', 'subtitle', 'source', 'tags'] as const
type FieldKey = (typeof FIELD_KEYS)[number]

// --- header fields ---------------------------------------------------------

// The header fields as shown for editing, derived faithfully from what the
// server served: a scalar title or subtitle (number, boolean) is shown as its
// text; `tags` written as a single string is one tag, and in a list the string
// entries are the tags. Anything else is shown as nothing, and is only ever
// replaced if the person edits that very field (see applyFields).
//
// `fileMeta` is the header of the file when `meta` is a draft's or a version's:
// other keys that differ from the file's come along (see Fields.others).
export function fieldsOf(meta: PageMeta, fileMeta?: PageMeta): Fields {
  const text = (v: unknown) =>
    typeof v === 'string' ? v : typeof v === 'number' || typeof v === 'boolean' ? String(v) : ''
  // A single text, written by hand, is one value.
  const list = (v: unknown): string[] =>
    typeof v === 'string'
      ? v === ''
        ? []
        : [v]
      : Array.isArray(v)
        ? v.filter((t): t is string => typeof t === 'string')
        : []
  const tags = list(meta.tags)
  const source = list(meta.source)
  const others = fileMeta && !sameData(othersOf(meta), othersOf(fileMeta)) ? { others: othersOf(meta) } : {}
  return { title: text(meta.title), subtitle: text(meta.subtitle), source, tags, ...others }
}

// What a field becomes when the person writes it: one line, trimmed, tags
// de-duplicated. Applied to what they typed, never to what the server served.
export function cleanFields(f: Fields): Fields {
  const line = (s: string) => s.replace(/[\r\n]+/g, ' ').trim()
  const tidy = (list: string[]) => {
    const out: string[] = []
    for (const raw of list) {
      const t = line(raw)
      if (t && !out.includes(t)) out.push(t)
    }
    return out
  }
  return {
    title: line(f.title),
    subtitle: line(f.subtitle),
    source: tidy(f.source),
    tags: tidy(f.tags),
    ...(f.others === undefined ? {} : { others: f.others }),
  }
}

// The keys of a header that `others` stands for.
const NAMED_KEYS = new Set<string>(['id', 'title', 'subtitle', 'source', 'tags'])
export function othersOf(meta: PageMeta): PageMeta {
  const out: PageMeta = {}
  for (const [k, v] of Object.entries(meta)) if (!NAMED_KEYS.has(k)) out[k] = v
  return out
}

// Equal as data. The order of keys does not count: where a key sits in the
// file is the server's business.
export function sameData(a: unknown, b: unknown): boolean {
  if (a === b) return true
  if (typeof a !== 'object' || typeof b !== 'object' || a === null || b === null) return false
  if (Array.isArray(a) !== Array.isArray(b)) return false
  if (Array.isArray(a))
    return a.length === (b as unknown[]).length && a.every((v, i) => sameData(v, (b as unknown[])[i]))
  const ka = Object.keys(a)
  return (
    ka.length === Object.keys(b).length &&
    ka.every(
      (k) => k in (b as object) && sameData((a as Record<string, unknown>)[k], (b as Record<string, unknown>)[k]),
    )
  )
}

// Other keys as the server will have them once written (internal/space/pages.go):
// `created` cannot be removed, and an emptied `type` is no key at all. Without
// this a header typed that way would never compare equal to the file it became.
function settleOthers(others: PageMeta, baseMeta: PageMeta): PageMeta {
  const out: PageMeta = { ...others }
  if ((!('created' in out) || out.created === null) && 'created' in baseMeta) out.created = baseMeta.created
  if (typeof out.type === 'number' || typeof out.type === 'boolean') out.type = String(out.type)
  if (typeof out.type === 'string') out.type = out.type.trim()
  if (out.type === '' || out.type === null) delete out.type
  return out
}

// The header with its other keys replaced by the ones written in the source
// view; `id` and the named fields stay as `meta` has them.
function withOthers(meta: PageMeta, others: PageMeta): PageMeta {
  const out: PageMeta = {}
  for (const [k, v] of Object.entries(meta)) if (NAMED_KEYS.has(k)) out[k] = v
  for (const [k, v] of Object.entries(others)) if (!NAMED_KEYS.has(k)) out[k] = v
  return out
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
    source: !sameTags(typed.source, served.source) && !sameTags(clean.source, served.source),
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
    if (key === 'tags' || key === 'source') {
      // Entries of a list that are not text (a bare `~`, say) are not the
      // person's to drop: they stay, after the edited ones.
      const cur = meta[key]
      const kept = Array.isArray(cur) ? cur.filter((t) => typeof t !== 'string') : []
      const list = [...clean[key], ...kept]
      if (list.length) out[key] = list
      else delete out[key]
    } else if (clean[key]) out[key] = clean[key]
    else delete out[key]
  }
  return out
}

// What to send for `draft` on top of `base`, or null when it equals what the
// server has. The body is replaced only if the person really changed it; any
// other save sends the original body string untouched.
export function buildSave(base: Page, draft: Draft, normalizedBase: () => string): SavePageInput | null {
  const changed = changedFields(fieldsOf(base.meta), draft)
  const bodyChanged =
    draft.body !== undefined &&
    draft.body !== base.body &&
    (draft.bodyTyped === true || draft.body !== normalizedBase())
  const settled = draft.others === undefined ? undefined : settleOthers(draft.others, base.meta)
  const others = settled !== undefined && !sameData(settled, othersOf(base.meta)) ? settled : undefined
  if (!bodyChanged && !changed.title && !changed.subtitle && !changed.source && !changed.tags && !others) return null
  const meta = applyFields(base.meta, draft, changed)
  return {
    baseRev: base.rev,
    meta: others ? withOthers(meta, others) : meta,
    body: bodyChanged ? draft.body! : base.body,
  }
}

// --- comparing -----------------------------------------------------------------

export { sameContent }
const OURS_CAP = 500

// The file with the header and body of a draft: what the editor was loaded with.
export const draftAsPage = (file: Page, d: { meta: PageMeta; body: string; baseRev?: string }): Page => ({
  ...file,
  meta: d.meta,
  body: d.body,
  ...(d.baseRev ? { rev: d.baseRev } : {}),
})

// --- controller --------------------------------------------------------------

// Requests still on their way, by space and page id, so a page that is opened
// again right after leaving it waits for them instead of reading old state.
const inflightByKey = new Map<string, Promise<void>>()
const keyOf = (scope: string, id: string) => `${scope}/${id}`
export const waitForSaves = (scope: string, id: string) => inflightByKey.get(keyOf(scope, id)) ?? Promise.resolve()

// Every open editor, so signing out can store what is typed first.
const live = new Set<SaveController>()
export function flushAllSaves(): Promise<void> {
  return Promise.all([...live].map((c) => (c.hasUnsaved() ? c.flush() : undefined))).then(() => undefined)
}

type Step = 'none' | 'delete' | 'store' | 'commit'
type StepResult = 'ok' | 'stop'

export class SaveController {
  private file: Page
  private serverDraft: ServerDraft | null
  // The server draft cannot be committed as it is: the file moved on.
  private stale: boolean
  // The body that content with no body edit stands for.
  private openBody: string
  private snapshot: SaveSnapshot
  private timer: ReturnType<typeof setTimeout> | undefined
  private draftTimer: ReturnType<typeof setTimeout> | undefined
  private running = false
  private inflight: Promise<void> | null = null
  private editSeq = 0
  private storedSeq = 0
  // Every full content this controller has sent as a draft since it adopted the
  // current file rev (answered or not), and the draft the page was opened on.
  // It is what makes "this is our own text" decidable (see reconcile.ts).
  private ours: Ours[] = []
  // The cap on `ours` (a session never gets near it). If it is reached the list is
  // no longer complete, so it is not used: anything unknown reads as not ours and
  // ends in a conflict dialog, never in a silent adoption.
  private oursFull = false
  // The file rev the editor's text is typed on top of: what a local draft records
  // as its base. Changes only when the text is re-based by a decision or a choice,
  // never because a conflict showed a newer file.
  private lineage: string
  // A draft was sent and its answer never came: the server may hold it. While this
  // is set nothing else is sent: the next step is to ask the server what it holds
  // (observe), so at most one store is ever outstanding and `ours` does not grow
  // while offline.
  private uncertain = false
  // Consecutive failed attempts to reach the server, and when the next one may start.
  private failures = 0
  private backoffUntil = 0
  // Bumped by anything that changes what this controller knows or is about to
  // tell the server; a slow answer to an older question is not applied.
  private activity = 0
  // Store even though nothing new was typed (the person kept their text).
  private forceStore = false
  // The version of a server draft that is to be deleted (text the person undid or
  // gave up). The version, not a flag: whatever draft the controller comes to hold
  // later is never deleted in its place.
  private pendingDelete: string | null = null
  private wantCommit = false
  private retryCommit = false
  private keepaliveOnce = false
  private conflict: ConflictInfo | null = null
  private frozen: Draft | undefined
  private closed = false
  private detached = false
  private discarded = false
  private authLost = false
  private draftToken: string | undefined
  private tokenSeq = 0
  private normalized = new Map<string, string>()
  private listeners = new Set<() => void>()
  private readonly opts: SaveControllerOptions
  private readonly scope: string

  constructor(opts: SaveControllerOptions) {
    this.opts = opts
    this.scope = opts.scope ?? ''
    // Initial state: the page (and draft) the opener has just read, as the restore plan decided.
    this.file = opts.page
    this.serverDraft = opts.draft ? toServerDraft(opts.draft) : null
    this.stale = !!opts.draft?.stale
    this.lineage = opts.seed ? opts.seed.lineage : (opts.draft?.baseRev ?? opts.page.rev)
    this.ours = opts.seed ? [...opts.seed.ours] : opts.draft ? [oursOf(opts.draft.baseRev, opts.draft)] : []
    this.openBody = opts.draft ? opts.draft.body : opts.page.body
    this.draftToken = opts.draftToken
    this.snapshot = { state: this.serverDraft && !this.stale ? 'drafted' : 'saved' }
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
  // The file as the server has it.
  get page(): Page {
    return this.file
  }
  // The body the editor was loaded with (the file's, or a draft's): what the
  // body is while nobody has edited it.
  get shownBody(): string {
    return this.openBody
  }
  // Typed text the server has not confirmed.
  hasUnsaved(): boolean {
    return this.editSeq > this.storedSeq
  }
  private set(state: SaveState, error?: Error) {
    if (this.snapshot.state === state && this.snapshot.error === error) return
    this.snapshot = { state, error }
    this.listeners.forEach((l) => l())
  }
  private notify() {
    this.snapshot = { ...this.snapshot }
    this.listeners.forEach((l) => l())
  }

  // What the state is when no request is out.
  private settle() {
    if (this.conflict) return this.set('conflict')
    if (this.hasUnsaved() || this.forceStore) return this.set('dirty')
    this.set(this.serverDraft && !this.stale ? 'drafted' : 'saved')
  }

  private clock(): number {
    return (this.opts.now ?? Date.now)()
  }

  private fail(e: unknown) {
    const err = e instanceof Error ? e : new Error(String(e))
    if (isUnauthenticated(err)) this.authLost = true
    if (err instanceof ApiError && err.code === 'network') {
      this.failures++
      this.backoffUntil = this.clock() + Math.min(1000 * 2 ** (this.failures - 1), 30_000)
    }
    // A failed "save version" is repeated by retry, not by the next autosave.
    if (this.wantCommit) {
      this.retryCommit = true
      this.wantCommit = false
    }
    this.set('error', err)
  }

  // --- input ---
  // Something changed. Cheap: no serialisation happens here.
  markDirty() {
    if (this.closed) return
    this.activity++
    this.editSeq++
    this.scheduleLocal()
    // Keep the text, stop talking to the server until the person decides.
    if (this.conflict) return
    // Typing after a failed "save version" does not turn the next autosave into one.
    this.retryCommit = false
    if (this.state !== 'storing' && this.state !== 'committing') this.set('dirty')
    this.clearTimer()
    // After failures the next attempt waits (1 s, 2 s, 4 s … up to 30 s), so a long
    // offline stretch is not a request per typing pause; going online resets it.
    const wait = Math.max(this.opts.debounceMs ?? DEFAULT_DEBOUNCE_MS, this.backoffUntil - this.clock())
    this.timer = setTimeout(() => void this.flush(), wait)
  }

  private clearTimer() {
    if (this.timer !== undefined) clearTimeout(this.timer)
    this.timer = undefined
  }

  // --- local draft ---
  private scheduleLocal() {
    if (!this.opts.drafts) return
    if (this.draftTimer !== undefined) clearTimeout(this.draftTimer)
    this.draftTimer = setTimeout(() => this.persistNow(), this.opts.draftDebounceMs ?? DEFAULT_DRAFT_DEBOUNCE_MS)
  }

  // The rev of the file the text was typed on.
  private lineageRev(): string {
    return this.lineage
  }

  // Write the unconfirmed content to the local draft, synchronously. Called for
  // the debounce, and straight from `pagehide`, `visibilitychange`,
  // `beforeunload` and unmount, where nothing asynchronous can be relied on.
  persistNow(force = false) {
    if (this.draftTimer !== undefined) clearTimeout(this.draftTimer)
    this.draftTimer = undefined
    const { drafts, readDraft } = this.opts
    if (!drafts || !readDraft || this.discarded || (!force && !this.hasUnsaved())) return
    try {
      const { fields, bodyJson, bodyText } = readDraft()
      const at = (this.opts.now ?? Date.now)()
      const token = `${at}-${++this.tokenSeq}-${Math.random().toString(36).slice(2, 8)}`
      // `sent` says what was sent on this rev, so the
      // next visit can tell "newer than the server's" from "unrelated to it".
      const confirmed =
        !this.hasUnsaved() && !this.uncertain && this.serverDraft && !this.stale
          ? { confirmed: this.serverDraft.version }
          : {}
      drafts.write({
        baseRev: this.lineageRev(),
        fields,
        bodyJson,
        ...(bodyText === undefined ? {} : { bodyText }),
        savedAt: at,
        token,
        sent: this.knownOurs(),
        ...confirmed,
      } satisfies DraftContent)
      this.draftToken = token
    } catch {
      // The draft is a safety net; failing to take it must not break typing.
    }
  }

  private clearLocal() {
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

  // Unconditionally: the page is gone, or the person chose another version.
  private forgetLocal() {
    if (this.draftTimer !== undefined) clearTimeout(this.draftTimer)
    this.draftTimer = undefined
    // Only a draft this controller wrote or adopted: another tab on the same page
    // shares the key and may hold text of its own.
    if (this.draftToken === undefined) return
    try {
      this.opts.drafts?.clearIf(this.draftToken)
    } catch {
      // see persistNow
    }
  }

  // Tell the other tabs what the server has now.
  private announce(rev: string, draft: string | null) {
    try {
      this.opts.drafts?.announce(rev, draft)
    } catch {
      // Other tabs then find out at their next request.
    }
  }

  // --- the other tab or device ---
  // The latest notice that arrived while a request was out or a conflict was
  // open; it is looked at when the loop next finishes.
  private queued: { rev: string; draft: string | null } | undefined

  // Another tab committed this page (its new rev arrives via the storage event).
  // Nothing unsaved here: show what the server has. Something unsaved: leave
  // everything alone and store now, so the person meets the conflict dialog
  // straight away instead of at some later save.
  noticeExternalSave(rev: string) {
    if (this.closed || rev === this.file.rev) return
    if (this.running || this.conflict) {
      this.queued = { rev, draft: this.queued?.draft ?? this.serverDraft?.version ?? null }
      return
    }
    if (!this.hasUnsaved()) void this.refresh()
    else void this.flush()
  }

  // Another tab stored (or deleted) a draft; `version` identifies it.
  noticeExternalDraft(version: string | null) {
    if (this.closed || version === (this.serverDraft?.version ?? null)) return
    if (this.running || this.conflict) {
      this.queued = { rev: this.file.rev, draft: version }
      return
    }
    void this.refresh()
  }

  private takeQueued() {
    const q = this.queued
    this.queued = undefined
    if (!q || this.closed || this.running || this.conflict) {
      if (q && !this.closed) this.queued = q
      return
    }
    if (q.rev !== this.file.rev) this.noticeExternalSave(q.rev)
    else this.noticeExternalDraft(q.draft)
  }

  // The tab is in view again. If nothing is waiting here, see whether the page
  // was changed elsewhere meanwhile (on another device, say), so the person
  // continues from what is there instead of typing on an old view and meeting a
  // conflict. One request per return to the tab, and none while there is text
  // still to store: that goes through the loop, which finds out by itself.
  noticeVisible() {
    this.noticeOnline()
    if (this.closed || this.running || this.conflict || this.hasUnsaved() || this.uncertain || this.state === 'error')
      return
    void this.refresh()
  }

  // The browser is back online.
  noticeOnline() {
    this.backoffUntil = 0
    if (this.state === 'error' && this.snapshot.error instanceof ApiError && this.snapshot.error.code === 'network')
      void this.retry()
  }

  private async refresh(again = 1) {
    const asked = this.activity
    let page: Page
    try {
      page = await this.opts.api.getPage(this.file.id)
    } catch {
      return
    }
    // Typing, a request or a resolution since the question went out make the
    // answer old; it never replaces what was typed.
    if (this.closed || this.running || this.conflict) return
    if (this.activity !== asked) {
      // The answer is old; what it saw may still be news, so ask once more.
      if (again > 0) void this.refresh(again - 1)
      return
    }
    if (page.rev === this.file.rev && sameServerDraft(page.draft, this.serverDraft)) return
    // Nothing unsaved and nothing sent without an answer: the editor's text is in
    // the file this controller adopted, or in a draft the server confirmed. The
    // server never drops a draft on its own: one that is gone was committed (its
    // text is in the page's history), or replaced or deleted by a request naming
    // that very version, that is, by someone who had it in front of them. So what
    // the server shows now succeeds what is on screen, and is shown. A draft that
    // is still there while the file moved under it comes back stale, and the
    // caller raises that as the conflict it is.
    if (!this.hasUnsaved() && !this.uncertain) {
      // The editor is replaced with the server's page, so what is adopted is what is shown.
      this.resetTo(page)
      this.opts.onExternalChange?.(page)
      return
    }
    // Otherwise the one decision. (A `proceed` is then carried out by the loop.)
    if (this.diverged({ file: page, draft: page.draft ?? null }, this.editSeq) === 'ok') void this.kick()
  }

  // --- requests ---
  // Store now (or queue behind the request that is out). Resolves when
  // nothing is left to do.
  flush(): Promise<void> {
    this.clearTimer()
    return this.kick()
  }

  // "Save version": store what is typed, then write it to the file.
  commit(): Promise<void> {
    this.clearTimer()
    if (this.conflict) return this.inflight ?? Promise.resolve()
    this.wantCommit = true
    return this.kick()
  }

  retry(): Promise<void> {
    this.authLost = false
    this.backoffUntil = 0
    if (this.retryCommit) this.wantCommit = true
    return this.kick()
  }

  // The tab is being hidden or closed: store what is unsaved with `keepalive`
  // so the browser lets the request finish. The commit is left to the server:
  // nothing that matters can depend on a request that has to outlive the page.
  flushKeepalive() {
    // The local draft first: it is what survives if the request below does not.
    this.persistNow()
    this.clearTimer()
    if (this.closed || this.conflict) return
    if (this.running) {
      // A request already out cannot be doubled; the next one goes with keepalive.
      this.keepaliveOnce = true
      return
    }
    if (this.hasUnsaved()) {
      this.keepaliveOnce = true
      void this.kick()
    }
  }

  // The editor is being left. Capture the content now, while the editor still
  // exists, then commit it on our own. Nothing typed, nothing sent.
  detach(): Promise<void> {
    this.clearTimer()
    live.delete(this)
    if (this.discarded) return Promise.resolve()
    // The local draft first; then (below) the exact text of the requests that follow.
    this.persistNow()
    if (!this.conflict && this.hasUnsaved()) {
      try {
        this.frozen = this.opts.read()
      } catch {
        // The local draft is all there is; it is offered when the page opens next.
      }
    }
    this.closed = true
    this.detached = true
    // A conflict waits for the person (the draft stays on the server and here);
    // with no session nothing can be sent, and the local draft keeps the text.
    if (this.conflict || this.authLost) return this.inflight ?? Promise.resolve()
    if (this.editSeq > 0) this.wantCommit = true
    return this.kick()
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
    live.add(this)
  }
  // Drop whatever is unsaved for good (the page has been deleted).
  discard() {
    this.suspend()
    live.delete(this)
    this.discarded = true
    this.wantCommit = false
    this.pendingDelete = null
    this.frozen = undefined
    this.forgetLocal()
  }

  // --- conflict resolution ---
  private raise(info: ConflictInfo) {
    this.conflict = info
    this.clearTimer()
    // The person's text is not on the server in a form that survives the choice
    // (the other side may replace or delete the draft that holds it), so while
    // the conflict is open it counts as unsaved and the browser keeps it.
    if (this.editSeq <= this.storedSeq) this.editSeq = this.storedSeq + 1
    this.persistNow()
    this.set('conflict')
    // After the page is left nobody is listening: the draft stays, and the
    // conflict is raised again when the page is opened next.
    if (!this.detached) this.opts.onConflict?.(info)
  }

  // The page opened with a stale server draft, or with a local draft that
  // disagrees with the server's: start in the conflict state. `unsaved` says
  // whether `info.mine` exists only in this browser.
  enterConflict(info: ConflictInfo, unsaved: boolean) {
    this.editSeq = Math.max(this.editSeq, unsaved ? 1 : 0)
    // From the GET the opener read; the dialog shows it, and the person chooses.
    this.file = info.kind === 'file' ? info.theirs : (info.after ?? this.file)
    if (info.kind === 'draft' && info.draft) {
      this.serverDraft = info.draft
      this.stale = !!info.after
    }
    this.raise(info)
  }

  // Keep the person's text: store it again on top of the file as it is now.
  keepMine(): Promise<void> {
    this.activity++
    this.setOurs([])
    this.uncertain = false
    const info = this.conflict
    this.conflict = null
    if (info?.kind === 'draft') {
      if (info.draft) {
        // The draft the dialog showed; kept as the server's because the person chose to replace it.
        this.serverDraft = info.draft
        this.stale = false
        // The text now sits on the rev that draft was based on, which is the file as
        // it is. Not so when the file has still to be weighed (`after`): until the
        // person has been shown that file, their text stays on the rev it was typed on.
        if (!info.after) this.lineage = info.draft.baseRev
      }
      if (info.after) {
        // The other draft was stale as well: the text still has to be weighed
        // against the file before it can be stored.
        // The file the dialog showed.
        this.file = info.after
        this.stale = true
        this.raise({ kind: 'file', theirs: info.after, mine: info.mine })
        return Promise.resolve()
      }
    } else if (info?.kind === 'file') {
      // The file the dialog showed; adopted because the person chose to keep their
      // text on it. "Mine" is what the dialog showed and the editor still shows,
      // body included: `openBody` stays, so a body the person never edited is sent
      // as it is on screen, not swapped for the file's behind their back. Header
      // fields this app does not show come from the file as it is now.
      this.file = info.theirs
      this.lineage = info.theirs.rev
    }
    this.forceStore = true
    this.set('dirty')
    return this.flush()
  }

  // Take the other side. The caller replaces the editor content with the page
  // returned.
  useTheirs(): Page {
    this.activity++
    this.setOurs([])
    this.uncertain = false
    const info = this.conflict
    this.conflict = null
    this.clearTimer()
    this.frozen = undefined
    this.editSeq = 0
    this.storedSeq = 0
    this.forceStore = false
    this.forgetLocal()
    if (!info) {
      this.settle()
      return this.file
    }
    if (info.kind === 'file') {
      // The file the dialog showed; adopted because the person took it.
      this.file = info.theirs
      this.lineage = info.theirs.rev
      this.openBody = info.theirs.body
      // The draft on the server is the person's text that they just gave up.
      if (this.serverDraft) this.pendingDelete = this.serverDraft.version
      this.set(this.pendingDelete ? 'storing' : 'saved')
      void this.flush()
    } else if (info.draft) {
      // The draft the dialog showed; adopted because the person took it.
      this.serverDraft = info.draft
      this.lineage = info.draft.baseRev
      // Taken: the draft is now the person's own text, so the server committing it
      // later is recognised as theirs.
      this.setOurs([oursOf(info.draft.baseRev, info.draft)])
      this.stale = false
      this.openBody = info.draft.body
      if (info.after) {
        // Its text is now the person's; it still disagrees with the file.
        // The file the dialog showed.
        this.file = info.after
        this.stale = true
        this.raise({
          kind: 'file',
          theirs: info.after,
          mine: { ...fieldsOf(info.draft.meta, info.after.meta), body: info.draft.body },
        })
      } else this.settle()
    }
    return info.theirs
  }

  // The page was changed by something other than a save (a move): learn the
  // new path without touching the content the person is editing.
  adopt(moved: Page) {
    // A move does not change content, so the same rev and content is only a new
    // path. Anything else means the page was changed by someone else: that is a
    // divergence, dealt with like any other notice (shown when nothing is typed,
    // reconciled when something is), never adopted here.
    if (moved.rev === this.file.rev && sameContent(moved, this.file)) {
      this.file = { ...this.file, path: moved.path, folder: moved.folder, updated: moved.updated }
      this.notify()
      return
    }
    this.noticeExternalSave(moved.rev)
  }

  // An action of the person's own changed the header of the file on the server (a field of its type got another key),
  // and the caller has made the same change to the header being edited: the new file becomes the base of the text, as
  // if it had been read with it. The body being typed stays (the action changes no body); a draft the server holds is
  // on the old file now and is replaced by the next store. Without this the next save meets a newer file, and keeping
  // one's own text there would write the old key back.
  async rebaseOnto(page: Page) {
    if (this.inflight) await this.inflight.catch(() => {})
    if (this.closed || this.conflict) return
    this.activity++
    const { draft, ...file } = page
    this.file = file
    this.lineage = file.rev
    this.serverDraft = draft ? toServerDraft(draft) : null
    this.stale = !!draft?.stale
    this.setOurs(draft ? [oursOf(draft.baseRev, draft)] : [])
    this.uncertain = false
    this.announce(file.rev, this.serverDraft?.version ?? null)
    // What is typed, and a draft on the old file, are stored again on the new one.
    if (this.hasUnsaved() || this.serverDraft) this.markDirty()
    else this.settle()
  }

  // Replace everything this controller knows with what the server has (the
  // caller shows the same in the editor).
  resetTo(page: Page) {
    this.activity++
    // The text is replaced by the server's: the browser's copy of the old one has no more to protect.
    this.clearLocal()
    this.setOurs([])
    this.uncertain = false
    this.clearTimer()
    this.frozen = undefined
    // Replaces the editor's text with this page, so what is adopted is what is shown.
    this.file = page
    this.lineage = page.draft?.baseRev ?? page.rev
    this.serverDraft = page.draft ? toServerDraft(page.draft) : null
    this.setOurs(page.draft ? [oursOf(page.draft.baseRev, page.draft)] : [])
    this.uncertain = false
    this.stale = !!page.draft?.stale
    this.openBody = (page.draft ?? page).body
    this.editSeq = 0
    this.storedSeq = 0
    this.forceStore = false
    // Nothing queued for the old state applies to this one.
    this.pendingDelete = null
    this.wantCommit = false
    this.retryCommit = false
    this.settle()
  }

  // --- the loop ---
  private kick(): Promise<void> {
    if (this.conflict) return this.inflight ?? Promise.resolve()
    if (this.running) return this.inflight ?? Promise.resolve()
    this.running = true
    const p = this.loop()
    // A loop with nothing to do has finished by now.
    if (this.running) {
      this.inflight = p
      inflightByKey.set(keyOf(this.scope, this.file.id), p)
    }
    return p
  }

  private nextStep(): Step {
    if (this.conflict) return 'none'
    if (this.pendingDelete) return 'delete'
    if (this.forceStore || this.hasUnsaved()) {
      // Typed while a request was out: the debounce timer stores it once the
      // person pauses.
      return this.timer !== undefined ? 'none' : 'store'
    }
    if (this.wantCommit && this.serverDraft && !this.stale) return 'commit'
    // Asked to commit with nothing to commit: done.
    this.wantCommit = false
    return 'none'
  }

  private async loop(): Promise<void> {
    const mine = this.file.id
    try {
      for (let guard = 0; guard < 12; guard++) {
        const step = this.nextStep()
        if (step === 'none') break
        const r =
          step === 'delete'
            ? await this.deleteStep()
            : step === 'store'
              ? await this.storeStep()
              : await this.commitStep()
        if (r === 'stop') break
        this.failures = 0
        this.backoffUntil = 0
      }
    } finally {
      this.running = false
      this.activity++
      const p = this.inflight
      this.inflight = null
      if (p && inflightByKey.get(keyOf(this.scope, mine)) === p) inflightByKey.delete(keyOf(this.scope, mine))
      this.takeQueued()
    }
  }

  private normalize(body: string): string {
    let v = this.normalized.get(body)
    if (v === undefined) {
      v = this.opts.normalizeBody ? this.opts.normalizeBody(body) : body
      if (this.normalized.size >= 4) this.normalized.clear()
      this.normalized.set(body, v)
    }
    return v
  }

  // The typed content, with an unedited body filled in.
  private readContent(): Draft {
    const d = this.frozen ?? this.opts.read()
    return d.body === undefined ? { ...d, body: this.openBody } : d
  }

  private async deleteStep(): Promise<StepResult> {
    this.activity++
    this.set('storing')
    const version = this.pendingDelete ?? ''
    try {
      await this.opts.api.deleteDraft(this.file.id, version, this.file.rev)
    } catch (e) {
      if (isConflict(e) && e.draft !== undefined && e.current) {
        this.pendingDelete = null
        return this.diverged({ file: e.current, draft: e.draft }, this.editSeq)
      }
      this.fail(e)
      return 'stop'
    }
    // 204 to a delete that named the version and the rev: the server checked it was
    // that draft and that the file is still the one this controller knows.
    if (this.serverDraft?.version === version) {
      this.serverDraft = null
      this.stale = false
    }
    this.pendingDelete = null
    this.announce(this.file.rev, this.serverDraft?.version ?? null)
    this.settle()
    return 'ok'
  }

  // The server confirmed holding the content as of edit `seq` (as a draft, or
  // because it equals the file).
  // `where` is what holds the text now. In the file, the browser's copy has done
  // its job and goes. In a draft the server confirmed, the copy is no longer what
  // protects the text (the server does not drop a draft on its own, see
  // `refresh`); it is rewritten marked `confirmed` with the draft's version, which
  // makes it inert when the page is opened again (no old text coming back as a
  // question after the page was edited elsewhere) and leaves something to offer
  // back should the page itself be deleted on another device.
  private markStored(seq: number, where: 'file' | 'draft') {
    this.storedSeq = Math.max(this.storedSeq, seq)
    this.forceStore = false
    // The text is in the file and nothing of ours is outstanding: nothing is left to recognise.
    if (where === 'file' && !this.uncertain) this.setOurs([])
    if (this.editSeq === seq) {
      this.frozen = undefined
      if (where === 'file') this.clearLocal()
      else {
        // Unless another tab has written a copy of its own under the same key.
        const stored = this.opts.drafts?.tokenOf()
        if (stored == null || stored === this.draftToken) this.persistNow(true)
      }
    } else if (!this.detached) {
      // More was typed meanwhile: the local draft is rewritten on top of what
      // the server has now.
      this.persistNow()
    } else if (this.draftToken !== undefined) {
      // Left the page with more queued: the editor cannot be read any more, but
      // the stored draft has all of it. Point it at what the server has now, so
      // that if the next request fails it is not mistaken for a conflict with
      // our own store.
      this.opts.drafts?.rebase(this.draftToken, this.lineageRev(), this.knownOurs())
    }
  }

  private setOurs(list: Ours[]) {
    this.ours = list
    this.oursFull = false
  }
  // What counts as ours: all of it, or nothing once the cap was reached.
  private knownOurs(): Ours[] {
    return this.oursFull ? [] : this.ours
  }

  // Remember a draft we are about to send: if its answer is lost it may still
  // have landed, and a later observation of it is recognised by its content.
  private remember(c: { baseRev: string; meta: PageMeta; body: string }) {
    // Each distinct content once (the same text stored again is one entry), newest last.
    const entry = oursOf(c.baseRev, c)
    if (this.ours.some((o) => o.baseRev === entry.baseRev && o.digest === entry.digest)) return
    if (this.ours.length >= OURS_CAP) this.oursFull = true
    else this.ours.push(entry)
  }

  // The local draft learns what is about to be sent, before it goes: a tab closed
  // while the request is out leaves a record that recognises the draft that may
  // have landed.
  private syncSent() {
    if (this.draftToken === undefined) return
    try {
      this.opts.drafts?.rebase(this.draftToken, this.lineageRev(), this.knownOurs())
    } catch {
      // see persistNow
    }
  }

  // Adopt a new file rev: our own older text was committed (or the file moved
  // and the decision said so). What we sent on the old rev is forgotten.
  private adoptFile(file: Page) {
    const { draft: _draft, ...bare } = file
    void _draft
    // Only reached through `reconcile` (the file is our own text, committed).
    this.file = bare
    this.lineage = bare.rev
    this.setOurs([])
    this.openBody = bare.body
    this.opts.onCommitted?.(bare)
    this.announce(bare.rev, null)
  }

  // THE place where a divergence is dealt with: `server` is what the server
  // showed (a 409 body, or a GET); the rules are in reconcile.ts.
  private diverged(server: { file: Page; draft: PageDraft | null }, seq: number): StepResult {
    this.uncertain = false
    if (!this.detached) this.persistNow()
    const { draft: _d, ...file } = server.file
    void _d
    let mine: Draft
    try {
      mine = this.readContent()
    } catch (e) {
      this.fail(e)
      return 'stop'
    }
    const dv = server.draft
    const norm = (p: Page) => () => this.normalize(p.body)
    const equalsFile = buildSave(file, mine, norm(file)) === null
    const equalsDraft = dv ? buildSave(draftAsPage(file, dv), mine, () => this.normalize(dv.body)) === null : false
    const d = reconcile(
      { fileRev: this.file.rev, ours: this.knownOurs() },
      { file, draft: dv },
      { equalsFile, equalsDraft },
    )
    switch (d.kind) {
      case 'conflict-draft':
        // `reconcile` decided; every assignment in this method follows its answer.
        this.file = file
        this.raise(draftConflict(file, dv!, mine, d.afterFile))
        return 'stop'
      case 'conflict-file':
        this.file = file
        this.setOurs([])
        this.serverDraft = dv ? toServerDraft(dv) : null
        this.stale = !!dv
        this.wantCommit = false
        this.raise({ kind: 'file', theirs: file, mine })
        return 'stop'
      case 'holds':
        if (d.adoptFile) this.adoptFile(file)
        else this.file = file
        this.serverDraft = d.where === 'draft' && dv ? toServerDraft(dv) : null
        this.stale = false
        this.wantCommit = this.wantCommit && d.where === 'draft'
        // Observed: what is still ours is the draft there, if any.
        this.setOurs(dv && d.where === 'draft' ? [oursOf(dv.baseRev, dv)] : [])
        this.markStored(seq, d.where)
        this.settle()
        return 'ok'
      case 'proceed':
        if (d.adoptFile) this.adoptFile(file)
        else this.file = file
        this.serverDraft = dv ? toServerDraft(dv) : null
        this.stale = !!dv?.stale
        // Observed: what is still ours is the draft there, if any.
        this.setOurs(dv ? [oursOf(dv.baseRev, dv)] : [])
        if (d.then === 'delete') this.pendingDelete = dv ? dv.version : null
        else this.forceStore = true
        return 'ok'
    }
  }

  private async storeStep(): Promise<StepResult> {
    this.activity++
    const seq = this.editSeq
    const keepalive = this.keepaliveOnce
    this.keepaliveOnce = false
    let content: Draft
    let input: SavePageInput | null
    let sameAsFile: boolean
    // A store that was sent and never answered: before anything else, find out what
    // the server holds. (Offline the question fails too, so nothing new is sent.)
    if (this.uncertain) return this.observe(seq)
    const prev = this.serverDraft?.version ?? ''
    try {
      content = this.readContent()
      // What the server shows: its draft when it has a usable one, else the file.
      const shown = this.serverDraft && !this.stale ? draftAsPage(this.file, this.serverDraft) : this.file
      input = buildSave(shown, content, () => this.normalize(shown.body))
      sameAsFile = buildSave(this.file, content, () => this.normalize(this.file.body)) === null
    } catch (e) {
      // The editor document had no Markdown form, or the editor is gone.
      this.fail(e)
      return 'stop'
    }

    if (sameAsFile) {
      // The text is the file's own. A draft on the server would only get
      // committed over it, so it goes (by version). A store we never heard back
      // about may have landed, so with one unanswered "equal to the file" proves
      // nothing: ask the server what it has.
      if (this.serverDraft) {
        this.pendingDelete = this.serverDraft.version
        const before = this.file.rev
        const r = await this.deleteStep()
        // Saved only if the delete itself was answered: a refusal went through
        // `diverged`, which has marked what it proved.
        if (r === 'ok' && !this.serverDraft && !this.pendingDelete && this.file.rev === before && !this.forceStore) {
          this.markStored(seq, 'file')
          this.settle()
        }
        return r
      }
      this.markStored(seq, 'file')
      this.settle()
      return 'ok'
    }
    if (!input) {
      // Equal to the draft the server holds, as far as we know. A store that was
      // never answered may have replaced it since: ask before claiming anything.
      this.markStored(seq, 'draft')
      this.settle()
      return 'ok'
    }

    this.set('storing')
    let receipt: DraftReceipt
    const sent = { baseRev: input.baseRev, meta: input.meta, body: input.body }
    this.remember(sent)
    // The browser's copy is on disk, with what is about to be sent, before the
    // request leaves: if the tab dies while it is out, the next visit has the text
    // and recognises the draft that may have landed.
    if (!this.detached) this.persistNow()
    this.syncSent()
    this.uncertain = true
    try {
      receipt = await this.opts.api.putDraft(this.file.id, { ...input, prev }, { keepalive })
    } catch (e) {
      if (isConflict(e) && e.draft !== undefined && e.current)
        return this.diverged({ file: e.current, draft: e.draft }, seq)
      this.fail(e)
      return 'stop'
    }
    this.uncertain = false
    this.opts.onStored?.(this.file.id)
    const stored: ServerDraft = { version: receipt.version, ...sent, updated: receipt.updated }
    if (!receipt.stale) {
      // 200: the server checked `prev` was its draft and the file was still at baseRev.
      this.serverDraft = stored
      this.stale = false
      // Answered, with `prev` checked: every earlier store was replaced by this one, so only
      // this one can still be committed behind our back. The list stays short.
      this.setOurs([oursOf(sent.baseRev, sent)])
      this.syncSent()
      this.markStored(seq, 'draft')
      this.announce(this.file.rev, stored.version)
      this.settle()
      return 'ok'
    }
    // Stale: the file is no longer at the rev we built on. See what it is.
    // Stored but stale: the server keeps it; `observe` reconciles the file next.
    this.serverDraft = stored
    this.stale = true
    return this.observe(seq)
  }

  // Ask the server what it has and let the one decision deal with it.
  private async observe(seq: number): Promise<StepResult> {
    let page: Page
    try {
      page = await this.opts.api.getPage(this.file.id)
    } catch (e) {
      this.fail(e)
      return 'stop'
    }
    return this.diverged({ file: page, draft: page.draft ?? null }, seq)
  }

  private async commitStep(): Promise<StepResult> {
    this.activity++
    this.set('committing')
    const version = this.serverDraft?.version ?? ''
    let page: Page
    try {
      page = await this.opts.api.commit(this.file.id, version)
    } catch (e) {
      if (isConflict(e) && e.current && e.draft !== undefined)
        return this.diverged({ file: e.current, draft: e.draft }, this.editSeq)
      this.fail(e)
      return 'stop'
    }
    this.committed(page)
    return 'ok'
  }

  // The file now has our draft's content, as `page`: the server committed the
  // version we named.
  private committed(page: Page) {
    // 200 to a commit that named the version: the server checked it was that draft.
    this.file = page
    this.lineage = page.rev
    this.setOurs([])
    this.serverDraft = null
    this.stale = false
    this.openBody = page.body
    this.wantCommit = false
    this.retryCommit = false
    // Text typed while the commit was out is not in the file. Its local draft
    // is rewritten on top of the new rev, and the loop stores it next.
    if (this.hasUnsaved()) {
      if (!this.detached) this.persistNow()
      else if (this.draftToken !== undefined) this.opts.drafts?.rebase(this.draftToken, page.rev, [])
    } else this.clearLocal()
    this.opts.onCommitted?.(page)
    this.announce(page.rev, null)
    this.settle()
  }
}

// Two drafts of the same page: the text in hand and the one on the server. When
// the server's is itself stale, the file is weighed against the winner after.
const draftConflict = (file: Page, other: PageDraft, mine: Draft, afterFile = other.stale): ConflictInfo => ({
  kind: 'draft',
  theirs: draftAsPage(file, other),
  mine,
  draft: toServerDraft(other),
  ...(afterFile ? { after: file } : {}),
})
const toServerDraft = (d: PageDraft): ServerDraft => ({
  version: d.version,
  baseRev: d.baseRev,
  meta: d.meta,
  body: d.body,
  updated: d.updated,
})
const sameServerDraft = (a: PageDraft | undefined, b: ServerDraft | null) =>
  (a?.version ?? null) === (b?.version ?? null)
