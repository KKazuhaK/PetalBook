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
	{Name: "email_added", Category: Security},
	{Name: "email_verified", Category: Security},
	{Name: "email_primary_changed", Category: Security},
	{Name: "email_removed", Category: Security},
	{Name: "email_claim_withdrawn", Category: Security},
	{Name: "email_released", Category: Security},
	{Name: "account_connected", Category: Security},
	{Name: "account_disconnected", Category: Security},
	{Name: "provider_created", Category: Security, Visibility: Site},
	{Name: "provider_changed", Category: Security, Visibility: Site},
	{Name: "provider_deleted", Category: Security, Visibility: Site},
	{Name: "connection_provider_changed", Category: Security, Visibility: Site},
	{Name: "mail_changed", Category: Security, Visibility: Site},
	{Name: "mail_test", Category: Security, Visibility: Site},
	{Name: "secret_changed", Category: Security},

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
	{Name: "registered", Category: Admin},
	{Name: "user_changed", Category: Admin},
	{Name: "user_deleted", Category: Admin},
	{Name: "user_restored", Category: Admin},
	{Name: "user_purged", Category: Admin},
	{Name: "members_batch", Category: Admin},
	{Name: "invites_batch", Category: Admin},
	{Name: "group_created", Category: Admin},
	{Name: "group_changed", Category: Admin},
	{Name: "group_deleted", Category: Admin},
	{Name: "group_member_added", Category: Admin},
	{Name: "group_member_removed", Category: Admin},
	{Name: "group_members_batch", Category: Admin},
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
