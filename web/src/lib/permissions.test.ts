import { describe, expect, it } from 'vitest'
import type { SpaceInfo } from '@/api/types'
import { can, permissionsOf } from './permissions'

const space = (permissions: SpaceInfo['permissions'], disabled = false): Pick<SpaceInfo, 'permissions' | 'disabled'> => ({ permissions, disabled })

describe('can', () => {
  it('is what the space says the caller may do, and nothing else', () => {
    const owner = space(['read', 'write', 'members', 'manage'])
    const editor = space(['read', 'write'])
    const viewer = space(['read'])
    expect(['read', 'write', 'members', 'manage'].map((p) => can(owner, p as 'read'))).toEqual([true, true, true, true])
    expect(['read', 'write', 'members', 'manage'].map((p) => can(editor, p as 'read'))).toEqual([true, true, false, false])
    expect(['read', 'write', 'members', 'manage'].map((p) => can(viewer, p as 'read'))).toEqual([true, false, false, false])
  })

  it('does not look at a role: a permission list no role has is believed as it is', () => {
    expect(can(space(['read', 'members']), 'members')).toBe(true)
    expect(can(space(['read', 'members']), 'write')).toBe(false)
  })

  it('grants nothing without a space (not known yet, or not the person\'s)', () => {
    expect(can(undefined, 'read')).toBe(false)
    expect(can(null, 'write')).toBe(false)
  })

  it('grants nothing in a disabled space, whatever the permissions they would have', () => {
    const off = space(['read', 'write', 'members', 'manage'], true)
    expect(permissionsOf(off)).toEqual({ read: false, write: false, members: false, manage: false })
    expect(permissionsOf(space(['read']))).toEqual({ read: true, write: false, members: false, manage: false })
  })
})
