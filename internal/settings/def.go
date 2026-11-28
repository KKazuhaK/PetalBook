// Package settings is the layered settings of docs/api.md ("Settings by
// layer"): a setting has a key, a kind and the layers it exists in (site,
// tenant, space, personal). Each layer either sets a value or follows the one
// above, and each layer says what the layers below it may do: allow, restrict
// (a subset of options, or a number at most max) or lock. A value that a lock
// or a restriction forbids is kept, marked as not in effect, and counts again
// when the rule is lifted.
//
// The site and tenant layers live in the state database (layered_settings);
// the space layer is space.yml and the personal layer a table of the person's
// own, both read by the code that owns them. The effective value of a tenant is
// cached and read in one map lookup (see Service.For).
package settings

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"petalbook.app/server/internal/store"
)

// Layer is one level of the hierarchy.
type Layer string

// The layers, from the widest.
const (
	Site   Layer = "site"
	Tenant Layer = "tenant"
	Space  Layer = "space"
	Person Layer = "person"
)

// Kind is what values of a setting are.
type Kind string

// The kinds.
const (
	Bool   Kind = "boolean"
	Choice Kind = "choice"
	Limit  Kind = "limit" // a number; smaller is stricter; null may mean none
	List   Kind = "list"  // a list of strings
	Text   Kind = "text"  // a string
	Doc    Kind = "doc"   // a JSON document the owner of the setting validates
)

// Modes of a Lower.
const (
	Allow = "allow"
	// Tighten lets a lower layer set a value at least as strict as the value
	// above it (the site's own value, or its value for the tenant); the key's
	// Strict says what stricter means. Between Allow and Restrict.
	Tighten  = "tighten"
	Restrict = "restrict"
	Lock     = "lock"
)

// Strictness says which of two values of a key is the stricter, which a
// tighten rule needs. A key without one cannot be tightened.
type Strictness string

// The orders of strictness.
const (
	// StrictHigher: a larger number is stricter, or true for a boolean.
	StrictHigher Strictness = "higher"
	// StrictLower: a smaller number is stricter, or false for a boolean. For a
	// number, null (no limit) is the loosest.
	StrictLower Strictness = "lower"
	// StrictOrder: a choice is the stricter the later it is in Choices.
	StrictOrder Strictness = "order"
	// StrictSubset: a list is stricter when it holds fewer of the entries
	// allowed above ("*", the entry for all, is the loosest of all).
	StrictSubset Strictness = "subset"
)

// MaxNote is the most characters of a rule's note.
const MaxNote = store.MaxLayeredNote

// Lower says what the layers below may do.
type Lower struct {
	Mode string `json:"mode"`
	// Options are the values a lower layer may choose (restrict, choice and
	// boolean settings; a boolean may only be restricted to [false]).
	Options []any `json:"options,omitempty"`
	// Max is the largest number a lower layer may set (restrict, limit settings).
	Max *int64 `json:"max,omitempty"`
	// Note is the reason the rule is shown with, at most MaxNote characters. It
	// is stored in the note column of the row, never in the rule's JSON, and
	// read back into the views. NoteSet says a writer sent one (an empty note
	// clears it); a writer that sends none keeps the one stored.
	Note    string `json:"note,omitempty"`
	NoteSet bool   `json:"-"`
}

