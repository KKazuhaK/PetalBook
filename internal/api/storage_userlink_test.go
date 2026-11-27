package api_test

import (
	"context"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
)

// A space that syncs with a person's own GitHub tokens loses access when the
// tokens go, by whoever removes them: the storage requirement then asks for a
// remote, with the grace period from that moment (docs/api.md, "Storage
// management").
func TestSpacesSyncingWithAPersonsTokensLoseAccessWhenTheTokensGo(t *testing.T) {
	t.Parallel()
	for _, by := range []string{"the person", "an administrator"} {
		t.Run(by, func(t *testing.T) {
			t.Parallel()
			eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
				admin := a.setup("root", pw)
				saveGitHub(t, a, admin, nil)
				dee := a.addUser("dee", pw, "member")
				eve := a.addUser("eve", pw, "member")
				a.fake.AddUser("dee-gh")
				a.fake.AddUser("eve-gh")
				ghLink(t, a, dee, "signin", "dee-gh")
				ghLink(t, a, dee, "repos", "dee-gh")
				ghLink(t, a, eve, "repos", "eve-gh")
				userID := func(name string) string {
					var id string
					if err := a.st.Q().QueryRow(context.Background(), `SELECT id FROM users WHERE username = ?`, name).Scan(&id); err != nil {
						t.Fatal(err)
					}
					return id
				}
				// Each personal space follows a repository with its owner's tokens.
				for _, c := range []struct {
					c    *client
					name string
				}{{dee, "dee"}, {eve, "eve"}} {
					if err := store.PutRemote(context.Background(), a.st.Q(), store.Remote{SpaceID: c.c.personalSpace(), URL: "https://github.com/" + c.name + "-gh/notes.git", Branch: "main", State: "ok",
						Updated: a.clock.Now(), CredType: "connection", Provider: "github", Via: "user", RepoID: 1, CredUserID: userID(c.name)}); err != nil {
						t.Fatal(err)
					}
				}
				admin.putSetting("storage.remotes.require", "any").want(t, 200, "")
				admin.putSetting("storage.remotes.scope", "all").want(t, 200, "")
				admin.putSetting("storage.remotes.graceDays", 5).want(t, 200, "")
				a.clock.Advance(6 * 24 * time.Hour) // far past what the requirement gave a space
				note := func(c *client) map[string]any {
					for _, s := range c.do("GET", "/api/spaces", nil).want(t, 200, "").body["spaces"].([]any) {
						if s.(map[string]any)["personal"] == true {
							if st, ok := s.(map[string]any)["storage"].(map[string]any); ok {
								if n, ok := st["remote"].(map[string]any); ok {
									return n
								}
							}
						}
					}
					return nil
				}
				if note(dee) != nil || note(eve) != nil {
					t.Fatalf("with their tokens: %v %v", note(dee), note(eve))
				}
				lostAt := a.clock.Now()
				id := linkAt(t, dee, "github")["id"].(string)
				if by == "the person" {
					dee.do("DELETE", "/api/account/links/"+id+"/repos", nil).want(t, 204, "")
				} else {
					admin.do("DELETE", "/api/users/"+userID("dee")+"/links/"+id, nil).want(t, 204, "")
				}
				// In grace from the moment of the loss.
				n := note(dee)
				if n == nil || n["overdue"] != false || n["deadline"] != lostAt.Add(5*24*time.Hour).UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano) {
					t.Fatalf("just after the loss: %v (lost at %s)", n, lostAt.UTC().Format(time.RFC3339Nano))
				}
				if note(eve) != nil {
					t.Fatalf("another person's space: %v", note(eve))
				}
				if g := admin.do("GET", "/api/admin/storage", nil).want(t, 200, ""); g.body["affected"].(map[string]any)["inGrace"] != float64(1) {
					t.Fatal(g.raw)
				}
				// After the grace it is overdue; the other person's space never is.
				a.clock.Advance(5*24*time.Hour + time.Second)
				if n := note(dee); n == nil || n["overdue"] != true {
					t.Fatalf("after the grace: %v", n)
				}
				if note(eve) != nil {
					t.Fatalf("another person's space after the grace: %v", note(eve))
				}
				if l := admin.do("GET", "/api/admin/spaces?overdue=1", nil).want(t, 200, ""); len(l.body["items"].([]any)) != 2 { // the administrator's own space has no remote at all
					t.Fatal(l.raw)
				}
				if g := admin.do("GET", "/api/admin/storage", nil).want(t, 200, ""); g.body["affected"].(map[string]any)["overdue"] != float64(2) {
					t.Fatal(g.raw)
				}
			})
		})
	}
}
