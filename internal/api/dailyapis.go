package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/dailyfetch"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// The external interfaces of the daily text, as the administrators keep them
// (see dailyfetch for how they are asked).

type storedHeader struct {
	Name string `json:"name"`
	// Enc is the value, encrypted with the server's key and bound to the
	// interface and the header: it is never given back.
	Enc string `json:"enc"`
}

type storedAPI struct {
	dailyfetch.API
	Headers []storedHeader `json:"headers"`
}

type siteDaily struct {
	Default *siteDefault `json:"default"`
	Hosts   []string     `json:"hosts"`
	// Languages are the languages the settings pages have a tab for, besides
	// "all languages"; the administrators' own list.
	Languages []string    `json:"languages"`
	APIs      []storedAPI `json:"apis"`
	// Allow is what the administrators let the spaces do; absent is all of it.
	Allow *dailyAllow `json:"allow"`
}

// dailyAllow is what the administrators let the spaces do. What a space wrote
// or chose while it was not allowed is kept, and has no effect until it is.
type dailyAllow struct {
	// Own is whether a space may use a source of its own (daily in space.yml).
	Own bool `json:"own"`
	// API is whether a space may choose an external interface.
	API bool `json:"api"`
}

var defaultLanguages = []string{"zh", "en"}

func (d *siteDaily) fill() {
	if d.Hosts == nil {
		d.Hosts = []string{}
	}
	if d.APIs == nil {
		d.APIs = []storedAPI{}
	}
	if d.Languages == nil {
		d.Languages = append([]string(nil), defaultLanguages...)
	}
	if d.Allow == nil {
		d.Allow = &dailyAllow{Own: true, API: true}
	}
}

func (s *server) loadSite(ctx context.Context) (siteDaily, error) {
	var d siteDaily
	raw, ok, err := store.GetSiteSetting(ctx, s.opt.Store.Q(), siteDailyKey)
	if err != nil {
		return d, err
	}
	if ok {
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			s.log.Error("the daily text settings of the server cannot be read; they are taken as not set", "error", err)
			d = siteDaily{}
		}
	}
	d.fill()
	return d, nil
}

func (s *server) saveSite(ctx context.Context, d siteDaily) error {
	d.fill()
	b, _ := json.Marshal(d)
	return store.SetSiteSetting(ctx, s.opt.Store.Q(), siteDailyKey, string(b), time.Now())
}

// apiView is an interface as the administrators see it: the headers only by
// name, with whether a value is kept.
type apiView struct {
	dailyfetch.API
	Headers []headerView `json:"headers"`
}

type headerView struct {
	Name string `json:"name"`
	Set  bool   `json:"set"`
}

func viewOfAPI(a storedAPI) apiView {
	v := apiView{API: a.API, Headers: []headerView{}}
	for _, h := range a.Headers {
		v.Headers = append(v.Headers, headerView{h.Name, h.Enc != ""})
	}
	return v
}

func headerPurpose(id, name string) string { return "daily-header/" + id + "/" + strings.ToLower(name) }

// runtime is the interface with its header values in the clear, to be asked.
func (s *server) runtime(a storedAPI) (dailyfetch.API, error) {
	out := a.API
	out.Headers = nil
	for _, h := range a.Headers {
		box := s.spaces.Secrets()
		if box == nil {
			return out, errors.New("this server has no key for stored secrets")
		}
		v, err := box.Open("site", headerPurpose(a.ID, h.Name), h.Enc)
		if err != nil {
			return out, err
		}
		out.Headers = append(out.Headers, dailyfetch.Header{Name: h.Name, Value: string(v)})
	}
	return out, nil
}

// NewDailyBacking keeps what the interfaces said in the state database.
func NewDailyBacking(st *store.Store) dailyfetch.Backing { return dailyBacking{st} }

type dailyBacking struct{ st *store.Store }

func (b dailyBacking) result(c store.DailyCached) (dailyfetch.Result, bool) {
	var q dailyfetch.Quote
	if json.Unmarshal([]byte(c.Body), &q) != nil || q.Text == "" {
		return dailyfetch.Result{}, false
	}
	return dailyfetch.Result{Quote: q, Fetched: c.Fetched}, true
}

