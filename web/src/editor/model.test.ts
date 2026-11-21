// A model-based test of the whole editing path: the real SaveController, the real
// restore logic and local-draft store, against the in-memory server (whose draft
// rules the contract tests keep equal to the real server's), driven by many
// random sequences of what a person and the server's world can do. Seeded and
// deterministic: a failure prints the seed and the steps, and
//   MODEL_SEED=<seed> npx vitest run src/editor/model.test.ts
// replays it alone.
//
// Who is who. "A" is the person at this editor; "B" is the same user on a second
// device (it acts on the same server, with its own texts "B<n>" that A never
// typed, and also copies of texts A once sent or gave up, which is what makes
// "ours" and "someone else's" look alike). Every file rev and every draft version
// has an author, so "replaced" can be decided by provenance and not by text.
//
// Invariants checked after every step
//  I1  the controller says "saved" or "stored" (drafted) only if the server's file
//      or the person's server draft equals the editor's text. The one exclusion:
//      the second device acted and the controller has made no request since, so
//      it cannot have noticed (nothing is sent when text equals the file it knows).
//  I2  a file rev written by B is replaced by A's content (a commit of an A-authored
//      draft, by A or by the server's own commit) only if the person was shown that
//      rev's text: the editor displayed it from the file (a reload that opened on
//      the file, a refresh, a conflict dialog, "take theirs"), or opened on a draft
//      based on it (the person continues from that draft), or A committed it. A local
//      draft or another text on screen does not count.
//  I3  the editor's latest typed text (an "A" text) exists somewhere: after leaving
//      or closing, in the local draft, the server draft or the file; after quiescence
//      (retries done, no conflict open) in the server draft or the file. Excluded: a
//      text equal to a file version the controller itself held (it is in the file's
//      history, and going back to it is "unchanged"), and anything the server handed
//      the editor ("F0", "B<n>") rather than the person typing it.
//  I4  no conflict is raised when B never acted: lost responses, failed requests and
//      the server's own commits alone never make a dialog.
//  I5  A replaces or deletes a draft written by B only when the person chose to keep
//      their text (a conflict resolved by "keep mine"), or when that draft's base rev
//      and content are exactly something A sent itself.
//  I7  a draft written by B that appears after the page opened becomes the controller's own
//      (the version it will replace or commit) only if the editor displayed it, or its base
//      rev and content are exactly something A sent.
//  I8  the editor's text is never replaced by a controller reset (a refresh after a notice, a
//      move that changed the file) unless the server has held it: as the file, or as a draft
//      it stored. The server never drops a draft on its own (a direct save of the page leaves
//      it, stale): a stored draft that is gone was committed, or replaced or deleted by a
//      request naming its version, so what the server shows now succeeds it. A copy in the
//      browser alone does not count.
//  I6  after B deletes the page, the person's typed text is still in the browser (or
//      was already in a file version).

import { afterEach, beforeEach, describe, it, vi } from 'vitest'
import { ApiError } from '@/api/client'
import { createMockServer, type MockServer } from '@/api/mock/server'
import type { DraftReceipt, Page, PageDraft } from '@/api/types'
import type { DocJSON } from '@/markdown'
import { planRestore, type RestorePlan } from './draftRestore'
import { draftRefFor, draftStoreFor, loadDraft, type KeyValueStorage } from './draftStore'
import { SaveController, type ConflictInfo, type Draft, type DraftApi } from './saveController'

class MemStorage implements KeyValueStorage {
  data = new Map<string, string>()
  get length() {
    return this.data.size
  }
  key(i: number) {
    return [...this.data.keys()][i] ?? null
  }
  getItem(k: string) {
    return this.data.get(k) ?? null
  }
  setItem(k: string, v: string) {
    this.data.set(k, v)
  }
  removeItem(k: string) {
    this.data.delete(k)
  }
}

const doc = (text: string): DocJSON => ({ type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text }] }] })
const toMd = (d: DocJSON) => (d.content?.[0]?.content?.[0]?.text ?? '') + '\n'
const norm = (s: string) => s

