// What can be done to files and folders, shared by the listing, the sidebar
// tree and the breadcrumbs: one place for the dialogs, the requests, the
// undo after a move and the redirect when the folder being looked at moves.

import { createContext, lazy, Suspense, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useLocation, useNavigate } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import {
  useActiveSpace,
  useCreatePage,
  useSpaceApi,
  useTree,
  useUrls,
  invalidateListings,
  keys,
  useSpaceId,
} from '@/api/hooks'
import type { PageMeta, PageTemplate } from '@/api/types'
import { defaultFor, tzOffset } from '@/lib/templates'
import { ApiError } from '@/api/client'
import {
  baseName,
  buildFolderIndex,
  isSameOrUnder,
  joinPath,
  pagesInside,
  parentOf,
  pathAfter,
  planDrop,
  type FileItem,
  type FolderIndex,
} from '@/lib/folders'
import { t } from '@/i18n'
import { DeleteItemsDialog } from './DeleteItemsDialog'
import { MoveItemsDialog } from './MoveItemsDialog'
import { RenameFolderDialog } from './RenameFolderDialog'
import { NewFolderDialog } from '@/components/layout/NewFolderDialog'

// Binding a folder to a type: loaded when the dialog is first opened.
const SetTypeDialog = lazy(() => import('./SetTypeDialog'))
// So are the choice of a template for a new page and a folder's default template (with the words of templates).
const NewPageDialog = lazy(() => import('./NewPageDialog'))
const SetTemplateDialog = lazy(() => import('./SetTemplateDialog'))
// The icon and colour of a folder or a page; the picker itself is a chunk of its own, loaded when it opens.
const StyleDialog = lazy(() => import('./StyleDialog'))

type Dialog =
  | { kind: 'newFolder'; parent: string }
  | { kind: 'rename'; path: string }
  | { kind: 'setType'; path: string }
  | { kind: 'setTemplate'; path: string }
  | { kind: 'style'; item: FileItem }
  | { kind: 'newPage'; folder: string; meta?: PageMeta; templates: PageTemplate[]; initial: string }
  | { kind: 'move'; items: FileItem[]; onDone?: () => void }
  | { kind: 'delete'; items: FileItem[]; onDone?: () => void }

export interface FileOps {
  index: FolderIndex
  // The folder list has arrived (or failed): before that a folder looks empty.
  ready: boolean
  spaceName: string
  open: (item: FileItem) => void
  // Makes a page in the folder and opens it; when the space has templates (and the folder is not the inbox, whose
  // pages are quick notes) the person first chooses one. `meta` is what the page starts with (a tag's page, say).
  newPage: (folder: string, meta?: PageMeta) => Promise<void>
  newFolder: (parent: string) => void
  rename: (path: string) => void
  // Opens the choice of a type for a folder (needs `manage`).
  setType: (path: string) => void
  // Opens the choice of a folder's default template (needs `manage`).
  setTemplate: (path: string) => void
  // Opens the icon and colour of a folder or a page (needs `write`).
  setStyle: (item: FileItem) => void
  // Opens the folder picker.
  chooseMove: (items: FileItem[], onDone?: () => void) => void
  // Asks, then deletes.
  confirmDelete: (items: FileItem[], onDone?: () => void) => void
  // Moves without asking (a drop, or the picker's answer); shows the undo.
  moveTo: (items: FileItem[], target: string) => Promise<boolean>
  // Folder renamed or moved by the dialogs: follows the address if it was inside.
  renameFolder: (from: string, to: string) => Promise<void>
}

const Ctx = createContext<FileOps | null>(null)

// null outside the provider (a test of one component, say).
export const useFileOps = (): FileOps | null => useContext(Ctx)

const failureText = (e: unknown) => (e instanceof Error ? e.message : '')