func (b dailyBacking) Get(ctx context.Context, apiKey, lang, period string, skip int) (dailyfetch.Result, bool, error) {
	c, ok, err := store.GetDailyCache(ctx, b.st.Q(), apiKey, lang, period, skip)
	if err != nil || !ok {
		return dailyfetch.Result{}, false, err
	}
	r, ok := b.result(c)
	return r, ok, nil
}

func (b dailyBacking) Put(ctx context.Context, apiKey, lang, period string, skip int, r dailyfetch.Result) error {
	body, _ := json.Marshal(r.Quote)
	return store.PutDailyCache(ctx, b.st.Q(), apiKey, lang, period, skip, store.DailyCached{Body: string(body), Fetched: r.Fetched})
}

func (b dailyBacking) Latest(ctx context.Context, apiKey, lang string) (dailyfetch.Result, bool, error) {
	c, ok, err := store.LatestDailyCache(ctx, b.st.Q(), apiKey, lang)
	if err != nil || !ok {
		return dailyfetch.Result{}, false, err
	}
	r, ok := b.result(c)
	return r, ok, nil
}

func (b dailyBacking) Prune(ctx context.Context, before time.Time) error {
	return store.PruneDailyCache(ctx, b.st.Q(), before)
}

// siteDaily is what the administrators' settings give for a space with no
// source of its own: the external interface the space chose, else the default
// when the space takes it. A nil quote with a reason is a block that does not
// show; with none, there is nothing set.
func (s *server) siteDaily(c *call, site siteDaily, date string, skip int, lang string) (*dailyQuote, string, error) {
	ctx := c.r.Context()
	prefs, err := store.GetSpaceDaily(ctx, s.opt.Store.Q(), c.sp.Info.ID)
	if err != nil {
		return nil, "", err
	}
	var ids []string
	if site.Allow.API { // when it is not allowed, the choice is kept and not used
		json.Unmarshal([]byte(prefs.APIs), &ids)
	}
	if a := chooseAPI(site.APIs, ids, lang); a != nil {
		rt, err := s.runtime(*a)
		if err != nil {
			s.log.Error("an external daily text interface cannot be set up", "interface", a.ID, "error", err)
			return nil, "unavailable", nil
		}
		r, err := s.opt.DailyFetch.Get(ctx, rt, site.Hosts, lang, date, skip)
		switch {
		case errors.Is(err, dailyfetch.ErrRateLimited):
			return nil, "", err
		case err != nil:
			return nil, "unavailable", nil
		}
		q := &dailyQuote{Text: r.Text, Source: r.Source, Author: r.Author, Link: r.Link, Fetched: rfc3339(r.Fetched), Stale: r.Stale, origin: "external"}
		if r.Stale {
			q.expires = s.opt.DailyFetch.Now().Add(time.Minute)
		} else if _, ends := rt.Period(s.opt.DailyFetch.Now(), date); !ends.IsZero() {
			q.expires = ends
		}
		return q, "", nil
	}
	if !prefs.Inherit || site.Default == nil {
		return nil, "", nil
	}
	switch site.Default.Kind {
	case "list":
		return s.dailyFromLines(ctx, date, skip, lang)
	case "space":
		return s.dailyFromSpace(ctx, *site.Default, date, skip, lang)
	}
	return nil, "", nil
}

// chooseAPI picks, among the enabled interfaces a space chose, the one for the
// language: made for it, else one that takes every language, else any.
func chooseAPI(all []storedAPI, ids []string, lang string) *storedAPI {
	chosen := map[string]bool{}
	for _, id := range ids {
		chosen[id] = true
	}
	var mine []storedAPI
	for _, a := range all {
		if chosen[a.ID] && a.Enabled {
			mine = append(mine, a)
		}
	}
	if len(mine) == 0 {
		return nil
	}
	base := lang
	if i := strings.IndexByte(lang, '-'); i > 0 {
		base = lang[:i]
	}
	for _, want := range []func(a storedAPI) bool{
		func(a storedAPI) bool { return a.Lang != "" && a.Lang == lang },
		func(a storedAPI) bool { return a.Lang != "" && a.Lang == base },
		func(a storedAPI) bool { return a.Lang == "" },
		func(storedAPI) bool { return true },
	} {
		for i := range mine {
			if want(mine[i]) {
				return &mine[i]
			}
		}
	}
	return nil
}

// --- the administrators' side -------------------------------------------------------

