import type { BoardsMessages } from '../boardsTypes.ts'

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`

export const boardsEn: BoardsMessages = {
  tools: {
    settings: 'View settings',
    group: 'Group by',
    groupNone: 'Not chosen',
    groupKeyHelp:
      'The name of a property in the page headers; the pages under this tag are put in columns by its value.',
    groupKey: 'Property name',
    columns: 'Table columns',
    columnsHelp: 'Columns that are unchecked are hidden in the table.',
    cardFields: 'Fields on a card',
    cardFieldsHelp: (n) => `At most ${n}.`,
    cardFieldsDefault: 'Default (the first fields)',
    noFields: 'There is no type here, so no fields to choose from.',
  },
  board: {
    label: 'Board',
    columnLabel: (name, count) => `${name}, ${count} cards`,
    other: 'Other values',
    empty: 'Empty',
    noCards: 'No cards',
    loadMore: 'Load more',
    loadingMore: 'Loading…',
    loadFailed: 'Could not read this column.',
    retry: 'Retry',
    needsGroup: 'Choose a field to group the board by.',
    noGroupField: 'This type has no select field, so there is nothing to make a board of.',
    pickGroup: 'Choose the grouping field',
    groupsFailed: 'Could not read the number of cards in each column.',
    untitled: 'Untitled',
    moveFailed: (name, message) => `Could not move "${name}": ${message}`,
    conflict: (name) => `"${name}" was just changed by someone else. The card was refreshed and not moved.`,
    partial: 'The list holds only part of this value; open the page to change it',
    moved: (name, column) => `Moved "${name}" to "${column}"`,
    moveTo: (name) => `Move "${name}" to…`,
    dropOther: '"Other values" is not one value, so a card cannot be dropped there',
    valuesReadOnly:
      'These columns are the values the pages have; to move a card, change the property in its page, or group by a single-choice field',
  },
  reading: {
    label: 'Reading',
    loading: 'Loading…',
    loadMore: 'Load more',
    loaded: (n) => plural(n, 'page read', 'pages read'),
    allLoaded: (n) => `${plural(n, 'page', 'pages')}, that is all`,
    edit: (name) => `Edit "${name}"`,
    empty: 'Nothing to read here.',
    emptyBody: '(no text)',
    diagram: 'Diagram',
    loadFailed: 'Could not read the pages.',
  },
}
