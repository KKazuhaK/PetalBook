import { CircleHelp, GitBranch, Route, ShieldCheck } from 'lucide-react'
import { useNavigate } from 'react-router'
import { useSession } from '@/api/hooks'
import { administers } from '@/lib/access'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { requestOnboarding } from '@/lib/onboarding'
import { adminPath } from '@/lib/urls'
import { to } from '@/i18n/onboard'

// The help menu: where the tour is opened again, and the steps of connecting one's own repository.
export function HelpMenu() {
  const session = useSession().data
  const navigate = useNavigate()
  if (!session?.user) return null
  const accounts = session.mode === 'accounts'
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={<Button variant="ghost" size="icon" aria-label={to.help.label} title={to.help.label} />}
      >
        <CircleHelp className="size-4" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" side="top" className="min-w-56">
        <DropdownMenuItem onClick={() => requestOnboarding({ kind: 'tour' })}>
          <Route />
          {to.help.tour}
        </DropdownMenuItem>
        {accounts && (
          <DropdownMenuItem onClick={() => requestOnboarding({ kind: 'repo' })}>
            <GitBranch />
            {to.help.repo}
          </DropdownMenuItem>
        )}
        {administers(session.user) && accounts && (
          <DropdownMenuItem onClick={() => navigate(adminPath('daily'))}>
            <ShieldCheck />
            {to.help.admin}
          </DropdownMenuItem>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
