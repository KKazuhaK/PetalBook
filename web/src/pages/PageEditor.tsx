import { useEffect, useRef, useState, useSyncExternalStore } from 'react'
import { Link, useBlocker, useNavigate, useParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { EditorContent, useEditor } from '@tiptap/react'
import { Placeholder } from '@tiptap/extensions'
import { Folder, MoreHorizontal, Trash2, FolderInput } from 'lucide-react'
import { toast } from 'sonner'
import { api, ApiError } from '@/api/client'
import { invalidateListings, keys, usePage, useTags, useTree } from '@/api/hooks'
import type { Page } from '@/api/types'
import { createExtensions, docToMarkdown, markdownToDoc } from '@/markdown'
import { planRestore } from '@/editor/draftRestore'
import { draftStoreFor, loadDraft, parseSavedMarker, savedMarkerKey } from '@/editor/draftStore'
import { LateBound } from '@/editor/lateBound'
import { recoverDraft } from '@/editor/recoverDraft'
import { LiveDraft } from '@/editor/liveDraft'
import { fieldsOf, SaveController, waitForSaves, type Draft, type Fields } from '@/editor/saveController'
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
import { folderUrl } from '@/lib/folders'
import { useDocumentTitle } from '@/lib/useDocumentTitle'
import { t } from '@/i18n/zh-CN'

// The page's own extensions: exactly the Markdown layer's, with a link click
// that edits instead of navigating, plus the empty-body hint (display only).
const extensions = [...createExtensions({ linkOpensOnClick: false }), Placeholder.configure({ placeholder: t.editor.bodyPlaceholder })]

export default function PageEditor() {
  const { id = '' } = useParams()
  const { data, isPending, error, refetch } = usePage(id)

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
    if (missing) return <MissingPage id={id} />
    return (
      <>
        <TopBar />
        <div className="py-24 text-center">
          <ErrorState message={missing ? t.editor.notFound : `${t.editor.loadFailed}：${error?.message ?? ''}`} onRetry={missing ? undefined : () => void refetch()} />
          <Link to="/recent" className="text-sm text-brand underline underline-offset-4">
            {t.editor.backToList}
          </Link>
        </div>
      </>
    )
  }
  // One session per page id. It reads `data` once: later cache updates (our
  // own saves) must never reset what the person is typing.
  return <EditorSession key={data.id} initial={data} />
}

function MissingPage({ id }: { id: string }) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [draft] = useState(() => loadDraft(id))
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
      const page = await recoverDraft(id, draft, { create: (input) => api.createPage(input), toMarkdown: docToMarkdown })
      void invalidateListings(qc)
      toast.success(t.editor.recovered)
      navigate(`/p/${encodeURIComponent(page.id)}`, { replace: true })
    } catch (e) {
      setBusy(false)
      toast.error(`${t.editor.recoverFailed}：${e instanceof Error ? e.message : ''}`)
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
        <Link to="/recent" className="text-sm text-brand underline underline-offset-4">
          {t.editor.backToList}
        </Link>
      </div>
    </>
  )
}

