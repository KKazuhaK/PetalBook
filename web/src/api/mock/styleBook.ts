// The icons and colours of folders and the presets of the sources of one space of the in-memory server (docs/api.md,
// "Icons, colours and the presets of the sources"). A folder's entry moves with the folder, as it does in space.yml.

import type { Appearance, FolderStyle, IconColor } from '../types.ts'
import { HttpError } from './store.ts'

export const ICON_COLORS: readonly IconColor[] = ['red', 'orange', 'amber', 'green', 'teal', 'blue', 'purple', 'pink']
const isUnder = (path: string, parent: string) => path === parent || path.startsWith(parent + '/')
const invalid = (m: string) => new HttpError(400, 'invalid', m)

export function createStyleBook(folderExists: (path: string) => string | null) {
  let styles: FolderStyle[] = []
  let presets: string[] | null = null
  return {
    appearance: (): Appearance => ({
      folders: [...styles].sort((a, b) => (a.folder < b.folder ? -1 : 1)).map((s) => ({ ...s })),
      sourcePresets: presets ? [...presets] : null,
      iconColors: [...ICON_COLORS],
    }),
    setStyle(folder: string, icon: string, color: string): FolderStyle {
      if (
        typeof icon !== 'string' ||
        new TextEncoder().encode(icon).length > 64 ||
        /[\p{Cc}]/u.test(icon) ||
        icon.trim() !== icon
      )
        throw invalid('图标是一个名字或表情，不超过 64 字节')
      if (color !== '' && !(ICON_COLORS as readonly string[]).includes(color)) throw invalid('颜色要是八种之一')
      if (folder === '') throw invalid('根目录没有图标')
      const spelled = folderExists(folder)
      if (spelled === null) throw new HttpError(404, 'not_found', '找不到这个文件夹')
      styles = styles.filter((s) => s.folder !== spelled)
      const next: FolderStyle = {
        folder: spelled,
        ...(icon ? { icon } : {}),
        ...(color ? { color: color as IconColor } : {}),
      }
      if (icon || color) styles.push(next)
      return { folder: spelled, ...(icon ? { icon } : {}), ...(color ? { color: color as IconColor } : {}) }
    },
    setPresets(list: unknown): string[] | null {
      if (list === null) return (presets = null)
      if (!Array.isArray(list) || list.some((x) => typeof x !== 'string')) throw invalid('presets 要是字符串列表')
      const out: string[] = []
      for (const raw of list as string[]) {
        const p = raw.trim()
        if (p === '' || out.includes(p)) continue
        if (new TextEncoder().encode(p).length > 200 || /[\r\n\0]/.test(p))
          throw invalid('来源是一行文字，不超过 200 字节')
        out.push(p)
      }
      if (out.length > 100) throw invalid('最多 100 个来源')
      return (presets = out)
    },
    folderMoved(from: string, to: string) {
      styles = styles.map((s) => (isUnder(s.folder, from) ? { ...s, folder: to + s.folder.slice(from.length) } : s))
    },
    folderRemoved(path: string) {
      styles = styles.filter((s) => !isUnder(s.folder, path))
    },
  }
}