// UnmarshalJSON reads a rule and remembers whether it carried a note.
func (l *Lower) UnmarshalJSON(b []byte) error {
	type plain Lower
	aux := struct {
		*plain
		Note *string `json:"note"`
	}{plain: (*plain)(l)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	l.Note, l.NoteSet = "", aux.Note != nil
	if aux.Note != nil {
		l.Note = *aux.Note
	}
	return nil
}

// Def defines a setting.
type Def struct {
	Key    string
	Kind   Kind
	Layers []Layer
	// Default is the site's value until an administrator sets one.
	Default any
	// Lower is what the site lets the layers below do until an administrator says.
	Lower Lower
	// Choices are the values of a choice setting, loosest first.
	Choices []string
	// Min and Max bound a limit; Nullable lets it be null (no limit).
	Min, Max int64
	Nullable bool
	// Check validates and normalises a value of any layer beyond its kind (a
	// domain list, an address); nil accepts what the kind accepts.
	Check func(v any) (any, error)
	// Doc is true when the layers' values are validated elsewhere (Kind Doc).

	// A setting with no tenant layer says one of two things. ForTenant: a site
	// administrator may still set it for one tenant, as the site's rule for that
	// tenant (see Rule and Service.SetTenant); the tenant's administrators only
	// see it. ServerWide: why it must be the same for every tenant (usernames
	// unique on the whole server, one purge job over the server's disk). Every
	// definition has exactly one of a tenant layer, ForTenant and ServerWide;
	// the registry's test checks it.
	ForTenant  bool
	ServerWide string
	// Outward marks a boolean that lets content leave the organisation or be
	// seen without signing in (public links, publishing, anonymous access,
	// external sharing, machine access). The stricter first-start profiles
	// (profile.go) switch every such setting off, and their test fails for one
	// that is not listed.
	Outward bool
	// Strict says which of two values is the stricter, so that a site (or its rule
	// for a tenant) may set the lower mode tighten: a layer below then holds a
	// value at least as strict as the one above (docs/api.md, "Forced,
	// minimum, default"). "" means the key cannot be tightened.
	Strict Strictness
	// TenantDefault, when set, is the value of a tenant that neither sets the
	// setting nor has a rule for it: the site's own value is then for the
	// site's own people (the default tenant) only and never flows into a
	// tenant (registration.mode). The tenant layer shows it as the site's.
	TenantDefault any
}

// looser reports whether v is looser than above under the key's strictness;
// false for a key without one.
func (d *Def) looser(v, above any) bool {
	switch d.Strict {
	case StrictHigher, StrictLower:
		switch d.Kind {
		case Bool:
			a, ok1 := above.(bool)
			b, ok2 := v.(bool)
			if !ok1 || !ok2 || a == b {
				return false
			}
			return b == (d.Strict == StrictLower) // the stricter value is above's
		case Limit:
			if above == nil {
				return false
			}
			if v == nil {
				return true
			}
			a, ok1 := toInt(above)
			b, ok2 := toInt(v)
			if !ok1 || !ok2 {
				return false
			}
			if d.Strict == StrictLower {
				return b > a
			}
			return b < a
		}
	case StrictOrder:
		a, ok1 := above.(string)
		b, ok2 := v.(string)
		if !ok1 || !ok2 || a == b {
			return false
		}
		// A floor that is not one of the choices holds nothing open: it is the strictest.
		return slices.Index(d.Choices, a) < 0 || slices.Index(d.Choices, b) < slices.Index(d.Choices, a)
	case StrictSubset:
		a, ok1 := toStrings(above)
		b, ok2 := toStrings(v)
		if !ok1 || !ok2 || slices.Contains(a, "*") {
			return false
		}
		if slices.Contains(b, "*") {
			return true
		}
		for _, x := range b {
			if !slices.Contains(a, x) {
				return true
			}
		}
	}
	return false
}

// heldTo is what the layer below the site is held to and sits on: the rule
// the site's layer lays on it and the value above it. That is the site's own
// layer, unless a rule for this tenant is in effect (rule, nil for none):
// its value and its lower replace the site's, each only when it gives one.
// A key with a TenantDefault is not held by the site's own layer at all (its
// value governs the site's own people): the rule is then allow and the value
// TenantDefault, and only a rule for the tenant holds it. byRule says the rule
// for the tenant is what holds it.
func (d *Def) heldTo(site SiteView, rule *RuleView) (above Lower, floor any, byRule bool) {
	above, floor = site.Lower, site.Value
	if d.TenantDefault != nil {
		above, floor = Lower{Mode: Allow}, d.TenantDefault
	}
	if rule != nil && rule.InEffect {
		if rule.Value != nil {
			floor = rule.Value
		}
		if rule.Lower != nil {
			above, byRule = *rule.Lower, true
		}
	}
	return above, floor, byRule
}

// Has reports whether the setting exists in layer l.
func (d *Def) Has(l Layer) bool { return slices.Contains(d.Layers, l) }

// IsFeature reports whether the setting is a feature: a whole thing a tenant may
// or may not have, a boolean named features.* (and mail.ownSender and
// accounts.multipleEmails). A route that belongs to one is refused while it is
// off (the route table's access.on).
func (d *Def) IsFeature() bool {
	return d.Kind == Bool && (strings.HasPrefix(d.Key, "features.") || d.Key == MailOwnSender || d.Key == AccountsMultipleEmails)
}

// PerTenant reports whether the setting can differ between tenants: it has a
// tenant layer, or a site administrator may force it for one tenant.
func (d *Def) PerTenant() bool { return d.Has(Tenant) || d.ForTenant }

// Error is a refusal; Code is the API's error code.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// The codes of the package (see docs/api.md).
const (
	CodeInvalid    = "invalid"
	CodeNotFound   = "not_found"
	CodeConflict   = "conflict"
	CodeLocked     = "setting_locked"
	CodeRestricted = "setting_restricted"
	// CodeAboveMax: a value the site gives above the maximum it holds that
	// value to (its own restriction, or its rule's for one tenant), which
	// would only be capped.
	CodeAboveMax = "setting_above_max"
	// CodeUntilPast: a rule for a tenant whose until is already past, which
	// would never count.
	CodeUntilPast = "setting_until_past"
	// CodeRuleNeedsMax: a plan's or billing's rule for a limit the tenant also
	// sets, with a value but no maximum or lock: it would not cap the tenant.
	CodeRuleNeedsMax = "setting_rule_needs_max"
)

// aboveMax refuses a site value over the restriction it is held to; a value
// a restriction would cap is refused instead of stored and silently capped.
// Only a limit has a maximum; nil is no limit, above any.
func aboveMax(d *Def, l Lower, v any) error {
	if d.Kind != Limit || l.Mode != Restrict || l.Allows(d, v) {
		return nil
	}
	return &Error{Code: CodeAboveMax, Msg: "The value is above the maximum; raise the maximum first."}
}

func bad(format string, a ...any) *Error {
	return &Error{Code: CodeInvalid, Msg: fmt.Sprintf(format, a...)}
}

// Value checks a value against the kind and range of the definition and
// returns it in its normal form (a number as int64, a list as []string).
func (d *Def) Value(v any) (any, error) {
	switch d.Kind {
	case Bool:
		b, ok := v.(bool)
		if !ok {
			return nil, bad("%s is true or false", d.Key)
		}
		v = b
	case Choice:
		s, ok := v.(string)
		if !ok || !slices.Contains(d.Choices, s) {
			return nil, bad("%s is one of %s", d.Key, strings.Join(d.Choices, ", "))
		}
		v = s
	case Limit:
		if v == nil {
			if !d.Nullable {
				return nil, bad("%s is a number from %d to %d", d.Key, d.Min, d.Max)
			}
			return nil, nil
		}
		n, ok := toInt(v)
		if !ok || n < d.Min || n > d.Max {
			return nil, bad("%s is a number from %d to %d", d.Key, d.Min, d.Max)
		}
		v = n
	case List:
		l, ok := toStrings(v)
		if !ok {
			return nil, bad("%s is a list of text", d.Key)
		}
		v = l
	case Text:
		s, ok := v.(string)
		if !ok {
			return nil, bad("%s is text", d.Key)
		}
		v = s
	}
	if d.Check != nil && v != nil {
		return d.Check(v)
	}
	return v, nil
}

func toInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		if n == float64(int64(n)) {
			return int64(n), true
		}
	}
	return 0, false
}

