import { useState, type FormEvent, type KeyboardEvent } from 'react'
import { toast } from 'sonner'
import { useCreatePage, useSession } from '@/api/hooks'
import { Spinner } from '@/components/common'
import { FormMessage } from '@/components/auth/Field'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { captureKey, loadCapture, noteBody, saveCapture } from '@/lib/capture'
import { th } from '@/i18n/home'

// A note written right on the home page: it goes to the space's inbox, as the quick note box does (and shares the
// text that was kept unsaved with it). Written, the box is empty again and the inbox below reads itself again.
export function QuickNote({ space, inbox }: { space: string; inbox: string }) {
  const user = useSession().data?.user?.id
  const key = captureKey(user, space)
  const [text, setText] = useState(() => loadCapture(key))
  const [error, setError] = useState('')
  const create = useCreatePage()
  const body = noteBody(text)

  const change = (value: string) => {
    setText(value)
    saveCapture(key, value)
  }
  const save = async () => {
    if (body === null || create.isPending) return
    setError('')
    try {
      await create.mutateAsync({ folder: inbox, body })
      saveCapture(key, '')
      setText('')
      toast.success(th.quick.saved)
    } catch (e) {
      setError(`${th.quick.failed}${e instanceof Error && e.message ? `: ${e.message}` : ''}`)
    }
  }
  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) {
      e.preventDefault()
      void save()
    }
  }
  return (
    <form
      className="grid gap-2"
      data-slot="home-quick-note"
      onSubmit={(e: FormEvent) => {
        e.preventDefault()
        void save()
      }}
    >
      <Textarea value={text} readOnly={create.isPending} onChange={(e) => change(e.target.value)} onKeyDown={onKeyDown} placeholder={th.quick.placeholder} aria-label={th.quick.label} className="max-h-[40dvh] min-h-20 resize-none overflow-y-auto" />
      <FormMessage>{error}</FormMessage>
      <div className="flex items-center justify-between gap-2">
        <span className="hidden text-xs text-muted-foreground sm:inline">{th.quick.hint}</span>
        <Button type="submit" size="sm" className="ml-auto" disabled={body === null || create.isPending}>
          {create.isPending && <Spinner />}
          {create.isPending ? th.quick.saving : th.quick.save}
        </Button>
      </div>
    </form>
  )
}
