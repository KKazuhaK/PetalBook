package settings

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"petalbook.app/server/internal/store"
)

// ProfileRecord is the site record that says which profile the database was
// seeded with.
const ProfileRecord = "profile"

// Profile names.
const (
	ProfileDefault = "default"
	ProfileCN      = "cn"
)

// profiles are the values of a first-start profile: plain site values (not
// locks) that an administrator can change afterwards. Every key must exist in
// the registry (the test fails for one that was renamed). default is the
// registry's defaults and seeds nothing.
//
// cn keeps GitHub on (it is reachable from mainland China, only slow) and
// switches off what lets content leave the organisation: every setting the
// registry marks Outward must be listed here.
var profiles = map[string][]KeyValue{
	ProfileDefault: nil,
	ProfileCN: {
		{Key: FeatureTokens, Value: false},
		{Key: FeatureMCP, Value: false},
	},
}

// ProfileNames are the profiles, sorted.
func ProfileNames() []string {
	names := make([]string, 0, len(profiles))
	for n := range profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ProfileValues are the site values the profile seeds.
func ProfileValues(name string) ([]KeyValue, bool) {
	v, ok := profiles[name]
	return slices.Clone(v), ok
}

// SeedProfile applies a profile on the first start. When the database is empty
// (no account, no site value, no earlier profile) it writes the profile's
// values as ordinary site values (set_by "profile:<name>") and records the
// name; seeded is true. On any other database nothing is written: a profile
// that differs from the one seeded (a database from before profiles counts as
// "default") is an error, an equal one is ignored. An empty name is no flag:
// the default profile is recorded on an empty database, so that a later flag
// is refused.
func (s *Service) SeedProfile(ctx context.Context, name string) (seeded bool, err error) {
	flagged := name != ""
	if !flagged {
		name = ProfileDefault
	}
	vals, ok := profiles[name]
	if !ok {
		return false, fmt.Errorf("unknown profile %q (one of %v)", name, ProfileNames())
	}
	q := s.st.Q()
	if have, _, err := store.GetRecord(ctx, q, ProfileRecord); err == nil {
		return false, profileKept(flagged, name, have)
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	empty, err := s.emptyDatabase(ctx)
	if err != nil {
		return false, err
	}
	if !empty {
		// Started before profiles: it has the default's values.
		return false, profileKept(flagged, name, ProfileDefault)
	}
	// The record is the arbiter between two first starts at once: the loser's
	// insert does nothing and it sees the winner's profile.
	if _, err := q.Exec(ctx, `INSERT INTO site_records(key, value, updated) VALUES (?, ?, ?) ON CONFLICT (key) DO NOTHING`,
		ProfileRecord, name, time.Now().UnixMilli()); err != nil {
		return false, err
	}
	if have, _, err := store.GetRecord(ctx, q, ProfileRecord); err != nil {
		return false, err
	} else if have != name {
		return false, profileKept(flagged, name, have)
	}
	if len(vals) == 0 {
		return true, nil
	}
	if _, err := s.ForceSite(ctx, "profile:"+name, vals); err != nil {
		_, _ = q.Exec(ctx, `DELETE FROM site_records WHERE key = ?`, ProfileRecord)
		return false, fmt.Errorf("seed profile %s: %w", name, err)
	}
	return true, nil
}

// profileKept is the answer on a database that already has a profile: an equal
// one (or no flag) is fine, a different one is refused.
func profileKept(flagged bool, want, have string) error {
	if flagged && want != have {
		return fmt.Errorf("--profile %s: this database was set up with the %s profile, and a profile is only applied on the first start; change single settings in the administration instead", want, have)
	}
	return nil
}

func (s *Service) emptyDatabase(ctx context.Context) (bool, error) {
	q := s.st.Q()
	users, err := store.HasUsers(ctx, q)
	if err != nil || users {
		return false, err
	}
	rows, err := store.ListLayered(ctx, q, store.LayeredSite, "")
	if err != nil {
		return false, err
	}
	return len(rows) == 0, nil
}
