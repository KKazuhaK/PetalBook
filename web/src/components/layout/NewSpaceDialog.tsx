import { useEffect, useRef, useState, type FormEvent } from 'react'
import { ExternalLink } from 'lucide-react'
import { ApiError, api } from '@/api/client'
import { useCreateSpace, useGitHub, useGitHubOwners } from '@/api/hooks'
import type { GitHubOwner, NewGitHubRepo, SpaceInfo } from '@/api/types'
import { Field } from '@/components/auth/Field'
import { Spinner } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import { t } from '@/i18n'
import { tg } from '@/i18n/github'

// A name for a repository from the name of a space: letters, digits and the few
// marks GitHub keeps; everything else becomes a hyphen. Only a suggestion.
export function repoNameFrom(name: string): string {
  const s = name
    .trim()
    .normalize('NFKD')
    .replace(/[^A-Za-z0-9._-]+/g, '-')
    .replace(/^[-.]+|[-.]+$/g, '')
    .slice(0, 100)
  return s.toLowerCase().endsWith('.git') ? s.slice(0, -4) : s
}

const repoNameOk = (n: string) => /^[A-Za-z0-9._-]{1,100}$/.test(n) && n !== '.' && n !== '..' && !n.toLowerCase().endsWith('.git')

interface Left {
  fullName: string
  htmlUrl: string
}

// Makes a space: its name and, when the server has a GitHub App, whether to
// make a repository for it on GitHub too (owner, name, private, description).
// Mounted only while open, so the form starts empty each time.
export function NewSpaceDialog({ open, onOpenChange, onCreated }: { open: boolean; onOpenChange: (open: boolean) => void; onCreated: (space: SpaceInfo) => void }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {open && <NewSpaceForm onClose={() => onOpenChange(false)} onCreated={onCreated} />}
    </Dialog>
  )
}

