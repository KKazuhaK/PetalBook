import { describe, expect, it } from 'vitest'
import { inOpenView, openView } from './adminView'

const now = Date.parse('2026-11-23T10:00:00Z')
const open = { since: '2026-11-23T09:30:00Z', until: '2026-11-23T10:30:00Z' }
const over = { since: '2026-11-23T08:30:00Z', until: '2026-11-23T09:30:00Z' }

describe('the audited view of a space', () => {
  it('is open until its hour is over', () => {
    expect(openView(open, now)).toEqual(open)
    expect(openView(over, now)).toBeNull()
    expect(openView(null, now)).toBeNull()
  })

  it('puts a non-member under the banner only while a view is open', () => {
    expect(inOpenView({ role: '', view: open }, now)).toBe(true)
    expect(inOpenView({ role: '', view: over }, now)).toBe(false)
    expect(inOpenView({ role: '', view: null }, now)).toBe(false)
    expect(inOpenView({ role: '' }, now)).toBe(false)
    // A member has no view and no banner.
    expect(inOpenView({ role: 'owner', view: open }, now)).toBe(false)
  })
})
