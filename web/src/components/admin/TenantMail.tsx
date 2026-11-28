import { useState, type FormEvent } from 'react'
import { Save, Send, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import {
  adminApi,
  useDeleteTenantMail,
  usePutSetting,
  usePutTenantMail,
  useTenantMail,
  useTenantSettings,
} from '@/api/admin'
import { ApiError } from '@/api/client'
import { useSession } from '@/api/hooks'
import type { TenantMail as TenantMailSettings, TenantMailPatch, TenantMailSecurity, TenantSetting } from '@/api/types'
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
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'
import { tm } from '@/i18n/mail'
import { mailLang } from '@/pages/auth/MailPages'
import { useScope } from './TenantScope'

const to = tm.org
const OWN_SENDER = 'mail.ownSender'
const USE_SITE = 'mail.useSite'

const errorText = (e: unknown) => (e instanceof ApiError ? (to.errors[e.code] ?? e.message) : t.auth.failed)

// An organisation's own mail server (3.6, docs/api.md "Mail: GET/PUT /api/admin/tenants/{tenant}/mail"), in its
// console: the tenant whose scope the console is in, or the administrator's own.
export function OrgMail() {
  const scope = useScope()
  const own = useSession().data?.user?.tenant?.id ?? ''
  return (
    <div className="max-w-3xl">
      <TenantMail tenant={scope ?? own} />
    </div>
  )
}

// One component for the organisation's console and the site's page of a tenant: whether the organisation has the
// feature, its server, a test message, and the switch that lends it the site's mail while it has none.
export function TenantMail({ tenant }: { tenant: string }) {
  const settings = useTenantSettings(tenant)
  const off = settings.data?.settings.find((s) => s.key === OWN_SENDER)?.effective === false
  const useSite = settings.data?.settings.find((s) => s.key === USE_SITE)
  if (settings.isPending) return <PageFallback />
  if (settings.isError) return <ErrorState message={to.loadFailed} onRetry={() => void settings.refetch()} />
  return (
    <div className="grid w-full min-w-0 grid-cols-1 gap-8" data-slot="tenant-mail">
      {off ? (
        <SettingsSection title={to.title}>
          <p className="text-sm text-muted-foreground" data-slot="org-mail-off">
            {to.notAllowed}
          </p>
        </SettingsSection>
      ) : (
        <OwnServer tenant={tenant} />
      )}
      {useSite && <UseSite tenant={tenant} setting={useSite} />}
    </div>
  )
}

function OwnServer({ tenant }: { tenant: string }) {
  const mail = useTenantMail(tenant)
  if (mail.isPending) return <PageFallback />
  if (mail.isError)
    return (
      <ErrorState message={t.fmt.labelled(to.loadFailed, mail.error.message)} onRetry={() => void mail.refetch()} />
    )
  return (
    <>
      <ServerForm tenant={tenant} mail={mail.data} />
      <TestMessage tenant={tenant} />
    </>
  )
}

function ServerForm({ tenant, mail }: { tenant: string; mail: TenantMailSettings }) {
  const put = usePutTenantMail(tenant)
  const del = useDeleteTenantMail(tenant)
  const [enabled, setEnabled] = useState(mail.enabled)
  const [host, setHost] = useState(mail.host)
  const [port, setPort] = useState(String(mail.port))
  const [security, setSecurity] = useState<TenantMailSecurity>(mail.security)
  const [username, setUsername] = useState(mail.username)
  const [password, setPassword] = useState('')
  const [clear, setClear] = useState(false)
  const [from, setFrom] = useState(mail.from)
  const [fromName, setFromName] = useState(mail.fromName)
  const [replyTo, setReplyTo] = useState(mail.replyTo)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [removing, setRemoving] = useState(false)

  const submit = (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setSaved(false)
    const patch: TenantMailPatch = {
      enabled,
      host: host.trim(),
      port: Number(port),
      security,
      username: username.trim(),
      from: from.trim(),
      fromName: fromName.trim(),
      replyTo: replyTo.trim(),
    }
    if (clear) patch.password = ''
    else if (password) patch.password = password
    put.mutate(patch, {
      onSuccess: () => {
        setPassword('')
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
        setHost('')
        setPort('587')
        setSecurity('starttls')
        setUsername('')
        setPassword('')
        setClear(false)
        setFrom('')
        setFromName('')
        setReplyTo('')
        setSaved(false)
        toast.success(to.removed)
      },
      onError: (err) => setError(`${to.saveFailed}: ${errorText(err)}`),
    })
  }
  const st = mail.status
  return (
    <SettingsSection title={to.title} id={`org-mail-${tenant}`}>
      <p className="text-sm text-muted-foreground">{to.help}</p>
      <form className="grid gap-4" onSubmit={submit} noValidate data-slot="org-mail-form">
        <label className="flex items-start gap-3 text-sm">
          <Switch checked={enabled} onCheckedChange={setEnabled} aria-label={to.enabled} className="mt-0.5" />
          <span>
            {to.enabled}
            <span className="block text-xs text-muted-foreground">{to.enabledHelp}</span>
          </span>
        </label>
        <div className="grid gap-4 sm:grid-cols-[1fr_8rem]">
          <Field
            label={to.host}
            value={host}
            onChange={(e) => setHost(e.target.value)}
            autoCapitalize="none"
            spellCheck={false}
          />
          <Field
            label={to.port}
            type="number"
            min={1}
            max={65535}
            value={port}
            onChange={(e) => setPort(e.target.value)}
            help={to.portHelp}
          />
        </div>
        <div className="grid gap-1.5">
          <label className="text-sm font-medium" htmlFor={`org-mail-security-${tenant}`}>
            {to.security}
          </label>
          <NativeSelect
            id={`org-mail-security-${tenant}`}
            value={security}
            onChange={(e) => setSecurity(e.target.value as TenantMailSecurity)}
          >
            {(['starttls', 'tls'] as const).map((k) => (
              <option key={k} value={k}>
                {to.securityOptions[k]}
              </option>
            ))}
          </NativeSelect>
          <p className="text-xs text-muted-foreground">{to.securityHelp}</p>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label={to.username}
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
          />
          <Field
            label={to.password}
            type="password"
            value={password}
            onChange={(e) => {
              setPassword(e.target.value)
              setClear(false)
            }}
            autoComplete="new-password"
            placeholder={mail.hasPassword ? to.passwordSet : to.passwordNone}
            help={mail.hasPassword ? to.passwordSet : to.passwordNone}
          />
        </div>
        {mail.hasPassword && (
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            <input
              type="checkbox"
              checked={clear}
              onChange={(e) => {
                setClear(e.target.checked)
                if (e.target.checked) setPassword('')
              }}
            />
            {to.passwordClear}
          </label>
        )}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label={to.from}
            type="email"
            value={from}
            onChange={(e) => setFrom(e.target.value)}
            autoCapitalize="none"
            help={to.fromHelp}
          />
          <Field label={to.fromName} value={fromName} onChange={(e) => setFromName(e.target.value)} maxLength={60} />
        </div>
        <Field
          label={to.replyTo}
          type="email"
          value={replyTo}
          onChange={(e) => setReplyTo(e.target.value)}
          autoCapitalize="none"
        />
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
      <div className="grid gap-1 text-sm" data-slot="org-mail-status">
        <div>
          <span className="font-medium">{to.status}: </span>
          {st.checked === null
            ? to.statusNever
            : st.ok
              ? to.statusOk(absoluteTime(st.checked))
              : to.statusFailed(absoluteTime(st.checked), st.message)}
        </div>
        <div className="text-muted-foreground" data-slot="org-mail-sender">
          {to.sender[mail.sender] ?? ''}
        </div>
      </div>
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

function TestMessage({ tenant }: { tenant: string }) {
  const [address, setAddress] = useState('')
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<{ tone: 'error' | 'info'; text: string } | null>(null)
  const send = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setResult(null)
    try {
      const r = await adminApi.testTenantMail(tenant, address.trim(), mailLang())
      setResult(r.ok ? { tone: 'info', text: to.testOk } : { tone: 'error', text: to.testFailed(r.message ?? '') })
    } catch (err) {
      setResult({ tone: 'error', text: errorText(err) })
    }
    setBusy(false)
  }
  return (
    <SettingsSection title={to.test}>
      <p className="text-sm text-muted-foreground">{to.testHelp}</p>
      <form className="flex flex-wrap items-end gap-3" onSubmit={(e) => void send(e)} data-slot="org-mail-test">
        <div className="min-w-0 flex-1 basis-56">
          <Field
            label={to.testTo}
            type="email"
            value={address}
            onChange={(e) => setAddress(e.target.value)}
            autoComplete="off"
            autoCapitalize="none"
            required
          />
        </div>
        <Button type="submit" disabled={busy || !address.trim()}>
          <Send aria-hidden="true" />
          {busy ? to.testSending : to.testSend}
        </Button>
      </form>
      {result && <FormMessage tone={result.tone}>{result.text}</FormMessage>}
    </SettingsSection>
  )
}

// mail.useSite as the organisation is allowed to change it: a lock forces the site's value, and a restriction lets it
// be turned off and not on again (a boolean has nothing else to restrict). The site's note is shown with the rule.
function UseSite({ tenant, setting }: { tenant: string; setting: TenantSetting }) {
  const put = usePutSetting()
  const [error, setError] = useState('')
  const on = setting.effective !== false
  const rule = setting.reason
  const locked = rule?.mode === 'lock'
  const onlyOff = rule?.mode === 'restrict'
  const change = (next: boolean) => {
    setError('')
    put.mutate(
      { tenant, key: USE_SITE, value: next },
      {
        onSuccess: () => toast.success(to.useSite.saved),
        onError: (e) => setError(e instanceof ApiError ? e.message || to.useSite.failed : to.useSite.failed),
      },
    )
  }
  const why = locked ? (on ? to.useSite.lockedOn : to.useSite.lockedOff) : onlyOff ? to.useSite.onlyOff : ''
  return (
    <SettingsSection title={to.useSite.title} id={`org-mail-usesite-${tenant}`}>
      <div className="grid gap-2" data-slot="org-mail-usesite">
        <label className="flex items-start gap-3 text-sm">
          <Switch
            checked={on}
            onCheckedChange={change}
            disabled={put.isPending || locked || (onlyOff && !on)}
            aria-label={to.useSite.label}
            className="mt-0.5"
          />
          <span>
            {to.useSite.label}
            <span className="block text-xs text-muted-foreground">{to.useSite.help}</span>
          </span>
        </label>
        {why && (
          <p className="text-xs text-muted-foreground" data-slot="org-mail-usesite-rule">
            {why}
            {rule?.note ? ` ${rule.note}` : ''}
          </p>
        )}
        <FormMessage>{error}</FormMessage>
      </div>
    </SettingsSection>
  )
}
