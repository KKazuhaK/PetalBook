import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { to } from '@/i18n/onboard'
import { RepoSteps } from './RepoSteps'

// The same steps, alone: from the help menu, and from "step by step" in the notice that says the space has to be connected.
export function RepoGuideDialog({ onClose }: { onClose: () => void }) {
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent
        data-slot="repo-guide"
        className="max-h-svh overflow-y-auto max-sm:top-0 max-sm:left-0 max-sm:h-svh max-sm:max-w-none max-sm:translate-x-0 max-sm:translate-y-0 max-sm:rounded-none sm:max-w-lg"
      >
        <div className="space-y-4 max-sm:pt-8">
          <DialogTitle className="text-lg font-semibold">{to.repo.title}</DialogTitle>
          <DialogDescription className="sr-only">{to.repo.title}</DialogDescription>
          <RepoSteps onNavigate={onClose} />
          <div className="flex justify-end">
            <Button variant="outline" className="h-10 sm:h-8" onClick={onClose}>
              {to.repo.close}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
