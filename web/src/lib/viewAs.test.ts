// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import { rememberSpace } from '@/api/hooks'
import { forgetViewAs, startViewAs } from './viewAs'

afterEach(() => {
  forgetViewAs()
  localStorage.clear()
  sessionStorage.clear()
})

describe('a view tab and the browser storage it shares with the administrator', () => {
  it('writes nothing to localStorage while the view is open, and again after it', () => {
    localStorage.setItem('pb.space', 'mine')
    startViewAs({ pathname: '/as/va_1' })
    rememberSpace('theirs')
    localStorage.setItem('pb.recent', 'x')
    localStorage.removeItem('pb.space')
    expect(localStorage.getItem('pb.space')).toBe('mine')
    expect(localStorage.getItem('pb.recent')).toBeNull()
    // The tab's own storage still works.
    expect(sessionStorage.getItem('petalbook.viewAs')).toBe('va_1')
    forgetViewAs()
    rememberSpace('again')
    expect(localStorage.getItem('pb.space')).toBe('again')
  })

  it('leaves an ordinary tab alone', () => {
    startViewAs({ pathname: '/' })
    rememberSpace('a')
    expect(localStorage.getItem('pb.space')).toBe('a')
  })
})
