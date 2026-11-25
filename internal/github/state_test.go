package github_test

import (
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// The App's settings are read again from the database while other calls are
// using the App they took a moment before: a reload must publish a new value,
// never rewrite the one those calls hold (run with -race).
func TestTheAppIsReloadedWhileItIsInUse(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.configure()
		var wg sync.WaitGroup
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					h.clk.Advance(time.Millisecond) // past SettingsTTL: every call reads the settings again
					h.svc.RenewDue(ctx)
					if _, err := h.svc.View(ctx); err != nil {
						t.Error(err)
						return
					}
				}
			}()
		}
		wg.Wait()
		if !h.svc.Configured(ctx) {
			t.Fatal("the App is gone")
		}
	})
}
