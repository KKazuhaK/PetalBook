import { useTenantSettings } from '@/api/admin'
import type { FeatureKey } from '@/api/types'
import { useFeature } from '@/lib/features'
import { useScope } from './TenantScope'

// Whether the tenant the console acts in has a feature: false when it is off, true when on, undefined while it is not
// known. A tenant's own administrator, and a site administrator in the whole site, read their own tenant's features
// (GET /api/settings, which takes no scope); a site administrator in a tenant's scope reads that tenant's, from its
// settings, as the server binds a request in the scope to the scoped tenant.
export function useConsoleFeature(key: FeatureKey): boolean | undefined {
  const scope = useScope()
  const own = useFeature(key)
  const scoped = useTenantSettings(scope ?? '', scope !== null)
  if (scope === null) return own
  if (scoped.data === undefined) return undefined
  return scoped.data.settings.find((s) => s.key === key)?.effective !== false
}
