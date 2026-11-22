import { lazy, Suspense, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { Link, useBlocker, useNavigate, useParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { EditorContent, useEditor } from '@tiptap/react'
import { Placeholder } from '@tiptap/extensions'
import { Folder, History, MoreHorizontal, Save, Trash2, FolderInput } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError, isUnauthenticated, spaceApi, type SpaceApi } from '@/api/client'
import { invalidateListings, keys, usePage, useSession, useTags, useTree, useUrls } from '@/api/hooks'
import type { Page } from '@/api/types'
import type { VersionContent } from '@/diff'
import { createExtensions, docToMarkdown, markdownToDoc } from '@/markdown'
import { planRestore, type Content } from '@/editor/draftRestore'
import { draftRefFor, draftStoreFor, loadDraft, parseSavedMarker, savedMarkerKey } from '@/editor/draftStore'
import { LateBound } from '@/editor/lateBound'
import { recoverDraft } from '@/editor/recoverDraft'
import { LiveDraft } from '@/editor/liveDraft'
import { fieldsOf, SaveController, waitForSaves, type ConflictInfo, type Fields } from '@/editor/saveController'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { ErrorState, PageFallback } from '@/components/common'
import { TopBar } from '@/components/layout/TopBar'
import { AutoTextarea } from '@/components/editor/AutoTextarea'
import { LinkPopover, SelectionBubble } from '@/components/editor/BubbleToolbar'
import { ConflictDialog } from '@/components/editor/ConflictDialog'
import { EditorToolbar } from '@/components/editor/EditorToolbar'
import { MovePageDialog } from '@/components/editor/MovePageDialog'
import { SaveIndicator } from '@/components/editor/SaveIndicator'
import { TagsEditor } from '@/components/editor/TagsEditor'
import { failureText } from '@/lib/failure'
import { useDocumentTitle } from '@/lib/useDocumentTitle'
import { t } from '@/i18n/zh-CN'

// The history of a page (and the comparison code with it) loads when it is first
// opened: someone who only writes never pays for it.
const loadHistory = () => import('@/components/history/HistoryMode')
const HistoryMode = lazy(loadHistory)

// The page's own extensions: exactly the Markdown layer's, with a link click
// that edits instead of navigating, plus the empty-body hint (display only).
const extensions = [...createExtensions({ linkOpensOnClick: false }), Placeholder.configure({ placeholder: t.editor.bodyPlaceholder })]

export default function PageEditor() {
  const { id = '', space = '' } = useParams()
  const urls = useUrls()
  // Local drafts belong to a person: `local` in single-user mode.
  const userId = useSession().data?.user?.id ?? 'local'
  const { data, isPending, error, refetch } = usePage(space, id)

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
          <ErrorState message={`${t.editor.loadFailed}：${error?.message ?? ''}`} onRetry={() => void refetch()} />
          <Link to={urls.recent()} className="text-sm text-brand underline underline-offset-4">
            {t.editor.backToList}
          </Link>
        </div>
      </>
    )
  }
  // One session per page id. It reads `data` once: later cache updates must
  // never reset what the person is typing.
  return <EditorSession key={`${userId}/${space}/${data.id}`} space={space} userId={userId} initial={data} />
}

