import type { DailyOption, DailyOptionChoice, DailyPrefs } from '@/api/types'
import { NativeSelect } from '@/components/ui/native-select'
import { th } from '@/i18n/home'
import type { SpaceItem } from '@/lib/dailySettings'
import { emptyDraft } from '@/lib/dailySettings'
import { ListButtons, moved, OptionChoices } from './DailyChoices'
import { SourceFields, type SourceChoices } from './SourceFields'

// The ordered list of sources of one language of a space: interfaces the administrators set up (with the options of a
// built-in one), their lines, and pages of this space. With more than one they take turns by day; one that gives
// nothing is passed over for the next, and "another" moves to the next. Only one text is ever shown.
export function SourceList({
  items,
  onItems,
  available,
  options,
  onOptions,
  choices,
  canOwn,
  apiLocked,
  disabled,
  owner,
}: {
  items: SpaceItem[]
  onItems: (next: SpaceItem[]) => void
  // The interfaces made for this language (or for every language, on the first tab).
  available: NonNullable<DailyPrefs['available']>
  options: DailyOptionChoice
  onOptions: (next: DailyOptionChoice) => void
  choices: SourceChoices
  // Whether the administrators let a space use its own pages; and keep it from choosing an interface.
  canOwn: boolean
  apiLocked: boolean
  disabled?: boolean
  owner: string
}) {
  const nameOf = (id: string) => available.find((a) => a.id === id)?.name ?? id
  const label = (i: SpaceItem) =>
    i.kind === 'api' ? nameOf(i.id) : i.kind === 'list' ? th.settings.srcLines : th.settings.srcOwn
  const optionsOf = (id: string): DailyOption[] => available.find((a) => a.id === id)?.options ?? []
  const setItem = (at: number, patch: Partial<SpaceItem>) =>
    onItems(items.map((x, i) => (i === at ? ({ ...x, ...patch } as SpaceItem) : x)))
  const free = available.filter((a) => !items.some((x) => x.kind === 'api' && x.id === a.id))
  return (
    <div className="grid gap-2" data-slot="daily-apis">
      <p className="text-xs text-muted-foreground">{th.settings.apisHelp}</p>
      {items.length > 1 && <p className="text-xs text-muted-foreground">{th.settings.apisOrderHelp}</p>}
      {items.length === 0 && available.length === 0 && (
        <p className="text-sm text-muted-foreground">{th.settings.noApis}</p>
      )}
      {items.length > 0 && (
        <ol className="grid gap-2" data-slot="source-list">
          {items.map((it, i) => (
            <li
              key={it.kind === 'api' ? it.id : `${it.kind}-${i}`}
              data-api-item={it.kind === 'api' ? it.id : undefined}
              data-source-kind={it.kind}
              className="grid min-w-0 gap-1.5 rounded-lg border p-2"
            >
              <div className="flex min-w-0 items-center justify-between gap-2">
                <span className="min-w-0 text-sm break-words">
                  {i + 1}.{' '}
                  {it.kind === 'api'
                    ? th.settings.apiFor(nameOf(it.id), available.find((a) => a.id === it.id)?.lang ?? '')
                    : label(it)}
                  {it.kind === 'api' && apiLocked && (
                    <span className="text-xs text-muted-foreground"> {th.settings.lockedByAdmin}</span>
                  )}
                </span>
                <ListButtons
                  name={label(it)}
                  first={i === 0}
                  last={i === items.length - 1}
                  onUp={() => onItems(moved(items, i, -1))}
                  onDown={() => onItems(moved(items, i, 1))}
                  onRemove={() => onItems(items.filter((_, at) => at !== i))}
                  words={th.settings.choice}
                  disabled={disabled}
                />
              </div>
              {it.kind === 'own' && (
                <SourceFields
                  draft={it.draft}
                  onChange={(draft) => setItem(i, { draft })}
                  choices={choices}
                  disabled={disabled}
                />
              )}
              {it.kind === 'api' && optionsOf(it.id).length > 0 && (
                <OptionChoices
                  options={optionsOf(it.id)}
                  chosen={options[it.id]}
                  owner={`${owner}-${it.id}`}
                  words={th.settings.choice}
                  disabled={disabled}
                  onChange={(next) => {
                    const out = { ...options }
                    if (Object.keys(next).length === 0) delete out[it.id]
                    else out[it.id] = next
                    onOptions(out)
                  }}
                />
              )}
            </li>
          ))}
        </ol>
      )}
      <NativeSelect
        aria-label={th.settings.addSource}
        value=""
        disabled={disabled}
        onChange={(e) => {
          const v = e.target.value
          if (v === '') return
          if (v === '@list') onItems([...items, { kind: 'list' }])
          else if (v === '@own') onItems([...items, { kind: 'own', draft: emptyDraft() }])
          else onItems([...items, { kind: 'api', id: v }])
        }}
      >
        <option value="">{th.settings.addSource}</option>
        {free.length > 0 && (
          <optgroup label={th.settings.srcInterfaces}>
            {free.map((a) => (
              <option key={a.id} value={a.id}>
                {th.settings.apiFor(a.name, a.lang)}
              </option>
            ))}
          </optgroup>
        )}
        {!items.some((x) => x.kind === 'list') && <option value="@list">{th.settings.srcLines}</option>}
        {canOwn && <option value="@own">{th.settings.srcOwn}</option>}
      </NativeSelect>
    </div>
  )
}
