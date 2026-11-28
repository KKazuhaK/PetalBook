import { useId, useState } from 'react'
import { toast } from 'sonner'
import { usePutSetting } from '@/api/admin'
import { ApiError } from '@/api/client'
import type { TenantSetting } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { tdom } from '@/i18n/domains'

// The role of a person who joins the organisation by the domain of their address (3.6 d5), automatically or when an
// administrator approves: domains.joinRole, a choice among the roles that exist and are not the administrator's.
// One component for the organisation's console and the site's page of a tenant.
export const JOIN_ROLE_KEY = 'domains.joinRole'

export function JoinRole({ tenant, setting }: { tenant: string; setting: TenantSetting }) {
  const put = usePutSetting()
  const id = useId()
  const [error, setError] = useState('')
  const choices = setting.choices ?? []
  const current = String(setting.effective ?? '')
  const change = (role: string) => {
    setError('')
    put.mutate(
      { tenant, key: JOIN_ROLE_KEY, value: role },
      {
        onSuccess: () => toast.success(tdom.joinRole.saved),
        onError: (e) => setError(e instanceof ApiError ? e.message || tdom.failed : tdom.failed),
      },
    )
  }
  return (
    <SettingsSection title={tdom.joinRole.title} description={tdom.joinRole.help} id={`join-role-${tenant}`}>
      <div className="grid min-w-0 gap-1.5" data-slot="join-role">
        <Label htmlFor={id}>{tdom.joinRole.label}</Label>
        <NativeSelect
          id={id}
          className="w-full sm:max-w-xs"
          value={current}
          disabled={put.isPending}
          onChange={(e) => change(e.target.value)}
        >
          {choices.map((r) => (
            <option key={r} value={r}>
              {tdom.joinRole.roles[r] ?? r}
            </option>
          ))}
        </NativeSelect>
        <FormMessage>{error}</FormMessage>
      </div>
    </SettingsSection>
  )
}