function MissingPage({ space, id, userId }: { space: string; id: string; userId: string }) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const urls = useUrls()
  const [ref] = useState(() => draftRefFor(userId, space, id))
  const [draft] = useState(() => loadDraft(ref))
  const [busy, setBusy] = useState(false)
  const preview = (() => {
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
      toast.error(`${t.editor.recoverFailed}：${failureText(e)}`)
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
  const [local] = useState(() => loadDraft(ref))
  const [restore] = useState(() => planRestore(initial, local, docToMarkdown, (md) => docToMarkdown(markdownToDoc(md))))
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
        void controller.commit()
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
        // Only in this browser: it goes to the server next.
        live.bodyChanged()
        controller.markDirty()
        return
      case 'conflict':
        live.bodyChanged()
        if (restore.dropLocal && local) drafts.clearIf(local.token)
        controller.enterConflict(restore.info, restore.unsaved)
    }
  }, [restore, local, drafts, live, controller])

  const editor = useEditor({
    extensions,
    content: initialDoc,
    autofocus: startInBody ? 'start' : false,
    editorProps: { attributes: { class: 'pb-prose', 'aria-label': '正文' } },
    onUpdate: ({ editor: ed }) => {
      // Always the editor that is live: in development React creates and
      // discards a first one, and that one must never be read from.
      live.attachEditor(ed)
      live.bodyChanged()
      controller.markDirty()
    },
  })

  useEffect(() => {
    if (editor) live.attachEditor(editor)
  }, [editor, live])

  // Leaving with a conflict open, or with text that never reached the server,
  // asks first. (Signed out: the sign-in page takes over, and the browser keeps the text.)
  const blocker = useBlocker(() => {
    const s = controller.getSnapshot()
    return s.state === 'conflict' || (s.state === 'error' && !isUnauthenticated(s.error))
  })
  useEffect(() => {
    if (blocker.state !== 'blocked') return
    if (window.confirm(controller.state === 'conflict' ? t.editor.leaveConflict : t.editor.leaveUnsaved)) blocker.proceed()
    else blocker.reset()
  }, [blocker, controller])

  const page = controller.page
  useDocumentTitle(`${fields.title.trim() || t.editor.untitled} · ${t.appName}`)

  const setField = (key: 'title' | 'subtitle' | 'source', value: string) => {
    setFields(live.setField(key, value))
    controller.markDirty()
  }
  const setTags = (next: string[]) => {
    setFields(live.setTags(next))
    controller.markDirty()
  }

  // Put these fields and this Markdown on screen, replacing what is there.
  const showContent = (f: Fields, markdown: string) => {
    if (!editor) return
    live.reset(f)
    setFields(f)
    editor.commands.setContent(markdownToDoc(markdown), { emitUpdate: false })
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
    showContent(fieldsOf(shown.meta), shown.body)
  }

  // Another tab or device changed what the server has, and this one has nothing
  // unsaved: show it. (With something unsaved the controller stores now and the
  // person meets the conflict dialog instead.)
  useEffect(() => {
    reload.bind((fresh) => {
      const plan = planRestore(fresh, null, docToMarkdown, (md) => docToMarkdown(markdownToDoc(md)))
      if (plan.kind === 'file') showContent(fieldsOf(fresh.meta), fresh.body)
      else {
        showContent(plan.content.fields, plan.content.markdown ?? fresh.body)
        live.bodyChanged()
        if (plan.kind === 'conflict') controller.enterConflict(plan.info, false)
      }
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
      toast.error(`${t.editor.moveFailed}：${failureText(e)}`)
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
      toast.error(`${t.editor.deleteFailed}：${failureText(e)}`)
      return
    }
    controller.discard()
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
    const meta: Record<string, unknown> = { ...controller.page.meta }
    for (const k of ['title', 'subtitle', 'source'] as const) {
      const v = f[k] ?? ''
      if (v === '') delete meta[k]
      else meta[k] = v
    }
    if (f.tags.length) meta.tags = f.tags
    else delete meta.tags
    return { meta, body: editor ? docToMarkdown(editor.getJSON()) : controller.page.body }
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
    const before = { fields: live.fields, markdown: docToMarkdown(editor.getJSON()) }
    const put = (f: Fields, markdown: string) => {
      showContent(f, markdown)
      live.bodyChanged()
      controller.markDirty()
    }
    put(fieldsOf(v.meta), v.body)
    setHistoryOpen(false)
    // Undo puts back what was there, but only while the restored text is still
    // what is on screen: once the person has typed on, or left the page, there is
    // nothing this button may replace.
    const restored = { fields: live.fields, markdown: docToMarkdown(editor.getJSON()) }
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
            docToMarkdown(editor.getJSON()) === restored.markdown
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
            <SaveIndicator snapshot={snapshot} draftSafe={!controller.hasUnsaved()} onRetry={() => void controller.retry()} onOpenConflict={() => setConflictOpen(true)} />
            {!historyOpen && (
              <Button variant="ghost" size="sm" onClick={() => setHistoryOpen(true)} onPointerEnter={() => void loadHistory()} onFocus={() => void loadHistory()} title={t.history.openHint} aria-label={t.history.open}>
                <History />
                <span className="hidden sm:inline">{t.history.open}</span>
              </Button>
            )}
            {!historyOpen && (
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
                  <DropdownMenuItem onClick={() => setMoveOpen(true)}>
                    <FolderInput />
                    {t.editor.move}
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem variant="destructive" onClick={() => setDeleteOpen(true)}>
                    <Trash2 />
                    {t.editor.delete}
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            )}
          </>
        }
      >
        <Link to={urls.folder(page.folder)} className="inline-flex min-w-0 items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
          <Folder className="size-3.5 shrink-0" />
          <span className="truncate">{page.folder === '' ? t.nav.rootFolder : page.folder}</span>
        </Link>
      </TopBar>

      {historyOpen && (
        <Suspense fallback={<PageFallback />}>
          <HistoryMode space={space} pageId={page.id} getCurrent={currentContent} saveState={snapshot.state} onClose={() => setHistoryOpen(false)} onRestore={restoreVersion} />
        </Suspense>
      )}

      {/* Under the history view the editor stays as it is (so does everything typed
          in it), hidden and out of reach of the keyboard. */}
      <div className={historyOpen ? 'hidden' : 'flex flex-1 flex-col'} inert={historyOpen}>
      {editor && (
        <div className="sticky top-12 z-20 border-b bg-background/90 backdrop-blur">
          <div className="mx-auto max-w-[720px] px-3">
            <EditorToolbar editor={editor} />
          </div>
        </div>
      )}

      <div className="mx-auto w-full max-w-[720px] flex-1 px-5 pt-10 pb-24">
        <AutoTextarea
          value={fields.title}
          onChange={(v) => setField('title', v)}
          onEnter={() => subtitleRef.current?.focus()}
          placeholder={t.editor.titlePlaceholder}
          label={t.editor.titlePlaceholder}
          className="pb-title"
        />
        <AutoTextarea
          ref={subtitleRef}
          value={fields.subtitle}
          onChange={(v) => setField('subtitle', v)}
          onEnter={() => editor?.commands.focus('start')}
          placeholder={t.editor.subtitlePlaceholder}
          label={t.editor.subtitlePlaceholder}
          className="pb-subtitle mt-1"
        />
        <div className="mt-4 mb-6 flex flex-col gap-2">
          <TagsEditor tags={fields.tags} onChange={setTags} suggestions={tags.data?.tags ?? []} />
          <AutoTextarea
            value={fields.source ?? ''}
            onChange={(v) => setField('source', v)}
            onEnter={() => editor?.commands.focus('start')}
            placeholder={t.editor.sourcePlaceholder}
            label={t.editor.sourceLabel}
            className="pb-source"
          />
        </div>
        {editor && (
          <>
            <EditorContent editor={editor} />
            <SelectionBubble editor={editor} />
            <LinkPopover editor={editor} />
          </>
        )}
      </div>
      </div>

      {conflict && <ConflictDialog open={conflictOpen} onOpenChange={setConflictOpen} info={conflict} onKeepMine={keepMine} onUseTheirs={useTheirs} />}
      <MovePageDialog open={moveOpen} onOpenChange={setMoveOpen} current={page.folder} folders={(tree.data?.folders ?? []).map((f) => f.path)} onMove={move} />
      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t.delete.title}</AlertDialogTitle>
            <AlertDialogDescription>
              「{fields.title.trim() || t.editor.untitled}」{t.delete.description}
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
