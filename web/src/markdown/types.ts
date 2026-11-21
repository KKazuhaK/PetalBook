import type { JSONContent } from '@tiptap/core'

export type DocJSON = JSONContent

// mdast is only touched structurally here. The real node types are a large
// union that adds nothing: every node this layer builds or reads is checked by
// the round-trip tests.
export interface MdNode {
  type: string
  children?: MdNode[]
  [key: string]: any
}
