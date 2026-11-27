import { GitHubInstallations } from '@/components/settings/GitHubInstallations'
import { useAdminPaths } from './TenantScope'

// The organisation's own GitHub installations, in its console (Storage, GitHub): any of its administrators installs the
// site's App on the organisation's GitHub organisation, sees it and disconnects it. GitHub sends the browser back
// here, in the same scope.
export function OrgGitHub() {
  const { path } = useAdminPaths()
  return (
    <div className="max-w-3xl">
      <GitHubInstallations of="org" returnTo={path('storage', 'github')} />
    </div>
  )
}
