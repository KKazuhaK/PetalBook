import { useState } from 'react'
import { useNavigate } from 'react-router'
import { CheckCircle2, FilePlus2, GitBranch, PenLine, Search, ShieldCheck, Sparkles, Star } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { useQuickCapture } from '@/components/layout/QuickCapture'
import { useSearchDialog } from '@/components/search/SearchDialog'
import { tourSteps, type TourStep } from '@/lib/onboarding'
import { adminPath } from '@/lib/urls'
import { to } from '@/i18n/onboard'
import { RepoSteps } from './RepoSteps'

const ICONS: Record<TourStep, typeof Sparkles> = {
  welcome: Sparkles,
  page: FilePlus2,
  editor: PenLine,
  favorites: Star,
  search: Search,
  repo: GitBranch,
  admin: ShieldCheck,
  done: CheckCircle2,
}

// The short tour of a first sign-in: a dialog of steps (full-screen on a phone), each with a line or two and, where
// there is something to try, a button that does it. It can be skipped at any step; however it ends, `onClose` says
// whether it was finished, and the caller remembers that.
export function Tour({
  admin,
  repo,
  onClose,
}: {
  admin: boolean
  repo: boolean
  onClose: (finished: boolean) => void
}) {
  const steps = tourSteps({ admin, repo })
  const [i, setI] = useState(0)
  const navigate = useNavigate()
  const capture = useQuickCapture()
  const search = useSearchDialog()
  const id = steps[i]
  const Icon = ICONS[id]
  const last = i === steps.length - 1
  const text = to.tour.steps[id]
  // An action leaves the tour (it shows the thing itself): the tour counts as seen, and the help menu opens it again.
  const leave = (then: () => void) => {
    onClose(false)
    then()
  }
  const action: { label: string; run: () => void } | null =
    id === 'page' && 'action' in text
      ? { label: text.action, run: () => leave(() => capture.open()) }
      : id === 'search' && 'action' in text
        ? { label: text.action, run: () => leave(() => search.open()) }
        : id === 'admin' && 'action' in text
          ? { label: text.action, run: () => leave(() => navigate(adminPath('daily'))) }
          : null
  return (
    <Dialog open onOpenChange={(o) => !o && onClose(false)}>
      <DialogContent
        showCloseButton
        data-slot="tour"
        className="max-sm:top-0 max-sm:left-0 max-sm:h-svh max-sm:max-w-none max-sm:translate-x-0 max-sm:translate-y-0 max-sm:rounded-none sm:max-w-md"
      >
        <div className="flex h-full flex-col gap-4 max-sm:pt-8">
          <p className="text-xs text-muted-foreground" data-slot="tour-progress">
            {to.tour.step(i + 1, steps.length)}
          </p>
          <div key={id} className="flex flex-1 flex-col gap-3 animate-in duration-200 fade-in-0 slide-in-from-right-2">
            <span aria-hidden className="flex size-10 items-center justify-center rounded-xl bg-brand/10 text-brand">
              <Icon className="size-5" />
            </span>
            <DialogTitle className="text-lg font-semibold">{text.title}</DialogTitle>
            {id === 'repo' ? (
              <>
                <DialogDescription>{to.tour.steps.repo.bodyAsked}</DialogDescription>
                <div className="max-h-[50svh] overflow-y-auto overscroll-contain">
                  <RepoSteps onNavigate={() => onClose(false)} />
                </div>
              </>
            ) : (
              <DialogDescription className="text-sm leading-relaxed">
                {'body' in text ? text.body : ''}
              </DialogDescription>
            )}
            {action && (
              <div>
                <Button variant="outline" className="h-10 sm:h-8" onClick={action.run}>
                  {action.label}
                </Button>
              </div>
            )}
          </div>
          <div aria-hidden className="flex gap-1.5">
            {steps.map((s, n) => (
              <span
                key={s}
                className={`h-1.5 rounded-full transition-all duration-300 ${n === i ? 'w-6 bg-brand' : n < i ? 'w-1.5 bg-brand/50' : 'w-1.5 bg-border'}`}
              />
            ))}
          </div>
          <p className="text-xs text-muted-foreground">{to.tour.skipHelp}</p>
          <div className="flex items-center justify-between gap-2">
            <Button variant="ghost" className="h-10 sm:h-8" onClick={() => onClose(false)}>
              {to.tour.skip}
            </Button>
            <div className="flex gap-2">
              {i > 0 && (
                <Button variant="outline" className="h-10 sm:h-8" onClick={() => setI(i - 1)}>
                  {to.tour.back}
                </Button>
              )}
              <Button className="h-10 sm:h-8" onClick={() => (last ? onClose(true) : setI(i + 1))}>
                {last ? to.tour.finish : to.tour.next}
              </Button>
            </div>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
