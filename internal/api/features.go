package api

import (
	"net/http"

	"petalbook.app/server/internal/settings"
)

// The features a route may belong to (docs/api.md, "Features"). A route names
// one in the route table (access.on, or the feature of its key) and handle
// checks it for every caller before the handler or the space is opened: one
// read of the settings' cache, no query. A route that belongs to none says so
// (access.always), so a new route cannot forget to choose.

// featureNames are the features in the words of the refusal: "The <name>
// feature is turned off for this organization." The web app has its own texts
// for them (src/i18n), chosen by the key.
var featureNames = map[string]string{
	settings.FeatureTokens:          "API tokens",
	settings.FeatureMCP:             "MCP",
	settings.OfflineCopies:          "offline copies",
	settings.RegistrationOpen:       "self-registration",
	settings.FeatureTenancy:         "multiple tenants",
	settings.FeatureAdminView:       "administrator view of spaces",
	settings.FeatureAuditDownload:   "audit log download",
	settings.FeatureOwnSSO:          "own single sign-on",
	settings.FeatureDomains:         "claimed email domains",
	settings.FeatureManagedAccounts: "managed accounts",
	settings.FeatureIdPAdmins:       "administrators from the identity provider",
	settings.FeatureOwnCaptcha:      "own CAPTCHA",
	settings.FeatureOwnConnections:  "own repository providers",
	settings.FeatureOwnDaily:        "own daily text",
	settings.FeaturePasskeys:        "passkeys",
	settings.MailOwnSender:          "own mail sender",
}

// featureOffError is the refusal of a feature the tenant does not have: 403
// feature_off, with the feature's key in "feature" (and the permission key
// that needs it, when a key does).
func featureOffError(feature, permission string) *accessError {
	name := featureNames[feature]
	if name == "" {
		name = feature
	}
	return &accessError{status: http.StatusForbidden, code: "feature_off", key: permission, feature: feature,
		msg: "The " + name + " feature is turned off for this organization."}
}

// featureGate is the check of the route's feature for the caller's tenant: one
// read of the settings' cache. It reports false after answering. A route that
// asks a key whose feature it is was checked by allow (which a holder of the
// key across the site is not bound by); in single-user mode every feature is on.
func (s *server) featureGate(c *call, rt route) bool {
	f := rt.access.feature
	if f == "" || c.p == nil || (rt.access.kind == kindKey && rt.access.key.Feature() == f) {
		return true
	}
	if s.single || s.settings == nil {
		return true
	}
	tenant := c.p.User.TenantID
	var on bool
	var err error
	if s.featureFault != nil {
		err = s.featureFault(tenant, f)
	}
	if err == nil {
		on, err = s.settings.Feature(c.r.Context(), tenant, f)
	}
	if err != nil {
		// Not knowing is not "on": the handler does not run (as the hand checks it replaced answered).
		s.fail(c.w, err)
		return false
	}
	if on {
		return true
	}
	s.refuse(c.w, featureOffError(f, ""))
	return false
}
