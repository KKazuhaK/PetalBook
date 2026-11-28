package mail_test

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"petalbook.app/server/internal/mail"
	"petalbook.app/server/internal/mail/mailtest"
)

// tarpit accepts connections and never says a word.
func tarpit(t *testing.T) (port int, conns *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conns = new(atomic.Int64)
	var held []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			held = append(held, c)
			conns.Add(1)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		for _, c := range held {
			c.Close()
		}
	})
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ = strconv.Atoi(p)
	return port, conns
}

// A tenant whose mail server stalls holds one delivery and delays only its own
// messages: the site's go out on time, and another tenant's too.
func TestAStalledTenantServerNeverDelaysTheSitesMail(t *testing.T) {
	site := mailtest.New(t, mailtest.Options{})
	other := mailtest.New(t, mailtest.Options{})
	port, conns := tarpit(t)
	s := mail.New(mail.Options{})
	s.Configure(settings(site, mail.None))
	stalled := mail.Settings{Enabled: true, Host: "127.0.0.1", Port: port, Security: mail.None, From: "a@example.test"}
	s.SetTenant("STALLED001", &stalled)
	s.SetTenant("OTHER00001", func() *mail.Settings { c := settings(other, mail.None); return &c }())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	for range 5 {
		if err := s.EnqueueVia("STALLED001", "STALLED001", false, msg("a@example.test")); err != nil {
			t.Fatal(err)
		}
	}
	for end := time.Now().Add(5 * time.Second); conns.Load() == 0 && time.Now().Before(end); time.Sleep(time.Millisecond) {
	}
	if err := s.Enqueue(msg("site@example.test")); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueVia("OTHER00001", "OTHER00001", false, msg("other@example.test")); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	site.Wait(1)
	other.Wait(1)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v", d)
	}
	// One delivery at a time for the stalled tenant, whatever it has waiting.
	time.Sleep(100 * time.Millisecond)
	if n := conns.Load(); n != 1 {
		t.Fatalf("%d connections to one tenant's server at once", n)
	}
}

// The tenants' senders take nothing from the site's queue, and the site keeps
// room for its security mail when the rest is full.
func TestTheSitesQueueIsItsOwnAndKeepsRoomForSecurityMail(t *testing.T) {
	s := mail.New(mail.Options{})
	c := mail.Settings{Enabled: true, Host: "127.0.0.1", Port: 1, Security: mail.None, From: "a@example.test"}
	s.Configure(c)
	for i := range mail.MaxTenantQueued / mail.MaxPerSender {
		id := "TENANT" + strconv.Itoa(1000+i)
		s.SetTenant(id, &c)
		for range mail.MaxPerSender {
			if err := s.EnqueueVia(id, id, false, msg("a@example.test")); err != nil {
				t.Fatal(i, err)
			}
		}
	}
	for i := range mail.MaxQueue - mail.SecurityReserve {
		if err := s.EnqueueVia("", "T"+strconv.Itoa(i/100), false, msg("a@example.test")); err != nil {
			t.Fatal(i, err)
		}
	}
	if err := s.EnqueueVia("", "", false, msg("a@example.test")); !errors.Is(err, mail.ErrQueueFull) {
		t.Fatalf("ordinary mail used the reserve: %v", err)
	}
	for range mail.SecurityReserve {
		if err := s.EnqueueVia("", "", true, msg("a@example.test")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnqueueVia("", "", true, msg("a@example.test")); !errors.Is(err, mail.ErrQueueFull) {
		t.Fatal(err)
	}
}

// What a tenant may no longer use is dropped at delivery, not sent another way.
func TestAMessageOfATenantThatMayNotUseItsSenderAnymoreIsDropped(t *testing.T) {
	site := mailtest.New(t, mailtest.Options{})
	own := mailtest.New(t, mailtest.Options{})
	s := mail.New(mail.Options{})
	s.Configure(settings(site, mail.None))
	oc := settings(own, mail.None)
	s.SetTenant("TENANT0001", &oc)
	var allowed atomic.Bool
	allowed.Store(true)
	s.SetRouteAllowed(func(string) bool { return allowed.Load() })
	allowed.Store(false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	if err := s.EnqueueVia("TENANT0001", "TENANT0001", false, msg("a@example.test")); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(5 * time.Second); s.Waiting() != 0 && time.Now().Before(end); time.Sleep(time.Millisecond) {
	}
	time.Sleep(50 * time.Millisecond)
	if len(own.Messages())+len(site.Messages()) != 0 || s.Waiting() != 0 {
		t.Fatal("the message was sent, or is still waiting")
	}
}

// A refused address is told as "not a public address", without the address.
func TestAnAddressThatIsNotPublicIsRefusedWithoutBeingNamed(t *testing.T) {
	srv := mailtest.New(t, mailtest.Options{})
	s := mail.New(mail.Options{})
	c := settings(srv, mail.None)
	c.Control = func(string, string, syscall.RawConn) error { return mail.ErrNotPublic }
	err := s.TestFor(context.Background(), "TENANT0001", c, msg("a@example.test"))
	if err == nil || mail.Describe(err, &c) != "not a public address" || srv.Connections() != 0 {
		t.Fatalf("%v", err)
	}
	if st := s.TenantStatus("TENANT0001"); st.Message != "not a public address" {
		t.Fatalf("%q", st.Message)
	}
}
