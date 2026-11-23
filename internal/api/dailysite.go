package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KKazuhaK/PetalBook/internal/dailyfetch"
	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// What the administrators of a server give the daily text: a default for the
// spaces that have no source of their own, and external interfaces the spaces
// may choose. They are in the state database (site_settings and daily_lines),
// and are not part of any space.

const siteDailyKey = "daily"

type siteDefault struct {
	// Kind is "space" (the source is a folder, tag or type of that space) or
	// "list" (the lines an administrator pasted or uploaded).
	Kind   string             `json:"kind"`
	Space  string             `json:"space,omitempty"`
	Source *space.DailyConfig `json:"source,omitempty"`
}

type siteDaily struct {
	Default *siteDefault     `json:"default"`
	Hosts   []string         `json:"hosts"`
	APIs    []dailyfetch.API `json:"apis"`
}

func (d *siteDaily) fill() {
	if d.Hosts == nil {
		d.Hosts = []string{}
	}
	if d.APIs == nil {
		d.APIs = []dailyfetch.API{}
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

type dailyQuote struct {
	Text   string `json:"text"`
	Source string `json:"source,omitempty"`
	Author string `json:"author,omitempty"`
	origin string
}

// siteDaily is what the administrators' settings give for a space with no
// source of its own: the external interface the space chose, else the default
// when the space takes it. A nil quote with a reason is a block that does not
// show; with none, there is nothing set.
func (s *server) siteDaily(c *call, date string, skip int, lang string) (*dailyQuote, string, error) {
	ctx := c.r.Context()
	prefs, err := store.GetSpaceDaily(ctx, s.opt.Store.Q(), c.sp.Info.ID)
	if err != nil {
		return nil, "", err
	}
	site, err := s.loadSite(ctx)
	if err != nil {
		return nil, "", err
	}
	var ids []string
	json.Unmarshal([]byte(prefs.APIs), &ids)
	if a := chooseAPI(site.APIs, ids, lang); a != nil {
		q, err := s.opt.DailyFetch.Get(ctx, *a, site.Hosts, lang, date, skip)
		switch {
		case errors.Is(err, dailyfetch.ErrRateLimited):
			return nil, "", err
		case err != nil:
			return nil, "unavailable", nil
		}
		return &dailyQuote{Text: q.Text, Source: q.Source, Author: q.Author, origin: "external"}, "", nil
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

// chooseAPI picks, among the interfaces a space chose, the one for the
// language: made for it, else one that takes any language, else any.
func chooseAPI(all []dailyfetch.API, ids []string, lang string) *dailyfetch.API {
	chosen := map[string]bool{}
	for _, id := range ids {
		chosen[id] = true
	}
	var mine []dailyfetch.API
	for _, a := range all {
		if chosen[a.ID] {
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
	for _, want := range []func(a dailyfetch.API) bool{
		func(a dailyfetch.API) bool { return a.Lang != "" && a.Lang == lang },
		func(a dailyfetch.API) bool { return a.Lang != "" && a.Lang == base },
		func(a dailyfetch.API) bool { return a.Lang == "" },
		func(dailyfetch.API) bool { return true },
	} {
		for i := range mine {
			if want(mine[i]) {
				return &mine[i]
			}
		}
	}
	return nil
}

// dailyFromLines picks one of the lines the administrators gave: those in the
// language, else those in its base language, else those for any language, else
// the first language that has some.
func (s *server) dailyFromLines(ctx context.Context, date string, skip int, lang string) (*dailyQuote, string, error) {
	q := s.opt.Store.Q()
	counts, err := store.DailyLineCounts(ctx, q)
	if err != nil {
		return nil, "", err
	}
	base := lang
	if i := strings.IndexByte(lang, '-'); i > 0 {
		base = lang[:i]
	}
	use, found := "", false
	for _, l := range []string{lang, base, ""} {
		if counts[l] > 0 {
			use, found = l, true
			break
		}
	}
	if !found {
		var langs []string
		for l, n := range counts {
			if n > 0 {
				langs = append(langs, l)
			}
		}
		if len(langs) == 0 {
			return nil, "", nil
		}
		sort.Strings(langs)
		use = langs[0]
	}
	n := int((space.DailyNumber("site:"+use, date) + uint64(skip)) % uint64(counts[use]))
	line, err := store.NthDailyLine(ctx, q, use, n)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "no_match", nil // the lines changed while this was read
	}
	if err != nil {
		return nil, "", err
	}
	return &dailyQuote{Text: line.Body, Source: line.Source, origin: "site"}, "", nil
}

// dailyFromSpace picks from a space the administrators named. What it shows is
// shown to everybody, whether or not they are members of that space.
func (s *server) dailyFromSpace(ctx context.Context, d siteDefault, date string, skip int, lang string) (*dailyQuote, string, error) {
	sp, release, err := s.spaces.Acquire(ctx, d.Space)
	if errors.Is(err, spaces.ErrNotFound) || errors.Is(err, spaces.ErrDisabled) {
		return nil, "unavailable", nil
	}
	if err != nil {
		return nil, "", err
	}
	defer release()
	src := d.Source.For(lang)
	if src == nil {
		return nil, "", nil
	}
	pick, _, err := sp.DailyFrom("site:"+d.Space, date, skip, *src)
	if err != nil {
		return nil, "", err
	}
	if pick == nil {
		return nil, "no_match", nil
	}
	title := pick.Page.Title
	return &dailyQuote{Text: pick.Body, Source: title, origin: "site"}, "", nil
}

// --- the administrators' side ------------------------------------------------------

type dailyLinesInfo struct {
	Total  int            `json:"total"`
	ByLang map[string]int `json:"byLang"`
	Max    int            `json:"max"`
}

type adminDailyResp struct {
	siteDaily
	Lines dailyLinesInfo `json:"lines"`
}

func (s *server) adminDailyView(ctx context.Context) (adminDailyResp, error) {
	site, err := s.loadSite(ctx)
	if err != nil {
		return adminDailyResp{}, err
	}
	counts, err := store.DailyLineCounts(ctx, s.opt.Store.Q())
	if err != nil {
		return adminDailyResp{}, err
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	return adminDailyResp{site, dailyLinesInfo{total, counts, store.MaxDailyLines}}, nil
}

func (s *server) adminGetDaily(c *call) {
	out, err := s.adminDailyView(c.r.Context())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, out)
}

func bad(c *call, msg string) { writeError(c.w, http.StatusBadRequest, "invalid", msg) }

// adminPutDaily changes the default, the hosts and the interfaces. What the
// request leaves out stays as it is.
func (s *server) adminPutDaily(c *call) {
	var req map[string]json.RawMessage
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	for k := range req {
		if k != "default" && k != "hosts" && k != "apis" {
			bad(c, "unknown key "+k+": the settings are default, hosts and apis")
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
			bad(c, "at most 50 hosts")
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
	// The interfaces are checked against the hosts as they will be.
	if raw, ok := req["apis"]; ok {
		var apis []dailyfetch.API
		if err := json.Unmarshal(raw, &apis); err != nil || apis == nil {
			bad(c, "apis is a list of interfaces")
			return
		}
		site.APIs = apis
	}
	if len(site.APIs) > dailyfetch.MaxAPIs {
		bad(c, "at most 20 interfaces")
		return
	}
	seen := map[string]bool{}
	for i, a := range site.APIs {
		a, err := dailyfetch.CheckAPI(a, site.Hosts, s.opt.DailyFetch.AllowHTTP())
		if err != nil {
			bad(c, "interface "+a.ID+": "+err.Error())
			return
		}
		if seen[a.ID] {
			bad(c, "the interface id "+a.ID+" is used twice")
			return
		}
		seen[a.ID] = true
		site.APIs[i] = a
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
	site.fill()
	b, _ := json.Marshal(site)
	if err := store.SetSiteSetting(ctx, s.opt.Store.Q(), siteDailyKey, string(b), time.Now()); err != nil {
		s.fail(c.w, err)
		return
	}
	s.adminAnswerDaily(c)
}

func (s *server) adminAnswerDaily(c *call) {
	out, err := s.adminDailyView(c.r.Context())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, out)
}

func (s *server) checkSiteDefault(ctx context.Context, d *siteDefault) error {
	switch d.Kind {
	case "list":
		if d.Space != "" || d.Source != nil {
			return &space.InvalidError{Msg: "a default that is a list has no space or source"}
		}
	case "space":
		if !page.ValidID(d.Space) || d.Source == nil {
			return &space.InvalidError{Msg: "a default that is a space needs the space and a source (folder, tag or type)"}
		}
		if _, err := store.GetSpace(ctx, s.opt.Store.Q(), d.Space); errors.Is(err, store.ErrNotFound) {
			return &space.InvalidError{Msg: "there is no such space"}
		} else if err != nil {
			return err
		}
		sp, release, err := s.spaces.Acquire(ctx, d.Space)
		if errors.Is(err, spaces.ErrNotFound) || errors.Is(err, spaces.ErrDisabled) {
			return &space.InvalidError{Msg: "there is no such space, or it is disabled"}
		}
		if err != nil {
			return err
		}
		defer release()
		checked, err := sp.CheckDailyConfig(*d.Source)
		if err != nil {
			return err
		}
		d.Source = &checked
	default:
		return &space.InvalidError{Msg: "kind is space or list"}
	}
	return nil
}

const maxLinesBody = 2 << 20

// adminPutDailyLines replaces the lines of one language.
func (s *server) adminPutDailyLines(c *call) {
	var req struct {
		Lang   string `json:"lang"`
		Text   string `json:"text"`
		Format string `json:"format"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, maxLinesBody) {
		return
	}
	lang := space.NormalLang(req.Lang)
	if lang != "" && !space.ValidLang(lang) {
		bad(c, "lang is a language code like zh or en, or empty for any")
		return
	}
	var texts []string
	switch req.Format {
	case "", "lines":
		texts = splitLines(req.Text)
	case "markdown":
		texts = splitMarkdown(req.Text)
	default:
		bad(c, "format is lines or markdown")
		return
	}
	if len(texts) == 0 {
		bad(c, "there is no text in it; to take the lines away, delete them")
		return
	}
	lines := make([]store.DailyLine, len(texts))
	for i, t := range texts {
		body, source, _ := strings.Cut(t, "\t")
		body, source = strings.TrimSpace(body), strings.TrimSpace(source)
		if len(body) > store.MaxDailyLineBytes || utf8.RuneCountInString(source) > 200 || strings.ContainsRune(t, 0) {
			bad(c, "an entry is too long (at most 8000 bytes, its source 200 characters) or has a null character: "+firstWords(body))
			return
		}
		lines[i] = store.DailyLine{Lang: lang, Body: body, Source: source}
	}
	ctx := c.r.Context()
	err := s.opt.Store.Tx(ctx, func(q store.Q) error {
		_, err := store.ReplaceDailyLines(ctx, q, lang, lines)
		return err
	})
	if errors.Is(err, store.ErrTooManyLines) {
		bad(c, "the server keeps at most 5000 lines in all")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.adminAnswerDaily(c)
}

func firstWords(s string) string {
	r := []rune(s)
	if len(r) > 20 {
		r = r[:20]
	}
	return string(r)
}

// splitLines is one entry for each line that is not blank; a tab in a line
// separates the text from where it is from.
func splitLines(text string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// splitMarkdown cuts a file into entries at the lines that are `---`; a file
// with none is cut at its blank lines.
func splitMarkdown(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var parts []string
	hasRule := false
	var cur []string
	flush := func() {
		if t := strings.TrimSpace(strings.Join(cur, "\n")); t != "" {
			parts = append(parts, t)
		}
		cur = nil
	}
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == "---" {
			hasRule = true
			flush()
			continue
		}
		cur = append(cur, l)
	}
	flush()
	if hasRule {
		return parts
	}
	parts = nil
	cur = nil
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == "" {
			flush()
			continue
		}
		cur = append(cur, l)
	}
	flush()
	return parts
}

// adminDeleteDailyLines removes the lines of one language (`?lang=`, empty for those for any).
func (s *server) adminDeleteDailyLines(c *call) {
	lang := space.NormalLang(c.r.URL.Query().Get("lang"))
	if lang != "" && !space.ValidLang(lang) {
		bad(c, "lang is a language code like zh or en, or empty for any")
		return
	}
	if err := store.DeleteDailyLines(c.r.Context(), s.opt.Store.Q(), lang); err != nil {
		s.fail(c.w, err)
		return
	}
	s.adminAnswerDaily(c)
}

// adminTestDailyAPI asks an interface now and says what came back.
func (s *server) adminTestDailyAPI(c *call) {
	var req struct {
		Lang string `json:"lang"`
	}
	if !s.readJSON(c.w, c.r, &req, true) {
		return
	}
	lang := space.NormalLang(req.Lang)
	if lang != "" && !space.ValidLang(lang) {
		bad(c, "lang is a language code like zh or en")
		return
	}
	site, err := s.loadSite(c.r.Context())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	id := c.r.PathValue("id")
	for _, a := range site.APIs {
		if a.ID != id {
			continue
		}
		q, err := s.opt.DailyFetch.Try(c.r.Context(), a, site.Hosts, lang)
		if err != nil {
			writeJSON(c.w, http.StatusOK, struct {
				OK      bool   `json:"ok"`
				Message string `json:"message"`
			}{false, err.Error()})
			return
		}
		writeJSON(c.w, http.StatusOK, struct {
			OK    bool             `json:"ok"`
			Quote dailyfetch.Quote `json:"quote"`
		}{true, q})
		return
	}
	writeError(c.w, http.StatusNotFound, "not_found", "no such interface")
}

// --- what the owner of a space chooses ---------------------------------------------

type dailyPrefsResp struct {
	Inherit   bool       `json:"inherit"`
	APIs      []string   `json:"apis"`
	Available []availAPI `json:"available"`
	Default   *struct {
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
	var ids []string
	json.Unmarshal([]byte(prefs.APIs), &ids)
	known := map[string]bool{}
	for _, a := range site.APIs {
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
		known[a.ID] = true
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, id := range *req.APIs {
		if !known[id] {
			bad(c, "there is no interface "+id+"; only those the administrators set up can be used")
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
