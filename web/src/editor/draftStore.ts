// A local copy of what has been typed but not yet confirmed by the server, per page.
//
// Autosave goes to the server; this goes to the browser's own storage, because
// that is the only thing that is still there when a tab is closed while a
// request is in flight, when the body is too large for a `keepalive` request,
// or when the session ended. It is written synchronously (storage has no async
// step to lose) and removed once the server has confirmed storing that exact
// state, as a draft or as the page. Every storage access can fail (blocked,
// private window, full), and none of those failures may reach the editor.

import type { DocJSON } from '@/markdown'
import type { Fields } from './saveController'

// What is kept. The body is the editor document, not Markdown: reading it is
// cheap, so it can be taken synchronously on `pagehide`.
export interface DraftContent {
  // The server revision this was typed on top of.
  baseRev: string
  fields: Fields
  // Absent when the body was never edited.
  bodyJson?: DocJSON
  // The body as Markdown, when it was last written in the source view: that
  // text is kept as typed, so it is stored as text and not as a document.
  // When present it is the body and `bodyJson` is not.
  bodyText?: string
  savedAt: number
  // Identifies this write. A tab removes a draft only if the stored one still
  // carries the token it wrote itself, so it cannot remove another tab's.
  token: string
  // Digests of the drafts (header, body and base rev) the editor had sent to the
  // server since that revision was adopted, and of the one it was opened on. When this text
  // comes back after the page was closed, a file or draft that is exactly one of
  // them is the person's own text (the server committed it), not someone else's:
  // see reconcile.ts. An entry with `onto` is the file a commit wrote with link
  // rewrites (its rev `onto`): the text in hand is on `baseRev`, its `textRev`.
  sent?: { baseRev: string; digest: string; onto?: string }[]
  // Set when the server confirmed holding exactly this text as its draft of this
  // version. Such a copy is no longer what protects the text (the server does not
  // drop a draft on its own); it is kept only so the text can be offered back if
  // the page itself is deleted elsewhere. When the page is opened again it decides
  // nothing: see draftRestore.ts.
  confirmed?: string
}

export interface StoredDraft extends DraftContent {
  v: 1
  // The draft's reference (see draftRef): whose, in which space, of which page.
  id: string
}

// The part of the Storage interface used here, so tests can fake it.
export interface KeyValueStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
  readonly length: number
  key(index: number): string | null
}

const PREFIX = 'pb.draft.'
const SAVED_PREFIX = 'pb.saved.'
const keyOf = (ref: string) => PREFIX + ref

// A local draft belongs to a person, a space and a page: a second user of the
// same browser is never offered someone else's text. In single-user mode that
// is `local`, `default`.
export const draftRef = (userId: string, spaceId: string, pageId: string) =>
  [userId, spaceId, pageId].map(encodeURIComponent).join('/')

// Milestone 1.1 kept drafts under the page id alone. In single-user mode (the
// only person there can have been) such a draft moves to the new key, so it is
// not orphaned. In accounts mode there is no telling whose it was, so it is
// left alone (and expires like any other).
export function draftRefFor(
  userId: string,
  spaceId: string,
  pageId: string,
  storage: KeyValueStorage | null = browserStorage(),
): string {
  const ref = draftRef(userId, spaceId, pageId)
  if (!storage || userId !== 'local' || spaceId !== 'default') return ref
  try {
    const old = storage.getItem(PREFIX + pageId)
    if (old === null || storage.getItem(keyOf(ref)) !== null) return ref
    const d = JSON.parse(old) as StoredDraft
    if (d && d.id === pageId) {
      storage.setItem(keyOf(ref), JSON.stringify({ ...d, id: ref }))
      storage.removeItem(PREFIX + pageId)
    }
  } catch {
    // A draft that cannot be moved stays where it was.
  }
  return ref
}
export const savedMarkerKey = (ref: string) => SAVED_PREFIX + ref
export const DRAFT_MAX_AGE_MS = 30 * 24 * 60 * 60 * 1000

export function browserStorage(): KeyValueStorage | null {
  try {
    return window.localStorage
  } catch {
    return null
  }
}

