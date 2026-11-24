// The audited view of a space an administrator has open is the server's to say (`view` on the space: docs/api.md,
// "Viewing a space"); these only read it against the clock.
import type { AdminViewState } from '@/api/types'

// The view, when it is open still; null when there is none or its hour is over.
export function openView(view: AdminViewState | null | undefined, now = Date.now()): AdminViewState | null {
  return view && Date.parse(view.until) > now ? view : null
}

// Whether the space is one an administrator manages without being a member, with its view open now: only then is
// the page under the banner "read only, recorded". Outside a view the settings open without one (the banner is not
// there) and any page of the space asks for a reason first.
export function inOpenView(space: { role: string; view?: AdminViewState | null }, now = Date.now()): boolean {
  return space.role === '' && openView(space.view, now) !== null
}
