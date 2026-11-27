package api_test

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	"petalbook.app/server/internal/githubapp/githubapptest"
)

// deliverGitHub posts a webhook event as GitHub does: no session, no browser.
func deliverGitHub(t *testing.T, a *acc, e githubapptest.Event) result {
	t.Helper()
	c := a.newClient("")
	return c.do("POST", "/api/github/webhook", string(e.Body), "X-GitHub-Event", e.Name, "X-Hub-Signature-256", e.Signature)
}

func installerRows(t *testing.T, a *acc) int {
	t.Helper()
	return rowCount(t, a, `SELECT COUNT(*) FROM github_installers`)
}

// GitHub's webhook takes no session: a delivery is believed only with the
// App's signature, and records nothing otherwise; the site's switch turns
// receiving off.
func TestTheWebhookRouteRefusesWhatGitHubDidNotSign(t *testing.T) {
	t.Parallel()
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		f := ghTenFx(t, a)
		id := a.fake.InstallationOn("acme-gh")
		e := a.fake.InstallationEvent("created", id, "mem-b-gh")
		deliverGitHub(t, a, e.Forged()).want(t, 401, "unauthenticated")
		deliverGitHub(t, a, e.Unsigned()).want(t, 401, "unauthenticated")
		if n := installerRows(t, a); n != 0 {
			t.Fatalf("%d rows", n)
		}
		big := githubapptest.Event{Name: "installation", Body: []byte(`{"x":"` + strings.Repeat("a", 3<<20) + `"}`), Signature: e.Signature}
		deliverGitHub(t, a, big).want(t, 413, "too_large")
		f.root.putSetting("features.githubConnect", false).want(t, 200, "")
		deliverGitHub(t, a, e).want(t, 403, "feature_off")
		f.root.putSetting("features.githubConnect", true).want(t, 200, "")
		deliverGitHub(t, a, e).want(t, 204, "")
		deliverGitHub(t, a, e).want(t, 204, "") // redelivered
		if n := installerRows(t, a); n != 1 {
			t.Fatalf("%d rows", n)
		}
		// Other events are taken and ignored.
		ping := githubapptest.Event{Name: "ping", Body: []byte(`{"zen":"x"}`)}
		ping.Signature = a.fake.InstallationEvent("created", id, "mem-b-gh").Signature
		deliverGitHub(t, a, ping).want(t, 401, "unauthenticated")
	})
}

// Through the API: a member of the GitHub organisation who did not install the
// App there is refused not_installer; the installer's claim made before
// GitHub's event shows as pending in the console until the event completes it;
// the uninstall disconnects it, audited.
func TestTheOrgConsoleClaimsThroughGitHubsEvent(t *testing.T) {
	t.Parallel()
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		f := ghTenFx(t, a)
		a.fake.AddUser("adm-b-gh")
		a.fake.AddMember("acme-gh", "adm-b-gh")
		a.fake.AddOrg("beta-gh", "adm-b-gh", "mem-b-gh")
		// mem-b-gh installs it on beta-gh; adm-b-gh, a member there, tries to claim it.
		inst := a.fake.InstallationOn("acme-gh")
		deliverGitHub(t, a, a.fake.InstallationEvent("created", inst, "mem-b-gh")).want(t, 204, "")
		r := f.admB.do("POST", "/api/admin/github/install", map[string]any{"account": "acme-gh"}).want(t, 200, "")
		back, _ := url.Parse(a.fake.InstallAppOn(r.body["url"].(string), "adm-b-gh", "acme-gh", "https://notes.example.test/api/account/links/github/callback", true))
		cb := f.admB.do("GET", back.Path+"?"+back.RawQuery, nil, "Sec-Fetch-Site", "cross-site")
		if to := cb.resp.Header.Get("Location"); to != "/admin/storage/github?connection_error=not_installer" {
			t.Fatalf("%q", to)
		}
		// beta-gh, installed by adm-b-gh now: the browser is back before GitHub's event.
		r = f.admB.do("POST", "/api/admin/github/install", map[string]any{"account": "beta-gh"}).want(t, 200, "")
		back, _ = url.Parse(a.fake.InstallAppOn(r.body["url"].(string), "adm-b-gh", "beta-gh", "https://notes.example.test/api/account/links/github/callback", true))
		cb = f.admB.do("GET", back.Path+"?"+back.RawQuery, nil, "Sec-Fetch-Site", "cross-site")
		if to := cb.resp.Header.Get("Location"); to != "/admin/storage/github?connection_pending=beta-gh" {
			t.Fatalf("%q", to)
		}
		list := f.admB.do("GET", "/api/admin/github/installations", nil).want(t, 200, "")
		pend := list.body["pending"].([]any)
		if len(items(t, list)) != 0 || len(pend) != 1 || pend[0].(map[string]any)["account"] != "beta-gh" || pend[0].(map[string]any)["error"] != "" {
			t.Fatalf("%s", list.raw)
		}
		for _, e := range a.fake.Events() {
			deliverGitHub(t, a, e).want(t, 204, "")
		}
		list = f.admB.do("GET", "/api/admin/github/installations", nil).want(t, 200, "")
		got := items(t, list)
		if len(got) != 1 || got[0]["account"] != "beta-gh" || len(list.body["pending"].([]any)) != 0 {
			t.Fatalf("%s", list.raw)
		}
		audit := f.root.do("GET", "/api/admin/audit?kind=github_installation_connected", nil).want(t, 200, "")
		if !strings.Contains(audit.raw, "beta-gh") || !strings.Contains(audit.raw, "webhook") {
			t.Fatalf("%s", audit.raw)
		}
		// Uninstalled on GitHub: gone here too.
		beta := int64(got[0]["id"].(float64))
		a.fake.Uninstall(beta, "adm-b-gh")
		for _, e := range a.fake.Events() {
			deliverGitHub(t, a, e).want(t, 204, "")
		}
		if l := items(t, f.admB.do("GET", "/api/admin/github/installations", nil).want(t, 200, "")); len(l) != 0 {
			t.Fatalf("%v", l)
		}
		audit = f.root.do("GET", "/api/admin/audit?kind=github_installation_disconnected", nil).want(t, 200, "")
		if !strings.Contains(audit.raw, strconv.FormatInt(beta, 10)) {
			t.Fatalf("%s", audit.raw)
		}
	})
}
