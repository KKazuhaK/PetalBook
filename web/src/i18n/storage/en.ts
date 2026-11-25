export const storageEn = {
  // The administration's page for the site's storage policy.
  admin: {
    section: 'Storage',
    title: 'Storage',
    intro:
      'How the server’s disk is used: whether spaces must follow a remote repository, how much a space that has none may take, and when the copy of an idle space is removed. Nothing is ever deleted that is not on a remote.',
    loadFailed: 'Could not load the storage settings',
    save: 'Save the storage rules',
    saved: 'Saved.',
    saveFailed: 'Could not save',
    unchanged: 'Nothing to save',
    git: (version: string, partial: boolean) =>
      `Git ${version}. ${partial ? 'It can make partial clones.' : 'It is older than 2.44 and cannot make partial clones; idle spaces can still be evicted.'}`,
    remotes: {
      title: 'Remote repositories',
      help: 'A space that follows a remote repository is never the only copy of its pages, so the server may drop its own copy.',
      require: 'Require a remote',
      requireOptions: {
        none: 'Do not require one',
        any: 'Any allowed host',
        provider: (name: string) => `${name} only`,
      },
      requireHelp:
        'Under a platform only, a space must sync to that platform through a connection. The platform has to be set up under Connections below.',
      scope: 'Applies to',
      scopeOptions: { new: 'Spaces made from now on', all: 'Every space' },
      scopeHelp: 'Existing spaces get a grace period before the requirement applies to them.',
      graceDays: 'Grace period (days)',
      graceHelp:
        'How long an existing space has to follow a remote after the requirement began. After it the space is read only; nothing is deleted.',
      since: (when: string) => `The requirement took its present form on ${when}.`,
      affected: (inGrace: number, overdue: number, exempt: number) =>
        `${inGrace} in their grace period, ${overdue} past their deadline, ${exempt} exempt.`,
      overdueWarning: (n: number) => `${n} space(s) would be read only now.`,
    },
    hosts: {
      title: 'Allowed hosts',
      help: 'The hosts a space’s remote repository may be on, one per line; * allows any.',
      label: 'Hosts',
      fromFlag: 'The server was started with --remote-host, which wins. Remove the flag to set the hosts here.',
      invalid: 'Give at least one host name, without a scheme or a path.',
    },
    local: {
      title: 'Spaces without a remote',
      help: 'A space that is only on this server is the only copy of its pages, so it is never removed; it can only be limited.',
      maxBytes: 'Largest size (MiB)',
      maxHelp:
        'An upload or import that would take such a space over this is refused. Saving text is never refused. Leave empty for no limit; at least 1 MiB.',
      invalid: 'Give a whole number of MiB, at least 1, or leave it empty.',
    },
    cache: {
      title: 'Spaces that follow a remote',
      help: 'The server keeps the current text and the index of these spaces; old versions are fetched when someone asks for them, and a space nobody uses is evicted down to its index.',
      partial: 'Clone partially',
      partialHelp:
        'New clones keep only the current version’s files. Old versions are fetched from the remote when someone opens them.',
      partialOff: 'Needs Git 2.44 or later.',
      evictAfter: 'Evict after (days)',
      evictAfterHelp:
        'A space not used for this long is evicted: its files are removed, its index and records stay, and the next use restores it. Leave empty to never evict by age.',
      minIdle: 'Never evict a space used within (hours)',
      minIdleHelp: 'Also when the disk is short.',
      clones: 'Budget for clones (GiB)',
      clonesHelp:
        'The most the clones of these spaces may take together; the least recently used are evicted above it. Leave empty for none; at least 1 GiB.',
      minFree: 'Keep free on the disk (GiB)',
      minFreeHelp:
        'When the disk has less room than this, spaces are evicted as for an exceeded budget. Leave empty for none.',
      assetsBytes: 'Attachment cache (MiB)',
      assetsBytesHelp:
        'The contents of attachments that live in attachment repositories are kept here, shared by all spaces; the least recently read go first. At least 64 MiB.',
      invalid: 'Check the numbers: whole numbers, within the ranges shown.',
    },
    assets: {
      title: 'Attachment repositories',
      help: "Attachments go to a second repository, named <name>-assets, so the space's own stays small. Where they go, as a default or a rule, is set under Settings by layer (storage.assets).",
    },
    errors: {
      failed: 'Could not save the settings.',
      codes: {
        provider_not_configured:
          'That platform is not set up on this server, so it cannot be required. Set it up under Connections first.',
        conflict: 'This setting cannot be changed now.',
      },
    },
  },
  // What each space shows in the administration's list.
  spaces: {
    cache: 'Copy on the server',
    caches: {
      local: 'Local only',
      full: 'Full clone',
      partial: 'Partial clone',
      evicted: 'Evicted',
      archived: 'Archived',
      restoring: 'Restoring',
    },
    cacheHelp: {
      local: 'Only on this server: never partially cloned or evicted.',
      full: 'Every version of every file is on the server.',
      partial: 'The current text and every commit; older versions are fetched on demand.',
      evicted: 'Only the index is on the server; the next use fetches the clone again.',
      archived: "Only the space's record is on the server; opening it fetches the text again.",
      restoring: 'Being fetched from the remote right now.',
    },
    filter: 'Show',
    filterAll: 'All spaces',
    filterOverdue: 'Past their deadline for a remote',
    lastUsed: (when: string) => `Last used ${when}`,
    neverUsed: 'Not used yet',
    sizes: (clone: string, index: string) => `Clone ${clone}, index ${index}`,
    assetsIn: (cached: string) => `Attachments in their own repository, ${cached} cached`,
    pinned: 'Pinned',
    exempt: 'Exempt',
    pin: 'Keep on the server (pin)',
    unpin: 'Stop keeping on the server',
    pinHelp: 'A pinned space is never evicted and its clone is made whole.',
    exemptOn: 'Leave out of the remote requirement',
    exemptOff: 'Apply the remote requirement',
    evict: 'Evict now',
    evictTitle: (name: string) => `Evict “${name}”?`,
    evictHelp:
      'Its files are removed from the server after checking that its remote has everything. Its index and records stay, and the next use fetches the clone again. Nothing is changed on the remote.',
    evictConfirm: 'Evict',
    evictedDone: 'Evicted.',
    pinnedDone: 'Pinned.',
    unpinnedDone: 'Unpinned.',
    exemptDone: 'Exemption changed.',
    changeFailed: 'Could not change this space’s storage.',
    errors: {
      busy: 'It was not evicted:',
      conflict:
        'It cannot be evicted: a space without a remote is the only copy of its pages, and a pinned one is kept.',
      unavailable:
        'The remote cannot be reached, and a space is not evicted without checking that the remote has everything.',
    },
    deadline: (when: string, overdue: boolean) => (overdue ? `Read only since ${when}` : `Needs a remote by ${when}`),
  },
  users: {
    remoteExempt: 'Remote exempt',
    remoteExemptHelp: 'The storage policy does not ask the spaces this user owns for a remote.',
    exemptDone: 'Saved.',
  },
  status: {
    title: 'Storage',
    spaces: 'Spaces by copy',
    spacesValue: (local: number, full: number, partial: number, evicted: number, restoring: number) =>
      `${local} local, ${full} full, ${partial} partial, ${evicted} evicted${restoring > 0 ? `, ${restoring} restoring` : ''}`,
    clones: 'Clones',
    indexes: 'Indexes',
    assetCache: 'Attachment cache',
    assetCacheValue: (used: string, budget: string) => `${used} of ${budget}`,
    budget: 'Budget for clones',
    noBudget: 'None',
    restoresWaiting: 'Restores waiting',
    remotes: 'Remote requirement',
    remotesValue: (inGrace: number, overdue: number) =>
      `${inGrace} in their grace period, ${overdue} past their deadline`,
  },
  // The attachment repository, in a space's settings.
  assets: {
    title: 'Attachment repository',
    intro:
      'Keep this space’s attachments in a second Git repository. The space’s own repository then holds only text and stays small; attachments are fetched when someone opens them and kept in a cache of limited size. Links in pages do not change.',
    needsRemote: 'The space has to follow a remote repository first.',
    address: 'Repository address',
    token: 'Token',
    connect: 'Connect',
    connecting: 'Connecting…',
    connected: 'Attachment repository connected.',
    connectFailed: 'Could not connect the attachment repository',
    disconnect: 'Disconnect',
    disconnectTitle: 'Disconnect the attachment repository?',
    disconnectHelp:
      'Only possible while no attachment has gone to it. The space goes on keeping its attachments in its own repository.',
    disconnected: 'Disconnected.',
    disconnectFailed: 'Could not disconnect',
    state: { ok: 'Reachable', offline: 'Cannot be reached', rewritten: 'History rewritten', error: 'Needs attention' },
    offlineBody:
      'Text is read and written as usual and cached attachments are shown; others show a placeholder and uploads wait.',
    keepsOld: 'The attachments that were already in the space stay in the space’s repository until you move them.',
    move: 'Move existing attachments',
    moveHelp:
      'Copies the files of assets/ to the attachment repository, checks that they are there, then removes assets/ from the space’s repository (its history keeps them). The space stays usable meanwhile.',
    moveStart: 'Move attachments',
    moveStarted: 'Moving the attachments.',
    moveFailed: 'Could not start the move',
    moveNothing: 'There are no attachments in the space to move.',
    progress: (done: number, total: number) => `${done} of ${total} files`,
    states: {
      copying: 'Copying to the attachment repository',
      switching: 'Taking attachments out of the space’s repository',
      done: 'Moved',
      failed: 'The move stopped',
    },
    retry: 'Try again',
    newSpace: 'Sync attachments to a place of their own',
    newSpaceHelp: 'Makes a second one, <name>-assets, next to it, so the space itself stays small and quick.',
    placeholder: 'This attachment is not on the server and cannot be fetched right now.',
  },
  // What the people who use a space see.
  notice: {
    graceTitle: 'This space needs a remote repository',
    graceHelp: (when: string) =>
      `This server asks every space to follow a remote repository. This one has until ${when}; after that it is read only (nothing is deleted).`,
    overdueTitle: 'This space is read only',
    overdueHelp: (when: string) =>
      `It was due to follow a remote repository by ${when}. You can read and search it, and drafts are kept; connect a remote repository and it can be changed again.`,
    providerHelp: (name: string) => `It must sync to ${name}.`,
    connect: 'Connect a remote',
    dismiss: 'Put this away',
    personalConnect: 'Connect your account, then make a private place for your notes.',
  },
  // What the server answers that needs a sentence of its own.
  errors: {
    remoteRequired: (deadline?: string) =>
      deadline
        ? `This space is read only: it had to follow a remote repository by ${deadline}.`
        : 'This server requires a space to follow a remote repository.',
    notCached:
      'This version is not on the server yet and the remote repository cannot be reached right now. Try again in a moment.',
    fetching: 'This version is being fetched from the remote repository. Try again in a moment.',
    restoring: (phase: string) =>
      `This space was not used for a while and is being fetched again (${phase}). Try again in a moment.`,
    phases: {
      waiting: 'waiting for its turn',
      fetching: 'fetching',
      checkout: 'checking out',
      indexing: 'reading the files',
    },
    // A space waiting for its turn (a restore, or a rebuild of its index) or whose index is being rebuilt.
    preparing: {
      waiting: 'Waiting to prepare this space…',
      rebuilding: 'Preparing this space: its index is being built…',
    },
    spaceFull:
      'This would take the space over the size limit the administrator set for spaces that are not on a remote repository. Saving text still works.',
  },
}
