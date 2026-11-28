package auditlog

// registered is the table of kinds: the one place a kind is named with its
// category and visibility. A new kind goes here, in its category's block;
// TestEveryKindWrittenIsRegistered fails until it does.
//
// Visibility: a tenant's administrators see the rows about their tenant, a
// site administrator's changes to it included. Only the server's own doings
// (its sign-in providers, mail, rate limits, lockouts and dropped rows) and the
// site's plans (a plan is shared by tenants) are site-only.
var registered = []Kind{
	// security: failures, lockouts, passkeys, identities, addresses, passwords,
	// ended sessions, providers, secrets.
	{Name: "login_failed", Category: Security},
	{Name: "rate_limited", Category: Security, Visibility: Site},
	{Name: "lockout", Category: Security, Visibility: Site},
	{Name: "passkey_clone", Category: Security},
	{Name: "passkey_added", Category: Security},
	{Name: "passkey_removed", Category: Security},
	{Name: "identity_linked", Category: Security},
	{Name: "identity_unlinked", Category: Security},
	{Name: "sessions_ended", Category: Security},
	{Name: "password_reset_requested", Category: Security},
	{Name: "password_reset", Category: Security},
	{Name: "password_set_by_admin", Category: Security},
	{Name: "email_added", Category: Security, AddressFields: []string{"email"}},
	{Name: "email_verified", Category: Security, AddressFields: []string{"email"}},
	{Name: "email_primary_changed", Category: Security, AddressFields: []string{"email", "old"}},
	{Name: "email_removed", Category: Security, AddressFields: []string{"email"}},
	{Name: "email_claim_withdrawn", Category: Security, AddressFields: []string{"email"}},
	{Name: "email_released", Category: Security, AddressFields: []string{"email"}},
	{Name: "account_connected", Category: Security},
	{Name: "account_disconnected", Category: Security},
	{Name: "github_installation_connected", Category: Security},
	{Name: "github_installation_disconnected", Category: Security},
	{Name: "provider_created", Category: Security, Visibility: Site},
	{Name: "provider_changed", Category: Security, Visibility: Site},
	{Name: "provider_deleted", Category: Security, Visibility: Site},
	// A tenant's own SSO provider (3.6 d3): the tenant's administrators see them.
	{Name: "tenant_provider_created", Category: Security},
	{Name: "tenant_provider_changed", Category: Security},
	{Name: "tenant_provider_deleted", Category: Security},
	{Name: "connection_provider_changed", Category: Security, Visibility: Site},
	{Name: "mail_changed", Category: Security, Visibility: Site, AddressFields: []string{"from", "to"}},
	{Name: "mail_test", Category: Security, Visibility: Site, AddressFields: []string{"to"}},
	// An organisation's own sender (3.6): its administrators see them. The
	// password is never in a row.
	{Name: "tenant_mail_changed", Category: Security, AddressFields: []string{"from", "replyTo"}},
	{Name: "tenant_mail_test", Category: Security, AddressFields: []string{"to"}},
	// An organisation's own captcha provider (3.6): the secret is never in a row.
	{Name: "tenant_captcha_changed", Category: Security},
	{Name: "tenant_captcha_test", Category: Security},
	{Name: "secret_changed", Category: Security},
	// Email domains (3.6 d2): claims, codes, every check by hand, verifying,
	// the recheck's findings, and a site administrator's transfers (each
	// tenant sees its own side of one, never the other tenant).
	{Name: "domain_added", Category: Security},
	{Name: "domain_code_made", Category: Security},
	{Name: "domain_checked", Category: Security},
	{Name: "domain_verified", Category: Security},
	{Name: "domain_changed", Category: Security},
	{Name: "domain_removed", Category: Security},
	{Name: "domain_missing", Category: Security},
	{Name: "domain_lapsed", Category: Security},
	{Name: "domain_assigned", Category: Security},
	{Name: "domain_unassigned", Category: Security},

	// admin: settings, rules, plans, people, invites, groups, roles, tenants,
	// tokens and grants.
	{Name: "setting_changed", Category: Admin},
	{Name: "setting_rule_started", Category: Admin},
	{Name: "setting_rule_ended", Category: Admin},
	{Name: "plan_created", Category: Admin, Visibility: Site},
	{Name: "plan_changed", Category: Admin, Visibility: Site},
	{Name: "plan_deleted", Category: Admin, Visibility: Site},
	{Name: "tenant_plan_changed", Category: Admin},
	{Name: "tenant_created", Category: Admin},
	{Name: "tenant_changed", Category: Admin},
	{Name: "tenant_deleted", Category: Admin},
	{Name: "tenant_console_opened", Category: Admin},
	{Name: "registered", Category: Admin, AddressFields: []string{"email"}},
	// Joining by email domain at sign-up (3.6 d5): an account made at once
	// (auto), a request, its approval or denial by an administrator, the
	// person withdrawing it, and the domain going (it is cancelled). The
	// tenant's administrators see them; the address is the person's, on the
	// tenant's own domain.
	{Name: "join_auto", Category: Admin, AddressFields: []string{"email"}},
	{Name: "join_requested", Category: Admin, AddressFields: []string{"email"}},
	{Name: "join_approved", Category: Admin, AddressFields: []string{"email"}},
	{Name: "join_denied", Category: Admin, AddressFields: []string{"email"}},
	{Name: "join_withdrawn", Category: Admin, AddressFields: []string{"email"}},
	{Name: "join_cancelled", Category: Admin, AddressFields: []string{"email"}},
	{Name: "user_created", Category: Admin, AddressFields: []string{"email"}},
	{Name: "user_changed", Category: Admin, AddressFields: []string{"email", "old.email"}},
	{Name: "user_deleted", Category: Admin},
	{Name: "user_restored", Category: Admin},
	{Name: "user_purged", Category: Admin},
	{Name: "members_batch", Category: Admin, AddressFields: []string{"results[].user"}},
	{Name: "invites_batch", Category: Admin, AddressFields: []string{"results[].user"}},
	{Name: "group_created", Category: Admin},
	{Name: "group_changed", Category: Admin},
	{Name: "group_deleted", Category: Admin},
	{Name: "group_member_added", Category: Admin},
	{Name: "group_member_removed", Category: Admin},
	{Name: "group_members_batch", Category: Admin, AddressFields: []string{"results[].user"}},
	{Name: "group_self_added", Category: Admin},
	{Name: "groups_synced", Category: Admin},
	{Name: "groups_claim_missing", Category: Admin},
	{Name: "groups_claim_truncated", Category: Admin},
	{Name: "space_group_added", Category: Admin},
	{Name: "space_group_changed", Category: Admin},
	{Name: "space_group_removed", Category: Admin},
	{Name: "token_created", Category: Admin},
	{Name: "token_revoked", Category: Admin},
	{Name: "grant_revoked", Category: Admin},
	{Name: "quota_exceeded", Category: Admin},
	// A message a tenant's mail could not be sent by: it has no sender of its own
	// and the site's is not for it (mail.useSite). At most once a day for a tenant.
	{Name: "mail_not_sent", Category: Admin},
	{Name: "usage_exported", Category: Admin},
	{Name: "bulk_started", Category: Admin},
	{Name: "bulk_finished", Category: Admin},

	// data: deleting, restoring, purging, exporting, taking over and
	// transferring spaces, space views, views as a person, opening a page in
	// a view.
	{Name: "space_deleted", Category: Data},
	{Name: "space_restored", Category: Data},
	{Name: "space_purged", Category: Data},
	{Name: "space_taken_over", Category: Data},
	{Name: "space_transferred", Category: Data},
	{Name: "space_viewed", Category: Data},
	{Name: "page_viewed", Category: Data},
	{Name: "view_as_started", Category: Data},
	{Name: "view_as_ended", Category: Data},
	{Name: "export_created", Category: Data},
	{Name: "export_deleted", Category: Data},
	{Name: "export_downloaded", Category: Data},
	{Name: "audit_downloaded", Category: Data},

	// system: what the log says about itself.
	{Name: "dropped", Category: System, Visibility: Site},
	// audit_purged: a site administrator purged rows of the log by hand; the
	// one row the purge's job writes about itself when it ends, which neither a
	// purge nor the retention trim ever deletes.
	{Name: "audit_purged", Category: System, Visibility: Site},

	// signins: a session was started.
	{Name: "login", Category: Signins},

	// reads: a member opened a page or a file of a space (once an hour each).
	{Name: "page_read", Category: Reads},
	{Name: "file_read", Category: Reads},

	// downloads: a file was downloaded as an attachment (once an hour each).
	{Name: "file_downloaded", Category: Downloads},

	// api: a request made with an API token or through MCP.
	{Name: "api_call", Category: API},
}
