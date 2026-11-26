import { useMemo, useState } from 'react'
import { Plus, Save, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import {
  errorMessage,
  useDeleteTemplate,
  useSaveTemplate,
  useSetFolderTemplate,
  useSetQuickNoteTemplate,
  useTemplate,
  useTemplates,
  useTree,
  useTypes,
} from '@/api/hooks'
import type { PageTemplate, PutTemplateInput, TemplateDefaults } from '@/api/types'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import {
  draftOf,
  metaOf,
  otherKeys,
  tagList,
  templateLabel,
  templateNameProblem,
  type HeaderDraft,
} from '@/lib/templates'
import { templateMessage } from '@/lib/templateErrors'
import { tt } from '@/i18n/templates'
import { SettingsNote, SettingsSection } from './SettingsParts'

// The templates of the space: pages that new pages start from, and the default template of folders and of quick
// notes. Everyone who can read the space sees them; making, changing and deleting them and setting the defaults is
// for those who may manage it. The code is a chunk of its own, loaded when this section is opened.
export default function TemplatesPanel({ canManage }: { canManage: boolean }) {
  const templates = useTemplates()
  // The template being edited, or `new`.
  const [open, setOpen] = useState<string | 'new' | null>(null)

  if (templates.isPending) return <PageFallback />
  if (templates.isError) return <ErrorState message={tt.settings.loadFailed} onRetry={() => void templates.refetch()} />
  const { templates: list, defaults } = templates.data
  const foldersOf = (name: string) => Object.values(defaults.folders).filter((x) => x === name).length
  const names = list.map((x) => x.name)

  return (
    <div className="space-y-8">
      <SettingsSection title={tt.settings.title} description={canManage ? tt.settings.help : tt.settings.helpReadOnly}>
        {canManage && <p className="text-xs text-muted-foreground">{tt.settings.variablesHelp}</p>}
        {list.length === 0 && open !== 'new' && <SettingsNote>{tt.settings.none}</SettingsNote>}
        <ul className="grid gap-2">
          {list.map((tpl) => {
            const folders = foldersOf(tpl.name)
            return (
              <li key={tpl.name} data-template={tpl.name} className="rounded-lg border">
                <button
                  type="button"
                  aria-expanded={open === tpl.name}
                  onClick={() => setOpen(open === tpl.name ? null : tpl.name)}
                  className="flex w-full items-center justify-between gap-3 rounded-lg px-3 py-2.5 text-left outline-none transition-colors hover:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring/50"
                >
                  <span className="min-w-0">
                    <span className="block truncate text-sm font-medium">{tpl.name}</span>
                    <span className="block truncate text-xs text-muted-foreground">
                      {typeof tpl.meta?.title === 'string' ? tpl.meta.title : ''}
                    </span>
                  </span>
                  <span className="flex shrink-0 flex-wrap justify-end gap-x-2 text-xs text-muted-foreground">
                    {defaults.quickNote === tpl.name && <span>{tt.settings.quickBadge}</span>}
                    {folders > 0 && <span>{tt.settings.folderBadge(folders)}</span>}
                  </span>
                </button>
                {open === tpl.name && (
                  <div className="border-t p-3">
                    <OneTemplate
                      key={tpl.rev}
                      template={tpl}
                      others={names}
                      defaults={defaults}
                      canManage={canManage}
                      onClose={() => setOpen(null)}
                    />
                  </div>
                )}
              </li>
            )
          })}
          {open === 'new' && (
            <li className="rounded-lg border p-3" data-template="new">
              <TemplateEditor
                template={null}
                body=""
                others={names}
                defaults={defaults}
                onClose={() => setOpen(null)}
                onCreated={(name) => setOpen(name)}
              />
            </li>
          )}
        </ul>
        {canManage && open !== 'new' && (
          <div>
            <Button variant="outline" size="sm" onClick={() => setOpen('new')}>
              <Plus />
              {tt.settings.newTemplate}
            </Button>
          </div>
        )}
      </SettingsSection>
      <DefaultsSection templates={list} defaults={defaults} canManage={canManage} />
    </div>
  )
}

// --- one template: read with its body first -----------------------------------------------

function OneTemplate({
  template,
  others,
  defaults,
  canManage,
  onClose,
}: {
  template: PageTemplate
  others: string[]
  defaults: TemplateDefaults
  canManage: boolean
  onClose: () => void
}) {
  // The list leaves the body out; it is read when the template is opened.
  const one = useTemplate(template.name)
  if (one.isPending) return <PageFallback />
  if (one.isError) return <ErrorState message={tt.settings.loadOne} onRetry={() => void one.refetch()} />
  // Made again from what the server has when it has changed (someone else's save, or a conflict).
  if (!canManage) return <TemplateView template={one.data} />
  return (
    <TemplateEditor
      key={one.data.rev}
      template={one.data}
      body={one.data.body ?? ''}
      others={others}
      defaults={defaults}
      onClose={onClose}
    />
  )
}

// What someone who may not manage sees: the header and the body, as text.
function TemplateView({ template }: { template: PageTemplate }) {
  const d = draftOf(template.meta)
  const rows: [string, string][] = [
    [tt.settings.pageTitle, d.title],
    [tt.settings.subtitle, d.subtitle],
    [tt.settings.pageType, d.type],
    [tt.settings.tags, d.tags],
  ]
  return (
    <div className="grid gap-2 text-sm">
      <dl className="grid grid-cols-[max-content_1fr] gap-x-3 gap-y-1">
        {rows
          .filter(([, v]) => v !== '')
          .map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-xs text-muted-foreground">{k}</dt>
              <dd className="break-words">{v}</dd>
            </div>
          ))}
      </dl>
      <h4 className="text-xs font-medium text-muted-foreground">{tt.settings.view}</h4>
      <pre className="max-h-72 overflow-auto rounded-md bg-muted/40 p-2.5 font-mono text-xs whitespace-pre-wrap">
        {template.body ? template.body : tt.settings.emptyBody}
      </pre>
    </div>
  )
}

