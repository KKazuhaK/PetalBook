// Shapes of docs/api.md.

export interface SpaceInfo {
  id: string
  name: string
}
export interface MetaResponse {
  version: string
  spaces: SpaceInfo[]
}

export interface PageSummary {
  id: string
  path: string
  folder: string
  title?: string
  subtitle?: string
  label: string
  untitled: boolean
  tags?: string[]
  type?: string
  created?: string
  updated: string
  excerpt?: string
}

// The page header as the server serves it. Fields the app does not know about
// are sent back exactly as received; the server decides the key order.
export type PageMeta = Record<string, unknown>

export interface Page {
  id: string
  path: string
  folder: string
  rev: string
  updated: string
  meta: PageMeta
  body: string
}

export type SortKey = 'updated' | 'created' | 'title' | '-updated' | '-created' | '-title'

export interface ListParams {
  folder?: string
  recursive?: boolean
  tag?: string[]
  q?: string
  sort?: SortKey
  limit?: number
  cursor?: string
}
export interface PageList {
  items: PageSummary[]
  next: string
}

export interface CreatePageInput {
  folder?: string
  meta?: PageMeta
  body?: string
}
export interface SavePageInput {
  baseRev: string
  meta: PageMeta
  body: string
}

export interface TreeResponse {
  folders: { path: string; pages: number }[]
}
export interface TagsResponse {
  tags: { tag: string; pages: number }[]
}

export type ErrorCode = 'not_found' | 'invalid' | 'conflict' | 'exists' | 'forbidden' | 'internal' | 'network' | 'unknown'
