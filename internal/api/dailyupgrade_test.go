package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/api"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// What 2.3 saved must still work after the upgrade, with nothing for the
// administrators to repair.

func noticeCodes(r map[string]any) string {
	var out []string
	for _, n := range asSlice(r["notices"]) {
		m := asMap(n)
		str := func(k string) string { v, _ := m[k].(string); return v }
		out = append(out, str("code")+":"+str("id")+str("lang")+">"+str("to"))
	}
	return strings.Join(out, " ")
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

func apiByID(r map[string]any, id string) map[string]any {
	for _, a := range asSlice(r["apis"]) {
		if asMap(a)["id"] == id {
			return asMap(a)
		}
	}
	return nil
}

// In 2.3 "use preset" saved an interface of the administrators' own with the
// id of what is now a built-in one. It becomes the built-in one when it is the
// same, and stays an interface of their own, renamed, when they changed it.
func TestDailyInterfacesSavedFromPresetsAfterTheUpgrade(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		ctx := context.Background()
		st := x.a.st
		url := x.qs.URL + "/q?lang={lang}&d={date}"
		enc, err := x.a.mgr.Secrets().Seal("site", api.HeaderPurpose("zenquotes", "X-Api-Key", url), []byte("the-key-value"))
		if err != nil {
			t.Fatal(err)
		}
		hitokoto := `{"id":"hitokoto","name":"一言","lang":"zh","url":"https://international.v1.hitokoto.cn/?max_length=80","text":"hitokoto","source":"from","author":"from_who","link":"https://hitokoto.cn?uuid={uuid}","refresh":"minutes","refreshMinutes":30,"skipNew":false,"rps":50,"enabled":true,"headers":[]}`
		zenquotes := `{"id":"zenquotes","name":"ZenQuotes","lang":"en","url":"` + url + `","text":"data.0.q","source":"","author":"data.0.a","link":"","refresh":"daily","skipNew":true,"rps":100,"enabled":true,"headers":[{"name":"X-Api-Key","enc":"` + enc + `"}]}`
		old := `{"default":{"kind":"api","apis":{"":"zenquotes","zh-Hans":"hitokoto"}},"languages":["zh","en"],"apis":[` + hitokoto + `,` + zenquotes + `],"allow":{"own":true,"api":true}}`
		if err := store.SetSiteSetting(ctx, st.Q(), "daily", old, x.now); err != nil {
			t.Fatal(err)
		}
		if err := store.SetSpaceDaily(ctx, st.Q(), x.tm.sid, store.SpaceDaily{Inherit: true, APIs: `["hitokoto","zenquotes"]`}); err != nil {
			t.Fatal(err)
		}
		check := func() {
			t.Helper()
			code, r := x.adm("GET", "/api/admin/daily", nil)
			if code != 200 {
				t.Fatalf("%d %v", code, r)
			}
			h, q := apiByID(r, "hitokoto"), apiByID(r, "zenquotes-custom")
			if len(asSlice(r["apis"])) != 2 || h == nil || h["builtin"] != true || h["refresh"] != "minutes" || h["refreshMinutes"] != float64(30) || h["rps"] != float64(20) || h["skipNew"] != false || h["enabled"] != true {
				t.Fatalf("hitokoto: %v", r["apis"])
			}
			if q == nil || q["builtin"] == true || q["url"] != url || fmt.Sprint(q["headers"]) != "[map[name:X-Api-Key set:true]]" || apiByID(r, "zenquotes") != nil {
				t.Fatalf("zenquotes: %v", r["apis"])
			}
			if ps := asSlice(r["providers"]); asMap(ps[0])["enabled"] != true || asMap(ps[1])["enabled"] != false {
				t.Fatalf("%v", ps)
			}
			if fmt.Sprint(asMap(r["default"])["apis"]) != "map[:[zenquotes-custom] zh-Hans:[hitokoto]]" {
				t.Fatalf("%v", r["default"])
			}
			if got := noticeCodes(r); got != "builtin_renamed:zenquotes>zenquotes-custom" {
				t.Fatalf("%q", got)
			}
			_, p := x.own("GET", x.base+"/daily/prefs", nil)
			if fmt.Sprint(p["apis"]) != "[hitokoto zenquotes-custom]" {
				t.Fatalf("%v", p)
			}
			// A built-in one is offered with each of its languages.
			for _, a := range asSlice(p["available"]) {
				if m := asMap(a); m["id"] == "hitokoto" && fmt.Sprint(m["languages"]) != "[zh-Hans zh-Hant]" || m["id"] == "zenquotes-custom" && m["languages"] != nil {
					t.Fatalf("%v", p["available"])
				}
			}
		}
		check()
		raw, _, _ := store.GetSiteSetting(ctx, st.Q(), "daily")
		var saved struct {
			APIs []struct {
				ID string `json:"id"`
			} `json:"apis"`
			Providers map[string]struct {
				Enabled bool `json:"enabled"`
			} `json:"providers"`
		}
		json.Unmarshal([]byte(raw), &saved)
		if len(saved.APIs) != 1 || saved.APIs[0].ID != "zenquotes-custom" || !saved.Providers["hitokoto"].Enabled {
			t.Fatalf("not saved: %s", raw)
		}
		// Once: reading again changes nothing.
		check()
		if again, _, _ := store.GetSiteSetting(ctx, st.Q(), "daily"); again != raw {
			t.Fatalf("saved again:\n%s\n%s", raw, again)
		}
		// The renamed one still sends its key.
		x.qs.key.Store("")
		if r := quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en"); r["origin"] != "external" || x.qs.key.Load() != "the-key-value" {
			t.Fatalf("%v (key %v)", r, x.qs.key.Load())
		}
		// The administrators can delete their own, and switch the built-in one on.
		if code, r := x.adm("DELETE", "/api/admin/daily/apis/zenquotes-custom", nil); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		if code, r := x.adm("PUT", "/api/admin/daily/providers/zenquotes", map[string]any{"enabled": true}); code != 200 || apiByID(r, "zenquotes")["builtin"] != true {
			t.Fatalf("%d %v", code, r)
		}
		// The notices go when they are dismissed.
		if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"notices": []any{}}); code != 200 || len(asSlice(r["notices"])) != 0 {
			t.Fatalf("%d %v", code, r)
		}
	})
}

