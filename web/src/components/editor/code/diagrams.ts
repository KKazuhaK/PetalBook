// Which code blocks are diagrams: a fence whose language is `mermaid`. The
// document keeps an ordinary code block; only the drawing differs.

import { DIAGRAM, resolveLanguage } from './languages'

export function isDiagramLanguage(attr: unknown): boolean {
  return resolveLanguage(attr) === DIAGRAM
}
