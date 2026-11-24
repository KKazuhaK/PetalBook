// The source view next to (or instead of) the editor: keeps the two in step.
//
// There is one piece of content (LiveDraft: header fields and body) and two
// ways to edit it. Typing in the source view makes that text the body, exactly
// as typed, and its header the fields; typing in the editor makes the document
// the body, and the source view is rewritten from it. Whichever side was typed
// in last is what a save sends.

import { useCallback, useEffect, useRef, useState, type RefObject } from 'react'
import type { Editor } from '@tiptap/react'
import { markdownToDoc } from '@/markdown'
import { replaceDoc } from './replaceDoc'
import type { LiveDraft } from '@/editor/liveDraft'
import { buildSave, othersOf, sameData, type Fields, type SaveController } from '@/editor/saveController'
import { composeFile, headerText, parseHeader, splitFile, type HeaderProblem } from './fileText'
import type { SourceHandle } from './SourceEditor'
import { blockLines, blockToLine, lineToBlock } from './scrollMap'

export type EditMode = 'rich' | 'source' | 'split'
const MODE_KEY = 'pb.editor.mode'

export function loadMode(): EditMode {
  try {
    const m = window.localStorage.getItem(MODE_KEY)
    return m === 'source' || m === 'split' ? m : 'rich'
  } catch {
    return 'rich'
  }
}
export function saveMode(mode: EditMode) {
  try {
    window.localStorage.setItem(MODE_KEY, mode)
  } catch {
    // Not remembered, then.
  }
}

// Converting a long body takes a moment (about half a second for 50,000
// characters), so the other side follows after a pause that grows with it.
const pauseFor = (length: number) => (length > 20000 ? 600 : length > 4000 ? 300 : 150)
// A header that does not parse is normal half way through typing a line; it is
// reported only when it stays that way.
const PROBLEM_DELAY_MS = 700

// Source text whose header cannot be read is not content the editor can hold
// or the server can store, so it is kept here, per page, until it reads again:
// a reload or a closed tab must not lose it.
const RAW_PREFIX = 'pb.source.'
// Text set aside because the page changed under it (see takeOldSource).
const OLD_PREFIX = 'pb.source.old.'
// `rev` is the file the text was typed on: text left on a page that has
// changed since is not taken up again without the person saying so.
export interface RawSource {
  rev: string
  text: string
}
export function loadRawSource(ref: string): RawSource | null {
  try {
    const d = JSON.parse(window.localStorage.getItem(RAW_PREFIX + ref) ?? 'null') as RawSource | null
    return d && typeof d.rev === 'string' && typeof d.text === 'string' ? d : null
  } catch {
    return null
  }
}
// Unfinished source text is never taken up again by itself: by the time the
// page is opened again, or when it is replaced under the open view, the page
// or its draft may have been changed elsewhere, and the text would go over
// that unasked. It is set aside in a list of its own, where nothing typed
// later can touch it, and the person is asked about each. Returns the list.
const OLD_MAX = 20
function readOld(ref: string): RawSource[] {
  try {
    const d = JSON.parse(window.localStorage.getItem(OLD_PREFIX + ref) ?? '[]') as unknown
    return Array.isArray(d)
      ? d.filter((x): x is RawSource => !!x && typeof x.rev === 'string' && typeof x.text === 'string')
      : []
  } catch {
    return []
  }
}
// Reports whether the list is stored as given (storage may be blocked or full).
function writeOld(ref: string, list: RawSource[]): boolean {
  try {
    if (list.length === 0) window.localStorage.removeItem(OLD_PREFIX + ref)
    else window.localStorage.setItem(OLD_PREFIX + ref, JSON.stringify(list.slice(-OLD_MAX)))
    return true
  } catch {
    return false
  }
}
export function takeOldSource(ref: string): RawSource[] {
  const raw = loadRawSource(ref)
  const list = readOld(ref)
  if (!raw) return list
  // Set aside first, and removed from its place only once that is stored: the
  // text is never in neither. When it cannot be stored it stays where it was,
  // and is still offered (from memory) so the person can decide about it now.
  if (!list.some((x) => x.text === raw.text)) list.push(raw)
  if (!writeOld(ref, list)) return list
  saveRawSource(ref, null)
  return readOld(ref)
}
// Sets this text aside, and says whether it is stored now. When it is not
// (storage blocked or full) nothing was changed, and the caller must leave the
// text where the person can still see it.
export function setAsideSource(ref: string, raw: RawSource): { stored: boolean; list: RawSource[] } {
  const list = readOld(ref)
  if (!list.some((x) => x.text === raw.text)) list.push(raw)
  if (!writeOld(ref, list) || !readOld(ref).some((x) => x.text === raw.text))
    return { stored: false, list: readOld(ref) }
  if (loadRawSource(ref)?.text === raw.text) saveRawSource(ref, null)
  return { stored: true, list: readOld(ref) }
}
// Removes one set-aside text (the person took it up again, or dropped it).
export function dropOldSource(ref: string, raw?: RawSource): RawSource[] {
  const list = raw ? readOld(ref).filter((x) => x.text !== raw.text) : []
  writeOld(ref, list)
  // One that could not be set aside is still in its first place.
  if (raw && loadRawSource(ref)?.text === raw.text) saveRawSource(ref, null)
  return list
}
export function saveRawSource(ref: string, raw: RawSource | null) {
  try {
    if (raw === null) window.localStorage.removeItem(RAW_PREFIX + ref)
    else window.localStorage.setItem(RAW_PREFIX + ref, JSON.stringify(raw))
  } catch {
    // Storage blocked or full: the text lives in the open view only.
  }
}