function EditorSession({ initial }: { initial: Page }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const tree = useTree()
  const tags = useTags()

  // A draft left by an earlier visit (closed tab, failed save) is compared with
  // what the server has now before anything is shown.
  const [drafts] = useState(() => draftStoreFor(initial.id))
  const [restore] = useState(() =>
    planRestore(initial, loadDraft(initial.id), docToMarkdown, (md) => docToMarkdown(markdownToDoc(md))),
  )
  const [fields, setFields] = useState<Fields>(() => (restore.kind === 'none' ? fieldsOf(initial.meta) : restore.fields))
  // What has been typed, readable when a save runs (see LiveDraft).
  const [live] = useState(() => new LiveDraft(fields, docToMarkdown))
  const [conflict, setConflict] = useState<{ current: Page; mine: Draft } | null>(null)
  const [conflictOpen, setConflictOpen] = useState(false)
  const [moveOpen, setMoveOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const subtitleRef = useRef<HTMLTextAreaElement>(null)

  // Computed once: a brand-new blank page puts the cursor in the body.
  const [startInBody] = useState(() => !fields.title && initial.body.trim() === '')
  const [initialDoc] = useState(() => (restore.kind !== 'none' && restore.doc ? restore.doc : markdownToDoc(initial.body)))

  // Called when another tab saved this page and this one has nothing unsaved.
  const [reload] = useState(() => new LateBound<[string]>())
  const [controller] = useState(
    () =>
      new SaveController({
        draftToken: restore.kind === 'none' ? undefined : restore.token,
        onExternalSave: reload.call,
        page: initial,
        read: live.read,
        save: (id, input, opts) => api.savePage(id, input, opts),
        // What the editor would write for the original text, so an edit that
        // is undone again is recognised as no change at all.
        normalizeBody: (md) => docToMarkdown(markdownToDoc(md)),
        drafts,
        readDraft: live.readStorable,
        onSaved: (page) => {
          qc.setQueryData(keys.page(page.id), page)
          void invalidateListings(qc)
        },
        onConflict: (current, mine) => {
          setConflict({ current, mine })
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
    }
    const onPageHide = () => controller.flushKeepalive()
    const onBeforeUnload = (e: BeforeUnloadEvent) => {
      controller.persistNow()
      // Anything not yet confirmed saved (typed, in flight, failed, in conflict)
      // asks first. The local draft keeps it either way.
      if (controller.state !== 'saved') {
        e.preventDefault()
        e.returnValue = ''
      }
    }
    document.addEventListener('visibilitychange', onHidden)
    window.addEventListener('pagehide', onPageHide)
    window.addEventListener('beforeunload', onBeforeUnload)
    return () => {
      document.removeEventListener('visibilitychange', onHidden)
      window.removeEventListener('pagehide', onPageHide)
      window.removeEventListener('beforeunload', onBeforeUnload)
      void controller.detach()
    }
  }, [controller])

  // Once, on open: put a draft back, or raise the conflict it implies.
  useEffect(() => {
    if (restore.kind === 'none') {
      if (restore.stale) drafts.clear()
      return
    }
    if (restore.doc) live.bodyChanged()
    if (restore.kind === 'apply') controller.markDirty()
    else controller.enterConflict(initial, restore.mine)
  }, [restore, drafts, live, controller, initial])

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

  // Leaving with text that never reached the server asks first.
  const blocker = useBlocker(() => controller.state === 'conflict' || controller.state === 'error')
  useEffect(() => {
    if (blocker.state !== 'blocked') return
    if (window.confirm(controller.state === 'conflict' ? t.editor.leaveConflict : t.editor.leaveUnsaved)) blocker.proceed()
    else blocker.reset()
  }, [blocker, controller])

  const page = controller.page
  useDocumentTitle(`${fields.title.trim() || t.editor.untitled} · ${t.appName}`)

  const setField = (key: 'title' | 'subtitle', value: string) => {
    setFields(live.setField(key, value))
    controller.markDirty()
  }
  const setTags = (next: string[]) => {
    setFields(live.setTags(next))
    controller.markDirty()
  }

  const keepMine = () => {
    if (!conflict) return
    setConflictOpen(false)
    void controller.keepMine(conflict.current)
    setConflict(null)
  }
  // Replace everything on screen with what the server has.
  const loadServerVersion = (theirs: Page) => {
    if (!editor) return
    controller.useTheirs(theirs)
    const f = fieldsOf(theirs.meta)
    live.reset(f)
    setFields(f)
    editor.commands.setContent(markdownToDoc(theirs.body), { emitUpdate: false })
    qc.setQueryData(keys.page(theirs.id), theirs)
  }
  const useTheirs = () => {
    if (!conflict) return
    loadServerVersion(conflict.current)
    setConflictOpen(false)
    setConflict(null)
  }

  // Another tab saved this same page. This tab has nothing unsaved, so show
  // what was saved; with something unsaved the controller instead saves now and
  // the person meets the usual conflict dialog.
  useEffect(() => {
    reload.bind(() => {
      void api.getPage(initial.id).then((page) => {
        if (page.rev === controller.page.rev) return
        if (!controller.hasUnsaved() && controller.state === 'saved') loadServerVersion(page)
      }, () => undefined)
    })
    const key = savedMarkerKey(initial.id)
    const onStorage = (e: StorageEvent) => {
      if (e.key !== key) return
      const marker = parseSavedMarker(e.newValue)
      if (marker) controller.noticeExternalSave(marker.rev)
    }
    window.addEventListener('storage', onStorage)
    return () => {
      window.removeEventListener('storage', onStorage)
      reload.bind(null)
    }
    // loadServerVersion closes over the editor, which exists once this runs.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [controller, initial.id, reload, editor])

  const move = async (folder: string) => {
    // Whatever is typed goes to the server first; the file then moves as saved.
    await controller.flush()
    if (controller.state === 'conflict' || controller.state === 'error') {
      toast.error(t.editor.moveFailed)
      throw new Error('unsaved')
    }
    try {
      const moved = await api.movePage(page.id, folder)
      controller.adopt(moved)
      void invalidateListings(qc)
      toast.success(t.editor.moved(folder))
    } catch (e) {
      toast.error(`${t.editor.moveFailed}：${e instanceof Error ? e.message : ''}`)
      throw e
    }
  }

  const remove = async () => {
    controller.suspend()
    try {
      await waitForSaves(page.id)
      await api.deletePage(page.id)
    } catch (e) {
      controller.attach()
      if (controller.hasUnsaved()) controller.markDirty()
      toast.error(`${t.editor.deleteFailed}：${e instanceof Error ? e.message : ''}`)
      return
    }
    controller.discard()
    qc.removeQueries({ queryKey: keys.page(page.id) })
    void invalidateListings(qc)
    toast.success(t.editor.deleted)
    navigate(folderUrl(page.folder), { replace: true })
  }

  return (
    <>
      <TopBar
        actions={
          <>
            <SaveIndicator snapshot={snapshot} onRetry={() => void controller.retry()} onOpenConflict={() => setConflictOpen(true)} />
            <DropdownMenu>
              <DropdownMenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label={t.editor.more} />}>
                <MoreHorizontal />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="min-w-40">
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
          </>
        }
      >
        <Link to={folderUrl(page.folder)} className="inline-flex min-w-0 items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
          <Folder className="size-3.5 shrink-0" />
          <span className="truncate">{page.folder === '' ? t.nav.rootFolder : page.folder}</span>
        </Link>
      </TopBar>

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
        <div className="mt-4 mb-6">
          <TagsEditor tags={fields.tags} onChange={setTags} suggestions={tags.data?.tags ?? []} />
        </div>
        {editor && (
          <>
            <EditorContent editor={editor} />
            <SelectionBubble editor={editor} />
            <LinkPopover editor={editor} />
          </>
        )}
      </div>

      {conflict && <ConflictDialog open={conflictOpen} onOpenChange={setConflictOpen} current={conflict.current} mine={conflict.mine} onKeepMine={keepMine} onUseTheirs={useTheirs} />}
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
