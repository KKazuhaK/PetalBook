package auth

import (
	"testing"
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