// apiBody is an interface in a request. A header with no value keeps the value
// it has; the headers that are left out are removed.
type apiBody struct {
	dailyfetch.API
	Headers []struct {
		Name  string  `json:"name"`
		Value *string `json:"value"`
	} `json:"headers"`
	Enabled *bool `json:"enabled"`
}

// build checks an interface from a request and makes the stored form, keeping
// the header values already stored for names that come without one. old is the
// stored interface with the same id, if any.
func (s *server) build(b apiBody, old *storedAPI) (storedAPI, dailyfetch.API, string, error) {
	a := b.API
	a.Enabled = true
	if old != nil {
		a.Enabled = old.Enabled
	}
	if b.Enabled != nil {
		a.Enabled = *b.Enabled
	}
	// Headers in the clear, for checking and for asking.
	var clear []dailyfetch.Header
	var stored []storedHeader
	for _, h := range b.Headers {
		var enc string
		if h.Value == nil {
			if old != nil {
				for _, oh := range old.Headers {
					if strings.EqualFold(oh.Name, h.Name) {
						enc = oh.Enc
					}
				}
			}
			if enc == "" {
				return storedAPI{}, a, "", &space.InvalidError{Msg: "the header " + h.Name + " has no value yet; give one"}
			}
			box := s.spaces.Secrets()
			if box == nil {
				return storedAPI{}, a, "", errors.New("this server has no key for stored secrets")
			}
			v, err := box.Open("site", headerPurpose(old.ID, h.Name), enc)
			if err != nil {
				return storedAPI{}, a, "", err
			}
			clear = append(clear, dailyfetch.Header{Name: h.Name, Value: string(v)})
		} else {
			clear = append(clear, dailyfetch.Header{Name: h.Name, Value: *h.Value})
		}
		stored = append(stored, storedHeader{Name: h.Name, Enc: enc})
	}
	a.Headers = clear
	checked, host, err := dailyfetch.CheckAPI(a, s.opt.DailyFetch.AllowHTTP())
	if err != nil {
		return storedAPI{}, a, "", &space.InvalidError{Msg: err.Error()}
	}
	// Values given now are sealed under this interface's id.
	for i, h := range b.Headers {
		if h.Value == nil {
			continue
		}
		box := s.spaces.Secrets()
		if box == nil {
			return storedAPI{}, a, "", &space.InvalidError{Msg: "this server has no key for storing secrets, so it cannot keep a header value"}
		}
		enc, err := box.Seal("site", headerPurpose(checked.ID, h.Name), []byte(*h.Value))
		if err != nil {
			return storedAPI{}, a, "", err
		}
		stored[i].Enc = enc
	}
	checked.Headers = nil
	return storedAPI{API: checked, Headers: stored}, a, host, nil
}

// adminPutDaily changes the default, the hosts and the languages. What the
// request leaves out stays as it is.
func (s *server) adminPutDaily(c *call) {
	var req map[string]json.RawMessage
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	for k := range req {
		if k != "default" && k != "hosts" && k != "languages" && k != "allow" {
			bad(c, "unknown key "+k+": the settings are default, hosts, languages and allow (interfaces have their own requests)")
			return
		}
	}
	ctx := c.r.Context()
	site, err := s.loadSite(ctx)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if raw, ok := req["hosts"]; ok {
		var hosts []string
		if err := json.Unmarshal(raw, &hosts); err != nil || hosts == nil {
			bad(c, "hosts is a list of host names")
			return
		}
		if len(hosts) > dailyfetch.MaxHosts {
			bad(c, "at most 100 hosts")
			return
		}
		seen := map[string]bool{}
		site.Hosts = []string{}
		for _, h := range hosts {
			h, err := dailyfetch.CheckHost(h)
			if err != nil {
				bad(c, err.Error())
				return
			}
			if !seen[h] {
				seen[h] = true
				site.Hosts = append(site.Hosts, h)
			}
		}
	}
	if raw, ok := req["languages"]; ok {
		var langs []string
		if err := json.Unmarshal(raw, &langs); err != nil || langs == nil {
			bad(c, "languages is a list of language codes like zh and en")
			return
		}
		if len(langs) > 30 {
			bad(c, "at most 30 languages")
			return
		}
		seen := map[string]bool{}
		site.Languages = []string{}
		for _, l := range langs {
			l = space.NormalLang(l)
			if !space.ValidLang(l) {
				bad(c, l+" is not a language code")
				return
			}
			if !seen[l] {
				seen[l] = true
				site.Languages = append(site.Languages, l)
			}
		}
	}
	if raw, ok := req["allow"]; ok {
		var a struct {
			Own *bool `json:"own"`
			API *bool `json:"api"`
		}
		if err := json.Unmarshal(raw, &a); err != nil || a.Own == nil && a.API == nil {
			bad(c, "allow is {\"own\": true or false, \"api\": true or false}")
			return
		}
		// What is left out stays.
		if a.Own != nil {
			site.Allow.Own = *a.Own
		}
		if a.API != nil {
			site.Allow.API = *a.API
		}
	}
	if raw, ok := req["default"]; ok {
		var d *siteDefault
		if err := json.Unmarshal(raw, &d); err != nil {
			bad(c, "default: "+err.Error())
			return
		}
		if d != nil {
			if err := s.checkSiteDefault(ctx, d); err != nil {
				s.fail(c.w, err)
				return
			}
		}
		site.Default = d
	}
	if err := s.saveSite(ctx, site); err != nil {
		s.fail(c.w, err)
		return
	}
	s.adminAnswerDaily(c)
}

