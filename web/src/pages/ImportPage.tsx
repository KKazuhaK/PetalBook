// Bring Markdown files into the space as pages: one or more .md files, or one
// .zip of files and folders. Several .md files are sent one after another, each
// as its own request, and their reports are added up.

import { fileTooLargeText } from '@/lib/fileTooLarge'
import { useEffect, useRef, useState, type DragEvent, type FormEvent } from 'react'
import { useBlocker, useNavigate, useParams, useSearchParams } from 'react-router'
import { NotFoundPage } from '@/pages/NotFoundPage'
import { useQueryClient } from '@tanstack/react-query'
import { CheckCircle2, FileText, Upload } from 'lucide-react'
import { spaceApi } from '@/api/client'
import { invalidateListings, useActiveSpace, useSession, useSpaces } from '@/api/hooks'
import { can } from '@/lib/permissions'
import type { ImportReport } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { TopBar } from '@/components/layout/TopBar'
import { Button } from '@/components/ui/button'
import { formatBytes } from '@/lib/bytes'
import { checkFolderPath } from '@/lib/folders'
import { addReports, classifyFiles, emptyReport, mergeFiles } from '@/lib/importFiles'
import { useDocumentTitle } from '@/lib/useDocumentTitle'
import { useMaxImport } from '@/lib/useMaxImport'
import { spaceUrls } from '@/lib/urls'
import { t } from '@/i18n'

