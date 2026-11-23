package auth

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Groups from sign-in (milestone 3.2): a provider maps the values of a groups
// claim to synced groups, and every sign-in brings the person's memberships
// from that provider up to date. See docs/api.md, "Groups from sign-in".

const (
	maxGroupMap      = 1000
	maxClaimValues   = 1000
	maxClaimBytes    = 256
	maxGitHubOrgs    = 500
	defaultOIDCClaim = "groups"
	missingKeep      = "keep"
	missingClear     = "clear"
)

// GroupMapEntry maps one claim value to a synced group.
type GroupMapEntry struct {
	Value   string `json:"value"`
	GroupID string `json:"groupId"`
}

// GroupsCfg is a provider's groups setting, as stored and as shown.
type GroupsCfg struct {
	Enabled bool            `json:"enabled"`
	Claim   string          `json:"claim"`
	Missing string          `json:"missing"`
	Map     []GroupMapEntry `json:"map"`
}

// groupsView is the setting with its defaults filled in, never nil slices.
func groupsView(kind string, g *GroupsCfg) GroupsCfg {
	v := GroupsCfg{Missing: missingKeep, Map: []GroupMapEntry{}}
	if g != nil {
		v.Enabled, v.Claim = g.Enabled, g.Claim
		if g.Missing != "" {
			v.Missing = g.Missing
		}
		v.Map = append(v.Map, g.Map...)
	}
	if kind == KindOIDC && v.Claim == "" {
		v.Claim = defaultOIDCClaim
	}
	if kind == KindGitHub {
		v.Claim = ""
	}
	return v
}

// claimName is the claim or attribute the values are read from.
func (g *GroupsCfg) claimName(kind string) string {
	if g.Claim == "" && kind == KindOIDC {
		return defaultOIDCClaim
	}
	return g.Claim
}

// groupsInput is the `groups` of a provider request; every field is optional.
type groupsInput struct {
	Enabled *bool            `json:"enabled"`
	Claim   *string          `json:"claim"`
	Missing *string          `json:"missing"`
	Map     *[]GroupMapEntry `json:"map"`
}

func badControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

// applyGroups checks the groups part of a provider request and puts it in b.
func (s *Service) applyGroups(ctx context.Context, b *built, in *groupsInput) error {
	kind := b.row.Kind
	var g GroupsCfg
	if b.cfg.Groups != nil {
		g = *b.cfg.Groups
		g.Map = slices.Clone(g.Map)
	}
	mark := func(f string) { b.changed = append(b.changed, "groups."+f) }
	if in.Enabled != nil {
		if *in.Enabled != g.Enabled {
			mark("enabled")
		}
		g.Enabled = *in.Enabled
	}
	if in.Claim != nil {
		c := strings.TrimSpace(*in.Claim)
		if kind == KindGitHub && c != "" {
			return providerInvalid("a GitHub provider's groups are the organisations: groups.claim cannot be set")
		}
		if len(c) > maxClaimBytes || !utf8.ValidString(c) || badControl(c) {
			return providerInvalid("groups.claim is at most %d bytes of text", maxClaimBytes)
		}
		if c != g.Claim {
			mark("claim")
		}
		g.Claim = c
	}
	if in.Missing != nil {
		switch *in.Missing {
		case missingKeep, missingClear:
		case "":
			*in.Missing = missingKeep
		default:
			return providerInvalid("groups.missing is keep or clear")
		}
		if *in.Missing != orKeep(g.Missing) {
			mark("missing")
		}
		g.Missing = *in.Missing
	}
	if in.Map != nil {
		list := *in.Map
		if len(list) > maxGroupMap {
			return providerInvalid("groups.map has at most %d entries", maxGroupMap)
		}
		seen := map[string]bool{}
		var ids []string
		for _, e := range list {
			if e.Value == "" || len(e.Value) > maxClaimBytes || !utf8.ValidString(e.Value) || badControl(e.Value) {
				return providerInvalid("a groups.map value is 1 to %d bytes of text", maxClaimBytes)
			}
			if !validGroupID(e.GroupID) {
				return coded(404, "group_not_found", "a groups.map entry names a group that does not exist")
			}
			ids = append(ids, e.GroupID)
		}
		for _, e := range list {
			if seen[e.Value] {
				return coded(409, "exists", "the value %q is mapped twice", e.Value)
			}
			seen[e.Value] = true
		}
		sources, err := store.GroupSources(ctx, s.st.Q(), ids)
		if err != nil {
			return err
		}
		for _, e := range list {
			src, ok := sources[e.GroupID]
			if !ok {
				return coded(404, "group_not_found", "a groups.map entry names a group that does not exist")
			}
			if src != store.GroupSynced {
				return coded(409, "group_not_synced", "only a synced group can be mapped; %s is local", e.GroupID)
			}
		}
		if !slices.Equal(list, g.Map) {
			mark("map")
		}
		g.Map = slices.Clone(list)
	}
	if g.Enabled && kind == KindSAML && g.Claim == "" {
		return providerInvalid("groups.claim is the name of the SAML attribute that holds the groups")
	}
	if g.Enabled || b.cfg.Groups != nil || in.Map != nil || in.Claim != nil || in.Missing != nil {
		b.cfg.Groups = &g
	}
	return nil
}