const scrollTo = (el: HTMLElement, y: number) => {
  el.scrollTop = y
}
const proportional = (from: HTMLElement, to: HTMLElement) => {
  const max = from.scrollHeight - from.clientHeight
  scrollTo(to, max > 0 ? (from.scrollTop / max) * (to.scrollHeight - to.clientHeight) : 0)
}

interface Deps {
  id: string
  mode: EditMode
  live: LiveDraft
  controller: SaveController
  editor: Editor | null
  setFields: (f: Fields) => void
  // The element that scrolls the editor side in split mode.
  richScroller: RefObject<HTMLElement | null>
  // The source view, while it is shown.
  source: RefObject<SourceHandle | null>
  // Whose page this is (the local draft's reference): where unreadable source text is kept.
  draftRef: string
}

export function useSourceView({ id, mode, live, controller, editor, setFields, richScroller, source, draftRef }: Deps) {
  const [problem, setProblem] = useState<HeaderProblem | null>(null)
  // True from the first keystroke that breaks the header (the banner waits a moment).
  const [broken, setBroken] = useState(false)
  // What is wrong right now, shown or not: switching away is refused at once.
  const problemNow = useRef<HeaderProblem | null>(null)
  const problemTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  // The body the source view holds, and the line of the file it starts on.
  const body = useRef<{ text: string; line: number } | null>(null)
  // The editor does not show the typed body yet.
  const richStale = useRef(false)
  const richTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const sourceTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  // The rewrite of the source view that is waiting for its pause to end.
  const pendingSource = useRef<{ run: (() => void) | null }>({ run: null })
  // A field was edited outside the source view: its header text is out of date.
  const headerStale = useRef(false)
  const adoptRaw = useRef<(text: string) => void>(() => {})
  const rawRev = useRef<string | null>(null)
  // The text this view last put under the page's key.
  const kept = useRef<string | null>(null)
  // Text left on an older file that the person asked to take up again.
  const takeStale = useRef<RawSource | null>(null)
  const modeRef = useRef(mode)
  useEffect(() => {
    modeRef.current = mode
  }, [mode])

  // `text` is the source text the problem is in; it is kept until it reads again.
  const report = useCallback(
    (p: HeaderProblem | null, text?: string) => {
      problemNow.current = p
      clearTimeout(problemTimer.current)
      setBroken(p !== null)
      if (p === null) {
        setProblem(null)
        // Only text this view itself kept: it must not remove what another tab
        // is in the middle of, or text that could not be set aside at open.
        if (kept.current !== null && loadRawSource(draftRef)?.text === kept.current) saveRawSource(draftRef, null)
        kept.current = null
        rawRev.current = null
      } else {
        // The rev of the first break stays: that is the file this text is about.
        rawRev.current ??= controller.page.rev
        // Written over this view's own earlier text only, never over other text
        // that is waiting there (the view's text is then on screen alone; it is
        // set aside when the page is left, see PageEditor).
        const there = loadRawSource(draftRef)
        if (text !== undefined && (there === null || there.text === kept.current)) {
          saveRawSource(draftRef, { rev: rawRev.current, text })
          kept.current = text
        }
        problemTimer.current = setTimeout(() => setProblem(p), PROBLEM_DELAY_MS)
      }
    },
    [controller, draftRef],
  )

  // The header as text for the fields as they stand: the file's own text while
  // nothing in it was changed, else written out from the data.
  const currentHeader = useCallback((): string => {
    const base = controller.page
    const save = buildSave(base, { ...live.fields, body: undefined }, () => base.body)
    return save ? headerText(save.meta) : (base.header ?? headerText(base.meta))
  }, [controller, live])

  const currentBody = useCallback((): string => live.read().body ?? controller.shownBody, [controller, live])

  // The whole file as text for what is being edited now.
  const currentText = useCallback((): string => {
    // Text the person asked to take up again: it comes back as it was, and is
    // read as if just typed.
    const raw = takeStale.current
    if (raw !== null) {
      // Handed on only once it has been read as typed text: a view that is
      // built twice (as React does in development) gets it both times.
      queueMicrotask(() => {
        adoptRaw.current(raw.text)
        if (takeStale.current === raw) takeStale.current = null
      })
      return raw.text
    }
    const b = currentBody()
    const h = currentHeader()
    body.current = { text: b, line: h.split('\n').length + 2 - (h.endsWith('\n') || h === '' ? 1 : 0) }
    headerStale.current = false
    return composeFile(h, b)
  }, [currentBody, currentHeader])

  // Put the typed body into the editor (which reports nothing back for it).
  const syncRich = useCallback(() => {
    clearTimeout(richTimer.current)
    if (!richStale.current || !editor || editor.isDestroyed || !body.current) return
    richStale.current = false
    replaceDoc(editor, markdownToDoc(body.current.text))
  }, [editor])

  // The person typed in the source view.
  const onSourceChange = useCallback(
    (text: string) => {
      const sp = splitFile(text)
      if (!sp.ok) {
        // Not a file with a header: nothing of it can be taken as content yet.
        report(sp.problem, text)
        return
      }
      if (body.current?.text !== sp.body) {
        live.setBodyText(sp.body)
        richStale.current = true
        clearTimeout(richTimer.current)
        // Also while the editor is hidden: something may still be on its way
        // into it (an upload), and must land in this text, not in an old one.
        richTimer.current = setTimeout(syncRich, pauseFor(sp.body.length))
      }
      body.current = { text: sp.body, line: sp.bodyLine }
      const h = parseHeader(sp.header, id)
      if (h.ok) {
        // Other keys that are the file's own are not "edited": see Fields.others.
        const { others, ...named } = h.fields
        const f: Fields = others && !sameData(others, othersOf(controller.page.meta)) ? h.fields : named
        setFields(live.setFields(f))
        headerStale.current = false
      }
      report(h.ok ? null : h.problem, text)
      controller.markDirty()
    },
    [controller, id, live, report, setFields, syncRich],
  )

  useEffect(() => {
    adoptRaw.current = onSourceChange
  }, [onSourceChange])

  // Something changed outside the source view (typing in the editor, a field
  // box, content replaced): rewrite the source text to match. `fields` says
  // the header changed too; otherwise the header text stays as it was typed.
  const noteRichChange = useCallback(
    (what: { fields?: boolean; now?: boolean; force?: boolean } = {}) => {
      if (what.fields) headerStale.current = true
      // The person chose other content (the other side of a conflict, an older
      // version): unfinished source text gives way to it.
      if (what.force && problemNow.current) report(null)
      // What the editor shows is the content again.
      richStale.current = false
      clearTimeout(richTimer.current)
      if (!source.current) return
      const run = () => {
        const s = source.current
        // Text whose header does not read is the person's to finish: it is never
        // rewritten from outside (the editor side is out of reach meanwhile).
        if (!s || problemNow.current) return
        const b = currentBody()
        const typed = splitFile(s.getText())
        const keepHeader = typed.ok && !headerStale.current
        const h = keepHeader ? typed.header : currentHeader()
        if (!keepHeader) headerStale.current = false
        const text = composeFile(h, b)
        const sp = splitFile(text)
        body.current = { text: b, line: sp.ok ? sp.bodyLine : 1 }
        s.setText(text)
      }
      clearTimeout(sourceTimer.current)
      const once = () => {
        pendingSource.current.run = null
        run()
      }
      if (what.now) once()
      else {
        pendingSource.current.run = once
        sourceTimer.current = setTimeout(once, pauseFor(body.current?.text.length ?? 0))
      }
    },
    [currentBody, currentHeader, report, source],
  )

  // The person is about to type on a side: whatever was still on its way to
  // that side arrives first, or their next keystroke would be made on old text
  // and take its place.
  const settle = useCallback(
    (side: 'source' | 'rich') => {
      if (side === 'rich') syncRich()
      else if (pendingSource.current.run) {
        clearTimeout(sourceTimer.current)
        pendingSource.current.run()
      }
    },
    [syncRich],
  )

  // Give up source text whose header does not read: back to the content as it stands.
  const discardBroken = useCallback(() => {
    report(null)
    noteRichChange({ fields: true, now: true })
  }, [noteRichChange, report])

  // An uploaded file has just been put into the editor (`markdown` is how it is
  // written). The source view takes it at once: the next keystroke there must
  // already be on text that has it. Text that does not read yet cannot be
  // rewritten, so there the line is added at its end.
  const landed = useCallback(
    (markdown: string) => {
      const s = source.current
      if (!s) return
      if (problemNow.current) {
        const text = `${s.getText().replace(/\n*$/, '')}\n\n${markdown}\n`
        s.setText(text)
        onSourceChange(text)
      } else if (pendingSource.current.run) {
        clearTimeout(sourceTimer.current)
        pendingSource.current.run()
      }
    },
    [onSourceChange, source],
  )

  const resumeStale = useCallback((raw: RawSource) => {
    takeStale.current = raw
  }, [])

  // The page was replaced by what the server has now (someone else saved it)
  // while the source text did not read. That text was about the file as it
  // was: it is set aside for the person to decide about (takeOldSource), and
  // the view shows the page as it is. Returns whether there was such text.
  const parkBroken = useCallback((): boolean => {
    if (!problemNow.current) return false
    problemNow.current = null
    rawRev.current = null
    kept.current = null
    clearTimeout(problemTimer.current)
    setProblem(null)
    setBroken(false)
    return true
  }, [])

  // The source view is gone: nothing is on its way to it any more.
  const forgetSource = useCallback(() => {
    body.current = null
    clearTimeout(sourceTimer.current)
    pendingSource.current.run = null
    problemNow.current = null
    clearTimeout(problemTimer.current)
  }, [])

  // Leaving the source view for the editor is refused while the header is
  // wrong: the editor would show fields that are not what the text says.
  const canLeaveSource = useCallback((): HeaderProblem | null => {
    if (problemNow.current) setProblem(problemNow.current)
    return problemNow.current
  }, [])

  // Entering a mode that shows the editor: make it show the typed body.
  useEffect(() => {
    if (mode !== 'source') syncRich()
    if (mode === 'rich') forgetSource()
  }, [mode, syncRich, forgetSource])

  useEffect(
    () => () => {
      clearTimeout(problemTimer.current)
      clearTimeout(richTimer.current)
      clearTimeout(sourceTimer.current)
    },
    [],
  )

  // --- scrolling the two sides together (split mode) ---------------------------
  const lines = useRef<{ text: string; lines: number[]; total: number } | null>(null)
  const linesOf = (text: string) => {
    if (lines.current?.text !== text) lines.current = { text, lines: blockLines(text), total: text.split('\n').length }
    return lines.current
  }
  // The side being moved by the other one, so its scroll event is not answered.
  const driven = useRef<{ side: 'source' | 'rich'; until: number } | null>(null)
  const isDriven = (side: 'source' | 'rich') =>
    driven.current?.side === side && performance.now() < driven.current.until
  const drive = (side: 'source' | 'rich') => {
    driven.current = { side, until: performance.now() + 120 }
  }
  // Where each block of the editor starts inside its scroller, and where the last one ends.
  const blockTops = (): number[] | null => {
    const sc = richScroller.current
    if (!sc || !editor || editor.isDestroyed) return null
    const base = sc.getBoundingClientRect().top - sc.scrollTop
    const kids = Array.from(editor.view.dom.children) as HTMLElement[]
    if (kids.length === 0) return null
    const tops = kids.map((k) => k.getBoundingClientRect().top - base)
    tops.push(kids[kids.length - 1].getBoundingClientRect().bottom - base)
    return tops
  }

  const onSourceScroll = useCallback(() => {
    const s = source.current
    const sc = richScroller.current
    const scroller = s?.scroller()
    if (
      modeRef.current !== 'split' ||
      !s ||
      !sc ||
      !scroller ||
      !body.current ||
      isDriven('source') ||
      richStale.current
    )
      return
    drive('rich')
    const info = linesOf(body.current.text)
    const tops = blockTops()
    if (!tops || tops.length !== info.lines.length + 1) return proportional(scroller, sc)
    const line = s.topLine()
    const first = body.current.line
    if (line < first) {
      scrollTo(sc, ((line - 1) / Math.max(1, first - 1)) * tops[0])
      return
    }
    const pos = lineToBlock(info.lines, line - first + 1, info.total)
    scrollTo(sc, tops[pos.index] + pos.frac * (tops[pos.index + 1] - tops[pos.index]))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editor, richScroller])

  const onRichScroll = useCallback(() => {
    const s = source.current
    const sc = richScroller.current
    const scroller = s?.scroller()
    if (modeRef.current !== 'split' || !s || !sc || !scroller || !body.current || isDriven('rich') || richStale.current)
      return
    drive('source')
    const info = linesOf(body.current.text)
    const tops = blockTops()
    if (!tops || tops.length !== info.lines.length + 1) return proportional(sc, scroller)
    const y = sc.scrollTop
    const first = body.current.line
    if (y < tops[0]) {
      s.scrollToLine(1 + (tops[0] > 0 ? y / tops[0] : 0) * (first - 1))
      return
    }
    let i = 0
    while (i + 2 < tops.length && tops[i + 1] <= y) i++
    const frac = tops[i + 1] > tops[i] ? Math.min(1, (y - tops[i]) / (tops[i + 1] - tops[i])) : 0
    s.scrollToLine(blockToLine(info.lines, { index: i, frac }, info.total) + first - 1)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editor, richScroller])

  const hasProblem = useCallback(() => problemNow.current !== null, [])
  // The source text as it stands while it does not read, with the file it is about.
  const brokenText = useCallback((): RawSource | null => {
    const s = source.current
    return problemNow.current && s ? { rev: rawRev.current ?? controller.page.rev, text: s.getText() } : null
  }, [controller, source])
  return {
    problem,
    broken,
    hasProblem,
    brokenText,
    discardBroken,
    landed,
    resumeStale,
    parkBroken,
    currentText,
    onSourceChange,
    noteRichChange,
    canLeaveSource,
    settle,
    onSourceScroll,
    onRichScroll,
  }
}
