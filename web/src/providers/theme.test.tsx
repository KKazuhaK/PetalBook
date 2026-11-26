// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { ThemeToggle } from '@/components/layout/ThemeToggle'
import { t } from '@/i18n'
import { click, installDomShims, mount, settle, type FxMounted } from '@/testing/harness'

beforeAll(installDomShims)
beforeEach(() => {
  localStorage.clear()
  document.documentElement.removeAttribute('data-palette')
  vi.resetModules()
})
afterEach(() => vi.restoreAllMocks())

describe('the palette', () => {
  it('is applied as this module loads, before anything renders', async () => {
    localStorage.setItem('pb.palette', 'gruvbox')
    await import('./theme')
    expect(document.documentElement.getAttribute('data-palette')).toBe('gruvbox')
  })

  it('the default is no attribute at all', async () => {
    const { applyPalette } = await import('./theme')
    applyPalette('nord')
    expect(document.documentElement.getAttribute('data-palette')).toBe('nord')
    applyPalette('default')
    expect(document.documentElement.hasAttribute('data-palette')).toBe(false)
  })

  it('ignores a stored value that is not a palette', async () => {
    localStorage.setItem('pb.palette', 'neon')
    const { storedPalette } = await import('./theme')
    expect(storedPalette()).toBe('default')
    expect(document.documentElement.hasAttribute('data-palette')).toBe(false)
  })

  it('survives storage that throws', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    const { storedPalette } = await import('./theme')
    expect(storedPalette()).toBe('default')
  })
})

describe('the small theme toggle', () => {
  let m: FxMounted | undefined
  afterEach(async () => {
    await m?.unmount()
  })
  it('opens with light, dark and system, and choosing one is remembered', async () => {
    m = await mount(<ThemeToggle />, { fixtures: true })
    await click(document.querySelector(`[aria-label="${t.theme.toggle}"]`))
    await settle(() => document.querySelectorAll('[role="menuitemradio"]').length === 3, 'menu')
    expect([...document.querySelectorAll('[role="menuitemradio"]')].map((e) => e.textContent?.trim())).toEqual([
      t.theme.light,
      t.theme.dark,
      t.theme.system,
    ])
    await click([...document.querySelectorAll('[role="menuitemradio"]')][1])
    expect(localStorage.getItem('theme')).toBe('dark')
  })
})
