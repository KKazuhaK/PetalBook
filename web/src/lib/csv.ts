// A small CSV reader for the lists of people the add dialogs import: quoted fields, doubled quotes, a comma,
// semicolon or tab between fields, a byte order mark in front. Nothing is sent anywhere from here.

import type { BatchRowIn } from '@/api/people'

export interface CsvTable {
  header: string[] | null
  rows: string[][]
}

function delimiterOf(first: string): string {
  const count = (c: string) => first.split(c).length - 1
  const best = [',', ';', '\t'].map((c) => [c, count(c)] as const).sort((a, b) => b[1] - a[1])[0]
  return best[1] > 0 ? best[0] : ','
}

export function parseCsv(text: string): string[][] {
  const src = text.replace(/^\uFEFF/, '')
  const delim = delimiterOf(src.split(/\r?\n/, 1)[0] ?? '')
  const out: string[][] = []
  let row: string[] = []
  let field = ''
  let quoted = false
  let touched = false
  const endField = () => {
    row.push(field)
    field = ''
  }
  const endRow = () => {
    endField()
    if (touched || row.some((f) => f.trim() !== '')) out.push(row)
    row = []
    touched = false
  }
  for (let i = 0; i < src.length; i++) {
    const ch = src[i]
    if (quoted) {
      if (ch === '"' && src[i + 1] === '"') {
        field += '"'
        i++
      } else if (ch === '"') quoted = false
      else field += ch
      continue
    }
    if (ch === '"' && field === '') {
      quoted = true
      touched = true
    } else if (ch === delim) {
      endField()
      touched = true
    } else if (ch === '\n' || ch === '\r') {
      if (ch === '\r' && src[i + 1] === '\n') i++
      endRow()
    } else field += ch
  }
  if (field !== '' || row.length > 0 || touched) endRow()
  return out
}

type Column = 'user' | 'role' | 'name' | 'groups'

const HEADS: Record<Column, string[]> = {
  user: ['user', 'username', 'email', 'e-mail', 'mail', 'user name', '用户', '用户名', '邮箱', '邮件', '账号'],
  role: ['role', '角色'],
  name: ['name', 'display name', 'full name', '名字', '姓名', '名称'],
  groups: ['groups', 'group', '组', '分组', '群组'],
}

// The columns of a file by the words of its first row; a first row that names none of them is data, and the
// columns are taken in the order user, role, name, groups.
export function readPeopleCsv(text: string): { rows: BatchRowIn[]; headed: boolean } {
  const all = parseCsv(text)
  if (all.length === 0) return { rows: [], headed: false }
  const cols = new Map<Column, number>()
  all[0].forEach((h, i) => {
    const k = h.trim().toLowerCase()
    for (const c of Object.keys(HEADS) as Column[]) if (!cols.has(c) && HEADS[c].includes(k)) cols.set(c, i)
  })
  const headed = cols.has('user')
  if (!headed) (['user', 'role', 'name', 'groups'] as Column[]).forEach((c, i) => cols.set(c, i))
  const get = (r: string[], c: Column) => (cols.has(c) ? (r[cols.get(c)!] ?? '').trim() : '')
  const rows = all.slice(headed ? 1 : 0).map((r) => {
    const row: BatchRowIn = { user: get(r, 'user') }
    const role = get(r, 'role')
    if (role) row.role = role
    const name = get(r, 'name')
    if (name) row.name = name
    const groups = get(r, 'groups')
      .split(/[;|]/)
      .map((g) => g.trim())
      .filter(Boolean)
    if (groups.length > 0) row.groups = groups
    return row
  })
  return { rows, headed }
}
