import { afterEach, beforeEach, describe, it, vi } from 'vitest'
import { ONLY, SCALE, runSequence } from './modelHarness'

// One slice of the model-based test (see modelHarness.ts). It has a file of its own
// so the test runner runs the slices side by side.
describe('model-based: random sequences against the in-memory server', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('holds them in a world where the second device mostly copies what the person sent or gave up', async () => {
    if (ONLY !== undefined) return
    for (let seed = 800_000; seed < 800_000 + Math.floor(3500 * SCALE); seed++)
      await runSequence(seed, { steps: 24, profile: 'copy' })
  }, 120_000)
})
