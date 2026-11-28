import { useSession } from '@/api/hooks'
import { DomainsSection } from './DomainsSection'
import { useScope } from './TenantScope'

// The organisation's own email domains, in its console (Settings, Email domains): the tenant whose scope the console
// is in, or the administrator's own.
export function OrgDomains() {
  const scope = useScope()
  const own = useSession().data?.user?.tenant?.id ?? ''
  return (
    <div className="max-w-3xl">
      <DomainsSection tenant={scope ?? own} />
    </div>
  )
}