export default function ImportPage() {
  const { space = '' } = useParams()
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const session = useSession().data
  const spaces = useSpaces()
  // The space in use, which for an administrator may be one they are not a member of.
  const { spaces: usable, resolving } = useActiveSpace()
  const current = usable.find((s) => s.id === space)
  const maxImport = useMaxImport()
  useDocumentTitle(`${current ? t.import.pageTitle(current.name) : t.import.title} · ${t.appName}`)

  const [files, setFiles] = useState<File[]>([])
  const [folder, setFolder] = useState(params.get('folder') ?? '')
  const [dragging, setDragging] = useState(false)
  const [running, setRunning] = useState<{ at: number; of: number } | null>(null)
  const [error, setError] = useState('')
  const [report, setReport] = useState<{ total: ImportReport; folder: string } | null>(null)
  // The report of the files that went through before a failure.
  const [partial, setPartial] = useState<ImportReport | null>(null)
  const input = useRef<HTMLInputElement>(null)
  const abort = useRef<AbortController | null>(null)
  useEffect(() => () => abort.current?.abort(), [])
  // Leaving in the middle would stop the import after some of the files:
  // closing the tab and going elsewhere in the app both ask first.
  const busyRef = useRef(false)
  useEffect(() => {
    busyRef.current = running !== null
  }, [running])
  useEffect(() => {
    const onBeforeUnload = (e: BeforeUnloadEvent) => {
      if (!busyRef.current) return
      e.preventDefault()
      e.returnValue = ''
    }
    window.addEventListener('beforeunload', onBeforeUnload)
    return () => window.removeEventListener('beforeunload', onBeforeUnload)
  }, [])
  const blocker = useBlocker(() => busyRef.current)
  useEffect(() => {
    if (blocker.state !== 'blocked') return
    if (window.confirm(t.import.leaveWhileRunning)) blocker.proceed()
    else blocker.reset()
  }, [blocker])

  if (!session || resolving) return <PageFallback />
  if (spaces.isError && !spaces.data) return <ErrorState message={spaces.error.message} onRetry={() => void spaces.refetch()} />
  if (!current) return <NotFoundPage message={t.editor.spaceMissing} />
  // An import makes pages.
  if (!can(current, 'write')) return <NotFoundPage message={t.permissions.readOnlyNote} />

  const picked = classifyFiles(files)
  const pickError = picked.kind === 'error' ? (picked.error === 'zipAlone' ? t.import.zipAlone : t.import.oneZip) : ''
  const ignoredNote = picked.ignored.length ? t.import.notImportable(t.fmt.list(picked.ignored)) : ''
  const busy = running !== null
  // Only what can be sent is listed and counted; the rest is named in the note.
  const listed = files.filter((f) => !picked.ignored.includes(f.name))

  const add = (list: FileList | File[] | null) => {
    if (!list || busy) return
    // A copy now: the input's own list is emptied right after, and a drop's is gone once the event ends.
    const added = Array.from(list)
    setFiles((prev) => mergeFiles(prev, added))
    setReport(null)
    setPartial(null)
    setError('')
  }
  const onDrop = (e: DragEvent) => {
    e.preventDefault()
    setDragging(false)
    add(e.dataTransfer.files)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    if (picked.kind === 'none' || picked.kind === 'error') return setError(picked.kind === 'error' ? pickError : t.import.noFiles)
    let target = ''
    // Nothing, or only slashes, is the root.
    if (folder.replace(/[\s/]/g, '') !== '') {
      const checked = checkFolderPath(folder)
      if ('error' in checked) return setError(checked.error)
      target = checked.path
    }
    const send = picked.kind === 'zip' ? [picked.file] : picked.files
    // Checked here so a file the server would refuse is not sent at all.
    const big = maxImport === undefined ? undefined : send.find((f) => f.size > maxImport)
    if (big && maxImport !== undefined) return setError(t.import.tooLarge(big.name, formatBytes(big.size), formatBytes(maxImport)))

    setError('')
    setReport(null)
    setPartial(null)
    const ctl = new AbortController()
    abort.current = ctl
    let total = emptyReport()
    let done = 0
    try {
      for (const file of send) {
        setRunning({ at: done + 1, of: send.length })
        total = addReports(total, await spaceApi(space).importMarkdown(file, { folder: target, name: file.name }, ctl.signal))
        done++
      }
      setReport({ total, folder: target })
      setFiles([])
    } catch (err) {
      if (err instanceof DOMException && err.name === 'AbortError') return
      const tooBig = fileTooLargeText(err)
      const why = err instanceof Error ? err.message : ''
      const text = tooBig ?? t.fmt.labelled(t.import.failed, why)
      setError(done > 0 ? `${text} ${t.import.failedAfter(done, send.length)}` : text)
      // What the files before it brought (and left out) is still shown.
      if (done > 0) setPartial(total)
      // What was imported before the failure is already there: those files leave
      // the list, so trying again does not import them a second time.
      if (done > 0) {
        const sent = send.slice(0, done)
        setFiles((prev) => prev.filter((f) => !sent.includes(f)))
      }
    } finally {
      setRunning(null)
      if (done > 0) void invalidateListings(qc, space)
    }
  }

  const urls = spaceUrls(space)

  return (
    <>
      <TopBar>
        <h1 className="truncate text-sm font-medium">{t.import.title}</h1>
      </TopBar>
      <div className="mx-auto w-full max-w-[640px] flex-1 space-y-6 px-4 py-6 sm:px-5">
        {report ? (
          <ImportDone report={report.total} onOpen={() => navigate(report.folder ? urls.folder(report.folder) : urls.recent())} folder={report.folder} onAgain={() => setReport(null)} />
        ) : (
          <form className="grid gap-5" onSubmit={(e) => void submit(e)} noValidate>
            <p className="text-sm text-muted-foreground">{t.import.intro}</p>
            <div className="grid gap-1.5">
              <span id="import-files" className="text-sm font-medium">{t.import.filesLabel}</span>
              <div
                aria-labelledby="import-files"
                onDragOver={(e) => {
                  e.preventDefault()
                  if (!busy) setDragging(true)
                }}
                onDragLeave={() => setDragging(false)}
                onDrop={onDrop}
                className={`flex flex-col items-center gap-2 rounded-lg border border-dashed px-4 py-8 text-center text-sm transition-colors ${dragging ? 'border-brand bg-brand/5' : 'border-input'} ${busy ? 'opacity-60' : ''}`}
              >
                <Upload className="size-5 text-muted-foreground" />
                {dragging ? (
                  <p>{t.import.dropRelease}</p>
                ) : (
                  <p className="text-muted-foreground">
                    {t.import.drop}{' '}
                    <button type="button" disabled={busy} onClick={() => input.current?.click()} className="font-medium text-foreground underline underline-offset-2 disabled:no-underline">
                      {t.import.choose}
                    </button>
                  </p>
                )}
                <input
                  ref={input}
                  type="file"
                  multiple
                  hidden
                  accept=".md,.markdown,.zip"
                  onChange={(e) => {
                    add(e.target.files)
                    // The same file can be chosen again after it was cleared.
                    e.target.value = ''
                  }}
                />
              </div>
            </div>

            {files.length > 0 && (
              <div className="grid gap-2">
                <div className="flex items-center justify-between text-xs text-muted-foreground">
                  <span>{t.import.chosen(listed.length)}</span>
                  <button type="button" disabled={busy} onClick={() => (setFiles([]), setPartial(null))} className="underline underline-offset-2 disabled:no-underline">
                    {t.import.clear}
                  </button>
                </div>
                <ul className="max-h-40 divide-y divide-border/60 overflow-y-auto rounded-md border text-sm">
                  {listed.map((f, i) => (
                    <li key={`${f.name}:${f.size}:${f.lastModified}:${i}`} className="flex items-center gap-2 px-3 py-1.5">
                      <FileText className="size-4 shrink-0 text-muted-foreground" />
                      <span className="min-w-0 flex-1 truncate">{f.name}</span>
                      <span className="shrink-0 text-xs text-muted-foreground tabular-nums">{formatBytes(f.size)}</span>
                    </li>
                  ))}
                </ul>
                {ignoredNote && <p className="text-xs text-muted-foreground">{ignoredNote}</p>}
              </div>
            )}

            <Field label={t.import.folderLabel} help={t.import.folderHelp} placeholder={t.import.folderPlaceholder} value={folder} onChange={(e) => setFolder(e.target.value)} disabled={busy} autoComplete="off" spellCheck={false} />

            <FormMessage>{error || pickError}</FormMessage>
            {partial && (
              <div className="grid gap-2">
                <p className="text-sm text-muted-foreground">{t.import.totals(partial.pages, partial.files)}</p>
                <SkippedList report={partial} />
              </div>
            )}
            {running && (
              <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
                <Spinner />
                {running.of > 1 ? t.import.progress(running.at, running.of) : t.import.progressOne}
              </p>
            )}
            <div>
              <Button type="submit" disabled={busy || files.length === 0}>
                {busy && <Spinner />}
                {busy ? t.import.running : t.import.start}
              </Button>
            </div>
          </form>
        )}
      </div>
    </>
  )
}

