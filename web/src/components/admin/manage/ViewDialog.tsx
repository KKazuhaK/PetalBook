import { useNavigate } from 'react-router'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { spaceUrls } from '@/lib/urls'
import { tg } from '@/i18n/manage'
import { ViewForm } from './ViewForm'
import { fitScreen } from './shared'

// From the administration: asks the reason, then opens the space with the view on.
export function ViewDialog({ space, name, onClose }: { space: string; name: string; onClose: () => void }) {
  const navigate = useNavigate()
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className={`${fitScreen} sm:max-w-md`} data-slot="view-dialog">
        <DialogHeader>
          <DialogTitle>{tg.view.title(name)}</DialogTitle>
          <DialogDescription>{tg.view.gateHelp}</DialogDescription>
        </DialogHeader>
        <ViewForm
          space={space}
          name={name}
          onCancel={onClose}
          onStarted={() => {
            onClose()
            void navigate(spaceUrls(space).home())
          }}
        />
      </DialogContent>
    </Dialog>
  )
}
