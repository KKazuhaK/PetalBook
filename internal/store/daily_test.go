package store_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

func TestSiteSettings(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		if _, ok, err := store.GetSiteSetting(ctx, st.Q(), "daily"); ok || err != nil {
			t.Fatalf("%v %v", ok, err)
		}
		for _, v := range []string{`{"a":1}`, `{"a":2,"b":"字"}`} {
			if err := store.SetSiteSetting(ctx, st.Q(), "daily", v, time.Now()); err != nil {
				t.Fatal(err)
			}
			if got, ok, err := store.GetSiteSetting(ctx, st.Q(), "daily"); !ok || err != nil || got != v {
				t.Fatalf("%q %v %v", got, ok, err)
			}
		}
		if _, ok, _ := store.GetSiteSetting(ctx, st.Q(), "other"); ok {
			t.Fatal("another key")
		}
	})
}

func TestDailyLines(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		replace := func(lang string, bodies ...string) (int, error) {
			var total int
			err := st.Tx(ctx, func(q store.Q) error {
				lines := make([]store.DailyLine, len(bodies))
				for i, b := range bodies {
					lines[i] = store.DailyLine{Body: b, Source: "s" + b}
				}
				var err error
				total, err = store.ReplaceDailyLines(ctx, q, lang, lines)
				return err
			})
			return total, err
		}
		if n, err := replace("zh", "甲", "乙", "丙"); err != nil || n != 3 {
			t.Fatalf("%d %v", n, err)
		}
		if n, err := replace("en", "a", "b"); err != nil || n != 5 {
			t.Fatalf("%d %v", n, err)
		}
		if n, err := replace("", "any"); err != nil || n != 6 {
			t.Fatalf("%d %v", n, err)
		}
		counts, err := store.DailyLineCounts(ctx, st.Q())
		if err != nil || counts["zh"] != 3 || counts["en"] != 2 || counts[""] != 1 || len(counts) != 3 {
			t.Fatalf("%v %v", counts, err)
		}
		// In the order they came, with their source, one language at a time.
		for i, want := range []string{"甲", "乙", "丙"} {
			l, err := store.NthDailyLine(ctx, st.Q(), "zh", i)
			if err != nil || l.Body != want || l.Source != "s"+want {
				t.Fatalf("%d: %+v %v", i, l, err)
			}
		}
		if _, err := store.NthDailyLine(ctx, st.Q(), "zh", 3); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		// Replacing a language leaves the others, and puts the new lines in their order.
		if n, err := replace("zh", "丁", "戊"); err != nil || n != 5 {
			t.Fatalf("%d %v", n, err)
		}
		if l, _ := store.NthDailyLine(ctx, st.Q(), "zh", 0); l.Body != "丁" {
			t.Fatalf("%+v", l)
		}
		if l, _ := store.NthDailyLine(ctx, st.Q(), "en", 1); l.Body != "b" {
			t.Fatalf("%+v", l)
		}
		if err := store.DeleteDailyLines(ctx, st.Q(), "en"); err != nil {
			t.Fatal(err)
		}
		if counts, _ := store.DailyLineCounts(ctx, st.Q()); counts["en"] != 0 || counts["zh"] != 2 {
			t.Fatalf("%v", counts)
		}
		// The most there may be, over all languages.
		many := make([]string, store.MaxDailyLines)
		for i := range many {
			many[i] = fmt.Sprint("line ", i)
		}
		if _, err := replace("fr", many...); !errors.Is(err, store.ErrTooManyLines) {
			t.Fatalf("%v", err)
		}
		if counts, _ := store.DailyLineCounts(ctx, st.Q()); counts["zh"] != 2 || counts["fr"] != 0 {
			t.Fatalf("a refused replace changed the lines: %v", counts)
		}
	})
}

