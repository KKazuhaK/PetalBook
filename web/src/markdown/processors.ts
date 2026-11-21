// Markdown text <-> mdast. The same rules must be used everywhere Markdown is
// rendered (editor, server, publishing): the CJK-friendly emphasis rules are
// not standard CommonMark.

import { unified, type Processor } from 'unified'
import remarkParse from 'remark-parse'
import remarkStringify from 'remark-stringify'
import remarkGfm from 'remark-gfm'
import remarkCjkFriendly from 'remark-cjk-friendly'
import remarkCjkFriendlyStrike from 'remark-cjk-friendly-gfm-strikethrough'
import { gfmTableToMarkdown } from 'mdast-util-gfm-table'
import { gfmTaskListItemToMarkdown } from 'mdast-util-gfm-task-list-item'

// The unified plugin API hands plugins a Processor as `this`; the data bag it
// exposes is typed per extension, so the extension lists are reached untyped.
type ToMarkdownData = { toMarkdownExtensions?: unknown[] }
const toMarkdownExtensions = (processor: Processor) => ((processor.data() as ToMarkdownData).toMarkdownExtensions ??= [])

export const parser = unified()
  .use(remarkParse)
  .use(remarkGfm, { singleTilde: false })
  .use(remarkCjkFriendly)
  .use(remarkCjkFriendlyStrike, { singleTilde: false })

export const printer = unified()
  .use(remarkGfm, { singleTilde: false, tablePipeAlign: false })
  .use(remarkCjkFriendly)
  .use(remarkCjkFriendlyStrike, { singleTilde: false })
  // The CJK plugins replace handlers that the task-list serializer builds on,
  // which silently drops "[ ]" / "[x]". Registering it again afterwards
  // restores the checkboxes.
  .use(function taskListLast(this: Processor) {
    toMarkdownExtensions(this).push(gfmTaskListItemToMarkdown())
  })
  // Unpadded tables keep diffs to the edited row, but the serializer then
  // writes one-dash delimiter cells. Widen them to the conventional three.
  .use(function tableDelimiters(this: Processor) {
    const base = gfmTableToMarkdown({ tablePipeAlign: false }).handlers!.table!
    toMarkdownExtensions(this).push({
      handlers: {
        table(node: any, parent: any, state: any, info: any) {
          const lines = base(node, parent, state, info).split('\n')
          if (lines.length > 1) lines[1] = lines[1].replace(/(:?)-+(:?)/g, '$1---$2')
          return lines.join('\n')
        },
      },
    })
  })
  .use(remarkStringify, {
    bullet: '-',
    emphasis: '*',
    strong: '*',
    rule: '-',
    fences: true,
    listItemIndent: 'one',
    incrementListMarker: true,
  })
