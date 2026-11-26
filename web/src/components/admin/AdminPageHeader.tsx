import { createContext, useContext, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

// The slot a page's primary action goes in. The header owns it; the page's own component fills it with
// AdminPageActions, so a panel keeps its action next to the state it acts on.
const ActionsSlot = createContext<HTMLElement | null | undefined>(undefined)

// The top of an administration page: the title (the page's h1), a line saying what the page is for, and on the right
// the page's primary action. On a phone the title wraps and the actions go below it. The tab strip and the page itself
// are the children, under the header.
export function AdminPageHeader({
  title,
  description,
  actions,
  leading,
  meta,
  children,
}: {
  title: string
  description?: string
  actions?: ReactNode
  // Before the title (a person's avatar).
  leading?: ReactNode
  // After the title (a badge).
  meta?: ReactNode
  children?: ReactNode
}) {
  const [slot, setSlot] = useState<HTMLElement | null>(null)
  return (
    <ActionsSlot.Provider value={slot}>
      <div className="min-w-0 space-y-5">
        <header
          data-slot="admin-page-header"
          className="flex min-w-0 flex-col gap-3 sm:flex-row sm:items-start sm:justify-between"
        >
          <div className="flex min-w-0 items-center gap-3">
            {leading}
            <div className="min-w-0">
              <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
                <h1 className="min-w-0 text-xl font-semibold tracking-tight [overflow-wrap:anywhere]">{title}</h1>
                {meta}
              </div>
              {description && (
                <p data-slot="page-description" className="mt-1 text-sm text-muted-foreground">
                  {description}
                </p>
              )}
            </div>
          </div>
          <div
            ref={setSlot}
            data-slot="admin-page-actions"
            className="flex shrink-0 flex-wrap items-center gap-2 empty:hidden"
          >
            {actions}
          </div>
        </header>
        {children}
      </div>
    </ActionsSlot.Provider>
  )
}

// Puts a page's primary action in the header of the page it is on.
export function AdminPageActions({ children }: { children: ReactNode }) {
  const slot = useContext(ActionsSlot)
  // Not under a header (a panel on its own): the action stays where it is written.
  if (slot === undefined) return <>{children}</>
  return slot ? createPortal(children, slot) : null
}
