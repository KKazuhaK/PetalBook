// Package limits is the limits sheet of 3.6: for every tenant, the capabilities
// (what it may have at all) and the limits (how much), each from the first
// present of a manual value, a billing value, the tenant's plan and the site's
// default (docs/api.md, "The limits sheet"). It is server-only: no engine
// package imports it.
//
// A tenant's effective sheet is computed in memory, at start and again for the
// tenant a change touches; a check is one map read on an immutable Sheet and
// never a query.
package limits

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
)

// Kind is the type of a key's value.
type Kind uint8

const (
	// KindBool is true or false.
	KindBool Kind = iota
	// KindLimit is a whole number of at least 0, or null: no limit. In memory
	// it is an int64, or nil for null.
	KindLimit
	// KindList is a list of distinct strings, each one of the key's Allowed.
	KindList
)

func (k Kind) String() string {
	switch k {
	case KindBool:
		return "boolean"
	case KindLimit:
		return "limit"
	}
	return "list"
}

// Def defines a key of the sheet.
type Def struct {
	Key  string
	Kind Kind
	// Default is the site's value until a site administrator changes it: a bool,
	// an int64 or nil (KindLimit), a []string (KindList).
	Default any
	// NoNull makes a KindLimit key refuse null: it has a number or nothing.
	NoNull bool
	// Allowed are the entries a KindList value may hold.
	Allowed []string
	// Capability is true for a key that is a capability (a switch of what a
	// tenant may have); the others are limits. The capability keys are the ones
	// internal/authz names.
	Capability  bool
	Description string
}

// StorageModes are the modes a space may sync in (docs/api.md, "Storage modes").
var StorageModes = []string{"server", "own", "tenant"}

// registry lists the keys in the order of docs/api.md ("Capabilities", then
// "The limits sheet"). Keys that stand for a set of numbers in the capability
// table (storage.quota and storage.perPerson, each with spaces, bytes and
// pages) are one limit each here, which is how the sheet's own table lists
// them.
var registry = []Def{
	{Key: "signin.sso", Kind: KindBool, Default: true, Capability: true, Description: "The tenant's own SSO providers, joining through them, the tenant layer of auth.ssoOnly"},
	{Key: "signin.domains", Kind: KindBool, Default: true, Capability: true, Description: "Email domain claims, the SSO entry by domain, joining by domain, restrictOrgs"},
	{Key: "signin.managedAccounts", Kind: KindBool, Default: true, Capability: true, Description: "Managed accounts; they also need signin.sso and signin.domains"},
	{Key: "signin.socialChoice", Kind: KindBool, Default: true, Capability: true, Description: "The tenant layer of auth.social"},
	{Key: "signin.idpAdmins", Kind: KindBool, Default: false, Capability: true, Description: "groups.admins on the tenant's SSO providers"},
	{Key: "data.adminView", Kind: KindBool, Default: true, Capability: true, Description: "Tenant administrators viewing and exporting the content of the organisation's spaces"},
	{Key: "data.auditDownload", Kind: KindBool, Default: true, Capability: true, Description: "Downloading the audit log"},
	{Key: "integrations.smtp", Kind: KindBool, Default: true, Capability: true, Description: "The tenant's own SMTP"},
	{Key: "integrations.captcha", Kind: KindBool, Default: true, Capability: true, Description: "The tenant's own captcha keys"},
	{Key: "integrations.connections", Kind: KindBool, Default: true, Capability: true, Description: "The tenant's own providers, also as a storage target"},
	{Key: "integrations.daily", Kind: KindBool, Default: true, Capability: true, Description: "The tenant's own daily-text interfaces and lines"},
	{Key: "integrations.tokens", Kind: KindBool, Default: true, Capability: true, Description: "API tokens of the tenant's people, and the tenant layer of access.tokens"},
	{Key: "integrations.mcp", Kind: KindBool, Default: true, Capability: true, Description: "MCP and OAuth grants of the tenant's people, and the tenant layer of access.mcp"},
	{Key: "other.offline", Kind: KindBool, Default: true, Capability: true, Description: "Offline copies of the tenant's spaces, and the tenant layer of offline.copies"},
	{Key: "other.registration", Kind: KindBool, Default: true, Capability: true, Description: "The tenant layer of registration.*"},
	{Key: "sync.assetsRepository", Kind: KindBool, Default: true, Capability: true, Description: "Whether the tenant's spaces may use an attachment repository (off: storage.assets is space)"},
	{Key: "storage.modes", Kind: KindList, Default: StorageModes, Allowed: StorageModes, Capability: true, Description: "The storage modes the tenant may use for new spaces"},
	{Key: "storage.maxFileBytes", Kind: KindLimit, Default: nil, Description: "The ceiling of the tenant's files.maxBytes, in bytes; null leaves --max-upload"},
	{Key: "storage.pinnedBytes", Kind: KindLimit, Default: int64(0), NoNull: true, Description: "What the tenant's administrators may pin, in bytes"},
	{Key: "storage.quota.spaces", Kind: KindLimit, Description: "Spaces in the tenant in total"},
	{Key: "storage.quota.bytes", Kind: KindLimit, Description: "Storage of the tenant in total, in bytes; also its allocation for storage pressure"},
	{Key: "storage.quota.pages", Kind: KindLimit, Description: "Pages in the tenant in total"},
	{Key: "storage.perPerson.spaces", Kind: KindLimit, Description: "The ceiling of the tenant's spacesPerPerson"},
	{Key: "storage.perPerson.bytes", Kind: KindLimit, Description: "The ceiling of the tenant's bytesPerPerson"},
	{Key: "storage.perPerson.pages", Kind: KindLimit, Description: "The ceiling of the tenant's pagesPerPerson"},
	{Key: "other.people", Kind: KindLimit, Description: "People in the tenant (counted and shown, not refused)"},
	{Key: "usage.apiPerMinute", Kind: KindLimit, Description: "API-token requests per minute over the tenant"},
	{Key: "usage.mcpPerMinute", Kind: KindLimit, Description: "MCP requests per minute over the tenant"},
	{Key: "usage.mailPerDay", Kind: KindLimit, Description: "Mail sent for the tenant per day"},
	{Key: "usage.outboundPerMinute", Kind: KindLimit, Description: "The tenant's outgoing calls to external interfaces per minute; null is the site's outbound.tenantPerMinute"},
}

