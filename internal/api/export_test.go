package api

// HeaderPurpose is what a stored header value is bound to, for tests that
// write settings as an earlier version saved them.
var HeaderPurpose = headerPurpose

// IndexOnlyRoutes lists the routes an evicted space answers from its index.
func IndexOnlyRoutes() []string {
	out := make([]string, 0, len(indexOnlyRoutes))
	for r := range indexOnlyRoutes {
		out = append(out, r)
	}
	return out
}