// --- delete -------------------------------------------------------------------------------------

function DeleteTemplate({
  template,
  defaults,
  onGone,
}: {
  template: PageTemplate
  defaults: TemplateDefaults
  onGone: () => void
}) {
  const del = useDeleteTemplate()
  const [asking, setAsking] = useState(false)
  const folders = Object.values(defaults.folders).filter((x) => x === template.name).length
  return (
    <>
      <Button type="button" variant="outline" size="sm" className="text-destructive" onClick={() => setAsking(true)}>
        <Trash2 />
        {tt.settings.delete}
      </Button>
      <AlertDialog open={asking} onOpenChange={setAsking}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tt.settings.deleteTitle(template.name)}</AlertDialogTitle>
            <AlertDialogDescription>
              {tt.settings.deleteHelp(folders, defaults.quickNote === template.name)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tt.cancel}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={del.isPending}
              onClick={() =>
                del.mutate(
                  { name: template.name, baseRev: template.rev },
                  {
                    onSuccess: () => {
                      toast.success(tt.settings.deleted)
                      onGone()
                    },
                    onError: (e) =>
                      toast.error(
                        e instanceof ApiError && e.code === 'conflict'
                          ? tt.settings.deleteChanged
                          : templateMessage(e, errorMessage(e, tt.settings.deleteFailed)),
                      ),
                  },
                )
              }
            >
              {tt.settings.deleteConfirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

// --- the editor of one template --------------------------------------------------------------

const nameText = {
  empty: tt.settings.nameEmpty,
  long: tt.settings.nameLong,
  chars: tt.settings.nameChars,
  reserved: tt.settings.nameReserved,
  exists: tt.settings.nameExists,
}

function TemplateEditor({
  template,
  body: savedBody,
  others,
  defaults,
  onClose,
  onCreated,
}: {
  template: PageTemplate | null
  body: string
  others: string[]
  defaults: TemplateDefaults
  onClose: () => void
  onCreated?: (name: string) => void
}) {
  const save = useSaveTemplate()
  const types = useTypes()
  const isNew = template === null
  const [name, setName] = useState('')
  const [header, setHeader] = useState<HeaderDraft>(() => draftOf(template?.meta))
  const [body, setBody] = useState(savedBody)
  const [error, setError] = useState('')

  const before = useMemo(
    () => JSON.stringify([metaOf(template?.meta, draftOf(template?.meta)), savedBody]),
    [template, savedBody],
  )
  const meta = metaOf(template?.meta, header)
  const dirty = isNew || JSON.stringify([meta, body]) !== before
  const nameProblem = isNew ? templateNameProblem(name.trim(), others) : null
  const invalid = isNew && nameProblem !== null
  const set = (patch: Partial<HeaderDraft>) => setHeader((h) => ({ ...h, ...patch }))
  const typeList = (types.data?.types ?? []).filter((x) => !x.error)
  const extra = otherKeys(template?.meta).length

  const submit = async () => {
    if (invalid || save.isPending) return
    setError('')
    // Made from the rev the form was opened at: someone else's change in between is a 409, not lost.
    const input: PutTemplateInput = { baseRev: template?.rev ?? '', meta, body }
    try {
      const saved = await save.mutateAsync({ name: isNew ? name.trim() : template.name, input })
      toast.success(tt.settings.saved)
      if (isNew) onCreated?.(saved.name)
    } catch (e) {
      // The template is read again by the hook, and the form is made again from it (it is keyed by its rev), so
      // what was typed is not kept: the message says so, where it outlives the form.
      if (e instanceof ApiError && e.code === 'conflict') toast.error(tt.settings.changedMeanwhile, { duration: 12000 })
      else setError(templateMessage(e, errorMessage(e, tt.settings.saveFailed)))
    }
  }

  return (
    <form
      className="grid gap-4"
      onSubmit={(e) => {
        e.preventDefault()
        void submit()
      }}
    >
      <div className="grid gap-1.5">
        <Label htmlFor="template-name">{tt.settings.name}</Label>
        {isNew ? (
          <Input
            id="template-name"
            autoFocus
            autoComplete="off"
            value={name}
            onChange={(e) => setName(e.target.value)}
            aria-invalid={nameProblem !== null && name !== '' ? true : undefined}
            aria-describedby="template-name-help"
          />
        ) : (
          <Input
            id="template-name"
            readOnly
            value={template.name}
            className="font-mono"
            aria-describedby="template-name-help"
          />
        )}
        <p
          id="template-name-help"
          className={`text-xs ${nameProblem && name !== '' ? 'text-destructive' : 'text-muted-foreground'}`}
        >
          {nameProblem && name !== '' ? nameText[nameProblem] : tt.settings.nameHelp}
        </p>
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="grid gap-1.5">
          <Label htmlFor="template-title">{tt.settings.pageTitle}</Label>
          <Input
            id="template-title"
            autoComplete="off"
            value={header.title}
            onChange={(e) => set({ title: e.target.value })}
          />
          <p className="text-xs text-muted-foreground">{tt.settings.pageTitleHelp}</p>
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="template-subtitle">{tt.settings.subtitle}</Label>
          <Input
            id="template-subtitle"
            autoComplete="off"
            value={header.subtitle}
            onChange={(e) => set({ subtitle: e.target.value })}
          />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="template-type">{tt.settings.pageType}</Label>
          <NativeSelect id="template-type" value={header.type} onChange={(e) => set({ type: e.target.value })}>
            <option value="">{tt.settings.noType}</option>
            {/* A type the template names that the space no longer has stays selectable, so saving does not drop it. */}
            {header.type !== '' && !typeList.some((x) => x.name === header.type) && (
              <option value={header.type}>{header.type}</option>
            )}
            {typeList.map((x) => (
              <option key={x.name} value={x.name}>
                {x.title || x.name}
              </option>
            ))}
          </NativeSelect>
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="template-tags">{tt.settings.tags}</Label>
          <Input
            id="template-tags"
            autoComplete="off"
            value={header.tags}
            onChange={(e) => set({ tags: e.target.value })}
            aria-describedby="template-tags-help"
          />
          <p id="template-tags-help" className="text-xs text-muted-foreground">
            {tagList(header.tags).length > 0 ? tagList(header.tags).join(' · ') : tt.settings.tagsHelp}
          </p>
        </div>
      </div>
      {extra > 0 && <p className="text-xs text-muted-foreground">{tt.settings.otherFields(extra)}</p>}
      <div className="grid gap-1.5">
        <Label htmlFor="template-body">{tt.settings.body}</Label>
        <Textarea
          id="template-body"
          rows={10}
          value={body}
          spellCheck={false}
          className="font-mono text-xs"
          onChange={(e) => setBody(e.target.value)}
          aria-describedby="template-body-help"
        />
        <p id="template-body-help" className="text-xs text-muted-foreground">
          {tt.settings.bodyHelp} {tt.settings.variablesHelp}
        </p>
      </div>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>{template && <DeleteTemplate template={template} defaults={defaults} onGone={onClose} />}</div>
        <div className="flex gap-2">
          <Button type="button" variant="outline" onClick={onClose}>
            {isNew || dirty ? tt.cancel : tt.close}
          </Button>
          <Button type="submit" disabled={!dirty || invalid || save.isPending}>
            {save.isPending ? <Spinner /> : <Save aria-hidden="true" />}
            {isNew ? tt.settings.create : tt.settings.save}
          </Button>
        </div>
      </div>
    </form>
  )
}

// --- the default templates ---------------------------------------------------------------------

function DefaultsSection({
  templates,
  defaults,
  canManage,
}: {
  templates: PageTemplate[]
  defaults: TemplateDefaults
  canManage: boolean
}) {
  const setFolder = useSetFolderTemplate()
  const setQuick = useSetQuickNoteTemplate()
  const tree = useTree()
  const [folder, setPickedFolder] = useState('')
  const [template, setPickedTemplate] = useState('')
  const folders = Object.entries(defaults.folders).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
  // The root has no default: only a folder can.
  const choices = (tree.data?.folders ?? []).map((f) => f.path).filter((p) => p !== '')
  const titleOf = (name: string) => templates.find((x) => x.name === name)?.name ?? name

  const fail = (e: unknown) => toast.error(templateMessage(e, errorMessage(e, tt.defaults.failed)))

  return (
    <SettingsSection title={tt.defaults.title} description={tt.defaults.help}>
      <div className="grid gap-1.5">
        <Label htmlFor="template-quick">{tt.defaults.quick}</Label>
        <NativeSelect
          id="template-quick"
          value={defaults.quickNote}
          disabled={!canManage || setQuick.isPending}
          onChange={(e) => {
            const to = e.target.value
            setQuick.mutate(to, {
              onSuccess: () => toast.success(to === '' ? tt.defaults.removed : tt.defaults.saved),
              onError: fail,
            })
          }}
        >
          <option value="">{tt.defaults.none}</option>
          {templates.map((x) => (
            <option key={x.name} value={x.name}>
              {templateLabel(x)}
            </option>
          ))}
        </NativeSelect>
        <p className="text-xs text-muted-foreground">{tt.defaults.quickHelp}</p>
      </div>
      {folders.length === 0 ? (
        <SettingsNote>{tt.defaults.noFolders}</SettingsNote>
      ) : (
        <ul className="divide-y divide-border/70 rounded-lg border">
          {folders.map(([path, name]) => (
            <li key={path} data-default={path} className="flex items-center justify-between gap-3 px-3 py-2">
              <span className="min-w-0 truncate text-sm">
                <span className="font-mono">{path}</span>
                <span className="mx-2 text-muted-foreground">→</span>
                <span>{titleOf(name)}</span>
              </span>
              {canManage && (
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={setFolder.isPending}
                  aria-label={tt.defaults.removeOf(path)}
                  onClick={() => setFolder.mutate({ folder: path, template: '' }, { onError: fail })}
                >
                  <Trash2 aria-hidden="true" />
                  {tt.defaults.remove}
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {canManage && (
        <form
          className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]"
          onSubmit={(e) => {
            e.preventDefault()
            if (folder === '' || template === '') return
            setFolder.mutate(
              { folder, template },
              {
                onSuccess: () => {
                  toast.success(tt.defaults.saved)
                  setPickedFolder('')
                  setPickedTemplate('')
                },
                onError: fail,
              },
            )
          }}
        >
          <NativeSelect
            aria-label={tt.defaults.folder}
            value={folder}
            onChange={(e) => setPickedFolder(e.target.value)}
          >
            <option value="">{tt.defaults.pickFolder}</option>
            {choices.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </NativeSelect>
          <NativeSelect
            aria-label={tt.defaults.template}
            value={template}
            disabled={templates.length === 0}
            onChange={(e) => setPickedTemplate(e.target.value)}
          >
            <option value="">{templates.length === 0 ? tt.defaults.noTemplates : tt.defaults.pickTemplate}</option>
            {templates.map((x) => (
              <option key={x.name} value={x.name}>
                {templateLabel(x)}
              </option>
            ))}
          </NativeSelect>
          <Button
            type="submit"
            variant="outline"
            size="sm"
            disabled={folder === '' || template === '' || setFolder.isPending}
          >
            {tt.defaults.setFolder}
          </Button>
        </form>
      )}
    </SettingsSection>
  )
}