// A stored interface with a built-in's id whose header values cannot be moved
// to a new id stays under its id, as the administrators' own: it can be
// changed and deleted, and the built-in one switched on after that.
func TestDailyPresetWhoseHeadersCannotMoveStays(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		ctx := context.Background()
		url := x.qs.URL + "/q?lang={lang}"
		old := `{"apis":[{"id":"zenquotes","name":"ZenQuotes","lang":"en","url":"` + url + `","text":"data.0.q","refresh":"daily","rps":3,"enabled":true,"headers":[{"name":"X-Api-Key","enc":"not-sealed"}]}]}`
		if err := store.SetSiteSetting(ctx, x.a.st.Q(), "daily", old, x.now); err != nil {
			t.Fatal(err)
		}
		code, r := x.adm("GET", "/api/admin/daily", nil)
		if q := apiByID(r, "zenquotes"); code != 200 || q == nil || q["builtin"] == true || noticeCodes(r) != "builtin_kept:zenquotes>" {
			t.Fatalf("%d %v", code, r)
		}
		if code, r := x.adm("PUT", "/api/admin/daily/providers/zenquotes", map[string]any{"enabled": true}); code != 400 {
			t.Fatalf("%d %v", code, r)
		}
		b := map[string]any{"id": "zenquotes", "name": "Mine", "lang": "en", "url": url, "text": "data.0.q", "headers": []any{map[string]any{"name": "X-Api-Key", "value": "new"}}}
		if code, r := x.putAPI("zenquotes", b); code != 200 || apiByID(r, "zenquotes")["name"] != "Mine" || apiByID(r, "zenquotes")["builtin"] == true {
			t.Fatalf("%d %v", code, r)
		}
		if _, r := x.adm("GET", "/api/admin/daily", nil); apiByID(r, "zenquotes")["name"] != "Mine" {
			t.Fatalf("%v", r["apis"])
		}
		if code, r := x.adm("DELETE", "/api/admin/daily/apis/zenquotes", nil); code != 200 || apiByID(r, "zenquotes") != nil {
			t.Fatalf("%d %v", code, r)
		}
		if code, r := x.adm("PUT", "/api/admin/daily/providers/zenquotes", map[string]any{"enabled": true}); code != 200 || apiByID(r, "zenquotes")["builtin"] != true {
			t.Fatalf("%d %v", code, r)
		}
	})
}

