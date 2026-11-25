// Tap areas on touch screens (the owner's rule: at least 40 px). A small control keeps its look and gets a bigger area
// to hit, by a transparent box around it that exists only on a phone-width screen or a coarse pointer, so the desktop
// does not change. The Tailwind classes are written out in full here so the build finds them.

// Grows the area by 8 px on every side (a 24 px control becomes 40 px).
export const touchHit =
  'relative max-md:before:absolute max-md:before:-inset-2 pointer-coarse:before:absolute pointer-coarse:before:-inset-2'

// For controls that sit side by side, where growing sideways would overlap the next one: 40 px wide by layout, and
// 8 px more above and below.
export const touchHitTall =
  'relative max-md:w-10 pointer-coarse:w-10 max-md:before:absolute max-md:before:inset-x-0 max-md:before:-inset-y-2 pointer-coarse:before:absolute pointer-coarse:before:inset-x-0 pointer-coarse:before:-inset-y-2'
