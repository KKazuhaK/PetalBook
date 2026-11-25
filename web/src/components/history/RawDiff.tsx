import type { LineDiff } from '@/diff'
import { useRef } from 'react'
import { scrollFadeTouchClass, useScrollFade } from '@/components/ui/scroll-fade'
import { t } from '@/i18n'

// A line-by-line comparison of the Markdown text. A sign column says what each
// line is, so it does not depend on colour.
export function RawDiff({ diff }: { diff: LineDiff }) {
  const fadeRef = useRef<HTMLDivElement>(null)
  useScrollFade(fadeRef)
  if (diff.ops.length === 0) return <p className="text-sm text-muted-foreground">{t.history.identical}</p>
  return (
    <div
      ref={fadeRef}
      className={`pb-rawdiff ${scrollFadeTouchClass} rounded-lg border`}
      role="table"
      aria-label={t.history.raw}
    >
      {diff.ops.map((op, i) => (
        <div key={i} className="pb-line" data-op={op.type} role="row">
          <span className="pb-no" role="cell">
            {op.oldLine ?? ''}
          </span>
          <span className="pb-no" role="cell">
            {op.newLine ?? ''}
          </span>
          <span className="pb-sign" role="cell">
            <span aria-hidden>{op.type === 'ins' ? '+' : op.type === 'del' ? '−' : ''}</span>
            <span className="sr-only">
              {op.type === 'ins' ? t.history.srAdded : op.type === 'del' ? t.history.srRemoved : ''}
            </span>
          </span>
          <span className="pb-text" role="cell">
            {op.text === '' ? ' ' : op.text}
          </span>
        </div>
      ))}
    </div>
  )
}
