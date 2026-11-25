// The first letter of a name in a circle, for a row or a header: the same colour for the same name, so a person is
// recognised at a glance.
const TONES = [
  'bg-rose-100 text-rose-700 dark:bg-rose-950 dark:text-rose-200',
  'bg-amber-100 text-amber-700 dark:bg-amber-950 dark:text-amber-200',
  'bg-emerald-100 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-200',
  'bg-sky-100 text-sky-700 dark:bg-sky-950 dark:text-sky-200',
  'bg-violet-100 text-violet-700 dark:bg-violet-950 dark:text-violet-200',
]

export function Avatar({ name, size = 'sm' }: { name: string; size?: 'sm' | 'lg' }) {
  const letter = [...name.trim()][0]?.toUpperCase() ?? '?'
  let h = 0
  for (const c of name) h = (h * 31 + c.codePointAt(0)!) >>> 0
  return (
    <span
      aria-hidden="true"
      data-slot="avatar"
      className={`flex shrink-0 items-center justify-center rounded-full font-medium ${TONES[h % TONES.length]} ${size === 'lg' ? 'size-14 text-xl' : 'size-8 text-sm'}`}
    >
      {letter}
    </span>
  )
}
