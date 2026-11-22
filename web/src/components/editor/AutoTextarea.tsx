import { useLayoutEffect, useRef, type KeyboardEvent, type Ref } from 'react'

// A one-logical-line text box that grows when a long title wraps. Enter does
// not insert a line break; it hands control on (to the next field).
export function AutoTextarea({
  value,
  onChange,
  onEnter,
  placeholder,
  className,
  label,
  ref,
}: {
  value: string
  onChange: (value: string) => void
  onEnter?: () => void
  placeholder: string
  className?: string
  label: string
  ref?: Ref<HTMLTextAreaElement>
}) {
  const own = useRef<HTMLTextAreaElement | null>(null)
  useLayoutEffect(() => {
    const el = own.current
    if (!el) return
    const fit = () => {
      el.style.height = 'auto'
      el.style.height = `${el.scrollHeight}px`
    }
    fit()
    // The width decides where the text wraps, and a box that was hidden when
    // its text arrived measured nothing: fit again whenever the width changes.
    if (typeof ResizeObserver === 'undefined') return
    let width = el.clientWidth
    const ro = new ResizeObserver(() => {
      if (el.clientWidth === width) return
      width = el.clientWidth
      fit()
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [value])

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    // Enter that picks an input-method candidate must not move focus.
    if (e.key === 'Enter' && !e.nativeEvent.isComposing) {
      e.preventDefault()
      onEnter?.()
    }
  }

  return (
    <textarea
      ref={(el) => {
        own.current = el
        if (typeof ref === 'function') ref(el)
        else if (ref) ref.current = el
      }}
      rows={1}
      value={value}
      placeholder={placeholder}
      aria-label={label}
      onChange={(e) => onChange(e.target.value.replace(/[\r\n]+/g, ' '))}
      onKeyDown={onKeyDown}
      className={`block w-full resize-none overflow-hidden border-0 bg-transparent p-0 outline-none ${className ?? ''}`}
    />
  )
}
