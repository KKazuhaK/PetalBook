// The registry of the audit log's kinds as the server has it (internal/auditlog/kinds.go), for the mock: the category
// of each kind, and which kinds only a site administrator sees. The contract test compares it with
// GET /api/admin/audit/kinds, so a drift fails there.

import type { AuditCategory } from '../types.ts'

export interface MockAuditCategory {
  id: AuditCategory
  core: boolean
  setting: string | null
  // On unless the setting says otherwise.
  def: boolean
  kinds: { name: string; site: boolean }[]
}

export const AUDIT_CATEGORIES: MockAuditCategory[] = [
  {
    id: 'security',
    core: true,
    setting: null,
    def: true,
    kinds: [
      { name: 'login_failed', site: false },
      { name: 'rate_limited', site: true },
      { name: 'lockout', site: true },
      { name: 'passkey_clone', site: false },
      { name: 'passkey_added', site: false },
      { name: 'passkey_removed', site: false },
      { name: 'identity_linked', site: false },
      { name: 'identity_unlinked', site: false },
      { name: 'sessions_ended', site: false },
      { name: 'password_reset_requested', site: false },
      { name: 'password_reset', site: false },
      { name: 'password_set_by_admin', site: false },
      { name: 'email_added', site: false },
      { name: 'email_verified', site: false },
      { name: 'email_primary_changed', site: false },
      { name: 'email_removed', site: false },
      { name: 'email_claim_withdrawn', site: false },
      { name: 'email_released', site: false },
      { name: 'account_connected', site: false },
      { name: 'account_disconnected', site: false },
      { name: 'provider_created', site: true },
      { name: 'provider_changed', site: true },
      { name: 'provider_deleted', site: true },
      { name: 'connection_provider_changed', site: true },
      { name: 'mail_changed', site: true },
      { name: 'mail_test', site: true },
      { name: 'secret_changed', site: false },
    ],
  },
  {
    id: 'admin',
    core: true,
    setting: null,
    def: true,
    kinds: [
      { name: 'setting_changed', site: false },
      { name: 'setting_rule_started', site: false },
      { name: 'setting_rule_ended', site: false },
      { name: 'plan_created', site: true },
      { name: 'plan_changed', site: true },
      { name: 'plan_deleted', site: true },
      { name: 'tenant_plan_changed', site: false },
      { name: 'tenant_created', site: false },
      { name: 'tenant_changed', site: false },
      { name: 'tenant_deleted', site: false },
      { name: 'tenant_console_opened', site: false },
      { name: 'registered', site: false },
      { name: 'user_changed', site: false },
      { name: 'user_deleted', site: false },
      { name: 'user_restored', site: false },
      { name: 'user_purged', site: false },
      { name: 'members_batch', site: false },
      { name: 'invites_batch', site: false },
      { name: 'group_created', site: false },
      { name: 'group_changed', site: false },
      { name: 'group_deleted', site: false },
      { name: 'group_member_added', site: false },
      { name: 'group_member_removed', site: false },
      { name: 'group_members_batch', site: false },
      { name: 'group_self_added', site: false },
      { name: 'groups_synced', site: false },
      { name: 'groups_claim_missing', site: false },
      { name: 'groups_claim_truncated', site: false },
      { name: 'space_group_added', site: false },
      { name: 'space_group_changed', site: false },
      { name: 'space_group_removed', site: false },
      { name: 'token_created', site: false },
      { name: 'token_revoked', site: false },
      { name: 'grant_revoked', site: false },
      { name: 'quota_exceeded', site: false },
      { name: 'bulk_started', site: false },
      { name: 'bulk_finished', site: false },
    ],
  },
  {
    id: 'data',
    core: true,
    setting: null,
    def: true,
    kinds: [
      { name: 'space_deleted', site: false },
      { name: 'space_restored', site: false },
      { name: 'space_purged', site: false },
      { name: 'space_taken_over', site: false },
      { name: 'space_transferred', site: false },
      { name: 'space_viewed', site: false },
      { name: 'page_viewed', site: false },
      { name: 'export_created', site: false },
      { name: 'export_deleted', site: false },
      { name: 'export_downloaded', site: false },
      { name: 'audit_downloaded', site: false },
    ],
  },
  {
    id: 'system',
    core: true,
    setting: null,
    def: true,
    kinds: [{ name: 'dropped', site: true }],
  },
  {
    id: 'signins',
    core: false,
    setting: 'audit.signins',
    def: true,
    kinds: [{ name: 'login', site: false }],
  },
  {
    id: 'reads',
    core: false,
    setting: 'audit.reads',
    def: false,
    kinds: [],
  },
  {
    id: 'downloads',
    core: false,
    setting: 'audit.downloads',
    def: false,
    kinds: [],
  },
  {
    id: 'api',
    core: false,
    setting: 'audit.api',
    def: false,
    kinds: [],
  },
]

const byKind = new Map(
  AUDIT_CATEGORIES.flatMap((c) => c.kinds.map((k) => [k.name, { category: c.id, site: k.site }] as const)),
)

// The category a kind is filed under (a kind nobody registered is filed under admin, as the server does).
export const categoryOfKind = (kind: string): AuditCategory => byKind.get(kind)?.category ?? 'admin'
// Whether only the server's own administrators see the kind.
export const isSiteKind = (kind: string): boolean => byKind.get(kind)?.site ?? false
