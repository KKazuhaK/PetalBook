// Deciding what to do with a local draft when a page is opened.

import type { Page } from '@/api/types'
import type { DocJSON } from '@/markdown'
import type { StoredDraft } from './draftStore'
import { buildSave, type Draft, type Fields } from './saveController'

export type RestorePlan =
  // No draft, or one that says nothing the server does not already say.
  // `stale` means a draft exists and should be removed.
  | { kind: 'none'; stale: boolean }
  // Typed on top of the revision the server still has: put it back and save normally.
  | { kind: 'apply'; fields: Fields; doc?: DocJSON; token: string }
  // Typed on top of an older revision: show both sides and let the person choose.
  | { kind: 'conflict'; fields: Fields; doc?: DocJSON; mine: Draft; token: string }

export function planRestore(page: Page, draft: StoredDraft | null, toMarkdown: (doc: DocJSON) => string, normalizeBody: (md: string) => string): RestorePlan {
  if (!draft) return { kind: 'none', stale: false }
  const mine: Draft = { ...draft.fields, body: draft.bodyJson ? toMarkdown(draft.bodyJson) : undefined }
  // Same as the server version (a save landed after the draft was taken, or
  // the edit was undone): nothing to restore.
  if (buildSave(page, mine, () => normalizeBody(page.body)) === null) return { kind: 'none', stale: true }
  if (draft.baseRev === page.rev) return { kind: 'apply', fields: draft.fields, doc: draft.bodyJson, token: draft.token }
  return { kind: 'conflict', fields: draft.fields, doc: draft.bodyJson, token: draft.token, mine: { ...mine, body: mine.body ?? page.body } }
}
