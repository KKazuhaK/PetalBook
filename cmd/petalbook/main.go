// Command petalbook is the PetalBook server: a notes and writing app whose
// pages are Markdown files in a Git repository.
package main

import (
	"errors"
	"fmt"
	"os"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

// buildTime is set at build time when wanted: -ldflags "-X main.buildTime=2026-11-25T14:48:00Z". Without it the admin status
// shows the time of the commit the binary was built from.
var buildTime string

func main() {
	if err := newRoot().Execute(); err != nil {
		var ee exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// exitError makes main exit with a code without printing anything more.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }
