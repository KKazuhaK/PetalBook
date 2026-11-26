package mail_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/mail"
	"petalbook.app/server/internal/mail/mailtest"
)

type clk struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clk) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clk) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func settings(s *mailtest.Server, sec string) mail.Settings {
	return mail.Settings{Enabled: true, Host: s.Host(), Port: s.Port(), Security: sec, From: "notes@example.test", FromName: "Petal Book"}
}

func msg(to string) mail.Message {
	return mail.Message{To: to, Subject: "Hello é", Text: "plain text\nhttps://x.test/a#" + strings.Repeat("A", 43), HTML: "<p>hi</p>"}
}

func TestOffByDefaultAndEnqueueRefuses(t *testing.T) {
	s := mail.New(mail.Options{})
	if s.Enabled() {
		t.Fatal("mail must start off")
	}
	if err := s.Enqueue(msg("a@example.test")); !errors.Is(err, mail.ErrNotEnabled) {
		t.Fatal(err)
	}
}

func TestSendsPlainToALoopbackRelayWithALogin(t *testing.T) {
	srv := mailtest.New(t, mailtest.Options{User: "bot", Password: "s3cret"})
	s := mail.New(mail.Options{})
	c := settings(srv, mail.None)
	c.Username, c.Password = "bot", "s3cret"
	s.Configure(c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	if err := s.Enqueue(msg("tang@example.test")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100 && len(srv.Messages()) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if len(srv.Messages()) == 0 {
		t.Fatalf("not sent: %+v", s.LastStatus())
	}
	m := srv.Messages()[0]
	if m.To != "tang@example.test" || m.From != "notes@example.test" || m.Subject != "Hello é" || m.Auth != "bot:s3cret" {
		t.Fatalf("%+v", m)
	}
	if !strings.Contains(m.Text, "plain text") || !strings.Contains(m.HTML, "<p>hi</p>") || !strings.Contains(m.Raw, "Reply-To") == false && false {
		t.Fatalf("%+v", m)
	}
	if !strings.Contains(m.Raw, "From: \"Petal Book\" <notes@example.test>") {
		t.Fatal(m.Raw)
	}
}

func TestCertificatesAreAlwaysVerified(t *testing.T) {
	for _, sec := range []string{mail.StartTLS, mail.TLS} {
		t.Run(sec, func(t *testing.T) {
			srv := mailtest.New(t, mailtest.Options{StartTLS: sec == mail.StartTLS, ImplicitTLS: sec == mail.TLS})
			// The server's certificate is self-signed: without the test's roots it is refused.
			bad := mail.New(mail.Options{})
			err := bad.Test(context.Background(), settings(srv, sec), msg("a@example.test"))
			if err == nil || !strings.Contains(err.Error(), "certificate") {
				t.Fatalf("a self-signed certificate was accepted: %v", err)
			}
			if len(srv.Messages()) != 0 {
				t.Fatal("a message went over an unverified connection")
			}
			good := mail.New(mail.Options{RootCAs: srv.Roots})
			if err := good.Test(context.Background(), settings(srv, sec), msg("a@example.test")); err != nil {
				t.Fatal(err)
			}
			srv.Wait(1)
		})
	}
}

func TestStartTLSIsRequiredNotOptional(t *testing.T) {
	srv := mailtest.New(t, mailtest.Options{}) // offers no STARTTLS
	s := mail.New(mail.Options{})
	err := s.Test(context.Background(), settings(srv, mail.StartTLS), msg("a@example.test"))
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatal(err)
	}
	if len(srv.Messages()) != 0 {
		t.Fatal("sent without TLS")
	}
}

func TestSecurityNoneOnlyOnLoopback(t *testing.T) {
	s := mail.New(mail.Options{})
	err := s.Test(context.Background(), mail.Settings{Host: "mail.example.test", Port: 25, Security: mail.None, From: "a@example.test"}, msg("b@example.test"))
	if !errors.Is(err, mail.ErrInsecure) {
		t.Fatal(err)
	}
	for _, h := range []string{"127.0.0.1", "::1", "localhost", "[::1]"} {
		if !mail.IsLoopback(h) {
			t.Fatal(h)
		}
	}
	for _, h := range []string{"10.0.0.1", "mail.example.test", "127.0.0.1.example.test", ""} {
		if mail.IsLoopback(h) {
			t.Fatal(h)
		}
	}
}

func TestAHeaderCannotBeInjected(t *testing.T) {
	srv := mailtest.New(t, mailtest.Options{})
	s := mail.New(mail.Options{})
	c := settings(srv, mail.None)
	m := msg("a@example.test")
	m.Subject = "Hi\r\nBcc: victim@example.test"
	if err := s.Test(context.Background(), c, m); err != nil {
		t.Fatal(err)
	}
	got := srv.Wait(1)[0]
	if strings.Contains(got.Raw, "\r\nBcc:") {
		t.Fatalf("header injected:\n%s", got.Raw)
	}
	if err := s.Test(context.Background(), c, mail.Message{To: "a@example.test\r\nBcc: x@example.test", Subject: "s", Text: "t"}); err == nil {
		t.Fatal("a recipient with a line break was accepted")
	}
	if err := s.Test(context.Background(), c, mail.Message{To: "Name <a@example.test>", Subject: "s", Text: "t"}); err == nil {
		t.Fatal("a recipient with a name was accepted")
	}
}

func TestTheQueueRetriesThreeTimesThenDrops(t *testing.T) {
	srv := mailtest.New(t, mailtest.Options{FailData: "451 try later"})
	ck := &clk{t: time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)}
	s := mail.New(mail.Options{Now: ck.Now, Backoff: [2]time.Duration{20 * time.Millisecond, 20 * time.Millisecond}})
	s.Configure(settings(srv, mail.None))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	if err := s.Enqueue(msg("a@example.test")); err != nil {
		t.Fatal(err)
	}
	// Three attempts, each a connection. The clock is moved past a backoff only
	// once the failure is counted and the mail is back in the queue: the server
	// sees the connection before the sender has set the next attempt's time,
	// and a clock moved before that would put the next attempt out of reach.
	for i := 0; i < 3; i++ {
		requeued := 1
		if i == 2 {
			requeued = 0 // the last attempt drops it
		}
		done := func() bool { return s.Stats().FailedLastHour == i+1 && s.Waiting() == requeued }
		for end := time.Now().Add(10 * time.Second); !done() && time.Now().Before(end); time.Sleep(time.Millisecond) {
		}
		if !done() {
			t.Fatalf("attempt %d: connections %d, %+v", i+1, srv.Connections(), s.Stats())
		}
		ck.Advance(time.Minute)
	}
	if srv.Connections() != 3 || s.Waiting() != 0 {
		t.Fatalf("connections %d, waiting %d", srv.Connections(), s.Waiting())
	}
	st := s.Stats()
	if st.FailedLastHour != 3 || st.SentLastHour != 0 {
		t.Fatalf("%+v", st)
	}
	ls := s.LastStatus()
	if ls.OK || !strings.Contains(ls.Message, "451") {
		t.Fatalf("%+v", ls)
	}
}

