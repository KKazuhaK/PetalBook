package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/mail/mailtest"
	"petalbook.app/server/internal/store"
)

// fakeMail is the SMTP server of a test: in this process, on a loopback
// port. Nothing in these tests can reach any other mail server.
func fakeMail(t *testing.T) *mailtest.Server { return mailtest.New(t, mailtest.Options{}) }

// enableMail points the server's mail at the fake and switches it on.
func enableMail(t *testing.T, root *client, srv *mailtest.Server) {
	t.Helper()
	root.do("PUT", "/api/admin/mail", map[string]any{"enabled": true, "host": srv.Host(), "port": srv.Port(), "security": "none",
		"from": "notes@example.test", "fromName": "Notes"}).want(t, 200, "")
}

// openRegistration enables mail and opens registration at the site.
func openRegistration(t *testing.T, root *client, srv *mailtest.Server) {
	t.Helper()
	enableMail(t, root, srv)
	root.do("PUT", "/api/admin/settings/features.registration", map[string]any{"value": true}).want(t, 200, "")
}

// verifiedUser makes an account with a verified address and a password.
func verifiedUser(t *testing.T, a *acc, username, email string) {
	t.Helper()
	if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: username, Role: "member", Password: pw, Email: email, EmailVerified: true}); err != nil {
		t.Fatal(err)
	}
}

// lastTo is the last message to an address. The mail queue delivers after the
// request that queued it has been answered, so it waits (up to 5 seconds) for
// the address's first message; a caller that needs a later one waits for it
// first (srv.Wait).
func lastTo(t *testing.T, srv *mailtest.Server, to string) mailtest.Message {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		var found *mailtest.Message
		for _, m := range srv.Messages() {
			m := m
			if m.To == to {
				found = &m
			}
		}
		if found != nil {
			return *found
		}
		if time.Now().After(end) {
			t.Fatalf("no message to %s", to)
		}
	}
}

func countTo(srv *mailtest.Server, to string) int {
	n := 0
	for _, m := range srv.Messages() {
		if m.To == to {
			n++
		}
	}
	return n
}

// settle waits a moment so a message that should not be sent has the time to be.
func settle() { time.Sleep(60 * time.Millisecond) }

func rowCount(t *testing.T, a *acc, query string, args ...any) int {
	t.Helper()
	var n int
	q := a.st.Q()
	if err := q.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func auditKinds(t *testing.T, root *client, kind string) []any {
	t.Helper()
	r := root.do("GET", "/api/admin/audit?kind="+kind, nil).want(t, 200, "")
	items, _ := r.body["items"].([]any)
	return items
}

func contains(s string, subs ...string) bool {
	for _, x := range subs {
		if !strings.Contains(s, x) {
			return false
		}
	}
	return true
}

var _ = store.DefaultTenantID
