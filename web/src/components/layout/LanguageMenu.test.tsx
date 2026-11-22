// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { LanguageToggle } from './LanguageMenu'

// The menus are only built when opened, so a mistake in how they are put
// together shows up only then.
describe('language menu', () => {
  it('opens and lists both languages under their own names', async () => {
    ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    const host = document.createElement('div')
    document.body.appendChild(host)
    const root = createRoot(host)
    await act(async () => root.render(<LanguageToggle />))
    const button = host.querySelector('button')!
    await act(async () => {
      button.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      button.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
      button.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    const text = document.body.textContent ?? ''
    expect(text).toContain('中文')
    expect(text).toContain('English')
    await act(async () => root.unmount())
    host.remove()
  })
})