// index is the position of each key in the registry, and in every Sheet.
var index = func() map[string]int {
	m := make(map[string]int, len(registry))
	for i, d := range registry {
		if _, dup := m[d.Key]; dup {
			panic("limits: duplicate key " + d.Key)
		}
		m[d.Key] = i
	}
	return m
}()

// Defs returns the definitions in order. The caller must not change them.
func Defs() []Def { return registry }

// Lookup returns the definition of a key.
func Lookup(key string) (Def, bool) {
	i, ok := index[key]
	if !ok {
		return Def{}, false
	}
	return registry[i], true
}

// Parse checks a JSON value against the key's definition and returns it as it
// is held in memory: a bool, an int64 or nil, or a []string.
func Parse(key string, raw json.RawMessage) (any, error) {
	d, ok := Lookup(key)
	if !ok {
		return nil, fmt.Errorf("%s is not a key of the limits sheet", key)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%s: more than one value", key)
	}
	switch d.Kind {
	case KindBool:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("%s is true or false", key)
		}
		return b, nil
	case KindLimit:
		if v == nil {
			if d.NoNull {
				return nil, fmt.Errorf("%s is a number, not null", key)
			}
			return nil, nil
		}
		n, ok := v.(json.Number)
		if !ok {
			return nil, fmt.Errorf("%s is a number or null", key)
		}
		i, err := n.Int64()
		if err != nil || i < 0 {
			return nil, fmt.Errorf("%s is a whole number of at least 0, or null", key)
		}
		return i, nil
	}
	items, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s is a list", key)
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, ok := it.(string)
		if !ok || !slices.Contains(d.Allowed, s) {
			return nil, fmt.Errorf("%s: %v is not one of %v", key, it, d.Allowed)
		}
		if slices.Contains(out, s) {
			return nil, fmt.Errorf("%s: %s twice", key, s)
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}

// parseSet parses a JSON object of key to value, the shape of a plan's values
// and of the site's default.
func parseSet(raw string) (map[string]any, error) {
	if raw == "" {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	out := make(map[string]any, len(m))
	for k, r := range m {
		v, err := Parse(k, r)
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}
