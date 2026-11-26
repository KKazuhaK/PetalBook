import { Check } from 'lucide-react'
import { Fragment, type ReactNode } from 'react'
import { t } from '@/i18n'

// A task item as it can be left in an excerpt: "[ ] ", "[x] " or "[X] ", after any indentation.
const TASK = /^\s*\[([ xX])\]\s+/

// The text of an excerpt. A line that starts with a task marker shows a small checkbox in its place; everything else is
// the text as it is, so the caller keeps its own whitespace, wrapping and clamping classes. `body` renders the text of
// a line (the search dialog marks its matches there).
export function ExcerptText({ text, body }: { text: string; body?: (line: string) => ReactNode }) {
  const lines = text.split('\n')
  return (
    <>
      {lines.map((line, i) => {
        const m = TASK.exec(line)
        const nl = i < lines.length - 1 ? '\n' : ''
        if (!m) {
          return (
            <Fragment key={i}>
              {body ? body(line) : line}
              {nl}
            </Fragment>
          )
        }
        const done = m[1] !== ' '
        const rest = line.slice(m[0].length)
        return (
          <Fragment key={i}>
            <span
              data-task={done ? 'done' : 'todo'}
              className={done ? 'text-muted-foreground line-through' : undefined}
            >
              <span
                data-box
                aria-hidden="true"
                className={`mr-[0.45em] inline-flex size-[0.9em] translate-y-[0.1em] items-center justify-center rounded-[0.25em] border align-baseline ${
                  done ? 'border-primary bg-primary text-primary-foreground' : 'border-muted-foreground/70'
                }`}
              >
                {done && <Check className="size-[80%]" strokeWidth={3.5} />}
              </span>
              <span className="sr-only">{done ? t.list.taskDone : t.list.taskTodo}</span>
              {body ? body(rest) : rest}
            </span>
            {nl}
          </Fragment>
        )
      })}
    </>
  )
}
