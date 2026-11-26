// The registry of the audit log's kinds, generated from the server's (internal/auditlog/kinds.go), for the mock: the
// category of each kind, and which kinds only a site administrator sees.

import registries from '../generated/registries.json' with { type: 'json' }
import type { AuditCategory } from '../types.ts'

export interface MockAuditCategory {
  id: AuditCategory
  core: boolean
  setting: string | null
  // On unless the setting says otherwise.
  def: boolean
  kinds: { name: string; site: boolean }[]
}

export const AUDIT_CATEGORIES: MockAuditCategory[] = registries.auditCategories.map((c) => ({
  id: c.id as AuditCategory,
  core: c.core,
  setting: c.setting,
  def: c.default,
  kinds: c.kinds,
}))

const byKind = new Map(
  AUDIT_CATEGORIES.flatMap((c) => c.kinds.map((k) => [k.name, { category: c.id, site: k.site }] as const)),
)

// The category a kind is filed under (a kind nobody registered is filed under admin, as the server does).
export const categoryOfKind = (kind: string): AuditCategory => byKind.get(kind)?.category ?? 'admin'
// Whether only the server's own administrators see the kind.
export const isSiteKind = (kind: string): boolean => byKind.get(kind)?.site ?? false