function NewSpaceForm({ onClose, onCreated }: { onClose: () => void; onCreated: (space: SpaceInfo) => void }) {
  const create = useCreateSpace()
  const github = useGitHub()
  const app = github.data?.app ?? null
  const [name, setName] = useState('')
  const [useRepo, setUseRepo] = useState(false)
  const [owner, setOwner] = useState('')
  const [repo, setRepo] = useState('')
  const [repoTouched, setRepoTouched] = useState(false)
  const [priv, setPriv] = useState(true)
  const [description, setDescription] = useState('')
  const [error, setError] = useState('')
  const [fieldError, setFieldError] = useState<{ name?: string; repo?: string; description?: string }>({})
  // A repository the server made and could not use yet, or one that was there already: the retry connects to it.
  const [left, setLeft] = useState<Left | null>(null)
  const [taken, setTaken] = useState(false)
  const [busy, setBusy] = useState(false)
  const input = useRef<HTMLInputElement>(null)
  useEffect(() => input.current?.select(), [])

  const owners = useGitHubOwners(useRepo && app !== null)
  const list: GitHubOwner[] = owners.data?.pages.flatMap((p) => p.items) ?? []
  // The first account until the person picks one.
  const chosen = list.find((o) => o.login === owner) ?? list[0]
  const connected = github.data?.authorization?.state === 'ok'

  const submit = async (e: FormEvent, existing?: { owner: string; name: string }) => {
    e.preventDefault()
    if (busy) return
    const trimmed = name.trim()
    const problems: typeof fieldError = {}
    if (trimmed === '' || Array.from(trimmed).length > 80) problems.name = t.spaces.nameInvalid
    let gh: NewGitHubRepo | undefined
    if (useRepo) {
      const repoName = existing?.name ?? repo.trim()
      const ownerLogin = existing?.owner ?? chosen?.login ?? ''
      if (!repoNameOk(repoName)) problems.repo = tg.newSpace.repoInvalid
      if (Array.from(description).length > 350 || Array.from(description).some((c) => c.charCodeAt(0) < 0x20 || c.charCodeAt(0) === 0x7f)) problems.description = tg.newSpace.descriptionInvalid
      if (ownerLogin === '') problems.repo ??= tg.newSpace.ownerNone
      if (existing === undefined && chosen?.needsInstall) problems.repo ??= tg.newSpace.errors.notInstalled(ownerLogin)
      gh = { owner: ownerLogin, name: repoName, private: priv, ...(description.trim() ? { description: description.trim() } : {}), create: existing === undefined }
    }
    setFieldError(problems)
    if (Object.keys(problems).length > 0) return
    setBusy(true)
    setError('')
    setLeft(null)
    setTaken(false)
    try {
      // A Chinese-language app names the inbox folder in Chinese.
      const space = await create.mutateAsync({ name: trimmed, inbox: t.inbox.folderName, ...(gh ? { github: gh } : {}) })
      onCreated(space)
      onClose()
    } catch (err) {
      setBusy(false)
      explain(err)
    }
  }

  const explain = (err: unknown) => {
    if (!(err instanceof ApiError)) return setError(t.spaces.failed)
    const body = err.body as { repo?: { fullName?: string; htmlUrl?: string } } | undefined
    const code = err.code as string
    switch (code) {
      case 'github_name_taken':
        setTaken(true)
        return setFieldError({ repo: tg.newSpace.errors.nameTaken })
      case 'github_push_failed':
      case 'github_repo_not_in_installation':
      case 'internal':
        if (body?.repo?.fullName) setLeft({ fullName: body.repo.fullName, htmlUrl: body.repo.htmlUrl ?? '' })
        return setError(code === 'github_repo_not_in_installation' ? tg.newSpace.errors.notInInstallation : code === 'github_push_failed' ? tg.newSpace.errors.pushFailed : t.spaces.failed)
      case 'github_no_permission':
        return setError(tg.newSpace.errors.noPermission(err.message))
      case 'github_app_not_installed':
        return setError(tg.newSpace.errors.notInstalled(chosen?.login ?? ''))
      case 'github_app_missing_permission':
        if (body?.repo?.fullName) setLeft({ fullName: body.repo.fullName, htmlUrl: body.repo.htmlUrl ?? '' })
        return setError(tg.newSpace.errors.missingPermission((err.body as { missing?: string[] } | undefined)?.missing ?? []))
      case 'github_owner_not_allowed':
        return setError(tg.newSpace.errors.ownerNotAllowed)
      case 'github_not_authorized':
        return setError(tg.newSpace.errors.notAuthorized)
      case 'github_not_configured':
        return setError(tg.newSpace.errors.notConfigured)
      case 'rate_limited':
        return setError(tg.newSpace.errors.rateLimited)
      case 'unavailable':
        return setError(tg.newSpace.errors.unavailable)
      default:
        return setError(err.message || t.spaces.failed)
    }
  }

  const connect = async () => {
    try {
      // Installing the App on one's own account connects it in the same step.
      const { url } = await api.githubStart(window.location.pathname, true)
      window.location.assign(url)
    } catch (err) {
      explain(err)
    }
  }

  const [leftOwner, leftName] = left ? left.fullName.split('/') : ['', '']

  return (
    <DialogContent className="max-h-[calc(100dvh-2rem)] grid-cols-[minmax(0,1fr)] overflow-y-auto">
      <form className="grid min-w-0 grid-cols-1 gap-4" onSubmit={(e) => void submit(e)} noValidate>
        <DialogHeader>
          <DialogTitle>{t.spaces.newTitle}</DialogTitle>
          <DialogDescription>{t.spaces.nameHelp}</DialogDescription>
        </DialogHeader>
        <Field
          label={t.spaces.nameLabel}
          inputRef={input}
          value={name}
          onChange={(e) => {
            setName(e.target.value)
            if (!repoTouched) setRepo(repoNameFrom(e.target.value))
          }}
          error={fieldError.name}
          autoFocus
          autoComplete="off"
        />

        {app && (
          <fieldset className="grid min-w-0 grid-cols-1 gap-3 rounded-lg border p-3">
            <legend className="px-1 text-xs text-muted-foreground">{tg.newSpace.legend}</legend>
            <label className="flex cursor-pointer items-start gap-2 text-sm">
              <input type="checkbox" className="mt-1 accent-[var(--brand)]" checked={useRepo} onChange={(e) => setUseRepo(e.target.checked)} />
              <span className="min-w-0">
                <span className="block font-medium">{tg.newSpace.option}</span>
                <span className="block text-xs text-muted-foreground">{tg.newSpace.optionHelp}</span>
              </span>
            </label>

            {useRepo && (
              <div className="grid min-w-0 grid-cols-1 gap-3">
                {owners.isPending ? (
                  <p className="flex items-center gap-2 text-sm text-muted-foreground">
                    <Spinner />
                    {tg.newSpace.loadingOwners}
                  </p>
                ) : owners.isError ? (
                  <p role="alert" className="text-sm text-destructive break-words">
                    {t.fmt.labelled(tg.newSpace.ownersFailed, owners.error.message)}
                  </p>
                ) : list.length === 0 ? (
                  <div className="grid grid-cols-1 gap-2 text-sm">
                    <p className="text-muted-foreground">{tg.newSpace.ownerNone}</p>
                    {github.data?.userAuthorization && !connected && (
                      <div>
                        <Button type="button" variant="outline" onClick={() => void connect()}>
                          {tg.newSpace.connect}
                        </Button>
                      </div>
                    )}
                  </div>
                ) : (
                  <div className="grid min-w-0 grid-cols-1 gap-1.5">
                    <Label htmlFor="new-space-owner">{tg.newSpace.owner}</Label>
                    <NativeSelect id="new-space-owner" value={chosen?.login ?? ''} onChange={(e) => setOwner(e.target.value)}>
                      {list.map((o) => (
                        <option key={o.login} value={o.login}>
                          {o.login}
                          {o.type === 'Organization' ? ` · ${tg.newSpace.organisation}` : ''}
                          {o.needsInstall ? ` · ${tg.newSpace.needsInstall}` : ''}
                        </option>
                      ))}
                    </NativeSelect>
                    {owners.hasNextPage && (
                      <div>
                        <Button type="button" variant="ghost" size="sm" disabled={owners.isFetchingNextPage} onClick={() => void owners.fetchNextPage()}>
                          {owners.isFetchingNextPage ? <Spinner /> : null}
                          {tg.newSpace.moreOwners}
                        </Button>
                      </div>
                    )}
                    <p className="text-xs text-muted-foreground">{chosen?.via === 'app' ? tg.newSpace.viaApp : tg.newSpace.viaUser}</p>
                    {chosen?.needsInstall && (
                      <div className="grid grid-cols-1 gap-2 rounded-lg border border-warning/50 bg-warning/5 p-3 text-sm">
                        <p className="break-words">{tg.newSpace.needsInstallHelp(chosen.login)}</p>
                        <div>
                          <Button type="button" variant="outline" size="sm" onClick={() => void connect()}>
                            {tg.newSpace.installOnMine}
                          </Button>
                        </div>
                      </div>
                    )}
                  </div>
                )}

                <Field
                  label={tg.newSpace.repoName}
                  help={tg.newSpace.repoNameHelp}
                  value={repo}
                  onChange={(e) => {
                    setRepo(e.target.value)
                    setRepoTouched(true)
                    setTaken(false)
                  }}
                  error={fieldError.repo}
                  autoComplete="off"
                  spellCheck={false}
                  className="w-full min-w-0"
                />
                <label className="flex cursor-pointer items-start gap-2 text-sm">
                  <input type="checkbox" className="mt-1 accent-[var(--brand)]" checked={priv} onChange={(e) => setPriv(e.target.checked)} />
                  <span className="min-w-0">
                    <span className="block font-medium">{tg.newSpace.private}</span>
                    <span className="block text-xs text-muted-foreground">{tg.newSpace.privateHelp}</span>
                  </span>
                </label>
                <div className="grid min-w-0 grid-cols-1 gap-1.5">
                  <Label htmlFor="new-space-description">{tg.newSpace.description}</Label>
                  <Textarea id="new-space-description" rows={2} maxLength={350} value={description} onChange={(e) => setDescription(e.target.value)} className="w-full min-w-0" aria-invalid={fieldError.description ? true : undefined} />
                  {fieldError.description && <p className="text-xs text-destructive">{fieldError.description}</p>}
                </div>
                {taken && chosen && (
                  <div className="grid grid-cols-1 gap-2 rounded-lg border bg-muted/30 p-3 text-sm">
                    <p className="break-words">{tg.newSpace.takenAsk(`${chosen.login}/${repo.trim()}`)}</p>
                    <div>
                      <Button type="button" variant="outline" size="sm" disabled={busy} onClick={(e) => void submit(e, { owner: chosen.login, name: repo.trim() })}>
                        {tg.newSpace.useExisting}
                      </Button>
                    </div>
                  </div>
                )}
              </div>
            )}
          </fieldset>
        )}

        {left && (
          <div role="alert" className="grid grid-cols-1 gap-2 rounded-lg border border-warning/50 bg-warning/5 p-3 text-sm">
            <p className="break-words">{tg.newSpace.leftBehind(left.fullName)}</p>
            <div className="flex flex-wrap items-center gap-2">
              <Button type="button" size="sm" disabled={busy} onClick={(e) => void submit(e, { owner: leftOwner, name: leftName })}>
                {busy && <Spinner />}
                {tg.newSpace.useLeft}
              </Button>
              {left.htmlUrl && /^https?:\/\//.test(left.htmlUrl) && (
                <a className="inline-flex items-center gap-1 text-xs break-all underline underline-offset-4" href={left.htmlUrl} target="_blank" rel="noreferrer noopener">
                  {tg.newSpace.openOnGitHub}
                  <ExternalLink className="size-3 shrink-0" />
                </a>
              )}
            </div>
          </div>
        )}
        {error && (
          <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm break-words text-destructive">
            {error}
          </p>
        )}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            {t.spaces.cancel}
          </Button>
          <Button type="submit" disabled={busy}>
            {busy && <Spinner />}
            {busy && useRepo ? tg.newSpace.creating : t.spaces.create}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  )
}