func orKeep(m string) string {
	if m == "" {
		return missingKeep
	}
	return m
}

func validGroupID(id string) bool { return page.ValidID(id) }

// mapIndex is value -> group for the sync of a sign-in.
func (c *GroupsCfg) mapIndex() map[string]string {
	idx := make(map[string]string, len(c.Map))
	for _, e := range c.Map {
		idx[e.Value] = e.GroupID
	}
	return idx
}

// groupClaim is what a sign-in said about the person's groups.
type groupClaim struct {
	// read: this sign-in was asked for groups at all.
	read bool
	// present: the claim was there (possibly empty). Overage: the identity
	// provider sent a pointer instead of the list.
	present, overage bool
	values           []string
	truncated        bool
}

func (g *groupClaim) add(v string) {
	if len(v) == 0 || len(v) > maxClaimBytes {
		return
	}
	if len(g.values) >= maxClaimValues {
		g.truncated = true
		return
	}
	g.values = append(g.values, v)
}

// oidcGroupClaim reads the claim from the verified ID token's claims.
func oidcGroupClaim(cfg *GroupsCfg, raw map[string]json.RawMessage) groupClaim {
	gc := groupClaim{read: true}
	name := cfg.claimName(KindOIDC)
	v, ok := raw[name]
	if ok && string(v) != "null" {
		var one string
		var many []json.RawMessage
		switch {
		case json.Unmarshal(v, &one) == nil:
			gc.present = true
			gc.add(one)
		case json.Unmarshal(v, &many) == nil:
			gc.present = true
			for _, m := range many {
				var str string
				if json.Unmarshal(m, &str) == nil {
					gc.add(str)
				}
			}
		}
	}
	if !gc.present {
		// Entra ID names the claim in _claim_names instead of sending it.
		var names map[string]json.RawMessage
		if cn, ok := raw["_claim_names"]; ok && json.Unmarshal(cn, &names) == nil {
			if _, over := names[name]; over {
				gc.overage = true
			}
		}
	}
	return gc
}

// samlGroupClaim reads every value of the attribute.
func samlGroupClaim(cfg *GroupsCfg, attrs map[string][]string) groupClaim {
	gc := groupClaim{read: true}
	vals, ok := attrs[cfg.claimName(KindSAML)]
	if ok {
		gc.present = true
		for _, v := range vals {
			gc.add(v)
		}
	}
	return gc
}

// groupsOn reports whether the provider syncs groups, and its setting.
func (rt *providerRT) groupsOn() (*GroupsCfg, bool) {
	g := rt.cfg.Groups
	return g, g != nil && g.Enabled
}

