import type { SettingKind, Strictness } from './types.ts'

// Whether `value` is looser than `floor` under the key's strictness: the same rule as the server's, so that a layer
// under a tighten rule can grey out what it would refuse. false when there is nothing to compare.
export function isLooser(
  strictness: Strictness | undefined,
  kind: SettingKind,
  choices: string[] | undefined,
  value: unknown,
  floor: unknown,
): boolean {
  switch (strictness) {
    case 'higher':
    case 'lower':
      if (kind === 'boolean') {
        if (typeof value !== 'boolean' || typeof floor !== 'boolean' || value === floor) return false
        return value === (strictness === 'lower')
      }
      if (kind === 'limit') {
        // null is no limit, the loosest.
        if (floor === null || floor === undefined) return false
        if (value === null || value === undefined) return true
        if (typeof value !== 'number' || typeof floor !== 'number') return false
        return strictness === 'lower' ? value > floor : value < floor
      }
      return false
    case 'order':
      return (
        typeof value === 'string' &&
        typeof floor === 'string' &&
        (choices ?? []).indexOf(value) < (choices ?? []).indexOf(floor)
      )
    case 'subset': {
      if (!Array.isArray(value) || !Array.isArray(floor) || floor.includes('*')) return false
      return value.includes('*') || value.some((x) => !floor.includes(x))
    }
    default:
      return false
  }
}
