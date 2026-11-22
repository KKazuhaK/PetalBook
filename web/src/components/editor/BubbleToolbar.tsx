import { useEffect, useState, type KeyboardEvent, type ReactNode } from 'react'
import type { Editor } from '@tiptap/core'
import { useEditorState } from '@tiptap/react'
import { BubbleMenu } from '@tiptap/react/menus'
import { Bold, Code, ExternalLink, Italic, Link2, Link2Off, Strikethrough, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { normalizeUrl } from '@/lib/url'
import { t } from '@/i18n/zh-CN'
import { resolveAddressIn } from './pageFiles'

const panel = 'flex items-center gap-0.5 rounded-lg bg-popover p-1 text-popover-foreground shadow-md ring-1 ring-foreground/10'

function Tool({ label, active, onClick, children }: { label: string; active?: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-sm"
      title={label}
      aria-label={label}
      aria-pressed={active}
      onMouseDown={(e) => e.preventDefault()}
      onClick={onClick}
      className={active ? 'bg-muted text-foreground' : 'text-muted-foreground'}
    >
      {children}
    </Button>
  )
}

// The URL box shared by both floating panels. Remounted (by key) whenever the
// link under the cursor changes so it always starts from that link's address.
function UrlInput({ editor, initial, onDone, autoFocus }: { editor: Editor; initial: string; onDone?: () => void; autoFocus?: boolean }) {
  const [value, setValue] = useState(initial)
  const apply = () => {
    const href = normalizeUrl(value)
    if (href) editor.chain().focus().extendMarkRange('link').setLink({ href }).run()
    onDone?.()
  }
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing) return
    if (e.key === 'Enter') {
      e.preventDefault()
      apply()
    } else if (e.key === 'Escape') {
      e.preventDefault()
      editor.commands.focus()
      onDone?.()
    }
  }
  return (
    <>
      <input
        autoFocus={autoFocus}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder={t.link.urlPlaceholder}
        aria-label={t.link.edit}
        className="h-7 w-56 rounded-md border border-input bg-background px-2 text-sm outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/50"
      />
      <Button type="button" size="sm" variant="ghost" onClick={apply}>
        {t.link.apply}
      </Button>
    </>
  )
}

// Whole-cell or node selections have their own handling; only a plain text
// range gets the formatting bubble.
const isTextRange = (sel: { empty: boolean }) => !sel.empty && !('node' in sel) && !('$anchorCell' in sel)

// Appears over selected text: bold, italic, strike, inline code, link.
export function SelectionBubble({ editor }: { editor: Editor }) {
  const [linkMode, setLinkMode] = useState(false)
  const s = useEditorState({
    editor,
    selector: ({ editor: e }) => ({
      bold: e.isActive('bold'),
      italic: e.isActive('italic'),
      strike: e.isActive('strike'),
      code: e.isActive('code'),
      link: e.isActive('link'),
      href: (e.getAttributes('link').href as string | undefined) ?? '',
    }),
  })

  // A new selection starts in the plain formatting row again.
  useEffect(() => {
    const reset = () => setLinkMode(false)
    editor.on('selectionUpdate', reset)
    return () => void editor.off('selectionUpdate', reset)
  }, [editor])

  return (
    <BubbleMenu
      editor={editor}
      pluginKey="selectionBubble"
      options={{ placement: 'top', offset: 8 }}
      shouldShow={({ editor: e, state }) => e.isEditable && isTextRange(state.selection) && !e.isActive('codeBlock')}
    >
      <div className={panel}>
        {linkMode ? (
          <UrlInput key={s.href} editor={editor} initial={s.href} autoFocus onDone={() => setLinkMode(false)} />
        ) : (
          <>
            <Tool label={t.toolbar.bold} active={s.bold} onClick={() => editor.chain().focus().toggleBold().run()}>
              <Bold />
            </Tool>
            <Tool label={t.toolbar.italic} active={s.italic} onClick={() => editor.chain().focus().toggleItalic().run()}>
              <Italic />
            </Tool>
            <Tool label={t.toolbar.strike} active={s.strike} onClick={() => editor.chain().focus().toggleStrike().run()}>
              <Strikethrough />
            </Tool>
            <Tool label={t.toolbar.code} active={s.code} onClick={() => editor.chain().focus().toggleCode().run()}>
              <Code />
            </Tool>
            <Tool label={t.toolbar.link} active={s.link} onClick={() => setLinkMode(true)}>
              <Link2 />
            </Tool>
          </>
        )}
      </div>
    </BubbleMenu>
  )
}

// Appears when the cursor rests inside a link: change the address, open it,
// or take the link off (the text stays).
export function LinkPopover({ editor }: { editor: Editor }) {
  const { href } = useEditorState({
    editor,
    selector: ({ editor: e }) => ({ href: (e.getAttributes('link').href as string | undefined) ?? '' }),
  })
  return (
    <BubbleMenu
      editor={editor}
      pluginKey="linkPopover"
      options={{ placement: 'bottom-start', offset: 6 }}
      shouldShow={({ editor: e, state }) => e.isEditable && state.selection.empty && e.isActive('link')}
    >
      <div className={panel}>
        <UrlInput key={href} editor={editor} initial={href} />
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          // The address is kept as written in the page; a file of the space is opened through the files endpoint.
          render={<a href={resolveAddressIn(editor, href)} target="_blank" rel="noopener noreferrer" />}
          title={t.link.open}
          aria-label={t.link.open}
          className="text-muted-foreground"
          nativeButton={false}
        >
          <ExternalLink />
        </Button>
        <Tool label={t.link.remove} onClick={() => editor.chain().focus().extendMarkRange('link').unsetLink().run()}>
          <Link2Off />
        </Tool>
      </div>
    </BubbleMenu>
  )
}

// The description box of the image popover. Remounted (by key) when another
// image is selected or the description changes from outside.
function AltInput({ editor, initial }: { editor: Editor; initial: string }) {
  const [value, setValue] = useState(initial)
  const apply = () => {
    editor.chain().focus().updateAttributes('image', { alt: value.trim() || null }).run()
  }
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing) return
    if (e.key === 'Enter') {
      e.preventDefault()
      apply()
    } else if (e.key === 'Escape') {
      e.preventDefault()
      editor.commands.focus()
    }
  }
  return (
    <>
      <input
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder={t.files.altPlaceholder}
        aria-label={t.files.altLabel}
        className="h-7 w-56 rounded-md border border-input bg-background px-2 text-sm outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/50"
      />
      <Button type="button" size="sm" variant="ghost" onClick={apply}>
        {t.files.apply}
      </Button>
    </>
  )
}

// Appears when an image is selected: its description (the alt text, which
// screen readers read and which shows when the file cannot be loaded), or take
// the image out of the page.
export function ImagePopover({ editor }: { editor: Editor }) {
  const { alt, from } = useEditorState({
    editor,
    selector: ({ editor: e }) => ({ alt: e.isActive('image') ? ((e.getAttributes('image').alt as string | null) ?? '') : '', from: e.state.selection.from }),
  })
  return (
    <BubbleMenu
      editor={editor}
      pluginKey="imagePopover"
      options={{ placement: 'bottom', offset: 6 }}
      shouldShow={({ editor: e, state }) => e.isEditable && 'node' in state.selection && e.isActive('image')}
    >
      <div className={panel}>
        <AltInput key={`${from}:${alt}`} editor={editor} initial={alt} />
        <Tool label={t.files.removeImage} onClick={() => editor.chain().focus().deleteSelection().run()}>
          <Trash2 />
        </Tool>
      </div>
    </BubbleMenu>
  )
}
