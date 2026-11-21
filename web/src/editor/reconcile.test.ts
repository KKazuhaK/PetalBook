// The one decision, over its whole state space, against the table in reconcile.ts
// written out a second time as a plain oracle, plus the situations that were
// each a bug once.

import { describe, expect, it } from 'vitest'
import { oursOf, reconcile, sameContent, type Decision, type EditorView, type Known, type ServerView } from './reconcile'

const OURS_BODY = { meta: { id: 'P', title: 'T' }, body: 'ours\n' }
const OURS = oursOf('r1', OURS_BODY)
const OTHER = { meta: { id: 'P', title: 'T' }, body: 'other\n' }
const known = (): Known => ({ fileRev: 'r1', ours: [OURS] })

type Draft = 'none' | 'ours' | 'ours-stale' | 'foreign' | 'foreign-stale'
const server = (moved: boolean, fileOurs: boolean, draft: Draft): ServerView => ({
  file: { rev: moved ? 'r2' : 'r1', ...(fileOurs ? OURS_BODY : OTHER) },
  draft:
    draft === 'none'
      ? null
      : { version: 'v', baseRev: 'r1', stale: draft.endsWith('stale'), ...(draft.startsWith('ours') ? OURS_BODY : OTHER) },
})

// The table, again, as straight-line code.
function oracle(moved: boolean, fileOurs: boolean, draft: Draft, e: EditorView): Decision {
  if (draft.startsWith('foreign')) return { kind: 'conflict-draft', afterFile: draft.endsWith('stale') || (moved && !fileOurs) }
  if (moved && !fileOurs) return { kind: 'conflict-file' }
  if (draft === 'none') return e.equalsFile ? { kind: 'holds', where: 'file', adoptFile: moved } : { kind: 'proceed', then: 'store', adoptFile: moved }
  if (e.equalsFile) return { kind: 'proceed', then: 'delete', adoptFile: moved }
  if (!moved && e.equalsDraft && draft === 'ours') return { kind: 'holds', where: 'draft', adoptFile: false }
  return { kind: 'proceed', then: 'store', adoptFile: moved }
}

describe('reconcile over every combination', () => {
  const bools = [false, true]
  const drafts: Draft[] = ['none', 'ours', 'ours-stale', 'foreign', 'foreign-stale']
  it('agrees with the table in all 80 states', () => {
    let n = 0
    for (const moved of bools)
      for (const fileOurs of bools)
        for (const draft of drafts)
          for (const equalsFile of bools)
            for (const equalsDraft of bools) {
              const e = { equalsFile, equalsDraft }
              expect(reconcile(known(), server(moved, fileOurs, draft), e), JSON.stringify({ moved, fileOurs, draft, e })).toEqual(oracle(moved, fileOurs, draft, e))
              n++
            }
    expect(n).toBe(80)
  })

  it('"holds" (the only way to saved) is never returned unless the editor equals what was observed', () => {
    for (const moved of bools)
      for (const fileOurs of bools)
        for (const draft of drafts)
          for (const equalsFile of bools)
            for (const equalsDraft of bools) {
              const d = reconcile(known(), server(moved, fileOurs, draft), { equalsFile, equalsDraft })
              if (d.kind === 'holds') expect(d.where === 'file' ? equalsFile : equalsDraft).toBe(true)
            }
  })

  it('a file that moved and is not ours is always a conflict, whatever else matches', () => {
    for (const draft of ['none', 'ours', 'ours-stale'] as Draft[])
      for (const equalsFile of bools)
        for (const equalsDraft of bools) {
          expect(reconcile(known(), server(true, false, draft), { equalsFile, equalsDraft }).kind).toBe('conflict-file')
        }
  })

  it('a draft that is not ours is always a conflict, whatever else matches', () => {
    for (const moved of bools)
      for (const fileOurs of bools)
        for (const equalsFile of bools)
          for (const equalsDraft of bools) {
            expect(reconcile(known(), server(moved, fileOurs, 'foreign'), { equalsFile, equalsDraft }).kind).toBe('conflict-draft')
          }
  })

  it('a file that has not moved is never a file conflict', () => {
    for (const fileOurs of bools)
      for (const draft of drafts)
        for (const equalsFile of bools)
          for (const equalsDraft of bools) {
            expect(reconcile(known(), server(false, fileOurs, draft), { equalsFile, equalsDraft }).kind).not.toBe('conflict-file')
          }
  })

  it('what we sent only counts with the base rev it was sent on', () => {
    const s = server(false, false, 'ours')
    s.draft!.baseRev = 'rOther'
    expect(reconcile(known(), s, { equalsFile: false, equalsDraft: false }).kind).toBe('conflict-draft')
  })

  it('with nothing sent, nothing is ours: even our own text coming back is a conflict until we sent it', () => {
    expect(reconcile({ fileRev: 'r1', ours: [] }, server(true, true, 'none'), { equalsFile: false, equalsDraft: false }).kind).toBe('conflict-file')
    expect(reconcile({ fileRev: 'r1', ours: [] }, server(false, false, 'ours'), { equalsFile: false, equalsDraft: false }).kind).toBe('conflict-draft')
  })
})

describe('situations that were each a bug once', () => {
  const noEdit = { equalsFile: false, equalsDraft: false }
  it('a response was lost, another device saved Z, the person undid to X: Z is not ours, a conflict (never re-based)', () => {
    expect(reconcile(known(), server(true, false, 'none'), { equalsFile: false, equalsDraft: false }).kind).toBe('conflict-file')
  })
  it('our draft was committed by the server, the person undid to the old file: not saved, the undo is stored on the new rev', () => {
    const d = reconcile(known(), server(true, true, 'none'), noEdit)
    expect(d).toEqual({ kind: 'proceed', then: 'store', adoptFile: true })
  })
  it('the delete was applied and its answer lost, the person typed again: the file is unmoved, no conflict, store', () => {
    expect(reconcile(known(), server(false, false, 'none'), noEdit)).toEqual({ kind: 'proceed', then: 'store', adoptFile: false })
  })
  it('the file equals the editor but is not ours and moved: still a conflict (rule 2 looks at nothing else)', () => {
    expect(reconcile(known(), server(true, false, 'none'), { equalsFile: true, equalsDraft: false }).kind).toBe('conflict-file')
  })
  it('after a conflict resolution `ours` is empty: our old text in the file is then someone else\'s', () => {
    expect(reconcile({ fileRev: 'r1', ours: [] }, server(true, true, 'none'), noEdit).kind).toBe('conflict-file')
  })
})

describe('sameContent', () => {
  it('ignores key order and an id or created one side leaves out, nothing else', () => {
    const a = { meta: { id: 'X', title: 't', created: 'c' }, body: 'b' }
    expect(sameContent(a, { meta: { title: 't' }, body: 'b' })).toBe(true)
    expect(sameContent(a, { meta: { title: 'u' }, body: 'b' })).toBe(false)
    expect(sameContent(a, { meta: a.meta, body: 'b\n' })).toBe(false)
  })
})
