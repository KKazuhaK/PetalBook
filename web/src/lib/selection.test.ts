import { describe, expect, it } from 'vitest'
import { applyClick, emptySelection, prune } from './selection'

const order = ['f:a', 'f:b', 'p:1', 'p:2', 'p:3']
const keysOf = (s: { keys: Set<string> }) => [...s.keys].sort()

describe('selection', () => {
  it('a click toggles one item and moves the anchor', () => {
    let s = applyClick(order, emptySelection(), 'p:2', {})
    expect(keysOf(s)).toEqual(['p:2'])
    expect(s.anchor).toBe('p:2')
    s = applyClick(order, s, 'f:a', {})
    expect(keysOf(s)).toEqual(['f:a', 'p:2'])
    s = applyClick(order, s, 'p:2', {})
    expect(keysOf(s)).toEqual(['f:a'])
    expect(s.anchor).toBe('p:2')
  })

  it('shift selects the run from the anchor, either way round, across folders and pages', () => {
    const base = applyClick(order, emptySelection(), 'f:b', {})
    expect(keysOf(applyClick(order, base, 'p:2', { shift: true }))).toEqual(['f:b', 'p:1', 'p:2'])
    const up = applyClick(order, applyClick(order, emptySelection(), 'p:3', {}), 'f:b', { shift: true })
    expect(keysOf(up)).toEqual(['f:b', 'p:1', 'p:2', 'p:3'])
    // The anchor stays, so the next shift-click measures from the same place.
    expect(up.anchor).toBe('p:3')
    expect(keysOf(applyClick(order, up, 'p:2', { shift: true }))).toEqual(['p:2', 'p:3'])
  })

  it('shift with nothing to measure from selects that item', () => {
    expect(keysOf(applyClick(order, emptySelection(), 'p:1', { shift: true }))).toEqual(['p:1'])
    const lost = { keys: new Set(['gone']), anchor: 'gone' }
    expect(keysOf(applyClick(order, lost, 'p:1', { shift: true }))).toEqual(['gone', 'p:1'])
  })

  it('does not change what it was given', () => {
    const s = applyClick(order, emptySelection(), 'p:1', {})
    applyClick(order, s, 'p:2', {})
    expect(keysOf(s)).toEqual(['p:1'])
  })

  it('drops what is no longer shown, and keeps the same object when nothing changed', () => {
    const s = { keys: new Set(['p:1', 'p:9']), anchor: 'p:9' }
    const pruned = prune(s, order)
    expect(keysOf(pruned)).toEqual(['p:1'])
    expect(pruned.anchor).toBeNull()
    const same = { keys: new Set(['p:1']), anchor: 'p:1' }
    expect(prune(same, order)).toBe(same)
  })
})
