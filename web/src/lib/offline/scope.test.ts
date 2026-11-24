import { describe, expect, it } from 'vitest'
import type { CatalogEntry } from '@/api/types'
import { wantedText } from './scope'
import { DEFAULT_CONFIG, RECENT_KEPT, type OfflineConfig } from './store'

const e = (id: string, folder = '', tags: string[] = []): CatalogEntry => ({ id, path: `${folder}/${id}.md`, folder, label: id, untitled: false, tags, type: '', updated: '2026-11-23T08:00:00Z', rev: id })
const catalog = [e('A', 'Inbox'), e('B', 'Inbox/sub'), e('C', 'Notes'), e('D', 'Notes/2026', ['poem']), e('E', 'Inboxes'), e('F', 'Other'), e('G', '')]
const text: OfflineConfig = { ...DEFAULT_CONFIG, mode: 'text' }
const none = { pages: [], folders: [] }

describe('the text a space keeps', () => {
  it('is nothing in the catalogue-only mode', () => {
    expect(wantedText(catalog, { ...text, mode: 'catalog' }, 'Inbox', { pages: ['C'], folders: [] }, [{ id: 'F', at: 1 }])).toEqual({ pinned: [], recent: [] })
  })

  it('is, by default, the inbox and below it, the favourites and the last opened', () => {
    const w = wantedText(catalog, text, 'Inbox', { pages: ['C'], folders: ['Other'] }, [
      { id: 'G', at: 9 },
      { id: 'C', at: 8 },
      { id: 'D', at: 7 },
    ])
    expect(w.pinned.sort()).toEqual(['A', 'B', 'C', 'F']) // Inboxes is another folder
    expect(w.recent).toEqual(['G', 'D']) // C is pinned already; the newest first
  })

  it('adds the chosen folders (and what is below them) and tags, and each part can be turned off', () => {
    const w = wantedText(catalog, { ...text, favourites: false, inbox: false, recent: false, folders: ['Notes'], tags: ['poem'] }, 'Inbox', { pages: ['F'], folders: [] }, [{ id: 'G', at: 1 }])
    expect(w).toEqual({ pinned: ['C', 'D'], recent: [] })
    expect(wantedText(catalog, { ...text, folders: ['Notes/2026'], tags: [] }, '', none, []).pinned).toEqual(['D'])
    expect(wantedText(catalog, { ...text, tags: ['poem'] }, '', none, []).pinned).toEqual(['D'])
  })

  it('leaves out what is not in the catalogue any more, and keeps at most 200 recent ones', () => {
    const opened = Array.from({ length: RECENT_KEPT + 50 }, (_, i) => ({ id: i === 3 ? 'GONE' : `G${i}`, at: 1000 - i }))
    const big = Array.from({ length: RECENT_KEPT + 50 }, (_, i) => e(`G${i}`))
    const w = wantedText(big, { ...text, favourites: false, inbox: false }, '', none, opened)
    expect(w.recent).not.toContain('GONE')
    expect(w.recent).toHaveLength(RECENT_KEPT)
    expect(w.recent[0]).toBe('G0')
  })
})
