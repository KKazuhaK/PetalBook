import { beforeEach, describe, expect, it, vi } from 'vitest'
import { isChunkLoadError, recoverFromStaleChunk, RELOAD_WINDOW_MS, resetStaleChunkForTests } from './staleChunk'

function memory() {
  const m = new Map<string, string>()
  return {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
  }
}

describe('isChunkLoadError', () => {
  it('matches the wording of Chrome, Firefox and Safari', () => {
    expect(isChunkLoadError(new TypeError('error loading dynamically imported module: http://x/assets/A-1.js'))).toBe(
      true,
    )
    expect(isChunkLoadError(new TypeError('Failed to fetch dynamically imported module: http://x/assets/A-1.js'))).toBe(
      true,
    )
    expect(isChunkLoadError(new TypeError('Importing a module script failed.'))).toBe(true)
    expect(isChunkLoadError(new Error('boom'))).toBe(false)
    expect(isChunkLoadError(null)).toBe(false)
  })
})

describe('recoverFromStaleChunk', () => {
  let storage: ReturnType<typeof memory>
  beforeEach(() => {
    resetStaleChunkForTests()
    storage = memory()
  })

  it('reloads exactly once for a chunk-load error, however often it is reported', async () => {
    const reload = vi.fn()
    const flush = vi.fn(async () => undefined)
    const deps = { storage, reload, flush, now: () => 1_000 }
    expect(recoverFromStaleChunk(deps)).toBe(true)
    expect(recoverFromStaleChunk(deps)).toBe(true) // the boundary reports the same failure
    await vi.waitFor(() => expect(reload).toHaveBeenCalledTimes(1))
    expect(flush).toHaveBeenCalledTimes(1)
  })

  it('does not reload again within 10 seconds', async () => {
    const reload = vi.fn()
    const flush = async () => undefined
    expect(recoverFromStaleChunk({ storage, reload, flush, now: () => 1_000 })).toBe(true)
    await vi.waitFor(() => expect(reload).toHaveBeenCalledTimes(1))
    resetStaleChunkForTests() // a fresh page after the reload keeps the session flag
    expect(recoverFromStaleChunk({ storage, reload, flush, now: () => 1_000 + RELOAD_WINDOW_MS - 1 })).toBe(false)
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('reloads again once the window has passed', async () => {
    const reload = vi.fn()
    const flush = async () => undefined
    recoverFromStaleChunk({ storage, reload, flush, now: () => 1_000 })
    await vi.waitFor(() => expect(reload).toHaveBeenCalledTimes(1))
    resetStaleChunkForTests()
    expect(recoverFromStaleChunk({ storage, reload, flush, now: () => 1_000 + RELOAD_WINDOW_MS })).toBe(true)
    await vi.waitFor(() => expect(reload).toHaveBeenCalledTimes(2))
  })

  it('flushes a pending draft before it reloads', async () => {
    const order: string[] = []
    let release!: () => void
    const flush = () =>
      new Promise<void>((r) => {
        order.push('flush-start')
        release = () => {
          order.push('flush-done')
          r()
        }
      })
    const reload = vi.fn(() => order.push('reload'))
    recoverFromStaleChunk({ storage, reload, flush, now: () => 1 })
    await vi.waitFor(() => expect(order).toEqual(['flush-start']))
    expect(reload).not.toHaveBeenCalled()
    release()
    await vi.waitFor(() => expect(order).toEqual(['flush-start', 'flush-done', 'reload']))
  })

  it('reloads even when the flush fails or hangs', async () => {
    const reload = vi.fn()
    recoverFromStaleChunk({ storage, reload, flush: () => Promise.reject(new Error('x')), now: () => 1 })
    await vi.waitFor(() => expect(reload).toHaveBeenCalledTimes(1))
    resetStaleChunkForTests()
    vi.useFakeTimers()
    const reload2 = vi.fn()
    recoverFromStaleChunk({
      storage: memory(),
      reload: reload2,
      flush: () => new Promise(() => undefined),
      now: () => 1,
    })
    await vi.advanceTimersByTimeAsync(5000)
    expect(reload2).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
  })
})
