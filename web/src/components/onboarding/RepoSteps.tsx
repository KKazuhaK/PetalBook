import { Check, Circle } from 'lucide-react'
import { rememberSpace } from '@/api/hooks'
import { Button } from '@/components/ui/button'
import { useOpenSettings } from '@/components/settings/useSettings'
import { absoluteTime } from '@/lib/time'
import { to } from '@/i18n/onboard'
import { useRepoGuide, type RepoStepId } from './useRepoGuide'

// The steps of connecting one's own repository, one after another: why, what to click, what to authorise, and where to
// get help. The step to take now has its button; the ones before it are ticked, the ones after wait. Used by the
// guide's dialog and by the tour's repository step.
export function RepoSteps({ onNavigate }: { onNavigate?: () => void }) {
  const g = useRepoGuide()
  const openSettings = useOpenSettings()
  const r = to.repo
  if (!g.note || !g.space)
    return (
      <p data-slot="repo-done" className="rounded-lg border bg-muted/40 px-3 py-3 text-sm">
        {r.nothing}
      </p>
    )
  const name = g.providerName || '…'
  const go = (section: 'connections' | 'sync') => {
    rememberSpace(g.space!.id)
    onNavigate?.()
    openSettings(section)
  }
  const text: Record<
    RepoStepId,
    { title: string; what: string; authorise?: string; action?: string; section?: 'connections' | 'sync' }
  > = {
    connect: {
      title: r.connect.title(name),
      what: r.connect.what(name),
      authorise: r.connect.authorise(name),
      action: r.connect.action,
      section: 'connections',
    },
    repo: {
      title: r.repo.title,
      what: r.repo.what,
      authorise: r.repo.authorise,
      action: r.repo.action,
      section: 'sync',
    },
    sync: { title: r.sync.title, what: r.sync.what },
  }
  return (
    <div data-slot="repo-steps" data-forced={g.forced} className="space-y-4 text-sm">
      <section className="space-y-1">
        <h3 className="font-medium">{r.whyTitle}</h3>
        <p className="text-muted-foreground">
          {g.forced ? r.whyForced : r.whyAsked(absoluteTime(g.note.deadline))}{' '}
          {g.note.required === 'any' ? r.anyProvider : r.byProvider(name)}
        </p>
      </section>
      <section className="space-y-2">
        <h3 className="font-medium">
          {r.stepsTitle} <span className="font-normal text-muted-foreground">· {r.left(g.left)}</span>
        </h3>
        <ol className="space-y-2">
          {g.steps.map((s, i) => {
            const t = text[s.id]
            const now = s.id === g.current
            return (
              <li
                key={s.id}
                data-step={s.id}
                data-state={s.done ? 'done' : now ? 'now' : 'todo'}
                className={`rounded-lg border p-3 transition-colors duration-200 ${now ? 'border-brand/50 bg-brand/5' : ''}`}
              >
                <div className="flex items-start gap-2">
                  <span aria-hidden className="mt-0.5 shrink-0 text-muted-foreground">
                    {s.done ? <Check className="size-4 text-brand" /> : <Circle className="size-4" />}
                  </span>
                  <div className="min-w-0 flex-1 space-y-1">
                    <p className="font-medium">
                      <span className="sr-only">{r.stepOf(i + 1, g.steps.length)}: </span>
                      {t.title}{' '}
                      <span className="text-xs font-normal text-muted-foreground">
                        {s.done ? r.done : s.auto ? r.auto : now ? r.now : r.todo}
                      </span>
                    </p>
                    <p className="text-muted-foreground">{t.what}</p>
                    {t.authorise && <p className="text-muted-foreground">{t.authorise}</p>}
                    {now && t.action && t.section && (
                      <Button size="sm" className="mt-1 h-10 sm:h-8" onClick={() => go(t.section!)}>
                        {t.action}
                      </Button>
                    )}
                  </div>
                </div>
              </li>
            )
          })}
        </ol>
      </section>
      <section className="space-y-1">
        <h3 className="font-medium">{r.helpTitle}</h3>
        <p className="text-muted-foreground">{r.help}</p>
      </section>
    </div>
  )
}
