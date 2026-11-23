package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/dailyfetch"
	"github.com/KKazuhaK/PetalBook/internal/langtag"
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
	// Languages are the languages the settings pages have a tab for, besides
	// "all languages"; the administrators' own list, as canonical BCP 47 tags.
	Languages []string `json:"languages"`
	// APIs are the interfaces the administrators wrote; Providers what they
	// chose of the built-in ones (see all).
	APIs      []storedAPI              `json:"apis"`
	Providers map[string]providerState `json:"providers,omitempty"`
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

// providerState is what the administrators chose of a built-in interface.
type providerState struct {
	Enabled        bool    `json:"enabled"`
	Refresh        string  `json:"refresh,omitempty"`
	RefreshMinutes int     `json:"minutes,omitempty"`
	RPS            float64 `json:"rps,omitempty"`
	SkipNew        *bool   `json:"skipNew,omitempty"`
}

var defaultLanguages = []string{"zh-Hans", "en"}

// canonLangs reads a list of language tags (saved now or before the rule) as
// canonical tags, each once.
func canonLangs(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, l := range in {
		l = langtag.Read(l)
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

func (d *siteDaily) fill() {
	if d.APIs == nil {
		d.APIs = []storedAPI{}
	}
	if d.Languages == nil {
		d.Languages = append([]string(nil), defaultLanguages...)
	}
	// Values saved before the rule (zh, pt-br) are read as canonical tags and
	// written back as such on the next save.
	d.Languages = canonLangs(d.Languages)
	for i := range d.APIs {
		d.APIs[i].Lang = langtag.Read(d.APIs[i].Lang)
		if d.APIs[i].LangMap == nil {
			d.APIs[i].LangMap = map[string]string{}
		}
	}
	if d.Allow == nil {
		d.Allow = &dailyAllow{Own: true, API: true}
	}
}

func (s *server) loadSite(ctx context.Context) (siteDaily, error) {
	return s.loadSiteQ(ctx, s.opt.Store.Q())
}

func (s *server) loadSiteQ(ctx context.Context, q store.Q) (siteDaily, error) {
	var d siteDaily
	raw, ok, err := store.GetSiteSetting(ctx, q, siteDailyKey)
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

// builtinAPI is a built-in interface as the administrators set it.
func builtinAPI(p dailyfetch.Provider, st providerState) storedAPI {
	a := p.API
	a.Enabled = st.Enabled
	a.Builtin = true
	if st.Refresh != "" {
		a.Refresh, a.RefreshMinutes = st.Refresh, st.RefreshMinutes
	}
	if st.RPS > 0 {
		a.RPS = st.RPS
	}
	if st.SkipNew != nil {
		a.SkipNew = *st.SkipNew
	}
	return storedAPI{API: a, Headers: []storedHeader{}}
}

// all are the interfaces that are set up: those the administrators wrote and
// the built-in ones they switched on.
func (d *siteDaily) all() []storedAPI {
	out := append([]storedAPI(nil), d.APIs...)
	for _, p := range dailyfetch.Providers() {
		if st := d.Providers[p.ID]; st.Enabled {
			out = append(out, builtinAPI(p, st))
		}
	}
	return out
}

// editSite changes the administrators' daily text settings: it reads them,
// lets change change them and writes them, in one transaction that holds
// them, so that two administrators saving at once both have their change. An
// error from change leaves them as they were. change must use the state
// database only through q.
func (s *server) editSite(ctx context.Context, change func(q store.Q, d *siteDaily) error) error {
	return s.opt.Store.Tx(ctx, func(q store.Q) error {
		if err := store.LockSiteSetting(ctx, q, siteDailyKey, time.Now()); err != nil {
			return err
		}
		d, err := s.loadSiteQ(ctx, q)
		if err != nil {
			return err
		}
		if err := change(q, &d); err != nil {
			return err
		}
		d.fill()
		b, _ := json.Marshal(d)
		return store.SetSiteSetting(ctx, q, siteDailyKey, string(b), time.Now())
	})
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

// headerPurpose binds a stored header value to the exact request it was given
// for: the interface, the header's name and the address as it is written, with
// its scheme, host, port, path, query and placeholders. A value sealed for one
// does not open for another, so a changed address cannot send a kept value
// anywhere else, nor anywhere that says it back.
func headerPurpose(id, name, rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return "daily-header/" + id + "/" + strings.ToLower(name) + "/" + hex.EncodeToString(sum[:])
}

// runtime is the interface with its header values in the clear, to be asked.
func (s *server) runtime(a storedAPI) (dailyfetch.API, error) {
	out := a.API
	out.Headers = nil
	for _, h := range a.Headers {
		box := s.spaces.Secrets()
		if box == nil {
			return out, errors.New("this server has no key for stored secrets")
		}
		v, err := box.Open("site", headerPurpose(a.ID, h.Name, a.URL), h.Enc)
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

func (b dailyBacking) Trim(ctx context.Context, apiKey string, keep int) error {
	return store.TrimDailyCache(ctx, b.st.Q(), apiKey, keep)
}

// siteDaily is what the administrators' settings give for a space with no
// source of its own: the external interface the space chose, else the default
// when the space takes it. A nil quote with a reason is a block that does not
// show; with none, there is nothing set.
func (s *server) siteDaily(c *call, site siteDaily, date string, skip int, lang string, tz *int) (*dailyQuote, string, error) {
	ctx := c.r.Context()
	prefs, err := store.GetSpaceDaily(ctx, s.opt.Store.Q(), c.sp.Info.ID)
	if err != nil {
		return nil, "", err
	}
	var ids []string
	if site.Allow.API { // when it is not allowed, the choice is kept and not used
		json.Unmarshal([]byte(prefs.APIs), &ids)
	}
	all := site.all()
	if a := chooseAPI(all, ids, lang); a != nil {
		return s.dailyFromAPI(c, site, *a, date, skip, lang, tz)
	}
	if !prefs.Inherit || site.Default == nil {
		return nil, "", nil
	}
	switch site.Default.Kind {
	case "list":
		return s.dailyFromLines(ctx, date, skip, lang)
	case "space":
		return s.dailyFromSpace(ctx, *site.Default, date, skip, lang)
	case "api":
		// The administrators' own choice: not limited by what spaces may choose.
		id, ok := defaultAPIFor(site.Default.APIs, lang)
		if !ok {
			return nil, "", nil
		}
		for i := range all {
			if all[i].ID == id && all[i].Enabled {
				return s.dailyFromAPI(c, site, all[i], date, skip, lang, tz)
			}
		}
		return nil, "unavailable", nil // gone, or switched off
	}
	return nil, "", nil
}

// defaultAPIFor is the interface of the default for a reader's language: the
// one for the language it is matched to, else the one for all languages.
func defaultAPIFor(apis map[string]string, lang string) (string, bool) {
	keys := make([]string, 0, len(apis))
	for k := range apis {
		if k != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if m, ok := langtag.Match(lang, keys); ok {
		return apis[m], true
	}
	id, ok := apis[""]
	return id, ok
}

// dailyFromAPI asks an external interface (or what is kept of it), for a space's
// choice and for the site's default alike: the same cache, refresh and rate limit.
func (s *server) dailyFromAPI(c *call, site siteDaily, a storedAPI, date string, skip int, lang string, tz *int) (*dailyQuote, string, error) {
	// An interface is asked about the days around today only: each other
	// day would be a request, a row kept and a lane of its own.
	if !nearToday(date, s.opt.DailyFetch.Now(), tz) {
		return nil, "out_of_range", nil
	}
	rt, err := s.runtime(a)
	if err != nil {
		s.log.Error("an external daily text interface cannot be set up", "interface", a.ID, "error", err)
		return nil, "unavailable", nil
	}
	r, err := s.opt.DailyFetch.Get(c.r.Context(), rt, rt.LaneLang(lang, site.Languages), date, skip)
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

// apiLangs are the languages an interface answers in: its own list when it
// has one (a built-in interface), else its language, none for every language.
func apiLangs(a storedAPI) []string {
	if len(a.Languages) > 0 {
		return a.Languages
	}
	if a.Lang != "" {
		return []string{a.Lang}
	}
	return nil
}

// chooseAPI picks, among the enabled interfaces a space chose, the one for the
// language: made for the language the reader's is matched to (the BCP 47 way),
// else one that takes every language, else any.
func chooseAPI(all []storedAPI, ids []string, lang string) *storedAPI {
	chosen := map[string]bool{}
	for _, id := range ids {
		chosen[id] = true
	}
	var mine []storedAPI
	var tags []string
	for _, a := range all {
		if chosen[a.ID] && a.Enabled {
			mine = append(mine, a)
			tags = append(tags, apiLangs(a)...)
		}
	}
	if len(mine) == 0 {
		return nil
	}
	if m, ok := langtag.Match(lang, tags); ok {
		for i := range mine {
			if slices.Contains(apiLangs(mine[i]), m) {
				return &mine[i]
			}
		}
	}
	for i := range mine {
		if mine[i].Lang == "" {
			return &mine[i]
		}
	}
	return &mine[0]
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

// Codes of the refusals of the daily text's requests (errorBody.Code); those
// of what a trial finds are dailyfetch's.
const (
	codeHeaderNoValue    = "header_missing"
	codeHeaderValueAgain = "header_reenter"
	codeSkipTooLarge     = "skip_too_large"
	codeBuiltin          = "builtin"
	codeAPINotFound      = "api_not_found"
)

// build checks an interface from a request and makes the stored form, keeping
// the header values already stored for names that come without one. old is the
// stored interface with the same id, if any.
func (s *server) build(b apiBody, old *storedAPI) (storedAPI, dailyfetch.API, error) {
	a := b.API
	a.Builtin, a.Languages = false, nil // only the program makes a built-in interface
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
				return storedAPI{}, a, &codedError{code: codeHeaderNoValue, header: h.Name, msg: "the header " + h.Name + " has no value yet; give one"}
			}
			// A kept value goes only to the request it was given for: with any
			// change to the address (its path and query too) it is given again,
			// by someone who knows it.
			if a.URL != old.URL {
				return storedAPI{}, a, &codedError{code: codeHeaderValueAgain, header: h.Name,
					msg: "the address is not the one the value of the header " + h.Name + " was given for; give the value again"}
			}
			box := s.spaces.Secrets()
			if box == nil {
				return storedAPI{}, a, errors.New("this server has no key for stored secrets")
			}
			v, err := box.Open("site", headerPurpose(old.ID, h.Name, old.URL), enc)
			if err != nil {
				return storedAPI{}, a, err
			}
			clear = append(clear, dailyfetch.Header{Name: h.Name, Value: string(v)})
		} else {
			clear = append(clear, dailyfetch.Header{Name: h.Name, Value: *h.Value})
		}
		stored = append(stored, storedHeader{Name: h.Name, Enc: enc})
	}
	a.Headers = clear
	checked, err := dailyfetch.CheckAPI(a, s.opt.DailyFetch.AllowHTTP())
	if err != nil {
		var ce *dailyfetch.CodedError
		if errors.As(err, &ce) {
			return storedAPI{}, a, &codedError{code: ce.Code, field: ce.Field, msg: ce.Msg}
		}
		return storedAPI{}, a, &space.InvalidError{Msg: err.Error()}
	}
	// Values given now are sealed for this interface, header and address.
	for i, h := range b.Headers {
		if h.Value == nil {
			continue
		}
		box := s.spaces.Secrets()
		if box == nil {
			return storedAPI{}, a, &space.InvalidError{Msg: "this server has no key for storing secrets, so it cannot keep a header value"}
		}
		enc, err := box.Seal("site", headerPurpose(checked.ID, h.Name, checked.URL), []byte(*h.Value))
		if err != nil {
			return storedAPI{}, a, err
		}
		stored[i].Enc = enc
	}
	checked.Headers = nil
	return storedAPI{API: checked, Headers: stored}, a, nil
}

// jsonCoded is the refusal a decoding error is, when it carries a code
// (a language that is not one), else the error itself.
func jsonCoded(prefix string, err error) error {
	var inv *space.InvalidError
	if errors.As(err, &inv) && inv.Code != "" {
		return &codedError{code: inv.Code, msg: prefix + inv.Msg}
	}
	return &space.InvalidError{Msg: prefix + err.Error()}
}

func badLangError(tag string) error {
	return &codedError{code: space.CodeBadLanguage, msg: fmt.Sprintf("%q is not a language tag like zh-Hans, en or pt-BR", tag)}
}

// adminPutDaily changes the default, the languages and what spaces may do.
// What the request leaves out stays as it is; a `hosts` sent by an older client
// is ignored (there is no list of hosts).
func (s *server) adminPutDaily(c *call) {
	var req map[string]json.RawMessage
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	for k := range req {
		if k != "default" && k != "hosts" && k != "languages" && k != "allow" {
			bad(c, "unknown key "+k+": the settings are default, languages and allow (interfaces have their own requests)")
			return
		}
	}
	ctx := c.r.Context()
	// Everything is checked first; the change is then made to the settings as
	// they are at that moment (see editSite).
	var langs []string
	if raw, ok := req["languages"]; ok {
		var given []string
		if err := json.Unmarshal(raw, &given); err != nil || given == nil {
			bad(c, "languages is a list of language tags like zh-Hans and en")
			return
		}
		if len(given) > 30 {
			bad(c, "at most 30 languages")
			return
		}
		seen := map[string]bool{}
		langs = []string{}
		for _, l := range given {
			cl, ok := langtag.Canonical(l)
			if !ok {
				s.fail(c.w, badLangError(l))
				return
			}
			if !seen[cl] {
				seen[cl] = true
				langs = append(langs, cl)
			}
		}
	}
	var allow struct {
		Own *bool `json:"own"`
		API *bool `json:"api"`
	}
	if raw, ok := req["allow"]; ok {
		if err := json.Unmarshal(raw, &allow); err != nil || allow.Own == nil && allow.API == nil {
			bad(c, "allow is {\"own\": true or false, \"api\": true or false}")
			return
		}
	}
	var def *siteDefault
	if raw, ok := req["default"]; ok {
		if err := json.Unmarshal(raw, &def); err != nil {
			s.fail(c.w, jsonCoded("default: ", err))
			return
		}
		if def != nil {
			site, err := s.loadSite(ctx)
			if err == nil {
				err = s.checkSiteDefault(ctx, def, &site)
			}
			if err != nil {
				s.fail(c.w, err)
				return
			}
		}
	}
	err := s.editSite(ctx, func(_ store.Q, site *siteDaily) error {
		if def != nil && def.Kind == "api" {
			// The interfaces as they are while the settings are held.
			if err := checkDefaultAPIs(def, site); err != nil {
				return err
			}
		}
		if langs != nil {
			site.Languages = langs
		}
		// What is left out stays.
		if allow.Own != nil {
			site.Allow.Own = *allow.Own
		}
		if allow.API != nil {
			site.Allow.API = *allow.API
		}
		if _, ok := req["default"]; ok {
			site.Default = def
		}
		return nil
	})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.adminAnswerDaily(c)
}

// adminPutDailyAPI makes or changes an interface. A built-in one is only
// switched and tuned: its address, paths, link and language mapping cannot be
// changed.
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
	if p, ok := dailyfetch.ProviderByID(id); ok {
		s.putBuiltinFromAPI(c, p, b)
		return
	}
	err := s.editSite(c.r.Context(), func(_ store.Q, site *siteDaily) error {
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
			return &space.InvalidError{Msg: "at most 50 interfaces"}
		}
		made, _, err := s.build(b, old)
		if err != nil {
			return err
		}
		if at >= 0 {
			site.APIs[at] = made
		} else {
			site.APIs = append(site.APIs, made)
		}
		return nil
	})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.adminAnswerDaily(c)
}

// putBuiltinFromAPI is a PUT of a built-in interface through the interfaces'
// address: what is fixed must be as it is, the rest is what a provider takes.
func (s *server) putBuiltinFromAPI(c *call, p dailyfetch.Provider, b apiBody) {
	same := func(field, got, want string) error {
		if got != "" && got != want {
			return &codedError{code: codeBuiltin, field: field, msg: "the " + field + " of the built-in interface " + p.ID + " cannot be changed"}
		}
		return nil
	}
	for _, e := range []error{same("name", b.Name, p.Name), same("lang", b.Lang, p.Lang), same("url", b.URL, p.URL), same("text", b.Text, p.Text),
		same("source", b.Source, p.Source), same("author", b.Author, p.Author), same("link", b.Link, p.Link)} {
		if e != nil {
			s.fail(c.w, e)
			return
		}
	}
	if len(b.Headers) > 0 || len(b.LangMap) > 0 && !maps.Equal(b.LangMap, p.LangMap) {
		field := "langMap"
		if len(b.Headers) > 0 {
			field = "headers"
		}
		s.fail(c.w, &codedError{code: codeBuiltin, field: field, msg: "the " + field + " of the built-in interface " + p.ID + " cannot be changed"})
		return
	}
	in := providerBody{Enabled: b.Enabled, Refresh: b.Refresh, Minutes: b.RefreshMinutes, RPS: b.RPS}
	skip := b.SkipNew
	in.SkipNew = &skip
	s.putProvider(c, p, in, true)
}

// providerBody is what a request switches or tunes of a built-in interface.
type providerBody struct {
	Enabled *bool   `json:"enabled"`
	Refresh string  `json:"refresh"`
	Minutes int     `json:"minutes"`
	RPS     float64 `json:"rps"`
	SkipNew *bool   `json:"skipNew"`
}

// adminPutDailyProvider switches a built-in interface on or off and tunes how
// it is asked.
func (s *server) adminPutDailyProvider(c *call) {
	p, ok := dailyfetch.ProviderByID(c.r.PathValue("id"))
	if !ok {
		writeError(c.w, http.StatusNotFound, "not_found", "no such built-in interface")
		return
	}
	var in providerBody
	if !s.readJSON(c.w, c.r, &in, false) {
		return
	}
	if in.Enabled == nil {
		bad(c, "give enabled (true or false)")
		return
	}
	s.putProvider(c, p, in, false)
}

func (s *server) putProvider(c *call, p dailyfetch.Provider, in providerBody, fromAPI bool) {
	err := s.editSite(c.r.Context(), func(_ store.Q, site *siteDaily) error {
		st := site.Providers[p.ID]
		if in.Enabled != nil {
			st.Enabled = *in.Enabled
		} else if fromAPI && !st.Enabled {
			st.Enabled = true // a new interface that leaves it out is enabled
		}
		if st.Enabled {
			for _, a := range site.APIs {
				if a.ID == p.ID {
					return &space.InvalidError{Msg: "an interface of the same id exists; delete it before switching the built-in one on"}
				}
			}
		}
		switch in.Refresh {
		case "":
		case dailyfetch.RefreshDaily, dailyfetch.RefreshHourly:
			st.Refresh, st.RefreshMinutes = in.Refresh, 0
		case dailyfetch.RefreshMinutes:
			if in.Minutes < 1 || in.Minutes > 1440 {
				return &space.InvalidError{Msg: "minutes is 1 to 1440"}
			}
			st.Refresh, st.RefreshMinutes = in.Refresh, in.Minutes
		default:
			return &space.InvalidError{Msg: "refresh is daily, hourly or minutes"}
		}
		if in.RPS != 0 {
			if in.RPS < 0.01 || in.RPS > p.RPS {
				return &space.InvalidError{Msg: fmt.Sprintf("rps is 0.01 to %g, the most %s allows", p.RPS, p.Name)}
			}
			st.RPS = in.RPS
			if in.RPS == p.RPS {
				st.RPS = 0
			}
		}
		if in.SkipNew != nil {
			v := *in.SkipNew
			st.SkipNew = &v
			if v == p.SkipNew {
				st.SkipNew = nil
			}
		}
		if site.Providers == nil {
			site.Providers = map[string]providerState{}
		}
		site.Providers[p.ID] = st
		return nil
	})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.adminAnswerDaily(c)
}

var errNoInterface = errors.New("no such interface")

// adminDeleteDailyAPI removes an interface.
func (s *server) adminDeleteDailyAPI(c *call) {
	id := c.r.PathValue("id")
	if _, ok := dailyfetch.ProviderByID(id); ok {
		s.fail(c.w, &codedError{code: codeBuiltin, msg: "a built-in interface cannot be deleted; switch it off"})
		return
	}
	err := s.editSite(c.r.Context(), func(_ store.Q, site *siteDaily) error {
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
			return errNoInterface
		}
		site.APIs = kept
		return nil
	})
	if errors.Is(err, errNoInterface) {
		writeError(c.w, http.StatusNotFound, "not_found", "no such interface")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.adminAnswerDaily(c)
}

// adminTryDailyAPI makes one request now with an interface as it is in the
// form (saved or not) and shows what came back: the answer as it is, cut, and
// what the paths take out of it. It goes only to the host and port of the
// address in the form, under the same checks as any request.
func (s *server) adminTryDailyAPI(c *call) {
	var req struct {
		API  apiBody `json:"api"`
		Lang string  `json:"lang"`
		Date string  `json:"date"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	lang := ""
	if req.Lang != "" {
		var ok bool
		if lang, ok = langtag.Canonical(req.Lang); !ok {
			s.fail(c.w, badLangError(req.Lang))
			return
		}
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
	_, rt, err := s.build(req.API, old)
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
	if e := probeRefusal(res); e != nil {
		// The refusal, with what the trial showed beside it.
		writeJSON(c.w, http.StatusBadRequest, struct {
			errorBody
			Result dailyfetch.ProbeResult `json:"result"`
		}{e.body(), res})
		return
	}
	writeJSON(c.w, http.StatusOK, res)
}

// probeRefusal is the refusal a trial's result is, if it is one with a code
// (docs/api.md, "Daily text refusals"): the answer is not JSON or too large,
// or a path is wrong, a path that takes out a
// request header's value first. Other failures (the service did not answer,
// or answered with an error status) are a result with ok false.
func probeRefusal(res dailyfetch.ProbeResult) *codedError {
	switch res.Code {
	case dailyfetch.CodeNotJSON, dailyfetch.CodeTooLarge:
		return &codedError{code: res.Code, msg: res.Message}
	}
	for _, want := range []string{dailyfetch.CodePathRevealsSecret, dailyfetch.CodePathError} {
		for _, pe := range res.PathErrors {
			if pe.Code == want {
				return &codedError{code: want, field: pe.Field, msg: pe.Message}
			}
		}
	}
	return nil
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
	for _, a := range site.all() {
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
	for _, a := range site.all() {
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

// nearToday reports whether date is today, yesterday or tomorrow where the
// asker is (tz is minutes east of UTC; without it, in UTC).
func nearToday(date string, now time.Time, tz *int) bool {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return false
	}
	local := now.UTC()
	if tz != nil {
		local = local.Add(time.Duration(*tz) * time.Minute)
	}
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	diff := d.Sub(today)
	return diff >= -24*time.Hour && diff <= 24*time.Hour
}

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
