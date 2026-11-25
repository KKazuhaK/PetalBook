// The look every control in the top bar shares, so the pages cannot drift apart: a 40 px bordered, rounded button
// (icon only, or with a label), and the segmented group that holds a few views or modes side by side (40 px in all).
// The bar itself is 56 px tall, so these sit 8 px from its lines. Kept apart from TopBar so the bell can use it too.
const base =
  'relative inline-flex shrink-0 items-center justify-center rounded-lg border border-input bg-background text-foreground outline-none transition-colors hover:bg-accent focus-visible:ring-3 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-60 aria-pressed:bg-muted data-popup-open:bg-accent'

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
