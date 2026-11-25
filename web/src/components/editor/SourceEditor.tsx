import { useState } from 'react'
import { BookMarked, Settings2 } from 'lucide-react'
import { useSpacePermissions } from '@/api/hooks'
import { t } from '@/i18n'
import { ChipInput } from './ChipInput'
import { SourcePresetsDialog } from './SourcePresetsDialog'

// The sources of a page (a person, a book, a film): the same input as the tags, with the presets of the space after the
// values pages already use.
export function SourcesEditor({
  sources,
  onChange,
  used,
  presets,
  chosen = false,
  readOnly = false,
}: {
  sources: string[]
  onChange: (sources: string[]) => void
  used: { source: string; pages: number }[]
  presets: string[]
  // The space chose its presets (they are not the defaults of the language).
  chosen?: boolean
  readOnly?: boolean
}) {
  const { manage } = useSpacePermissions()
  const [editing, setEditing] = useState(false)
  return (
    <div className="flex items-start gap-1">
      <div className="min-w-0 flex-1">
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
      </div>
      {manage && !readOnly && (
        <button
          type="button"
          data-slot="edit-presets"
          aria-label={t.sources.editPresets}
          title={t.sources.editPresets}
          onClick={() => setEditing(true)}
          className="flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground opacity-0 transition-opacity outline-none hover:bg-muted focus-visible:opacity-100 focus-visible:ring-2 focus-visible:ring-ring/50 pointer-coarse:opacity-60 [.group\/title:hover_&]:opacity-100"
        >
          <Settings2 className="size-3.5" aria-hidden />
        </button>
      )}
      {editing && <SourcePresetsDialog presets={presets} chosen={chosen} onClose={() => setEditing(false)} />}
    </div>
  )
}
