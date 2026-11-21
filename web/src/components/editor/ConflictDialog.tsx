import type { Page } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { fieldsOf, type Draft } from '@/editor/saveController'
import { t } from '@/i18n/zh-CN'

interface Shown {
  title: string
  subtitle: string
  tags: string[]
  body: string
}

const line = (label: string, value: string) => (
  <div className="grid grid-cols-[3.5rem_1fr] gap-2 text-sm">
    <span className="text-muted-foreground">{label}</span>
    <span className="min-w-0 break-words">{value || <span className="text-muted-foreground">{t.conflict.none}</span>}</span>
  </div>
)

function Version({ heading, shown }: { heading: string; shown: Shown }) {
  return (
    <section className="flex min-w-0 flex-col gap-2 rounded-lg border p-3">
      <h3 className="text-sm font-medium">{heading}</h3>
      {line(t.conflict.title_, shown.title)}
      {line(t.conflict.subtitle_, shown.subtitle)}
      {line(t.conflict.tags_, shown.tags.join('、'))}
      <div className="text-sm text-muted-foreground">{t.conflict.body_}</div>
      {/* Plain Markdown text: it is the file's content, shown as written. */}
      <pre className="max-h-72 overflow-auto rounded-md bg-muted p-2.5 text-xs leading-relaxed whitespace-pre-wrap break-words">{shown.body || t.conflict.none}</pre>
    </section>
  )
}

export function ConflictDialog({
  open,
  onOpenChange,
  current,
  mine,
  onKeepMine,
  onUseTheirs,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  current: Page
  mine: Draft
  onKeepMine: () => void
  onUseTheirs: () => void
}) {
  const theirs = fieldsOf(current.meta)
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-4xl" showCloseButton={false}>
        <DialogHeader>
          <DialogTitle>{t.conflict.title}</DialogTitle>
          <DialogDescription>{t.conflict.description}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3 md:grid-cols-2">
          <Version heading={t.conflict.mine} shown={{ title: mine.title, subtitle: mine.subtitle, tags: mine.tags, body: mine.body ?? '' }} />
          <Version heading={t.conflict.theirs} shown={{ ...theirs, body: current.body }} />
        </div>
        <DialogFooter className="gap-2 sm:justify-between">
          <Button type="button" variant="outline" onClick={onUseTheirs} title={t.conflict.useTheirsHint}>
            {t.conflict.useTheirs}
          </Button>
          <Button type="button" onClick={onKeepMine} title={t.conflict.keepMineHint}>
            {t.conflict.keepMine}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
