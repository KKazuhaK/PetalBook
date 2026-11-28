// An organisation's email domains (3.6 d2): the claim, the DNS record that proves it, the daily recheck, and the
// confirmation before the feature is switched off.

import type { DomainsMessages } from '../domainsTypes.ts'

export const domainsEn: DomainsMessages = {
  title: 'Email domains',
  help: 'Claim the email domains your people use, then publish a DNS record to prove you manage each. Only that exact domain counts: claim subdomains one by one. A claim that is not verified does nothing.',
  featureOff: 'The email domains feature is off for this organization: the claims are kept and have no effect.',
  loadFailed: "Could not read this organization's domains",
  add: {
    label: 'Domain',
    placeholder: 'example.com',
    submit: 'Claim',
    adding: 'Claiming…',
    added: (domain: string) => `Claimed ${domain}. Publish one of the records below, then choose Check now.`,
  },
  empty: 'No domain is claimed yet.',
  states: {
    unverified: 'Unverified',
    verified: 'Verified',
    missing: (since: string) => `Missing since ${since}`,
    lapsed: 'Lapsed',
  },
  restrictedBadge: 'No other organizations',
  mixedBadge: 'Mixed scripts',
  mixedHelp:
    'A part of this domain mixes scripts (such as a Cyrillic letter among Latin ones), which is how a name is made to look like another. Make sure it is the one you mean before you trust or move it.',
  // The site's list: every organization's claims, and moving a verified domain to another organization.
  site: {
    title: 'Email domains',
    help: 'The email domains every organization has claimed. A verified domain can be moved to another organization: its record in DNS keeps counting.',
    loadFailed: 'Could not read the domains',
    empty: 'No organization has claimed a domain yet.',
    more: 'Load more',
    tenant: (name: string) => `Organization: ${name}`,
    transfer: 'Transfer',
    transferLabel: (domain: string) => `Transfer ${domain}`,
    transferTitle: (domain: string) => `Transfer ${domain}`,
    transferHelp:
      'Move this verified domain to another organization. The one that holds it loses it; each sees only its own side in its audit log.',
    to: 'To',
    pick: 'Choose an organization',
    confirm: 'Transfer',
    cancel: 'Cancel',
    done: (domain: string, name: string) => `Moved ${domain} to ${name}`,
  },
  punycode: (key: string) => `In DNS: ${key}`,
  records: {
    title: 'Publish one of these records',
    help: "Add either record to the domain's DNS. The code is shown here this once.",
    type: 'Type',
    name: 'Name',
    value: 'Value',
    copy: (what: string) => `Copy the ${what}`,
    copied: 'Copied',
    copyFailed: 'Could not copy: select it and copy it by hand.',
    txtFirst: 'TXT (recommended)',
    cname: 'CNAME',
  },
  hidden: {
    title: 'Name of the record',
    help: 'The code is shown once, when it is made. If you lost it, make a new one and publish the new record.',
  },
  code: {
    validUntil: (when: string) => `Code valid until ${when}`,
    expired: 'Code expired',
    renew: 'New code',
    renewing: 'Making a code…',
    renewed: 'A new code is made: publish the new record. The old one no longer counts.',
  },
  check: {
    now: 'Check now',
    checking: 'Checking…',
    again: (s: number) => `Check again in ${s} s`,
    last: (when: string, by: string) => `Last checked ${when} (${by})`,
    by: { manual: 'by hand', daily: 'daily recheck' },
    results: {
      found: (via: string) => `The record was found (${via}).`,
      missing: 'No record was found.',
      wrong_value: 'A record is there, but not with this code:',
      dns_error: 'DNS could not be asked; this does not count as missing:',
    },
    verified: (domain: string) => `${domain} is verified.`,
    nextDaily: (when: string) => `Next daily recheck: ${when}`,
    lapsedHelp:
      'Its record was missing for 7 days, so the claim lapsed. Make a new code, publish the record and check it to verify it again.',
    missingHelp: 'The daily recheck does not find its record. If it is still missing after 7 days, the claim lapses.',
  },
  restrict: {
    label: 'Keep others from making organizations on this domain',
    help: 'Registering a new organization with an address of this domain is refused, with a pointer to yours. It works for verified domains only.',
    on: 'On: nobody else can make an organization on this domain.',
    off: 'Off: others can make organizations on this domain.',
  },
  remove: {
    action: 'Remove',
    label: (domain: string) => `Remove ${domain}`,
    title: (domain: string) => `Remove ${domain}?`,
    body: 'The claim and its checks are deleted. To use the domain later, claim and verify it again.',
    verifiedBody:
      'This domain is verified: removing it stops it at once. Another organization can verify it afterwards.',
    confirm: 'Remove',
    cancel: 'Cancel',
    done: (domain: string) => `Removed ${domain}`,
  },
  failed: 'That did not work',
  // Before a feature that is in use is switched off (change_capabilities).
  capability: {
    title: (feature: string) => `Turn off ${feature}?`,
    help: 'This feature is in use. Turning it off stops what these organizations have of it, and deletes nothing: turning it on again brings it all back as it was.',
    loading: 'Counting what would stop…',
    line: (domains: number, verified: number) =>
      `${domains} ${domains === 1 ? 'domain' : 'domains'}, ${verified} verified`,
    typeLabel: (n: number) => `Type ${n} to confirm`,
    confirm: 'Turn off',
    cancel: 'Cancel',
    failed: 'That did not work',
  },
}
