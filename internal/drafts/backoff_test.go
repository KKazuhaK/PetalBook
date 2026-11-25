package drafts

import (
	"testing"
	"time"

	"petalbook.app/server/internal/store"
)

func TestUnavailableDelayGrowsAndIsBounded(t *testing.T) {
	s := &Service{backoff: map[string]int{}}
	d := &store.Draft{SpaceID: "S", PageID: "P", UserID: "U"}
	var got []time.Duration
	for i := 0; i < 8; i++ {
		got = append(got, s.unavailableDelay(d))
	}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 10 * time.Minute, 10 * time.Minute, 10 * time.Minute}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delays %v, want %v", got, want)
		}
	}
	// Another draft starts again, and a success forgets the count.
	other := &store.Draft{SpaceID: "S", PageID: "Q", UserID: "U"}
	if s.unavailableDelay(other) != 30*time.Second {
		t.Fatal("the delay of one draft leaked into another")
	}
	s.forget(d)
	if s.unavailableDelay(d) != 30*time.Second {
		t.Fatal("a draft that was applied keeps its delay")
	}
	// The memory of delays is bounded.
	for i := 0; i < maxBackoffs+50; i++ {
		s.unavailableDelay(&store.Draft{SpaceID: "S", PageID: "P", UserID: string(rune('a'+i%26)) + time.Duration(i).String()})
	}
	if len(s.backoff) > maxBackoffs {
		t.Fatalf("%d entries", len(s.backoff))
	}
}
