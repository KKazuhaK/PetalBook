// The audited view of a space an administrator has open is the server's to say (`view` on the space: docs/api.md,
// "Viewing a space"); these only read it against the clock.
import type { AdminViewState } from '@/api/types'

// The view, when it is open still; null when there is none or its hour is over.
export function openView(view: AdminViewState | null | undefined, now = Date.now()): AdminViewState | null {
  return view && Date.parse(view.until) > now ? view : null
}
