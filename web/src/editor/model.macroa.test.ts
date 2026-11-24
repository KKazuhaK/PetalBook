import { afterEach, beforeEach, describe, it, vi } from 'vitest'
import { ONLY, SCALE, runSequence } from './modelHarness'

// One slice of the model-based test (see modelHarness.ts). It has a file of its own
// so the test runner runs the slices side by side.
describe('model-based: random sequences against the in-memory server', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  // The sequences are cut in two halves, one file each, that together are seeds 600_000 up to 600_000 + 3500 * SCALE.
  it('holds them with slow requests, moves and notices interleaved with typing and the second device, and the swept/offline macros (first half)', async () => {
    if (ONLY !== undefined) return
    for (let seed = 600_000 + 0; seed < 600_000 + Math.floor(Math.floor(3500 * SCALE) / 2); seed++)
      await runSequence(seed, { steps: 14, profile: 'macro' })
  }, 240_000)
})
