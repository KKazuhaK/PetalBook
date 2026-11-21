// Deciding what to do when a page is opened and there may be two drafts besides
// the file: the server's (`page.draft`) and this browser's (local).
//
// Nothing is decided by clocks on two machines. A local draft records the rev its
// text was typed on and digests of what had been sent from it; those go to
// `reconcile` (reconcile.ts, the one decision) together with what the server shows
// now. A file or draft that is exactly something this editor sent is its own text
// (the server committed it) and the local text goes up on top; anything else is
// shown to the person next to what the server has. A local draft that equals what
// the server holds is dropped. Whichever branch is taken, no text is dropped
// unless the server already holds it or the person chose against it.

import type { Page } from '@/api/types'
import type { DocJSON } from '@/markdown'
import type { StoredDraft } from './draftStore'
import { oursOf, reconcile, type Ours } from './reconcile'
import { buildSave, draftAsPage, fieldsOf, type ConflictInfo, type Draft, type Fields } from './saveController'

// What the editor is loaded with. `doc` is a local draft's document;
// `markdown` is the server draft's body (or the basis of a local draft that
// never edited the body); with neither, the body is the file's, untouched.
export interface Content {
  fields: Fields
  doc?: DocJSON
  markdown?: string
}

// What the controller starts out knowing (see reconcile.ts): the rev the text on
// screen is typed on, and the drafts that count as the person's own text.
export interface Seed {
  lineage: string
  ours: Ours[]
}

export type RestorePlan =
  // The editor shows the file. `dropLocal`: a local draft that says nothing the
  // server does not already say is to be removed.
  | { kind: 'file'; dropLocal: boolean; seed: Seed }
  // The editor shows the server's draft, which is already stored: nothing to send.
  // `token` is the browser's copy of the same text, kept until the text is in the file.
  | { kind: 'server'; content: Content; dropLocal: boolean; token?: string; seed: Seed }
  // The editor shows text that exists only in this browser: it is stored next.
  | { kind: 'local'; content: Content; token: string; seed: Seed }
  // Two versions of the person's text, or the text and the file. The editor
  // shows `content` (mine); the dialog offers the other side. `unsaved` says
  // whether mine exists only in this browser; `token` is the local draft.
  | { kind: 'conflict'; info: ConflictInfo; content: Content; unsaved: boolean; token?: string; dropLocal: boolean; seed: Seed }