// adminPutDailyAPI makes or changes an interface. Its host is added to the
// hosts that may be fetched from.
func (s *server) adminPutDailyAPI(c *call) {
	var b apiBody
	if !s.readJSON(c.w, c.r, &b, false) {
		return
	}
	id := c.r.PathValue("id")
	if b.ID != "" && b.ID != id {
		bad(c, "the id in the body is not the one in the address")
		return
	}
	b.ID = id
	ctx := c.r.Context()
	site, err := s.loadSite(ctx)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	at := -1
	for i, a := range site.APIs {
		if a.ID == id {
			at = i
		}
	}
	var old *storedAPI
	if at >= 0 {
		old = &site.APIs[at]
	} else if len(site.APIs) >= dailyfetch.MaxAPIs {
		bad(c, "at most 50 interfaces")
		return
	}
	made, _, host, err := s.build(b, old)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if at >= 0 {
		site.APIs[at] = made
	} else {
		site.APIs = append(site.APIs, made)
	}
	if !hostIn(site.Hosts, host) {
		if len(site.Hosts) >= dailyfetch.MaxHosts {
			bad(c, "the list of hosts is full")
			return
		}
		site.Hosts = append(site.Hosts, host)
	}
	if err := s.saveSite(ctx, site); err != nil {
		s.fail(c.w, err)
		return
	}
	s.adminAnswerDaily(c)
}

func hostIn(hosts []string, h string) bool {
	for _, x := range hosts {
		if x == h {
			return true
		}
	}
	return false
}

// adminDeleteDailyAPI removes an interface (its host stays on the list; the
// list is the administrators' to change).
func (s *server) adminDeleteDailyAPI(c *call) {
	ctx := c.r.Context()
	site, err := s.loadSite(ctx)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	id := c.r.PathValue("id")
	kept := site.APIs[:0]
	found := false
	for _, a := range site.APIs {
		if a.ID == id {
			found = true
			continue
		}
		kept = append(kept, a)
	}
	if !found {
		writeError(c.w, http.StatusNotFound, "not_found", "no such interface")
		return
	}
	site.APIs = kept
	if err := s.saveSite(ctx, site); err != nil {
		s.fail(c.w, err)
		return
	}
	s.adminAnswerDaily(c)
}

