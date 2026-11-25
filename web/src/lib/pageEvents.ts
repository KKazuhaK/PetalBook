import type { Page } from '@/api/types'

// A page saved in this tab by something other than its editor (the icon picker): the editor open on it takes the
// new file as its base at once, rather than meeting it at its next save as a change made elsewhere.
const target = new EventTarget()

export function announcePageSaved(space: string, page: Page) {
  target.dispatchEvent(new CustomEvent('saved', { detail: { space, page } }))
}

export function onPageSaved(fn: (space: string, page: Page) => void): () => void {
  const listener = (e: Event) => {
    const { space, page } = (e as CustomEvent<{ space: string; page: Page }>).detail
    fn(space, page)
  }
  target.addEventListener('saved', listener)
  return () => target.removeEventListener('saved', listener)
}
