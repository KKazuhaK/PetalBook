// The mark of PetalBook, as the sidebars show it. One asset, one size, so every header agrees.
export function Logo({ className = 'size-6 rounded-md' }: { className?: string }) {
  return <img src="/favicon.svg" alt="" className={className} />
}
