// What the settings surface offers and where it lives in the address. Pure, so
// the rules (which sections a person sees, how an old address maps to one) are
// tested without rendering anything.

export type SectionId = 'profile' | 'preferences' | 'security' | 'general' | 'sync' | 'import' | 'users'
export type GroupId = 'account' | 'space' | 'admin'

export interface SectionDef {
  id: SectionId
  group: GroupId
}

// In the order of the navigation.
export const SECTIONS: readonly SectionDef[] = [
  { id: 'profile', group: 'account' },
  { id: 'preferences', group: 'account' },
  { id: 'security', group: 'account' },
  { id: 'general', group: 'space' },
  { id: 'sync', group: 'space' },
  { id: 'import', group: 'space' },
  { id: 'users', group: 'admin' },
]

export interface Visibility {
  // `undefined` until the session has been read.
  mode: 'single' | 'accounts' | undefined
  signedIn: boolean
  admin: boolean
  // Whether there is a space to show settings of.
  hasSpace: boolean
}

// Single-user mode has no account, no remote repository and no users: only what
// applies is listed. Accounts mode lists the space sections while there is a
// space, and the administrator's section for administrators.
export function visibleSections(v: Visibility): SectionDef[] {
  if (v.mode === undefined) return []
  return SECTIONS.filter((s) => {
    switch (s.id) {
      case 'profile':
      case 'security':
        return v.mode === 'accounts' && v.signedIn
      case 'preferences':
        return true
      case 'general':
      case 'import':
        return v.hasSpace
      case 'sync':
        return v.mode === 'accounts' && v.hasSpace
      case 'users':
        return v.mode === 'accounts' && v.signedIn && v.admin
    }
  })
}

export const groupsOf = (sections: readonly SectionDef[]): { group: GroupId; sections: SectionDef[] }[] => {
  const out: { group: GroupId; sections: SectionDef[] }[] = []
  for (const s of sections) {
    const last = out[out.length - 1]
    if (last && last.group === s.group) last.sections.push(s)
    else out.push({ group: s.group, sections: [s] })
  }
  return out
}

export const isSectionId = (v: unknown): v is SectionId => SECTIONS.some((s) => s.id === v)

export const settingsPath = (section: SectionId) => `/settings/${section}`

// Where `/settings` goes: the first section there is.
export const defaultSection = (v: Visibility): SectionId => visibleSections(v)[0]?.id ?? 'preferences'

// The section an address asks for, or what to show instead when it names one
// that does not exist or that this person does not have (a member asking for
// the administrator's page).
export function resolveSection(requested: string | undefined, v: Visibility): SectionId {
  const shown = visibleSections(v)
  if (isSectionId(requested) && shown.some((s) => s.id === requested)) return requested
  return defaultSection(v)
}

// `/settings/<section>` -> the section; `/settings` -> null (no section named);
// anything else is not a settings address.
export function sectionFromPath(pathname: string): SectionId | null | undefined {
  const m = /^\/settings(?:\/([^/]*))?\/?$/.exec(pathname)
  if (!m) return undefined
  return isSectionId(m[1]) ? m[1] : null
}

// The old addresses, and the section each one is now:
//   /settings            -> profile (accounts) or preferences (single-user)
//   /users               -> users
//   /s/<space>/settings  -> sync (accounts) or general (single-user), for that space
// The space is returned so the caller can make it the one in use.
export function legacySettingsTarget(pathname: string, v: Visibility): { section: SectionId; space?: string } | null {
  if (pathname === '/settings' || pathname === '/settings/') return { section: defaultSection(v) }
  if (pathname === '/users' || pathname === '/users/') return { section: 'users' }
  const m = /^\/s\/([^/]+)\/settings\/?$/.exec(pathname)
  if (m) {
    let space = m[1]
    try {
      space = decodeURIComponent(space)
    } catch {
      // Left as it is; the settings then show the space in use.
    }
    return { section: v.mode === 'single' ? 'general' : 'sync', space }
  }
  return null
}
