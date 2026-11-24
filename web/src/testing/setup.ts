// Runs in every test file (vite.config.ts, test.setupFiles).

import 'fake-indexeddb/auto'
import { afterAll } from 'vitest'
import { destroyEditors } from './editors'

afterAll(destroyEditors)
