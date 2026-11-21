// Typed functions for every endpoint of docs/api.md.

import type {
  AccountInfo,
  CreatedInvite,
  CreatePageInput,
  DraftInput,
  DraftReceipt,
  ErrorCode,
  Invite,
  InviteCheck,
  ListParams,
  MetaResponse,
  OidcIntent,
  Page,
  PageDraft,
  PageList,
  Role,
  SavePageInput,
  SessionInfo,
  SpaceInfo,
  TagsResponse,
  TreeResponse,
  User,
} from './types'

export const API_BASE = '/api'
// Single-user mode has exactly one space, with this id.
export const DEFAULT_SPACE = 'default'

export class ApiError extends Error {
  readonly status: number
  readonly code: ErrorCode
  // Present on a 409 conflict: the page as the server has it now.
  readonly current?: Page
  // On a draft 409: the caller's draft as the server has it, or null when there
  // is none. Undefined when the answer did not carry one.
  readonly draft?: PageDraft | null
  // Present on 429: seconds the server asks us to wait.
  readonly retryAfter?: number
  constructor(status: number, code: ErrorCode, message: string, current?: Page, retryAfter?: number, draft?: PageDraft | null) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.current = current
    this.retryAfter = retryAfter
    this.draft = draft
  }
}

// 403 `forbidden` normally means the server will not talk to this page's
// address (its allowed origins do not include it). The generic message would
// send people looking for a save bug, so say what is wrong and show where we are.
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

// --- being signed out --------------------------------------------------------
//
// The first 401 on any request other than the sign-in ones tells the app once
// that the session is gone. From then until someone signs in again, requests
// that need a session fail on the spot with the same error instead of going to
// the server: several queries failing together make one redirect, not a storm,
// and an editor that leaves in the meantime does not hammer the server with
// saves that cannot work. Nothing here touches local drafts.

type SignedOutListener = () => void
const signedOutListeners = new Set<SignedOutListener>()
export function onSignedOut(fn: SignedOutListener): () => void {
  signedOutListeners.add(fn)
  return () => void signedOutListeners.delete(fn)
}
let signedOut = false
export const isSignedOut = () => signedOut
// Called when a sign-in succeeded or the session shows a user.
export function markSignedIn() {
  signedOut = false
}
function markSignedOut() {
  if (signedOut) return
  signedOut = true
  signedOutListeners.forEach((fn) => fn())
}

// Paths where a 401 is an answer to the request itself (wrong password), not
// a lost session.
const SIGN_IN_PATHS = /^\/(login|logout|setup|session|meta|invites\/(check|accept)|auth\/oidc\/start)$/
// Paths where 403 is an answer about the request itself (a wrong setup token,
// a wrong current password, a member asking for admin things), not a refused
// origin. Page and space endpoints have no such 403: a space of someone else
// is a 404.
const USER_LEVEL_403 = /^\/(setup|account(\/.*)?|users(\/.*)?|invites(\/.*)?)$|^\/spaces(\/[^/]+)?$/

export const isConflict = (e: unknown): e is ApiError => e instanceof ApiError && e.code === 'conflict'
export const isUnauthenticated = (e: unknown): e is ApiError => e instanceof ApiError && e.status === 401

// Browsers refuse keepalive requests whose body is over 64 KiB; stay under it
// with room for headers, and send larger bodies as ordinary requests.
const KEEPALIVE_LIMIT = 60_000
const byteLength = (s: string) => new TextEncoder().encode(s).length

interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  body?: unknown
  keepalive?: boolean
  signal?: AbortSignal
}

async function request<T>(path: string, { method = 'GET', body, keepalive, signal }: RequestOptions = {}): Promise<T> {
  const barePath = path.split('?')[0]
  const isSignIn = SIGN_IN_PATHS.test(barePath)
  if (signedOut && !isSignIn) throw new ApiError(401, 'unauthenticated', '登录已失效，请重新登录')
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
      // The session is a same-origin cookie.
      credentials: 'same-origin',
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
    const err = (json ?? {}) as { error?: string; message?: string; current?: Page; draft?: PageDraft | null }
    if (err.error === 'forbidden' && !USER_LEVEL_403.test(barePath)) {
      const message = forbiddenMessage(globalThis.location?.origin ?? '')
      forbiddenListeners.forEach((fn) => fn(message))
      throw new ApiError(res.status, 'forbidden', message)
    }
    if (res.status === 401 && !isSignIn) markSignedOut()
    const retry = Number(res.headers.get('Retry-After'))
    throw new ApiError(
      res.status,
      (err.error as ErrorCode) || 'unknown',
      err.message || `请求失败（${res.status}）`,
      err.current,
      res.status === 429 && Number.isFinite(retry) && retry > 0 ? retry : undefined,
      err.draft,
    )
  }
  return json as T
}

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

const enc = encodeURIComponent

