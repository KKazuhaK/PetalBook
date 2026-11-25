import { useMemo } from 'react'
import { useAppearance, useTypes } from '@/api/hooks'

export interface Style {
  icon?: string
  color?: string
}

// The icon and colour of folders, by path (from space.yml): a function of the path, nothing for a folder with none.
export function useFolderStyle(): (path: string) => Style {
  const a = useAppearance()
  const by = useMemo(() => new Map((a.data?.folders ?? []).map((f) => [f.folder, f])), [a.data])
  return (path) => by.get(path) ?? {}
}

// The icon and colour of a page: its own, else the default of its type.
export function usePageStyle(): (p: { icon?: string; color?: string; effectiveType?: string }) => Style {
  const types = useTypes()
  const by = useMemo(() => new Map((types.data?.types ?? []).map((ty) => [ty.name, ty])), [types.data])
  return (p) => {
    const ty = p.effectiveType ? by.get(p.effectiveType) : undefined
    return { icon: p.icon || ty?.icon, color: p.color || (p.icon ? undefined : ty?.color) }
  }
}
