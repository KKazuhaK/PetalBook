package mail_test

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	"petalbook.app/server/internal/mail"
	"petalbook.app/server/internal/mail/mailtest"
)

// A tenant's own sender is a route of its own: the message goes to its server,
// not the site's, and a retry goes the same way even if the site's settings
// change meanwhile.
func TestATenantsMessageGoesByItsRouteAndARetryKeepsIt(t *testing.T) {
	site := mailtest.New(t, mailtest.Options{})
	ten := mailtest.New(t, mailtest.Options{FailData: "451 try later"})
	s := mail.New(mail.Options{Backoff: [2]time.Duration{10 * time.Millisecond, 10 * time.Millisecond}})
	s.Configure(settings(site, mail.None))
	tc := settings(ten, mail.None)
	tc.FromName = "Acme"
	s.SetTenant("TENANT0001", &tc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	if err := s.EnqueueVia("TENANT0001", "TENANT0001", false, msg("a@example.test")); err != nil {
		t.Fatal(err)
	}
	for ten.Connections() < 1 {
		time.Sleep(time.Millisecond)
	}
	// The site's settings change, and the tenant's server recovers: the retry
	// still goes to the tenant's server.
	dead := settings(site, mail.None)
	dead.Port = 1
	s.Configure(dead)
	ten.SetFailData("")
	got := ten.Wait(1)
	if len(got) != 1 || len(site.Messages()) != 0 {
		t.Fatalf("tenant %d, site %d", len(got), len(site.Messages()))
	}
	if !strings.Contains(got[0].Raw, `From: "Acme" <notes@example.test>`) {
		t.Fatal(got[0].Raw)
	}
	for end := time.Now().Add(5 * time.Second); !s.TenantStatus("TENANT0001").OK && time.Now().Before(end); time.Sleep(time.Millisecond) {
	}
	if !s.TenantStatus("TENANT0001").OK || s.LastStatus().OK {
		t.Fatalf("tenant %+v, site %+v", s.TenantStatus("TENANT0001"), s.LastStatus())
	}
}

func TestTakingATenantsSenderAwayDropsItsMessagesOnly(t *testing.T) {
	s := mail.New(mail.Options{})
	c := mail.Settings{Enabled: true, Host: "127.0.0.1", Port: 1, Security: mail.None, From: "a@example.test"}
	s.Configure(c)
	s.SetTenant("TENANT0001", &c)
	if err := s.EnqueueVia("TENANT0001", "TENANT0001", false, msg("a@example.test")); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(msg("b@example.test")); err != nil {
		t.Fatal(err)
	}
	s.SetTenant("TENANT0001", nil)
	if s.Waiting() != 1 || s.TenantEnabled("TENANT0001") {
		t.Fatalf("waiting %d", s.Waiting())
	}
	if err := s.EnqueueVia("TENANT0001", "", false, msg("c@example.test")); !errors.Is(err, mail.ErrNotEnabled) {
		t.Fatal(err)
	}
	// The site off does not touch a tenant's own sender.
	s.SetTenant("TENANT0001", &c)
	if err := s.EnqueueVia("TENANT0001", "", false, msg("d@example.test")); err != nil {
		t.Fatal(err)
	}
	s.Configure(mail.Settings{})
	if s.Waiting() != 1 {
		t.Fatalf("waiting %d", s.Waiting())
	}
}

func TestATenantHasAtMostTwoHundredWaiting(t *testing.T) {
	s := mail.New(mail.Options{})
	c := mail.Settings{Enabled: true, Host: "127.0.0.1", Port: 1, Security: mail.None, From: "a@example.test"}
	s.Configure(c)
	s.SetTenant("TENANT0001", &c)
	for range mail.MaxPerSender {
		if err := s.EnqueueVia("TENANT0001", "TENANT0001", false, msg("a@example.test")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnqueueVia("TENANT0001", "TENANT0001", false, msg("a@example.test")); !errors.Is(err, mail.ErrSenderFull) {
		t.Fatal(err)
	}
	// Nor may its use of the site's mailer pass 200, and another tenant is not held up.
	for range mail.MaxPerSender {
		if err := s.EnqueueVia("", "TENANT0002", false, msg("a@example.test")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnqueueVia("", "TENANT0002", false, msg("a@example.test")); !errors.Is(err, mail.ErrSenderFull) {
		t.Fatal(err)
	}
	if err := s.EnqueueVia("", "TENANT0003", false, msg("a@example.test")); err != nil {
		t.Fatal(err)
	}
}

func TestAControlCanRefuseTheConnection(t *testing.T) {
	srv := mailtest.New(t, mailtest.Options{})
	s := mail.New(mail.Options{})
	c := settings(srv, mail.None)
	c.Control = func(string, string, syscall.RawConn) error { return errors.New("not a public address") }
	err := s.Test(context.Background(), c, msg("a@example.test"))
	if err == nil || !strings.Contains(err.Error(), "not a public address") || srv.Connections() != 0 {
		t.Fatalf("%v, %d connections", err, srv.Connections())
	}
}
