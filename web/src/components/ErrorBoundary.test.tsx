// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import * as stale from '@/lib/staleChunk'
import { t } from '@/i18n'

function Boom(): never {
  throw new TypeError('error loading dynamically imported module: http://x/assets/SettingsRoute-1.js')
}

let host: HTMLElement
let root: Root
const reload = vi.fn()

beforeEach(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  stale.resetStaleChunkForTests()
  reload.mockReset()
  sessionStorage.clear()
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
  vi.spyOn(stale.env, 'reload').mockImplementation(reload)
})
afterEach(() => {
  act(() => root.unmount())
  host.remove()
  vi.restoreAllMocks()
})

describe('ErrorBoundary and a chunk that is gone', () => {
  it('reloads once on the first failure', async () => {
    await act(async () =>
      root.render(
        <ErrorBoundary>
          <Boom />
        </ErrorBoundary>,
      ),
    )
    await vi.waitFor(() => expect(reload).toHaveBeenCalledTimes(1))
  })

  it('within 10 seconds it shows the update message with a full-reload button', async () => {
    sessionStorage.setItem(stale.RELOAD_KEY, String(Date.now() - 2000))
    await act(async () =>
      root.render(
        <ErrorBoundary>
          <Boom />
        </ErrorBoundary>,
      ),
    )
    expect(reload).not.toHaveBeenCalled()
    expect(host.textContent).toContain(t.errors.updated)
    const button = host.querySelector('button')!
    expect(button.textContent).toBe(t.errors.reloadPage)
    act(() => button.click())
    expect(reload).toHaveBeenCalledTimes(1)
  })
})
