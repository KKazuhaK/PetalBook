// Package boundary pins the engine/server import boundary described in
// docs/design/overview.md ("The boundary between the engine and the server").
// It holds only this test.
package boundary

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/KKazuhaK/PetalBook/"

// enginePackages are the packages a single-user offline client embeds: pages,
// the index, git storage and one space. Keep in step with the design doc.
var enginePackages = []string{
	"internal/space", "internal/gitstore", "internal/index", "internal/page",
	"internal/links",
	// Leaf helpers the engine uses; they carry no server concept either.
	"internal/langtag", "internal/sentence", "internal/icons",
}

// forbiddenExternal are import paths the engine may not reach: the server side
// of HTTP, mail, and server databases. An entry matches itself and everything
// below it. The embedded SQLite driver is allowed, since the index is a local
// SQLite file.
var forbiddenExternal = []string{
	"net/http", "net/smtp", "net/rpc",
	"github.com/jackc/pgx", "github.com/lib/pq",
}

// knownExceptions maps "enginePackage -> forbiddenImport" to a reason. Empty:
// the engine had no violations when this test was written.
var knownExceptions = map[string]string{}

type listed struct {
	ImportPath string
	Imports    []string
}

func TestEngineDoesNotImportServer(t *testing.T) {
	args := []string{"list", "-deps", "-json"}
	for _, p := range enginePackages {
		args = append(args, module+p)
	}
	cmd := exec.Command("go", args...)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	graph := map[string][]string{}
	dec := json.NewDecoder(&out)
	for dec.More() {
		var p listed
		if err := dec.Decode(&p); err != nil {
			t.Fatal(err)
		}
		graph[p.ImportPath] = p.Imports
	}

	engine := map[string]bool{}
	for _, p := range enginePackages {
		engine[module+p] = true
	}

	for _, root := range enginePackages {
		start := module + root
		// Breadth-first, so the reported chain is a shortest one.
		parent := map[string]string{start: ""}
		queue := []string{start}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, imp := range graph[cur] {
				if _, seen := parent[imp]; seen {
					continue
				}
				parent[imp] = cur
				if bad := violation(imp, engine); bad != "" {
					// Report the first hit and do not descend into it, so
					// its own imports do not add noise.
					if _, ok := knownExceptions[root+" -> "+imp]; ok {
						continue
					}
					t.Errorf("engine package %s reaches %s (%s):\n  %s",
						root, imp, bad, chain(parent, imp))
					continue
				}
				queue = append(queue, imp)
			}
		}
	}
}

// violation says why imp is not allowed under the engine, or "" if it is.
func violation(imp string, engine map[string]bool) string {
	if strings.HasPrefix(imp, module) {
		if !engine[imp] {
			return "a server-only package of this module"
		}
		return ""
	}
	for _, f := range forbiddenExternal {
		if imp == f || strings.HasPrefix(imp, f+"/") {
			return "a server-side or database-server dependency"
		}
	}
	return ""
}

func chain(parent map[string]string, to string) string {
	var path []string
	for p := to; p != ""; p = parent[p] {
		path = append([]string{strings.TrimPrefix(p, module)}, path...)
	}
	return strings.Join(path, " -> ")
}
