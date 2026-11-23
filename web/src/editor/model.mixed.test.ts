import { afterEach, beforeEach, describe, it, vi } from 'vitest'
import { BASE, MODEL_PROFILE, MODEL_STEPS, ONLY, SCALE, runSequence } from './modelHarness'

// One slice of the model-based test (see modelHarness.ts). It has a file of its own
// so the test runner runs the slices side by side.
describe('model-based: random sequences against the in-memory server', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it(`holds the invariants over ${ONLY === undefined ? Math.floor(2000 * SCALE) : 1} mixed sequences`, async () => {
    const seeds = ONLY === undefined ? Array.from({ length: Math.floor(2000 * SCALE) }, (_, i) => BASE + i) : [ONLY]
    for (const seed of seeds) await runSequence(seed, { steps: MODEL_STEPS, profile: MODEL_PROFILE })
  }, 120_000)

  it('holds them through long offline stretches and reloads', async () => {
    if (ONLY !== undefined) return
    for (let seed = 900_000; seed < 900_000 + Math.floor(1500 * SCALE); seed++) await runSequence(seed, { steps: 20, profile: 'offline' })
  }, 120_000)

  it('holds them through a very long offline session (thousands of pauses) with no dialog and a small list of own drafts', async () => {
    if (ONLY !== undefined) return
    for (let seed = 400_000; seed < 400_000 + Math.floor(40 * SCALE); seed++) await runSequence(seed, { steps: 6, profile: 'longoffline' })
  }, 240_000)

  it('holds them when the server commits a stored text while newer text waits, across reopenings, with no second device or with one', async () => {
    if (ONLY !== undefined) return
    for (let seed = 300_000; seed < 300_000 + Math.floor(600 * SCALE); seed++) await runSequence(seed, { steps: 8, profile: 'swept' })
    for (let seed = 200_000; seed < 200_000 + Math.floor(300 * SCALE); seed++) await runSequence(seed, { steps: 6, profile: 'stores' })
  }, 240_000)

  it('holds them over long sequences too', async () => {
    if (ONLY !== undefined) return
    for (let seed = 500_000; seed < 500_000 + Math.floor(300 * SCALE); seed++) await runSequence(seed, { steps: 40 })
  }, 120_000)
})
