// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { noteWrote, visitKey, wroteKey } from '@/lib/homePrefs'
import { Greeting } from './Greeting'

beforeEach(() => localStorage.clear())

const vk = visitKey('u1', 's1')
const wk = wroteKey('u1', 's1')
const at = (h: number, min = 0, d = 25) => new Date(2026, 10, d, h, min)
// Opening the home page at `now`: what the hello says.
const open = (now: Date, over: { name?: string; variety?: boolean } = {}) =>
  renderToStaticMarkup(<Greeting now={now} name={over.name} visitKey={vk} wroteKey={wk} variety={over.variety ?? true} />).replace(/<[^>]*>/g, '')
const plain = (now: Date, name?: string) => open(now, { variety: false, name })
const JUST = /刚写完|写得不错/

describe('the greeting kept for the day', () => {
  it('is said again when the part of the day has changed', () => {
    expect(plain(at(8))).toBe('早上好')
    expect(plain(at(8))).toBe('早上好')
    expect(plain(at(21))).toBe('晚上好')
  })

  it('is said again when the name changes', () => {
    expect(plain(at(8), 'Ann')).toContain('Ann')
    expect(plain(at(8), 'Bo')).toContain('Bo')
    expect(plain(at(8), 'Bo')).not.toContain('Ann')
  })

  it('is said again when the variety is switched', () => {
    open(at(8))
    expect(open(at(8), { variety: false })).toBe('早上好')
  })

  it('is said again when "just wrote" no longer holds', () => {
    open(at(15, 0))
    noteWrote(wk, 'p1', at(15, 2))
    const just = open(at(15, 5))
    expect(just).toMatch(JUST)
    // Still the same a few minutes on.
    expect(open(at(15, 20))).toBe(just)
    // Thirty minutes after the save it is not "just wrote" any more.
    expect(open(at(15, 40))).not.toMatch(JUST)
  })

  it('does not keep "just wrote" into the evening after a save in the morning', () => {
    open(at(9, 0))
    noteWrote(wk, 'p1', at(9, 5))
    expect(open(at(9, 10))).toMatch(JUST)
    expect(open(at(23, 50))).not.toMatch(JUST)
  })

  it('stays the same while nothing it goes by has changed', () => {
    const first = open(at(10, 0))
    expect(open(at(10, 30))).toBe(first)
    expect(open(at(11, 59))).toBe(first)
  })
})
