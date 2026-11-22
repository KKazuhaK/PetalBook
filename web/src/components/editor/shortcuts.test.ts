import { describe, expect, it } from 'vitest'
import { shortcutText, withShortcut } from './shortcuts'

describe('shortcut labels', () => {
  it('use symbols on a Mac and words elsewhere', () => {
    expect(shortcutText('Mod-b', true)).toBe('⌘B')
    expect(shortcutText('Mod-Shift-s', true)).toBe('⌘⇧S')
    expect(shortcutText('Mod-Alt-c', true)).toBe('⌘⌥C')
    expect(shortcutText('Mod-b', false)).toBe('Ctrl+B')
    expect(shortcutText('Mod-Shift-s', false)).toBe('Ctrl+Shift+S')
  })
  it('go after the name', () => {
    expect(withShortcut('Bold', 'Mod-b', false)).toBe('Bold (Ctrl+B)')
  })
})
