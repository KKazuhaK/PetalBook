import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { fieldsOf, type ConflictInfo } from '@/editor/saveController'
import { t } from '@/i18n'
import { absoluteTime } from '@/lib/time'

interface Shown {
  title: string
  subtitle: string
  source?: string
  tags: string[]
  body: string
}

const line = (label: string, value: string) => (
  <div className="grid grid-cols-[3.5rem_1fr] gap-2 text-sm">
    <span className="text-muted-foreground">{label}</span>
    <span className="min-w-0 break-words">
      {value || <span className="text-muted-foreground">{t.conflict.none}</span>}
    </span>
  </div>
)

function Version({ heading, shown }: { heading: string; shown: Shown }) {
  return (
    <section className="flex min-w-0 flex-col gap-2 rounded-lg border p-3">
      <h3 className="text-sm font-medium">{heading}</h3>
      {line(t.conflict.title_, shown.title)}
      {line(t.conflict.subtitle_, shown.subtitle)}
      {shown.source ? line(t.conflict.source_, shown.source) : null}
      {line(t.conflict.tags_, t.fmt.list(shown.tags))}
      <div className="text-sm text-muted-foreground">{t.conflict.body_}</div>
      {/* Plain Markdown text: it is the file's content, shown as written. */}
      <pre className="max-h-72 overflow-auto rounded-md bg-muted p-2.5 text-xs leading-relaxed whitespace-pre-wrap break-words">
        {shown.body || t.conflict.none}
      </pre>
    </section>
  )
}

// Two versions of the person's text, or the text and the page file, side by
// side. Which two is `info.kind`: against the file (the draft was written on a
// version the file has left) or against another draft (another tab or device).
export function ConflictDialog({
  open,
  onOpenChange,
  info,
  onKeepMine,
  onUseTheirs,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  info: ConflictInfo
  onKeepMine: () => void
  onUseTheirs: () => void
}) {
  const theirs = fieldsOf(info.theirs.meta)
  const c = t.conflict
  const draft = info.kind === 'draft'
  const mine = info.mine
  // An older copy kept in this browser: taking the server's version is the default.
  const when = info.older ? absoluteTime(new Date(info.older.savedAt).toISOString()) : ''
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-4xl" showCloseButton={false}>
        <DialogHeader>
          <DialogTitle>{info.older ? c.olderTitle : draft ? c.draftTitle : c.title}</DialogTitle>
          <DialogDescription>
            {info.older ? c.olderDescription(when) : draft ? c.draftDescription : c.description}
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3 md:grid-cols-2">
          <Version
            heading={info.older ? c.olderMine(when) : draft ? c.draftMine : c.mine}
            shown={{
              title: mine.title,
              subtitle: mine.subtitle,
              source: mine.source,
              tags: mine.tags,
              body: mine.body ?? '',
            }}
          />
          <Version
            heading={info.older ? c.olderTheirs : draft ? c.draftTheirs : c.theirs}
            shown={{ ...theirs, body: info.theirs.body }}
          />
        </div>
        <DialogFooter className="gap-2 sm:justify-between">
          <Button
            type="button"
            variant={info.older ? 'default' : 'outline'}
            onClick={onUseTheirs}
            title={draft ? c.draftUseTheirsHint : c.useTheirsHint}
          >
            {draft ? c.draftUseTheirs : c.useTheirs}
          </Button>
          <Button
            type="button"
            variant={info.older ? 'outline' : 'default'}
            onClick={onKeepMine}
            title={info.older ? c.olderKeepMineHint : draft ? c.draftKeepMineHint : c.keepMineHint}
          >
            {info.older ? c.olderKeepMine : draft ? c.draftKeepMine : c.keepMine}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
