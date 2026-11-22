import { useEffect, useState } from 'react'

// `value`, but only after it has stopped changing for `ms`. The first value is
// available at once.
export function useDebounced<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value)
  useEffect(() => {
    if (Object.is(settled, value)) return
    const timer = setTimeout(() => setSettled(value), ms)
    return () => clearTimeout(timer)
  }, [value, settled, ms])
  return settled
}
