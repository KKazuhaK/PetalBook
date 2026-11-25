import { Tag } from 'lucide-react'
import { t } from '@/i18n'
import { ChipInput } from './ChipInput'

// The tags of a page: chips plus an input, suggestions from the tags already used elsewhere.
export function TagsEditor({
  tags,
  onChange,
  suggestions,
  readOnly = false,
}: {
  tags: string[]
  onChange: (tags: string[]) => void
  suggestions: { tag: string; pages: number }[]
  readOnly?: boolean
}) {
  return (
    <ChipInput
      values={tags}
      onChange={onChange}
      used={suggestions.map((s) => ({ value: s.tag, pages: s.pages }))}
      icon={<Tag className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />}
      placeholder={t.editor.tagsPlaceholder}
      label={t.editor.tagsLabel}
      removeLabel={t.editor.removeTag}
      readOnly={readOnly}
    />
  )
}