const isFields = (f: unknown): f is Fields => {
  const x = f as Fields | null
  return (
    !!x &&
    typeof x.title === 'string' &&
    typeof x.subtitle === 'string' &&
    Array.isArray(x.source) &&
    x.source.every((t) => typeof t === 'string') &&
    Array.isArray(x.tags) &&
    x.tags.every((t) => typeof t === 'string') &&
    (x.others === undefined || (typeof x.others === 'object' && x.others !== null && !Array.isArray(x.others)))
  )
}

export function loadDraft(
  id: string,
  storage: KeyValueStorage | null = browserStorage(),
  now: number = Date.now(),
): StoredDraft | null {
  if (!storage) return null
  let raw: string | null
  try {
    raw = storage.getItem(keyOf(id))
  } catch {
    return null
  }
  if (raw === null) return null
  try {
    const d = JSON.parse(raw) as StoredDraft
    if (
      d &&
      d.v === 1 &&
      d.id === id &&
      typeof d.baseRev === 'string' &&
      typeof d.token === 'string' &&
      isFields(d.fields) &&
      (d.bodyText === undefined || typeof d.bodyText === 'string') &&
      typeof d.savedAt === 'number' &&
      (d.confirmed === undefined || typeof d.confirmed === 'string') &&
      (d.sent === undefined ||
        (Array.isArray(d.sent) &&
          d.sent.every(
            (x) =>
              x &&
              typeof x.baseRev === 'string' &&
              typeof x.digest === 'string' &&
              (x.onto === undefined || typeof x.onto === 'string'),
          )))
    ) {
      if (now - d.savedAt <= DRAFT_MAX_AGE_MS) return d
      // Too old to be what anyone still means to recover.
      clearDraft(id, storage)
      return null
    }
  } catch {
    // fall through: unreadable
  }
  // Unreadable or from an unknown format: useless, and in the way.
  clearDraft(id, storage)
  return null
}

// Other pages' drafts, oldest first, for making room.
function otherDrafts(id: string, storage: KeyValueStorage): { key: string; savedAt: number }[] {
  const out: { key: string; savedAt: number }[] = []
  try {
    for (let i = 0; i < storage.length; i++) {
      const key = storage.key(i)
      if (!key || !key.startsWith(PREFIX) || key === keyOf(id)) continue
      let savedAt = 0
      try {
        savedAt = (JSON.parse(storage.getItem(key) ?? '{}') as { savedAt?: number }).savedAt ?? 0
      } catch {
        // unreadable entries go first
      }
      out.push({ key, savedAt })
    }
  } catch {
    return []
  }
  return out.sort((a, b) => a.savedAt - b.savedAt)
}

// Returns whether the draft is now stored. When storage is full, the oldest
// drafts of other pages are dropped one at a time to make room.
export function saveDraft(
  id: string,
  content: DraftContent,
  storage: KeyValueStorage | null = browserStorage(),
): boolean {
  if (!storage) return false
  let payload: string
  try {
    payload = JSON.stringify({ v: 1, id, ...content } satisfies StoredDraft)
  } catch {
    return false
  }
  const attempt = () => {
    try {
      storage.setItem(keyOf(id), payload)
      return true
    } catch {
      return false
    }
  }
  if (attempt()) return true
  for (const victim of otherDrafts(id, storage)) {
    try {
      storage.removeItem(victim.key)
    } catch {
      return false
    }
    if (attempt()) return true
  }
  return false
}

export function clearDraft(id: string, storage: KeyValueStorage | null = browserStorage()): void {
  try {
    storage?.removeItem(keyOf(id))
  } catch {
    // Nothing to do: a draft that cannot be removed is compared with the
    // server version when the page opens, and discarded if it is the same.
  }
}

