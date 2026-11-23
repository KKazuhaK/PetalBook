import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent, type ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { PenLine } from 'lucide-react'
import { toast } from 'sonner'
import { tzOffset } from '@/lib/templates'
import { useActiveSpace, useCreatePage, useSession, useUrls } from '@/api/hooks'
import { can } from '@/lib/permissions'
import { FormMessage } from '@/components/auth/Field'
import { Spinner } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Textarea } from '@/components/ui/textarea'
import { captureKey, loadCapture, noteBody, saveCapture } from '@/lib/capture'
import { t } from '@/i18n'

const CaptureContext = createContext<{ open: () => void; available: boolean } | null>(null)

// What the sidebar button and the phone's floating button call.
export function useQuickCapture() {
  const ctx = useContext(CaptureContext)
  if (!ctx) throw new Error('useQuickCapture needs a QuickCaptureProvider')
  return ctx
}

// Another dialog (or the phone's sidebar drawer, which is one) is on screen:
// the shortcut must not stack a second one on it.
const dialogOpen = () => document.querySelector('[role="dialog"], [role="alertdialog"]') !== null

// Owns the box and the shortcut for the whole app shell, so the shortcut works from any
// page, the editor included.
export function QuickCaptureProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false)
  const { id, spaces } = useActiveSpace()
  const space = spaces.find((s) => s.id === id)
  // A note is a page, made in the inbox: it needs `write`, and an inbox to go to.
  const available = space !== undefined && !!space.inbox && can(space, 'write')

  const show = useCallback(() => setOpen(true), [])
  const busy = useRef(false)
  const setBusy = useCallback((b: boolean) => {
    busy.current = b
  }, [])
  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      // Option/Alt+Shift+N: browsers keep the Ctrl/⌘+Shift letters for themselves
      // (developer tools, private windows), and a page cannot take those back.
      if (!available || e.isComposing || !e.altKey || !e.shiftKey || e.metaKey || e.ctrlKey || e.code !== 'KeyN') return
      e.preventDefault()
      if (!dialogOpen()) setOpen(true)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [available])

  const value = useMemo(() => ({ open: show, available }), [show, available])
  return (
    <CaptureContext.Provider value={value}>
      {children}
      <Dialog open={open && available} onOpenChange={(o) => (o || !busy.current) && setOpen(o)}>
        {/* Mounted only while open, so the text is read from storage each time. */}
        {open && space && <CaptureForm space={space.id} inbox={space.inbox} onClose={() => !busy.current && setOpen(false)} onDone={() => setOpen(false)} onBusy={setBusy} />}
      </Dialog>
    </CaptureContext.Provider>
  )
}

function CaptureForm({ space, inbox, onClose, onDone, onBusy }: { space: string; inbox: string; onClose: () => void; onDone: () => void; onBusy: (busy: boolean) => void }) {
  const user = useSession().data?.user?.id
  const key = captureKey(user, space)
  const [text, setText] = useState(() => loadCapture(key))
  const [error, setError] = useState('')
  const create = useCreatePage()
  const navigate = useNavigate()
  const urls = useUrls()
  const field = useRef<HTMLTextAreaElement>(null)
  const body = noteBody(text)
  // The box cannot be closed while the note is on its way: opened again it
  // would offer the same text to be saved a second time.
  const pending = create.isPending
  useEffect(() => {
    onBusy(pending)
    return () => onBusy(false)
  }, [pending, onBusy])

  // The cursor goes to the end of what was left from last time.
  useEffect(() => {
    const el = field.current
    if (el) el.setSelectionRange(el.value.length, el.value.length)
  }, [])

  const change = (value: string) => {
    setText(value)
    // Kept as it is typed: Escape, a click outside or a closed tab loses nothing.
    saveCapture(key, value)
  }

  const save = async () => {
    if (body === null || create.isPending) return
    setError('')
    try {
      // An untitled page: the text is all there is (docs/design.md, 3.2).
      const page = await create.mutateAsync({ folder: inbox, body, tz: tzOffset() })
      saveCapture(key, '')
      onDone()
      toast.success(t.capture.saved, { action: { label: t.capture.open, onClick: () => navigate(urls.page(page.id)) } })
    } catch (e) {
      // The box stays open with the text.
      setError(t.fmt.labelled(t.capture.failed, e instanceof Error ? e.message : ''))
    }
  }

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) {
      e.preventDefault()
      void save()
    }
  }

  return (
    <DialogContent className="sm:max-w-md">
      <form
        className="grid gap-3"
        onSubmit={(e: FormEvent) => {
          e.preventDefault()
          void save()
        }}
      >
        <DialogHeader>
          <DialogTitle>{t.capture.title}</DialogTitle>
          <DialogDescription>{t.capture.help}</DialogDescription>
        </DialogHeader>
        <Textarea
          ref={field}
          autoFocus
          value={text}
          // Not while it is being saved: what is typed then would be in neither
          // the page nor the kept text.
          readOnly={create.isPending}
          onChange={(e) => change(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder={t.capture.placeholder}
          aria-label={t.capture.textLabel}
          // Grows with the text (field-sizing) up to half the screen, then scrolls.
          className="max-h-[50dvh] min-h-24 resize-none overflow-y-auto"
        />
        <FormMessage>{error}</FormMessage>
        <DialogFooter className="items-center sm:justify-between">
          <span className="hidden text-xs text-muted-foreground sm:inline">{t.capture.shortcutHint}</span>
          <div className="flex gap-2 sm:ml-auto">
            <Button type="button" variant="outline" onClick={onClose}>
              {t.capture.cancel}
            </Button>
            <Button type="submit" disabled={body === null || create.isPending}>
              {create.isPending && <Spinner />}
              {create.isPending ? t.capture.saving : t.capture.save}
            </Button>
          </div>
        </DialogFooter>
      </form>
    </DialogContent>
  )
}

// A round button in the corner on a phone, where there is no shortcut and the
// sidebar is a drawer. Listing pages place it; the editor does not.
export function QuickCaptureFab() {
  const { open, available } = useQuickCapture()
  if (!available) return null
  return (
    <button
      type="button"
      onClick={open}
      aria-label={t.capture.fab}
      title={t.capture.fab}
      className="fixed right-4 bottom-[calc(1rem+env(safe-area-inset-bottom))] z-40 flex size-12 items-center justify-center rounded-full bg-primary text-primary-foreground shadow-lg outline-none transition-transform hover:scale-105 focus-visible:ring-3 focus-visible:ring-ring/50 active:scale-95 md:hidden"
    >
      <PenLine className="size-5" />
    </button>
  )
}
