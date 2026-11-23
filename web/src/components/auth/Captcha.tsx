import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { api, ApiError } from '@/api/client'
import type { CaptchaAnswer, CaptchaChallenge, CaptchaContext, CaptchaProvider } from '@/api/types'
import { Field } from '@/components/auth/Field'
import { Button } from '@/components/ui/button'
import { ts } from '@/i18n/signin'

// The captcha of a form (docs/api.md, "Captcha"): whether one is needed is asked of the server, and the answer
// is what the form sends with its request. The self-hosted image is an `id` and a typed `answer`; the
// third-party widgets (Turnstile, reCAPTCHA, hCaptcha) hand back a `token`.

export interface CaptchaState {
  challenge: CaptchaChallenge | null
  required: boolean
  loading: boolean
  loadError: string
  answer: string
  setAnswer: (v: string) => void
  setToken: (v: string) => void
  // Bumped when a widget has to start over.
  resets: number
  // Asks again (a new image, a widget reset). `account` is the username a login is for; without one, the last one given is kept.
  refresh: (account?: string) => Promise<void>
  // What to send with the request, or undefined when none is needed or none is ready.
  value: () => CaptchaAnswer | undefined
  // True when a captcha is required and there is nothing to send yet.
  missing: boolean
}

export function useCaptcha(ctx: CaptchaContext, { auto = true }: { auto?: boolean } = {}): CaptchaState {
  const [challenge, setChallenge] = useState<CaptchaChallenge | null>(null)
  const [loading, setLoading] = useState(auto)
  const [loadError, setLoadError] = useState('')
  const [answer, setAnswer] = useState('')
  const [token, setTokenState] = useState('')
  const [resets, setResets] = useState(0)
  const seq = useRef(0)
  // The username last asked about, so "another image" asks about the same one.
  const lastAccount = useRef<string | undefined>(undefined)

  // Asks the server; only the latest question's answer is kept.
  const run = useCallback(
    (account?: string) => {
      const mine = ++seq.current
      return api.captcha(ctx, account).then(
        (c) => {
          if (mine !== seq.current) return
          setChallenge(c)
          setAnswer('')
          setTokenState('')
          setResets((n) => n + 1)
          setLoading(false)
        },
        (e: unknown) => {
          if (mine !== seq.current) return
          setLoadError(e instanceof ApiError && e.code === 'rate_limited' ? e.message : ts.captcha.loadFailed)
          setLoading(false)
        },
      )
    },
    [ctx],
  )
  const refresh = useCallback(
    (account?: string) => {
      if (account !== undefined) lastAccount.current = account
      setLoading(true)
      setLoadError('')
      return run(lastAccount.current)
    },
    [run],
  )

  useEffect(() => {
    if (auto) void run()
  }, [auto, run])

  const required = challenge?.required === true
  const value = (): CaptchaAnswer | undefined => {
    if (!required) return undefined
    if (challenge?.provider === 'image') return challenge.id && answer.trim() ? { id: challenge.id, answer: answer.trim() } : undefined
    return token ? { token } : undefined
  }
  return { challenge, required, loading, loadError, answer, setAnswer, setToken: setTokenState, resets, refresh, value, missing: required && value() === undefined }
}

export function CaptchaField({ captcha, disabled }: { captcha: CaptchaState; disabled?: boolean }) {
  const c = captcha.challenge
  if (captcha.loadError && !c?.required) {
    return (
      <p role="status" className="text-xs text-muted-foreground">
        {captcha.loadError}{' '}
        <button type="button" className="underline underline-offset-4" onClick={() => void captcha.refresh()}>
          {ts.captcha.refresh}
        </button>
      </p>
    )
  }
  if (!c?.required) return null
  if (c.provider === 'image') {
    return (
      <div className="grid gap-2" data-slot="captcha">
        <div className="flex min-w-0 items-center gap-2">
          {c.image && <img src={c.image} alt={ts.captcha.imageAlt} className="h-12 max-w-[70%] rounded-md border bg-white" />}
          <Button type="button" variant="outline" size="sm" disabled={captcha.loading || disabled} onClick={() => void captcha.refresh()}>
            {captcha.loading ? ts.captcha.refreshing : ts.captcha.refresh}
          </Button>
        </div>
        <Field label={ts.captcha.answer} value={captcha.answer} onChange={(e) => captcha.setAnswer(e.target.value)} autoComplete="off" autoCapitalize="none" autoCorrect="off" spellCheck={false} disabled={disabled} />
      </div>
    )
  }
  return <Widget provider={c.provider!} siteKey={c.siteKey ?? ''} resets={captcha.resets} onToken={captcha.setToken} />
}

