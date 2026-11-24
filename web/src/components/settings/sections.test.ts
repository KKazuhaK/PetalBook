import { describe, expect, it } from 'vitest'
import {
  defaultSection,
  groupsOf,
  isSectionId,
  resolveSection,
  sectionFromPath,
  settingsPath,
  visibleSections,
  type Visibility,
} from './sections'

const single: Visibility = { mode: 'single', signedIn: false, hasSpace: true, canRead: true, canWrite: true }
const member: Visibility = { mode: 'accounts', signedIn: true, hasSpace: true, canRead: true, canWrite: true }
const viewer: Visibility = { ...member, canWrite: false }
const ids = (v: Visibility) => visibleSections(v).map((s) => s.id)

describe('which sections are shown', () => {
  it("single-user mode has only what applies: preferences, and the space's general, types, templates and import", () => {
    expect(ids(single)).toEqual(['preferences', 'general', 'types', 'templates', 'offline', 'import'])
  })
  it('a member has the account and the space, with its members, sync and the offline copy', () => {
    expect(ids(member)).toEqual([
      'profile',
      'preferences',
      'security',
      'tokens',
      'connections',
      'usage',
      'general',
      'types',
      'templates',
      'members',
      'sync',
      'offline',
      'import',
    ])
  })
  it('someone who may only read has no import (it writes pages) but still sees the members', () => {
    expect(ids(viewer)).toEqual([
      'profile',
      'preferences',
      'security',
      'tokens',
      'connections',
      'usage',
      'general',
      'types',
      'templates',
      'members',
      'sync',
      'offline',
    ])
  })
  it('a space that cannot be read shows no members', () => {
    expect(ids({ ...member, canRead: false, canWrite: false })).toEqual([
      'profile',
      'preferences',
      'security',
      'tokens',
      'connections',
      'usage',
      'general',
      'sync',
    ])
  })
  it('the offline copy is for whoever may read the space, in either mode, and not for a space that cannot be read', () => {
    expect(ids(single)).toContain('offline')
    expect(ids(viewer)).toContain('offline')
    expect(ids({ ...member, canRead: false, canWrite: false })).not.toContain('offline')
    expect(ids({ ...member, hasSpace: false })).not.toContain('offline')
  })
  it('a space that is switched off still has the members section, for leaving it', () => {
    expect(ids({ ...member, canRead: false, canWrite: false, disabled: true })).toEqual([
      'profile',
      'preferences',
      'security',
      'tokens',
      'connections',
      'usage',
      'general',
      'members',
      'sync',
    ])
  })
  it('users and invites are not a section here any more: they are in the administration', () => {
    expect(ids(member)).not.toContain('users')
    expect(isSectionId('users')).toBe(false)
  })
  it('without a space the space sections are not offered', () => {
    expect(ids({ ...member, hasSpace: false })).toEqual([
      'profile',
      'preferences',
      'security',
      'tokens',
      'connections',
      'usage',
    ])
    expect(ids({ ...single, hasSpace: false })).toEqual(['preferences'])
  })
  it('shows nothing until the session is known', () => {
    expect(ids({ mode: undefined, signedIn: false, hasSpace: true, canRead: true, canWrite: true })).toEqual([])
  })
  it('groups the navigation: account, space', () => {
    expect(groupsOf(visibleSections(member)).map((g) => [g.group, g.sections.map((s) => s.id)])).toEqual([
      ['account', ['profile', 'preferences', 'security', 'tokens', 'connections', 'usage']],
      ['space', ['general', 'types', 'templates', 'members', 'sync', 'offline', 'import']],
    ])
    expect(groupsOf(visibleSections(single)).map((g) => g.group)).toEqual(['account', 'space'])
  })
})

describe('settings addresses', () => {
  it('reads the section out of /settings/<section>', () => {
    expect(sectionFromPath('/settings/sync')).toBe('sync')
    expect(sectionFromPath('/settings/sync/')).toBe('sync')
    expect(sectionFromPath('/settings')).toBeNull()
    expect(sectionFromPath('/settings/nonsense')).toBeNull()
    expect(sectionFromPath('/s/x/recent')).toBeUndefined()
    expect(sectionFromPath('/settingsx')).toBeUndefined()
    expect(settingsPath('members')).toBe('/settings/members')
  })
  it('asks for the first section there is when none, or one that is not available, is named', () => {
    expect(defaultSection(member)).toBe('profile')
    expect(defaultSection(single)).toBe('preferences')
    expect(resolveSection(undefined, member)).toBe('profile')
    expect(resolveSection('users', member)).toBe('profile')
    expect(resolveSection('members', member)).toBe('members')
    expect(resolveSection('members', single)).toBe('preferences')
    expect(resolveSection('import', viewer)).toBe('profile')
    expect(resolveSection('profile', single)).toBe('preferences')
    expect(resolveSection('sync', single)).toBe('preferences')
    expect(resolveSection('bogus', member)).toBe('profile')
  })
})

describe('the sign-in link flow', () => {
  it('returns to the security section, where the linked sign-ins are', () => {
    expect(resolveSection('security', member)).toBe('security')
    expect(resolveSection('security', single)).toBe('preferences')
    expect(sectionFromPath('/settings/security')).toBe('security')
  })
})
