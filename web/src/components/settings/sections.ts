// What the settings surface offers and where it lives in the address. Pure, so
// the rules (which sections a person sees, how an old address maps to one) are
// tested without rendering anything.

export type SectionId = 'profile' | 'preferences' | 'security' | 'tokens' | 'usage' | 'general' | 'types' | 'templates' | 'home' | 'members' | 'sync' | 'offline' | 'import'
export type GroupId = 'account' | 'space'

export interface SectionDef {
  id: SectionId
  group: GroupId
}

// In the order of the navigation.
export const SECTIONS: readonly SectionDef[] = [
  { id: 'profile', group: 'account' },
  { id: 'preferences', group: 'account' },
  { id: 'security', group: 'account' },
  { id: 'tokens', group: 'account' },
  { id: 'usage', group: 'account' },
  { id: 'general', group: 'space' },
  { id: 'types', group: 'space' },
  { id: 'templates', group: 'space' },
  { id: 'home', group: 'space' },
  { id: 'members', group: 'space' },
  { id: 'sync', group: 'space' },
  { id: 'offline', group: 'space' },
  { id: 'import', group: 'space' },
]

export interface Visibility {
  // `undefined` until the session has been read.
  mode: 'single' | 'accounts' | undefined
  signedIn: boolean
  // Whether there is a space to show settings of.
  hasSpace: boolean
  // Whether the person may read that space (the people of a space are for its
  // members to see). Not the case for a disabled one.
  canRead: boolean
  // Whether the person may change that space's pages (an import writes them).
  canWrite: boolean
  // Whether the person may manage that space (where its home page takes its text from is for them to say).
  canManage?: boolean
  // That space is switched off: its members cannot be listed, but a member may
  // still leave it, and that is on the members section.
  disabled?: boolean
}

// Single-user mode has no account, no remote repository and no members: only
// what applies is listed. Accounts mode lists the space sections while there is
// a space. (The administration is not a section here: it has its own address,
// /admin.)
export function visibleSections(v: Visibility): SectionDef[] {
  if (v.mode === undefined) return []
  return SECTIONS.filter((s) => {
    switch (s.id) {
      case 'profile':
      case 'security':
      case 'tokens':
      case 'usage':
        return v.mode === 'accounts' && v.signedIn
      case 'preferences':
        return true
      case 'general':
        return v.hasSpace
      case 'types':
      case 'templates':
        // Reading the types is reading the space; changing them is for those who manage it.
        return v.hasSpace && v.canRead
      case 'home':
        return v.hasSpace && v.canRead && v.canManage === true
      case 'import':
        return v.hasSpace && v.canWrite
      case 'members':
        return v.mode === 'accounts' && v.hasSpace && (v.canRead || v.disabled === true)
      case 'offline':
        // A copy of the space on this device: for anyone who may read it, in either mode.
        return v.hasSpace && v.canRead
      case 'sync':
        return v.mode === 'accounts' && v.hasSpace
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
//   /s/<space>/settings  -> sync (accounts) or general (single-user), for that space
// The space is returned so the caller can make it the one in use.
export function legacySettingsTarget(pathname: string, v: Visibility): { section: SectionId; space?: string } | null {
  if (pathname === '/settings' || pathname === '/settings/') return { section: defaultSection(v) }
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
