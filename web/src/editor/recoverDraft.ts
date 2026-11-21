// A page that no longer exists on the server may still have a local draft: text
// typed and never saved. Offer to keep it as a new page.

import type { CreatePageInput, Page, PageMeta } from '@/api/types'
import type { DocJSON } from '@/markdown'
import { clearDraft, type KeyValueStorage, type StoredDraft } from './draftStore'
import { cleanFields } from './saveController'

export function draftToCreateInput(draft: StoredDraft, toMarkdown: (doc: DocJSON) => string): CreatePageInput {
  const f = cleanFields(draft.fields)
  const meta: PageMeta = {}
  if (f.title) meta.title = f.title
  if (f.subtitle) meta.subtitle = f.subtitle
  if (f.source) meta.source = f.source
  if (f.tags.length) meta.tags = f.tags
  // The draft does not remember a folder: the new page goes to the root.
  return { folder: '', meta, body: draft.bodyJson ? toMarkdown(draft.bodyJson) : '' }
}

// The draft is removed only after the new page exists, so a failed attempt
// loses nothing.
export async function recoverDraft(
  id: string,
  draft: StoredDraft,
  deps: { create: (input: CreatePageInput) => Promise<Page>; toMarkdown: (doc: DocJSON) => string; storage?: KeyValueStorage | null },
): Promise<Page> {
  const page = await deps.create(draftToCreateInput(draft, deps.toMarkdown))
  if (deps.storage === undefined) clearDraft(id)
  else clearDraft(id, deps.storage)
  return page
}