// What an import left out, and why.
function SkippedList({ report }: { report: ImportReport }) {
  const listed = report.skipped.length
  if (listed === 0) return null
  return (
    <div className="grid gap-1.5">
      <h3 className="text-sm font-medium">{t.import.skippedTitle(listed + (report.skippedMore ?? 0))}</h3>
      <ul tabIndex={0} className="max-h-64 divide-y divide-border/60 overflow-y-auto rounded-md border text-sm">
        {report.skipped.map((s, i) => (
          <li key={`${s.path}:${i}`} className="grid gap-0.5 px-3 py-2">
            <span className="break-all">{s.path}</span>
            <span className="text-xs text-muted-foreground">{s.reason}</span>
          </li>
        ))}
      </ul>
      {report.skippedMore ? <p className="text-xs text-muted-foreground">{t.import.skippedMore(report.skippedMore)}</p> : null}
    </div>
  )
}

function ImportDone({ report, folder, onOpen, onAgain }: { report: ImportReport; folder: string; onOpen: () => void; onAgain: () => void }) {
  return (
    <div className="grid gap-4">
      <div role="status" className="flex items-start gap-3 rounded-lg border p-4">
        <CheckCircle2 className="mt-0.5 size-5 shrink-0 text-brand" />
        <div className="grid gap-1">
          <h2 className="text-sm font-medium">{t.import.doneTitle}</h2>
          <p className="text-sm text-muted-foreground">{t.import.totals(report.pages, report.files)}</p>
        </div>
      </div>
      <SkippedList report={report} />
      <div className="flex flex-wrap gap-2">
        <Button onClick={onOpen}>{folder ? t.import.openFolder : t.import.openPages}</Button>
        <Button variant="outline" onClick={onAgain}>
          {t.import.another}
        </Button>
      </div>
    </div>
  )
}
