import { useState } from 'react'
import { Pencil } from 'lucide-react'
import { useSecuritySettings } from '@/api/admin'
import type { Role, User } from '@/api/types'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { t } from '@/i18n'
import { tc } from '@/i18n/console'
import { ts } from '@/i18n/storage'
import { tp } from '@/i18n/people'
import { tsec } from '@/i18n/security'
import { tt } from '@/i18n/tenants'
import { adminKindOf, isAdminAccount } from '@/lib/access'
import { OwnLimits } from '../OwnLimits'
import { ChangePersonEmailDialog, EditPersonDialog, isManaged } from './PersonEditDialogs'
import { EmailList } from '@/components/settings/EmailsSection'
import { tm } from '@/i18n/mail'
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
  const [editing, setEditing] = useState(false)
  const [changingEmail, setChangingEmail] = useState(false)
  // A site administrator is edited by site administrators only; the server refuses anyone else.
  const canEdit = site || adminKindOf(u) !== 'site'
  return (
    <div className="grid min-w-0 gap-8" data-slot="person-profile">
      <SettingsSection title={tc.person.profile.account} id="person-account">
        <dl className="grid gap-3 rounded-lg border p-3 text-sm sm:grid-cols-[8rem_1fr]" data-slot="person-values">
          <dt className="text-muted-foreground">{tp.profile.name}</dt>
          <dd className="flex min-w-0 flex-wrap items-center gap-2">
            <span className="break-words">{u.name}</span>
            {isManaged(u) && <Badge variant="secondary">{tp.admin.managed}</Badge>}
          </dd>
          <dt className="text-muted-foreground">{tp.profile.username}</dt>
          <dd className="min-w-0 break-words">{u.username}</dd>
          <dt className="text-muted-foreground">{tp.profile.email}</dt>
          <dd className="flex min-w-0 flex-wrap items-center gap-2">
            <span className="break-all">{u.email || tp.profile.noEmail}</span>
            {isManaged(u) && u.email && <Badge variant="secondary">{tp.admin.managed}</Badge>}
            {canEdit && (
              <Button type="button" variant="outline" size="sm" disabled={busy} onClick={() => setChangingEmail(true)}>
                {tp.profile.change}
              </Button>
            )}
          </dd>
        </dl>
        {u.emails && u.emails.length > 1 && (
          <div className="grid min-w-0 gap-2" data-slot="person-emails">
            <h3 className="text-sm font-medium">{tm.emails.title}</h3>
            <EmailList emails={u.emails} />
          </div>
        )}
        {canEdit && (
          <div>
            <Button type="button" variant="outline" className="h-10" disabled={busy} onClick={() => setEditing(true)}>
              <Pencil aria-hidden="true" />
              {tp.profile.editProfile}
            </Button>
          </div>
        )}
        <EditPersonDialog u={u} open={editing} onOpenChange={setEditing} />
        <ChangePersonEmailDialog u={u} open={changingEmail} onOpenChange={setChangingEmail} />
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
        {isAdminAccount(u) && site && u.siteAdmin !== undefined && (
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
