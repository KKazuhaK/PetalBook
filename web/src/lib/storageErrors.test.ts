import { describe, expect, it } from 'vitest'
import { ApiError } from '@/api/client'
import { storageEn } from '@/i18n/storage/en'
import { storageZhCN } from '@/i18n/storage/zh-CN'
import { ts } from '@/i18n/storage'
import { storageErrorMessage } from './storageErrors'

const restoring = (phase: string) =>
  new ApiError(503, 'restoring', 'being prepared', undefined, 5, undefined, undefined, { error: 'restoring', phase })

// docs/api.md, "Server storage": a space waiting for its turn (a restore, or a
// rebuild of its index) or being rebuilt answers 503 restoring with a phase.
describe('the restoring answer', () => {
  it('says the space waits to be prepared, or is being prepared', () => {
    expect(storageErrorMessage(restoring('waiting'))).toBe(ts.errors.preparing.waiting)
    expect(storageErrorMessage(restoring('rebuilding'))).toBe(ts.errors.preparing.rebuilding)
  })
  it('keeps the restore sentence for the phases of a restore', () => {
    expect(storageErrorMessage(restoring('fetching'))).toBe(ts.errors.restoring(ts.errors.phases.fetching))
  })
  it('has the texts in both languages', () => {
    expect(storageEn.errors.preparing.waiting).toBe('Waiting to prepare this space…')
    expect(storageZhCN.errors.preparing.waiting).toBe('正在排队准备这个空间…')
    expect(storageZhCN.errors.preparing.rebuilding).not.toBe('')
  })
})
