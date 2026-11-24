import { useEffect, useState, type KeyboardEvent, type ReactNode } from 'react'
import type { Editor } from '@tiptap/core'
import { useEditorState } from '@tiptap/react'
import { BubbleMenu } from '@tiptap/react/menus'
import { Bold, Code, ExternalLink, Italic, Link2, Link2Off, Strikethrough, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { normalizeUrl } from '@/lib/url'
import { t } from '@/i18n'
import { resolveAddressIn } from './pageFiles'
import { pageLinksOf } from './pageLinks'

const panel =
  'flex items-center gap-0.5 rounded-lg bg-popover p-1 text-popover-foreground shadow-md ring-1 ring-foreground/10'

function Tool({
  label,
  active,
  onClick,
  children,
}: {
  label: string
  active?: boolean
  onClick: () => void
  children: ReactNode
}) {
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
export function UrlInput({
  editor,
  initial,
  onDone,
  autoFocus,
}: {
  editor: Editor
  initial: string
  onDone?: () => void
  autoFocus?: boolean
}) {
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
            <Tool
              label={t.toolbar.italic}
              active={s.italic}
              onClick={() => editor.chain().focus().toggleItalic().run()}
            >
              <Italic />
            </Tool>
            <Tool
              label={t.toolbar.strike}
              active={s.strike}
              onClick={() => editor.chain().focus().toggleStrike().run()}
            >
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
  const { href, title } = useEditorState({
    editor,
    selector: ({ editor: e }) => ({
      href: (e.getAttributes('link').href as string | undefined) ?? '',
      title: (e.getAttributes('link').title as string | null | undefined) ?? null,
    }),
  })
  // A link to a page of the space opens that page in the app.
  const pageLinks = pageLinksOf(editor)
  const isPage = !!pageLinks && href !== '' && pageLinks.isPage(href)
  return (
    <BubbleMenu
      editor={editor}
      pluginKey="linkPopover"
      options={{ placement: 'bottom-start', offset: 6 }}
      shouldShow={({ editor: e, state }) => e.isEditable && state.selection.empty && e.isActive('link')}
    >
      <div className={panel}>
        <UrlInput key={href} editor={editor} initial={href} />
        {isPage ? (
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            onClick={() => pageLinks.openHref(href, title)}
            title={t.pageLinks.openInApp}
            aria-label={t.pageLinks.openInApp}
            className="text-muted-foreground"
          >
            <ExternalLink />
          </Button>
        ) : (
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
        )}
        <Tool label={t.link.remove} onClick={() => editor.chain().focus().extendMarkRange('link').unsetLink().run()}>
          <Link2Off />
        </Tool>
      </div>
    </BubbleMenu>
  )
}

const imageField =
  'h-7 w-[min(18rem,calc(100vw-9rem))] rounded-md border border-input bg-background px-2 text-sm outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/50 aria-invalid:border-destructive'

// The two fields of the image popover. What is typed is kept as a draft until
// it is applied (Enter, or leaving the field); then the document's own value
// is shown again. The address is the one written in the page
// (`/assets/<id>/x.png`), not the URL the picture is loaded from, and it
// cannot be emptied: an image without an address is not an image.
export function ImageFields({ editor, alt, src }: { editor: Editor; alt: string; src: string }) {
  const [draftAlt, setDraftAlt] = useState<string | null>(null)
  const [draftSrc, setDraftSrc] = useState<string | null>(null)
  const [refused, setRefused] = useState(false)

  // Compared with the document at the moment of applying, not with what was drawn
  // last: leaving a field right after Enter must not apply twice.
  const applyAlt = () => {
    if (draftAlt === null) return
    const next = draftAlt.trim() || null
    if (next !== ((editor.getAttributes('image').alt as string | null) || null))
      editor.chain().updateAttributes('image', { alt: next }).run()
    setDraftAlt(null)
  }
  const applySrc = (): boolean => {
    if (draftSrc === null) return true
    const next = draftSrc.trim()
    setDraftSrc(null)
    if (!next) {
      setRefused(true)
      return false
    }
    setRefused(false)
    if (next !== editor.getAttributes('image').src) editor.chain().updateAttributes('image', { src: next }).run()
    return true
  }
  const keys = (apply: () => boolean | void, revert: () => void) => (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing) return
    if (e.key === 'Enter') {
      e.preventDefault()
      if (apply() !== false) editor.commands.focus()
    } else if (e.key === 'Escape') {
      e.preventDefault()
      revert()
      editor.commands.focus()
    }
  }

  return (
    <div className="flex flex-col gap-1.5 p-1">
      <label className="flex items-center gap-2 text-xs text-muted-foreground">
        <span className="w-10 shrink-0">{t.files.descriptionLabel}</span>
        <input
          value={draftAlt ?? alt}
          onChange={(e) => setDraftAlt(e.target.value)}
          onBlur={applyAlt}
          onKeyDown={keys(applyAlt, () => setDraftAlt(null))}
          placeholder={t.files.altPlaceholder}
          aria-label={t.files.altLabel}
          className={imageField}
        />
      </label>
      <label className="flex items-center gap-2 text-xs text-muted-foreground">
        <span className="w-10 shrink-0">{t.files.addressLabel}</span>
        <input
          value={draftSrc ?? src}
          onChange={(e) => {
            setRefused(false)
            setDraftSrc(e.target.value)
          }}
          onBlur={() => void applySrc()}
          onKeyDown={keys(applySrc, () => {
            setDraftSrc(null)
            setRefused(false)
          })}
          placeholder={t.files.addressPlaceholder}
          aria-label={t.files.addressLabel}
          aria-invalid={refused || undefined}
          spellCheck={false}
          className={`${imageField} font-mono text-xs`}
        />
      </label>
      {refused && (
        <p role="alert" className="pl-12 text-xs text-destructive">
          {t.files.addressRequired}
        </p>
      )}
    </div>
  )
}

// Appears when an image is selected: its description (the alt text, which
// screen readers read and which shows when the file cannot be loaded), its
// address, or take the image out of the page.
export function ImagePopover({ editor }: { editor: Editor }) {
  const { alt, src, from } = useEditorState({
    editor,
    selector: ({ editor: e }) => ({
      alt: e.isActive('image') ? ((e.getAttributes('image').alt as string | null) ?? '') : '',
      src: e.isActive('image') ? ((e.getAttributes('image').src as string | null) ?? '') : '',
      from: e.state.selection.from,
    }),
  })
  return (
    <BubbleMenu
      editor={editor}
      pluginKey="imagePopover"
      // The panel is wider than most images; it starts at the image's left edge and is kept inside the window.
      options={{ placement: 'bottom-start', offset: 6, shift: { padding: 8 } }}
      shouldShow={({ editor: e, state }) => e.isEditable && 'node' in state.selection && e.isActive('image')}
    >
      <div className={`${panel} items-start`}>
        {/* Another image starts from its own values. */}
        <ImageFields key={from} editor={editor} alt={alt} src={src} />
        <Tool label={t.files.removeImage} onClick={() => editor.chain().focus().deleteSelection().run()}>
          <Trash2 />
        </Tool>
      </div>
    </BubbleMenu>
  )
}
