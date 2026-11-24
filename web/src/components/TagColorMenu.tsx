import { createContext, useContext, useState, type MouseEvent, type ReactNode, type TouchEvent } from 'react'
import { Check, Ban, Palette } from 'lucide-react'
import { toast } from 'sonner'
import { useSetTagColor, useSpacePermissions } from '@/api/hooks'
import type { TagColor } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
} from '@/components/ui/context-menu'
import { TAG_COLORS, TAG_HUES, normalizeHex, parseTagColor } from '@/lib/tagColors'
import { t } from '@/i18n'

// Tags in a list share one menu, like the file listing does: a tag carries its
// name in `data-tag`, and a right-click, a long press or its "…" button tell the
// menu which tag it is for. The menu exists only while open, and a sidebar with
// hundreds of tags builds one of it, not hundreds.
export const TAG_ATTR = 'data-tag'

const Open = createContext<((anchor: HTMLElement) => void) | null>(null)

// For the "…" button of a tag: asks the menu to open as if the tag had been
// right-clicked, just below the button.
export function useOpenTagColorMenu() {
  return useContext(Open)
}

export function TagColorMenu({
  children,
  className,
  colorOf,
}: {
  children: ReactNode
  className?: string
  colorOf: (tag: string) => TagColor | undefined
}) {
  const [tag, setTag] = useState<string | null>(null)
  const set = useSetTagColor()
  const { write } = useSpacePermissions()

  const capture = (e: MouseEvent) => {
    const el = (e.target as Element).closest?.(`[${TAG_ATTR}]`)
    const name = el?.getAttribute(TAG_ATTR) ?? null
    // Not on a tag: no menu of ours.
    if (name === null) return e.stopPropagation()
    setTag(name)
  }

  // A long press opens the menu without a contextmenu event: the tag under
  // the finger is noted when the touch begins (none: no tag to colour).
  const note = (e: TouchEvent) => {
    setTag((e.target as Element).closest?.(`[${TAG_ATTR}]`)?.getAttribute(TAG_ATTR) ?? null)
  }

  const openAt = (anchor: HTMLElement) => {
    const r = anchor.getBoundingClientRect()
    anchor.dispatchEvent(
      new window.MouseEvent('contextmenu', {
        bubbles: true,
        cancelable: true,
        clientX: r.left,
        clientY: r.bottom,
        button: 2,
      }),
    )
  }

  // `of` names the tag when it is not the one the menu was opened on (the
  // custom colour dialog carries its own).
  const pick = (color: TagColor | null, of: string | null = tag) => {
    if (of === null) return
    set.mutate(
      { tag: of, color },
      { onError: (e) => toast.error(t.fmt.labelled(t.tagColor.failed, e instanceof Error ? e.message : '')) },
    )
  }
  const current = tag === null ? undefined : colorOf(tag)
  const parsed = parseTagColor(current)
  const customHex = parsed?.kind === 'custom' ? parsed.hex : undefined
  const [customFor, setCustomFor] = useState<string | null>(null)

  // Colouring a tag changes the space: without `write` the tags are only shown.
  if (!write) return <div className={className}>{children}</div>
  return (
    <Open.Provider value={openAt}>
      <ContextMenu>
        <ContextMenuTrigger className={className} onContextMenuCapture={capture} onTouchStartCapture={note}>
          {children}
        </ContextMenuTrigger>
        <ContextMenuContent className="w-auto min-w-0 p-1.5">
          <div className="max-w-52 truncate px-1 pb-1.5 text-xs text-muted-foreground">
            {tag === null ? '' : t.tagColor.menu(tag)}
          </div>
          <div
            role="group"
            aria-label={tag === null ? t.tagColor.label : t.tagColor.menu(tag)}
            className="grid grid-cols-5 gap-1"
          >
            {TAG_COLORS.map((c) => (
              <ContextMenuItem
                key={c}
                aria-label={t.tagColor.names[c]}
                title={t.tagColor.names[c]}
                aria-current={current === c ? 'true' : undefined}
                onClick={() => pick(c)}
                className="size-7 justify-center rounded-full p-0"
              >
                <span
                  className="flex size-5 items-center justify-center rounded-full ring-1 ring-black/10"
                  style={{ background: TAG_HUES[c] }}
                >
                  {current === c && <Check className="size-3 text-white" />}
                </span>
              </ContextMenuItem>
            ))}
          </div>
          <ContextMenuItem
            aria-current={customHex ? 'true' : undefined}
            onClick={() => setCustomFor(tag)}
            className="mt-1"
          >
            <span
              className="flex size-4 items-center justify-center rounded-full ring-1 ring-black/10"
              style={customHex ? { background: customHex } : undefined}
            >
              {customHex ? <Check className="size-3 text-white" /> : <Palette className="size-3" />}
            </span>
            {t.tagColor.custom}
            {customHex && <span className="ml-auto pl-2 text-xs text-muted-foreground">{customHex}</span>}
          </ContextMenuItem>
          <ContextMenuSeparator />
          <ContextMenuItem disabled={current === undefined} onClick={() => pick(null)}>
            <Ban />
            {t.tagColor.none}
          </ContextMenuItem>
        </ContextMenuContent>
      </ContextMenu>
      {customFor !== null && (
        <CustomColorDialog
          tag={customFor}
          initial={customHex ?? (parsed?.kind === 'preset' ? TAG_HUES[parsed.name] : '#3b82f6')}
          onApply={(hex) => {
            pick(hex, customFor)
            setCustomFor(null)
          }}
          onClose={() => setCustomFor(null)}
        />
      )}
    </Open.Provider>
  )
}

// A native colour input and a hex field that follow each other; Apply sends the
// lower-case `#rrggbb`. It lives outside the menu, which closes on its own when
// "Custom…" is chosen, so typing is not caught by the menu's keyboard handling.
function CustomColorDialog({
  tag,
  initial,
  onApply,
  onClose,
}: {
  tag: string
  initial: string
  onApply: (hex: TagColor) => void
  onClose: () => void
}) {
  const [text, setText] = useState(initial)
  const hex = normalizeHex(text)
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-xs">
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            if (hex) onApply(hex)
          }}
        >
          <DialogHeader>
            <DialogTitle>{t.tagColor.customTitle(tag)}</DialogTitle>
          </DialogHeader>
          <div className="flex items-center gap-2">
            <input
              type="color"
              aria-label={t.tagColor.picker}
              value={hex ?? '#000000'}
              onChange={(e) => setText(e.target.value)}
              className="h-8 w-10 shrink-0 cursor-pointer rounded-md border border-input bg-transparent p-0.5"
            />
            <Input
              aria-label={t.tagColor.hex}
              value={text}
              onChange={(e) => setText(e.target.value)}
              aria-invalid={hex === null}
              spellCheck={false}
              autoComplete="off"
              maxLength={7}
              className="font-mono"
            />
          </div>
          {hex === null && <p className="-mt-2 text-xs text-destructive">{t.tagColor.hexInvalid}</p>}
          <DialogFooter>
            <Button type="submit" disabled={hex === null}>
              {t.tagColor.apply}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
