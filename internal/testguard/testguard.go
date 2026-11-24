// Package testguard stops a test binary whose heap runs away before it takes
// the whole machine with it.
//
// A test that builds something without bound, such as a YAML document whose
// aliases expand into millions of nodes, grows until the kernel's OOM killer
// steps in, and on a shared machine that can be someone else's process. With
// the guard the binary fails on its own at a fixed size, and the goroutine
// dump it prints names the test that was running.
package testguard

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"time"
)

// DefaultLimitMiB is well above what the largest package needs (internal/space
// peaks under 1 GiB of RSS even with the race detector) and well below what a
// shared development machine can give one process.
const DefaultLimitMiB = 2048

// EnvLimit sets the limit in MiB; 0 turns the guard off.
const EnvLimit = "PETALBOOK_TEST_HEAP_MIB"

const heapObjects = "/memory/classes/heap/objects:bytes"

// Heap starts the guard. Call it first in TestMain.
func Heap() {
	limit, ok := limitBytes()
	if !ok {
		return
	}
	// Let the collector work harder near the limit first, so that garbage is
	// never mistaken for a runaway heap. GOMEMLIMIT, when set, still wins.
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(limit)
	}
	go watch(limit)
}

func limitBytes() (int64, bool) {
	mib := int64(DefaultLimitMiB)
	if v := os.Getenv(EnvLimit); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			fmt.Fprintf(os.Stderr, "testguard: %s=%q is not a number of MiB; using %d\n", EnvLimit, v, mib)
		} else {
			mib = n
		}
	}
	return mib << 20, mib > 0
}

func watch(limit int64) {
	sample := []metrics.Sample{{Name: heapObjects}}
	for range time.Tick(100 * time.Millisecond) {
		if heap(sample) <= limit {
			continue
		}
		// What is above the limit may be garbage the collector has not got to.
		runtime.GC()
		if h := heap(sample); h > limit {
			debug.SetTraceback("all")
			panic(fmt.Sprintf("testguard: the heap holds %d MiB, more than the %d MiB a test may use; "+
				"the goroutine of the test that was running is below. "+
				"If a test really needs more, raise %s.", h>>20, limit>>20, EnvLimit))
		}
	}
}

func heap(s []metrics.Sample) int64 {
	metrics.Read(s)
	return int64(s[0].Value.Uint64())
}
