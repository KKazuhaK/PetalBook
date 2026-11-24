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
	// KeepsID marks an interface of the administrators' own with the id of a
	// built-in one, kept so (see adoptBuiltins).
	KeepsID bool `json:"keepsId,omitempty"`
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
	// Notices are what the server changed by itself (see siteNotice).
	Notices []siteNotice `json:"notices,omitempty"`
	// repair says what was read was repaired and is to be saved; renames are
	// the interfaces whose id changed, for the spaces' choices.
	repair  bool
	renames map[string]string
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

// loadSite reads the administrators' settings. Settings an earlier version
// saved that need repairing (see decodeSite and adoptBuiltins) are repaired
// and saved once, here, so that nobody has to.
func (s *server) loadSite(ctx context.Context) (siteDaily, error) {
	d, err := s.loadSiteQ(ctx, s.opt.Store.Q())
	if err != nil || !d.repair {
		return d, err
	}
	if err := s.editSite(ctx, func(store.Q, *siteDaily) error { return nil }); err != nil {
		// What was read is used as it is repaired in memory; saving is tried again next time.
		s.log.Error("the repaired daily text settings cannot be saved", "error", err)
		return d, nil
	}
	return s.loadSiteQ(ctx, s.opt.Store.Q())
}

// loadSiteQ reads the settings and repairs them in memory: repair says they
// are to be saved, renames which interfaces changed their id.
func (s *server) loadSiteQ(ctx context.Context, q store.Q) (siteDaily, error) {
	var d siteDaily
	raw, ok, err := store.GetSiteSetting(ctx, q, siteDailyKey)
	if err != nil {
		return d, err
	}
	if ok {
		d = s.decodeSite(raw)
		s.adoptBuiltins(&d)
	}
	d.fill()
	return d, nil
}

