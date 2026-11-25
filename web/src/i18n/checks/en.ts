import type { ChecksMessages } from '../checksTypes.ts'

export const checksEn: ChecksMessages = {
  title: 'Problems',
  pageTitle: (space: string) => `${space} · Problems`,
  intro:
    'The files of the space that have a problem. The result is noted when the index is built; opening this page does not scan the disk.',
  loadFailed: 'Could not read the list of problems.',
  empty: 'No problems found',
  emptyHelp: 'The headers, type fields, links and attachments of every page are fine.',
  emptyKind: 'No problems of this kind left.',
  showAll: 'Show all',
  summary: (n: number) => (n === 1 ? '1 problem' : `${n} problems`),
  filterLabel: 'Filter by kind',
  all: 'All',
  more: (n: number) => `Show ${n} more`,
  moreLoading: 'Loading…',
  moreFailed: 'Could not read the next batch.',
  retry: 'Try again',
  openPage: 'Open the page',
  openFolder: 'Open the folder',
  line: (n: number) => `line ${n}`,
  target: 'Points to',
  fields: 'Fields',
  files: 'These files',
  header: 'What the parser says',
  sharedId: (id: string) => `Shared id: ${id}`,
  kinds: {
    no_id: {
      title: 'No id',
      help: 'The header of the file has no id, so it is not a page yet: search, links and lists do not show it.',
    },
    duplicate_id: {
      title: 'Duplicate id',
      help: 'Several files use the same id. Only one of them is served as a page; change the id of the others by hand.',
    },
    bad_header: {
      title: 'Header cannot be read',
      help: 'The header is not valid YAML, so its title and fields cannot be read. Open the file and correct the header.',
    },
    type_mismatch: {
      title: 'Fields do not fit the type',
      help: 'Some values of a page do not fit what its type says (an option that is not in the list, a number written as text). Correct them in the page.',
    },
    not_in_type: {
      title: 'Fields not in the type',
      help: 'The header has fields that the type does not define. Remove them here, or add them to the type.',
    },
    broken_link: {
      title: 'Broken links',
      help: 'A link in a page leads to a page that does not exist: no page has its id, or there is no page at its path.',
    },
    missing_asset: {
      title: 'Missing attachments',
      help: 'An image or attachment in a page points to a file that does not exist. Upload it again or correct the file name.',
    },
    duplicate_title: {
      title: 'Duplicate titles',
      help: 'Several pages in one folder have the same title, so a list cannot tell them apart. Rename one of them.',
    },
  },
  fix: {
    addId: 'Add an id',
    removeFields: 'Remove these fields',
    working: 'Fixing…',
    readOnly: 'You can only look; fixing needs the permission to edit.',
    addIdDone: (path: string) => `Gave ${path} an id; it is a page now`,
    removeFieldsDone: (path: string) => `Removed the fields of ${path} that are not in its type`,
    confirmTitle: 'Remove these fields?',
    confirmHelp: (path: string, fields: string) =>
      `The header of ${path} loses these fields: ${fields}. The rest of the header stays as it is, and the history can bring them back.`,
    confirm: 'Remove',
    cancel: 'Cancel',
    conflict: 'The file was just changed. It has been read again; have another look before fixing it.',
    gone: 'That file is gone. The list has been updated.',
    notFixable: 'This kind of problem cannot be fixed automatically.',
    failed: 'Could not fix it',
  },
}
