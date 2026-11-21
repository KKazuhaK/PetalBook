// A local copy of what has been typed but not yet confirmed saved, per page.
//
// Autosave goes to the server; this goes to the browser's own storage, because
// that is the only thing that is still there when a tab is closed while a save
// is in flight, or when the body is too large for a `keepalive` request. It is
// written synchronously (storage has no async step to lose) and removed once a
// save of that exact state succeeds. Every storage access can fail (blocked,
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
  savedAt: number
  // Identifies this write. A tab removes a draft only if the stored one still
  // carries the token it wrote itself, so it cannot remove another tab's.
  token: string
}

export interface StoredDraft extends DraftContent {
  v: 1
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
const keyOf = (id: string) => PREFIX + id
export const savedMarkerKey = (id: string) => SAVED_PREFIX + id
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
  return !!x && typeof x.title === 'string' && typeof x.subtitle === 'string' && Array.isArray(x.tags) && x.tags.every((t) => typeof t === 'string')
}

export function loadDraft(id: string, storage: KeyValueStorage | null = browserStorage(), now: number = Date.now()): StoredDraft | null {
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
    if (d && d.v === 1 && d.id === id && typeof d.baseRev === 'string' && typeof d.token === 'string' && isFields(d.fields) && typeof d.savedAt === 'number') {
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
export function saveDraft(id: string, content: DraftContent, storage: KeyValueStorage | null = browserStorage()): boolean {
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

// Point the draft with `token` at a newer server revision (same content).
export function rebaseDraft(id: string, token: string, baseRev: string, storage: KeyValueStorage | null = browserStorage()): void {
  if (!storage) return
  try {
    const raw = storage.getItem(keyOf(id))
    if (raw === null) return
    const d = JSON.parse(raw) as StoredDraft
    if (d.token === token) storage.setItem(keyOf(id), JSON.stringify({ ...d, baseRev }))
  } catch {
    // Best effort; a failure leaves the draft on its old rev.
  }
}

// Written after every successful save. A `storage` event is delivered to the
// *other* tabs, which is how they learn that this page moved on.
export interface SavedMarker {
  rev: string
  at: number
}
export function announceSaved(id: string, rev: string, storage: KeyValueStorage | null = browserStorage(), now: number = Date.now()): void {
  try {
    storage?.setItem(savedMarkerKey(id), JSON.stringify({ rev, at: now } satisfies SavedMarker))
  } catch {
    // Other tabs then find out at their next save (a normal conflict).
  }
}
export function parseSavedMarker(value: string | null): SavedMarker | null {
  if (!value) return null
  try {
    const m = JSON.parse(value) as SavedMarker
    return typeof m.rev === 'string' ? m : null
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
  rebase(token: string, baseRev: string): void
  announceSaved(rev: string): void
}

export function draftStoreFor(id: string, storage: KeyValueStorage | null = browserStorage()): DraftStore {
  pruneExpired(storage)
  return {
    write: (content) => void saveDraft(id, content, storage),
    clear: () => clearDraft(id, storage),
    clearIf: (token) => clearDraftIf(id, token, storage),
    rebase: (token, rev) => rebaseDraft(id, token, rev, storage),
    announceSaved: (rev) => announceSaved(id, rev, storage),
  }
}