// --- the third-party widgets --------------------------------------------------------------

interface WidgetApi {
  render: (el: HTMLElement, options: Record<string, unknown>) => string | number
  reset?: (id?: string | number) => void
  remove?: (id?: string | number) => void
  ready?: (fn: () => void) => void
}
const SCRIPTS: Record<Exclude<CaptchaProvider, 'image'>, { src: string; global: 'turnstile' | 'grecaptcha' | 'hcaptcha' }> = {
  turnstile: { src: 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit', global: 'turnstile' },
  recaptcha: { src: 'https://www.google.com/recaptcha/api.js?render=explicit', global: 'grecaptcha' },
  hcaptcha: { src: 'https://js.hcaptcha.com/1/api.js?render=explicit', global: 'hcaptcha' },
}
const loading = new Map<string, Promise<void>>()
function loadScript(src: string): Promise<void> {
  const known = loading.get(src)
  if (known) return known
  const p = new Promise<void>((resolve, reject) => {
    const el = document.createElement('script')
    el.src = src
    el.async = true
    el.defer = true
    el.onload = () => resolve()
    el.onerror = () => {
      loading.delete(src)
      reject(new Error('script'))
    }
    document.head.appendChild(el)
  })
  loading.set(src, p)
  return p
}
const globalApi = (name: string) => (window as unknown as Record<string, WidgetApi | undefined>)[name]

function Widget({ provider, siteKey, resets, onToken }: { provider: Exclude<CaptchaProvider, 'image'>; siteKey: string; resets: number; onToken: (t: string) => void }) {
  const host = useRef<HTMLDivElement>(null)
  const widget = useRef<string | number | null>(null)
  const [failed, setFailed] = useState(false)
  const id = useId()
  const spec = SCRIPTS[provider]

  useEffect(() => {
    let live = true
    loadScript(spec.src)
      .then(
        () =>
          new Promise<WidgetApi>((resolve, reject) => {
            const api = globalApi(spec.global)
            if (!api) return reject(new Error('missing'))
            // reCAPTCHA can only render once its own `ready` has run.
            if (api.ready) api.ready(() => resolve(api))
            else resolve(api)
          }),
      )
      .then((api) => {
        if (!live || !host.current) return
        setFailed(false)
        host.current.replaceChildren()
        widget.current = api.render(host.current, { sitekey: siteKey, callback: (t: string) => onToken(t), 'expired-callback': () => onToken(''), 'error-callback': () => onToken('') })
      })
      .catch(() => live && setFailed(true))
    return () => {
      live = false
      const api = globalApi(spec.global)
      if (widget.current !== null) {
        try {
          api?.remove?.(widget.current)
        } catch {
          // The widget is already gone.
        }
        widget.current = null
      }
    }
  }, [spec, siteKey, onToken])

  // After a refusal the widget starts over, and what it handed back is no longer good.
  useEffect(() => {
    if (resets === 0 || widget.current === null) return
    try {
      globalApi(spec.global)?.reset?.(widget.current)
    } catch {
      // Nothing to reset.
    }
  }, [resets, spec])

  return (
    <div className="grid gap-1.5" data-slot="captcha" data-provider={provider}>
      <span id={id} className="text-sm font-medium">
        {ts.captcha.widgetLabel}
      </span>
      <div ref={host} role="group" aria-labelledby={id} className="min-h-[65px] max-w-full overflow-x-auto" />
      {failed && <p className="text-xs text-destructive">{ts.captcha.widgetLoadFailed}</p>}
    </div>
  )
}
