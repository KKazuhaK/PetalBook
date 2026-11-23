import { describe, expect, it } from 'vitest'
import { defaultSection, groupsOf, isSectionId, legacySettingsTarget, resolveSection, sectionFromPath, settingsPath, visibleSections, type Visibility } from './sections'

const single: Visibility = { mode: 'single', signedIn: false, hasSpace: true, canRead: true, canWrite: true }
const member: Visibility = { mode: 'accounts', signedIn: true, hasSpace: true, canRead: true, canWrite: true }
const viewer: Visibility = { ...member, canWrite: false }
const ids = (v: Visibility) => visibleSections(v).map((s) => s.id)

describe('which sections are shown', () => {
  it('single-user mode has only what applies: preferences, and the space\'s general, types, templates and import', () => {
    expect(ids(single)).toEqual(['preferences', 'general', 'types', 'templates', 'import'])
  })
  it('a member has the account and the space, with its members and sync', () => {
    expect(ids(member)).toEqual(['profile', 'preferences', 'security', 'tokens', 'github', 'usage', 'general', 'types', 'templates', 'members', 'sync', 'import'])
  })
  it('someone who may only read has no import (it writes pages) but still sees the members', () => {
    expect(ids(viewer)).toEqual(['profile', 'preferences', 'security', 'tokens', 'github', 'usage', 'general', 'types', 'templates', 'members', 'sync'])
  })
  it('a space that cannot be read shows no members', () => {
    expect(ids({ ...member, canRead: false, canWrite: false })).toEqual(['profile', 'preferences', 'security', 'tokens', 'github', 'usage', 'general', 'sync'])
  })
  it('a space that is switched off still has the members section, for leaving it', () => {
    expect(ids({ ...member, canRead: false, canWrite: false, disabled: true })).toEqual(['profile', 'preferences', 'security', 'tokens', 'github', 'usage', 'general', 'members', 'sync'])
  })
  it('users and invites are not a section here any more: they are in the administration', () => {
    expect(ids(member)).not.toContain('users')
    expect(isSectionId('users')).toBe(false)
  })
  it('without a space the space sections are not offered', () => {
    expect(ids({ ...member, hasSpace: false })).toEqual(['profile', 'preferences', 'security', 'tokens', 'github', 'usage'])
    expect(ids({ ...single, hasSpace: false })).toEqual(['preferences'])
  })
  it('shows nothing until the session is known', () => {
    expect(ids({ mode: undefined, signedIn: false, hasSpace: true, canRead: true, canWrite: true })).toEqual([])
  })
  it('groups the navigation: account, space', () => {
    expect(groupsOf(visibleSections(member)).map((g) => [g.group, g.sections.map((s) => s.id)])).toEqual([
      ['account', ['profile', 'preferences', 'security', 'tokens', 'github', 'usage']],
      ['space', ['general', 'types', 'templates', 'members', 'sync', 'import']],
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

describe('the old addresses', () => {
  it('/settings goes to the profile (accounts) or the preferences (single-user)', () => {
    expect(legacySettingsTarget('/settings', member)).toEqual({ section: 'profile' })
    expect(legacySettingsTarget('/settings', single)).toEqual({ section: 'preferences' })
  })
  it('the sign-in link flow returns to the security section, where the linked sign-ins are', () => {
    expect(resolveSection('security', member)).toBe('security')
    expect(resolveSection('security', single)).toBe('preferences')
    expect(sectionFromPath('/settings/security')).toBe('security')
  })
  it('/users is not a settings address any more (the router sends it to /admin/users)', () => {
    expect(legacySettingsTarget('/users', member)).toBeNull()
  })
  it('/s/<space>/settings goes to sync (general in single-user mode), naming the space', () => {
    expect(legacySettingsTarget('/s/team/settings', member)).toEqual({ section: 'sync', space: 'team' })
    expect(legacySettingsTarget('/s/default/settings', single)).toEqual({ section: 'general', space: 'default' })
    expect(legacySettingsTarget('/s/%E5%AE%B6/settings/', member)).toEqual({ section: 'sync', space: '家' })
  })
  it('anything else is not an old settings address', () => {
    expect(legacySettingsTarget('/s/team/import', member)).toBeNull()
    expect(legacySettingsTarget('/recent', member)).toBeNull()
  })
})
