import { useRef, useState, type FormEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api, ApiError } from '@/api/client'
import { keys, useSpaces } from '@/api/hooks'
import type { ApiToken, CreatedToken, TokenAccess } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { TokenItem } from '@/components/tokens/TokenItem'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { t } from '@/i18n'
import { tk } from '@/i18n/tokens'
import GrantsSection from './GrantsSection'
import { SettingsSection } from './SettingsParts'

const EXPIRY_CHOICES = [30, 90, 365, 0] as const

// API tokens (accounts mode): make one, see the ones there are, revoke one. The
// secret of a new token is shown here once, from the response that made it.
export default function TokensPanel() {
  const qc = useQueryClient()
  const list = useQuery({ queryKey: keys.tokens, queryFn: ({ signal }) => api.tokens(signal), staleTime: 15_000 })
  const [making, setMaking] = useState(false)
  const [fresh, setFresh] = useState<CreatedToken | null>(null)
  const [revoking, setRevoking] = useState<ApiToken | null>(null)
  const [busy, setBusy] = useState(false)

  const revoke = async () => {
    if (!revoking) return
    setBusy(true)
    try {
      await api.revokeToken(revoking.id)
      toast.success(tk.revoked)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : tk.revokeFailed)
    }
    // Whatever happened, the list may have moved on.
    void qc.invalidateQueries({ queryKey: keys.tokens })
    setRevoking(null)
    setBusy(false)
  }

  return (
    <div className="grid w-full min-w-0 grid-cols-1 gap-8">
      <div className="grid w-full min-w-0 gap-3">
        <p className="text-sm text-muted-foreground">{tk.help}</p>
        {fresh && <FreshToken created={fresh} onDone={() => setFresh(null)} />}
        {making ? (
          <MakeForm
            onCancel={() => setMaking(false)}
            onMade={(c) => {
              setMaking(false)
              setFresh(c)
              void qc.invalidateQueries({ queryKey: keys.tokens })
            }}
          />
        ) : (
          !fresh && (
            <div>
              <Button type="button" variant="outline" onClick={() => setMaking(true)}>
                {tk.create}
              </Button>
            </div>
          )
        )}
      </div>
      <SettingsSection title={tk.yours}>
        {list.isPending ? (
          <PageFallback />
        ) : list.isError ? (
          <ErrorState message={t.fmt.labelled(tk.loadFailed, list.error.message)} onRetry={() => void list.refetch()} />
        ) : list.data.tokens.length === 0 ? (
          <p className="text-sm text-muted-foreground">{tk.none}</p>
        ) : (
          <ul className="w-full min-w-0 divide-y rounded-lg border">
            {list.data.tokens.map((token) => (
              <TokenItem
                key={token.id}
                token={token}
                action={
                  token.state === 'active' ? (
                    <Button type="button" variant="outline" size="sm" className="max-sm:w-full" aria-label={tk.revokeLabel(token.name)} onClick={() => setRevoking(token)}>
                      {tk.revoke}
                    </Button>
                  ) : undefined
                }
              />
            ))}
          </ul>
        )}
      </SettingsSection>
      <GrantsSection />
      <AlertDialog open={revoking !== null} onOpenChange={(open) => !open && setRevoking(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tk.revokeTitle}</AlertDialogTitle>
            <AlertDialogDescription className="break-words">{revoking ? tk.revokeHelp(revoking.name) : ''}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tk.cancel}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" disabled={busy} onClick={() => void revoke()}>
              {tk.revokeConfirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function MakeForm({ onCancel, onMade }: { onCancel: () => void; onMade: (c: CreatedToken) => void }) {
  const spaces = useSpaces()
  const mine = spaces.data?.spaces ?? []
  const [name, setName] = useState('')
  const [access, setAccess] = useState<TokenAccess>('read')
  // The personal space is ticked to start with: the most common wish, and a safe one.
  const [picked, setPicked] = useState<Set<string> | null>(null)
  const chosen = picked ?? new Set(mine.filter((s) => s.personal).map((s) => s.id))
  const [days, setDays] = useState<number>(90)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [problems, setProblems] = useState<{ name?: string; spaces?: string }>({})
  const messageRef = useRef<HTMLParagraphElement>(null)

  const toggle = (id: string) => {
    const next = new Set(chosen)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    setPicked(next)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setError('')
    const p: typeof problems = {}
    if (name.trim() === '') p.name = tk.nameRequired
    if (chosen.size === 0) p.spaces = tk.spaceRequired
    setProblems(p)
    if (Object.keys(p).length) return
    setBusy(true)
    try {
      // In the order of the list, not of the ticking.
      onMade(await api.createToken({ name: name.trim(), access, spaces: mine.filter((s) => chosen.has(s.id)).map((s) => s.id), expiresInDays: days }))
      return
    } catch (err) {
      setError(err instanceof ApiError ? (err.code === 'token_limit' ? tk.limit : err.message) : tk.createFailed)
      setTimeout(() => messageRef.current?.focus(), 0)
    }
    setBusy(false)
  }

  return (
    <form className="grid w-full min-w-0 grid-cols-1 gap-4 rounded-lg border p-3 sm:p-4" onSubmit={(e) => void submit(e)} noValidate aria-label={tk.createTitle}>
      <Field label={tk.name} help={tk.nameHelp} error={problems.name} value={name} onChange={(e) => setName(e.target.value)} maxLength={80} placeholder={tk.namePlaceholder} autoComplete="off" required />
      <div className="grid min-w-0 gap-1.5">
        <Label htmlFor="token-access">{tk.access}</Label>
        <NativeSelect id="token-access" value={access} onChange={(e) => setAccess(e.target.value as TokenAccess)} aria-describedby="token-access-help">
          <option value="read">{tk.accessNames.read}</option>
          <option value="write">{tk.accessNames.write}</option>
        </NativeSelect>
        <p id="token-access-help" className="text-xs text-muted-foreground">
          {tk.accessHelp}
        </p>
      </div>
      <fieldset className="grid min-w-0 gap-2" aria-describedby="token-spaces-help">
        <legend className="mb-1 text-sm font-medium">{tk.spaces}</legend>
        {mine.length === 0 ? (
          <p className="text-sm text-muted-foreground">{tk.noSpaces}</p>
        ) : (
          <ul className="grid min-w-0 gap-1.5">
            {mine.map((s) => (
              <li key={s.id} className="min-w-0">
                <label className="flex min-w-0 items-start gap-2 text-sm">
                  <input type="checkbox" className="mt-0.5 size-4 shrink-0 accent-[var(--color-primary)]" checked={chosen.has(s.id)} onChange={() => toggle(s.id)} />
                  <span className="min-w-0 break-words">{s.name}</span>
                </label>
              </li>
            ))}
          </ul>
        )}
        <p id="token-spaces-help" className="text-xs text-muted-foreground">
          {tk.spacesHelp}
        </p>
        {problems.spaces && (
          <p className="text-xs text-destructive" role="alert">
            {problems.spaces}
          </p>
        )}
      </fieldset>
      <div className="grid min-w-0 gap-1.5">
        <Label htmlFor="token-days">{tk.expires}</Label>
        <NativeSelect id="token-days" value={String(days)} onChange={(e) => setDays(Number(e.target.value))} aria-describedby="token-days-help">
          {EXPIRY_CHOICES.map((n) => (
            <option key={n} value={n}>
              {n === 0 ? tk.never : tk.days(n)}
            </option>
          ))}
        </NativeSelect>
        <p id="token-days-help" className="text-xs text-muted-foreground">
          {days === 0 ? tk.neverHelp : tk.expiresHelp}
        </p>
      </div>
      <FormMessage focusRef={messageRef}>{error}</FormMessage>
      <div className="flex flex-wrap items-center gap-3">
        <Button type="submit" disabled={busy} className="max-sm:w-full">
          {busy ? tk.creating : tk.submit}
        </Button>
        <Button type="button" variant="ghost" onClick={onCancel} className="max-sm:w-full">
          {tk.cancel}
        </Button>
      </div>
    </form>
  )
}

// The secret, once. It is in the response of the request that made the token
// and nowhere else; closing this drops it.
function FreshToken({ created, onDone }: { created: CreatedToken; onDone: () => void }) {
  const input = useRef<HTMLInputElement>(null)
  const [copied, setCopied] = useState(false)
  const [failed, setFailed] = useState(false)

  const copy = async () => {
    setFailed(false)
    try {
      await navigator.clipboard.writeText(created.secret)
      setCopied(true)
    } catch {
      input.current?.select()
      setFailed(true)
    }
  }

  return (
    <div className="grid w-full min-w-0 gap-2 rounded-lg border border-brand/40 bg-brand/5 p-3" role="group" aria-labelledby="fresh-token-title">
      <h3 id="fresh-token-title" className="text-sm font-medium break-words">
        {tk.secretTitle}
      </h3>
      <p className="text-xs break-words text-muted-foreground">{tk.secretHelp}</p>
      <div className="flex min-w-0 flex-wrap gap-2">
        <Input ref={input} className="w-full min-w-0 flex-1 basis-56 font-mono text-xs" readOnly value={created.secret} aria-label={created.token.name} onFocus={(e) => e.currentTarget.select()} />
        <Button type="button" onClick={() => void copy()}>
          {copied ? tk.copied : tk.copy}
        </Button>
        <Button type="button" variant="ghost" onClick={onDone}>
          {tk.done}
        </Button>
      </div>
      <span role="status" className="text-xs text-destructive">
        {failed ? tk.copyFailed : ''}
      </span>
    </div>
  )
}
