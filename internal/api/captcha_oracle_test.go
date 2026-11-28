package api_test

import (
	"fmt"
	"testing"
)

// The captcha that asks after failures counts them by what was typed, in every case, so an email that is
// an account's and one that is nobody's are asked after the same number of failures.
func TestTheCaptchaCountsFailuresByWhatWasTypedForEmailsToo(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		verifiedUser(t, a, "tang", "tang@example.test")
		admin.putSetting("captcha.contexts", map[string]any{"login": map[string]any{"on": true, "when": "after_failures", "failures": 3}}).want(t, 200, "")
		asked := func(who string) bool {
			c := a.newClient("203.0.113.99")
			return c.do("GET", "/api/captcha?ctx=login&account="+who, nil).want(t, 200, "").body["required"] == true
		}
		fail := func(who string, n int) {
			for i := range n {
				c := a.newClient(fmt.Sprintf("203.0.113.%d", 100+i))
				c.do("POST", "/api/login", bad(who)).want(t, 401, "")
			}
		}
		for _, who := range []string{"tang@example.test", "ghost@example.test", "Tang@Example.test"} {
			if asked(who) {
				t.Fatalf("%s: asked with no failures", who)
			}
		}
		fail("tang@example.test", 3)
		fail("ghost@example.test", 3)
		// The same answer for the account's address and for nobody's.
		if !asked("tang@example.test") || !asked("ghost@example.test") {
			t.Fatalf("after three failures: account %v, nobody %v", asked("tang@example.test"), asked("ghost@example.test"))
		}
		// Typed in another case it is the same name; another address is another.
		if !asked("TANG@example.test") || asked("other@example.test") {
			t.Fatal("the count is by the address as typed, folded")
		}
	})
}
