import { useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router'
import { api, ApiError } from '@/api/client'
import type { ConsentInfo, TokenAccess } from '@/api/types'
import { AuthLayout } from '@/components/auth/AuthLayout'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { t } from '@/i18n'
import { oa } from '@/i18n/oauth'

// Where the browser goes when the person has decided: the client's own address
// (a code, or an error), which the server built.
const leaveTo = (url: string) => window.location.assign(url)

// The page an MCP client sends the person to. It shows who asks and where the
// answer goes, and lets the person pick the spaces and the access; the server
// holds the request, so the address carries only its id.
export default function ConsentPage({ leave = leaveTo }: { leave?: (url: string) => void }) {
  const [params] = useSearchParams()
  const req = params.get('req') ?? ''
  const info = useQuery({
    queryKey: ['oauth-consent', req],
    queryFn: ({ signal }) => api.consent(req, signal),
    enabled: req !== '',
    retry: false,
    // A request is seen once, then decided: nothing to refresh behind the person's back.
    staleTime: Infinity,
    gcTime: 0,
  })
  const gone = req === '' || (info.isError && info.error instanceof ApiError && info.error.status === 404)
  if (gone) {
    return (
      <AuthLayout title={oa.consent.title}>
        <FormMessage>{oa.consent.expired}</FormMessage>
      </AuthLayout>
    )
  }
  if (info.isPending) {
    return (
      <AuthLayout title={oa.consent.title} intro={oa.consent.loading}>
        <PageFallback />
      </AuthLayout>
    )
  }
  if (info.isError) {
    return (
      <AuthLayout title={oa.consent.title}>
        <ErrorState
          message={t.fmt.labelled(oa.consent.loadFailed, info.error.message)}
          onRetry={() => void info.refetch()}
        />
      </AuthLayout>
    )
  }
  return <ConsentForm req={req} info={info.data} leave={leave} />
}

function ConsentForm({ req, info, leave }: { req: string; info: ConsentInfo; leave: (url: string) => void }) {
  const canWrite = info.scopes.includes('write')
  const [access, setAccess] = useState<TokenAccess>('read')
  // Only the personal space is ticked to start with: the common wish, and a safe one.
  const [picked, setPicked] = useState<Set<string> | null>(null)
  const chosen = picked ?? new Set(info.spaces.filter((s) => s.personal).map((s) => s.id))
  const [busy, setBusy] = useState<'allow' | 'deny' | null>(null)
  const [left, setLeft] = useState(false)
  const [error, setError] = useState('')
  const [problem, setProblem] = useState('')
  const messageRef = useRef<HTMLParagraphElement>(null)

  const toggle = (id: string) => {
    const next = new Set(chosen)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    setPicked(next)
  }

  const decide = async (approve: boolean) => {
    if (busy) return
    setError('')
    setProblem('')
    if (approve && chosen.size === 0) {
      setProblem(oa.consent.spaceRequired)
      return
    }
    setBusy(approve ? 'allow' : 'deny')
    try {
      const { redirect } = await api.decideConsent(
        approve
          ? {
              req,
              approve: true,
              spaces: info.spaces.filter((s) => chosen.has(s.id)).map((s) => s.id),
              access: canWrite ? access : 'read',
            }
          : { req, approve: false },
      )
      setLeft(true)
      leave(redirect)
      return
    } catch (e) {
      setError(e instanceof ApiError ? (e.code === 'not_found' ? oa.consent.expired : e.message) : oa.consent.failed)
      setTimeout(() => messageRef.current?.focus(), 0)
    }
    setBusy(null)
  }

  // Everything that can be long wraps: the name is whatever the app called itself.
  return (
    <AuthLayout title={oa.consent.titleFor(info.client.name)} intro={oa.consent.intro}>
      <form
        className="grid w-full min-w-0 grid-cols-1 gap-4"
        aria-label={oa.consent.title}
        onSubmit={(e) => {
          e.preventDefault()
          void decide(true)
        }}
        noValidate
      >
        <dl className="grid min-w-0 gap-2 rounded-lg border p-3 text-sm" data-slot="consent-client">
          <div className="grid min-w-0 gap-0.5">
            <dt className="text-xs text-muted-foreground">{oa.consent.nameLabel}</dt>
            <dd className="font-medium break-words">{info.client.name}</dd>
            <dd className="text-xs text-muted-foreground">{oa.consent.nameNote}</dd>
          </div>
          <div className="grid min-w-0 gap-0.5">
            <dt className="text-xs text-muted-foreground">{oa.consent.clientLabel}</dt>
            <dd>
              <code className="text-xs break-all">{info.client.host || info.client.id}</code>
            </dd>
          </div>
          <div className="grid min-w-0 gap-0.5">
            <dt className="text-xs text-muted-foreground">{oa.consent.redirectLabel}</dt>
            <dd>
              <code className="text-xs font-medium break-all">{info.redirectHost}</code>
            </dd>
            <dd className="text-xs text-muted-foreground">{oa.consent.redirectHelp(info.redirectHost)}</dd>
          </div>
          {info.punycode && (
            <p
              className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
              role="alert"
              data-slot="punycode"
            >
              {oa.consent.punycode}
            </p>
          )}
        </dl>

        {canWrite ? (
          <div className="grid min-w-0 gap-1.5">
            <Label htmlFor="consent-access">{oa.consent.access}</Label>
            <NativeSelect
              id="consent-access"
              value={access}
              onChange={(e) => setAccess(e.target.value as TokenAccess)}
              aria-describedby="consent-access-help"
            >
              <option value="read">{oa.consent.accessNames.read}</option>
              <option value="write">{oa.consent.accessNames.write}</option>
            </NativeSelect>
            <p id="consent-access-help" className="text-xs text-muted-foreground">
              {oa.consent.accessHelp}
            </p>
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">{oa.consent.readOnlyAsked}</p>
        )}

        <fieldset className="grid min-w-0 gap-2" aria-describedby="consent-spaces-help">
          <legend className="mb-1 text-sm font-medium">{oa.consent.spaces}</legend>
          {info.spaces.length === 0 ? (
            <p className="text-sm text-muted-foreground">{oa.consent.noSpaces}</p>
          ) : (
            <ul className="grid min-w-0 gap-1.5">
              {info.spaces.map((s) => (
                <li key={s.id} className="min-w-0">
                  <label className="flex min-w-0 items-start gap-2 text-sm">
                    <input
                      type="checkbox"
                      className="mt-0.5 size-4 shrink-0 accent-[var(--color-primary)]"
                      checked={chosen.has(s.id)}
                      onChange={() => toggle(s.id)}
                    />
                    <span className="min-w-0 break-words">{s.name}</span>
                  </label>
                </li>
              ))}
            </ul>
          )}
          <p id="consent-spaces-help" className="text-xs text-muted-foreground">
            {oa.consent.spacesHelp}
          </p>
          {problem && (
            <p className="text-xs text-destructive" role="alert">
              {problem}
            </p>
          )}
        </fieldset>

        <FormMessage focusRef={messageRef}>{error}</FormMessage>
        {left && <FormMessage tone="info">{oa.consent.going}</FormMessage>}
        <div className="flex flex-wrap items-center gap-3">
          <Button type="submit" disabled={busy !== null || left || info.spaces.length === 0} className="max-sm:w-full">
            {busy === 'allow' ? oa.consent.allowing : oa.consent.allow}
          </Button>
          <Button
            type="button"
            variant="outline"
            disabled={busy !== null || left}
            onClick={() => void decide(false)}
            className="max-sm:w-full"
          >
            {busy === 'deny' ? oa.consent.denying : oa.consent.deny}
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">{oa.consent.signedInAs(info.user.name)}</p>
        <p className="text-xs text-muted-foreground">{oa.consent.manage}</p>
      </form>
    </AuthLayout>
  )
}