// Everything under one space. A space id is a path segment of the URL, so each
// caller says which space it means; there is no "current" space in this module.
export function spaceApi(space: string) {
  const base = (rest = '') => `/spaces/${enc(space)}${rest}`
  const pageBase = (id: string, rest = '') => base(`/pages/${enc(id)}${rest}`)
  return {
    listPages: (params: ListParams = {}, signal?: AbortSignal) => request<PageList>(base(`/pages${listParamsToQuery(params)}`), { signal }),
    getPage: (id: string, signal?: AbortSignal) => request<Page>(pageBase(id), { signal }),
    createPage: (input: CreatePageInput = {}) => request<Page>(base('/pages'), { method: 'POST', body: input }),
    savePage: (id: string, input: SavePageInput, opts: { keepalive?: boolean } = {}) => request<Page>(pageBase(id), { method: 'PUT', body: input, keepalive: opts.keepalive }),
    movePage: (id: string, folder: string) => request<Page>(pageBase(id, '/move'), { method: 'POST', body: { folder } }),
    deletePage: (id: string) => request<void>(pageBase(id), { method: 'DELETE' }),

    // Drafts: what autosave writes. The file changes only on commit.
    putDraft: (id: string, input: DraftInput, opts: { keepalive?: boolean } = {}) => request<DraftReceipt>(pageBase(id, '/draft'), { method: 'PUT', body: input, keepalive: opts.keepalive }),
    // With a version, only that draft is deleted (409 when it is another).
    deleteDraft: (id: string, version?: string, rev?: string) => request<void>(pageBase(id, `/draft${version ? `?version=${enc(version)}${rev ? `&rev=${enc(rev)}` : ''}` : ''}`), { method: 'DELETE' }),
    // Only the draft of that version is committed (409 with the page and the draft otherwise).
    commitPage: (id: string, version: string) => request<Page>(pageBase(id, '/commit'), { method: 'POST', body: { version } }),

    tree: (signal?: AbortSignal) => request<TreeResponse>(base('/tree'), { signal }),
    createFolder: (path: string) => request<{ path: string }>(base('/folders'), { method: 'POST', body: { path } }),
    tags: (signal?: AbortSignal) => request<TagsResponse>(base('/tags'), { signal }),

    reindex: () => request<{ pages: number; ms: number }>(base('/reindex'), { method: 'POST' }),
  }
}
export type SpaceApi = ReturnType<typeof spaceApi>

export const api = {
  meta: (signal?: AbortSignal) => request<MetaResponse>('/meta', { signal }),

  // --- session and signing in ---
  session: async (signal?: AbortSignal) => {
    const s = await request<SessionInfo>('/session', { signal })
    if (s.user) markSignedIn()
    return s
  },
  setup: async (input: { token: string; username: string; name?: string; password: string }) => {
    const r = await request<{ user: User }>('/setup', { method: 'POST', body: input })
    markSignedIn()
    return r
  },
  login: async (input: { username: string; password: string }) => {
    const r = await request<{ user: User }>('/login', { method: 'POST', body: input })
    markSignedIn()
    return r
  },
  logout: () => request<void>('/logout', { method: 'POST' }),
  oidcStart: (input: { intent: OidcIntent; token?: string; return?: string }) => request<{ url: string }>('/auth/oidc/start', { method: 'POST', body: input }),

  // --- account ---
  account: (signal?: AbortSignal) => request<AccountInfo>('/account', { signal }),
  updateAccount: (input: { name?: string; email?: string }) => request<{ user: User }>('/account', { method: 'PATCH', body: input }),
  // `current` is left out when the account has no password yet.
  changePassword: (input: { current?: string; new: string }) => request<void>('/account/password', { method: 'POST', body: input }),
  removeIdentity: (id: string) => request<void>(`/account/identities/${enc(id)}`, { method: 'DELETE' }),

  // --- users and invites (administrators) ---
  users: (signal?: AbortSignal) => request<{ users: User[] }>('/users', { signal }),
  updateUser: (id: string, input: { role?: Role; disabled?: boolean }) => request<{ user: User }>(`/users/${enc(id)}`, { method: 'PATCH', body: input }),
  createInvite: (input: { role?: Role; days?: number } = {}) => request<CreatedInvite>('/invites', { method: 'POST', body: input }),
  invites: (signal?: AbortSignal) => request<{ invites: Invite[] }>('/invites', { signal }),
  revokeInvite: (id: string) => request<void>(`/invites/${enc(id)}`, { method: 'DELETE' }),
  checkInvite: (token: string, signal?: AbortSignal) => request<InviteCheck>('/invites/check', { method: 'POST', body: { token }, signal }),
  acceptInvite: async (input: { token: string; username: string; name?: string; password: string }) => {
    const r = await request<{ user: User }>('/invites/accept', { method: 'POST', body: input })
    markSignedIn()
    return r
  },

  // --- spaces ---
  spaces: (signal?: AbortSignal) => request<{ spaces: SpaceInfo[] }>('/spaces', { signal }),
  createSpace: (name: string) => request<SpaceInfo>('/spaces', { method: 'POST', body: { name } }),
  renameSpace: (id: string, name: string) => request<SpaceInfo>(`/spaces/${enc(id)}`, { method: 'PATCH', body: { name } }),
}
