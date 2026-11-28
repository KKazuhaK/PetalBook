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
    case 'superset': {
      // A list is stricter the more it holds: looser when it lacks an entry the floor has.
      if (!Array.isArray(value) || !Array.isArray(floor)) return false
      return floor.some((x) => !value.includes(x))
    }
    case 'document':
      return captchaLooser(value, floor)
    default:
      return false
  }
}

interface CaptchaForm {
  on?: boolean
  when?: string
  failures?: number
}

// captcha.contexts, form by form: a form on above is looser off; of two that are on, asking always is stricter than
// asking after failures, and of two that ask after failures, fewer failures is stricter. A form off above is free.
function captchaLooser(value: unknown, floor: unknown): boolean {
  if (typeof value !== 'object' || typeof floor !== 'object' || !value || !floor) return false
  const v = value as Record<string, CaptchaForm | undefined>
  return Object.entries(floor as Record<string, CaptchaForm | undefined>).some(([name, f]) => {
    if (!f?.on) return false
    const c = v[name] ?? { on: false }
    if (!c.on) return true
    if (f.when === 'always') return c.when !== 'always'
    return c.when !== 'always' && (c.failures ?? 3) > (f.failures ?? 3)
  })
}
