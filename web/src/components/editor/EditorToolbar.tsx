import { useEffect, useState, type ReactNode, useRef } from 'react'
import { scrollFadeClass, useScrollFade } from '@/components/ui/scroll-fade'
import type { Editor } from '@tiptap/core'
import { useEditorState } from '@tiptap/react'
import {
  Bold,
  ChevronDown,
  Code,
  Code2,
  Italic,
  Link2,
  List,
  ListChecks,
  ListOrdered,
  Minus,
  Quote,
  Redo2,
  Strikethrough,
  Superscript,
  Table2,
  Undo2,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { t } from '@/i18n'
import { UrlInput } from './BubbleToolbar'
import { canInsertFootnote, insertFootnote } from './footnote'
import { withShortcut } from './shortcuts'
import { TableSizePicker } from './table/TableSizePicker'
import { ColumnItems, RowItems, TableItems } from './table/TableMenus'
import { cellAtSelection, insertTable } from './table/tableOps'
import { UploadButtons } from './UploadButtons'

function Tool({
  label,
  shortcut,
  active,
  disabled,
  onClick,
  children,
}: {
  label: string
  shortcut?: string
  active?: boolean
  disabled?: boolean
  onClick: () => void
  children: ReactNode
}) {
  const title = shortcut ? withShortcut(label, shortcut) : label
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-sm"
      title={title}
      aria-label={label}
      aria-keyshortcuts={shortcut?.replace('Mod', 'Control')}
      aria-pressed={active}
      disabled={disabled}
      // Keep the text selection and focus in the editor while clicking.
      onMouseDown={(e) => e.preventDefault()}
      onClick={onClick}
      className={active ? 'bg-muted text-foreground' : 'text-muted-foreground'}
    >
      {children}
    </Button>
  )
}

const Sep = () => <span className="mx-1 h-4 w-px shrink-0 bg-border" aria-hidden />

// The levels the menu offers. Markdown has six, and a page that uses 5 or 6
// shows and keeps them; they are just not offered for new text.
const OFFERED_LEVELS = [1, 2, 3, 4]

