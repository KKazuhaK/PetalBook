import { lazy, Suspense, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { Link, useBlocker, useNavigate, useParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { EditorContent, useEditor } from '@tiptap/react'
import { Code, Columns2, History, MoreHorizontal, Save, Trash2, FolderInput, Type } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError, isUnauthenticated, spaceApi, type SpaceApi } from '@/api/client'
import { invalidateListings, keys, useActiveSpace, usePage, useSession, useSpacePermissions, useTags, useTree, useUrls } from '@/api/hooks'
import type { Page, PageMeta } from '@/api/types'
import type { VersionContent } from '@/diff'
import { docToMarkdown, markdownToDoc } from '@/markdown'
import { planRestore, type Content } from '@/editor/draftRestore'
import { draftRefFor, draftStoreFor, loadDraft, parseSavedMarker, savedMarkerKey } from '@/editor/draftStore'
import { LateBound } from '@/editor/lateBound'
import { recoverDraft } from '@/editor/recoverDraft'
import { LiveDraft } from '@/editor/liveDraft'
import { buildSave, fieldsOf, othersOf, SaveController, waitForSaves, type ConflictInfo, type Fields } from '@/editor/saveController'
import type { SourceHandle } from '@/source/SourceEditor'
import { dropOldSource, loadMode, saveMode, saveRawSource, setAsideSource, takeOldSource, useSourceView, type EditMode, type RawSource } from '@/source/useSourceView'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { ErrorState, PageFallback } from '@/components/common'
import { setEditableQuietly } from '@/components/editor/editable'
import { Breadcrumbs } from '@/components/files/Breadcrumbs'
import { TopBar } from '@/components/layout/TopBar'
import { AutoTextarea } from '@/components/editor/AutoTextarea'
import { ImagePopover, LinkPopover, SelectionBubble } from '@/components/editor/BubbleToolbar'
import { ConflictDialog } from '@/components/editor/ConflictDialog'
import { EditorToolbar } from '@/components/editor/EditorToolbar'
import { TableControls } from '@/components/editor/table/TableControls'
import { MovePageDialog } from '@/components/editor/MovePageDialog'
import { createPageExtensions } from '@/components/editor/pageExtensions'
import { SaveIndicator } from '@/components/editor/SaveIndicator'
import { TagsEditor } from '@/components/editor/TagsEditor'
import { useMaxUpload } from '@/components/editor/useMaxUpload'
import { failureText } from '@/lib/failure'
import { useDocumentTitle } from '@/lib/useDocumentTitle'
import { getUploadOriginal } from '@/lib/uploadPrefs'
import { t } from '@/i18n'

// The history of a page (and the comparison code with it) loads when it is first
// opened: someone who only writes never pays for it.
const loadHistory = () => import('@/components/history/HistoryMode')
const HistoryMode = lazy(loadHistory)
// The source view brings a code editor with it; it loads when first shown.
const SourceEditor = lazy(() => import('@/source/SourceEditor'))
// The properties of a page's type: a chunk of its own, asked for only by a page that has a type.
const PropertyPanel = lazy(() => import('@/components/editor/PropertyPanel'))

// Side by side needs room: on a narrow screen that choice shows the editor alone.
const WIDE = '(min-width: 768px)'
const subscribeWide = (cb: () => void) => {
  const mq = window.matchMedia(WIDE)
  mq.addEventListener('change', cb)
  return () => mq.removeEventListener('change', cb)
}
const isWide = () => window.matchMedia(WIDE).matches

function ModeSwitch({ mode, wide, onChange }: { mode: EditMode; wide: boolean; onChange: (m: EditMode) => void }) {
  const item = (m: EditMode, label: string, Icon: typeof Type) => (
    <button
      type="button"
      aria-pressed={mode === m}
      aria-label={label}
      title={label}
      onClick={() => onChange(m)}
      className={`flex size-7 items-center justify-center rounded-md outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 ${mode === m ? 'bg-muted text-foreground' : 'text-muted-foreground hover:text-foreground'}`}
    >
      <Icon className="size-4" />
    </button>
  )
  return (
    <div role="group" aria-label={t.modes.label} className="flex items-center rounded-lg border p-0.5">
      {item('rich', t.modes.rich, Type)}
      {item('source', t.modes.source, Code)}
      {wide && item('split', t.modes.split, Columns2)}
    </div>
  )
}

export default function PageEditor() {
  const { id = '', space = '' } = useParams()
  const urls = useUrls()
  // Local drafts belong to a person: `local` in single-user mode.
  const userId = useSession().data?.user?.id ?? 'local'
  const { data, isPending, error, refetch } = usePage(space, id)
  const qc = useQueryClient()
  const { write } = useSpacePermissions()
  const { resolving } = useActiveSpace()
  // Counts the times `write` came on while this page was open: each starts a new editor session.
  const [gained, setGained] = useState(0)
  const wrote = useRef<boolean | null>(null)
  useEffect(() => {
    if (resolving) return
    const before = wrote.current
    wrote.current = write
    if (before !== false || !write) return
    // Gaining `write` while a page is open: the page is read again and the editor
    // opens afresh, so the draft this person has (here, on the server) is weighed
    // and offered as on any normal open. The viewer's session never looked at it,
    // and switching a live editor on must not look like typing.
    let live = true
    void qc.resetQueries({ queryKey: keys.page(space, id) }).then(() => live && setGained((n) => n + 1))
    return () => {
      live = false
    }
  }, [write, resolving, qc, space, id])

  if (isPending) {
    return (
      <>
        <TopBar />
        <PageFallback />
      </>
    )
  }
  if (error || !data) {
    const missing = error instanceof ApiError && error.code === 'not_found'
    // Whatever was typed here and never saved is kept: offer it back.
    if (missing) return <MissingPage space={space} id={id} userId={userId} />
    return (
      <>
        <TopBar />
        <div className="py-24 text-center">
          <ErrorState message={t.fmt.labelled(t.editor.loadFailed, error?.message ?? '')} onRetry={() => void refetch()} />
          <Link to={urls.recent()} className="text-sm text-brand underline underline-offset-4">
            {t.editor.backToList}
          </Link>
        </div>
      </>
    )
  }
  // One session per page id. It reads `data` once: later cache updates must
  // never reset what the person is typing.
  return <EditorSession key={`${userId}/${space}/${data.id}/${gained}`} space={space} userId={userId} initial={data} />
}

function MissingPage({ space, id, userId }: { space: string; id: string; userId: string }) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const urls = useUrls()
  const { write } = useSpacePermissions()
  const [ref] = useState(() => draftRefFor(userId, space, id))
  // Putting text back makes a page: only for someone who may.
  const [draft] = useState(() => (write ? loadDraft(ref) : null))
  const [busy, setBusy] = useState(false)
  const preview = (() => {
    if (draft?.bodyText !== undefined) return draft.bodyText.slice(0, 400)
    if (!draft?.bodyJson) return ''
    try {
      return docToMarkdown(draft.bodyJson).slice(0, 400)
    } catch {
      return ''
    }
  })()

  const recover = async () => {
    if (!draft) return
    setBusy(true)
    try {
      const page = await recoverDraft(ref, draft, { create: (input) => spaceApi(space).createPage(input), toMarkdown: docToMarkdown })
      void invalidateListings(qc, space)
      toast.success(t.editor.recovered)
      navigate(urls.page(page.id), { replace: true })
    } catch (e) {
      setBusy(false)
      toast.error(t.fmt.labelled(t.editor.recoverFailed, failureText(e)))
    }
  }

  return (
    <>
      <TopBar />
      <div className="mx-auto flex max-w-[560px] flex-col items-center gap-4 px-5 py-20 text-center">
        <p className="text-sm text-muted-foreground">{t.editor.notFound}</p>
        {draft && (
          <section className="w-full rounded-lg border p-4 text-left">
            <h2 className="text-sm font-medium">{t.editor.recoverTitle}</h2>
            <p className="mt-1 text-xs text-muted-foreground">{t.editor.recoverHelp}</p>
            {(draft.fields.title || preview) && (
              <div className="mt-3 rounded-md bg-muted p-2.5 text-xs leading-relaxed whitespace-pre-wrap break-words">
                {draft.fields.title && <div className="mb-1 font-medium">{draft.fields.title}</div>}
                {preview}
              </div>
            )}
            <Button className="mt-3" size="sm" disabled={busy} onClick={() => void recover()}>
              {busy ? t.editor.recovering : t.editor.recover}
            </Button>
          </section>
        )}
        <Link to={urls.recent()} className="text-sm text-brand underline underline-offset-4">
          {t.editor.backToList}
        </Link>
      </div>
    </>
  )
}