func toStrings(v any) ([]string, bool) {
	switch l := v.(type) {
	case []string:
		return append([]string{}, l...), true
	case []any:
		out := make([]string, len(l))
		for i, x := range l {
			s, ok := x.(string)
			if !ok {
				return nil, false
			}
			out[i] = s
		}
		return out, true
	}
	return nil, false
}

// CheckLower validates a Lower for this setting.
func (d *Def) CheckLower(l Lower) (Lower, error) {
	switch l.Mode {
	case Allow, Lock:
		return Lower{Mode: l.Mode}, nil
	case Tighten:
		if d.Strict == "" {
			return Lower{}, bad("%s has no order of strictness, so it cannot be tightened", d.Key)
		}
		if !d.Has(Tenant) {
			return Lower{}, bad("%s has no layer below to tighten", d.Key)
		}
		return Lower{Mode: Tighten}, nil
	case Restrict:
	default:
		return Lower{}, bad("lower.mode is allow, tighten, restrict or lock")
	}
	switch d.Kind {
	case Bool:
		if len(l.Options) != 1 || l.Options[0] != false {
			return Lower{}, bad("a boolean setting can only be restricted to [false]")
		}
		return Lower{Mode: Restrict, Options: []any{false}}, nil
	case Choice:
		if len(l.Options) == 0 {
			return Lower{}, bad("restrict needs options")
		}
		seen := map[string]bool{}
		var out []any
		for _, o := range l.Options {
			s, ok := o.(string)
			if !ok || !slices.Contains(d.Choices, s) {
				return Lower{}, bad("%s: options are some of %s", d.Key, strings.Join(d.Choices, ", "))
			}
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
		return Lower{Mode: Restrict, Options: out}, nil
	case Limit:
		if l.Max == nil || *l.Max < d.Min || *l.Max > d.Max {
			return Lower{}, bad("%s: max is a number from %d to %d", d.Key, d.Min, d.Max)
		}
		m := *l.Max
		return Lower{Mode: Restrict, Max: &m}, nil
	}
	return Lower{}, bad("%s cannot be restricted, only allowed or locked", d.Key)
}

// Allows reports whether a layer below may hold the value under this Lower.
func (l Lower) Allows(d *Def, v any) bool {
	switch l.Mode {
	case Lock:
		return false
	case Restrict:
		switch d.Kind {
		case Bool:
			return v == false
		case Choice:
			s, _ := v.(string)
			return slices.Contains(optStrings(l.Options), s)
		case Limit:
			n, ok := toInt(v)
			return ok && l.Max != nil && n <= *l.Max // null (no limit) is looser than any max
		}
		return false
	}
	return true
}

// AllowsOver is Allows for a layer whose value sits on base, the value above
// it: under tighten a value is allowed when it is not looser than base.
func (l Lower) AllowsOver(d *Def, v, base any) bool {
	if l.Mode == Tighten {
		return !d.looser(v, base)
	}
	return l.Allows(d, v)
}

// Nearest is v when the Lower allows it, else the value it allows nearest to
// v: what a restriction makes of a value in effect it forbids (the site's
// value, or the site's value for the tenant), so "at most 10" means at most
// 10 whatever the layers above hold. By kind:
//
//   - a limit: the max (also for null, no limit);
//   - a boolean: false, the only value a boolean may be restricted to;
//   - a choice: the next stricter allowed choice (Choices are loosest first),
//     or, when every allowed one is looser, the strictest allowed.
//
// A lock or allow, and a kind that cannot be restricted, leave v as it is.
func (l Lower) Nearest(d *Def, v any) any {
	if l.Mode != Restrict || l.Allows(d, v) {
		return v
	}
	switch d.Kind {
	case Limit:
		if l.Max != nil {
			return *l.Max
		}
	case Bool:
		return false
	case Choice:
		opts := optStrings(l.Options)
		s, _ := v.(string)
		at := slices.Index(d.Choices, s)
		for i := at + 1; i < len(d.Choices); i++ {
			if slices.Contains(opts, d.Choices[i]) {
				return d.Choices[i]
			}
		}
		for i := min(at, len(d.Choices)) - 1; i >= 0; i-- {
			if slices.Contains(opts, d.Choices[i]) {
				return d.Choices[i]
			}
		}
	}
	return v
}

func optStrings(o []any) []string {
	out := make([]string, 0, len(o))
	for _, x := range o {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Tightens reports whether l is at least as strict as above.
func (l Lower) Tightens(d *Def, above Lower) bool {
	switch above.Mode {
	case Lock:
		return l.Mode == Lock
	case Restrict:
		switch l.Mode {
		case Lock:
			return true
		case Restrict:
			switch d.Kind {
			case Bool:
				return true
			case Choice:
				for _, o := range optStrings(l.Options) {
					if !slices.Contains(optStrings(above.Options), o) {
						return false
					}
				}
				return true
			case Limit:
				return l.Max != nil && above.Max != nil && *l.Max <= *above.Max
			}
		}
		return false
	case Tighten:
		return l.Mode != Allow
	}
	return true
}

// Combine is the rule a layer two steps down meets: the stricter of what the
// layers above allow.
func (l Lower) Combine(d *Def, below Lower) Lower {
	if below.Tightens(d, l) {
		return below
	}
	return l
}

// decode reads stored JSON text.
func decode(raw string) (any, error) {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, err
	}
	return v, nil
}

// noteOf checks the note of a rule a writer sends: the note to store, whether
// to replace the stored one (a note sent, or a rule that no longer limits
// anything has none), and a refusal. l is nil when the rule is removed.
func noteOf(l *Lower) (note string, set bool, err error) {
	switch {
	case l == nil:
		return "", true, nil
	case l.NoteSet:
		if n := utf8.RuneCountInString(l.Note); n > MaxNote {
			return "", false, bad("note is at most %d characters", MaxNote)
		}
		if l.Mode == Allow && l.Note != "" {
			return "", false, bad("a rule that allows everything has no note")
		}
		return l.Note, true, nil
	}
	return "", l.Mode == Allow, nil
}
