// The words of the platforms that need no App: a host an administrator set up with an OAuth application, which
// people connect their account to once (GitLab, Gitea, Forgejo, Codeberg). Everything that names the platform
// takes its name from the server, so no platform's name is written here.
export const forgeEn = {
  account: {
    intro: (name: string) => `Connect your ${name} account to sync new spaces to ${name}, or spaces you already have.`,
    userAuthOff: (name: string) => `People cannot connect their ${name} accounts on this server yet.`,
    userAuthOffAdmin: 'It needs the server’s public address (--public-url); see the administration, Storage & sync.',
    notConnected: (name: string) => `Your ${name} account is not connected.`,
    connect: (name: string) => `Connect ${name}`,
    connectHelp: (name: string) =>
      `Opens ${name} and asks it to let this server act for you there: create places to sync to, and read and write the ones you sync. You come back here when you have allowed it.`,
    startFailed: (name: string) => `Could not start connecting to ${name}.`,
    failed: (name: string) => `Could not connect to ${name}`,
    errors: {
      denied: (name: string) => `You did not allow it on ${name}.`,
      expired: () => 'The attempt took too long, or was not started from this browser. Try again.',
      provider_error: (name: string) => `${name} refused the request or could not be reached. Try again.`,
      account_in_use: (name: string) => `That ${name} account is connected to another user of this server.`,
      different_account: (name: string) =>
        `Spaces here sync through the ${name} account you connected before. Disconnect it first, or switch those spaces to another credential.`,
      unknown: () => 'Something went wrong.',
    },
    accountHelp: (name: string) => `The ${name} account connected to you.`,
    usedByHelp: 'Spaces that sync through your connection.',
    expiredBody: (name: string) =>
      `${name} no longer accepts it (it expired or was revoked). Spaces that sync through it can still be read; changes wait, and drafts are kept, until you connect again.`,
    disconnectTitle: (name: string) => `Disconnect ${name}?`,
    disconnectNone: (name: string) =>
      `No space syncs through this connection. The server also asks ${name} to revoke it, where ${name} allows that.`,
    disconnected: (name: string) => `${name} disconnected`,
    leftoverHelp: (name: string) =>
      `Making these spaces failed after their place on ${name} was made. It was kept, empty; nothing was deleted there. Sync a space to one and try again, or say you deleted it.`,
  },
  admin: {
    title: 'Hosts',
    intro:
      'A host with an OAuth application: people connect their own account to it once, and spaces sync with it as them. Each host has one application, which you make on the host and copy here.',
    loadFailed: 'Could not load the hosts',
    none: 'No host is set up.',
    add: 'Add a host',
    addTitle: 'Add a host',
    addHelp:
      'On the host, make an OAuth application with the callback address shown after saving (or shown below once it has an id), then enter its client id and secret. The server checks the host before it keeps anything.',
    typeLabel: 'Kind of host',
    typeGitLab: 'GitLab (gitlab.com or your own)',
    typeGitea: 'Gitea, Forgejo or Codeberg',
    id: 'Id',
    idHelp: 'Lowercase letters, digits and “-”, up to 40; it cannot be changed. It is in the callback address.',
    host: 'Host',
    hostHelp: 'The host name, with its port if it has one: gitlab.example.com. Always https.',
    name: 'Name people see',
    nameHelp: 'Optional; the host name is used if empty.',
    clientId: 'Client ID',
    clientSecret: 'Client secret',
    secretKept: 'A secret is saved. Enter one only to replace it.',
    secretHelp: 'Kept encrypted, and never shown again.',
    callback: 'Callback address',
    callbackHelp: 'Set this as the application’s redirect URI on the host:',
    noPublicUrl: 'The server has no public address (--public-url), so people cannot connect yet.',
    scopes: (list: readonly string[]) =>
      list.length > 0
        ? [...list].join(', ')
        : 'No scope: the application’s access is as wide as each person’s own on the host.',
    versionLabel: 'Version',
    scopesLabel: 'Asks for',
    stateOk: 'Usable',
    stateDown: 'Not usable',
    versionUnknown: 'The host does not say its version before someone signs in.',
    partial: 'Partial sync',
    partialYes: 'The host supports partial sync.',
    partialNo: 'The host does not support partial sync: spaces on it are kept whole on this server.',
    partialUnknown: 'Not known yet: found out the first time a space syncs with it.',
    usage: (spaces: number, users: number) =>
      `${spaces} space${spaces === 1 ? '' : 's'} sync through it, ${users} user${users === 1 ? ' is' : 's are'} connected`,
    notConfigured:
      'The host did not pass its last check, so nobody can use it. Check again once it is back, or fix the settings.',
    check: 'Check again',
    checking: 'Checking…',
    checked: 'Checked with the host',
    checkedAt: 'Last checked',
    save: 'Save',
    saving: 'Saving…',
    saved: 'The host is saved',
    cancel: 'Cancel',
    edit: 'Edit',
    remove: 'Remove',
    removeTitle: (name: string) => `Remove ${name}?`,
    removeDescription: 'Its settings are deleted from the server. Nothing on the host is removed.',
    removeBlocked: (spaces: number, users: number) =>
      `${spaces} space${spaces === 1 ? ' syncs' : 's sync'} through it and ${users} user${users === 1 ? ' is' : 's are'} connected. Switch the spaces and disconnect the people first.`,
    removed: 'The host is removed',
    needFields: 'Give the id, the host, the client id and the client secret.',
    errors: {
      failed: 'Something went wrong.',
      reasons: {
        host_invalid: 'The host is a name, with a port if it has one: no path and no user.',
        host_not_public: 'That host is not on a public address, so this server will not connect to it.',
        host_unreachable: 'The host could not be reached. Nothing was saved.',
        not_this_platform: 'That host does not answer as this kind of host.',
        unsupported_version: 'That version is too old: it has no authorisation with PKCE.',
        client_rejected: 'The host does not accept this client id and secret.',
        type_invalid: 'Choose a kind of host.',
      },
      codes: {
        exists: 'A host with this id, or on this host name, is set up already.',
        provider_limit: 'The server has as many hosts as it can have.',
        provider_in_use: 'Spaces still sync through it, or people are connected. Switch and disconnect those first.',
        invalid: 'The details are not right.',
        host_not_allowed: 'The storage settings do not allow this host. Allow it under Storage & sync first.',
      },
    },
  },
}
