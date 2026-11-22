import { describe, expect, it } from 'vitest'
import { defaultSection, groupsOf, legacySettingsTarget, resolveSection, sectionFromPath, settingsPath, visibleSections, type Visibility } from './sections'

const single: Visibility = { mode: 'single', signedIn: false, admin: false, hasSpace: true }
const member: Visibility = { mode: 'accounts', signedIn: true, admin: false, hasSpace: true }
const admin: Visibility = { mode: 'accounts', signedIn: true, admin: true, hasSpace: true }
const ids = (v: Visibility) => visibleSections(v).map((s) => s.id)

describe('which sections are shown', () => {
  it('single-user mode has only what applies: preferences, and the space\'s general and import', () => {
    expect(ids(single)).toEqual(['preferences', 'general', 'import'])
  })
  it('a member has the account, the space (with its sync) and no administration', () => {
    expect(ids(member)).toEqual(['profile', 'preferences', 'security', 'general', 'sync', 'import'])
  })
  it('an administrator also has users and invites', () => {
    expect(ids(admin)).toEqual(['profile', 'preferences', 'security', 'general', 'sync', 'import', 'users'])
  })
  it('without a space the space sections are not offered', () => {
    expect(ids({ ...admin, hasSpace: false })).toEqual(['profile', 'preferences', 'security', 'users'])
    expect(ids({ ...single, hasSpace: false })).toEqual(['preferences'])
  })
  it('shows nothing until the session is known', () => {
    expect(ids({ mode: undefined, signedIn: false, admin: false, hasSpace: true })).toEqual([])
  })
  it('groups the navigation: account, space, admin', () => {
    expect(groupsOf(visibleSections(admin)).map((g) => [g.group, g.sections.map((s) => s.id)])).toEqual([
      ['account', ['profile', 'preferences', 'security']],
      ['space', ['general', 'sync', 'import']],
      ['admin', ['users']],
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
    expect(settingsPath('users')).toBe('/settings/users')
  })
  it('asks for the first section there is when none, or one that is not available, is named', () => {
    expect(defaultSection(member)).toBe('profile')
    expect(defaultSection(single)).toBe('preferences')
    expect(resolveSection(undefined, member)).toBe('profile')
    expect(resolveSection('users', member)).toBe('profile')
    expect(resolveSection('users', admin)).toBe('users')
    expect(resolveSection('profile', single)).toBe('preferences')
    expect(resolveSection('sync', single)).toBe('preferences')
    expect(resolveSection('bogus', admin)).toBe('profile')
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
  it('/users goes to users and invites', () => {
    expect(legacySettingsTarget('/users', admin)).toEqual({ section: 'users' })
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
