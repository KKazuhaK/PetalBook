package api_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"petalbook.app/server/internal/spaces"
)

// The switch for offline copies is decided before the space is opened: a
// tenant with offline copies off is refused without restoring an evicted
// space, by any of the sync routes. Its answer does not depend on the space.
func TestTheOfflineSwitchRefusesBeforeAnEvictedSpaceIsRestored(t *testing.T) {
	t.Parallel()
	n := &netSwitch{}
	eachAccounts(t, storageOpts(n, func(o *spaces.Options) { o.EvictEvery = -1 }), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		remote := filterRemote(t)
		pushVersions(t, remote, 2)
		sid := admin.do("POST", "/api/spaces", map[string]any{"name": "Followed", "remote": connectBody("file://" + remote)}).want(t, 201, "").body["id"].(string)
		base := "/api/spaces/" + sid
		cursor := admin.do("GET", base+"/sync/catalog", nil).want(t, 200, "").body["changes"].(string)
		a.clock.Advance(48 * time.Hour)
		admin.do("POST", "/api/admin/spaces/"+sid+"/evict", nil).want(t, 204, "")
		// An index of another version: the catalogue and the changes would
		// restore the space too, as page bodies do.
		db, err := sql.Open("sqlite", "file:"+filepath.Join(a.dir, "cache", sid, "index.db"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`PRAGMA user_version = 9`); err != nil {
			t.Fatal(err)
		}
		db.Close()
		admin.do("PUT", "/api/admin/settings/features.offline", map[string]any{"value": false}).want(t, 200, "")

		cacheOf := func() any {
			return admin.do("GET", "/api/admin/spaces/"+sid, nil).body["storage"].(map[string]any)["cache"]
		}
		for _, p := range []string{base + "/sync/pages?ids=SNAP000001", base + "/sync/catalog", base + "/changes?since=" + cursor, "/api/sync/heads?space=" + sid} {
			admin.do("GET", p, nil).want(t, 403, "offline_off")
			if c := cacheOf(); c != "evicted" {
				t.Fatalf("%s restored the space although offline copies are off (%v)", p, c)
			}
		}
		// A space that does not exist answers the same: nothing is told about it.
		admin.do("GET", "/api/spaces/NOSUCHSPACE/sync/pages?ids=SNAP000001", nil).want(t, 403, "offline_off")
	})
}
