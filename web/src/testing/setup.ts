// Runs in every test file (vite.config.ts, test.setupFiles).

import { afterAll } from 'vitest'
import { destroyEditors } from './editors'

afterAll(destroyEditors)
