// Folder paths: tree building, URLs, validation.

import { t } from '@/i18n'
import { folderRuleMessage } from './folderRules'

const byName = (a: { name: string }, b: { name: string }) => a.name.localeCompare(b.name, 'zh')

export interface FolderNode {
  path: string
  name: string
  // Pages directly in this folder (not in subfolders), as the API counts them.
  pages: number
  // Pages in it and in every folder below it.
  total: number
  children: FolderNode[]
}

// The API lists every folder, parents included. Missing parents are tolerated
// so a partial list still nests correctly.
export function buildFolderTree(folders: { path: string; pages: number }[]): {
  rootPages: number
  nodes: FolderNode[]
} {
  const byPath = new Map<string, FolderNode>()
  const roots: FolderNode[] = []
  let rootPages = 0
  const ensure = (path: string): FolderNode => {
    let node = byPath.get(path)
    if (node) return node
    const slash = path.lastIndexOf('/')
    node = { path, name: path.slice(slash + 1), pages: 0, total: 0, children: [] }
    byPath.set(path, node)
    if (slash < 0) roots.push(node)
    else ensure(path.slice(0, slash)).children.push(node)
    return node
  }
  for (const f of folders) {
    if (f.path === '') rootPages = f.pages
    else ensure(f.path).pages = f.pages
  }
  const sort = (list: FolderNode[]) => {
    list.sort(byName)
    list.forEach((n) => {
      sort(n.children)
      n.total = n.pages + n.children.reduce((sum, c) => sum + c.total, 0)
    })
  }
  sort(roots)
  return { rootPages, nodes: roots }
}

export const ancestorsOf = (path: string): string[] => {
  const parts = path.split('/').filter(Boolean)
  return parts.map((_, i) => parts.slice(0, i + 1).join('/'))
}

// What the person typed -> a folder path, or the reason it cannot be one.
// Stray slashes and spaces around segments are tidied (a convenience); the
// server's own rules are then applied as they are.
export function checkFolderPath(input: string): { path: string } | { error: string } {
  const path = input
    .trim()
    .replace(/\\/g, '/')
    .replace(/^\/+|\/+$/g, '')
    .split('/')
    .map((s) => s.trim())
    .join('/')
  if (path === '') return { error: t.folderRules.empty }
  const error = folderRuleMessage(path)
  return error ? { error } : { path }
}

export const folderLabel = (path: string) => (path === '' ? t.nav.rootFolder : path.slice(path.lastIndexOf('/') + 1))

// "/f/随笔/2026" -> "随笔/2026" for a route prefix like "/f". Returns null
// when the pathname is not under that prefix.
export function pathAfter(pathname: string, prefix: string): string | null {
  if (pathname === prefix) return ''
  if (!pathname.startsWith(prefix + '/')) return null
  try {
    return pathname
      .slice(prefix.length + 1)
      .split('/')
      .map(decodeURIComponent)
      .join('/')
  } catch {
    return null
  }
}

// --- paths -----------------------------------------------------------------

// "a/b/c" -> "a/b"; a top-level folder's parent is the root, "".
export const parentOf = (path: string): string => path.slice(0, Math.max(0, path.lastIndexOf('/')))
export const baseName = (path: string): string => path.slice(path.lastIndexOf('/') + 1)
export const joinPath = (parent: string, name: string): string => (parent === '' ? name : `${parent}/${name}`)
// Whether `path` is `ancestor` or lies inside it. Everything is inside the root.
export const isSameOrUnder = (path: string, ancestor: string): boolean =>
  ancestor === '' || path === ancestor || path.startsWith(ancestor + '/')

// A single folder name (one level), as typed in the new-folder and rename
// dialogs: no slashes, and the server's rules for the path it would make.
export function checkFolderName(parent: string, input: string): { name: string; path: string } | { error: string } {
  const name = input.trim()
  if (name === '') return { error: t.folderRules.nameEmpty }
  if (/[\\/]/.test(name)) return { error: t.folderRules.noSlash }
  const path = joinPath(parent, name)
  const error = folderRuleMessage(path)
  return error ? { error } : { name, path }
}

// --- what is in a folder ------------------------------------------------------

export interface FolderEntry {
  path: string
  name: string
  // Pages directly in it, and in it plus everything below.
  direct: number
  total: number
  // The newest page in it or anywhere below it (the server works it out); absent for an empty folder.
  updated?: string
}

export interface FolderIndex {
  // Subfolders of a folder (the root is ""), by name.
  children: Map<string, FolderEntry[]>
  byPath: Map<string, FolderEntry>
}

