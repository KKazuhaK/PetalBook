// How a listing is shown (list or cards) and what a card says. Pure, so it can
// be tested without a page.

import type { PageSummary } from '@/api/types'

export type ListViewMode = 'list' | 'cards'

// What a listing shows is told apart by its kind and, for a folder or a tag,
// by which one; the space is in the key too. A search is one setting for all
// queries.
export function viewKey(space: string, kind: string, name = ''): string {
  return `pb.view.${space}.${kind}${name === '' ? '' : `.${name}`}`
}

// Storage can be blocked or hold something else: the list is the default.
export function loadView(key: string): ListViewMode {
  try {
    return localStorage.getItem(key) === 'cards' ? 'cards' : 'list'
  } catch {
    return 'list'
  }
}

export function saveView(key: string, mode: ListViewMode): void {
  try {
    if (mode === 'list') localStorage.removeItem(key)
    else localStorage.setItem(key, mode)
  } catch {
    // Storage blocked: the choice lasts until the page is left.
  }
}

// The line above a card's text: `《title》｜subtitle`, or `《title》` without a
// subtitle. Null for an untitled page, whose card is only its text.
export function cardTitleLine(page: Pick<PageSummary, 'title' | 'subtitle' | 'untitled'>): string | null {
  if (page.untitled || !page.title) return null
  return page.subtitle ? `《${page.title}》｜${page.subtitle}` : `《${page.title}》`
}
