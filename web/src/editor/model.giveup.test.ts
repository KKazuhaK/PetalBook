import { afterEach, beforeEach, describe, it, vi } from 'vitest'
import { ONLY, SCALE, runSequence } from './modelHarness'

// One slice of the model-based test (see modelHarness.ts). It has a file of its own
// so the test runner runs the slices side by side.
describe('model-based: random sequences against the in-memory server', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('holds them when the person gives a text up and the second device later writes it', async () => {
    if (ONLY !== undefined) return
    for (let seed = 700_000; seed < 700_000 + Math.floor(2000 * SCALE); seed++) await runSequence(seed, { steps: 16, profile: 'giveup' })
  }, 120_000)
})
