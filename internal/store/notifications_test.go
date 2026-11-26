package store_test

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

func notify(t *testing.T, st *store.Store, user, kind, ref string, at time.Time) string {
	t.Helper()
	id, err := store.InsertNotification(ctx, st.Q(), store.Notification{UserID: user, Tenant: store.DefaultTenantID, Kind: kind, Ref: ref, Created: at})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestNotificationsListNewestFirstInPages(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "member")
		mkUser(t, st, "B", "b", "member")
		base := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
		var ids []string
		for i := 0; i < 5; i++ {
			ids = append(ids, notify(t, st, "A", store.NotifyInvite, fmt.Sprint("S", i), base.Add(time.Duration(i)*time.Minute)))
		}
		notify(t, st, "B", store.NotifyInvite, "other", base)
		notify(t, st, "A", store.NotifySync, "s", base.Add(time.Hour))

		got, err := store.ListNotifications(ctx, st.Q(), "A", store.DefaultTenantID, store.NotifyFilter{Kind: store.NotifyInvite, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		// One more than asked for tells the caller there is another page.
		if len(got) != 3 || got[0].ID != ids[4] || got[1].ID != ids[3] || got[2].ID != ids[2] {
			t.Fatalf("first page: %+v", got)
		}
		got, _ = store.ListNotifications(ctx, st.Q(), "A", store.DefaultTenantID, store.NotifyFilter{Kind: store.NotifyInvite, Limit: 2, Before: ids[3]})
		if len(got) != 3 || got[0].ID != ids[2] || got[2].ID != ids[0] {
			t.Fatalf("second page: %+v", got)
		}
		all, _ := store.ListNotifications(ctx, st.Q(), "A", store.DefaultTenantID, store.NotifyFilter{Limit: 50})
		if len(all) != 6 || all[0].Kind != store.NotifySync {
			t.Fatalf("all: %+v", all)
		}
		// Another person's rows, and another tenant's, are never listed.
		other, _ := store.ListNotifications(ctx, st.Q(), "A", "0000000ZZZ", store.NotifyFilter{})
		if len(other) != 0 {
			t.Fatalf("a row of one tenant listed in another: %+v", other)
		}
		for _, n := range all {
			if n.UserID != "A" {
				t.Fatalf("a row of B: %+v", n)
			}
		}
	})
}

func TestNotificationsReadStateAndTheUnreadCount(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "member")
		mkUser(t, st, "B", "b", "member")
		now := time.Now()
		a1 := notify(t, st, "A", store.NotifyInvite, "1", now)
		a2 := notify(t, st, "A", store.NotifySync, "2", now.Add(time.Second))
		b1 := notify(t, st, "B", store.NotifyInvite, "3", now)
		count := func(u string, limit int) int {
			n, err := store.UnreadCapped(ctx, st.Q(), u, store.DefaultTenantID, limit)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		if count("A", 100) != 2 || count("B", 100) != 1 || count("A", 1) != 1 {
			t.Fatalf("counts %d %d %d", count("A", 100), count("B", 100), count("A", 1))
		}
		// Marking is the person's own: B's id marked by A changes nothing.
		n, err := store.MarkNotificationsRead(ctx, st.Q(), "A", store.DefaultTenantID, []string{a1, b1, "nope"}, now)
		if err != nil || n != 1 {
			t.Fatalf("marked %d: %v", n, err)
		}
		if count("A", 100) != 1 || count("B", 100) != 1 {
			t.Fatalf("after: %d %d", count("A", 100), count("B", 100))
		}
		// Marked twice is marked once.
		if n, _ := store.MarkNotificationsRead(ctx, st.Q(), "A", store.DefaultTenantID, []string{a1}, now); n != 0 {
			t.Fatalf("again: %d", n)
		}
		unread, _ := store.ListNotifications(ctx, st.Q(), "A", store.DefaultTenantID, store.NotifyFilter{UnreadOnly: true})
		if len(unread) != 1 || unread[0].ID != a2 {
			t.Fatalf("unread: %+v", unread)
		}
		// All of one kind, then all.
		if n, _ := store.MarkAllNotificationsRead(ctx, st.Q(), "A", store.DefaultTenantID, store.NotifyInvite, now); n != 0 {
			t.Fatalf("invites: %d", n)
		}
		if n, _ := store.MarkAllNotificationsRead(ctx, st.Q(), "A", store.DefaultTenantID, "", now); n != 1 || count("A", 100) != 0 || count("B", 100) != 1 {
			t.Fatalf("all: %d", n)
		}
		// More than a batch of ids.
		var ids []string
		for i := 0; i < 250; i++ {
			ids = append(ids, notify(t, st, "B", store.NotifyInvite, fmt.Sprint(i), now))
		}
		if n, err := store.MarkNotificationsRead(ctx, st.Q(), "B", store.DefaultTenantID, ids, now); err != nil || n != 250 {
			t.Fatalf("250: %d %v", n, err)
		}
	})
}

