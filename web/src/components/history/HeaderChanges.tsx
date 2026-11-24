import type { HeaderChange, TextPart } from '@/diff'
import { t } from '@/i18n'

const shown = (v: unknown): string =>
  v === undefined || v === null || v === '' ? t.history.empty_ : typeof v === 'string' ? v : JSON.stringify(v)

function Parts({ parts }: { parts: TextPart[] }) {
  return (
    <>
      {parts.map((p, i) =>
        p.type === 'ins' ? (
          <ins key={i}>
            <span className="sr-only">{t.history.srAdded}</span>
            {p.text}
          </ins>
        ) : p.type === 'del' ? (
          <del key={i}>
            <span className="sr-only">{t.history.srRemoved}</span>
            {p.text}
          </del>
        ) : (
          <span key={i}>{p.text}</span>
        ),
      )}
    </>
  )
}

// What changed above the body: title, subtitle, source, tags and any other header field.
export function HeaderChanges({ changes }: { changes: HeaderChange[] }) {
  if (changes.length === 0) return null
  return (
    <section aria-label={t.history.headerChanges} className="mb-6 rounded-lg border bg-muted/30 p-3 text-sm">
      <h2 className="mb-2 text-xs font-medium text-muted-foreground">{t.history.headerChanges}</h2>
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1.5">
        {changes.map((c, i) => (
          <div key={i} className="contents">
            <dt className="text-muted-foreground">
              {c.kind === 'text'
                ? t.history.headerLabels[c.field]
                : c.kind === 'tags'
                  ? t.history.headerLabels.tags
                  : t.history.otherKey(c.key)}
            </dt>
            <dd className="pb-diff-sample min-w-0 break-words">
              {c.kind === 'text' && <Parts parts={c.parts} />}
              {c.kind === 'tags' && (
                <span className="flex flex-wrap gap-1.5">
                  {c.removed.map((tag) => (
                    <del key={`-${tag}`} className="rounded-full px-2 py-0.5 text-xs">
                      <span className="sr-only">{t.history.tagRemoved(tag)}</span>
                      <span aria-hidden>− {tag}</span>
                    </del>
                  ))}
                  {c.added.map((tag) => (
                    <ins key={`+${tag}`} className="rounded-full px-2 py-0.5 text-xs">
                      <span className="sr-only">{t.history.tagAdded(tag)}</span>
                      <span aria-hidden>+ {tag}</span>
                    </ins>
                  ))}
                </span>
              )}
              {c.kind === 'other' && (
                <>
                  {c.change !== 'added' && (
                    <del>
                      <span className="sr-only">{t.history.srRemoved}</span>
                      {shown(c.from)}
                    </del>
                  )}
                  {c.change === 'changed' && ' '}
                  {c.change !== 'removed' && (
                    <ins>
                      <span className="sr-only">{t.history.srAdded}</span>
                      {shown(c.to)}
                    </ins>
                  )}
                </>
              )}
            </dd>
          </div>
        ))}
      </dl>
    </section>
  )
}
