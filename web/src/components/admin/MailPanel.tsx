import { useState, type FormEvent } from 'react'
import { Save, Send } from 'lucide-react'
import { adminApi, useMail, usePutMail } from '@/api/admin'
import { ApiError } from '@/api/client'
import type { MailPatch, MailSecurity, MailSettings } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { Button } from '@/components/ui/button'
import { NativeSelect } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'
import { tm } from '@/i18n/mail'
import { mailLang } from '@/pages/auth/MailPages'

// The server's outgoing mail (docs/api.md, "Mail"): the SMTP settings, the state of the queue, and a test message to
// an address typed here. Nothing is sent until mail is enabled.
export function MailPanel() {
  const mail = useMail()
  if (mail.isPending) return <PageFallback />
  if (mail.isError)
    return (
      <ErrorState
        message={t.fmt.labelled(tm.admin.loadFailed, mail.error.message)}
        onRetry={() => void mail.refetch()}
      />
    )
  return (
    <div className="grid w-full min-w-0 grid-cols-1 gap-8">
      <MailForm mail={mail.data} />
      <TestMessage />
    </div>
  )
}

const errorText = (e: unknown) => (e instanceof ApiError ? (tm.admin.errors[e.code] ?? e.message) : t.auth.failed)

function MailForm({ mail }: { mail: MailSettings }) {
  const put = usePutMail()
  const [enabled, setEnabled] = useState(mail.enabled)
  const [host, setHost] = useState(mail.host)
  const [port, setPort] = useState(String(mail.port))
  const [security, setSecurity] = useState<MailSecurity>(mail.security)
  const [username, setUsername] = useState(mail.username)
  const [password, setPassword] = useState('')
  const [clear, setClear] = useState(false)
  const [from, setFrom] = useState(mail.from)
  const [fromName, setFromName] = useState(mail.fromName)
  const [replyTo, setReplyTo] = useState(mail.replyTo)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)

  const submit = (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setSaved(false)
    const patch: MailPatch = {
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
      onError: (err) => setError(`${tm.admin.saveFailed}: ${errorText(err)}`),
    })
  }
  const st = mail.status
  return (
    <SettingsSection title={tm.admin.title}>
      <p className="text-sm text-muted-foreground">{tm.admin.help}</p>
      <form className="grid gap-4" onSubmit={submit} noValidate>
        <label className="flex items-start gap-3 text-sm">
          <Switch checked={enabled} onCheckedChange={setEnabled} aria-label={tm.admin.enabled} className="mt-0.5" />
          <span>
            {tm.admin.enabled}
            <span className="block text-xs text-muted-foreground">{tm.admin.enabledHelp}</span>
          </span>
        </label>
        <div className="grid gap-4 sm:grid-cols-[1fr_8rem]">
          <Field
            label={tm.admin.host}
            value={host}
            onChange={(e) => setHost(e.target.value)}
            autoCapitalize="none"
            spellCheck={false}
          />
          <Field
            label={tm.admin.port}
            type="number"
            min={1}
            max={65535}
            value={port}
            onChange={(e) => setPort(e.target.value)}
          />
        </div>
        <div className="grid gap-1.5">
          <label className="text-sm font-medium" htmlFor="mail-security">
            {tm.admin.security}
          </label>
          <NativeSelect
            id="mail-security"
            value={security}
            onChange={(e) => setSecurity(e.target.value as MailSecurity)}
          >
            {(['starttls', 'tls', 'none'] as const).map((k) => (
              <option key={k} value={k}>
                {tm.admin.securityOptions[k]}
              </option>
            ))}
          </NativeSelect>
          <p className="text-xs text-muted-foreground">{tm.admin.securityHelp}</p>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label={tm.admin.username}
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
          />
          <Field
            label={tm.admin.password}
            type="password"
            value={password}
            onChange={(e) => {
              setPassword(e.target.value)
              setClear(false)
            }}
            autoComplete="new-password"
            placeholder={mail.hasPassword ? tm.admin.passwordSet : tm.admin.passwordNone}
            help={mail.hasPassword ? undefined : tm.admin.passwordNone}
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
            {tm.admin.passwordClear}
          </label>
        )}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label={tm.admin.from}
            type="email"
            value={from}
            onChange={(e) => setFrom(e.target.value)}
            autoCapitalize="none"
          />
          <Field
            label={tm.admin.fromName}
            value={fromName}
            onChange={(e) => setFromName(e.target.value)}
            maxLength={60}
          />
        </div>
        <Field
          label={tm.admin.replyTo}
          type="email"
          value={replyTo}
          onChange={(e) => setReplyTo(e.target.value)}
          autoCapitalize="none"
        />
        <FormMessage>{error}</FormMessage>
        <div className="flex items-center gap-3">
          <Button type="submit" disabled={put.isPending}>
            <Save aria-hidden="true" />
            {put.isPending ? tm.admin.saving : tm.admin.save}
          </Button>
          <span role="status" className="text-sm text-muted-foreground">
            {saved ? tm.admin.saved : ''}
          </span>
        </div>
      </form>
      <div className="grid gap-1 text-sm" data-slot="mail-status">
        <div>
          <span className="font-medium">{tm.admin.status}: </span>
          {st.checked === null
            ? tm.admin.statusNever
            : st.ok
              ? tm.admin.statusOk(absoluteTime(st.checked))
              : tm.admin.statusFailed(absoluteTime(st.checked), st.message)}
        </div>
        <div className="text-muted-foreground">
          {tm.admin.queue(mail.queue.waiting, mail.queue.sentLastHour, mail.queue.failedLastHour)}
        </div>
        <div className="text-xs text-muted-foreground">{tm.admin.queueHelp}</div>
      </div>
    </SettingsSection>
  )
}

function TestMessage() {
  const [to, setTo] = useState('')
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<{ tone: 'error' | 'info'; text: string } | null>(null)
  const send = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setResult(null)
    try {
      const r = await adminApi.testMail(to.trim(), mailLang())
      setResult(
        r.ok ? { tone: 'info', text: tm.admin.testOk } : { tone: 'error', text: tm.admin.testFailed(r.message ?? '') },
      )
    } catch (err) {
      setResult({ tone: 'error', text: errorText(err) })
    }
    setBusy(false)
  }
  return (
    <SettingsSection title={tm.admin.test}>
      <p className="text-sm text-muted-foreground">{tm.admin.testHelp}</p>
      <form className="flex flex-wrap items-end gap-3" onSubmit={(e) => void send(e)}>
        <div className="min-w-0 flex-1 basis-56">
          <Field
            label={tm.admin.testTo}
            type="email"
            value={to}
            onChange={(e) => setTo(e.target.value)}
            autoComplete="off"
            autoCapitalize="none"
            required
          />
        </div>
        <Button type="submit" disabled={busy || !to.trim()}>
          <Send aria-hidden="true" />
          {busy ? tm.admin.testSending : tm.admin.testSend}
        </Button>
      </form>
      {result && <FormMessage tone={result.tone}>{result.text}</FormMessage>}
    </SettingsSection>
  )
}
