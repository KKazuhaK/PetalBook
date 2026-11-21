import type { ReactNode } from 'react'
import type { Editor } from '@tiptap/core'
import { useEditorState } from '@tiptap/react'
import { Code2, Heading1, Heading2, Heading3, List, ListChecks, ListOrdered, Minus, Quote, Redo2, Table2, Undo2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { t } from '@/i18n/zh-CN'

function Tool({ label, active, disabled, onClick, children }: { label: string; active?: boolean; disabled?: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-sm"
      title={label}
      aria-label={label}
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

// Block-level formatting. Shown above the page body and kept in view while
// scrolling; the same actions are on the Markdown shortcuts for those who know them.
export function EditorToolbar({ editor }: { editor: Editor }) {
  const s = useEditorState({
    editor,
    selector: ({ editor: e }) => ({
      h1: e.isActive('heading', { level: 1 }),
      h2: e.isActive('heading', { level: 2 }),
      h3: e.isActive('heading', { level: 3 }),
      bullet: e.isActive('bulletList'),
      ordered: e.isActive('orderedList'),
      task: e.isActive('taskList'),
      quote: e.isActive('blockquote'),
      code: e.isActive('codeBlock'),
      table: e.isActive('table'),
      canUndo: e.can().undo(),
      canRedo: e.can().redo(),
    }),
  })
  const run = () => editor.chain().focus()

  return (
    <div role="toolbar" aria-label="格式" className="flex items-center gap-0.5 overflow-x-auto px-1 py-1">
      <Tool label={t.toolbar.undo} disabled={!s.canUndo} onClick={() => run().undo().run()}>
        <Undo2 />
      </Tool>
      <Tool label={t.toolbar.redo} disabled={!s.canRedo} onClick={() => run().redo().run()}>
        <Redo2 />
      </Tool>
      <Sep />
      <Tool label={t.toolbar.heading1} active={s.h1} onClick={() => run().toggleHeading({ level: 1 }).run()}>
        <Heading1 />
      </Tool>
      <Tool label={t.toolbar.heading2} active={s.h2} onClick={() => run().toggleHeading({ level: 2 }).run()}>
        <Heading2 />
      </Tool>
      <Tool label={t.toolbar.heading3} active={s.h3} onClick={() => run().toggleHeading({ level: 3 }).run()}>
        <Heading3 />
      </Tool>
      <Sep />
      <Tool label={t.toolbar.bulletList} active={s.bullet} onClick={() => run().toggleBulletList().run()}>
        <List />
      </Tool>
      <Tool label={t.toolbar.orderedList} active={s.ordered} onClick={() => run().toggleOrderedList().run()}>
        <ListOrdered />
      </Tool>
      <Tool label={t.toolbar.taskList} active={s.task} onClick={() => run().toggleTaskList().run()}>
        <ListChecks />
      </Tool>
      <Sep />
      <Tool label={t.toolbar.blockquote} active={s.quote} onClick={() => run().toggleBlockquote().run()}>
        <Quote />
      </Tool>
      <Tool label={t.toolbar.codeBlock} active={s.code} onClick={() => run().toggleCodeBlock().run()}>
        <Code2 />
      </Tool>
      <Tool label={t.toolbar.rule} onClick={() => run().setHorizontalRule().run()}>
        <Minus />
      </Tool>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              title={t.toolbar.table}
              aria-label={t.toolbar.table}
              className={s.table ? 'bg-muted text-foreground' : 'text-muted-foreground'}
              onMouseDown={(e) => e.preventDefault()}
            />
          }
        >
          <Table2 />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="min-w-44">
          {/* Inserting inside a table would nest one, which Markdown cannot hold. */}
          <DropdownMenuItem disabled={s.table} onClick={() => run().insertTable({ rows: 3, cols: 3, withHeaderRow: true }).run()}>
            {t.toolbar.insertTable}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem disabled={!s.table} onClick={() => run().addRowAfter().run()}>
            {t.toolbar.addRowAfter}
          </DropdownMenuItem>
          <DropdownMenuItem disabled={!s.table} onClick={() => run().addColumnBefore().run()}>
            {t.toolbar.addColumnBefore}
          </DropdownMenuItem>
          <DropdownMenuItem disabled={!s.table} onClick={() => run().addColumnAfter().run()}>
            {t.toolbar.addColumnAfter}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem disabled={!s.table} onClick={() => run().deleteRow().run()}>
            {t.toolbar.deleteRow}
          </DropdownMenuItem>
          <DropdownMenuItem disabled={!s.table} onClick={() => run().deleteColumn().run()}>
            {t.toolbar.deleteColumn}
          </DropdownMenuItem>
          <DropdownMenuItem disabled={!s.table} variant="destructive" onClick={() => run().deleteTable().run()}>
            {t.toolbar.deleteTable}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}