func TestNotificationKindsAreARegistry(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "member")
		for _, k := range []string{"", "bogus", "system", store.NotifyProblems} {
			_, err := store.InsertNotification(ctx, st.Q(), store.Notification{UserID: "A", Tenant: store.DefaultTenantID, Kind: k})
			if !errors.Is(err, store.ErrNotifyKind) {
				t.Errorf("kind %q: %v", k, err)
			}
		}
		// The reserved kinds can be written: nothing makes them yet.
		for _, k := range []string{store.NotifyMention, store.NotifyTodo} {
			if _, err := store.InsertNotification(ctx, st.Q(), store.Notification{UserID: "A", Tenant: store.DefaultTenantID, Kind: k}); err != nil {
				t.Errorf("reserved kind %q: %v", k, err)
			}
		}
		// A person who is gone gets nothing, and it is no error.
		if err := store.NotifyUser(ctx, st.Q(), "nobody", store.NotifyInvite, "x", map[string]string{}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := store.NotifyUser(ctx, st.Q(), "A", store.NotifyInvite, "x", map[string]string{"space": "S"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		l, _ := store.ListNotifications(ctx, st.Q(), "A", store.DefaultTenantID, store.NotifyFilter{Kind: store.NotifyInvite})
		if len(l) != 1 || l[0].Payload != `{"space":"S"}` || l[0].Tenant != store.DefaultTenantID {
			t.Fatalf("%+v", l)
		}
		for _, k := range store.NotifyKinds {
			if k.Producer == "" && !k.Derived && k.Kind == store.NotifyInvite {
				t.Errorf("invite has no producer")
			}
		}
	})
}

func TestTheSeenCursorOfADerivedNotification(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "member")
		mkUser(t, st, "B", "b", "member")
		now := time.Now()
		for _, c := range [][3]string{{"A", "problems:S1", "x"}, {"A", "problems:S2", "y"}, {"B", "problems:S1", "z"}} {
			if err := store.SetSeen(ctx, st.Q(), c[0], c[1], c[2], now); err != nil {
				t.Fatal(err)
			}
		}
		// Set again: the signature is replaced, not added.
		if err := store.SetSeen(ctx, st.Q(), "A", "problems:S1", "x2", now); err != nil {
			t.Fatal(err)
		}
		got, _ := store.ListSeen(ctx, st.Q(), "A")
		if len(got) != 2 || got["problems:S1"] != "x2" || got["problems:S2"] != "y" {
			t.Fatalf("%v", got)
		}
		if err := store.ForgetSeen(ctx, st.Q(), "A", []string{"problems:S2"}); err != nil {
			t.Fatal(err)
		}
		got, _ = store.ListSeen(ctx, st.Q(), "A")
		other, _ := store.ListSeen(ctx, st.Q(), "B")
		if len(got) != 1 || got["problems:S2"] != "y" || len(other) != 1 {
			t.Fatalf("%v %v", got, other)
		}
		store.ForgetSeen(ctx, st.Q(), "A", nil)
		if got, _ = store.ListSeen(ctx, st.Q(), "A"); len(got) != 0 {
			t.Fatalf("%v", got)
		}
	})
}

