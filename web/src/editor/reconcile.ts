// The one decision: what to do when what the server shows may not be what this
// controller believes. Every divergence funnels here: any 409 to a store, a
// delete or a commit, any `stale: true`, any state with a request that was sent
// and never answered. No other code decides whether something is "ours" or marks
// text saved after a divergence.
//
// Inputs
//   known.fileRev   the file rev the controller last adopted
//   known.ours      a digest of every full content (header and body, with the base
//                   rev) this controller has sent as a draft since it adopted that
//                   rev, answered or not, plus the draft it was opened on. Emptied
//                   when a new file rev is adopted and at every conflict resolution.
//                   (If its cap were ever reached the caller passes it empty: the
//                   answer is then a conflict dialog, never a silent adoption.)
//   server          what the server shows, as just observed (the body of a 409, or
//                   a GET): the file with its rev, and the caller's draft or null
//   editor          whether the editor's text equals that file / that draft
//
// Definitions
//   moved       server.file.rev != known.fileRev
//   fileOurs    the server file's whole content is in known.ours
//   draftOurs   the server draft's whole content and base rev are in known.ours
//
// Rules, first match wins
//   #  draft     file          editor          decision
//   1  present   any           any             draft not ours        -> conflict-draft
//   2  any       moved, not    any                                   -> conflict-file
//                ours
//   3  none      unmoved, or   equals the file                       -> holds(file)
//                moved & ours
//   4  none      unmoved, or   differs                               -> proceed: store
//                moved & ours
//   5  ours      unmoved, or   equals the file                       -> proceed: delete the draft
//                moved & ours
//   6  ours      unmoved       equals the draft, draft not stale     -> holds(draft)
//   7  ours      unmoved, or   otherwise                             -> proceed: store
//                moved & ours
//
// "holds" is the only way to "saved" or "stored": the file or the draft, as just
// observed, is the editor's text. "proceed" with a moved file means: adopt that
// rev (our own older text was committed), forget `ours`, then do what it says
// with the editor's text. A conflict keeps the editor's text and hands the choice
// to the person. Rule 2 does not look at anything else: a file that changed
// under us and is not our own text is a conflict whatever the draft or the
// editor say.

import type { PageMeta } from '@/api/types'
import { sha256Hex } from './digest'

export interface Body {
  meta: PageMeta
  body: string
}
// A draft this controller sent, as a digest of its header and body (256 bits) and
// the base rev it was sent on. Digests, not contents: every send since the file
// rev was adopted is kept, however many, at a few dozen bytes each.
export interface Ours {
  baseRev: string
  digest: string
}
export interface ServerDraftView extends Body {
  version: string
  baseRev: string
  stale: boolean
}
export interface ServerView {
  file: Body & { rev: string }
  draft: ServerDraftView | null
}
export interface Known {
  fileRev: string
  ours: Ours[]
}
export interface EditorView {
  equalsFile: boolean
  equalsDraft: boolean
}

export type Decision =
  | { kind: 'conflict-draft'; afterFile: boolean }
  | { kind: 'conflict-file' }
  | { kind: 'holds'; where: 'file' | 'draft'; adoptFile: boolean }
  | { kind: 'proceed'; then: 'store' | 'delete'; adoptFile: boolean }

const canonical = (v: unknown): string => {
  if (Array.isArray(v)) return `[${v.map(canonical).join(',')}]`
  if (v && typeof v === 'object') {
    const o = v as Record<string, unknown>
    return `{${Object.keys(o)
      .sort()
      .map((k) => `${JSON.stringify(k)}:${canonical(o[k])}`)
      .join(',')}}`
  }
  return JSON.stringify(v) ?? 'null'
}

// Whether two header-and-body pairs say the same thing. Key order does not
// matter (the server decides it), and `id` and `created` that one side leaves
// out are the server's to keep, so they do not count.
export function sameContent(a: Body, b: Body): boolean {
  if (a.body !== b.body) return false
  const strip = (m: PageMeta, other: PageMeta) => {
    const out = { ...m }
    for (const k of ['id', 'created']) if (!(k in other)) delete out[k]
    return out
  }
  return canonical(strip(a.meta, b.meta)) === canonical(strip(b.meta, a.meta))
}

// `id` and `created` are the server's: they never tell two contents apart.
export const contentDigest = (b: Body): string => {
  const { id: _id, created: _created, ...meta } = b.meta
  void _id
  void _created
  return sha256Hex(`${canonical(meta)}\u0000${b.body}`)
}
export const oursOf = (baseRev: string, b: Body): Ours => ({ baseRev, digest: contentDigest(b) })

export function reconcile(known: Known, server: ServerView, editor: EditorView): Decision {
  const moved = server.file.rev !== known.fileRev
  const fileDigest = contentDigest(server.file)
  const fileOurs = known.ours.some((o) => o.digest === fileDigest)
  const d = server.draft
  const draftDigest = d ? contentDigest(d) : ''
  const draftOurs = d !== null && known.ours.some((o) => o.baseRev === d.baseRev && o.digest === draftDigest)

  if (d && !draftOurs) return { kind: 'conflict-draft', afterFile: d.stale || (moved && !fileOurs) }
  if (moved && !fileOurs) return { kind: 'conflict-file' }
  if (!d)
    return editor.equalsFile
      ? { kind: 'holds', where: 'file', adoptFile: moved }
      : { kind: 'proceed', then: 'store', adoptFile: moved }
  if (editor.equalsFile) return { kind: 'proceed', then: 'delete', adoptFile: moved }
  if (!moved && editor.equalsDraft && !d.stale) return { kind: 'holds', where: 'draft', adoptFile: false }
  return { kind: 'proceed', then: 'store', adoptFile: moved }
}
