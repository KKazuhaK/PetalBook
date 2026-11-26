import { useState, type ReactNode } from 'react'
import { cn } from 'cn'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { useIsMobile } from '@/hooks/use-mobile'

// A dialog on a computer, a sheet that rises from the bottom on a phone. The body scrolls inside it; the
// actions (`footer`) stay in view under it. Footer buttons carry words only: icons are for actions on a page.
export function ResponsiveDialog({
  open,
  onOpenChange,
  title,
  description,
  children,
  footer,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description?: string
  children: ReactNode
  footer?: ReactNode
}) {
  const mobile = useIsMobile()
  const body = (
    <div data-slot="dialog-body" className="-mx-4 max-h-[min(60vh,32rem)] min-w-0 overflow-y-auto px-4">
      {children}
    </div>
  )
  const actions = footer && (
    <div
      data-slot="dialog-footer"
      className={cn(
        'flex flex-col-reverse gap-2 sm:flex-row sm:justify-end',
        mobile ? 'border-t p-4' : '-mx-4 -mb-4 rounded-b-xl border-t bg-muted/50 p-4',
      )}
    >
      {footer}
    </div>
  )
  if (mobile)
    return (
      <Sheet open={open} onOpenChange={onOpenChange}>
        <SheetContent side="bottom" className="max-h-[92dvh] gap-0 rounded-t-xl">
          <SheetHeader>
            <SheetTitle>{title}</SheetTitle>
            {description && <SheetDescription>{description}</SheetDescription>}
          </SheetHeader>
          <div className="min-h-0 overflow-y-auto px-4 pb-4">{children}</div>
          {actions}
        </SheetContent>
      </Sheet>
    )
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        {body}
        {actions}
      </DialogContent>
    </Dialog>
  )
}

// A dialog that remembers whether it is open, for a page that has one button to open it.
export function useDialogState(initial = false) {
  const [open, setOpen] = useState(initial)
  return { open, setOpen }
}

// The tab strip of a dialog with a few tabs.
export function DialogTabs<T extends string>({
  tabs,
  value,
  onChange,
}: {
  tabs: { id: T; label: string }[]
  value: T
  onChange: (id: T) => void
}) {
  return (
    <div role="tablist" className="flex gap-1 rounded-lg bg-muted p-0.5">
      {tabs.map((tab) => (
        <button
          key={tab.id}
          type="button"
          role="tab"
          aria-selected={tab.id === value}
          className={cn(
            'min-h-8 flex-1 rounded-md px-3 text-sm font-medium transition-colors outline-none focus-visible:ring-3 focus-visible:ring-ring/50',
            tab.id === value ? 'bg-background shadow-sm' : 'text-muted-foreground hover:text-foreground',
          )}
          onClick={() => onChange(tab.id)}
        >
          {tab.label}
        </button>
      ))}
    </div>
  )
}