function mulberry32(seed: number) {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

type Fault = 'before' | 'lose' | null

class Violation extends Error {}

// `copy` makes the second device mostly copy what the person once sent or gave up, and
// move the file around, which is what makes "ours" and "someone else's" look alike.
async function runSequence(seed: number, opts: { steps?: number; profile?: 'mixed' | 'copy' | 'offline' | 'giveup' | 'macro' | 'longoffline' | 'swept' | 'stores' } = {}): Promise<void> {
  const rnd = mulberry32(seed)
  const pick = <T,>(xs: T[]) => xs[Math.floor(rnd() * xs.length)]
  const log: string[] = []
  // A violation found inside a callback of the controller (where it would vanish as an
  // unhandled rejection) is kept and raised by the next check.
  let firstViolation: Violation | null = null
  const fail = (msg: string): never => {
    const v = new Violation(`${msg}\n  seed ${seed}\n  steps:\n    ${log.join('\n    ')}`)
    firstViolation ??= v
    throw v
  }

  let clock = Date.parse('2026-11-21T08:30:00Z')
  const server: MockServer = createMockServer({ now: () => clock, empty: true })
  const ID = (() => {
    const r = server.handle('POST', '/api/spaces/default/pages', JSON.stringify({ meta: { title: 'T' }, body: 'F0\n' }), 'application/json')
    return (r.body as Page).id
  })()
  const call = (method: string, path: string, body?: unknown) => {
    const r = server.handle(method, `/api/spaces/default/pages/${ID}${path}`, body === undefined ? undefined : JSON.stringify(body), method === 'GET' ? null : 'application/json')
    if (TRACE && method !== 'GET') log.push(`      ${method} ${path} ${JSON.stringify(body)?.slice(0, 120)} -> ${r.status}`)
    // Every text the file has had: going back to one, or finding it replaced, is not a loss of typed text.
    if (r.status === 200 && method !== 'GET' && typeof (r.body as Page | undefined)?.rev === 'string') fileTextsEver.add(((r.body as Page).body ?? '').replace(/\n$/, ''))
    // Every text the server has held as this person's draft, with an answer of 200.
    if (r.status === 200 && method === 'PUT' && path === '/draft') draftTextsEver.add(String((body as { body?: string }).body ?? '').replace(/\n$/, ''))
    return r
  }
  const fileTextsEver = new Set<string>(['F0'])
  const draftTextsEver = new Set<string>()
  let gone = false
  const getPage = () => call('GET', '').body as Page

  const storage = new MemStorage()
  const ref = draftRefFor('local', 'default', ID, storage)

  // --- the world beside the person ---
  let n = 0
  let bActed = false
  let bEpoch = 0
  let lastReqEpoch = 0
  const personSeen = new Set<string>()
  const ownerOfRev = new Map<string, 'A' | 'B'>([[getPage().rev, 'A']])
  const authorOfVersion = new Map<string, 'A' | 'B'>()
  const shownDrafts = new Set<string>() // versions of drafts the editor displayed
  // `${baseRev}|${body}` of every draft A sent or adopted as its own text (a draft it opened
  // on, or took in a conflict): the same text on the same base is A's, whoever stored it.
  const aSent = new Set<string>()
  const aTexts: string[] = [] // every text A typed, sent or given up (B copies from here)
  const revHistory: string[] = [getPage().rev]
  // The model's own reading of "ours" (the spec, not the controller's code): the
  // contents A has sent since the file rev it holds was adopted. Cleared when the
  // controller's page rev changes and at every conflict resolution.
  const refOurs = new Set<string>()
  let refRev = ''
  const keyOf = (meta: Record<string, unknown>, body: string) => {
    const { id: _i, created: _c, ...m } = meta
    void _i
    void _c
    return `${JSON.stringify(Object.entries(m).sort())}|${body}`
  }
  const syncRef = () => {
    if (open && controller.page.rev !== refRev) {
      // The controller adopted a new file rev. If that file is exactly something A sent
      // since the rev it held, adopting it is right (it is A's own text, committed by
      // the server); the rev is then A's to build on.
      if (refOurs.has(keyOf(controller.page.meta, controller.page.body))) personSeen.add(controller.page.rev)
      refOurs.clear()
      refRev = controller.page.rev
    }
  }
  // Texts the person has been shown anywhere (editor, either side of a dialog): replacing a file of
  // exactly such a text loses nothing they have not seen.
  const shownBodies = new Set<string>(['F0\n'])
  let maxOurs = 0
  let allowReplaceB = false
  let gaveUpBody = '' // the text the person gave up by taking the other side: a draft of exactly it is theirs to delete
  const keptBodies = new Set<string>() // texts the person chose to keep over a draft: a retry of that choice may replace a draft of it
  const resolvedDrafts = new Set<string>() // drafts shown in a dialog the person has answered (either way): a retry may replace or delete them
  let keptMine = false // the person chose to keep their text over a draft they were shown
  // A text A once typed, for B to copy: often an early one, which is what is stale after A adopts
  // a new rev or gives a text up.
  const copyText = () => (rnd() < 0.45 ? aTexts[Math.floor(rnd() * Math.min(3, aTexts.length))] : rnd() < 0.5 ? aTexts[aTexts.length - 1] : pick(aTexts))
  let fault: Fault = null
  let faultBeforeLeft = 0
  // A request held in flight: it reaches the server before or after the interleaved steps, and
  // its answer is delivered when released.
  let hold: { kind: 'commit' | 'put'; when: 'before' | 'after'; p: Promise<void> } | null = null
  let offlineAll = false

  // --- the person's editor ---
  let text = 'F0'
  let title = 'T'
  const history = ['F0']
  // True from the moment the person types (or undoes) until the server hands the
  // editor a text; I3 is about what the person typed, not what they were shown.
  let typed = false
  let open = false
  let controller!: SaveController
  let conflict: ConflictInfo | null = null
  let conflictRaised = false

  const net = () => new ApiError(0, 'network', '无法连接到服务器')
  const takeFault = (): Fault => {
    if (offlineAll) return 'before'
    if (faultBeforeLeft > 0) {
      faultBeforeLeft--
      return 'before'
    }
    const f = fault
    fault = null
    return f
  }
  // `ep` is the second device's epoch when the server executed the request (a held request is
  // answered later, but what it saw is what it saw then).
  const conv = <T,>(r: { status: number; body?: unknown }, ep: number = bEpoch): T => {
    lastReqEpoch = ep
    if (r.status === 204) return undefined as T
    const b = r.body as { error?: string; message?: string; current?: Page; draft?: PageDraft | null }
    if (r.status === 200 || r.status === 201) return r.body as T
    throw new ApiError(r.status, (b.error as never) ?? 'unknown', b.message ?? '', b.current, undefined, 'draft' in b ? b.draft : undefined)
  }

  // A file rev changed from `before` to `after`; whose content is it, and did A just replace B's?
  const fileChanged = (before: Page, after: Page, owner: 'A' | 'B', why: string) => {
    // Replacing B's file is silent only when the file's text is something A itself sent
    // since adopting the rev it holds (the server committed A's own draft, in B's name).
    if (ownerOfRev.get(before.rev) === 'B' && owner === 'A' && !personSeen.has(before.rev) && !shownBodies.has(before.body) && !refOurs.has(keyOf(before.meta, before.body))) {
      fail(`I2: the second device's file (${JSON.stringify(before.body)}, rev ${before.rev.slice(0, 6)}) was replaced by A's content ${JSON.stringify(after.body)} without the person having been shown it (${why})`)
    }
    ownerOfRev.set(after.rev, owner)
    if (revHistory[revHistory.length - 1] !== after.rev) revHistory.push(after.rev)
  }

  // I5: A is about to replace or delete the draft with version `v` (written by B)
  const touchesB = (v: string, what: string) => {
    if (!v || authorOfVersion.get(v) !== 'B') return
    const d = getPage().draft
    if (!d || d.version !== v) return
    if (allowReplaceB || aSent.has(`${d.baseRev}|${d.body}`) || d.body === gaveUpBody || keptBodies.has(d.body) || resolvedDrafts.has(d.version)) return
    fail(`I5: A ${what} the second device's draft ${JSON.stringify(d.body)} (base ${d.baseRev.slice(0, 6)}) without having chosen to keep its own text, and it is not something A sent`)
  }

  const gate = (): Fault => {
    if (gone) throw new ApiError(404, 'not_found', '找不到这个页面')
    return takeFault()
  }
  const api: DraftApi = {
    putDraft: async (_id, input) => {
      // Attempted counts, answered or not (the controller remembers it before sending).
      syncRef()
      refOurs.add(keyOf(input.meta, input.body))
      aSent.add(`${input.baseRev}|${input.body}`)
      const f = gate()
      if (f === 'before') throw net()
      const h = hold?.kind === 'put' ? hold : null
      if (h) hold = null
      if (h?.when === 'before') await h.p
      touchesB(input.prev, 'replaced')
      const r = call('PUT', '/draft', input)
      const ep = bEpoch
      if (r.status === 200) authorOfVersion.set((r.body as DraftReceipt).version, 'A')
      if (h?.when === 'after') await h.p
      if (f === 'lose') throw net()
      return conv<DraftReceipt>(r, ep)
    },
    deleteDraft: async (_id, version, rev) => {
      const f = gate()
      if (f === 'before') throw net()
      const r = call('DELETE', `/draft?version=${encodeURIComponent(version)}&rev=${encodeURIComponent(rev)}`)
      if (r.status === 204) touchesB(version, 'deleted')
      if (f === 'lose') throw net()
      return conv<void>(r)
    },
    commit: async (_id, version) => {
      const f = gate()
      if (f === 'before') throw net()
      const h = hold?.kind === 'commit' ? hold : null
      if (h) hold = null
      if (h?.when === 'before') await h.p
      const before = getPage()
      const r = call('POST', '/commit', { version })
      const ep = bEpoch
      if (r.status === 200) {
        const after = r.body as Page
        personSeen.add(after.rev) // A asked for this commit
        if (after.rev !== before.rev) fileChanged(before, after, authorOfVersion.get(version) ?? 'A', 'A committed')
      }
      if (h?.when === 'after') await h.p
      if (f === 'lose') throw net()
      return conv<Page>(r, ep)
    },
    getPage: async () => {
      const f = gate()
      if (TRACE) log.push(`      GET ${f ?? ''} rev ${getPage().rev.slice(0, 6)}`)
      if (f === 'before' || f === 'lose') throw net()
      return conv<Page>({ status: 200, body: getPage() })
    },
  }

  const bodyOf = (t: string) => t + '\n'
  const read = (): Draft => ({ title, subtitle: '', tags: [], body: bodyOf(text) })
  const readDraft = () => ({ fields: { title, subtitle: '', tags: [] as string[] }, bodyJson: doc(text) })
  const textOfBody = (b: string) => b.replace(/\n$/, '')
  const titleOf = (p: { meta: Record<string, unknown> }) => String(p.meta.title ?? '')

  // What the editor now shows, and which file version that text is.
  const showPlan = (plan: RestorePlan, page: Page) => {
    switch (plan.kind) {
      case 'file':
        text = textOfBody(page.body)
        title = titleOf(page)
        personSeen.add(page.rev)
        break
      case 'server':
        text = textOfBody(plan.content.markdown ?? page.body)
        title = plan.content.fields.title
        break
      case 'local':
      case 'conflict':
        text = plan.content.doc ? textOfBody(toMd(plan.content.doc)) : textOfBody(plan.content.markdown ?? page.body)
        title = plan.content.fields.title
        break
    }
    history.push(text)
    shownBodies.add(bodyOf(text))
    typed = false
  }

  const makeController = (page: Page, plan: RestorePlan): SaveController => {
    const { draft, ...file } = page
    const c = new SaveController({
      scope: 'default',
      page: file,
      draft,
      seed: plan.seed,
      draftToken: plan.kind === 'local' || plan.kind === 'conflict' || plan.kind === 'server' ? plan.token : undefined,
      read,
      api,
      normalizeBody: norm,
      drafts: draftStoreFor(ref, storage),
      readDraft,
      debounceMs: 1e9,
      draftDebounceMs: 1e9,
      onConflict: (info) => {
        if (TRACE) log.push(`      conflict raised: ${info.kind} mine ${JSON.stringify(info.mine.body)} theirs ${JSON.stringify(info.theirs.body)}${info.draft ? ' draft ' + JSON.stringify(info.draft.body) + ' base ' + info.draft.baseRev.slice(0, 6) : ''}${info.after ? ' after' : ''}`)
        conflict = info
        conflictRaised = true
        shownBodies.add(info.theirs.body)
        if (info.draft) shownBodies.add(info.draft.body)
        // The dialog shows the file's text on one side.
        if (info.kind === 'file') personSeen.add(info.theirs.rev)
        // ... and the draft's text on one side of a draft conflict.
        if (info.draft) shownDrafts.add(info.draft.version)
        // A draft conflict is raised only after the file passed the ownership check
        // (rule 2): a file that is A's own text may be built on when the person chooses.
        if (!gone) {
          const f = getPage()
          if (refOurs.has(keyOf(f.meta, f.body))) personSeen.add(f.rev)
        }
      },
      onExternalChange: (fresh) => {
        // I8
        if (novel(text) && !draftTextsEver.has(text) && fileBody() !== bodyOf(text) && draftBody() !== bodyOf(text)) fail(`I8: the editor's text ${text} exists nowhere on the server and the controller replaced it by a reset to file ${JSON.stringify(fresh.body)} (draft ${JSON.stringify(fresh.draft?.body)})`)
        const p = planRestore(fresh, null, toMd, norm)
        showPlan(p, fresh)
        // The controller was reset to this page: what it holds is what the editor shows.
        refRev = c.page.rev
        refOurs.clear()
        // The reset hands the controller whatever draft is there, as at open.
        if (fresh.draft) {
          shownDrafts.add(fresh.draft.version)
          if (p.kind === 'file') aSent.add(`${fresh.draft.baseRev}|${fresh.draft.body}`)
        }
        if (p.kind === 'server' && fresh.draft) {
          shownDrafts.add(fresh.draft.version)
          aSent.add(`${fresh.draft.baseRev}|${fresh.draft.body}`)
          personSeen.add(fresh.draft.baseRev)
        }
        if (p.kind === 'server' && fresh.draft) refOurs.add(keyOf(fresh.draft.meta, fresh.draft.body))
        if (p.kind === 'conflict') c.enterConflict(p.info, false)
      },
    })
    c.attach()
    switch (plan.kind) {
      case 'file':
      case 'server':
        if (plan.dropLocal) draftStoreFor(ref, storage).clear()
        break
      case 'local':
        c.markDirty()
        break
      case 'conflict':
        if (plan.dropLocal) draftStoreFor(ref, storage).clear()
        c.enterConflict(plan.info, plan.unsaved)
        break
    }
    return c
  }

  const settle = async () => {
    for (let i = 0; i < 30; i++) await Promise.resolve()
  }

  const openEditor = async () => {
    if (gone) {
      open = false
      return
    }
    const page = getPage()
    const plan = planRestore(page, loadDraft(ref, storage), toMd, norm)
    if (TRACE) log.push(`      open: file rev ${page.rev.slice(0, 6)} body ${JSON.stringify(page.body)} draft ${JSON.stringify(page.draft && { b: page.draft.body, base: page.draft.baseRev.slice(0, 6), stale: page.draft.stale })} local ${JSON.stringify(loadDraft(ref, storage) && { base: loadDraft(ref, storage)!.baseRev.slice(0, 6), sent: loadDraft(ref, storage)!.sent?.length })} -> ${plan.kind}${plan.kind === 'conflict' ? '/' + plan.info.kind : ''}`)
    // A file that is exactly something A sent is A's own text (the server committed it): A can build on it.
    if (refOurs.has(keyOf(page.meta, page.body))) personSeen.add(page.rev)
    // ... and so is a file equal to something A sent on the base the browser's copy is on (the
    // copy remembers what was sent), whoever stored it there.
    const lastLocal = loadDraft(ref, storage)
    if (lastLocal && aSent.has(`${lastLocal.baseRev}|${page.body}`)) personSeen.add(page.rev)
    // A draft that is there when the page opens is known to the controller from the start (it
    // needs its version to replace it); I7 is about drafts that appear later.
    if (page.draft) shownDrafts.add(page.draft.version)
    if (plan.kind === 'file' && page.draft) aSent.add(`${page.draft.baseRev}|${page.draft.body}`) // a leftover draft the controller holds
    if (plan.kind === 'server' && page.draft) {
      aSent.add(`${page.draft.baseRev}|${page.draft.body}`)
      // Continuing from a draft is continuing from the file it was based on: that file's
      // successor is what the person edits, so its rev is theirs to build on.
      personSeen.add(page.draft.baseRev)
    }
    conflict = null
    showPlan(plan, page)
    controller = makeController(page, plan)
    open = true
    refRev = controller.page.rev
    if (plan.kind === 'file' || plan.kind === 'conflict') refOurs.clear()
    if (plan.kind === 'server' && page.draft) {
      refOurs.clear()
      refOurs.add(keyOf(page.draft.meta, page.draft.body))
    }
    await settle()
  }

  // --- what each store holds, for the invariants ---
  const fileBody = () => (gone ? null : getPage().body)
  const draftBody = () => (gone ? null : (getPage().draft?.body ?? null))
  const localText = () => {
    const d = loadDraft(ref, storage)
    if (!d) return null
    return d.bodyJson ? textOfBody(toMd(d.bodyJson)) : null
  }
  const exists = (t: string) => fileBody() === bodyOf(t) || draftBody() === bodyOf(t) || localText() === t
  // File versions the controller itself has had as its page (see I3).
  const knownFileTexts = new Set<string>(['F0'])
  const known = () => {
    if (open) knownFileTexts.add(textOfBody(controller.page.body))
  }
  // I3 applies to what the person typed and that is not a file version the controller had.
  const novel = (t: string) => typed && !knownFileTexts.has(t) && !fileTextsEver.has(t)

  const check = (what: string) => {
    if (firstViolation) throw firstViolation
    if (gone) return
    const page = getPage()
    // I4
    if (!bActed && (conflictRaised || controller?.state === 'conflict')) fail(`I4: a conflict with no second device (${what})`)
    // The editor shows exactly the file the controller holds, with nothing unsaved: the
    // person has that version's text in front of them. (A rev is the hash of the file's
    // bytes, so the same text written again later, by anyone, is the same rev.)
    if (open && controller.state === 'saved' && !controller.hasUnsaved() && bodyOf(text) === controller.page.body) personSeen.add(controller.page.rev)
    // I7
    if (open) {
      const held = (controller as unknown as { serverDraft: { version: string; baseRev: string; body: string } | null }).serverDraft
      if (held && authorOfVersion.get(held.version) === 'B' && !shownDrafts.has(held.version) && !aSent.has(`${held.baseRev}|${held.body}`)) fail(`I7: the controller holds the second device's draft ${JSON.stringify(held.body)} (base ${held.baseRev.slice(0, 6)}) as its own, but the editor never displayed it and it is not something A sent (${what})`)
    }
    // The list of own drafts stays small: one store outstanding at most, reduced when observed.
    if (open) {
      const n = (controller as unknown as { ours: unknown[] }).ours.length
      maxOurs = Math.max(maxOurs, n)
      if (n > OURS_LIMIT) fail(`ours: the controller's list of own drafts is ${n} long (${what})`)
    }
    // I1
    if (open) {
      const st = controller.state
      const synced = !bActed || lastReqEpoch === bEpoch
      if (synced && st === 'saved' && page.body !== bodyOf(text) && draftBody() !== bodyOf(text)) fail(`I1: "saved" (internals ${JSON.stringify({ ours: (controller as any).ours.length, unc: (controller as any).uncertain, sd: (controller as any).serverDraft?.version, frev: controller.page.rev.slice(0, 6), unsaved: controller.hasUnsaved(), running: (controller as any).running, q: (controller as any).queued })}) but the server has file ${JSON.stringify(page.body)} draft ${JSON.stringify(draftBody())}, editor ${text}`)
      if (synced && st === 'drafted' && draftBody() !== bodyOf(text) && page.body !== bodyOf(text)) fail(`I1: "drafted" but the server has file ${JSON.stringify(page.body)} draft ${JSON.stringify(draftBody())}, editor ${text}`)
    }
    if (page.rev !== revHistory[revHistory.length - 1]) {
      // Written by the server's own commit (the sweeper) or by something not seen above: the author is the draft's.
      void 0
    }
  }

  const ops: Record<string, () => Promise<void>> = {
    type: async () => {
      if (!open) return
      text = `A${++n}`
      typed = true
      history.push(text)
      aTexts.push(text)
      controller.markDirty()
    },
    undo: async () => {
      if (!open) return
      const t = pick(history)
      if (t === text) return
      text = t
      typed = true
      controller.markDirty()
    },
    autosave: async () => {
      if (open) await controller.flush()
    },
    // Offline for a while: several different texts, each tried and failing, with
    // one store that landed unheard before them.
    burst: async () => {
      if (!open) return
      fault = 'lose'
      for (let i = 0; i < 9; i++) {
        text = `A${++n}`
        typed = true
        history.push(text)
        aTexts.push(text)
        controller.markDirty()
        await controller.flush()
        faultBeforeLeft = 1
      }
      faultBeforeLeft = 0
    },
    loseNext: async () => {
      fault = 'lose'
    },
    failNext: async () => {
      fault = 'before'
    },
    sweep: async () => {
      if (gone) return
      const before = getPage()
      clock += 180_000
      server.sweep()
      const after = getPage()
      fileTextsEver.add(after.body.replace(/\n$/, ''))
      if (after.rev !== before.rev) fileChanged(before, after, before.draft ? (authorOfVersion.get(before.draft.version) ?? 'A') : 'A', 'the server committed a draft')
    },
    bStore: async () => {
      if (gone) return
      const p = getPage()
      const t = `B${++n}`
      const r = call('PUT', '/draft', { prev: p.draft?.version ?? '', baseRev: p.rev, meta: p.meta, body: bodyOf(t) })
      if (r.status !== 200) return
      authorOfVersion.set((r.body as DraftReceipt).version, 'B')
      bActed = true
      bEpoch++
    },
    // A draft of text A once typed (sent or given up): on the current base, or on an older one.
    bCopyStore: async () => {
      if (gone || aTexts.length === 0) return
      const p = getPage()
      // On the file's rev, or on an older one (a stale copy of text A sent on a newer base).
      const older = revHistory.filter((r) => r !== p.rev)
      const base = rnd() < 0.5 || older.length === 0 ? p.rev : pick(older)
      const r = call('PUT', '/draft', { prev: p.draft?.version ?? '', baseRev: base, meta: p.meta, body: bodyOf(copyText()) })
      if (r.status !== 200) return
      authorOfVersion.set((r.body as DraftReceipt).version, 'B')
      bActed = true
      bEpoch++
    },
    bCommit: async () => {
      if (gone) return
      const p = getPage()
      if (!p.draft || authorOfVersion.get(p.draft.version) !== 'B' || p.draft.stale) return
      const r = call('POST', '/commit', { version: p.draft.version })
      if (r.status !== 200) return
      fileChanged(p, r.body as Page, 'B', 'B committed')
      bActed = true
      bEpoch++
    },
    // B stores a copy of text A once typed and commits it.
    bCopyCommit: async () => {
      if (gone || aTexts.length === 0) return
      const p = getPage()
      const r1 = call('PUT', '/draft', { prev: p.draft?.version ?? '', baseRev: p.rev, meta: p.meta, body: bodyOf(copyText()) })
      if (r1.status !== 200) return
      const v = (r1.body as DraftReceipt).version
      authorOfVersion.set(v, 'B')
      const r2 = call('POST', '/commit', { version: v })
      if (r2.status === 200) fileChanged(p, r2.body as Page, 'B', 'B committed a copy')
      bActed = true
      bEpoch++
    },
    bSave: async () => {
      if (gone) return
      const p = getPage()
      const t = `B${++n}`
      const r = call('PUT', '', { baseRev: p.rev, meta: p.meta, body: bodyOf(t) })
      if (r.status !== 200) return
      fileChanged(p, r.body as Page, 'B', 'B saved')
      bActed = true
      bEpoch++
    },
    // B saves with another title: the file is renamed.
    bRename: async () => {
      if (gone) return
      const p = getPage()
      const t = `B${++n}`
      const r = call('PUT', '', { baseRev: p.rev, meta: { ...p.meta, title: rnd() < 0.5 ? 'T2' : 'T' }, body: bodyOf(t) })
      if (r.status !== 200) return
      fileChanged(p, r.body as Page, 'B', 'B renamed')
      bActed = true
      bEpoch++
    },
    bDelete: async () => {
      if (gone) return
      const p = getPage()
      if (!p.draft || authorOfVersion.get(p.draft.version) !== 'B') return
      call('DELETE', `/draft?version=${encodeURIComponent(p.draft.version)}`)
      bActed = true
      bEpoch++
    },
    bDeletePage: async () => {
      if (gone) return
      call('DELETE', '')
      gone = true
      bActed = true
      bEpoch++
    },
    move: async () => {
      if (!open || gone) return
      await controller.commit()
      if (controller.state === 'conflict' || controller.state === 'error') return
      const r = call('POST', '/move', { folder: pick(['', 'x', 'y/z']) })
      if (r.status === 200) controller.adopt(r.body as Page)
    },
    saveVersion: async () => {
      if (open) await controller.commit()
    },
    // The other tab's notice: the controller looks at the server (and may reset itself).
    notice: async () => {
      if (!open || gone) return
      const p = getPage()
      if (rnd() < 0.5) controller.noticeExternalSave(p.rev)
      else controller.noticeExternalDraft(p.draft?.version ?? null)
      await settle()
    },
    // A request in flight while the person types and the second device acts; it reaches the
    // server before or after them.
    slow: async () => {
      if (!open || gone) return
      let release!: () => void
      const p = new Promise<void>((r) => (release = r))
      const kind = rnd() < 0.5 ? 'commit' : 'put'
      hold = { kind, when: rnd() < 0.5 ? 'before' : 'after', p }
      if (kind === 'put') await ops.type()
      const run = kind === 'commit' ? controller.commit() : controller.flush()
      await settle()
      const mid = ['type', 'bStore', 'bSave', 'bCopyStore', 'bCommit', 'bCopyCommit', 'sweep', 'notice', 'bSavePage']
      for (let i = Math.floor(rnd() * 3); i > 0; i--) await sub(pick(mid))
      release()
      hold = null
      await run
      await settle()
    },
    // A move after a commit, with typing and the second device acting before it lands.
    moveSlow: async () => {
      if (!open || gone) return
      await controller.commit()
      if (controller.state === 'conflict' || controller.state === 'error') return
      for (let i = Math.floor(rnd() * 3); i > 0; i--) await sub(pick(['type', 'bStore', 'bSave', 'bCopyStore', 'bSavePage']))
      if (gone) return
      const r = call('POST', '/move', { folder: pick(['', 'x', 'y/z']) })
      if (r.status === 200) controller.adopt(r.body as Page)
      await settle()
    },
    // B saves the page directly, from its own base (what a plain "save" does).
    bSavePage: async () => {
      if (gone) return
      const p = getPage()
      const r = call('PUT', '', { baseRev: p.rev, meta: p.meta, body: bodyOf(`B${++n}`) })
      if (r.status !== 200) return
      fileChanged(p, r.body as Page, 'B', 'B saved directly')
      bActed = true
      bEpoch++
    },
    // Offline for a very long stretch: thousands of pauses, each one a failed attempt.
    longOffline: async () => {
      if (!open) return
      fault = 'lose'
      offlineAll = false
      text = `A${++n}`
      typed = true
      history.push(text)
      aTexts.push(text)
      controller.markDirty()
      await controller.flush()
      offlineAll = true
      for (let i = 0; i < 2500; i++) {
        text = `A${++n}`
        typed = true
        controller.markDirty()
        await controller.flush()
      }
      history.push(text)
      aTexts.push(text)
      const ours = (controller as unknown as { ours: unknown[] }).ours.length
      if (ours > 3) fail(`offline stretch: the controller's list of own drafts grew to ${ours}`)
      offlineAll = false
      clock += 60_000
      controller.noticeOnline()
      await settle()
    },
    // Many answered stores in a row: the list of own drafts does not grow with them.
    manyStores: async () => {
      if (!open) return
      for (let i = 0; i < 6; i++) {
        await sub('type')
        await sub('autosave')
      }
    },
    // A stored text, more typing, the server commits the stored one, the page is reopened: the browser's
    // newer copy sits on an older rev than the file, which is the person's own earlier text.
    sweptDirty: async () => {
      if (!open) return
      await sub('type')
      await sub('autosave')
      await sub('type')
      await sub('sweep')
      // The tab closes with no network: only the browser's copy of the newer text survives.
      offlineAll = true
      await sub('reload')
      // Typed on, still offline, closed again: the copy now carries what the reopened editor believed.
      await sub('type')
      await sub('autosave')
      await sub('reload')
      offlineAll = false
      await sub('autosave')
      await sub('saveVersion')
    },
    // The person stores, the server commits it, the page is reopened, they type offline and reopen.
    sweptReopen: async () => {
      if (!open) return
      await sub('type')
      await sub('autosave')
      await sub('sweep')
      await sub('reload')
      await sub('type')
      await sub('failNext')
      await sub('autosave')
      await sub('reload')
      await sub('saveVersion')
    },
    // Offline typing with reloads in between, no second device.
    offlineReload: async () => {
      if (!open) return
      await sub('failNext')
      await sub('type')
      await sub('autosave')
      await sub('reload')
      await sub('type')
      await sub('failNext')
      await sub('autosave')
      await sub('reload')
      await sub('autosave')
    },
    // A lost store, the second device overwrites or commits, the person types and goes on.
    lostThenB: async () => {
      if (!open) return
      await sub('type')
      await sub('loseNext')
      await sub('autosave')
      await sub(pick(['bStore', 'bSavePage', 'bCopyCommit', 'sweep']))
      await sub('type')
      await sub('autosave')
      await sub('saveVersion')
    },
    retry: async () => {
      if (open) await controller.retry()
    },
    leave: async () => {
      if (!open) return
      const t = text
      await controller.detach()
      open = false
      // I3 after leaving
      const st = controller.state
      if (!novel(t)) return
      if (gone) {
        if (localText() !== t) fail(`I6: the page was deleted by the second device and the typed text ${t} is no longer in the browser`)
      } else if (st === 'error' || st === 'conflict') {
        if (!exists(t)) fail(`I3: after leaving in state ${st} the text ${t} is nowhere (file ${JSON.stringify(fileBody())}, draft ${JSON.stringify(draftBody())}, local ${localText()})`)
      } else if (fileBody() !== bodyOf(t) && draftBody() !== bodyOf(t)) {
        // Quiet, yet the server holds neither: only if the second device changed things after the text was stored.
        // A copy in the browser alone is accepted only if the second device acted after the
        // controller last heard from the server (it cannot have known).
        if (lastReqEpoch === bEpoch) fail(`I3: left quietly with nothing newer from the second device, but the server holds neither the file nor a draft equal to ${t} (local ${localText()})`)
        else if (!exists(t)) fail(`I3: left, the text ${t} is nowhere`)
      }
    },
    reload: async () => {
      const t = text
      const wasOpen = open
      if (open) {
        // A closed tab: the local draft is taken, the keepalive store goes out, nothing else.
        controller.persistNow()
        controller.flushKeepalive()
        await settle()
        open = false
      }
      await openEditor()
      // I3 across the reload: the earlier text is shown, or is in a conflict's two sides, or still in a store.
      if (wasOpen && novel(t) && text !== t && !exists(t) && !(conflict && JSON.stringify(conflict.mine).includes(t))) fail(`I3: the text ${t} was lost across a reload (editor now ${text}; file ${JSON.stringify(fileBody())} draft ${JSON.stringify(draftBody())} local ${localText()}; conflict ${JSON.stringify(conflict)})`)
    },
    resolve: async () => resolveWith(rnd() < 0.5 ? 'keep' : 'take'),
  }
  const resolveWith = async (mode: 'keep' | 'take') => {
    {
      if (!open || !conflict) return
      const info = conflict
      conflict = null
      if (info.draft) resolvedDrafts.add(info.draft.version)
      if (mode === 'keep') {
        log[log.length - 1] += ' (keep mine)'
        keptMine = true
        keptBodies.add(String(info.mine.body ?? ''))
        refOurs.clear()
        allowReplaceB = true
        await controller.keepMine()
        await controller.flush()
        allowReplaceB = false
      } else {
        log[log.length - 1] += ' (take theirs)'
        refOurs.clear()
        // After "keep mine" on a draft the person has seen, taking the file next gives that draft up too.
        if (keptMine) allowReplaceB = true
        gaveUpBody = String(info.mine.body ?? '')
        const shown = controller.useTheirs()
        text = textOfBody(shown.body)
        title = titleOf(shown)
        typed = false
        history.push(text)
        if (info.kind === 'file') personSeen.add(shown.rev)
        // Taking another draft makes its text the person's own: the server committing it is theirs.
        else {
          refOurs.add(keyOf(shown.meta, shown.body))
          if (info.draft) {
            shownDrafts.add(info.draft.version)
            aSent.add(`${info.draft.baseRev}|${info.draft.body}`)
          }
        }
        await controller.flush()
        allowReplaceB = false
      }
    }
  }

  // Macros: the few-step coincidences a random walk would take very long to find.
  const sub = async (name: string) => {
    log.push('  ' + name)
    await ops[name]()
    await settle()
  }
  // The person stores a text, the page changes under them, they take the other side, and the
  // second device then writes the text they gave up; the person types on.
  ops.giveUp = async () => {
    if (!open) return
    await sub('type')
    await sub('autosave')
    await sub('bSave')
    await sub('type')
    await sub('autosave')
    log.push('  resolve (take)')
    await resolveWith('take')
    await sub('bCopyCommit')
    await sub('type')
    await sub('autosave')
    await sub('saveVersion')
  }
  // The server commits the person's own draft, the person goes on (adopting that rev), and the
  // second device then writes the older text.
  ops.adoptThenCopy = async () => {
    if (!open) return
    await sub('type')
    await sub('autosave')
    await sub('sweep')
    await sub('type')
    await sub('autosave')
    await sub('bCopyCommit')
    await sub('type')
    await sub('autosave')
    await sub('saveVersion')
  }
  const weights: [string, number][] = [
    ['type', 6],
    ['undo', 3],
    ['autosave', 5],
    ['burst', 0.4],
    ['loseNext', 2],
    ['failNext', 1],
    ['sweep', 2],
    ['bStore', 1],
    ['bCopyStore', 1],
    ['bCommit', 1],
    ['bCopyCommit', 0.7],
    ['bSave', 1],
    ['bRename', 0.5],
    ['bDelete', 0.5],
    ['bDeletePage', 0.1],
    ['move', 0.7],
    ['saveVersion', 1.5],
    ['retry', 1],
    ['leave', 1],
    ['reload', 1.5],
    ['resolve', 2],
    ['notice', 1.5],
    ['slow', 1.2],
    ['moveSlow', 0.6],
    ['bSavePage', 0.7],
    ['sweptReopen', 0.3],
    ['sweptDirty', 0.3],
    ['manyStores', 0.3],
    ['offlineReload', 0.3],
    ['lostThenB', 0.4],
    ['giveUp', 0.3],
    ['adoptThenCopy', 0.3],
  ]
  if (opts.profile === 'copy') {
    const boost: Record<string, number> = { bCopyStore: 3, bCopyCommit: 4, bSave: 2, bStore: 2, bCommit: 2, sweep: 3, loseNext: 2, autosave: 6, type: 5, resolve: 4, bDeletePage: 0, bRename: 0.3, reload: 2, move: 1 }
    for (const w of weights) if (w[0] in boost) w[1] = boost[w[0]]
  }
  if (opts.profile === 'offline') {
    // Long offline stretches (several different texts failing) with reloads in between.
    const boost: Record<string, number> = { burst: 3, reload: 3, loseNext: 3, type: 4, autosave: 4, sweep: 1, bCopyStore: 1, bCopyCommit: 1, bDeletePage: 0 }
    for (const w of weights) if (w[0] in boost) w[1] = boost[w[0]]
  }
  if (opts.profile === 'giveup') {
    // The person stores a text, the page changes under them, they take the other side; then the
    // second device commits a copy of the text they gave up.
    for (const w of weights) w[1] = ({ type: 3, autosave: 3, bSave: 1, bCopyCommit: 1, resolve: 2, sweep: 1, loseNext: 1, saveVersion: 1, reload: 0.5, giveUp: 4, adoptThenCopy: 4 } as Record<string, number>)[w[0]] ?? 0
  }
  if (opts.profile === 'macro') {
    for (const w of weights) w[1] = ({ type: 2, autosave: 2, sweptReopen: 2, sweptDirty: 3, manyStores: 1, offlineReload: 2, lostThenB: 3, slow: 3, moveSlow: 2, notice: 2, bStore: 1, bSavePage: 1, bCopyCommit: 1, reload: 1, saveVersion: 1, sweep: 1, resolve: 1 } as Record<string, number>)[w[0]] ?? 0
  }
  if (opts.profile === 'swept') {
    for (const w of weights) w[1] = ({ sweptDirty: 4, sweptReopen: 3, offlineReload: 1, type: 1, autosave: 1, sweep: 1, reload: 1, saveVersion: 1 } as Record<string, number>)[w[0]] ?? 0
  }
  if (opts.profile === 'stores') {
    for (const w of weights) w[1] = ({ manyStores: 4, type: 1, autosave: 1, saveVersion: 1, sweep: 1, reload: 1, lostThenB: 1 } as Record<string, number>)[w[0]] ?? 0
  }
  if (opts.profile === 'longoffline') {
    for (const w of weights) w[1] = ({ type: 2, autosave: 2, longOffline: 3, reload: 1, sweep: 1, bStore: 0.3, saveVersion: 1 } as Record<string, number>)[w[0]] ?? 0
  }
  const total = weights.reduce((a, [, w]) => a + w, 0)
  const choose = () => {
    let x = rnd() * total
    for (const [name, w] of weights) if ((x -= w) < 0) return name
    return 'type'
  }

  const run = async () => {
    await openEditor()
    check('open')
    const steps = opts.steps ?? 8 + Math.floor(rnd() * 10)
    for (let i = 0; i < steps; i++) {
      const name = choose()
      log.push(name)
      await ops[name]()
      await settle()
      if (TRACE) log.push(`      -> ${open ? controller.state + (controller.state === 'error' ? ' (' + String(controller.getSnapshot().error?.message) + ')' : '') + ' text ' + text + ' ctrlRev ' + controller.page.rev.slice(0, 6) : 'closed'} server ${gone ? 'gone' : getPage().rev.slice(0, 6) + ' ' + JSON.stringify(getPage().body)}`)
      known()
      check(name)
    }
    // Quiescence: no faults, no open conflict, everything retried, the page left.
    fault = null
    faultBeforeLeft = 0
    log.push('-- quiesce')
    if (!open && !gone) await openEditor()
    if (gone) {
      if (open) {
        const t = text
        await controller.detach()
        open = false
        if (novel(t) && localText() !== t) fail(`I6: after the page was deleted the typed text ${t} is not in the browser`)
      }
      return
    }
    for (let i = 0; i < 4 && (conflict || controller.state === 'conflict'); i++) {
      if (!conflict) await openEditor() // a conflict that arose while detached: shown again
      else {
        conflict = null
        log.push('keepMine')
        refOurs.clear()
        allowReplaceB = true
        await controller.keepMine()
        await controller.flush()
        allowReplaceB = false
      }
    }
    const t = text
    await controller.retry()
    await controller.commit()
    await controller.detach()
    open = false
    await settle()
    check('quiesce')
    if (!novel(t)) return
    if (controller.state !== 'conflict' && controller.state !== 'error') {
      if (fileBody() !== bodyOf(t) && draftBody() !== bodyOf(t)) fail(`I3: after quiescence neither the file nor the server draft holds ${t} (file ${JSON.stringify(fileBody())}, draft ${JSON.stringify(draftBody())}, state ${controller.state})`)
    } else if (!exists(t)) fail(`I3: quiescent in ${controller.state}, text ${t} is nowhere`)
  }
  await run()
  if (process.env.MODEL_DUMP) fail('dump')
}

const TRACE = !!process.env.MODEL_TRACE
const OURS_LIMIT = Number(process.env.OURS_LIMIT ?? 4)

declare const process: { env: Record<string, string | undefined> }
const COUNT = Number(process.env.MODEL_COUNT ?? 5000)
const BASE = Number(process.env.MODEL_BASE ?? 1000)
const ONLY = process.env.MODEL_SEED ? Number(process.env.MODEL_SEED) : undefined

// The default run is about 8,300 sequences; MODEL_COUNT scales it (5000 is the default).
const SCALE = COUNT / 5000

describe('model-based: random sequences against the in-memory server', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it(`holds the invariants over ${ONLY === undefined ? Math.floor(2000 * SCALE) : 1} mixed sequences`, async () => {
    const seeds = ONLY === undefined ? Array.from({ length: Math.floor(2000 * SCALE) }, (_, i) => BASE + i) : [ONLY]
    for (const seed of seeds) await runSequence(seed, { steps: process.env.MODEL_STEPS ? Number(process.env.MODEL_STEPS) : undefined, profile: process.env.MODEL_PROFILE as 'copy' | 'offline' | undefined })
  }, 120_000)

  it('holds them in a world where the second device mostly copies what the person sent or gave up', async () => {
    if (ONLY !== undefined) return
    for (let seed = 800_000; seed < 800_000 + Math.floor(3500 * SCALE); seed++) await runSequence(seed, { steps: 24, profile: 'copy' })
  }, 120_000)

  it('holds them when the person gives a text up and the second device later writes it', async () => {
    if (ONLY !== undefined) return
    for (let seed = 700_000; seed < 700_000 + Math.floor(2000 * SCALE); seed++) await runSequence(seed, { steps: 16, profile: 'giveup' })
  }, 120_000)

  it('holds them through long offline stretches and reloads', async () => {
    if (ONLY !== undefined) return
    for (let seed = 900_000; seed < 900_000 + Math.floor(1500 * SCALE); seed++) await runSequence(seed, { steps: 20, profile: 'offline' })
  }, 120_000)

  it('holds them with slow requests, moves and notices interleaved with typing and the second device, and the swept/offline macros', async () => {
    if (ONLY !== undefined) return
    for (let seed = 600_000; seed < 600_000 + Math.floor(3500 * SCALE); seed++) await runSequence(seed, { steps: 14, profile: 'macro' })
  }, 240_000)

  it('holds them through a very long offline session (thousands of pauses) with no dialog and a small list of own drafts', async () => {
    if (ONLY !== undefined) return
    for (let seed = 400_000; seed < 400_000 + Math.floor(40 * SCALE); seed++) await runSequence(seed, { steps: 6, profile: 'longoffline' })
  }, 240_000)

  it('holds them when the server commits a stored text while newer text waits, across reopenings, with no second device or with one', async () => {
    if (ONLY !== undefined) return
    for (let seed = 300_000; seed < 300_000 + Math.floor(600 * SCALE); seed++) await runSequence(seed, { steps: 8, profile: 'swept' })
    for (let seed = 200_000; seed < 200_000 + Math.floor(300 * SCALE); seed++) await runSequence(seed, { steps: 6, profile: 'stores' })
  }, 240_000)

  it('holds them over long sequences too', async () => {
    if (ONLY !== undefined) return
    for (let seed = 500_000; seed < 500_000 + Math.floor(300 * SCALE); seed++) await runSequence(seed, { steps: 40 })
  }, 120_000)
})