func TestSpaceDailyChoice(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkSpace(t, st, "S1", "one")
		d, err := store.GetSpaceDaily(ctx, st.Q(), "S1")
		if err != nil || !d.Inherit || d.APIs != "[]" {
			t.Fatalf("%+v %v", d, err)
		}
		for _, want := range []store.SpaceDaily{{Inherit: false, APIs: `["a","b"]`}, {Inherit: true, APIs: `[]`}} {
			if err := store.SetSpaceDaily(ctx, st.Q(), "S1", want); err != nil {
				t.Fatal(err)
			}
			if got, err := store.GetSpaceDaily(ctx, st.Q(), "S1"); err != nil || got != want {
				t.Fatalf("%+v %v", got, err)
			}
		}
		store.SetSpaceDaily(ctx, st.Q(), "S1", store.SpaceDaily{APIs: `["x"]`})
		if err := st.Tx(ctx, func(q store.Q) error { _, err := store.DeleteSpace(ctx, q, store.AllTenants(), "S1"); return err }); err != nil {
			t.Fatal(err)
		}
		var n int
		st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM space_daily`).Scan(&n)
		if n != 0 {
			t.Fatalf("%d", n)
		}
	})
}

func TestDailyCache(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		at := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
		put := func(key, lang, period string, skip int, body string, when time.Time) {
			t.Helper()
			if err := store.PutDailyCache(ctx, st.Q(), key, lang, period, skip, store.DailyCached{Body: body, Fetched: when}); err != nil {
				t.Fatal(err)
			}
		}
		if _, ok, err := store.GetDailyCache(ctx, st.Q(), "a:1", "zh", "d:2026-11-23", 0); ok || err != nil {
			t.Fatalf("%v %v", ok, err)
		}
		put("a:1", "zh", "d:2026-11-23", 0, `{"text":"one"}`, at)
		put("a:1", "zh", "d:2026-11-23", 1, `{"text":"two"}`, at.Add(time.Minute))
		put("a:1", "zh", "d:2026-11-24", 0, `{"text":"three"}`, at.Add(24*time.Hour))
		put("a:1", "en", "d:2026-11-23", 0, `{"text":"english"}`, at.Add(48*time.Hour))
		put("a:2", "zh", "d:2026-11-23", 0, `{"text":"other"}`, at.Add(72*time.Hour))
		got, ok, err := store.GetDailyCache(ctx, st.Q(), "a:1", "zh", "d:2026-11-23", 1)
		if err != nil || !ok || got.Body != `{"text":"two"}` || !got.Fetched.Equal(at.Add(time.Minute)) {
			t.Fatalf("%+v %v %v", got, ok, err)
		}
		// Putting again replaces.
		put("a:1", "zh", "d:2026-11-23", 1, `{"text":"two again"}`, at.Add(2*time.Minute))
		if got, _, _ := store.GetDailyCache(ctx, st.Q(), "a:1", "zh", "d:2026-11-23", 1); got.Body != `{"text":"two again"}` {
			t.Fatalf("%+v", got)
		}
		// The newest for an interface and a language, whatever the period.
		if l, ok, err := store.LatestDailyCache(ctx, st.Q(), "a:1", "zh"); err != nil || !ok || l.Body != `{"text":"three"}` {
			t.Fatalf("%+v %v %v", l, ok, err)
		}
		if _, ok, _ := store.LatestDailyCache(ctx, st.Q(), "a:3", "zh"); ok {
			t.Fatal("another interface")
		}
		// Pruning removes what is older than the time.
		if err := store.PruneDailyCache(ctx, st.Q(), at.Add(36*time.Hour)); err != nil {
			t.Fatal(err)
		}
		var n int
		st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM daily_cache`).Scan(&n)
		if n != 2 {
			t.Fatalf("%d rows after the pruning", n)
		}
		if _, ok, _ := store.LatestDailyCache(ctx, st.Q(), "a:1", "zh"); ok {
			t.Fatal("a pruned row is still the latest")
		}
		// Trimming keeps the newest of an interface, and touches no other.
		for i := 0; i < 10; i++ {
			put("t:1", fmt.Sprint("l", i%3), fmt.Sprint("p", i), i, `{"text":"x"}`, at.Add(time.Duration(i)*time.Minute))
		}
		if err := store.TrimDailyCache(ctx, st.Q(), "t:1", 4); err != nil {
			t.Fatal(err)
		}
		st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM daily_cache WHERE api_key = 't:1'`).Scan(&n)
		if n != 4 {
			t.Fatalf("%d rows after trimming to 4", n)
		}
		if _, ok, _ := store.GetDailyCache(ctx, st.Q(), "t:1", "l0", "p9", 9); !ok {
			t.Fatal("the newest went")
		}
		if err := store.TrimDailyCache(ctx, st.Q(), "a:2", 4); err != nil {
			t.Fatal(err)
		}
		st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM daily_cache`).Scan(&n)
		if n != 6 {
			t.Fatalf("%d rows", n)
		}
	})
}

// A setting is locked for the rest of a transaction: concurrent changes that
// read, change and write it lose nothing.
func TestLockSiteSetting(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		const n = 8
		errs := make(chan error, n)
		for i := 0; i < n; i++ {
			go func() {
				errs <- st.Tx(ctx, func(q store.Q) error {
					if err := store.LockSiteSetting(ctx, q, "k", time.Now()); err != nil {
						return err
					}
					v, _, err := store.GetSiteSetting(ctx, q, "k")
					if err != nil {
						return err
					}
					time.Sleep(5 * time.Millisecond)
					return store.SetSiteSetting(ctx, q, "k", v+"x", time.Now())
				})
			}()
		}
		for i := 0; i < n; i++ {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		if v, _, _ := store.GetSiteSetting(ctx, st.Q(), "k"); v != "{}"+strings.Repeat("x", n) {
			t.Fatalf("%q", v)
		}
	})
}