// adminTryDailyAPI makes one request now with an interface as it is in the
// form (saved or not) and shows what came back: the answer as it is, cut, and
// what the paths take out of it.
func (s *server) adminTryDailyAPI(c *call) {
	var req struct {
		API  apiBody `json:"api"`
		Lang string  `json:"lang"`
		Date string  `json:"date"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	lang := space.NormalLang(req.Lang)
	if lang != "" && !space.ValidLang(lang) {
		bad(c, "lang is a language code like zh or en")
		return
	}
	date := req.Date
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	if !space.ValidDailyDate(date) {
		bad(c, "date must be a day like 2026-11-23")
		return
	}
	ctx := c.r.Context()
	site, err := s.loadSite(ctx)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	var old *storedAPI
	for i, a := range site.APIs {
		if a.ID == req.API.ID {
			old = &site.APIs[i]
		}
	}
	if req.API.ID == "" {
		req.API.ID = "trial"
	}
	_, rt, _, err := s.build(req.API, old)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	res, err := s.opt.DailyFetch.Probe(ctx, rt, lang, date)
	if errors.Is(err, dailyfetch.ErrRateLimited) {
		c.w.Header().Set("Retry-After", "1")
		writeError(c.w, http.StatusTooManyRequests, "rate_limited", "trying too often; wait a moment")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, res)
}

// --- what the owner of a space chooses ---------------------------------------------

type dailyPrefsResp struct {
	Inherit   bool       `json:"inherit"`
	APIs      []string   `json:"apis"`
	Available []availAPI `json:"available"`
	// Languages are the languages there is a settings tab for.
	Languages []string `json:"languages"`
	// Allowed is what the administrators let the spaces do; what is false here
	// is saved when asked, and not in effect.
	Allowed dailyAllow `json:"allowed"`
	Default *struct {
		Kind string `json:"kind"`
	} `json:"default"`
}

type availAPI struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Lang string `json:"lang"`
}

func (s *server) dailyPrefs(c *call) (dailyPrefsResp, error) {
	ctx := c.r.Context()
	out := dailyPrefsResp{APIs: []string{}, Available: []availAPI{}}
	prefs, err := store.GetSpaceDaily(ctx, s.opt.Store.Q(), c.sp.Info.ID)
	if err != nil {
		return out, err
	}
	site, err := s.loadSite(ctx)
	if err != nil {
		return out, err
	}
	out.Inherit = prefs.Inherit
	out.Languages = site.Languages
	out.Allowed = *site.Allow
	var ids []string
	json.Unmarshal([]byte(prefs.APIs), &ids)
	known := map[string]bool{}
	for _, a := range site.APIs {
		if !a.Enabled {
			continue
		}
		known[a.ID] = true
		out.Available = append(out.Available, availAPI{a.ID, a.Name, a.Lang})
	}
	for _, id := range ids {
		if known[id] {
			out.APIs = append(out.APIs, id)
		}
	}
	if site.Default != nil {
		out.Default = &struct {
			Kind string `json:"kind"`
		}{site.Default.Kind}
	}
	return out, nil
}

func (s *server) getDailyPrefs(c *call) {
	out, err := s.dailyPrefs(c)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, out)
}

func (s *server) putDailyPrefs(c *call) {
	var req struct {
		Inherit *bool     `json:"inherit"`
		APIs    *[]string `json:"apis"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	if req.Inherit == nil || req.APIs == nil {
		bad(c, "give inherit (true or false) and apis (the ids of the interfaces to use)")
		return
	}
	ctx := c.r.Context()
	site, err := s.loadSite(ctx)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	known := map[string]bool{}
	for _, a := range site.APIs {
		if a.Enabled {
			known[a.ID] = true
		}
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, id := range *req.APIs {
		if !known[id] {
			bad(c, "there is no interface "+id+"; only those the administrators set up and enabled can be used")
			return
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	b, _ := json.Marshal(ids)
	if err := store.SetSpaceDaily(ctx, s.opt.Store.Q(), c.sp.Info.ID, store.SpaceDaily{Inherit: *req.Inherit, APIs: string(b)}); err != nil {
		s.fail(c.w, err)
		return
	}
	s.getDailyPrefs(c)
}

// --- how long a browser may keep an answer -------------------------------------------

// maxAgeFor is how many seconds an answer about a day stays right: until the
// day ends where the asker is (tz is minutes east of UTC); without tz, a short
// time, since the end of their day is not known. Between a minute and a day.
func maxAgeFor(date string, tz *int, now time.Time) int {
	if tz == nil {
		return 300
	}
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return 60
	}
	end := t.Add(24*time.Hour - time.Duration(*tz)*time.Minute)
	return clampAge(int(end.Sub(now) / time.Second))
}

func clampAge(sec int) int { return max(60, min(sec, 86400)) }

func parseTZ(v url.Values) (*int, bool) {
	raw := v.Get("tz")
	if raw == "" {
		return nil, true
	}
	n, ok := atoiRange(raw, -840, 840)
	if !ok {
		return nil, false
	}
	return &n, true
}

func atoiRange(s string, lo, hi int) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= lo && n <= hi
}