function HeadingMenu({ editor, level, disabled }: { editor: Editor; level: number; disabled: boolean }) {
  const label = level ? t.toolbar.heading(level) : t.toolbar.paragraph
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        disabled={disabled}
        render={
          <Button
            type="button"
            variant="ghost"
            size="sm"
            title={t.toolbar.headingMenu}
            aria-label={`${t.toolbar.headingMenu}: ${label}`}
            className="min-w-[4.5rem] justify-between gap-1 text-muted-foreground"
            onMouseDown={(e) => e.preventDefault()}
          />
        }
      >
        {label}
        <ChevronDown className="size-3" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-40">
        <DropdownMenuRadioGroup
          value={String(level)}
          onValueChange={(v) => {
            const next = Number(v)
            const chain = editor.chain().focus()
            ;(next === 0 ? chain.setParagraph() : chain.setHeading({ level: next as 1 | 2 | 3 | 4 })).run()
          }}
        >
          <DropdownMenuRadioItem value="0" closeOnClick>
            {t.toolbar.paragraph}
          </DropdownMenuRadioItem>
          {OFFERED_LEVELS.map((n) => (
            <DropdownMenuRadioItem key={n} value={String(n)} closeOnClick>
              {t.toolbar.heading(n)}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

// A grid to pick the size of a new table; inside a table, its row, column and
// table actions instead (the keyboard's way to what the handles at the table's
// edges do with the pointer).
function TableMenu({ editor, inTable, disabled }: { editor: Editor; inTable: boolean; disabled: boolean }) {
  const [open, setOpen] = useState(false)
  const cell = useEditorState({ editor, selector: ({ editor: e }) => cellAtSelection(e.state) })
  const trigger = (
    <Button
      type="button"
      variant="ghost"
      size="icon-sm"
      title={t.toolbar.table}
      aria-label={t.toolbar.table}
      disabled={disabled}
      className={inTable ? 'bg-muted text-foreground' : 'text-muted-foreground'}
      onMouseDown={(e) => e.preventDefault()}
    />
  )
  if (inTable && cell) {
    return (
      <DropdownMenu>
        <DropdownMenuTrigger render={trigger}>
          <Table2 />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="min-w-56">
          <RowItems editor={editor} cell={cell} />
          <DropdownMenuSeparator />
          <ColumnItems editor={editor} cell={cell} />
          <DropdownMenuSeparator />
          <TableItems editor={editor} cell={cell} />
        </DropdownMenuContent>
      </DropdownMenu>
    )
  }
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger render={trigger}>
        <Table2 />
      </PopoverTrigger>
      <PopoverContent align="start" className="w-auto p-2">
        <TableSizePicker
          onPick={({ cols, rows }) => {
            setOpen(false)
            insertTable(editor, cols, rows)
          }}
        />
      </PopoverContent>
    </Popover>
  )
}

// Formatting. Shown above the page body and kept in view while scrolling; the
// same actions are on the Markdown shortcuts for those who know them. It is one
// row that scrolls sideways when the window (or half of it, side by side with
// the source) is narrower than the buttons.
export function EditorToolbar({ editor }: { editor: Editor }) {
  const fadeRef = useRef<HTMLDivElement>(null)
  useScrollFade(fadeRef)
  const [linkOpen, setLinkOpen] = useState(false)
  const s = useEditorState({
    editor,
    selector: ({ editor: e }) => ({
      level: e.isActive('heading') ? Number(e.getAttributes('heading').level) : 0,
      bold: e.isActive('bold'),
      italic: e.isActive('italic'),
      strike: e.isActive('strike'),
      code: e.isActive('code'),
      link: e.isActive('link'),
      href: (e.getAttributes('link').href as string | undefined) ?? '',
      // Code blocks take no formatting; a link needs text to sit on or a link to edit.
      canMark: e.can().toggleBold(),
      canLink: e.can().setLink({ href: 'https://example.com' }) && (!e.state.selection.empty || e.isActive('link')),
      bullet: e.isActive('bulletList'),
      ordered: e.isActive('orderedList'),
      task: e.isActive('taskList'),
      quote: e.isActive('blockquote'),
      codeBlock: e.isActive('codeBlock'),
      table: e.isActive('table'),
      footnote: canInsertFootnote(e.state),
      canUndo: e.can().undo(),
      canRedo: e.can().redo(),
    }),
  })
  const run = () => editor.chain().focus()

  // Ctrl/Cmd+K opens the link box, as in Docs and Notion.
  useEffect(() => {
    const dom = editor.view.dom
    const onKey = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() !== 'k' || !(e.metaKey || e.ctrlKey) || e.shiftKey || e.altKey) return
      if (
        !editor.can().setLink({ href: 'https://example.com' }) ||
        (editor.state.selection.empty && !editor.isActive('link'))
      )
        return
      e.preventDefault()
      setLinkOpen(true)
    }
    dom.addEventListener('keydown', onKey)
    return () => dom.removeEventListener('keydown', onKey)
  }, [editor])

  return (
    <div
      ref={fadeRef}
      role="toolbar"
      aria-label={t.toolbar.label}
      className={`${scrollFadeClass} flex items-center gap-0.5 px-1 py-1`}
    >
      <Tool label={t.toolbar.undo} shortcut="Mod-z" disabled={!s.canUndo} onClick={() => run().undo().run()}>
        <Undo2 />
      </Tool>
      <Tool label={t.toolbar.redo} shortcut="Mod-Shift-z" disabled={!s.canRedo} onClick={() => run().redo().run()}>
        <Redo2 />
      </Tool>
      <Sep />
      {/* Headings do not go in table cells (a cell is one line of text in Markdown) or code. */}
      <HeadingMenu editor={editor} level={s.level} disabled={s.table || s.codeBlock} />
      <Sep />
      <Tool
        label={t.toolbar.bold}
        shortcut="Mod-b"
        active={s.bold}
        disabled={!s.canMark}
        onClick={() => run().toggleBold().run()}
      >
        <Bold />
      </Tool>
      <Tool
        label={t.toolbar.italic}
        shortcut="Mod-i"
        active={s.italic}
        disabled={!s.canMark}
        onClick={() => run().toggleItalic().run()}
      >
        <Italic />
      </Tool>
      <Tool
        label={t.toolbar.strike}
        shortcut="Mod-Shift-s"
        active={s.strike}
        disabled={!s.canMark}
        onClick={() => run().toggleStrike().run()}
      >
        <Strikethrough />
      </Tool>
      <Tool
        label={t.toolbar.code}
        shortcut="Mod-e"
        active={s.code}
        disabled={!s.canMark}
        onClick={() => run().toggleCode().run()}
      >
        <Code />
      </Tool>
      <Popover open={linkOpen} onOpenChange={setLinkOpen}>
        <PopoverTrigger
          disabled={!s.canLink}
          title={withShortcut(t.toolbar.link, 'Mod-k')}
          aria-label={t.toolbar.link}
          aria-pressed={s.link}
          aria-keyshortcuts="Control+K"
          onMouseDown={(e) => e.preventDefault()}
          render={
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              className={s.link ? 'bg-muted text-foreground' : 'text-muted-foreground'}
            />
          }
        >
          <Link2 />
        </PopoverTrigger>
        <PopoverContent align="start" className="w-auto flex-row items-center gap-0.5 p-1">
          <UrlInput key={s.href} editor={editor} initial={s.href} autoFocus onDone={() => setLinkOpen(false)} />
        </PopoverContent>
      </Popover>
      <Sep />
      <Tool
        label={t.toolbar.bulletList}
        shortcut="Mod-Shift-8"
        active={s.bullet}
        onClick={() => run().toggleBulletList().run()}
      >
        <List />
      </Tool>
      <Tool
        label={t.toolbar.orderedList}
        shortcut="Mod-Shift-7"
        active={s.ordered}
        onClick={() => run().toggleOrderedList().run()}
      >
        <ListOrdered />
      </Tool>
      <Tool
        label={t.toolbar.taskList}
        shortcut="Mod-Shift-9"
        active={s.task}
        onClick={() => run().toggleTaskList().run()}
      >
        <ListChecks />
      </Tool>
      <Sep />
      <Tool
        label={t.toolbar.blockquote}
        shortcut="Mod-Shift-b"
        active={s.quote}
        onClick={() => run().toggleBlockquote().run()}
      >
        <Quote />
      </Tool>
      <Tool
        label={t.toolbar.codeBlock}
        shortcut="Mod-Alt-c"
        active={s.codeBlock}
        onClick={() => run().toggleCodeBlock().run()}
      >
        <Code2 />
      </Tool>
      <Tool label={t.toolbar.rule} onClick={() => run().setHorizontalRule().run()}>
        <Minus />
      </Tool>
      <Tool label={t.toolbar.footnote} disabled={!s.footnote} onClick={() => insertFootnote(editor)}>
        <Superscript />
      </Tool>
      <UploadButtons editor={editor} />
      <TableMenu editor={editor} inTable={s.table} disabled={s.codeBlock} />
    </div>
  )
}