// syncGroups brings the person's groups from this provider up to date after a
// sign-in. It never refuses the sign-in: a failure is logged and the person
// keeps what they had.
func (s *Service) syncGroups(ctx context.Context, rt *providerRT, userID, username, ip string, gc groupClaim) {
	cfg, on := rt.groupsOn()
	if !on || s.cfg.Spaces.Single() {
		return
	}
	slug := rt.row.Slug
	missing := orKeep(cfg.Missing)
	if !gc.present {
		reason := "absent"
		if gc.overage {
			reason = "overage"
		}
		s.Audit(AuditEvent{Kind: "groups_claim_missing", User: userID, Username: username, IP: ip, Detail: map[string]any{"provider": slug, "missing": missing, "reason": reason}})
		if missing == missingKeep {
			return
		}
		gc.values = nil
	}
	if gc.truncated {
		s.Audit(AuditEvent{Kind: "groups_claim_truncated", User: userID, Username: username, IP: ip, Detail: map[string]any{"provider": slug, "limit": "values"}})
	}
	idx := rt.groupIdx()
	var want []store.SyncedMembership
	for _, v := range gc.values {
		if gid, ok := idx[v]; ok {
			m := store.SyncedMembership{GroupID: gid, Value: v}
			if !slices.Contains(want, m) {
				want = append(want, m)
			}
		}
	}
	res, err := s.cfg.Spaces.SyncProviderGroups(ctx, userID, slug, want)
	if err != nil {
		s.log.Error("could not sync the groups of a sign-in", "provider", slug, "user", userID, "error", err)
		return
	}
	if res.Limited {
		s.Audit(AuditEvent{Kind: "groups_claim_truncated", User: userID, Username: username, IP: ip, Detail: map[string]any{"provider": slug, "limit": "group_limit"}})
	}
	if res.Changed() {
		s.Audit(AuditEvent{Kind: "groups_synced", User: userID, Username: username, IP: ip, Detail: map[string]any{"provider": slug, "added": nonNil(res.Added), "removed": nonNil(res.Removed)}})
	}
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// groupIdx is the provider's value -> group table, built once with the provider.
func (rt *providerRT) groupIdx() map[string]string {
	rt.idxOnce.Do(func() {
		if rt.cfg.Groups != nil {
			rt.idx = rt.cfg.Groups.mapIndex()
		}
	})
	return rt.idx
}

// GroupMappings counts the provider entries that map to a group.
func (s *Service) GroupMappings(groupID string) int {
	n := 0
	for _, rt := range s.provs.all() {
		if rt.cfg.Groups == nil {
			continue
		}
		for _, e := range rt.cfg.Groups.Map {
			if e.GroupID == groupID {
				n++
			}
		}
	}
	return n
}

// UnmapGroup removes every entry that maps to a group, from every provider
// (the group is being deleted). Memberships go with the group's rows.
func (s *Service) UnmapGroup(ctx context.Context, groupID string) error {
	providerMu.Lock()
	defer providerMu.Unlock()
	var touched []string
	for _, rt := range s.provs.all() {
		g := rt.cfg.Groups
		if g == nil || !slices.ContainsFunc(g.Map, func(e GroupMapEntry) bool { return e.GroupID == groupID }) {
			continue
		}
		cfg := rt.cfg
		ng := *g
		ng.Map = slices.DeleteFunc(slices.Clone(g.Map), func(e GroupMapEntry) bool { return e.GroupID == groupID })
		cfg.Groups = &ng
		raw, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		row := rt.row
		row.Config, row.Updated = string(raw), s.now().UTC()
		row.StatusOK, row.StatusChecked, row.StatusMessage = rt.statusOK, rt.statusChecked, rt.statusMessage
		if err := store.UpdateProvider(ctx, s.st.Q(), row); err != nil {
			return err
		}
		touched = append(touched, row.Slug)
	}
	if len(touched) == 0 {
		return nil
	}
	return s.reloadProviders(ctx, touched...)
}

// dropUnmapped removes at once the memberships brought by entries that are no
// longer in the map, or that now point to another group.
func (s *Service) dropUnmapped(ctx context.Context, slug string, old, cur *GroupsCfg) {
	if old == nil {
		return
	}
	now := map[string]string{}
	if cur != nil {
		now = cur.mapIndex()
	}
	for _, e := range old.Map {
		if gid, ok := now[e.Value]; ok && gid == e.GroupID {
			continue
		}
		if err := s.cfg.Spaces.DropMapped(ctx, slug, e.Value, now[e.Value]); err != nil {
			s.log.Error("could not remove the memberships of a mapping entry", "provider", slug, "error", err)
		}
	}
}

// MappedFrom is a provider and claim value that feed a group.
type MappedFrom struct {
	Provider string `json:"provider"`
	Value    string `json:"value"`
}

// MappedFromOf says, for each of ids, which providers' claim values feed it.
func (s *Service) MappedFromOf(ids []string) map[string][]MappedFrom {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	out := map[string][]MappedFrom{}
	for _, rt := range s.provs.all() {
		if rt.cfg.Groups == nil {
			continue
		}
		for _, e := range rt.cfg.Groups.Map {
			if want[e.GroupID] {
				out[e.GroupID] = append(out[e.GroupID], MappedFrom{rt.row.Slug, e.Value})
			}
		}
	}
	return out
}
