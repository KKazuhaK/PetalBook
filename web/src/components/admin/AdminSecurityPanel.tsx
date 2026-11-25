import { useRef, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { adminApi, useSecuritySettings } from '@/api/admin'
import { ApiError } from '@/api/client'
import { keys } from '@/api/hooks'
import type {
  CaptchaContext,
  CaptchaProvider,
  CaptchaWhen,
  LockoutScope,
  LoginMode,
  SecuritySettings,
} from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { SettingsRow, SettingsRows, SettingsSection } from '@/components/settings/SettingsParts'
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
import { NativeSelect } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { t } from '@/i18n'
import { tsec } from '@/i18n/security'
import {
  buildPatch,
  CONTEXTS,
  draftOf,
  isEmptyPatch,
  LIMIT_FIELDS,
  partOf,
  type Draft,
  type Part,
  type RoleChoice,
} from './securityDraft'

const MODES: readonly LoginMode[] = ['dual', 'sso_first', 'sso_redirect', 'local_only']
const CAPTCHAS: readonly CaptchaProvider[] = ['image', 'turnstile', 'recaptcha', 'hcaptcha']
const SCOPES: readonly LockoutScope[] = ['ip_account', 'ip', 'account']

// The sign-in page of the security section: sign-in, passkeys, captcha, lockout and limits. The providers and the audit
// log are pages of their own. The settings are one form with one Save: only what changed is sent.
export function AdminSecurityPanel() {
  const settings = useSecuritySettings()
  if (settings.isPending) return <PageFallback />
  if (settings.isError)
    return (
      <ErrorState
        message={t.fmt.labelled(tsec.loadFailed, settings.error.message)}
        onRetry={() => void settings.refetch()}
      />
    )
  return (
    <div className="min-w-0 space-y-10">
      <SettingsForm key={JSON.stringify(settings.data)} settings={settings.data} />
    </div>
  )
}

function Notices({ settings }: { settings: SecuritySettings }) {
  if (settings.notices.length === 0) return null
  return (
    <div role="status" data-slot="security-notices" className="min-w-0 space-y-2 rounded-md border p-4 text-sm">
      <h3 className="font-medium">{tsec.notices.title}</h3>
      <ul className="list-disc space-y-1 pl-5 break-words">
        {settings.notices.map((n, i) => (
          <li key={`${n.code}-${i}`}>
            {tsec.noticeFor(Object.hasOwn(tsec.notices, n.code) ? tsec.notices[n.code] : n.message, n.slug, n.field)}
          </li>
        ))}
      </ul>
    </div>
  )
}

// A row's control on a narrow screen fills the row.
const field = 'w-full min-w-0 sm:w-56'

function SettingsForm({ settings }: { settings: SecuritySettings }) {
  const qc = useQueryClient()
  const [draft, setDraft] = useState<Draft>(() => draftOf(settings))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<{ part: Part; message: string } | null>(null)
  const [replacing, setReplacing] = useState(false)
  const [confirmScope, setConfirmScope] = useState(false)
  const messageRef = useRef<HTMLParagraphElement>(null)
  const set = (patch: Partial<Draft>) => {
    setError(null)
    setDraft((d) => ({ ...d, ...patch }))
  }
  const setContext = (c: CaptchaContext, patch: Partial<Draft['contexts'][CaptchaContext]>) =>
    set({ contexts: { ...draft.contexts, [c]: { ...draft.contexts[c], ...patch } } })

  const patch = buildPatch(settings, draft)
  const dirty = !isEmptyPatch(patch)
  const third = draft.provider !== 'image'
  // A secret stored for another provider does not count: changing provider needs a new one.
  const secretStored = settings.captcha.hasSecret && draft.provider === settings.captcha.provider
  const noProvider = !settings.login.ssoAvailable

  const save = async () => {
    if (busy || !dirty) return
    setBusy(true)
    setError(null)
    try {
      const next = await adminApi.putSecurity(patch)
      qc.setQueryData(keys.adminSecurity, next)
      void qc.invalidateQueries({ queryKey: keys.adminAudit('', '') })
      toast.success(tsec.saved)
      // The form is made again from the answer (see `key`).
    } catch (e) {
      const code = e instanceof ApiError ? e.code : 'unknown'
      const known = Object.hasOwn(tsec.errors, code) ? tsec.errors[code] : undefined
      const message = known && code !== 'invalid' ? known : e instanceof ApiError ? e.message : tsec.errors.unknown
      setError({ part: partOf(code, e instanceof ApiError ? e.message : ''), message })
      setTimeout(() => messageRef.current?.focus(), 0)
    }
    setBusy(false)
  }

  const err = (part: Part): ReactNode =>
    error?.part === part ? <FormMessage focusRef={messageRef}>{error.message}</FormMessage> : null
  const num = (value: string, on: (v: string) => void, id: string, label: string) => (
    <Input
      id={id}
      aria-label={label}
      className="w-full min-w-0 sm:w-32"
      type="number"
      inputMode="numeric"
      value={value}
      onChange={(e) => on(e.target.value)}
    />
  )

  return (
    <form
      className="min-w-0 space-y-8"
      onSubmit={(e) => {
        e.preventDefault()
        void save()
      }}
      data-slot="security-form"
    >
      <Notices settings={settings} />

      <SettingsSection title={tsec.login.title} description={tsec.login.help} id="security-login">
        {err('login')}
        <SettingsRows>
          <SettingsRow title={tsec.login.mode} help={tsec.login.modes[draft.mode].help} controlId="security-mode">
            <div className={field}>
              <NativeSelect
                id="security-mode"
                value={draft.mode}
                onChange={(e) => set({ mode: e.target.value as LoginMode })}
              >
                {MODES.map((m) => (
                  <option key={m} value={m}>
                    {tsec.login.modes[m].name}
                  </option>
                ))}
              </NativeSelect>
            </div>
          </SettingsRow>
          <SettingsRow title={tsec.login.ssoOnly} help={tsec.login.ssoOnlyHelp} controlId="security-ssoonly">
            <Switch id="security-ssoonly" checked={draft.ssoOnly} onCheckedChange={(on) => set({ ssoOnly: on })} />
          </SettingsRow>
        </SettingsRows>
        {noProvider && (
          <p
            role="status"
            className="rounded-md border bg-muted/40 px-3 py-2 text-xs break-words text-muted-foreground"
            data-slot="no-provider"
          >
            {tsec.login.noProvider(tsec.login.modes[settings.login.effectiveMode].name)}
            {draft.ssoOnly ? ` ${tsec.login.ssoOnlyNotInEffect}` : ''}
          </p>
        )}
        <p className="text-xs text-muted-foreground">
          {settings.passwordAdmins > 0
            ? tsec.login.passwordAdmins(settings.passwordAdmins)
            : tsec.login.noPasswordAdmins}
        </p>
      </SettingsSection>

      <SettingsSection title={tsec.passkeys.title} description={tsec.passkeys.help} id="security-passkeys">
        {err('passkeys')}
        {!settings.passkeys.available && (
          <p className="rounded-md border bg-muted/40 px-3 py-2 text-xs break-words text-muted-foreground">
            {tsec.passkeys.unavailable}
          </p>
        )}
        <SettingsRows>
          <SettingsRow title={tsec.passkeys.enabled} help={tsec.passkeys.enabledHelp} controlId="security-pk-enabled">
            <Switch
              id="security-pk-enabled"
              checked={draft.pkEnabled}
              onCheckedChange={(on) => set({ pkEnabled: on })}
            />
          </SettingsRow>
          <SettingsRow title={tsec.passkeys.requireRoles} controlId="security-pk-roles">
            <div className={field}>
              <NativeSelect
                id="security-pk-roles"
                value={draft.pkRoles}
                onChange={(e) => set({ pkRoles: e.target.value as RoleChoice })}
              >
                <option value="none">{tsec.passkeys.roles.none}</option>
                <option value="admins">{tsec.passkeys.roles.admins}</option>
                <option value="everyone">{tsec.passkeys.roles.everyone}</option>
              </NativeSelect>
            </div>
          </SettingsRow>
        </SettingsRows>
        <p className="text-xs text-muted-foreground" data-slot="passkeys-required">
          {tsec.passkeys.required(settings.passkeys.required, settings.passkeys.requiredWithout)}
        </p>
      </SettingsSection>

      <SettingsSection title={tsec.captcha.title} description={tsec.captcha.help} id="security-captcha">
        {err('captcha')}
        <SettingsRows>
          <SettingsRow title={tsec.captcha.provider} controlId="security-captcha-provider">
            <div className={field}>
              <NativeSelect
                id="security-captcha-provider"
                value={draft.provider}
                onChange={(e) => {
                  setReplacing(false)
                  const provider = e.target.value as CaptchaProvider
                  // A site key belongs to its provider: another one starts empty, the stored one gets its key back.
                  set({
                    provider,
                    secret: null,
                    siteKey: provider === settings.captcha.provider ? settings.captcha.siteKey : '',
                  })
                }}
              >
                {CAPTCHAS.map((p) => (
                  <option key={p} value={p}>
                    {tsec.captcha.providers[p]}
                  </option>
                ))}
              </NativeSelect>
            </div>
          </SettingsRow>
          {third && (
            <>
              <SettingsRow
                title={tsec.captcha.siteKey}
                help={tsec.captcha.siteKeyHelp}
                controlId="security-sitekey"
                required
              >
                <Input
                  id="security-sitekey"
                  aria-required="true"
                  className={`${field} break-all`}
                  value={draft.siteKey}
                  onChange={(e) => set({ siteKey: e.target.value })}
                  autoComplete="off"
                  spellCheck={false}
                />
              </SettingsRow>
              <SettingsRow
                title={tsec.captcha.secret}
                help={tsec.captcha.secretHelp}
                controlId="security-secret"
                required
              >
                <div className={`${field} grid gap-2`}>
                  {secretStored && !replacing && draft.secret === null ? (
                    <>
                      <p className="text-sm" data-slot="secret-saved">
                        {tsec.captcha.secretSaved} ·{' '}
                        <button
                          type="button"
                          className="underline underline-offset-4"
                          onClick={() => setReplacing(true)}
                        >
                          {tsec.captcha.replace}
                        </button>
                      </p>
                      <Button type="button" variant="outline" size="sm" onClick={() => set({ secret: '' })}>
                        {tsec.captcha.clear}
                      </Button>
                    </>
                  ) : draft.secret === '' && !replacing ? (
                    <p className="text-sm text-muted-foreground">
                      {tsec.providers.secretWillClear} ·{' '}
                      <button
                        type="button"
                        className="underline underline-offset-4"
                        onClick={() => set({ secret: null })}
                      >
                        {tsec.discard}
                      </button>
                    </p>
                  ) : (
                    <Input
                      id="security-secret"
                      aria-required="true"
                      type="password"
                      autoComplete="new-password"
                      placeholder={secretStored ? tsec.captcha.secretNew : tsec.captcha.secretNone}
                      value={draft.secret ?? ''}
                      onChange={(e) => set({ secret: e.target.value === '' ? null : e.target.value })}
                    />
                  )}
                </div>
              </SettingsRow>
            </>
          )}
        </SettingsRows>
        <div className="min-w-0 overflow-x-auto" data-slot="captcha-contexts">
          <h4 className="pb-1 text-xs font-medium text-muted-foreground">{tsec.captcha.contexts}</h4>
          <ul className="divide-y rounded-lg border">
            {CONTEXTS.map((c) => {
              const x = draft.contexts[c]
              return (
                <li
                  key={c}
                  className="grid grid-cols-1 gap-3 px-3 py-3 sm:grid-cols-[minmax(0,1fr)_auto_auto] sm:items-center"
                  data-context={c}
                >
                  <label className="flex items-center gap-2 text-sm font-medium">
                    <Switch
                      checked={x.on}
                      onCheckedChange={(on) => setContext(c, { on })}
                      aria-label={`${tsec.captcha.context[c]}: ${tsec.captcha.on}`}
                    />
                    {tsec.captcha.context[c]}
                  </label>
                  <NativeSelect
                    className="w-full sm:w-44"
                    aria-label={`${tsec.captcha.context[c]}: ${tsec.captcha.when}`}
                    value={x.when}
                    disabled={!x.on}
                    onChange={(e) => setContext(c, { when: e.target.value as CaptchaWhen })}
                  >
                    <option value="always">{tsec.captcha.whens.always}</option>
                    <option value="after_failures">{tsec.captcha.whens.after_failures}</option>
                  </NativeSelect>
                  {x.when === 'after_failures' ? (
                    <Input
                      className="w-full sm:w-24"
                      type="number"
                      inputMode="numeric"
                      aria-label={`${tsec.captcha.context[c]}: ${tsec.captcha.failures}`}
                      disabled={!x.on}
                      value={x.failures}
                      onChange={(e) => setContext(c, { failures: e.target.value })}
                    />
                  ) : (
                    <span className="hidden sm:block sm:w-24" />
                  )}
                </li>
              )
            })}
          </ul>
          <p className="pt-1 text-xs text-muted-foreground">{tsec.captcha.failuresHelp}</p>
        </div>
      </SettingsSection>

      <SettingsSection title={tsec.lockout.title} description={tsec.lockout.help} id="security-lockout">
        {err('lockout')}
        <SettingsRows>
          <SettingsRow title={tsec.lockout.enabled} controlId="security-lock-enabled">
            <Switch
              id="security-lock-enabled"
              checked={draft.lockEnabled}
              onCheckedChange={(on) => set({ lockEnabled: on })}
            />
          </SettingsRow>
          <SettingsRow title={tsec.lockout.minutes} help={tsec.lockout.minutesHelp} controlId="security-lock-minutes">
            {num(draft.lockMinutes, (v) => set({ lockMinutes: v }), 'security-lock-minutes', tsec.lockout.minutes)}
          </SettingsRow>
          <SettingsRow title={tsec.lockout.scope} controlId="security-lock-scope">
            <div className={field}>
              <NativeSelect
                id="security-lock-scope"
                value={draft.scope}
                onChange={(e) => {
                  // The one choice that lets someone else keep a person out asks first.
                  if (e.target.value === 'account' && draft.scope !== 'account') setConfirmScope(true)
                  else set({ scope: e.target.value as LockoutScope })
                }}
              >
                {SCOPES.map((s) => (
                  <option key={s} value={s}>
                    {tsec.lockout.scopes[s]}
                  </option>
                ))}
              </NativeSelect>
            </div>
          </SettingsRow>
        </SettingsRows>
        {draft.scope === 'account' && (
          <p
            role="alert"
            className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs break-words text-destructive"
            data-slot="scope-warning"
          >
            {tsec.lockout.accountWarning}
          </p>
        )}
        <AlertDialog open={confirmScope} onOpenChange={setConfirmScope}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{tsec.lockout.confirmTitle}</AlertDialogTitle>
              <AlertDialogDescription>{tsec.lockout.confirmHelp}</AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>{tsec.lockout.cancel}</AlertDialogCancel>
              <AlertDialogAction
                variant="destructive"
                onClick={() => {
                  set({ scope: 'account' })
                  setConfirmScope(false)
                }}
              >
                {tsec.lockout.confirm}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </SettingsSection>

      <SettingsSection title={tsec.limits.title} description={tsec.limits.help} id="security-limits">
        {err('limits')}
        <SettingsRows>
          {LIMIT_FIELDS.map((f) => (
            <SettingsRow
              key={f}
              title={tsec.limits[f]}
              help={
                f === 'sessionMaxDays'
                  ? tsec.limits.sessionMaxHelp
                  : f === 'perMinute'
                    ? tsec.limits.perMinuteHelp
                    : f === 'maxFailures'
                      ? tsec.limits.maxFailuresHelp
                      : undefined
              }
              controlId={`security-limit-${f}`}
            >
              {num(
                draft.limits[f],
                (v) => set({ limits: { ...draft.limits, [f]: v } }),
                `security-limit-${f}`,
                tsec.limits[f],
              )}
            </SettingsRow>
          ))}
        </SettingsRows>
      </SettingsSection>

      {err('page')}
      <div className="flex flex-wrap items-center gap-3">
        <Button type="submit" disabled={busy || !dirty}>
          {busy ? tsec.saving : tsec.save}
        </Button>
        {dirty && (
          <>
            <Button
              type="button"
              variant="ghost"
              onClick={() => {
                setDraft(draftOf(settings))
                setReplacing(false)
                setError(null)
              }}
            >
              {tsec.discard}
            </Button>
            <span role="status" className="text-sm text-muted-foreground">
              {tsec.unsaved}
            </span>
          </>
        )}
      </div>
    </form>
  )
}
