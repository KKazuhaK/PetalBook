import type { OfflineMessages } from '../offlineTypes.ts'

export const offlineEn: OfflineMessages = {
  title: 'Offline',
  help: 'Keep a copy of this space on this device, so you can browse, read and search it without a network. Pages cannot be changed while offline.',
  unencrypted:
    'Offline data is not encrypted on this device, like the data of any other site in the browser. If the device is lost, revoke your sessions in the account’s security settings; signing out deletes this account’s offline data from this device.',
  unencryptedSingle: 'Offline data is not encrypted on this device, like the data of any other site in the browser.',
  modeLabel: 'What this space keeps on this device',
  modes: { off: 'Nothing', catalog: 'Catalogue only', text: 'Catalogue and text' },
  modeHelp: {
    off: 'Nothing of this space is kept on this device.',
    catalog:
      'Keeps the title, place, tags and update time of every page: the folder tree, lists and finding by title work offline; the text cannot be opened.',
    text: 'Besides the catalogue, keeps the text of the pages in the scope: those can be read offline and are found by their text.',
  },
  scopeTitle: 'What text is kept',
  scopeHelp: (cap: string) =>
    `All the spaces on this device together keep at most ${cap} of text; when it is full, the text opened longest ago goes first.`,
  favourites: 'My favourite pages and folders',
  inbox: 'The inbox',
  inboxNamed: (name: string) => `The inbox (${name})`,
  recent: (n: number) => `The last ${n} pages opened`,
  folders: 'Chosen folders (and what is below them)',
  tags: 'Chosen tags',
  addFolder: 'Add a folder',
  addTag: 'Add a tag',
  choose: 'Choose…',
  remove: (what: string) => `Remove ${what}`,
  none: 'None chosen yet',
  statusTitle: 'On this device',
  syncedAt: (when: string) => `Last synced: ${when}`,
  neverSynced: 'Not synced yet',
  catalogCount: (n: number) => (n === 1 ? 'Catalogue: 1 page' : `Catalogue: ${n} pages`),
  textCount: (n: number) => (n === 1 ? 'With text: 1 page' : `With text: ${n} pages`),
  usedHere: (size: string) => `Used by this space: ${size}`,
  usedDevice: (size: string, cap: string) => `On this device in all: ${size} of ${cap}`,
  overCap:
    'More text was wanted than the limit allows, so some pages were not kept. Narrow the scope, or clear the offline data of another space.',
  disabled:
    'An administrator has switched this space off: what is here stays, read only, and syncing goes on when it is back.',
  lastError: (message: string) => `The last sync did not work: ${message}`,
  offlineNow: 'The server cannot be reached; it will sync again by itself when there is a network.',
  syncNow: 'Sync now',
  syncing: 'Syncing…',
  upToDate: 'Up to date',
  clear: 'Clear this space’s offline data',
  cleared: 'This space’s offline data is cleared from this device',
  saved: 'Saved',
  failed: 'Could not save',
  loadFailed: 'Could not read the offline data on this device',
  offByAdmin:
    'Offline copies are turned off by your administrators. Nothing is kept on this device, and any copy this account had here is deleted.',
  unsupported: 'This browser cannot keep data on this device.',
}