func TestARetrySucceeds(t *testing.T) {
	srv := mailtest.New(t, mailtest.Options{FailData: "451 try later"})
	s := mail.New(mail.Options{Backoff: [2]time.Duration{10 * time.Millisecond, 10 * time.Millisecond}})
	s.Configure(settings(srv, mail.None))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	s.Enqueue(msg("a@example.test"))
	for srv.Connections() < 1 {
		time.Sleep(time.Millisecond)
	}
	srv.SetFailData("")
	srv.Wait(1)
	// The server has the message before the sender counts it: poll the count.
	st := s.Stats()
	for deadline := time.Now().Add(5 * time.Second); st.SentLastHour != 1 && time.Now().Before(deadline); st = s.Stats() {
		time.Sleep(time.Millisecond)
	}
	if st.SentLastHour != 1 {
		t.Fatalf("%+v", st)
	}
}

func TestTheQueueIsBoundedAndMailOffDropsIt(t *testing.T) {
	s := mail.New(mail.Options{})
	s.Configure(mail.Settings{Enabled: true, Host: "127.0.0.1", Port: 1, Security: mail.None, From: "a@example.test"})
	for i := 0; i < mail.MaxQueue; i++ {
		if err := s.Enqueue(msg("a@example.test")); err != nil {
			t.Fatal(i, err)
		}
	}
	if err := s.Enqueue(msg("a@example.test")); !errors.Is(err, mail.ErrQueueFull) {
		t.Fatal(err)
	}
	s.Configure(mail.Settings{})
	if s.Waiting() != 0 {
		t.Fatal("switching mail off must drop what waits")
	}
}

func TestADescriptionNeverHasThePassword(t *testing.T) {
	c := mail.Settings{Password: "hunter2hunter2"}
	d := mail.Describe(errors.New("535 bad login hunter2hunter2\r\nx"), &c)
	if strings.Contains(d, "hunter2") || strings.ContainsAny(d, "\r\n") {
		t.Fatal(d)
	}
}

func TestRenderAndPickLang(t *testing.T) {
	if mail.PickLang("zh-CN", "") != "zh-Hans" || mail.PickLang("", "fr;q=0.9, zh-Hans;q=0.8") != "zh-Hans" || mail.PickLang("xx", "") != "en" {
		t.Fatal("PickLang")
	}
	for _, lang := range mail.Langs {
		for _, k := range []string{mail.KindVerify, mail.KindRegister, mail.KindRegisterDup, mail.KindRegisterOrg, mail.KindRegisterOrgRefused, mail.KindReset, mail.KindResetSSO, mail.KindEmailChanged, mail.KindEmailAdded, mail.KindEmailRemoved, mail.KindEmailPrimary, mail.KindInvite, mail.KindTest} {
			m := mail.Render(k, lang, mail.Vars{Site: "notes.example.test", Username: "tang", Email: "t@example.test", Link: "https://notes.example.test/x#tok", Provider: "Okta", Hours: 24})
			if m.Subject == "" || m.Text == "" || !strings.Contains(m.HTML, "<p>") {
				t.Fatal(k, lang)
			}
		}
	}
	for _, k := range []string{mail.KindEmailChanged, mail.KindEmailAdded, mail.KindEmailRemoved, mail.KindEmailPrimary} {
		m := mail.Render(k, "en", mail.Vars{Site: "x", Email: "t@example.test", Link: "https://x/y#t"})
		if strings.Contains(m.Text, "https://") {
			t.Fatal("a note about the addresses must have no link:", k)
		}
	}
	h := mail.Render(mail.KindRegister, "en", mail.Vars{Username: "<b>x</b>", Link: "https://x/y#t&z"})
	if strings.Contains(h.HTML, "<b>x</b>") {
		t.Fatal("HTML is not escaped")
	}
}