func TestTrimNotificationsKeepsTheRecentAndTheUnread(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "member")
		now := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
		day := 24 * time.Hour
		ancient := notify(t, st, "A", store.NotifyInvite, "ancient", now.Add(-200*day))     // unread, older than the most
		oldRead := notify(t, st, "A", store.NotifyInvite, "old-read", now.Add(-40*day))     // read long ago
		oldUnread := notify(t, st, "A", store.NotifyInvite, "old-unread", now.Add(-40*day)) // still unread
		recent := notify(t, st, "A", store.NotifyInvite, "recent", now.Add(-day))
		if _, err := store.MarkNotificationsRead(ctx, st.Q(), "A", store.DefaultTenantID, []string{oldRead}, now.Add(-35*day)); err != nil {
			t.Fatal(err)
		}
		n, err := store.TrimNotifications(ctx, st.Q(), now)
		if err != nil || n != 2 {
			t.Fatalf("trimmed %d: %v", n, err)
		}
		left, _ := store.ListNotifications(ctx, st.Q(), "A", store.DefaultTenantID, store.NotifyFilter{Limit: 100})
		var got []string
		for _, x := range left {
			got = append(got, x.ID)
		}
		if strings.Join(got, ",") != recent+","+oldUnread {
			t.Fatalf("left %v; ancient %s oldRead %s", got, ancient, oldRead)
		}
		// The cap per person keeps the newest, as the rows come in: no clean-up
		// has to count the rows of everyone.
		for i := 0; i < store.NotifyMaxPerUser+20; i++ {
			notify(t, st, "A", store.NotifySync, fmt.Sprint(i), now.Add(-time.Duration(store.NotifyMaxPerUser+20-i)*time.Second))
		}
		var total int
		if err := st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM notifications WHERE user_id = 'A'`).Scan(&total); err != nil || total != store.NotifyMaxPerUser {
			t.Fatalf("%d rows left: %v", total, err)
		}
	})
}

func TestNotificationQueriesUseTheirIndexes(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "member")
		// Many people, most of what they have read: the shape the indexes are for.
		for u := 0; u < 30; u++ {
			id := fmt.Sprint("U", u)
			mkUser(t, st, id, strings.ToLower(id), "member")
			for i := 0; i < 20; i++ {
				notify(t, st, id, store.NotifyInvite, fmt.Sprint(i), time.Now())
			}
			if _, err := store.MarkAllNotificationsRead(ctx, st.Q(), id, store.DefaultTenantID, "", time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 50; i++ {
			notify(t, st, "A", store.NotifyInvite, fmt.Sprint(i), time.Now())
		}
		withDearScans(t, st, func(q store.Q) {
			if plan := explain(t, st, q, `SELECT id, kind FROM notifications WHERE user_id = ? AND tenant_id = ? AND id < ? ORDER BY id DESC LIMIT 21`, "A", store.DefaultTenantID, "zzz"); !usesIndex(st, plan, "notifications_user") {
				t.Errorf("the list does not use notifications_user:\n%s", plan)
			}
			if plan := explain(t, st, q, `SELECT COUNT(*) FROM (SELECT 1 FROM notifications WHERE user_id = ? AND tenant_id = ? AND read_at IS NULL LIMIT 100) AS u`, "A", store.DefaultTenantID); !strings.Contains(plan, "notifications_unread") {
				t.Errorf("the unread count does not use notifications_unread:\n%s", plan)
			}
		})
	})
}

func TestADeletedPersonTakesTheirNotificationsAlong(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "member")
		notify(t, st, "A", store.NotifyInvite, "1", time.Now())
		if err := store.SetSeen(ctx, st.Q(), "A", "problems:S", "x", time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Q().Exec(ctx, `DELETE FROM users WHERE id = 'A'`); err != nil {
			t.Fatal(err)
		}
		var n, m int
		st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM notifications`).Scan(&n)
		st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM notification_seen`).Scan(&m)
		if n != 0 || m != 0 {
			t.Fatalf("%d notifications and %d cursors left", n, m)
		}
	})
}

var updateKinds = flag.Bool("update-kinds", false, "rewrite web/src/i18n/notifyKinds.json from the registry")

// The web app lists the kinds from this file and has a test that each has its
// text in both languages (web/src/i18n/notify.test.ts).
const kindsFile = "../../web/src/i18n/notifyKinds.json"

// TestNotifyKindsListIsCurrent keeps web/src/i18n/notifyKinds.json equal to
// store.NotifyKinds. When a kind is added, this fails until the list is
// regenerated (go test ./internal/store -run TestNotifyKindsListIsCurrent
// -update-kinds), and then the web test fails until the kind has its texts.
func TestNotifyKindsListIsCurrent(t *testing.T) {
	t.Parallel()
	var kinds []string
	for _, k := range store.NotifyKinds {
		kinds = append(kinds, k.Kind)
	}
	want, err := json.MarshalIndent(kinds, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if *updateKinds {
		if err := os.WriteFile(kindsFile, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(kindsFile)
	if err != nil || string(got) != string(want) {
		t.Fatalf("web/src/i18n/notifyKinds.json is not store.NotifyKinds; run: go test ./internal/store -run TestNotifyKindsListIsCurrent -update-kinds (%v)", err)
	}
}