// A default saved by 2.3 with a code that is not a language (`cn`) loses that
// code only: the interfaces and their keys, and the rest of the default, stay,
// and the administrators are told.
func TestDailyStoredSettingsAreReadLeniently(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		ctx := context.Background()
		st := x.a.st
		if code, r := x.putAPI("keyed", map[string]any{"id": "keyed", "name": "K", "url": x.qs.URL + "/q", "text": "data.0.q", "headers": []any{map[string]any{"name": "X-Api-Key", "value": "secret-value"}}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		raw, _, _ := store.GetSiteSetting(ctx, st.Q(), "daily")
		var d map[string]json.RawMessage
		json.Unmarshal([]byte(raw), &d)
		d["default"] = json.RawMessage(`{"kind":"space","space":"` + x.tm.sid + `","source":{"cn":{"folder":"x"},"zh":{"tag":"诗"},"en":{"tag":"q"}}}`)
		b, _ := json.Marshal(d)
		if err := store.SetSiteSetting(ctx, st.Q(), "daily", string(b), x.now); err != nil {
			t.Fatal(err)
		}
		code, r := x.adm("GET", "/api/admin/daily", nil)
		src := asMap(asMap(r["default"])["source"])
		if code != 200 || asMap(r["default"])["kind"] != "space" || src["cn"] != nil || asMap(src["zh-Hans"])["tag"] != "诗" || asMap(src["en"])["tag"] != "q" {
			t.Fatalf("%d %v", code, r)
		}
		if k := apiByID(r, "keyed"); k == nil || fmt.Sprint(k["headers"]) != "[map[name:X-Api-Key set:true]]" {
			t.Fatalf("%v", r["apis"])
		}
		if got := noticeCodes(r); got != "dropped_language:cn>" {
			t.Fatalf("%q", got)
		}
		// The next save keeps them.
		if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"allow": map[string]any{"api": false}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		raw, _, _ = store.GetSiteSetting(ctx, st.Q(), "daily")
		if !strings.Contains(raw, `"id":"keyed"`) || !strings.Contains(raw, `"enc":"`) || !strings.Contains(raw, `"zh-Hans":{`) || strings.Contains(raw, `"cn":{`) {
			t.Fatalf("%s", raw)
		}
	})
}

// Lines saved under a code that is not a language can be replaced and deleted
// by that code as it is stored.
func TestDailyLinesUnderAnOldCode(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		ctx := context.Background()
		st := x.a.st
		if _, err := store.ReplaceDailyLines(ctx, st.Q(), "cn", []store.DailyLine{{Lang: "cn", Body: "旧的一行"}}); err != nil {
			t.Fatal(err)
		}
		if code, r := x.adm("PUT", "/api/admin/daily/lines", map[string]any{"lang": "cn", "text": "新的\n另一行"}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		if counts, _ := store.DailyLineCounts(ctx, st.Q()); counts["cn"] != 2 || len(counts) != 1 {
			t.Fatalf("%v", counts)
		}
		if code, r := x.adm("DELETE", "/api/admin/daily/lines?lang=cn", nil); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		if counts, _ := store.DailyLineCounts(ctx, st.Q()); len(counts) != 0 {
			t.Fatalf("%v", counts)
		}
	})
}

// A stored choice of a service that is no longer built in (quotable, whose
// domain no longer resolves) never breaks the settings or the reader's page,
// and no other service is picked in its place.
func TestDailyRemovedBuiltInIsNotReplaced(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		ctx := context.Background()
		old := `{"default":{"kind":"api","apis":{"":"quotable"}},"providers":{"quotable":{"enabled":true}},"allow":{"own":true,"api":true}}`
		if err := store.SetSiteSetting(ctx, x.a.st.Q(), "daily", old, x.now); err != nil {
			t.Fatal(err)
		}
		if err := store.SetSpaceDaily(ctx, x.a.st.Q(), x.tm.sid, store.SpaceDaily{Inherit: true, APIs: `["quotable"]`}); err != nil {
			t.Fatal(err)
		}
		code, r := x.adm("GET", "/api/admin/daily", nil)
		if code != 200 || len(asSlice(r["apis"])) != 0 || len(asSlice(r["providers"])) != 8 {
			t.Fatalf("%d %v", code, r)
		}
		if code, _ := x.own("GET", x.base+"/daily/prefs", nil); code != 200 {
			t.Fatalf("prefs: %d", code)
		}
		if code, _ := x.viewer("GET", x.base+"/daily?date=2026-11-23&lang=en", nil); code != 200 {
			t.Fatalf("daily: %d", code)
		}
		if r := quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en"); r["quote"] != nil {
			t.Fatalf("another service was picked: %v", r)
		}
	})
}
