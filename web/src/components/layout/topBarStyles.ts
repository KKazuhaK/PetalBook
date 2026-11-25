// The look every control in the top bar shares, so the pages cannot drift apart: a 40 px bordered, rounded button
// (icon only, or with a label), and the segmented group that holds a few views or modes side by side (40 px in all).
// The bar itself is 56 px tall, so these sit 8 px from its lines. Kept apart from TopBar so the bell can use it too.
const base =
  'relative inline-flex shrink-0 items-center justify-center rounded-lg border border-input bg-background text-foreground outline-none transition-colors hover:bg-accent focus-visible:ring-3 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-60 aria-pressed:bg-muted data-popup-open:bg-accent'

/** The bar itself: 56 px under the safe area at the top, with room at the sides for a notch in landscape. */
export const topBarFrame =
  'sticky top-0 z-30 flex h-(--topbar-h) shrink-0 items-center gap-2 border-b bg-background/85 pt-[env(safe-area-inset-top)] pr-[max(0.75rem,env(safe-area-inset-right))] pl-[max(0.75rem,env(safe-area-inset-left))] backdrop-blur'

/** The room a bar has for its controls, in rem (the unit of the `@[..rem]/topbar` queries), read from any element in
 *  it, for the menus that are drawn outside the bar and so cannot use a container query. 0 when it is not laid out. */
export function topBarRoom(el: Element | null): number {
  const bar = el?.closest('header')
  if (!bar || bar.clientWidth === 0) return Infinity
  const cs = getComputedStyle(bar)
  const inner = bar.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight)
  return inner / parseFloat(getComputedStyle(document.documentElement).fontSize)
}

/** A 40 px square icon button. */
export const topBarButton = `${base} size-10`
/** A 40 px button with a label (and usually an icon). */
export const topBarTextButton = `${base} h-10 gap-1.5 px-3 text-sm font-medium`
/** The bordered group of a segmented switch; add `flex` or a container-query `display` to show it. */
export const topBarGroup = 'items-center rounded-lg border border-input p-0.5'
/** One 34 px item of the group; the chosen one is filled. */
export const topBarGroupItem = (on: boolean) =>
  `flex size-[34px] items-center justify-center rounded-md outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-60 ${on ? 'bg-muted text-foreground' : 'text-muted-foreground hover:text-foreground'}`
/** A status in the bar (not a button): the same 40 px line, plain text, no border. */
export const topBarStatus = 'inline-flex h-10 items-center gap-1 text-xs text-muted-foreground'
