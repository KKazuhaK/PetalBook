// Folder paths: tree building, URLs, validation.

import { folderRuleMessage } from './folderRules'

export interface FolderNode {
  path: string
  name: string
  // Pages directly in this folder (not in subfolders), as the API counts them.
  pages: number
  children: FolderNode[]
}

// The API lists every folder, parents included. Missing parents are tolerated
// so a partial list still nests correctly.
export function buildFolderTree(folders: { path: string; pages: number }[]): { rootPages: number; nodes: FolderNode[] } {
  const byPath = new Map<string, FolderNode>()
  const roots: FolderNode[] = []
  let rootPages = 0
  const ensure = (path: string): FolderNode => {
    let node = byPath.get(path)
    if (node) return node
    const slash = path.lastIndexOf('/')
    node = { path, name: path.slice(slash + 1), pages: 0, children: [] }
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
    list.sort((a, b) => a.name.localeCompare(b.name, 'zh'))
    list.forEach((n) => sort(n.children))
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
  if (path === '') return { error: '路径不能为空' }
  const error = folderRuleMessage(path)
  return error ? { error } : { path }
}

export const folderLabel = (path: string) => (path === '' ? '根目录' : path.slice(path.lastIndexOf('/') + 1))

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
