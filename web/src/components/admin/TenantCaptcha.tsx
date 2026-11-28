import { useState, type FormEvent } from 'react'
import { Save, ShieldCheck, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { adminApi, useDeleteTenantCaptcha, usePutTenantCaptcha, useTenantCaptcha, useTenantSettings } from '@/api/admin'
import { ApiError } from '@/api/client'
import { useSession } from '@/api/hooks'
import type { CaptchaProvider, TenantCaptcha as TenantCaptchaSettings, TenantCaptchaPatch } from '@/api/types'
import { CaptchaWidget } from '@/components/auth/Captcha'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
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
import { NativeSelect } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { t } from '@/i18n'
import { tm } from '@/i18n/mail'
import { useScope } from './TenantScope'

const to = tm.orgCaptcha
const OWN_CAPTCHA = 'features.ownCaptcha'
const PROVIDERS: readonly CaptchaProvider[] = ['image', 'turnstile', 'recaptcha', 'hcaptcha']

const errorText = (e: unknown) => (e instanceof ApiError ? (to.errors[e.code] ?? e.message) : t.auth.failed)

// An organisation's own captcha provider (3.6, docs/api.md "Captcha: GET/PUT /api/admin/tenants/{tenant}/captcha"),
// in its console: the tenant whose scope the console is in, or the administrator's own.
export function OrgCaptcha() {
  const scope = useScope()
  const own = useSession().data?.user?.tenant?.id ?? ''
  return (
    <div className="max-w-3xl">
      <TenantCaptcha tenant={scope ?? own} />
    </div>
  )
}

// One component for the organisation's console and the site's page of a tenant: whether the organisation has the
// feature, its provider and keys, and a way to try them.
export function TenantCaptcha({ tenant }: { tenant: string }) {
  const settings = useTenantSettings(tenant)
  const off = settings.data?.settings.find((s) => s.key === OWN_CAPTCHA)?.effective === false
  if (settings.isPending) return <PageFallback />
  if (settings.isError) return <ErrorState message={to.loadFailed} onRetry={() => void settings.refetch()} />
  return (
    <div className="grid w-full min-w-0 grid-cols-1 gap-8" data-slot="tenant-captcha">
      {off ? (
        <SettingsSection title={to.title}>
          <p className="text-sm text-muted-foreground" data-slot="org-captcha-off">
            {to.notAllowed}
          </p>
        </SettingsSection>
      ) : (
        <Own tenant={tenant} />
      )}
    </div>
  )
}

function Own({ tenant }: { tenant: string }) {
  const captcha = useTenantCaptcha(tenant)
  if (captcha.isPending) return <PageFallback />
  if (captcha.isError)
    return (
      <ErrorState
        message={t.fmt.labelled(to.loadFailed, captcha.error.message)}
        onRetry={() => void captcha.refetch()}
      />
    )
  return (
    <>
      <ProviderForm tenant={tenant} captcha={captcha.data} />
      <TryIt tenant={tenant} captcha={captcha.data} />
    </>
  )
}

function ProviderForm({ tenant, captcha }: { tenant: string; captcha: TenantCaptchaSettings }) {
  const put = usePutTenantCaptcha(tenant)
  const del = useDeleteTenantCaptcha(tenant)
  const [enabled, setEnabled] = useState(captcha.enabled)
  const [provider, setProvider] = useState<CaptchaProvider>(captcha.provider)
  const [siteKey, setSiteKey] = useState(captcha.siteKey)
  const [secret, setSecret] = useState('')
  const [clear, setClear] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [removing, setRemoving] = useState(false)
  const third = provider !== 'image'
  // The keys belong to a provider: another one starts with none.
  const changed = provider !== captcha.provider
  const hasSecret = captcha.hasSecret && !changed

  const pick = (next: CaptchaProvider) => {
    setProvider(next)
    setSiteKey(next === captcha.provider ? captcha.siteKey : '')
    setSecret('')
    setClear(false)
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setSaved(false)
    const patch: TenantCaptchaPatch = { enabled, provider }
    if (third) patch.siteKey = siteKey.trim()
    if (third && clear) patch.secret = ''
    else if (third && secret) patch.secret = secret
    put.mutate(patch, {
      onSuccess: () => {
        setSecret('')
        setClear(false)
        setSaved(true)
      },
      onError: (err) => setError(`${to.saveFailed}: ${errorText(err)}`),
    })
  }
  const remove = () => {
    setRemoving(false)
    del.mutate(undefined, {
      onSuccess: () => {
        setEnabled(false)
        setProvider('image')
        setSiteKey('')
        setSecret('')
        setClear(false)
        setSaved(false)
        toast.success(to.removed)
      },
      onError: (err) => setError(`${to.saveFailed}: ${errorText(err)}`),
    })
  }
  return (
    <SettingsSection title={to.title} id={`org-captcha-${tenant}`}>
      <p className="text-sm text-muted-foreground">{to.help}</p>
      <form className="grid gap-4" onSubmit={submit} noValidate data-slot="org-captcha-form">
        <label className="flex items-start gap-3 text-sm">
          <Switch checked={enabled} onCheckedChange={setEnabled} aria-label={to.enabled} className="mt-0.5" />
          <span>
            {to.enabled}
            <span className="block text-xs text-muted-foreground">{to.enabledHelp}</span>
          </span>
        </label>
        <div className="grid gap-1.5">
          <label className="text-sm font-medium" htmlFor={`org-captcha-provider-${tenant}`}>
            {to.provider}
          </label>
          <NativeSelect
            id={`org-captcha-provider-${tenant}`}
            value={provider}
            onChange={(e) => pick(e.target.value as CaptchaProvider)}
          >
            {PROVIDERS.map((k) => (
              <option key={k} value={k}>
                {to.providerOptions[k]}
              </option>
            ))}
          </NativeSelect>
          <p className="text-xs text-muted-foreground">{to.providerHelp}</p>
        </div>
        {third && (
          <>
            <Field
              label={to.siteKey}
              value={siteKey}
              onChange={(e) => setSiteKey(e.target.value)}
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              maxLength={255}
              help={to.siteKeyHelp}
            />
            <Field
              label={to.secret}
              type="password"
              value={secret}
              onChange={(e) => {
                setSecret(e.target.value)
                setClear(false)
              }}
              autoComplete="new-password"
              placeholder={hasSecret ? to.secretSet : to.secretNone}
              help={hasSecret ? to.secretSet : to.secretNone}
            />
            {hasSecret && (
              <label className="flex items-center gap-2 text-xs text-muted-foreground">
                <input
                  type="checkbox"
                  checked={clear}
                  onChange={(e) => {
                    setClear(e.target.checked)
                    if (e.target.checked) setSecret('')
                  }}
                />
                {to.secretClear}
              </label>
            )}
          </>
        )}
        <FormMessage>{error}</FormMessage>
        <div className="flex flex-wrap items-center gap-3">
          <Button type="submit" disabled={put.isPending}>
            <Save aria-hidden="true" />
            {put.isPending ? to.saving : to.save}
          </Button>
          <Button type="button" variant="outline" disabled={del.isPending} onClick={() => setRemoving(true)}>
            <Trash2 aria-hidden="true" />
            {to.remove}
          </Button>
          <span role="status" className="text-sm text-muted-foreground">
            {saved ? to.saved : ''}
          </span>
        </div>
      </form>
      <p className="text-sm text-muted-foreground" data-slot="org-captcha-state">
        {captcha.ready ? to.state.ready : to.state.notReady}
      </p>
      <AlertDialog open={removing} onOpenChange={setRemoving}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{to.removeTitle}</AlertDialogTitle>
            <AlertDialogDescription>{to.removeBody}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{to.removeCancel}</AlertDialogCancel>
            <AlertDialogAction onClick={remove}>{to.removeConfirm}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsSection>
  )
}

// A token from the provider's own widget, checked with the saved secret. The provider's script runs on this page, so
// the host it reports is the server's public one, as it is on the forms.
function TryIt({ tenant, captcha }: { tenant: string; captcha: TenantCaptchaSettings }) {
  const [token, setToken] = useState('')
  const [resets, setResets] = useState(0)
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<{ tone: 'error' | 'info'; text: string } | null>(null)
  const complete = captcha.siteKey !== '' && captcha.hasSecret
  const send = async (e: FormEvent) => {
    e.preventDefault()
    if (busy || !token) return
    setBusy(true)
    setResult(null)
    try {
      const r = await adminApi.testTenantCaptcha(tenant, token)
      setResult(r.ok ? { tone: 'info', text: to.testOk } : { tone: 'error', text: to.testFailed(r.message ?? '') })
    } catch (err) {
      setResult({ tone: 'error', text: errorText(err) })
    }
    // A token is good once: the widget starts over.
    setToken('')
    setResets((n) => n + 1)
    setBusy(false)
  }
  return (
    <SettingsSection title={to.test}>
      {captcha.provider === 'image' ? (
        <p className="text-sm text-muted-foreground" data-slot="org-captcha-test-note">
          {to.testImage}
        </p>
      ) : !complete ? (
        <p className="text-sm text-muted-foreground" data-slot="org-captcha-test-note">
          {to.testNeedsKeys}
        </p>
      ) : (
        <>
          <p className="text-sm text-muted-foreground">{to.testHelp}</p>
          <form className="grid gap-3" onSubmit={(e) => void send(e)} data-slot="org-captcha-test">
            <CaptchaWidget
              key={`${captcha.provider}:${captcha.siteKey}`}
              provider={captcha.provider as Exclude<CaptchaProvider, 'image'>}
              siteKey={captcha.siteKey}
              resets={resets}
              onToken={setToken}
            />
            <div>
              <Button type="submit" disabled={busy || !token}>
                <ShieldCheck aria-hidden="true" />
                {busy ? to.testSending : to.testSend}
              </Button>
            </div>
          </form>
          {result && <FormMessage tone={result.tone}>{result.text}</FormMessage>}
        </>
      )}
    </SettingsSection>
  )
}
