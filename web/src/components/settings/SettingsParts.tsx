import { useId, type ReactNode } from 'react'
import { CloudOff, Download, GitBranch, House, KeyRound, LayoutTemplate, Settings, Shapes, ShieldCheck, SlidersHorizontal, User, Users } from 'lucide-react'
import type { SectionId } from './sections'
import { RequiredMark } from '@/components/ui/field-mark'

// The building blocks every settings section is made of, so spacing and rows
// look the same everywhere: a heading with a rule under it, then rows of
// "name + one line of help + the control on the right".

export function SettingsSection({ title, children, id, description }: { title: string; children: ReactNode; id?: string; description?: string }) {
  const auto = useId()
  const headingId = id ?? auto
  return (
    <section aria-labelledby={headingId} className="grid grid-cols-1 gap-3">
      <div className="grid gap-0.5 border-b pb-2">
        <h3 id={headingId} className="text-sm font-semibold">
          {title}
        </h3>
        {description && <p className="text-xs text-muted-foreground">{description}</p>}
      </div>
      {children}
    </section>
  )
}

// A sentence in a box where a section would be: why there is nothing to do here.
export function SettingsNote({ children }: { children: ReactNode }) {
  return <p className="rounded-lg border p-4 text-sm text-muted-foreground">{children}</p>
}

// Rows are divided by a hairline; a section's own form sits below its heading
// without that.
export function SettingsRows({ children }: { children: ReactNode }) {
  return <div className="divide-y divide-border/70">{children}</div>
}

// `label` ties the name to the control for a screen reader; the help line is
// described-by the control as well.
// `required` puts the star of a required field after the name; the control itself says aria-required.
export function SettingsRow({ title, help, children, controlId, required }: { title: string; help?: string; children: ReactNode; controlId?: string; required?: boolean }) {
  const helpId = useId()
  return (
    <div className="flex items-center justify-between gap-6 py-3 max-sm:flex-col max-sm:items-stretch max-sm:gap-2.5" data-slot="settings-row">
      <div className="min-w-0 flex-1">
        {controlId ? (
          <label htmlFor={controlId} className="block text-sm font-medium">
            {title}
            {required && <RequiredMark />}
          </label>
        ) : (
          <div className="text-sm font-medium">
            {title}
            {required && <RequiredMark />}
          </div>
        )}
        {help && (
          <p id={helpId} className="mt-0.5 text-xs break-words text-muted-foreground">
            {help}
          </p>
        )}
      </div>
      <div className="shrink-0 max-sm:w-full" aria-describedby={help ? helpId : undefined}>
        {children}
      </div>
    </div>
  )
}

const ICONS: Record<Exclude<SectionId, 'profile'>, typeof User> = {
  preferences: SlidersHorizontal,
  security: ShieldCheck,
  tokens: KeyRound,
  general: Settings,
  types: Shapes,
  templates: LayoutTemplate,
  home: House,
  sync: GitBranch,
  offline: CloudOff,
  import: Download,
  members: Users,
}

// The small mark in front of a navigation entry. Profile shows the person's
// initial the way the user menu does, a user icon until the name is known.
export function SectionIcon({ id, name }: { id: SectionId; name?: string }) {
  const initial = name ? Array.from(name)[0]?.toUpperCase() : undefined
  if (id === 'profile' && initial)
    return (
      // The letter is drawn from an attribute so it is not part of the entry's text.
      <span aria-hidden="true" data-slot="section-icon" data-initial={initial} className="flex size-4 shrink-0 items-center justify-center rounded-full bg-brand/15 text-[0.625rem] leading-none font-medium text-brand before:content-[attr(data-initial)]" />
    )
  const Icon = id === 'profile' ? User : ICONS[id]
  return <Icon aria-hidden="true" data-slot="section-icon" className="size-4 shrink-0 text-muted-foreground" strokeWidth={1.75} />
}
