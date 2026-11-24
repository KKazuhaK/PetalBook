// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from 'vitest'
import { dropOldSource, loadRawSource, saveRawSource, setAsideSource, takeOldSource } from './useSourceView'

describe('unfinished source text kept in the browser', () => {
  beforeEach(() => window.localStorage.clear())
  const A = { rev: 'r1', text: 'A' }
  const B = { rev: 'r2', text: 'B' }
  it('is read back with the rev it was typed on', () => {
    saveRawSource('u/s/p', A)
    expect(loadRawSource('u/s/p')).toEqual(A)
    expect(loadRawSource('u/s/other')).toBeNull()
  })
  it('is set aside when taken, out of reach of what is typed next', () => {
    saveRawSource('k', A)
    expect(takeOldSource('k')).toEqual([A])
    expect(loadRawSource('k')).toBeNull()
    // Text kept and cleared afterwards is its own.
    saveRawSource('k', B)
    saveRawSource('k', null)
    expect(takeOldSource('k')).toEqual([A])
  })
  it('a second text set aside does not replace the first', () => {
    saveRawSource('k', A)
    takeOldSource('k')
    saveRawSource('k', B)
    expect(takeOldSource('k')).toEqual([A, B])
    // The same text twice is one.
    saveRawSource('k', B)
    expect(takeOldSource('k')).toEqual([A, B])
  })
  it('is removed one at a time', () => {
    saveRawSource('k', A)
    takeOldSource('k')
    saveRawSource('k', B)
    takeOldSource('k')
    expect(dropOldSource('k', A)).toEqual([B])
    expect(takeOldSource('k')).toEqual([B])
    expect(dropOldSource('k', B)).toEqual([])
    expect(window.localStorage.getItem('pb.source.old.k')).toBeNull()
  })
  it('something unreadable in storage is nothing', () => {
    window.localStorage.setItem('pb.source.k', '{"rev":1}')
    expect(loadRawSource('k')).toBeNull()
    window.localStorage.setItem('pb.source.old.k', 'not json')
    expect(takeOldSource('k')).toEqual([])
  })
})

describe('when the browser cannot store the list', () => {
  it('the text stays where it was and is still offered', () => {
    window.localStorage.clear()
    saveRawSource('k', { rev: 'r1', text: 'A' })
    const real = Storage.prototype.setItem
    Storage.prototype.setItem = function (key: string, value: string) {
      if (key.startsWith('pb.source.old.')) throw new DOMException('full', 'QuotaExceededError')
      real.call(this, key, value)
    }
    try {
      expect(takeOldSource('k')).toEqual([{ rev: 'r1', text: 'A' }])
      expect(loadRawSource('k')).toEqual({ rev: 'r1', text: 'A' })
    } finally {
      Storage.prototype.setItem = real
    }
    // Dropped by the person: gone from its first place too.
    expect(dropOldSource('k', { rev: 'r1', text: 'A' })).toEqual([])
    expect(loadRawSource('k')).toBeNull()
  })
})

describe("setting the view's text aside", () => {
  beforeEach(() => window.localStorage.clear())
  const C = { rev: 'r1', text: 'C' }
  it("stores it, says so, and clears the same text from the page's key", () => {
    saveRawSource('k', C)
    expect(setAsideSource('k', C)).toEqual({ stored: true, list: [C] })
    expect(loadRawSource('k')).toBeNull()
  })
  it("leaves other text under the page's key alone", () => {
    saveRawSource('k', { rev: 'r1', text: 'older keystroke' })
    expect(setAsideSource('k', C).stored).toBe(true)
    expect(loadRawSource('k')).toEqual({ rev: 'r1', text: 'older keystroke' })
  })
  it('says when it could not be stored, and changes nothing', () => {
    saveRawSource('k', C)
    const real = Storage.prototype.setItem
    Storage.prototype.setItem = function (key: string, value: string) {
      if (key.startsWith('pb.source.old.')) throw new DOMException('full', 'QuotaExceededError')
      real.call(this, key, value)
    }
    try {
      expect(setAsideSource('k', C)).toEqual({ stored: false, list: [] })
      expect(loadRawSource('k')).toEqual(C)
    } finally {
      Storage.prototype.setItem = real
    }
  })
  it('text that was never stored anywhere is stored now', () => {
    expect(setAsideSource('k', C)).toEqual({ stored: true, list: [C] })
  })
})
