import { BookMarked } from 'lucide-react'
import { t } from '@/i18n'
import { ChipInput } from './ChipInput'

// The sources of a page (a person, a book, a film): the same input as the tags, with the presets of the space after the
// values pages already use.
export function SourcesEditor({
  sources,
  onChange,
  used,
  presets,
  readOnly = false,
}: {
  sources: string[]
  onChange: (sources: string[]) => void
  used: { source: string; pages: number }[]
  presets: string[]
  readOnly?: boolean
}) {
  return (
    <ChipInput
      values={sources}
      onChange={onChange}
      used={used.map((s) => ({ value: s.source, pages: s.pages }))}
      presets={presets}
      icon={<BookMarked className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />}
      placeholder={t.editor.sourcePlaceholder}
      label={t.editor.sourceLabel}
      removeLabel={t.editor.removeSource}
      readOnly={readOnly}
    />
  )
}
