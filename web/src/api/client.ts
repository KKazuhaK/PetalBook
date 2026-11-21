// Typed functions for every endpoint of docs/api.md.

import type {
  CreatePageInput,
  ErrorCode,
  ListParams,
  MetaResponse,
  Page,
  PageList,
  SavePageInput,
  TagsResponse,
  TreeResponse,
} from './types'

export const API_BASE = '/api'
// Milestone 1.1 serves exactly one space.
export const SPACE = 'default'

export class ApiError extends Error {
  readonly status: number
  readonly code: ErrorCode
  // Present on a 409 conflict: the page as the server has it now.
  readonly current?: Page
  constructor(status: number, code: ErrorCode, message: string, current?: Page) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.current = current
  }
}

// 403 `forbidden` means the server will not talk to this page's address (its
// allowed origins do not include it). The generic message would send people
// looking for a save bug, so say what is wrong and show where we are.
export function forbiddenMessage(origin: string): string {
  return `服务器拒绝了这个页面的地址（${origin}）：它不在服务器允许的来源里。请让服务器允许 ${origin}（启动时加 --allow-origin ${origin}），然后刷新本页。`
}

type ForbiddenListener = (message: string) => void
const forbiddenListeners = new Set<ForbiddenListener>()
// The app registers one to show a persistent notice, whichever request hit it.
export function onForbidden(fn: ForbiddenListener): () => void {
  forbiddenListeners.add(fn)
  return () => void forbiddenListeners.delete(fn)
}

export const isConflict = (e: unknown): e is ApiError => e instanceof ApiError && e.code === 'conflict'

// Browsers refuse keepalive requests whose body is over 64 KiB; stay under it
// with room for headers, and send larger bodies as ordinary requests.
const KEEPALIVE_LIMIT = 60_000
const byteLength = (s: string) => new TextEncoder().encode(s).length

interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE'
  body?: unknown
  keepalive?: boolean
  signal?: AbortSignal
}

async function request<T>(path: string, { method = 'GET', body, keepalive, signal }: RequestOptions = {}): Promise<T> {
  // Every write carries a JSON content type, even with nothing to say: the
  // server refuses writes without one (it is what keeps other sites from
  // posting to it from a browser form).
  const isWrite = method !== 'GET'
  const payload = body === undefined ? (isWrite ? '{}' : undefined) : JSON.stringify(body)
  let res: Response
  try {
    res = await fetch(API_BASE + path, {
      method,
      signal,
      headers: { Accept: 'application/json', ...(payload === undefined ? {} : { 'Content-Type': 'application/json' }) },
      body: payload,
      keepalive: keepalive && payload !== undefined && byteLength(payload) < KEEPALIVE_LIMIT ? true : undefined,
    })
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e
    throw new ApiError(0, 'network', '无法连接到服务器')
  }
  if (res.status === 204) return undefined as T
  const text = await res.text()
  let json: unknown = null
  if (text) {
    try {
      json = JSON.parse(text)
    } catch {
      // A proxy answering with HTML when the server is down.
      throw new ApiError(res.status, 'network', res.ok ? '服务器返回了无法识别的内容' : '无法连接到服务器')
    }
  }
  if (!res.ok) {
    const err = (json ?? {}) as { error?: string; message?: string; current?: Page }
    if (err.error === 'forbidden') {
      const message = forbiddenMessage(globalThis.location?.origin ?? '')
      forbiddenListeners.forEach((fn) => fn(message))
      throw new ApiError(res.status, 'forbidden', message)
    }
    throw new ApiError(res.status, (err.error as ErrorCode) || 'unknown', err.message || `请求失败（${res.status}）`, err.current)
  }
  return json as T
}

const space = (rest = '') => `/spaces/${SPACE}${rest}`

export function listParamsToQuery(p: ListParams): string {
  const q = new URLSearchParams()
  if (p.folder !== undefined) q.set('folder', p.folder)
  if (p.recursive) q.set('recursive', 'true')
  for (const tag of p.tag ?? []) q.append('tag', tag)
  if (p.q) q.set('q', p.q)
  if (p.sort) q.set('sort', p.sort)
  if (p.limit) q.set('limit', String(p.limit))
  if (p.cursor) q.set('cursor', p.cursor)
  const s = q.toString()
  return s ? `?${s}` : ''
}

export const api = {
  meta: (signal?: AbortSignal) => request<MetaResponse>('/meta', { signal }),

  listPages: (params: ListParams = {}, signal?: AbortSignal) => request<PageList>(space(`/pages${listParamsToQuery(params)}`), { signal }),
  getPage: (id: string, signal?: AbortSignal) => request<Page>(space(`/pages/${encodeURIComponent(id)}`), { signal }),
  createPage: (input: CreatePageInput = {}) => request<Page>(space('/pages'), { method: 'POST', body: input }),
  savePage: (id: string, input: SavePageInput, opts: { keepalive?: boolean } = {}) =>
    request<Page>(space(`/pages/${encodeURIComponent(id)}`), { method: 'PUT', body: input, keepalive: opts.keepalive }),
  movePage: (id: string, folder: string) => request<Page>(space(`/pages/${encodeURIComponent(id)}/move`), { method: 'POST', body: { folder } }),
  deletePage: (id: string) => request<void>(space(`/pages/${encodeURIComponent(id)}`), { method: 'DELETE' }),

  tree: (signal?: AbortSignal) => request<TreeResponse>(space('/tree'), { signal }),
  createFolder: (path: string) => request<{ path: string }>(space('/folders'), { method: 'POST', body: { path } }),
  tags: (signal?: AbortSignal) => request<TagsResponse>(space('/tags'), { signal }),

  reindex: () => request<{ pages: number; ms: number }>(space('/reindex'), { method: 'POST' }),
}