export function FileOpsProvider({ children }: { children: ReactNode }) {
  const space = useSpaceId()
  const api = useSpaceApi()
  const qc = useQueryClient()
  const urls = useUrls()
  const navigate = useNavigate()
  const { pathname } = useLocation()
  const { id: spaceId, spaces } = useActiveSpace()
  const tree = useTree()
  const createPage = useCreatePage()
  const [dialog, setDialog] = useState<Dialog | null>(null)

  const index = useMemo(() => buildFolderIndex(tree.data?.folders ?? []), [tree.data])
  const spaceName = spaces.find((s) => s.id === spaceId)?.name ?? t.nav.rootFolder

  // The undo of a move happens later, when the address may be another one.
  const where = useRef({ pathname, base: urls.base, folder: urls.folder })
  useEffect(() => {
    where.current = { pathname, base: urls.base, folder: urls.folder }
  })
  const viewing = () => pathAfter(where.current.pathname, `${where.current.base}/f`)

  // A folder renamed or moved while someone is looking at it (or inside it)
  // is followed to its new address instead of leaving a page that is gone.
  const follow = (from: string, to: string) => {
    const here = viewing()
    if (here !== null && here !== '' && isSameOrUnder(here, from))
      navigate(where.current.folder(to + here.slice(from.length)), { replace: true })
  }

  const refresh = () => invalidateListings(qc, space)

  const moveFolderNow = async (from: string, to: string) => {
    await api.moveFolder(from, to)
    follow(from, to)
  }

  const moveTo = async (items: FileItem[], target: string): Promise<boolean> => {
    const plan = planDrop(items, target, index)
    if (!plan.ok) {
      if (plan.reason === 'self') toast.error(t.manage.cannotMoveIntoSelf)
      else if (plan.reason === 'exists')
        toast.error(t.manage.folderExistsThere(target === '' ? spaceName : baseName(target)))
      return false
    }
    if (plan.conflicts.length > 0) toast.error(t.manage.folderExistsThere(target === '' ? spaceName : baseName(target)))
    const pages = plan.moving.filter((i): i is Extract<FileItem, { kind: 'page' }> => i.kind === 'page')
    const folders = plan.moving.filter((i): i is Extract<FileItem, { kind: 'folder' }> => i.kind === 'folder')
    // What is needed to put it all back, newest first.
    const undo: (() => Promise<unknown>)[] = []
    let moved = 0
    try {
      if (pages.length > 0) {
        await api.batchPages({ op: 'move', ids: pages.map((p) => p.id), folder: target })
        moved += pages.length
        const from = new Map<string, string[]>()
        for (const p of pages) from.set(p.folder, [...(from.get(p.folder) ?? []), p.id])
        for (const [folder, ids] of from) undo.unshift(() => api.batchPages({ op: 'move', ids, folder }))
      }
      for (const f of folders) {
        const to = joinPath(target, baseName(f.path))
        await moveFolderNow(f.path, to)
        moved += 1
        undo.unshift(() => moveFolderNow(to, f.path))
      }
    } catch (e) {
      toast.error(t.fmt.labelled(t.manage.moveFailed, failureText(e)))
    }
    await refresh()
    if (moved === 0) return false
    toast(t.manage.moved(moved, target === '' ? spaceName : baseName(target)), {
      action: {
        label: t.manage.undo,
        onClick: () => {
          void (async () => {
            try {
              for (const step of undo) await step()
              toast.success(t.manage.undone)
            } catch (e) {
              toast.error(t.fmt.labelled(t.manage.moveFailed, failureText(e)))
            }
            await refresh()
          })()
        },
      },
    })
    return true
  }

  const deleteNow = async (items: FileItem[]) => {
    const pages = items.filter((i) => i.kind === 'page')
    const folders = items.filter((i): i is Extract<FileItem, { kind: 'folder' }> => i.kind === 'folder')
    let done = 0
    try {
      if (pages.length > 0) {
        await api.batchPages({ op: 'delete', ids: pages.map((p) => (p as { id: string }).id) })
        done += pages.length
      }
      for (const f of folders) {
        // The pages in it go with it; the dialog said so.
        await api.deleteFolder(f.path, true)
        done += 1
        const here = viewing()
        if (here !== null && here !== '' && isSameOrUnder(here, f.path))
          navigate(where.current.folder(parentOf(f.path)), { replace: true })
      }
      toast.success(t.manage.deleted(done))
    } catch (e) {
      // What went before the failure is gone already, and the person is told.
      const text = t.fmt.labelled(t.manage.deleteFailed, failureText(e))
      toast.error(done > 0 ? `${text} ${t.manage.deletedBefore(done)}` : text)
    } finally {
      await refresh()
    }
  }

  // `template` is left out for the server's choice (the folder's default, or the quick note's), "" is a blank page.
  const makePage = async (folder: string, meta?: PageMeta, template?: string) => {
    try {
      const page = await createPage.mutateAsync({
        folder,
        body: '',
        tz: tzOffset(),
        ...(meta ? { meta } : {}),
        ...(template === undefined ? {} : { template }),
      })
      navigate(urls.page(page.id))
    } catch (e) {
      // A refusal with a code of its own is told in the words of templates (loaded only now).
      const code = e instanceof ApiError ? (e.code as string) : ''
      const text = code.startsWith('template_')
        ? (await import('@/lib/templateErrors')).templateMessage(e, failureText(e))
        : failureText(e)
      toast.error(t.fmt.labelled(t.list.createFailed, text))
    }
  }

  const ops: FileOps = {
    index,
    ready: !tree.isPending,
    spaceName,
    open: (item) => navigate(item.kind === 'page' ? urls.page(item.id) : urls.folder(item.path)),
    newPage: async (folder, meta) => {
      const inbox = spaces.find((s) => s.id === spaceId)?.inbox ?? ''
      // A page made in the inbox is a quick note: the server uses the quick note default, nothing is asked.
      const quick = inbox !== '' && folder.toLowerCase() === inbox.toLowerCase()
      if (!quick) {
        // Read now, not from what was read a while ago: the folder's default may have changed since.
        const list = await qc
          .fetchQuery({ queryKey: keys.templates(space), queryFn: ({ signal }) => api.templates(signal), staleTime: 0 })
          .catch(() => null)
        if (list && list.templates.length > 0) {
          setDialog({
            kind: 'newPage',
            folder,
            meta,
            templates: list.templates,
            initial: defaultFor(folder, list.defaults, list.templates),
          })
          return
        }
      }
      await makePage(folder, meta)
    },
    newFolder: (parent) => setDialog({ kind: 'newFolder', parent }),
    rename: (path) => setDialog({ kind: 'rename', path }),
    setType: (path) => setDialog({ kind: 'setType', path }),
    setTemplate: (path) => setDialog({ kind: 'setTemplate', path }),
    setStyle: (item) => setDialog({ kind: 'style', item }),
    chooseMove: (items, onDone) => setDialog({ kind: 'move', items, onDone }),
    confirmDelete: (items, onDone) => setDialog({ kind: 'delete', items, onDone }),
    moveTo,
    renameFolder: async (from, to) => {
      await moveFolderNow(from, to)
      await refresh()
    },
  }
  const close = () => setDialog(null)

  return (
    <Ctx.Provider value={ops}>
      {children}
      <NewFolderDialog
        open={dialog?.kind === 'newFolder'}
        onOpenChange={(o) => !o && close()}
        parent={dialog?.kind === 'newFolder' ? dialog.parent : ''}
        spaceName={spaceName}
      />
      {dialog?.kind === 'rename' && (
        <RenameFolderDialog path={dialog.path} index={index} onRename={ops.renameFolder} onClose={close} />
      )}
      {dialog?.kind === 'setType' && (
        <Suspense fallback={null}>
          <SetTypeDialog path={dialog.path} spaceName={spaceName} onClose={close} />
        </Suspense>
      )}
      {dialog?.kind === 'setTemplate' && (
        <Suspense fallback={null}>
          <SetTemplateDialog path={dialog.path} spaceName={spaceName} onClose={close} />
        </Suspense>
      )}
      {dialog?.kind === 'style' && (
        <Suspense fallback={null}>
          <StyleDialog item={dialog.item} onClose={close} />
        </Suspense>
      )}
      {dialog?.kind === 'newPage' && (
        <Suspense fallback={null}>
          <NewPageDialog
            where={dialog.folder === '' ? spaceName : baseName(dialog.folder)}
            templates={dialog.templates}
            initial={dialog.initial}
            onCreate={(template) => makePage(dialog.folder, dialog.meta, template)}
            onClose={close}
          />
        </Suspense>
      )}
      {dialog?.kind === 'move' && (
        <MoveItemsDialog
          items={dialog.items}
          index={index}
          spaceName={spaceName}
          onMove={async (target) => {
            if (await moveTo(dialog.items, target)) dialog.onDone?.()
          }}
          onClose={close}
        />
      )}
      {dialog?.kind === 'delete' && (
        <DeleteItemsDialog
          items={dialog.items}
          pages={pagesInside(dialog.items, index)}
          onDelete={async () => {
            await deleteNow(dialog.items)
            dialog.onDone?.()
          }}
          onClose={close}
        />
      )}
    </Ctx.Provider>
  )
}