// Drop drafts (and saved markers) older than 30 days. Run when the store is
// opened; cheap, since there are few keys.
export function pruneExpired(storage: KeyValueStorage | null = browserStorage(), now: number = Date.now()): void {
  if (!storage) return
  try {
    const doomed: string[] = []
    for (let i = 0; i < storage.length; i++) {
      const key = storage.key(i)
      if (!key || !(key.startsWith(PREFIX) || key.startsWith(SAVED_PREFIX))) continue
      let at: number | undefined
      try {
        const v = JSON.parse(storage.getItem(key) ?? '') as { savedAt?: number; at?: number }
        at = v.savedAt ?? v.at
      } catch {
        // unreadable: goes
      }
      if (typeof at !== 'number' || now - at > DRAFT_MAX_AGE_MS) doomed.push(key)
    }
    for (const key of doomed) storage.removeItem(key)
  } catch {
    // Best effort.
  }
}

// The token of the stored draft, if there is one.
export function draftTokenOf(id: string, storage: KeyValueStorage | null = browserStorage()): string | null {
  try {
    const raw = storage?.getItem(keyOf(id))
    return raw == null ? null : ((JSON.parse(raw) as { token?: string }).token ?? null)
  } catch {
    return null
  }
}

// Remove the draft only if it is still the one with `token`.
export function clearDraftIf(id: string, token: string, storage: KeyValueStorage | null = browserStorage()): void {
  if (!storage) return
  try {
    const raw = storage.getItem(keyOf(id))
    if (raw === null) return
    if ((JSON.parse(raw) as { token?: string }).token === token) storage.removeItem(keyOf(id))
  } catch {
    // Unreadable or storage failing: leave it.
  }
}

// Point the draft with `token` at newer server state (same content): the file
// revision it sits on and what had been sent on it.
export function rebaseDraft(
  id: string,
  token: string,
  baseRev: string,
  sent: DraftContent['sent'],
  storage: KeyValueStorage | null = browserStorage(),
): void {
  if (!storage) return
  try {
    const raw = storage.getItem(keyOf(id))
    if (raw === null) return
    const d = JSON.parse(raw) as StoredDraft
    if (d.token === token) storage.setItem(keyOf(id), JSON.stringify({ ...d, baseRev, sent }))
  } catch {
    // Best effort; a failure leaves the draft on its old rev.
  }
}

// Written after every request that changed what the server has. A `storage`
// event is delivered to the *other* tabs, which is how they learn that this
// page moved on: `rev` is the file, `draft` the `version` of the server draft
// (null when there is none). A new value is written every time, so the event
// fires even when nothing in them changed.
export interface SavedMarker {
  rev: string
  draft: string | null
  at: number
}
export function announce(
  id: string,
  rev: string,
  draft: string | null,
  storage: KeyValueStorage | null = browserStorage(),
  now: number = Date.now(),
): void {
  try {
    storage?.setItem(savedMarkerKey(id), JSON.stringify({ rev, draft, at: now } satisfies SavedMarker))
  } catch {
    // Other tabs then find out at their next request (a normal conflict).
  }
}
export function parseSavedMarker(value: string | null): SavedMarker | null {
  if (!value) return null
  try {
    const m = JSON.parse(value) as Partial<SavedMarker>
    return typeof m.rev === 'string'
      ? { rev: m.rev, draft: typeof m.draft === 'string' ? m.draft : null, at: typeof m.at === 'number' ? m.at : 0 }
      : null
  } catch {
    return null
  }
}

// What the save controller talks to.
export interface DraftStore {
  write(content: DraftContent): void
  // Unconditional (the page was deleted, or the draft is known stale).
  clear(): void
  // Only if the stored draft is still the one written with `token`.
  clearIf(token: string): void
  // The token of the stored draft (null when there is none).
  tokenOf(): string | null
  rebase(token: string, baseRev: string, sent: DraftContent['sent']): void
  announce(rev: string, draft: string | null): void
}

export function draftStoreFor(id: string, storage: KeyValueStorage | null = browserStorage()): DraftStore {
  pruneExpired(storage)
  return {
    write: (content) => void saveDraft(id, content, storage),
    clear: () => clearDraft(id, storage),
    clearIf: (token) => clearDraftIf(id, token, storage),
    tokenOf: () => draftTokenOf(id, storage),
    rebase: (token, rev, sent) => rebaseDraft(id, token, rev, sent, storage),
    announce: (rev, draft) => announce(id, rev, draft, storage),
  }
}