// Leaving the editor commits what was typed, which can take a moment. A slow
// one gets a notice that does not block anything (the list refreshes by itself
// when the file is written); one that fails says where the text is.
function watchLeave(done: Promise<void>, c: SaveController, id: string) {
  // Nothing was typed, or nothing is out: nothing to watch.
  if (c.state !== 'storing' && c.state !== 'committing') return
  const toastId = `leave-${id}`
  const slow = setTimeout(() => toast.loading(t.editor.leaving, { id: toastId }), 700)
  void done.finally(() => {
    clearTimeout(slow)
    toast.dismiss(toastId)
    const err = c.getSnapshot().error
    if (c.state === 'error') {
      // Text that never reached the server is in this browser; a draft that did
      // is the server's to write to the file.
      if (c.hasUnsaved()) toast.error(isUnauthenticated(err) ? t.editor.leaveSignedOut : t.editor.leaveFailed, { duration: 8000 })
      else if (!isUnauthenticated(err)) toast.warning(t.editor.leaveDrafted, { duration: 8000 })
    } else if (c.state === 'conflict') toast.warning(t.editor.leftInConflict, { duration: 8000 })
  })
}

function EditorSession({ space, userId, initial }: { space: string; userId: string; initial: Page }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const urls = useUrls()
  const client: SpaceApi = useMemo(() => spaceApi(space), [space])
  const tree = useTree()
  const tags = useTags()
  // Without `write` the page is shown and nothing about it can be changed: no
  // typing, no draft, no move or delete, no going back to a version.
  const { write } = useSpacePermissions()
  const readOnly = !write
  const readOnlyRef = useRef(readOnly)
  useEffect(() => {
    readOnlyRef.current = readOnly
  })

  // The page file, without the draft that came with it.
  const [file] = useState<Page>(() => {
    const { draft: _draft, ...rest } = initial
    void _draft
    return rest
  })
  // What the browser kept from an earlier visit (closed tab, failed request,
  // signed out) and what the server holds as a draft are both weighed against
  // the file before anything is shown: by content and lineage, not by clocks.
  const [ref] = useState(() => draftRefFor(userId, space, initial.id))
  const [drafts] = useState(() => draftStoreFor(ref))
  // What this browser kept is the text of someone who could write; it is not offered back.
  const [local] = useState(() => (readOnly ? null : loadDraft(ref)))
  const [restore] = useState(() => planRestore(readOnly ? file : initial, local, docToMarkdown, (md) => docToMarkdown(markdownToDoc(md))))
  const content: Content | null = restore.kind === 'file' ? null : restore.content
  // A local copy from before the source field existed carries none: the box then shows
  // what the server has, so the person never types over a value they were not shown.
  const [fields, setFields] = useState<Fields>(() => (content ? { ...content.fields, source: content.fields.source ?? fieldsOf((initial.draft ?? initial).meta).source } : fieldsOf(initial.meta)))
  // What has been typed, readable when a request runs (see LiveDraft).
  const [live] = useState(() => new LiveDraft(fields, docToMarkdown))
  const [conflict, setConflict] = useState<ConflictInfo | null>(null)
  const [conflictOpen, setConflictOpen] = useState(false)
  const [moveOpen, setMoveOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [historyOpen, setHistoryOpen] = useState(false)
  const subtitleRef = useRef<HTMLTextAreaElement>(null)

  // Computed once: a brand-new blank page puts the cursor in the body.
  const [startInBody] = useState(() => !fields.title && !content?.doc && (content?.markdown ?? initial.body).trim() === '')
  const [initialDoc] = useState(() => content?.doc ?? markdownToDoc(content?.markdown ?? initial.body))

  // Called when another tab or device changed the server's copy and this one has
  // nothing unsaved.
  const [reload] = useState(() => new LateBound<[Page]>())
  const [controller] = useState(
    () =>
      new SaveController({
        scope: space,
        page: file,
        draft: initial.draft,
        seed: restore.seed,
        draftToken: restore.kind === 'local' || restore.kind === 'conflict' || restore.kind === 'server' ? restore.token : undefined,
        onExternalChange: reload.call,
        read: live.read,
        api: {
          putDraft: (id, input, opts) => client.putDraft(id, input, opts),
          deleteDraft: (id, version, rev) => client.deleteDraft(id, version, rev),
          commit: (id, version) => client.commitPage(id, version),
          getPage: (id) => client.getPage(id),
        },
        // What the editor would write for the original text, so an edit that
        // is undone again is recognised as no change at all.
        normalizeBody: (md) => docToMarkdown(markdownToDoc(md)),
        drafts,
        readDraft: live.readStorable,
        // The lists show the file, so they change only when the file does. This
        // also runs when the commit lands after the editor was left.
        onCommitted: () => void invalidateListings(qc, space),
        onConflict: (info) => {
          setConflict(info)
          setConflictOpen(true)
        },
      }),
  )
  const snapshot = useSyncExternalStore(controller.subscribe, controller.getSnapshot)

  // Declared before useEditor on purpose: React runs cleanups in declaration
  // order, so the unsaved content is captured while the editor still exists.
  useEffect(() => {
    controller.attach()
    const onHidden = () => {
      if (document.visibilityState === 'hidden') controller.flushKeepalive()
      else controller.noticeVisible() // back in view: retry a failed step, or see what changed elsewhere
    }
    const onPageHide = () => controller.flushKeepalive()
    const onBeforeUnload = (e: BeforeUnloadEvent) => {
      controller.persistNow()
      // Text the server has not confirmed (typed, failed, in conflict) asks
      // first. The local draft keeps it either way; a draft the server holds is
      // committed by the server itself, so closing is fine.
      if (controller.hasUnsaved() || controller.state === 'error' || controller.state === 'conflict') {
        e.preventDefault()
        e.returnValue = ''
      }
    }
    const onOnline = () => controller.noticeOnline()
    // Save a version, as the browser's own "save page" shortcut would suggest.
    const onKeyDown = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === 's') {
        e.preventDefault()
        if (!readOnlyRef.current) void controller.commit()
      }
    }
    document.addEventListener('visibilitychange', onHidden)
    window.addEventListener('pagehide', onPageHide)
    window.addEventListener('beforeunload', onBeforeUnload)
    window.addEventListener('online', onOnline)
    window.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('visibilitychange', onHidden)
      window.removeEventListener('pagehide', onPageHide)
      window.removeEventListener('beforeunload', onBeforeUnload)
      window.removeEventListener('online', onOnline)
      window.removeEventListener('keydown', onKeyDown)
      watchLeave(controller.detach(), controller, initial.id)
    }
  }, [controller, initial.id])

  // Once, on open: show the draft that was found, or raise the conflict it implies.
  useEffect(() => {
    switch (restore.kind) {
      case 'file':
        if (restore.dropLocal && local) drafts.clearIf(local.token)
        return
      case 'server':
        // Already on the server; the editor just shows it.
        if (restore.dropLocal && local) drafts.clearIf(local.token)
        live.bodyChanged()
        return
      case 'local':
        // Only in this browser: it goes to the server next. A body typed as
        // Markdown stays that text; one edited in the editor is its document.
        if (restore.content.typed) live.setBodyText(restore.content.markdown ?? '')
        else live.bodyChanged()
        controller.markDirty()
        return
      case 'conflict':
        if (restore.content.typed) live.setBodyText(restore.content.markdown ?? '')
        else live.bodyChanged()
        if (restore.dropLocal && local) drafts.clearIf(local.token)
        controller.enterConflict(restore.info, restore.unsaved)
    }
  }, [restore, local, drafts, live, controller])

  // Tells the source view (when it is open) that something changed outside it.
  const noteRich = useRef<(what?: { fields?: boolean; force?: boolean }) => void>(() => {})
  // Brings the editor up to date with text typed in the source view.
  const [settleRich] = useState(() => new LateBound<[]>())
  const [uploadLanded] = useState(() => new LateBound<[string]>())
  // The page's own extensions (see pageExtensions.ts). A page that is open always
  // exists on the server, so it always has an id to upload files under.
  const getMaxUpload = useMaxUpload()
  const [extensions] = useState(() =>
    createPageExtensions({
      space,
      pageId: initial.id,
      getFolder: () => controller.page.folder,
      getMaxUpload,
      upload: (file, name) => client.uploadAsset(initial.id, file, name),
      getOriginal: getUploadOriginal,
      notify: (message) => void toast.error(message),
      beforeInsert: settleRich.call,
      afterInsert: uploadLanded.call,
    }),
  )

  const editor = useEditor({
    extensions,
    content: initialDoc,
    editable: !readOnly,
    autofocus: startInBody && !readOnly ? 'start' : false,
    editorProps: { attributes: { class: 'pb-prose', 'aria-label': t.editor.bodyLabel } },
    onUpdate: ({ editor: ed }) => {
      // Always the editor that is live: in development React creates and
      // discards a first one, and that one must never be read from.
      live.attachEditor(ed)
      live.bodyChanged()
      controller.markDirty()
      noteRich.current()
    },
  })

  useEffect(() => {
    if (editor) live.attachEditor(editor)
  }, [editor, live])
  // The role can change while the page is open (the list of spaces is read again).
  useEffect(() => {
    if (editor) setEditableQuietly(editor, !readOnly)
  }, [editor, readOnly])

  // Leaving with a conflict open, or with text that never reached the server,
  // asks first. (Signed out: the sign-in page takes over, and the browser keeps the text.)
  // Source text whose header does not read, if any (set once the source view exists).
  const brokenNow = useRef<() => RawSource | null>(() => null)
  // The last attempt to leave found such text and could not store it.
  const lostOnLeave = useRef(false)
  const blocker = useBlocker(() => {
    // Unfinished source text is set aside on the way out, and offered again
    // when the page is next opened. Only if the browser cannot keep it does
    // leaving ask first: it would be gone.
    const text = brokenNow.current()
    lostOnLeave.current = text !== null && !setAsideSource(ref, text).stored
    const s = controller.getSnapshot()
    return lostOnLeave.current || s.state === 'conflict' || (s.state === 'error' && !isUnauthenticated(s.error))
  })
  useEffect(() => {
    if (blocker.state !== 'blocked') return
    const message = lostOnLeave.current ? t.modes.leaveBroken : controller.state === 'conflict' ? t.editor.leaveConflict : t.editor.leaveUnsaved
    if (window.confirm(message)) blocker.proceed()
    else blocker.reset()
  }, [blocker, controller])

  const page = controller.page
  useDocumentTitle(`${fields.title.trim() || t.editor.untitled} · ${t.appName}`)

  const setField = (key: 'title' | 'subtitle' | 'source', value: string) => {
    setFields(live.setField(key, value))
    controller.markDirty()
    noteRich.current({ fields: true })
  }
  const setTags = (next: string[]) => {
    setFields(live.setTags(next))
    controller.markDirty()
    noteRich.current({ fields: true })
  }

  // The properties of the page's type live in the same draft as the title and the
  // tags: `others` holds every header key that is not a named field, and is the file's
  // until the person changes one.
  const setProps = (next: PageMeta) => {
    setFields(live.setOthers(next))
    controller.markDirty()
    noteRich.current({ fields: true })
  }
  const typeName = page.effectiveType ?? ''
  const propValues = fields.others ?? othersOf(page.meta)

  // --- the three ways to edit (docs/design.md, 7.0) ---
  const wide = useSyncExternalStore(subscribeWide, isWide)
  // Source text left with a broken header last time is shown again where it was typed.
  // If the page has changed since, the person is asked first (see the notice below).
  const [rawLeft, setRawLeft] = useState(() => takeOldSource(ref))
  const [chosenMode, setChosenMode] = useState<EditMode>(loadMode)
  const richScroller = useRef<HTMLDivElement>(null)
  const sourceRef = useRef<SourceHandle>(null)
  const [narrowTo, setNarrowTo] = useState<EditMode>('rich')
  // Side by side on a screen too narrow for it shows one side: the editor,
  // unless the source text has a problem that only the source view can fix.
  const mode: EditMode = chosenMode === 'split' && !wide ? narrowTo : chosenMode
  const sv = useSourceView({ id: initial.id, mode, live, controller, editor, setFields, richScroller, source: sourceRef, draftRef: ref })
  if (narrowTo !== (sv.broken ? 'source' : 'rich') && (sv.broken || mode !== 'source')) setNarrowTo(sv.broken ? 'source' : 'rich')
  const { noteRichChange, settle, hasProblem, landed, brokenText } = sv
  useEffect(() => {
    brokenNow.current = brokenText
  }, [brokenText])
  useEffect(() => {
    noteRich.current = noteRichChange
    settleRich.bind(() => settle('rich'))
    uploadLanded.bind(landed)
    return () => {
      settleRich.bind(null)
      uploadLanded.bind(null)
    }
  }, [noteRichChange, settle, landed, settleRich, uploadLanded])
  // Source text left unfinished on an older file of this page: take it up
  // again (in the source view, where it can be finished), or let it go.
  const [sourceKey, setSourceKey] = useState(0)
  // Source text in the view that does not read is set aside (stored, then the
  // view let go of it). False when the browser could not store it: the view
  // then keeps it, and must not be rewritten.
  const setAsideBroken = (): boolean => {
    const text = sv.brokenText()
    if (!text) return !sv.hasProblem()
    const { stored, list } = setAsideSource(ref, text)
    if (!stored) return false
    sv.parkBroken()
    setRawLeft(list)
    return true
  }
  const staleRaw = rawLeft.length > 0
  const resumeRaw = () => {
    const raw = rawLeft[0]
    if (!raw) return
    // Broken text in the view now is set aside in turn, not replaced; if the
    // browser cannot keep it, it stays in the view and nothing is taken up.
    if (!setAsideBroken()) {
      toast.error(t.modes.cannotSetAside)
      return
    }
    sv.resumeStale(raw)
    saveMode('source')
    setRawLeft((dropOldSource(ref, raw), takeOldSource(ref)))
    setSourceKey((k) => k + 1)
    setChosenMode('source')
  }
  const dropRaw = () => {
    if (rawLeft[0]) setRawLeft(dropOldSource(ref, rawLeft[0]))
  }
  // Source text that does not read as a page is in no draft and on no server:
  // closing the tab asks first (the browser keeps the text for next time).
  useEffect(() => {
    const onBeforeUnload = (e: BeforeUnloadEvent) => {
      if (!hasProblem()) return
      e.preventDefault()
      e.returnValue = ''
    }
    window.addEventListener('beforeunload', onBeforeUnload)
    return () => window.removeEventListener('beforeunload', onBeforeUnload)
  }, [hasProblem])
  // A draft restored on open is put into the live content by an effect above;
  // a source view built before that is brought up to date here.
  useEffect(() => {
    if (restore.kind !== 'file') sv.noteRichChange({ fields: true })
    // Once, on open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  const changeMode = (next: EditMode) => {
    if (next === mode) return
    if (next === 'rich' && mode !== 'rich' && sv.canLeaveSource()) {
      toast.error(t.modes.cannotLeave)
      return
    }
    setChosenMode(next)
    saveMode(next)
  }
  // The body as it stands, whichever side it was last typed on.
  const bodyNow = () => live.read().body ?? controller.shownBody

  // Put these fields and this Markdown on screen, replacing what is there.
  // `chosen`: the person picked this content themself, so unfinished source text gives way to it.
  const showContent = (f: Fields, markdown: string, chosen = false) => {
    if (!editor) return
    live.reset(f)
    setFields(f)
    editor.commands.setContent(markdownToDoc(markdown), { emitUpdate: false })
    // After the caller has finished (it may still mark the body as edited).
    noteRich.current({ fields: true, force: chosen })
  }

  // The dialog is closed and cleared first: the controller may raise the next
  // question (both drafts stale, the file still to weigh) straight away.
  const keepMine = () => {
    setConflictOpen(false)
    setConflict(null)
    void controller.keepMine()
  }
  const useTheirs = () => {
    setConflictOpen(false)
    setConflict(null)
    const shown = controller.useTheirs()
    showContent(fieldsOf(shown.meta, controller.page.meta), shown.body, true)
  }

  // Another tab or device changed what the server has, and this one has nothing
  // unsaved: show it. (With something unsaved the controller stores now and the
  // person meets the conflict dialog instead.)
  useEffect(() => {
    reload.bind((fresh) => {
      // Source text that does not read was about the page as it was: it is set
      // aside, the page is shown as it is now, and the person is asked.
      // (When the browser cannot store it, the view keeps it and says so.)
      const had = sv.hasProblem()
      const parked = had && setAsideBroken()
      if (had && !parked) toast.warning(t.modes.changedUnderBroken, { duration: 12_000 })
      const plan = planRestore(fresh, null, docToMarkdown, (md) => docToMarkdown(markdownToDoc(md)))
      if (plan.kind === 'file') showContent(fieldsOf(fresh.meta), fresh.body)
      else {
        showContent(plan.content.fields, plan.content.markdown ?? fresh.body)
        live.bodyChanged()
        if (plan.kind === 'conflict') controller.enterConflict(plan.info, false)
      }
      // At once, not after a pause: a keystroke in between would be on the old text.
      if (parked) sv.noteRichChange({ fields: true, now: true })
    })
    const key = savedMarkerKey(ref)
    const onStorage = (e: StorageEvent) => {
      if (e.key !== key) return
      const marker = parseSavedMarker(e.newValue)
      if (!marker) return
      if (marker.rev !== controller.page.rev) controller.noticeExternalSave(marker.rev)
      else controller.noticeExternalDraft(marker.draft)
    }
    window.addEventListener('storage', onStorage)
    return () => {
      window.removeEventListener('storage', onStorage)
      reload.bind(null)
    }
    // showContent closes over the editor, which exists once this runs.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [controller, ref, reload, editor])

  const move = async (folder: string) => {
    // What is typed becomes the file first; then the file moves as it is.
    await controller.commit()
    if (controller.state === 'conflict' || controller.state === 'error') {
      toast.error(t.editor.moveFailed)
      throw new Error('unsaved')
    }
    try {
      const moved = await client.movePage(page.id, folder)
      controller.adopt(moved)
      void invalidateListings(qc, space)
      toast.success(t.editor.moved(folder))
    } catch (e) {
      toast.error(t.fmt.labelled(t.editor.moveFailed, failureText(e)))
      throw e
    }
  }

  const remove = async () => {
    controller.suspend()
    try {
      await waitForSaves(space, page.id)
      await client.deletePage(page.id)
    } catch (e) {
      controller.attach()
      if (controller.hasUnsaved()) controller.markDirty()
      toast.error(t.fmt.labelled(t.editor.deleteFailed, failureText(e)))
      return
    }
    controller.discard()
    saveRawSource(ref, null)
    dropOldSource(ref)
    qc.removeQueries({ queryKey: keys.page(space, page.id) })
    void invalidateListings(qc, space)
    toast.success(t.editor.deleted)
    navigate(urls.folder(page.folder), { replace: true })
  }

  // --- history ---
  // The content as the editor holds it now, in the shape of a version: the
  // file's header with the fields as typed, and the body as Markdown.
  const currentContent = (): VersionContent => {
    const f = live.fields
    // Header keys written in the source view are part of it.
    const base = controller.page
    const meta: Record<string, unknown> = { ...(buildSave(base, { ...f, body: undefined }, () => base.body)?.meta ?? base.meta) }
    for (const k of ['title', 'subtitle', 'source'] as const) {
      const v = f[k] ?? ''
      if (v === '') delete meta[k]
      else meta[k] = v
    }
    if (f.tags.length) meta.tags = f.tags
    else delete meta.tags
    return { meta, body: bodyNow() }
  }

  // Going back to a version is an ordinary edit: its header fields and body are
  // put in the editor as if they had been typed, and go the usual way (draft,
  // then a new version). Nothing here talks to the server except the optional
  // save of what is there first.
  const restoreVersion = async (v: VersionContent, saveFirst: boolean, whenText: string): Promise<boolean> => {
    if (!editor) return false
    if (saveFirst) {
      await controller.commit()
      if (controller.state === 'conflict' || controller.state === 'error') {
        toast.error(t.history.restoreSaveFailed)
        return false
      }
    }
    const before = { fields: live.fields, markdown: bodyNow() }
    const put = (f: Fields, markdown: string) => {
      showContent(f, markdown, true)
      live.bodyChanged()
      controller.markDirty()
    }
    put(fieldsOf(v.meta, controller.page.meta), v.body)
    setHistoryOpen(false)
    // Undo puts back what was there, but only while the restored text is still
    // what is on screen: once the person has typed on, or left the page, there is
    // nothing this button may replace.
    const restored = { fields: live.fields, markdown: bodyNow() }
    const sameTags = (a: string[], b: string[]) => a.length === b.length && a.every((x, i) => x === b[i])
    toast(t.history.restored(whenText), {
      duration: 12_000,
      action: {
        label: t.history.undoRestore,
        onClick: () => {
          const f = live.fields
          const untouched =
            !editor.isDestroyed &&
            f.title === restored.fields.title &&
            f.subtitle === restored.fields.subtitle &&
            (f.source ?? '') === (restored.fields.source ?? '') &&
            sameTags(f.tags, restored.fields.tags) &&
            f.others === restored.fields.others &&
            bodyNow() === restored.markdown
          if (!untouched) {
            toast.info(t.history.undoTooLate)
            return
          }
          put(before.fields, before.markdown)
          toast.success(t.history.undone)
        },
      },
    })
    return true
  }

  // "Save version" has something to do when there is text or a draft that is
  // not the file yet, and the request is not already out.
  const canSave = snapshot.state === 'dirty' || snapshot.state === 'drafted' || (snapshot.state === 'error' && !isUnauthenticated(snapshot.error))

  return (
    <>
      <TopBar
        actions={
          <>
            {readOnly ? (
              <span className="rounded-md border px-1.5 py-0.5 text-xs text-muted-foreground" title={t.permissions.readOnlyNote}>
                {t.permissions.readOnly}
              </span>
            ) : (
              <SaveIndicator snapshot={snapshot} draftSafe={!controller.hasUnsaved()} onRetry={() => void controller.retry()} onOpenConflict={() => setConflictOpen(true)} />
            )}
            {!historyOpen && <ModeSwitch mode={mode} wide={wide} onChange={changeMode} />}
            {!historyOpen && (
              <Button variant="ghost" size="sm" onClick={() => setHistoryOpen(true)} onPointerEnter={() => void loadHistory()} onFocus={() => void loadHistory()} title={t.history.openHint} aria-label={t.history.open}>
                <History />
                <span className="hidden sm:inline">{t.history.open}</span>
              </Button>
            )}
            {!historyOpen && !readOnly && (
              <Button variant="ghost" size="sm" disabled={!canSave} onClick={() => void controller.commit()} title={t.editor.saveVersionHint} aria-label={t.editor.saveVersion}>
                <Save />
                <span className="hidden sm:inline">{t.editor.saveVersion}</span>
              </Button>
            )}
            {!historyOpen && (
              <DropdownMenu>
                <DropdownMenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label={t.editor.more} />}>
                  <MoreHorizontal />
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="min-w-40">
                  <DropdownMenuItem onClick={() => setHistoryOpen(true)}>
                    <History />
                    {t.history.open}
                  </DropdownMenuItem>
                  {!readOnly && (
                    <>
                      <DropdownMenuItem onClick={() => setMoveOpen(true)}>
                        <FolderInput />
                        {t.editor.move}
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem variant="destructive" onClick={() => setDeleteOpen(true)}>
                        <Trash2 />
                        {t.editor.delete}
                      </DropdownMenuItem>
                    </>
                  )}
                </DropdownMenuContent>
              </DropdownMenu>
            )}
          </>
        }
      >
        <Breadcrumbs folder={page.folder} currentLast={false} />
      </TopBar>

      {historyOpen && (
        <Suspense fallback={<PageFallback />}>
          <HistoryMode space={space} pageId={page.id} folder={page.folder} getCurrent={currentContent} saveState={snapshot.state} onClose={() => setHistoryOpen(false)} onRestore={restoreVersion} readOnly={readOnly} />
        </Suspense>
      )}

      {/* Under the history view the editor stays as it is (so does everything typed
          in it), hidden and out of reach of the keyboard. */}
      <div className={historyOpen ? 'hidden' : mode === 'rich' ? 'flex flex-1 flex-col' : 'flex h-[calc(100dvh-3rem)] flex-col'} inert={historyOpen}>
      {mode !== 'rich' && sv.problem && (
        <div role="alert" className="shrink-0 border-b border-destructive/30 bg-destructive/10 px-4 py-2 text-sm">
          <span className="font-medium text-destructive">
            {t.fmt.labelled(t.modes.problemAt(sv.problem.line), t.modes.problem[sv.problem.code])}
          </span>
          {sv.problem.code === 'bad_yaml' && <span className="ml-2 font-mono text-xs text-muted-foreground">{sv.problem.message}</span>}
          <span className="ml-2 text-muted-foreground">{t.modes.problemHelp}</span>
          <button type="button" className="ml-2 underline underline-offset-4" onClick={sv.discardBroken}>
            {t.modes.discardBroken}
          </button>
        </div>
      )}
      {staleRaw && (
        <div role="status" className="shrink-0 border-b border-warning/30 bg-warning/10 px-4 py-2 text-sm">
          <span>{t.modes.staleRaw}</span>
          <button type="button" className="ml-2 underline underline-offset-4" onClick={resumeRaw}>
            {t.modes.staleRawResume}
          </button>
          <button type="button" className="ml-2 underline underline-offset-4" onClick={dropRaw}>
            {t.modes.staleRawDrop}
          </button>
        </div>
      )}
      <div className={mode === 'rich' ? 'flex flex-1 flex-col' : `grid min-h-0 flex-1 ${mode === 'split' ? 'grid-cols-2 divide-x' : 'grid-cols-1'}`}>
      {mode !== 'rich' && (
        <div className="min-h-0 min-w-0" onPointerDownCapture={() => sv.settle('source')} onFocusCapture={() => sv.settle('source')}>
          <Suspense fallback={<PageFallback />}>
            <SourceEditor
              key={sourceKey}
              ref={sourceRef}
              getInitialText={sv.currentText}
              onChange={sv.onSourceChange}
              onScroll={sv.onSourceScroll}
              problemLine={sv.problem?.line ?? null}
              label={t.modes.sourceLabel}
              className="h-full"
              readOnly={readOnly}
            />
          </Suspense>
        </div>
      )}
      <div
        ref={richScroller}
        onScroll={mode === 'split' ? sv.onRichScroll : undefined}
        onPointerDownCapture={mode === 'split' ? () => sv.settle('rich') : undefined}
        onFocusCapture={mode === 'split' ? () => sv.settle('rich') : undefined}
        // While the source text does not read as a page the editor side cannot
        // follow it, so it is out of reach: an edit there would be on old text.
        inert={mode === 'split' && sv.broken}
        className={mode === 'source' ? 'hidden' : mode === 'split' ? `min-h-0 min-w-0 overflow-y-auto ${sv.broken ? 'opacity-50' : ''}` : 'flex flex-1 flex-col'}
      >
      {editor && !readOnly && (
        <div className={`sticky z-20 border-b bg-background/90 backdrop-blur ${mode === 'split' ? 'top-0' : 'top-12'}`}>
          <div className="mx-auto max-w-[720px] px-3">
            <EditorToolbar editor={editor} />
          </div>
        </div>
      )}

      <div className="mx-auto w-full max-w-[720px] flex-1 px-5 pt-10 pb-24">
        <AutoTextarea
          value={fields.title}
          readOnly={readOnly}
          onChange={(v) => setField('title', v)}
          onEnter={() => subtitleRef.current?.focus()}
          placeholder={t.editor.titlePlaceholder}
          label={t.editor.titlePlaceholder}
          className="pb-title"
        />
        {(!readOnly || fields.subtitle !== '') && (
        <AutoTextarea
          ref={subtitleRef}
          value={fields.subtitle}
          readOnly={readOnly}
          onChange={(v) => setField('subtitle', v)}
          onEnter={() => editor?.commands.focus('start')}
          placeholder={t.editor.subtitlePlaceholder}
          label={t.editor.subtitlePlaceholder}
          className="pb-subtitle mt-1"
        />
        )}
        <div className="mt-4 mb-6 flex flex-col gap-2">
          <TagsEditor tags={fields.tags} onChange={setTags} suggestions={tags.data?.tags ?? []} readOnly={readOnly} />
          {(!readOnly || (fields.source ?? '') !== '') && (
          <AutoTextarea
            readOnly={readOnly}
            value={fields.source ?? ''}
            onChange={(v) => setField('source', v)}
            onEnter={() => editor?.commands.focus('start')}
            placeholder={t.editor.sourcePlaceholder}
            label={t.editor.sourceLabel}
            className="pb-source"
          />
          )}
          {typeName !== '' && (
            <Suspense fallback={null}>
              <PropertyPanel typeName={typeName} values={propValues} onChange={setProps} readOnly={readOnly} />
            </Suspense>
          )}
        </div>
        {editor && (
          <>
            <EditorContent editor={editor} />
            {!readOnly && (
              <>
                <SelectionBubble editor={editor} />
                <LinkPopover editor={editor} />
                <ImagePopover editor={editor} />
                <TableControls editor={editor} />
              </>
            )}
          </>
        )}
      </div>
      </div>
      </div>
      </div>

      {conflict && <ConflictDialog open={conflictOpen} onOpenChange={setConflictOpen} info={conflict} onKeepMine={keepMine} onUseTheirs={useTheirs} />}
      {!readOnly && <MovePageDialog open={moveOpen} onOpenChange={setMoveOpen} current={page.folder} folders={(tree.data?.folders ?? []).map((f) => f.path)} onMove={move} />}
      <AlertDialog open={deleteOpen && !readOnly} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t.delete.title}</AlertDialogTitle>
            <AlertDialogDescription>
              {t.fmt.quoted(fields.title.trim() || t.editor.untitled, t.delete.description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t.delete.cancel}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={() => void remove()}>
              {t.delete.confirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