// siteNotice is something the administrators are told about their settings:
// what the server changed in them by itself, reading what an earlier version
// saved. It stays until they dismiss it.
type siteNotice struct {
	// Code is builtin_adopted, builtin_renamed, builtin_kept, dropped_language
	// or unreadable.
	Code string `json:"code"`
	// ID is the interface, To its new id; Lang the code that was dropped;
	// Field where it was.
	ID      string `json:"id,omitempty"`
	To      string `json:"to,omitempty"`
	Lang    string `json:"lang,omitempty"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// notify adds a notice (once) and marks the settings as to be saved.
func (d *siteDaily) notify(n siteNotice) {
	d.repair = true
	for _, have := range d.Notices {
		if have.Code == n.Code && have.ID == n.ID && have.Lang == n.Lang && have.Field == n.Field {
			return
		}
	}
	d.Notices = append(d.Notices, n)
}

// decodeSite reads stored settings leniently: each part on its own, so that
// one that cannot be read is the only one taken as not set; in the default,
// language codes are read as canonical tags and those that are none (`cn`,
// saved by 2.3) are dropped, with a notice. Nothing else is lost.
func (s *server) decodeSite(raw string) siteDaily {
	var d siteDaily
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		s.log.Error("the daily text settings of the server cannot be read; they are taken as not set", "error", err)
		return d
	}
	part := func(name string, into any) {
		v, ok := top[name]
		if !ok || string(v) == "null" {
			return
		}
		if err := json.Unmarshal(v, into); err != nil {
			s.log.Error("a part of the daily text settings cannot be read; it is taken as not set", "part", name, "value", string(v), "error", err)
			d.notify(siteNotice{Code: "unreadable", Field: name, Message: "the " + name + " of the daily text settings could not be read and are taken as not set"})
		}
	}
	part("languages", &d.Languages)
	part("providers", &d.Providers)
	part("allow", &d.Allow)
	part("notices", &d.Notices)
	var apis []json.RawMessage
	part("apis", &apis)
	for _, v := range apis {
		var a storedAPI
		if err := json.Unmarshal(v, &a); err != nil {
			s.log.Error("an interface of the daily text settings cannot be read; it is left out", "value", string(v), "error", err)
			d.notify(siteNotice{Code: "unreadable", Field: "apis", Message: "an interface of the daily text settings could not be read and is left out"})
			continue
		}
		d.APIs = append(d.APIs, a)
	}
	var def *struct {
		Kind    string                         `json:"kind"`
		Space   string                         `json:"space"`
		Source  json.RawMessage                `json:"source"`
		APIs    apiLists                       `json:"apis"`
		Options map[string]map[string][]string `json:"options"`
		Sources sourceLists                    `json:"sources"`
	}
	part("default", &def)
	if def == nil {
		return d
	}
	d.Default = &siteDefault{Kind: def.Kind, Space: def.Space, Options: def.Options, Sources: def.Sources}
	dropped := func(field, code string) {
		s.log.Warn("a language of the daily text default is not a language tag; it is dropped", "field", field, "code", code)
		d.notify(siteNotice{Code: "dropped_language", Lang: code, Field: field,
			Message: fmt.Sprintf("%q in the default (%s) is not a language tag, or a second spelling of one; it was dropped", code, field)})
	}
	if len(def.Source) > 0 && string(def.Source) != "null" {
		src, out := space.ReadStoredDailyConfig(def.Source)
		d.Default.Source = src
		for _, k := range out {
			dropped("source", k)
		}
	}
	if def.APIs != nil {
		keys := slices.Sorted(maps.Keys(def.APIs))
		// The canonical spelling of a language first, then the others by order.
		sort.SliceStable(keys, func(i, j int) bool { return langtag.Read(keys[i]) == keys[i] && langtag.Read(keys[j]) != keys[j] })
		d.Default.APIs = apiLists{}
		for _, k := range keys {
			l := k
			if k != "" {
				var ok bool
				if l, ok = langtag.Canonical(k); !ok {
					dropped("apis", k)
					continue
				}
			}
			if _, taken := d.Default.APIs[l]; taken {
				dropped("apis", k)
				continue
			}
			d.Default.APIs[l] = def.APIs[k]
		}
	}
	return d
}

// sameAsBuiltin reports whether a stored interface asks what the built-in one
// does, the way it does: the same address, paths and link, no headers and no
// other language mapping (what 2.3's "use preset" saved, unchanged).
func sameAsBuiltin(a storedAPI, p dailyfetch.Provider) bool {
	return a.URL == p.URL && a.Text == p.Text && a.Source == p.Source && a.Author == p.Author && a.Link == p.Link &&
		len(a.Headers) == 0 && (len(a.LangMap) == 0 || maps.Equal(a.LangMap, p.LangMap))
}

// adoptBuiltins repairs the interfaces 2.3 saved from a preset with the id of
// what is now a built-in one. One the administrators did not change becomes
// the built-in one, switched on or off as it was, with its refresh, rate and
// "another" within the built-in's limits. One they changed stays theirs under
// a new id (`hitokoto-custom`), with its header values sealed again for that
// id; the spaces' choices (see editSite) and the default follow it. One whose
// values cannot be sealed again stays theirs under its id (KeepsID); the
// requests for interfaces then act on it before the built-in one.
func (s *server) adoptBuiltins(d *siteDaily) {
	taken := map[string]bool{}
	for _, a := range d.APIs {
		taken[a.ID] = true
	}
	for _, p := range dailyfetch.Providers() {
		taken[p.ID] = true
	}
	kept := d.APIs[:0:0]
	for _, a := range d.APIs {
		p, ok := dailyfetch.ProviderByID(a.ID)
		if !ok || a.KeepsID {
			kept = append(kept, a)
			continue
		}
		if sameAsBuiltin(a, p) {
			if d.Providers == nil {
				d.Providers = map[string]providerState{}
			}
			d.Providers[p.ID] = stateFrom(a, p)
			d.repair = true
			continue
		}
		to := a.ID + "-custom"
		for n := 2; taken[to]; n++ {
			to = fmt.Sprintf("%s-custom-%d", a.ID, n)
		}
		moved, err := s.resealed(a, to)
		if err != nil {
			s.log.Warn("an interface with the id of a built-in one keeps its id: its header values cannot be sealed again", "interface", a.ID, "error", err)
			a.KeepsID = true
			kept = append(kept, a)
			d.notify(siteNotice{Code: "builtin_kept", ID: a.ID,
				Message: "the interface " + a.ID + " has the id of a built-in one and keeps it, since its header values cannot be moved; delete it to use the built-in one"})
			continue
		}
		taken[to] = true
		kept = append(kept, moved)
		if d.renames == nil {
			d.renames = map[string]string{}
		}
		d.renames[a.ID] = to
		if d.Default != nil {
			for l, ids := range d.Default.APIs {
				d.Default.APIs[l] = renamed(ids, a.ID, to)
			}
			for l, list := range d.Default.Sources {
				for i := range list {
					if list[i].Kind == "api" && list[i].ID == a.ID {
						list[i].ID = to
					}
				}
				d.Default.Sources[l] = list
			}
			if o, ok := d.Default.Options[a.ID]; ok {
				delete(d.Default.Options, a.ID)
				d.Default.Options[to] = o
			}
		}
		d.notify(siteNotice{Code: "builtin_renamed", ID: a.ID, To: to,
			Message: "the interface " + a.ID + " differs from the built-in one of that id; it is kept as " + to})
	}
	d.APIs = kept
}

// renamed is the ids with from replaced by to, each once.
func renamed(ids []string, from, to string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == from {
			id = to
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// stateFrom is the built-in's state that keeps what a stored interface had,
// within the built-in's limits.
func stateFrom(a storedAPI, p dailyfetch.Provider) providerState {
	st := providerState{Enabled: a.Enabled}
	switch a.Refresh {
	case dailyfetch.RefreshDaily, dailyfetch.RefreshHourly:
		if a.Refresh != p.Refresh {
			st.Refresh = a.Refresh
		}
	case dailyfetch.RefreshMinutes:
		if a.RefreshMinutes >= 1 && a.RefreshMinutes <= 1440 {
			st.Refresh, st.RefreshMinutes = a.Refresh, a.RefreshMinutes
		}
	}
	if a.RPS >= 0.01 && a.RPS < p.RPS {
		st.RPS = a.RPS
	}
	if a.SkipNew != p.SkipNew {
		v := a.SkipNew
		st.SkipNew = &v
	}
	return st
}

// resealed is the interface under the id to, its header values sealed for it.
func (s *server) resealed(a storedAPI, to string) (storedAPI, error) {
	out := a
	out.ID = to
	out.Headers = make([]storedHeader, len(a.Headers))
	for i, h := range a.Headers {
		box := s.spaces.Secrets()
		if box == nil {
			return a, errors.New("this server has no key for stored secrets")
		}
		v, err := box.Open("site", headerPurpose(a.ID, h.Name, a.URL), h.Enc)
		if err != nil {
			return a, err
		}
		enc, err := box.Seal("site", headerPurpose(to, h.Name, a.URL), v)
		if err != nil {
			return a, err
		}
		out.Headers[i] = storedHeader{Name: h.Name, Enc: enc}
	}
	return out, nil
}

// builtinAPI is a built-in interface as the administrators set it.
func builtinAPI(p dailyfetch.Provider, st providerState) storedAPI {
	a := p.API
	if def, err := p.WithOptions(nil); err == nil {
		a.URL = def.URL // an option that is in the address, with its default
	}
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
	err := s.opt.Store.Tx(ctx, func(q store.Q) error { return s.editSiteQ(ctx, q, change) })
	// What the layered settings cached of it (the switches, the default) is old now.
	s.settings.Invalidate()
	return err
}

// editSiteQ is editSite inside a transaction of the caller's (the layered
// settings write the same record).
func (s *server) editSiteQ(ctx context.Context, q store.Q, change func(q store.Q, d *siteDaily) error) error {
	if err := store.LockSiteSetting(ctx, q, siteDailyKey, time.Now()); err != nil {
		return err
	}
	d, err := s.loadSiteQ(ctx, q)
	if err != nil {
		return err
	}
	// The spaces that chose an interface whose id changed follow it.
	for _, from := range slices.Sorted(maps.Keys(d.renames)) {
		if err := store.RenameSpaceDailyAPI(ctx, q, from, d.renames[from]); err != nil {
			return err
		}
	}
	if err := change(q, &d); err != nil {
		return err
	}
	d.fill()
	b, _ := json.Marshal(d)
	return store.SetSiteSetting(ctx, q, siteDailyKey, string(b), time.Now())
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
	allow, err := s.dailyAllowFor(ctx, c.user().TenantID)
	if err != nil {
		return nil, "", err
	}
	// What a space chose is kept when it is not allowed, and not used: the interfaces
	// while api is off, its own pages in a list while own is (see spaceSources).
	choice := store.ParseSpaceChoice(prefs.APIs)
	if !allow.API {
		choice.APIs, choice.Options = nil, nil
	}
	all := site.all()
	// The space's own list of sources, of any kind, for the reader's language.
	srcs, lost := s.spaceSources(choice, all, allow, lang)
	if len(srcs) > 0 {
		return s.dailyFromSources(c, site, srcs, "space:"+c.sp.Info.ID, date, skip, lang, tz)
	}
	if lost {
		// What the space chose is not there (gone, or switched off): nothing else is
		// picked in its place, and the owner is told in settings.
		return nil, "unavailable", nil
	}
	cands := chooseAPIs(all, choice.APIs, lang)
	if len(cands) == 0 && slices.ContainsFunc(choice.APIs, func(id string) bool { return site.ifaceStatus(id) != "" }) {
		return nil, "unavailable", nil // one it chose is gone or off, and none else gives a text
	}
	if len(cands) > 0 {
		srcs := make([]dailySource, len(cands))
		for i, a := range cands {
			srcs[i] = dailySource{Kind: "api", ID: a.ID, Options: choice.Options[a.ID]}
		}
		return s.dailyFromSources(c, site, srcs, "space:"+c.sp.Info.ID, date, skip, lang, tz)
	}
	// The default of the person's tenant comes between the space's and the site's.
	def, scope, err := s.dailyDefaultFor(ctx, c.user().TenantID, site.Default)
	if err != nil {
		return nil, "", err
	}
	if !prefs.Inherit || def == nil {
		return nil, "", nil
	}
	// The administrators' own choice: not limited by what spaces may choose.
	srcs, ok := def.sourcesFor(lang)
	if !ok {
		return nil, "", nil
	}
	return s.dailyFromSources(c, site, srcs, scope, date, skip, lang, tz)
}

// defaultAPIsFor are the interfaces of the default for a reader's language, in
// order: those for the language it is matched to, else those for all languages.
func defaultAPIsFor(apis apiLists, lang string) ([]string, bool) {
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
	ids, ok := apis[""]
	return ids, ok
}

// rotation is which of n interfaces the scope (a space, a tenant, the site)
// starts with on a day: the scope has its own place in the order and the
// interfaces take turns by day, so that everybody in the scope sees the same
// one that day.
func rotation(scope, date string, n int) int {
	t, err := time.Parse("2006-01-02", date)
	if err != nil || n <= 1 {
		return 0
	}
	day := int(t.Unix() / 86400)
	return ((day+int(space.DailyNumber("rotate:"+scope, "")%uint64(n)))%n + n) % n
}

// dailyFromSources gives the text of the sources a choice lists for a
// language (a space's, a tenant's, the site's; scope names which): one a day, in
// turn. A source is an external interface, the lines the administrators gave or
// pages of a space; one alone is used as it is. "Another" (skip) moves to the
// next; after the last it is the first again, which then asks for a new item if
// it can. A source that fails, is switched off, is asked too often or has
// nothing is passed over for the next; only when none gives anything is there
// nothing. What an interface said last (stale) is used only when no other source
// has a fresh one. Exactly one text is ever given.
func (s *server) dailyFromSources(c *call, site siteDaily, srcs []dailySource, scope, date string, skip int, lang string, tz *int) (*dailyQuote, string, error) {
	ctx := c.r.Context()
	n := len(srcs)
	start := rotation(scope, date, n)
	var stale *dailyQuote
	limited, tried := 0, 0
	reason := ""
	rank := map[string]int{"": 0, "no_match": 1, "out_of_range": 2, "unavailable": 3}
	// A step is a place in the turns: the source after the start, and the one after it, round and round;
	// each time round the list (the lap) an interface that can asks for a new item. "Another" is the next
	// step that gives a text, so it moves on from the source that gave today's, not from the one the day
	// started with when that one had nothing. With one source, step and skip are the same.
	steps := n * (skip + 2)
	if n == 1 {
		steps = skip + 1
	}
	found := 0
	for step := 0; step < steps; step++ {
		if n == 1 && step < skip {
			continue
		}
		q, why, err := s.fromSource(c, ctx, site, srcs[(start+step)%n], date, step/n, lang, tz)
		tried++
		switch {
		case errors.Is(err, dailyfetch.ErrRateLimited):
			limited++
		case err != nil:
			return nil, "", err
		case q != nil && q.Stale:
			if stale == nil {
				stale = q
			}
		case q != nil:
			if found == skip || n == 1 {
				return q, "", nil
			}
			found++
		default:
			if rank[why] >= rank[reason] {
				reason = why
			}
		}
		if found == 0 && step == n-1 && n > 1 {
			break // nothing gives a text today: later steps would not
		}
	}
	switch {
	case stale != nil && found == 0:
		return stale, "", nil
	case limited == tried:
		return nil, "", dailyfetch.ErrRateLimited
	}
	return nil, reason, nil
}

// fromSource is the text of one source: a text, or nothing with the reason
// (and the error when the interface was asked too often).
func (s *server) fromSource(c *call, ctx context.Context, site siteDaily, src dailySource, date string, skip int, lang string, tz *int) (*dailyQuote, string, error) {
	switch src.Kind {
	case "list":
		return s.dailyFromLines(ctx, date, skip, lang)
	case "space":
		return s.dailyFromSpace(ctx, siteDefault{Space: src.Space, Source: src.Source}, date, skip, lang)
	case "own":
		// Pages of the space the reader is in: one text, in the same fields as the others.
		if src.Source == nil {
			return nil, "", nil
		}
		ds := src.Source.For(lang)
		if ds == nil {
			return nil, "", nil
		}
		pick, none, err := c.sp.Space.DailyFrom(c.sp.Info.ID, date, skip, *ds)
		if err != nil {
			return nil, "", err
		}
		if pick == nil {
			if none == space.DailyNoMatch {
				return nil, "no_match", nil
			}
			return nil, "", nil
		}
		source := pick.Page.Source
		if source == "" {
			source = pick.Page.Title
		}
		return &dailyQuote{Text: pick.Body, Author: pick.Author, Source: source, origin: "space"}, "", nil
	case "api":
		// An interface is asked about the days around today only: each other
		// day would be a request, a row kept and a lane of its own.
		if !nearToday(date, s.opt.DailyFetch.Now(), tz) {
			return nil, "out_of_range", nil
		}
		var api *storedAPI
		for _, a := range site.all() {
			if a.ID == src.ID && a.Enabled {
				api = &a
				break
			}
		}
		if api == nil {
			return nil, "unavailable", nil // gone, or switched off
		}
		if !api.Serves(lang) {
			return nil, "", nil // it has no text in this language: the next source answers
		}
		q, err := s.askAPI(c, site, *api, src.Options, date, skip, lang)
		switch {
		case errors.Is(err, dailyfetch.ErrRateLimited):
			return nil, "", err
		case err != nil:
			return nil, "unavailable", nil
		}
		return q, "", nil
	}
	return nil, "", nil
}

// askAPI asks an external interface (or what is kept of it), for a space's
// choice and for the site's default alike: the same cache, refresh and rate
// limit. opts are the options chosen for it, used when it is a built-in one.
func (s *server) askAPI(c *call, site siteDaily, a storedAPI, opts map[string][]string, date string, skip int, lang string) (*dailyQuote, error) {
	rt, err := s.runtime(a)
	if err != nil {
		s.log.Error("an external daily text interface cannot be set up", "interface", a.ID, "error", err)
		return nil, dailyfetch.ErrUnavailable
	}
	if p, ok := dailyfetch.ProviderByID(a.ID); ok && a.Builtin {
		if withOpts, err := p.WithOptions(opts); err != nil {
			// What was chosen no longer fits the provider: it is asked for all.
			s.log.Warn("options of a built-in daily text interface are not valid; they are not used", "interface", a.ID, "error", err)
		} else {
			rt.URL = withOpts.URL
		}
	}
	r, err := s.opt.DailyFetch.Get(c.r.Context(), rt, rt.LaneLang(lang, site.Languages), date, skip)
	if err != nil {
		return nil, err
	}
	q := &dailyQuote{Text: r.Text, Source: r.Source, Author: r.Author, Link: r.Link, Fetched: rfc3339(r.Fetched), Stale: r.Stale, origin: "external"}
	if r.Stale {
		q.expires = s.opt.DailyFetch.Now().Add(time.Minute)
	} else if _, ends := rt.Period(s.opt.DailyFetch.Now(), date); !ends.IsZero() {
		q.expires = ends
	}
	return q, nil
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

// chooseAPIs picks, among the enabled interfaces a space chose, those for the
// language, in the order the space listed them: made for the language the
// reader's is matched to (the BCP 47 way), else those that take every
// language; none when there is no such, never one made for another language.
func chooseAPIs(all []storedAPI, ids []string, lang string) []storedAPI {
	var mine []storedAPI
	var tags []string
	for _, id := range ids {
		for _, a := range all {
			if a.ID == id && a.Enabled && !slices.ContainsFunc(mine, func(m storedAPI) bool { return m.ID == id }) {
				mine = append(mine, a)
				tags = append(tags, apiLangs(a)...)
			}
		}
	}
	if len(mine) == 0 {
		return nil
	}
	pick := func(keep func(storedAPI) bool) []storedAPI {
		var out []storedAPI
		for _, a := range mine {
			if keep(a) {
				out = append(out, a)
			}
		}
		return out
	}
	if m, ok := langtag.Match(lang, tags); ok {
		return pick(func(a storedAPI) bool { return slices.Contains(apiLangs(a), m) })
	}
	// Those that take every language; never one made for another language.
	return pick(func(a storedAPI) bool { return a.Lang == "" })
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
	a.Builtin, a.Languages, a.Texts, a.AnyContentType = false, nil, nil, false // only the program makes a built-in interface
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
		if k != "default" && k != "hosts" && k != "languages" && k != "allow" && k != "notices" {
			bad(c, "unknown key "+k+": the settings are default, languages and allow (interfaces have their own requests)")
			return
		}
	}
	// The notices are dismissed with an empty list; they are not written.
	dismiss := false
	if raw, ok := req["notices"]; ok {
		var given []json.RawMessage
		if err := json.Unmarshal(raw, &given); err != nil || given == nil || len(given) != 0 {
			bad(c, "notices is [] (to dismiss them)")
			return
		}
		dismiss = true
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
		if dismiss {
			site.Notices = nil
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
	// An interface of the administrators' own with a built-in's id (kept from
	// 2.3, see adoptBuiltins) is changed as theirs; else the id is the built-in's.
	p, builtin := dailyfetch.ProviderByID(id)
	if builtin {
		site, err := s.loadSite(c.r.Context())
		if err != nil {
			s.fail(c.w, err)
			return
		}
		if !slices.ContainsFunc(site.APIs, func(a storedAPI) bool { return a.ID == id }) {
			s.putBuiltinFromAPI(c, p, b)
			return
		}
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
		} else if builtin {
			return &codedError{code: codeBuiltin, msg: "the interface " + id + " is a built-in one"}
		} else if len(site.APIs) >= dailyfetch.MaxAPIs {
			return &space.InvalidError{Msg: "at most 50 interfaces"}
		}
		made, _, err := s.build(b, old)
		if err != nil {
			return err
		}
		if old != nil {
			made.KeepsID = old.KeepsID
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
	def, _ := p.WithOptions(nil)
	same := func(field, got, want string) error {
		if got != "" && got != want && !(field == "url" && got == def.URL) {
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
	err := s.editSite(c.r.Context(), func(_ store.Q, site *siteDaily) error {
		// The administrators' own first: one with a built-in's id (kept from
		// 2.3, see adoptBuiltins) is deleted like any other.
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
			if _, ok := dailyfetch.ProviderByID(id); ok {
				return &codedError{code: codeBuiltin, msg: "a built-in interface cannot be deleted; switch it off"}
			}
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
	Inherit bool     `json:"inherit"`
	APIs    []string `json:"apis"`
	// Unavailable says, by interface id, what is the matter with an interface the
	// space chose: "disabled" (switched off: kept, marked and skipped) or "gone"
	// (it no longer exists: left out of the answer).
	Unavailable map[string]string `json:"unavailable,omitempty"`
	// Sources are the space's own lists of sources by language, when it has
	// them (see putDailyPrefs); interfaces that are not set up are left out.
	Sources sourceLists `json:"sources,omitempty"`
	// Options are the options chosen for the built-in interfaces of apis, by
	// interface id and option key; absent is all.
	Options   map[string]map[string][]string `json:"options"`
	Available []availAPI                     `json:"available"`
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
	// Languages are those of a built-in interface that answers in more than
	// its first (see dailyfetch.API.Languages).
	Languages []string `json:"languages,omitempty"`
	// Options are what a reader may choose of a built-in interface.
	Options []dailyfetch.Option `json:"options,omitempty"`
}

func (s *server) dailyPrefs(c *call) (dailyPrefsResp, error) {
	ctx := c.r.Context()
	out := dailyPrefsResp{APIs: []string{}, Options: map[string]map[string][]string{}, Available: []availAPI{}}
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
	choice := store.ParseSpaceChoice(prefs.APIs)
	known := map[string]bool{}
	for _, a := range site.all() {
		if !a.Enabled {
			continue
		}
		known[a.ID] = true
		var opts []dailyfetch.Option
		if p, ok := dailyfetch.ProviderByID(a.ID); ok && a.Builtin {
			opts = p.Options
		}
		out.Available = append(out.Available, availAPI{a.ID, a.Name, a.Lang, a.Languages, opts})
	}
	if len(choice.Sources) > 0 {
		var lists sourceLists
		if json.Unmarshal(choice.Sources, &lists) == nil {
			out.Sources = sourceLists{}
			for l, list := range lists {
				kept := []dailySource{}
				for _, src := range list {
					src.Available, src.Reason = nil, ""
					if src.Kind == "api" && !known[src.ID] {
						switch site.ifaceStatus(src.ID) {
						case "gone":
							out.markUnavailable(src.ID, "gone")
							continue
						default:
							no := false
							src.Available, src.Reason = &no, "disabled"
							out.markUnavailable(src.ID, "disabled")
						}
					}
					kept = append(kept, src)
				}
				out.Sources[l] = kept
			}
		}
	}
	for _, id := range choice.APIs {
		switch {
		case known[id]:
		case site.ifaceStatus(id) == "gone":
			out.markUnavailable(id, "gone")
			continue
		default:
			out.markUnavailable(id, "disabled")
		}
		out.APIs = append(out.APIs, id)
		if o, ok := choice.Options[id]; ok {
			out.Options[id] = o
		}
	}
	if site.Default != nil {
		out.Default = &struct {
			Kind string `json:"kind"`
		}{site.Default.Kind}
	}
	return out, nil
}

func (r *dailyPrefsResp) markUnavailable(id, why string) {
	if r.Unavailable == nil {
		r.Unavailable = map[string]string{}
	}
	r.Unavailable[id] = why
}

// ifaceStatus is how an interface stands: "" when it is set up and enabled,
// "disabled" when it exists and is switched off, "gone" when there is none.
func (d *siteDaily) ifaceStatus(id string) string {
	for _, a := range d.APIs {
		if a.ID == id {
			if a.Enabled {
				return ""
			}
			return "disabled"
		}
	}
	if _, ok := dailyfetch.ProviderByID(id); ok {
		if d.Providers[id].Enabled {
			return ""
		}
		return "disabled"
	}
	return "gone"
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
		Inherit *bool                          `json:"inherit"`
		APIs    *[]string                      `json:"apis"`
		Options map[string]map[string][]string `json:"options"`
		Sources sourceLists                    `json:"sources"`
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
	// What the space already chose and the administrators switched off since stays
	// as it is (the page sends it back); it cannot be newly chosen.
	prefs, err := store.GetSpaceDaily(ctx, s.opt.Store.Q(), c.sp.Info.ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	site = site.withKept(store.ParseSpaceChoice(prefs.APIs))
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
	opts, err := checkOptions(&site, ids, req.Options)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	choice := store.SpaceChoice{APIs: ids, Options: opts}
	if len(req.Sources) > 0 {
		checked, err := s.checkSpaceSources(c, req.Sources, &site)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		choice.Sources, _ = json.Marshal(checked)
	}
	if err := store.SetSpaceDaily(ctx, s.opt.Store.Q(), c.sp.Info.ID, store.SpaceDaily{Inherit: *req.Inherit, APIs: choice.JSON()}); err != nil {
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

// spaceSources are the sources of a space's own list for a reader's language,
// in order, without those the administrators do not let spaces use (an
// interface while `allow.api` is off, the space's own pages while `allow.own`
// is) or that are not set up or are switched off; they are kept and not used.
func (s *server) spaceSources(choice store.SpaceChoice, all []storedAPI, allow dailyAllow, lang string) (out []dailySource, lost bool) {
	if len(choice.Sources) == 0 {
		return nil, false
	}
	var lists sourceLists
	if json.Unmarshal(choice.Sources, &lists) != nil {
		return nil, false
	}
	d := siteDefault{Kind: "sources", Sources: lists}
	list, ok := d.sourcesFor(lang)
	if !ok {
		return nil, false
	}
	for _, src := range list {
		switch src.Kind {
		case "api":
			if !allow.API {
				continue // locked: kept and not used, the way it always was
			}
			if !slices.ContainsFunc(all, func(a storedAPI) bool { return a.ID == src.ID && a.Enabled }) {
				lost = true
				continue
			}
		case "own":
			if !allow.Own {
				continue
			}
		}
		out = append(out, src)
	}
	return out, lost
}

// checkSpaceSources checks the sources a space's owner lists: interfaces that
// are set up and enabled (with their options), the administrators' lines, and
// pages of this space, as a folder, tag or type with filters.
func (s *server) checkSpaceSources(c *call, in sourceLists, site *siteDaily) (sourceLists, error) {
	if len(in) > 31 {
		return nil, &space.InvalidError{Msg: "at most 30 languages"}
	}
	out := make(sourceLists, len(in))
	for l, list := range in {
		if l != "" {
			cl, ok := langtag.Canonical(l)
			if !ok {
				return nil, badLangError(l)
			}
			if _, dup := out[cl]; dup {
				return nil, &space.InvalidError{Msg: fmt.Sprintf("%s is given twice", cl)}
			}
			l = cl
		}
		if len(list) > maxAPIsPerLanguage {
			return nil, &space.InvalidError{Msg: fmt.Sprintf("each language has at most %d sources", maxAPIsPerLanguage)}
		}
		checked := make([]dailySource, 0, len(list))
		for _, src := range list {
			src.Available, src.Reason = nil, "" // marks of an answer, sent back
			switch src.Kind {
			case "api":
				if src.Space != "" || src.Source != nil {
					return nil, &space.InvalidError{Msg: "a source that is an interface has an id and options only"}
				}
				api := slices.IndexFunc(site.all(), func(a storedAPI) bool { return a.ID == src.ID && a.Enabled })
				if api < 0 {
					return nil, &space.InvalidError{Msg: "there is no interface " + src.ID + "; only those the administrators set up and enabled can be used"}
				}
				if len(src.Options) > 0 {
					p, ok := dailyfetch.ProviderByID(src.ID)
					if !ok || !site.all()[api].Builtin {
						return nil, &space.InvalidError{Msg: "options are for built-in interfaces; " + src.ID + " is not one"}
					}
					canon, err := p.CheckOptions(src.Options)
					if err != nil {
						return nil, &space.InvalidError{Msg: err.Error()}
					}
					src.Options = canon
				}
			case "list":
				if src.ID != "" || src.Options != nil || src.Space != "" || src.Source != nil {
					return nil, &space.InvalidError{Msg: "a source that is the lines has nothing else"}
				}
			case "own":
				if src.ID != "" || src.Options != nil || src.Space != "" || src.Source == nil {
					return nil, &space.InvalidError{Msg: "a source that is pages of this space needs a source (folder, tag or type)"}
				}
				cfg, err := c.sp.Space.CheckDailyConfig(*src.Source)
				if err != nil {
					return nil, err
				}
				src.Source = &cfg
			default:
				return nil, &space.InvalidError{Msg: "kind of a source is api, list or own"}
			}
			checked = append(checked, src)
		}
		out[l] = checked
	}
	return out, nil
}

// withKept is the settings with the interfaces a space chose earlier and that
// are switched off counted as on, for checking what the space sends back (a
// copy: nothing is saved of it).
func (d siteDaily) withKept(choice store.SpaceChoice) siteDaily {
	ids := map[string]bool{}
	for _, id := range choice.APIs {
		ids[id] = true
	}
	if len(choice.Sources) > 0 {
		var lists sourceLists
		if json.Unmarshal(choice.Sources, &lists) == nil {
			for _, list := range lists {
				for _, src := range list {
					if src.Kind == "api" {
						ids[src.ID] = true
					}
				}
			}
		}
	}
	d.APIs = slices.Clone(d.APIs)
	d.Providers = maps.Clone(d.Providers)
	if d.Providers == nil {
		d.Providers = map[string]providerState{}
	}
	for id := range ids {
		if d.ifaceStatus(id) != "disabled" {
			continue
		}
		if i := slices.IndexFunc(d.APIs, func(a storedAPI) bool { return a.ID == id }); i >= 0 {
			d.APIs[i].Enabled = true
		} else {
			st := d.Providers[id]
			st.Enabled = true
			d.Providers[id] = st
		}
	}
	return d
}
