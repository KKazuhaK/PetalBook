import type { PageType, TypeField } from '@/api/types'
import { tp } from '@/i18n/props'

// The columns of a table, in a module of their own: the view's settings menu needs them without the table.

const WIDTHS: Record<TypeField['kind'], number> = {
  text: 200,
  longtext: 240,
  select: 150,
  multiselect: 220,
  date: 150,
  number: 120,
  checkbox: 90,
  url: 220,
  other: 180,
}

export interface Column {
  id: string
  label: string
  width: number
  // The sort a click starts with (ascending); none for a column the server cannot sort by.
  sort?: string
  field?: TypeField
  base?: 'title' | 'tags' | 'updated'
}

export function columnsOf(type: PageType | undefined): Column[] {
  return [
    { id: 'title', label: tp.table.title, width: 280, sort: 'title', base: 'title' },
    ...(type?.fields ?? []).map((f): Column => ({
      id: `f:${f.key}`,
      label: f.title || f.key,
      width: WIDTHS[f.kind],
      sort: `prop:${f.key}`,
      field: f,
    })),
    { id: 'tags', label: tp.table.tags, width: 200, base: 'tags' },
    { id: 'updated', label: tp.table.updated, width: 140, sort: 'updated', base: 'updated' },
  ]
}

// What a view's `hidden` calls a column: a field by its key, the two built-in ones by a name with a colon (a field's key has none).
export const hiddenKeyOf = (c: Pick<Column, 'id'>): string => (c.id.startsWith('f:') ? c.id.slice(2) : `:${c.id}`)