export function planRestore(page: Page, local: StoredDraft | null, toMarkdown: (doc: DocJSON) => string, normalizeBody: (md: string) => string): RestorePlan {
  const server = page.draft
  const norm = (body: string) => () => normalizeBody(body)
  const serverContent = (): Content => ({ fields: fieldsOf(server!.meta), markdown: server!.body })
  const serverAsDraft = (): Draft => ({ ...fieldsOf(server!.meta), body: server!.body })
  // The draft the controller holds when the editor shows it.
  const held = (): Ours[] => (server ? [oursOf(server.baseRev, server)] : [])
  const serverShown = () => draftAsPage(page, server!)
  const serverDraft = () => ({ version: server!.version, baseRev: server!.baseRev, meta: server!.meta, body: server!.body, updated: server!.updated })

  // A copy the server had confirmed as its draft decides nothing: that draft is
  // still there (the plan is the server's, and the copy stays with it), or it was
  // committed, or replaced or deleted by a request that named it, and then what
  // the server shows now succeeds it and the copy goes.
  if (local?.confirmed !== undefined) {
    const plan = planRestore(page, null, toMarkdown, normalizeBody)
    const same = server?.version === local.confirmed
    if (plan.kind === 'local') return plan
    return same && plan.kind !== 'file' ? { ...plan, token: local.token, dropLocal: false } : { ...plan, dropLocal: !same }
  }

  if (!local) {
    if (!server) return { kind: 'file', dropLocal: false, seed: { lineage: page.rev, ours: [] } }
    // A draft the file has moved on from: the draft is "mine", the file "theirs"
    // (unless the draft says what the file says: a leftover, nothing to choose).
    if (server.stale && buildSave(page, serverAsDraft(), norm(page.body)) === null) return { kind: 'file', dropLocal: false, seed: { lineage: page.rev, ours: held() } }
    if (server.stale) return { kind: 'conflict', info: { kind: 'file', theirs: page, mine: serverAsDraft() }, content: serverContent(), unsaved: false, dropLocal: false, seed: { lineage: server.baseRev, ours: held() } }
    return { kind: 'server', content: serverContent(), dropLocal: false, seed: { lineage: server.baseRev, ours: held() } }
  }

  // The local text as a draft, its body filled in from what it was typed on.
  const typed: Draft = { ...local.fields, body: local.bodyJson ? toMarkdown(local.bodyJson) : undefined }
  const localContent = (basisBody: string): Content => ({ fields: local.fields, ...(local.bodyJson ? { doc: local.bodyJson } : { markdown: basisBody }) })
  // What the page was when the text was typed, and what had been sent from it.
  const known = { fileRev: local.baseRev, ours: local.sent ?? [] }

  if (!server) {
    const mine: Draft = { ...typed, body: typed.body ?? page.body }
    // Same as the file (a commit landed after the draft was taken, or the edit
    // was undone): the file, as just observed, is the text. Nothing to restore.
    const equalsFile = buildSave(page, mine, norm(page.body)) === null
    if (equalsFile) return { kind: 'file', dropLocal: true, seed: { lineage: page.rev, ours: [] } }
    const d = reconcile(known, { file: page, draft: null }, { equalsFile, equalsDraft: false })
    // Typed on a file that has since changed and is not our own text: both sides.
    if (d.kind === 'conflict-file') {
      // A text that was stored on the server as a draft (it has sent digests) whose draft
      // is gone, with the page moved on, is an older copy: say so, and when it was written.
      const older = local.sent && local.sent.length > 0 ? { older: { savedAt: local.savedAt } } : {}
      return { kind: 'conflict', info: { kind: 'file', theirs: page, mine, ...older }, content: localContent(page.body), unsaved: true, token: local.token, dropLocal: false, seed: { lineage: local.baseRev, ours: [] } }
    }
    // Our own text was committed and the file moved to it: build on that file, as adopting it does.
    const adopted = d.kind === 'proceed' && d.adoptFile
    return { kind: 'local', content: localContent(page.body), token: local.token, seed: adopted ? { lineage: page.rev, ours: [] } : { lineage: local.baseRev, ours: known.ours } }
  }

  const mine: Draft = { ...typed, body: typed.body ?? server.body }
  const equalsDraft = buildSave(serverShown(), mine, norm(server.body)) === null
  // The server has all of it, and the draft can be committed as it is. The copy
  // stays with it (the controller marks it confirmed at its next store).
  if (equalsDraft && !server.stale) return { kind: 'server', content: serverContent(), dropLocal: false, token: local.token, seed: { lineage: server.baseRev, ours: held() } }
  // A stale draft that says what the file says is a leftover, nothing to choose.
  if (equalsDraft && buildSave(page, serverAsDraft(), norm(page.body)) === null) return { kind: 'file', dropLocal: true, seed: { lineage: page.rev, ours: held() } }
  const equalsFile = buildSave(page, mine, norm(page.body)) === null
  const d = reconcile(known, { file: page, draft: server }, { equalsFile, equalsDraft })
  if (d.kind === 'conflict-draft') {
    // Another draft than the one this text was typed on: the person's two texts.
    // When the server's is also stale, the file is weighed against the winner afterwards.
    return {
      kind: 'conflict',
      info: { kind: 'draft', theirs: serverShown(), mine, draft: serverDraft(), ...(d.afterFile ? { after: page } : {}) },
      content: localContent(server.body),
      unsaved: true,
      token: local.token,
      dropLocal: false,
      seed: { lineage: local.baseRev, ours: [] },
    }
  }
  if (d.kind === 'conflict-file') return { kind: 'conflict', info: { kind: 'file', theirs: page, mine }, content: localContent(server.body), unsaved: true, token: local.token, dropLocal: false, seed: { lineage: local.baseRev, ours: [] } }
  // Typed on top of the server draft that is there: newer, so it goes up.
  const adopted = d.kind === 'proceed' && d.adoptFile
  return { kind: 'local', content: localContent(server.body), token: local.token, seed: adopted ? { lineage: page.rev, ours: [] } : { lineage: local.baseRev, ours: known.ours } }
}
