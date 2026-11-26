import { useState, type FormEvent } from 'react'
import { Save } from 'lucide-react'
import { useSecuritySettings } from '@/api/admin'
import type { Role, User } from '@/api/types'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { t } from '@/i18n'
import { tc } from '@/i18n/console'
import { ts } from '@/i18n/storage'
import { tsec } from '@/i18n/security'
import { tt } from '@/i18n/tenants'
import { OwnLimits } from '../OwnLimits'
import type { UserPatch } from './useUserChange'

// What an administrator may change about who a person is: their address, their role and their own limits.
export function PersonProfile({
  u,
  busy,
  save,
  site,
}: {
  u: User
  busy: boolean
  save: (patch: UserPatch) => Promise<unknown>
  site: boolean
}) {
  const [email, setEmail] = useState(u.email)
  const [saved, setSaved] = useState(false)
  const saveEmail = async (e: FormEvent) => {
    e.preventDefault()
    setSaved(false)
    // An address set on its own is not verified; saying so is the next switch.
    if (await save({ email: email.trim() })) setSaved(true)
  }
  return (
    <div className="grid min-w-0 gap-8" data-slot="person-profile">
      <SettingsSection title={tc.person.profile.account} id="person-account">
        <form className="grid min-w-0 grid-cols-1 gap-2 sm:flex sm:items-end" onSubmit={(e) => void saveEmail(e)}>
          <div className="grid min-w-0 gap-1.5 sm:max-w-sm sm:flex-1">
            <Label htmlFor={`email-${u.id}`}>{tsec.users.email}</Label>
            <Input
              id={`email-${u.id}`}
              type="email"
              className="h-10"
              value={email}
              onChange={(e) => {
                setEmail(e.target.value)
                setSaved(false)
              }}
              autoComplete="off"
            />
          </div>
          <Button type="submit" variant="outline" className="h-10" disabled={busy || email.trim() === u.email}>
            <Save aria-hidden="true" />
            {tsec.users.saveEmail}
          </Button>
          <span role="status" className="text-xs text-muted-foreground">
            {saved ? tsec.users.emailSaved : ''}
          </span>
        </form>
        <label className="flex min-h-10 min-w-0 items-start gap-3 text-sm">
          <Switch
            checked={u.emailVerified}
            disabled={busy || !u.email}
            aria-label={tsec.users.emailVerified}
            onCheckedChange={(on) => void save({ emailVerified: on })}
            className="mt-0.5"
          />
          <span className="min-w-0">
            {tsec.users.emailVerified}
            <span className="block text-xs break-words text-muted-foreground">{tsec.users.emailVerifiedHelp}</span>
          </span>
        </label>
        <div className="grid max-w-xs gap-1.5">
          <Label htmlFor={`role-${u.id}`}>{tc.person.profile.role}</Label>
          <NativeSelect
            id={`role-${u.id}`}
            className="h-10"
            aria-label={t.fmt.labelled(u.name, t.users.role)}
            value={u.role}
            disabled={busy}
            onChange={(e) => void save({ role: e.target.value as Role })}
          >
            <option value="member">{t.roles.member}</option>
            <option value="admin">{t.roles.admin}</option>
          </NativeSelect>
        </div>
        {u.role === 'admin' && site && u.siteAdmin !== undefined && (
          <label className="flex min-h-10 items-center gap-3 text-sm">
            <Switch
              checked={u.siteAdmin}
              disabled={busy}
              aria-label={tt.siteAdminFor(u.name)}
              onCheckedChange={(on) => void save({ siteAdmin: on })}
            />
            {tt.siteAdminSwitch}
          </label>
        )}
        {site && (
          <label className="flex min-h-10 min-w-0 items-start gap-3 text-sm" title={ts.users.remoteExemptHelp}>
            <Switch
              aria-label={t.fmt.labelled(ts.users.remoteExempt, u.name)}
              checked={u.remoteExempt === true}
              disabled={busy}
              onCheckedChange={(on) => void save({ remoteExempt: on })}
              className="mt-0.5"
            />
            <span className="min-w-0">
              {ts.users.remoteExempt}
              <span className="block text-xs break-words text-muted-foreground">{ts.users.remoteExemptHelp}</span>
            </span>
          </label>
        )}
      </SettingsSection>
      {u.quota && <OwnLimits userId={u.id} own={u.quota} />}
    </div>
  )
}

// The roles a passkey is required of: for them the switch is on and cannot be turned off here. The security settings are
// the server's: only a site administrator may read them.
export function useRequiredRoles(site: boolean): Role[] {
  return useSecuritySettings(site).data?.passkeys.requireRoles ?? []
}
