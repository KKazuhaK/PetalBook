import { useTenantDomains, useTenantProviders, useTenantSettings } from '@/api/admin'
import { useSession } from '@/api/hooks'
import { PageFallback } from '@/components/common'
import { tsec } from '@/i18n/security'
import { DomainsSection } from './DomainsSection'
import { ProvidersSection } from './ProvidersSection'
import { useScope } from './TenantScope'

// The organisation's sign-in, in its console (Settings, Sign-in): its own single sign-on (3.6 d3) and its email
// domains (d2), for the tenant whose scope the console is in, or the administrator's own.
export function OrgDomains() {
  const scope = useScope()
  const own = useSession().data?.user?.tenant?.id ?? ''
  return (
    <div className="max-w-3xl">
      <TenantSignin tenant={scope ?? own} />
    </div>
  )
}

// A tenant's sign-in: its providers first, then its domains, whose verified ones may be routed to a provider. One
// component for the organisation's console and the site's page of a tenant.
export function TenantSignin({ tenant }: { tenant: string }) {
  const settings = useTenantSettings(tenant)
  const feature = (key: string) => settings.data?.settings.find((s) => s.key === key)?.effective
  const ssoOff = feature('features.ownSso') === false
  // Both share their cache with the sections' own: no request of their own.
  // The list also carries the site's SSO offered to the tenant, which its domains may use without its own (3.6 d4).
  const providers = useTenantProviders(tenant, settings.isSuccess)
  const domains = useTenantDomains(tenant)
  const verified = domains.data?.domains.some((d) => d.state === 'verified' || d.state === 'missing') ?? true
  return (
    <div className="grid min-w-0 gap-8" data-slot="tenant-signin">
      {settings.isPending ? (
        <PageFallback />
      ) : (
        <ProvidersSection tenant={tenant} off={ssoOff}>
          {!verified && (
            <p className="text-xs text-muted-foreground" data-slot="org-need-domain">
              {tsec.providers.org.needDomain}
            </p>
          )}
        </ProvidersSection>
      )}
      <DomainsSection
        tenant={tenant}
        providers={ssoOff ? undefined : providers.data?.providers}
        offered={providers.data?.offered}
      />
    </div>
  )
}