// One pass over the tree endpoint's list, so a listing of hundreds of folders
// does no repeated searching: the direct children of every folder, and the
// page counts of each one with its subfolders. Missing parents are tolerated.
export function buildFolderIndex(folders: { path: string; pages: number; updated?: string }[]): FolderIndex {
  const byPath = new Map<string, FolderEntry>()
  const children = new Map<string, FolderEntry[]>()
  const ensure = (path: string): FolderEntry => {
    let e = byPath.get(path)
    if (e) return e
    e = { path, name: baseName(path), direct: 0, total: 0 }
    byPath.set(path, e)
    if (path !== '') {
      const parent = parentOf(path)
      ensure(parent)
      const list = children.get(parent)
      if (list) list.push(e)
      else children.set(parent, [e])
    }
    return e
  }
  ensure('')
  for (const f of folders) {
    const e = ensure(f.path)
    e.direct = f.pages
    if (f.updated) e.updated = f.updated
  }
  for (const e of byPath.values()) {
    e.total = e.direct
  }
  // Every folder's direct pages count towards each folder above it.
  for (const e of byPath.values()) {
    if (e.path === '' || e.direct === 0) continue
    for (let p = parentOf(e.path); ; p = parentOf(p)) {
      byPath.get(p)!.total += e.direct
      if (p === '') break
    }
  }
  for (const list of children.values()) list.sort(byName)
  return { children, byPath }
}

export const childFolders = (index: FolderIndex, parent: string): FolderEntry[] => index.children.get(parent) ?? []

// --- breadcrumbs ----------------------------------------------------------------

export interface Crumb {
  path: string
  name: string
}

// The space, then each level of the folder: "a/b" -> [space, a, a/b].
export function crumbsOf(folder: string, rootName: string): Crumb[] {
  const out: Crumb[] = [{ path: '', name: rootName }]
  for (const path of ancestorsOf(folder)) out.push({ path, name: baseName(path) })
  return out
}

// Shortens a long trail by folding the middle into one "…": the first
// `keepStart` and the last `keepEnd` stay. Folding fewer than `minHidden`
// entries would save nothing, so then the trail is left whole.
export function collapseCrumbs<T>(
  items: T[],
  keepStart: number,
  keepEnd: number,
  minHidden = 2,
): { start: T[]; hidden: T[]; end: T[] } {
  const hiddenCount = items.length - keepStart - keepEnd
  if (hiddenCount < Math.max(minHidden, 1)) return { start: items, hidden: [], end: [] }
  return {
    start: items.slice(0, keepStart),
    hidden: items.slice(keepStart, items.length - keepEnd),
    end: items.slice(items.length - keepEnd),
  }
}

// --- dragging and moving -----------------------------------------------------------

// What can be picked up in a listing.
export type FileItem = { kind: 'page'; id: string; folder: string; label: string } | { kind: 'folder'; path: string }

export const itemKey = (i: FileItem): string => (i.kind === 'page' ? `p:${i.id}` : `f:${i.path}`)
export const itemName = (i: FileItem): string => (i.kind === 'page' ? i.label : baseName(i.path))

export type DropPlan =
  | {
      ok: true
      moving: FileItem[] // Folders that would land on a folder of the same name.
      conflicts: FileItem[]
    }
  // 'self': a folder onto itself or something inside it; 'nothing': all of it is there already;
  // 'exists': every folder would meet a folder of its name.
  | { ok: false; reason: 'self' | 'nothing' | 'exists' }

// What dropping `items` on the folder `target` would do. With the index, a
// folder that would meet one of its own name is held back (the server refuses
// to merge folders); without it only the structural rules apply, which is
// what decides whether a target lights up while dragging.
export function planDrop(items: FileItem[], target: string, index?: FolderIndex): DropPlan {
  if (items.length === 0) return { ok: false, reason: 'nothing' }
  for (const i of items) if (i.kind === 'folder' && isSameOrUnder(target, i.path)) return { ok: false, reason: 'self' }
  const moving: FileItem[] = []
  const conflicts: FileItem[] = []
  for (const i of items) {
    if (i.kind === 'page') {
      if (i.folder !== target) moving.push(i)
    } else if (parentOf(i.path) !== target) {
      if (index?.byPath.has(joinPath(target, baseName(i.path)))) conflicts.push(i)
      else moving.push(i)
    }
  }
  if (moving.length === 0) return { ok: false, reason: conflicts.length > 0 ? 'exists' : 'nothing' }
  return { ok: true, moving, conflicts }
}

// Folders a set of items cannot be moved into: each folder and everything inside it.
export const isForbiddenDestination = (items: FileItem[], target: string): boolean =>
  items.some((i) => i.kind === 'folder' && isSameOrUnder(target, i.path))

// Items to act on after a menu or a drag starts on `item`: the whole
// selection when the item is part of it, else only the item.
export function actingOn(item: FileItem, selected: FileItem[]): FileItem[] {
  const key = itemKey(item)
  return selected.some((s) => itemKey(s) === key) ? selected : [item]
}

// Folders whose pages go with them, counted once: a selected folder inside
// another selected one is already part of it.
export function pagesInside(items: FileItem[], index: FolderIndex): number {
  const folders = items.filter((i): i is Extract<FileItem, { kind: 'folder' }> => i.kind === 'folder')
  return folders
    .filter((f) => !folders.some((o) => o !== f && isSameOrUnder(f.path, o.path)))
    .reduce((n, f) => n + (index.byPath.get(f.path)?.total ?? 0), 0)
}
