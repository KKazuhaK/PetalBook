package auth

import (
	"testing"

	"petalbook.app/server/internal/mail"
)

// A tenant's sender is dialled with a check on the address each name resolves
// to, whatever the name says: public addresses only. The tests' open switch is
// the only way round it.
func TestATenantsSenderIsDialledToPublicAddressesOnly(t *testing.T) {
	t.Parallel()
	s := &Service{}
	cfg := s.tenantSettings(tenantMailStored{Enabled: true, Host: "smtp.example.com", Port: 587}, "")
	if cfg.Control == nil {
		t.Fatal("a tenant's sender has no check on what it dials")
	}
	for _, a := range []string{"127.0.0.1:587", "[::1]:587", "10.1.2.3:25", "192.168.0.9:465", "169.254.169.254:25", "100.64.0.1:587", "[::ffff:127.0.0.1]:587"} {
		if err := cfg.Control("tcp", a, nil); err == nil {
			t.Errorf("%s was dialled", a)
		}
	}
	for _, a := range []string{"8.8.8.8:587", "[2606:4700:4700::1111]:465"} {
		if err := cfg.Control("tcp", a, nil); err != nil {
			t.Errorf("%s was refused: %v", a, err)
		}
	}
	s.tenantMailOpen.Store(true)
	if s.tenantSettings(tenantMailStored{Enabled: true}, "").Control != nil {
		t.Fatal("the test switch does not open it")
	}
}

func TestTheNamesOfThisMachineAreNotPublicMailServers(t *testing.T) {
	t.Parallel()
	for h, want := range map[string]bool{"smtp.example.com": true, "8.8.8.8": true, "localhost": false, "LOCALHOST": false, "a.localhost": false,
		"printer.local": false, "db.internal": false, "127.0.0.1": false, "[::1]": false, "169.254.169.254": false, "10.0.0.1": false} {
		if got := validPublicHostName(h); got != want {
			t.Errorf("%s: %v", h, got)
		}
	}
}

// Every kind of message is classified, and what proves an address or lets
// someone in goes by the site's sender only.
func TestEveryKindOfMailIsClassifiedAndSecurityMailIsTheSites(t *testing.T) {
	t.Parallel()
	for _, k := range mail.Kinds {
		if _, ok := mailKinds[k]; !ok {
			t.Errorf("the kind %q is not in mailKinds", k)
		}
	}
	if len(mailKinds) != len(mail.Kinds) {
		t.Errorf("mailKinds has %d kinds, mail.Kinds %d", len(mailKinds), len(mail.Kinds))
	}
	for _, k := range []string{mail.KindVerify, mail.KindEmailClaim, mail.KindRegister, mail.KindRegisterDup, mail.KindRegisterOrg, mail.KindRegisterJoin,
		mail.KindReset, mail.KindResetSSO, mail.KindWelcome, mail.KindJoinApproved} {
		if !securityMail(k) {
			t.Errorf("%s is not security mail", k)
		}
	}
	for _, k := range []string{mail.KindInvite, mail.KindEmailAdded, mail.KindEmailChanged} {
		if securityMail(k) {
			t.Errorf("%s is security mail", k)
		}
	}
	if !securityMail("a-kind-nobody-classified") {
		t.Error("an unclassified kind is not security mail")
	}
}
