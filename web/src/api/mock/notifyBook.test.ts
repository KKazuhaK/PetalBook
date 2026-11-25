import { describe, expect, it } from 'vitest'
import { createNotifyBook, PROBLEMS_MAX, type DerivedProblems } from './notifyBook'

// The in-memory notification centre answers as the server does (docs/api.md, "Notifications").
describe('the problems of the spaces in the mock', () => {
  it('names at most 50 spaces with problems, counts only those towards it, and says how many more', () => {
    const book = createNotifyBook({ now: () => 0 })
    const derived: DerivedProblems[] = []
    // Spaces without problems first: they do not use up the cap.
    for (let i = 0; i < 10; i++) derived.push({ id: `ok${i}`, name: `ok${i}`, counts: {} })
    for (let i = 0; i < PROBLEMS_MAX + 3; i++) derived.push({ id: `p${i}`, name: `p${i}`, counts: { no_id: 1 } })
    const list = book.list('U', derived, new URLSearchParams())
    expect(list.items.filter((x) => x.derived)).toHaveLength(PROBLEMS_MAX)
    expect(list.moreProblems).toBe(3)
    // Later pages, and the other kinds, do not repeat it.
    expect(book.list('U', derived, new URLSearchParams({ kind: 'invite' })).moreProblems).toBeUndefined()
    const few = book.list('U', derived.slice(0, 12), new URLSearchParams())
    expect(few.moreProblems).toBeUndefined()
  })
})
