package api

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"petalbook.app/server/internal/dailyfetch"
	"petalbook.app/server/internal/langtag"
	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/space"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
)

// What the administrators of a server give the daily text: a default for the
// spaces that have no source of their own, and external interfaces the spaces
// may choose. They are in the state database (site_settings and daily_lines),
// and are not part of any space.

const siteDailyKey = "daily"

type siteDefault struct {
	// Kind is "space" (the source is a folder, tag or type of that space),
	// "list" (the lines an administrator pasted or uploaded) or "api" (an
	// interface for each language).
	Kind   string             `json:"kind"`
	Space  string             `json:"space,omitempty"`
	Source *space.DailyConfig `json:"source,omitempty"`
	// APIs are the ids of the interfaces by language, in order, "" for the
	// languages not listed.
	APIs apiLists `json:"apis,omitempty"`
	// Options are the options chosen for built-in interfaces of the lists, by
	// interface id and option key; absent is all.
	Options map[string]map[string][]string `json:"options,omitempty"`
	// Sources are, for kind "sources", the sources of each language in order,
	// of any of the other kinds: "" for the languages not listed.
	Sources sourceLists `json:"sources,omitempty"`
}

// dailySource is one source of the daily text: an external interface (with
// the options chosen for it), the lines the administrators gave, or pages of a
// space. A list of them is tried in turn (see dailyFromSources).
type dailySource struct {
	Kind    string              `json:"kind"`
	ID      string              `json:"id,omitempty"`
	Options map[string][]string `json:"options,omitempty"`
	Space   string              `json:"space,omitempty"`
	Source  *space.DailyConfig  `json:"source,omitempty"`
	// Available and Reason are only in an answer (a space's prefs): an interface
	// that is switched off stays in the list, marked, and is skipped when read.
	// They are not read from a request.
	Available *bool  `json:"available,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// sourceLists are the sources of each language of a default.
type sourceLists map[string][]dailySource

// UnmarshalJSON reads the list of each language; a source's keys that are not
// known are refused.
func (m *sourceLists) UnmarshalJSON(b []byte) error {
	var raw map[string][]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw == nil {
		*m = nil
		return nil
	}
	out := make(sourceLists, len(raw))
	for k, list := range raw {
		out[k] = []dailySource{}
		for _, e := range list {
			var src dailySource
			dec := json.NewDecoder(bytes.NewReader(e))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&src); err != nil {
				return fmt.Errorf("a source of %q is {\"kind\": \"api\", \"id\"}, {\"kind\": \"list\"} or {\"kind\": \"space\", \"space\", \"source\"}: %v", k, err)
			}
			out[k] = append(out[k], src)
		}
	}
	*m = out
	return nil
}

// sourcesFor are the sources of a default for a reader's language, in order,
// whatever the kind of the default: a "list" or "space" default is a list of
// one, an "api" one a list of interfaces; false when it has none for the
// language.
func (d *siteDefault) sourcesFor(lang string) ([]dailySource, bool) {
	switch d.Kind {
	case "list":
		return []dailySource{{Kind: "list"}}, true
	case "space":
		return []dailySource{{Kind: "space", Space: d.Space, Source: d.Source}}, true
	case "api":
		ids, ok := defaultAPIsFor(d.APIs, lang)
		if !ok {
			return nil, false
		}
		out := make([]dailySource, len(ids))
		for i, id := range ids {
			out[i] = dailySource{Kind: "api", ID: id, Options: d.Options[id]}
		}
		return out, true
	case "sources":
		keys := make([]string, 0, len(d.Sources))
		for k := range d.Sources {
			if k != "" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		if m, ok := langtag.Match(lang, keys); ok {
			return d.Sources[m], len(d.Sources[m]) > 0
		}
		list, ok := d.Sources[""]
		return list, ok && len(list) > 0
	}
	return nil, false
}

// maxAPIsPerLanguage is how many interfaces one language of a default or of a
// space's choice may list.
const maxAPIsPerLanguage = 8

// apiLists are the interface ids of each language of a default.
type apiLists map[string][]string

type dailyQuote struct {
	Text   string `json:"text"`
	Source string `json:"source,omitempty"`
	Author string `json:"author,omitempty"`
	Link   string `json:"link,omitempty"`
	// Fetched and Stale are set for an external interface: when the text was
	// got, and that it is the last thing the interface said because it could
	// not be asked now.
	Fetched string `json:"fetched,omitempty"`
	Stale   bool   `json:"stale,omitempty"`
	origin  string
}

// dailyFromLines picks one of the lines the administrators gave: those in the
// language the reader's is matched to, else those for any language; never those
// of another language.
func (s *server) dailyFromLines(ctx context.Context, date string, skip int, lang string) (*dailyQuote, string, error) {
	q := s.opt.Store.Q()
	counts, err := store.DailyLineCounts(ctx, q)
	if err != nil {
		return nil, "", err
	}
	var langs []string
	for l := range counts {
		if l != "" {
			langs = append(langs, l)
		}
	}
	sort.Strings(langs)
	use := ""
	if m, ok := langtag.Match(lang, langs); ok {
		use = m
	} else if counts[""] == 0 {
		return nil, "", nil
	}
	n := int((space.DailyNumber("site:"+use, date) + uint64(skip)) % uint64(counts[use]))
	line, err := store.NthDailyLine(ctx, q, use, n)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "no_match", nil // the lines changed while this was read
	}
	if err != nil {
		return nil, "", err
	}
	return &dailyQuote{Text: line.Body, Author: line.Author, Source: line.Source, origin: "site"}, "", nil
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
	// What the page says it is from (its source, its author), else its title.
	source := joinSources(pick.Page.Sources, lang)
	if source == "" {
		source = pick.Page.Title
	}
	return &dailyQuote{Text: pick.Body, Author: pick.Author, Source: source, origin: "site"}, "", nil
}

// --- the administrators' side ------------------------------------------------------

type dailyLinesInfo struct {
	Total  int            `json:"total"`
	ByLang map[string]int `json:"byLang"`
	Max    int            `json:"max"`
}

// providerView is a built-in interface as the administrators see it.
type providerView struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Languages []string `json:"languages"`
	Site      string   `json:"site"`
	Note      string   `json:"note"`
	Enabled   bool     `json:"enabled"`
	Refresh   string   `json:"refresh"`
	Minutes   int      `json:"minutes"`
	RPS       float64  `json:"rps"`
	MaxRPS    float64  `json:"maxRps"`
	SkipNew   bool     `json:"skipNew"`
	// Options are what a reader may choose of the service (see dailyfetch.Option).
	Options []dailyfetch.Option `json:"options"`
}

type adminDailyResp struct {
	Default        *siteDefault       `json:"default"`
	Languages      []string           `json:"languages"`
	KnownLanguages []langtag.Language `json:"knownLanguages"`
	Allow          dailyAllow         `json:"allow"`
	APIs           []apiView          `json:"apis"`
	Providers      []providerView     `json:"providers"`
	Lines          dailyLinesInfo     `json:"lines"`
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
	all := site.all()
	apis := make([]apiView, len(all))
	for i, a := range all {
		apis[i] = viewOfAPI(a)
	}
	providers := make([]providerView, 0, 8)
	for _, p := range dailyfetch.Providers() {
		st := site.Providers[p.ID]
		b := builtinAPI(p, st)
		providers = append(providers, providerView{p.ID, p.Name, p.Languages, p.Site, p.Note, st.Enabled, b.Refresh, b.RefreshMinutes, b.RPS, p.RPS, b.SkipNew, optionsOf(p)})
	}
	return adminDailyResp{site.Default, site.Languages, langtag.Known(), *site.Allow, apis, providers, dailyLinesInfo{total, counts, store.MaxDailyLines}}, nil
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

func (s *server) adminAnswerDaily(c *call) {
	out, err := s.adminDailyView(c.r.Context())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, out)
}

// checkDefaultAPIs checks a default that is an interface for each language:
// the languages are tags (written in canonical form), the ids are those of
// interfaces that are set up.
func checkDefaultAPIs(d *siteDefault, site *siteDaily) error {
	if d.Space != "" || d.Source != nil || len(d.APIs) == 0 {
		return &space.InvalidError{Msg: "A default that is an interface needs apis, the id of an interface for each language (\"\" for all the others), and no space or source."}
	}
	if len(d.APIs) > 31 {
		return &space.InvalidError{Msg: "At most 30 languages."}
	}
	known := map[string]bool{}
	for _, a := range site.all() {
		known[a.ID] = true
	}
	out := make(apiLists, len(d.APIs))
	used := map[string]bool{}
	for l, ids := range d.APIs {
		if l != "" {
			cl, ok := langtag.Canonical(l)
			if !ok {
				return badLangError(l)
			}
			if _, dup := out[cl]; dup {
				return &space.InvalidError{Msg: fmt.Sprintf("%s is given twice.", cl)}
			}
			l = cl
		}
		if len(ids) == 0 || len(ids) > maxAPIsPerLanguage {
			return &space.InvalidError{Msg: fmt.Sprintf("Each language has 1 to %d interfaces.", maxAPIsPerLanguage)}
		}
		list := make([]string, 0, len(ids))
		for _, id := range ids {
			if !known[id] {
				return &codedError{code: codeAPINotFound, msg: fmt.Sprintf("There is no interface %q; choose one of the interfaces that are set up.", id)}
			}
			if err := site.checkServes(id, l); err != nil {
				return err
			}
			if !slices.Contains(list, id) {
				list = append(list, id)
			}
			used[id] = true
		}
		out[l] = list
	}
	opts, err := checkOptions(site, slices.Collect(maps.Keys(used)), d.Options)
	if err != nil {
		return err
	}
	d.APIs, d.Options = out, opts
	return nil
}

// optionsOf is the options of a provider, never nil.
func optionsOf(p dailyfetch.Provider) []dailyfetch.Option {
	if p.Options == nil {
		return []dailyfetch.Option{}
	}
	return p.Options
}

// checkOptions checks the options chosen for the interfaces in use (ids): only
// built-in interfaces that are in use have options, with the values they
// declare. The result is in canonical form (see dailyfetch.Provider.CheckOptions).
func checkOptions(site *siteDaily, ids []string, given map[string]map[string][]string) (map[string]map[string][]string, error) {
	var out map[string]map[string][]string
	for id, chosen := range given {
		p, ok := dailyfetch.ProviderByID(id)
		builtin := slices.ContainsFunc(site.all(), func(a storedAPI) bool { return a.ID == id && a.Builtin })
		if !ok || !builtin || !slices.Contains(ids, id) {
			return nil, &space.InvalidError{Msg: fmt.Sprintf("Options are for the built-in interfaces that are chosen; %q is not one.", id)}
		}
		canon, err := p.CheckOptions(chosen)
		if err != nil {
			return nil, &space.InvalidError{Msg: err.Error()}
		}
		if len(canon) == 0 {
			continue
		}
		if out == nil {
			out = map[string]map[string][]string{}
		}
		out[id] = canon
	}
	return out, nil
}

// checkSiteDefault checks a default against the interfaces that are set up
// (site) and the spaces there are; apis are written as canonical tags.
func (s *server) checkSiteDefault(ctx context.Context, d *siteDefault, site *siteDaily) error {
	if d.Kind != "api" && (d.APIs != nil || (d.Options != nil && d.Kind != "sources")) {
		return &space.InvalidError{Msg: "Only a default that is an interface has apis."}
	}
	if d.Kind != "sources" && d.Sources != nil {
		return &space.InvalidError{Msg: "Only a default that is sources has sources."}
	}
	switch d.Kind {
	case "list":
		if d.Space != "" || d.Source != nil {
			return &space.InvalidError{Msg: "A default that is a list has no space or source."}
		}
	case "api":
		return checkDefaultAPIs(d, site)
	case "space":
		if !page.ValidID(d.Space) || d.Source == nil {
			return &space.InvalidError{Msg: "A default that is a space needs the space and a source (folder, tag or type)."}
		}
		checked, err := s.checkSpaceSource(ctx, d.Space, *d.Source)
		if err != nil {
			return err
		}
		d.Source = &checked
	case "sources":
		return s.checkDefaultSources(ctx, d, site)
	default:
		return &space.InvalidError{Msg: "Kind is space, list, api or sources."}
	}
	return nil
}

const maxLinesBody = 2 << 20

// lineOut is a line of the administrators' own text as the API gives and takes it.
type lineOut struct {
	Text   string `json:"text"`
	Author string `json:"author,omitempty"`
	Source string `json:"source,omitempty"`
}

// parseLines reads the text of an upload or a paste in a format into lines,
// each with its author and source split off: `text —— Author《Source》` (see
// parseAttribution), a tab for a source of its own, or the columns of a CSV.
func parseLines(text, format string) ([]lineOut, error) {
	var out []lineOut
	switch format {
	case "", "lines", "markdown":
		var texts []string
		if format == "markdown" {
			texts = splitMarkdown(text)
		} else {
			texts = splitLines(text)
		}
		for _, t := range texts {
			body, tabSource, _ := strings.Cut(t, "\t")
			a := parseAttribution(strings.TrimSpace(body))
			l := lineOut{Text: a.Text, Author: a.Author, Source: a.Source}
			if ts := strings.TrimSpace(tabSource); ts != "" {
				l.Source = ts
			}
			out = append(out, l)
		}
	case "csv":
		r := csv.NewReader(strings.NewReader(strings.TrimPrefix(text, "\ufeff")))
		r.FieldsPerRecord = -1
		r.LazyQuotes = true
		rows, err := r.ReadAll()
		if err != nil {
			return nil, fmt.Errorf("the CSV cannot be read: %v", err)
		}
		for i, row := range rows {
			cell := func(n int) string {
				if n < len(row) {
					return strings.TrimSpace(row[n])
				}
				return ""
			}
			// A header row, if the first row is one: text, author, source (or the Chinese names).
			if i == 0 && slices.Contains([]string{"text", "文案", "正文", "内容"}, strings.ToLower(cell(0))) {
				continue
			}
			if cell(0) == "" {
				continue
			}
			out = append(out, lineOut{Text: cell(0), Author: cell(1), Source: cell(2)})
		}
	default:
		return nil, errors.New("format is lines, markdown or csv")
	}
	return out, nil
}

// checkLines checks lines and makes them ready to be kept.
func checkLines(in []lineOut) ([]store.DailyLine, error) {
	lines := make([]store.DailyLine, 0, len(in))
	for _, l := range in {
		body, author, source := strings.TrimSpace(l.Text), strings.TrimSpace(l.Author), strings.TrimSpace(l.Source)
		if body == "" {
			continue
		}
		if len(body) > store.MaxDailyLineBytes || utf8.RuneCountInString(source) > 200 || utf8.RuneCountInString(author) > 200 || strings.ContainsRune(body+author+source, 0) {
			return nil, &space.InvalidError{Msg: "An entry is too long (at most 8000 bytes, its author and source 200 characters each) or has a null character: " + firstWords(body) + "."}
		}
		lines = append(lines, store.DailyLine{Body: body, Author: author, Source: source})
	}
	return lines, nil
}

// adminPutDailyLines replaces the lines of one language: the text of an
// upload (`text` and `format`: lines, markdown or csv), parsed, or the lines as
// they are (`lines`, after the administrators corrected the split).
func (s *server) adminPutDailyLines(c *call) {
	var req struct {
		Lang   string    `json:"lang"`
		Text   string    `json:"text"`
		Format string    `json:"format"`
		Lines  []lineOut `json:"lines"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, maxLinesBody) {
		return
	}
	ctx := c.r.Context()
	lang, ok := lineLang(req.Lang)
	if !ok {
		s.fail(c.w, badLangError(req.Lang))
		return
	}
	in := req.Lines
	if in == nil {
		var err error
		if in, err = parseLines(req.Text, req.Format); err != nil {
			bad(c, err.Error())
			return
		}
	}
	lines, err := checkLines(in)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if len(lines) == 0 {
		bad(c, "There is no text in it; to take the lines away, delete them.")
		return
	}
	for i := range lines {
		lines[i].Lang = lang
	}
	err = s.opt.Store.Tx(ctx, func(q store.Q) error {
		_, err := store.ReplaceDailyLines(ctx, q, lang, lines)
		return err
	})
	if errors.Is(err, store.ErrTooManyLines) {
		bad(c, "The server keeps at most 5000 lines in all.")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.adminAnswerDaily(c)
}

// adminPreviewDailyLines shows how a text would be split into lines, author
// and source, without keeping anything.
func (s *server) adminPreviewDailyLines(c *call) {
	var req struct {
		Text   string `json:"text"`
		Format string `json:"format"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, maxLinesBody) {
		return
	}
	lines, err := parseLines(req.Text, req.Format)
	if err != nil {
		bad(c, err.Error())
		return
	}
	if len(lines) > store.MaxDailyLines {
		lines = lines[:store.MaxDailyLines]
	}
	if lines == nil {
		lines = []lineOut{}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Lines []lineOut `json:"lines"`
	}{lines})
}

// adminGetDailyLines gives the lines of one language (`?lang=`, empty for those for any).
func (s *server) adminGetDailyLines(c *call) {
	lang, ok := lineLang(c.r.URL.Query().Get("lang"))
	if !ok {
		s.fail(c.w, badLangError(c.r.URL.Query().Get("lang")))
		return
	}
	got, err := store.DailyLines(c.r.Context(), s.opt.Store.Q(), lang)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := []lineOut{}
	for _, l := range got {
		out = append(out, lineOut{l.Body, l.Author, l.Source})
	}
	writeJSON(c.w, http.StatusOK, struct {
		Lines []lineOut `json:"lines"`
	}{out})
}

// lineLang is the language of lines: a canonical tag, "" for any.
func lineLang(s string) (string, bool) {
	if s == "" {
		return "", true
	}
	return langtag.Canonical(s)
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
	lang, ok := lineLang(c.r.URL.Query().Get("lang"))
	if !ok {
		s.fail(c.w, badLangError(c.r.URL.Query().Get("lang")))
		return
	}
	if err := store.DeleteDailyLines(c.r.Context(), s.opt.Store.Q(), lang); err != nil {
		s.fail(c.w, err)
		return
	}
	s.adminAnswerDaily(c)
}

// --- what the owner of a space chooses ---------------------------------------------

// checkSpaceSource checks a source that is pages of a space the
// administrators named: the space is there and enabled, the source is valid in it.
func (s *server) checkSpaceSource(ctx context.Context, id string, src space.DailyConfig) (space.DailyConfig, error) {
	if _, err := store.GetSpace(ctx, s.opt.Store.Q(), store.AllTenants(), id); errors.Is(err, store.ErrNotFound) {
		return src, &space.InvalidError{Msg: "There is no such space."}
	} else if err != nil {
		return src, err
	}
	sp, release, err := s.spaces.Acquire(ctx, id)
	if errors.Is(err, spaces.ErrNotFound) || errors.Is(err, spaces.ErrDisabled) {
		return src, &space.InvalidError{Msg: "There is no such space, or it is disabled."}
	}
	if err != nil {
		return src, err
	}
	defer release()
	return sp.CheckDailyConfig(src)
}

// checkDefaultSources checks a default that is a list of sources for each
// language: the languages are tags (written in canonical form); each language
// has 1 to maxAPIsPerLanguage sources, each an interface that is set up, the
// administrators' lines, or a space with a source.
func (s *server) checkDefaultSources(ctx context.Context, d *siteDefault, site *siteDaily) error {
	if d.Options != nil {
		return &space.InvalidError{Msg: "Options go with a source of a default that is sources (each source has its own), not with the default."}
	}
	if d.Space != "" || d.Source != nil || d.APIs != nil || len(d.Sources) == 0 {
		return &space.InvalidError{Msg: "A default that is sources needs sources, the list for each language (\"\" for all the others), and no space, source or apis."}
	}
	if len(d.Sources) > 31 {
		return &space.InvalidError{Msg: "At most 30 languages."}
	}
	known := map[string]bool{}
	for _, a := range site.all() {
		known[a.ID] = true
	}
	out := make(sourceLists, len(d.Sources))
	for l, list := range d.Sources {
		if l != "" {
			cl, ok := langtag.Canonical(l)
			if !ok {
				return badLangError(l)
			}
			if _, dup := out[cl]; dup {
				return &space.InvalidError{Msg: fmt.Sprintf("%s is given twice.", cl)}
			}
			l = cl
		}
		if len(list) == 0 || len(list) > maxAPIsPerLanguage {
			return &space.InvalidError{Msg: fmt.Sprintf("Each language has 1 to %d sources.", maxAPIsPerLanguage)}
		}
		checked := make([]dailySource, 0, len(list))
		for _, src := range list {
			switch src.Kind {
			case "api":
				if src.Space != "" || src.Source != nil {
					return &space.InvalidError{Msg: "A source that is an interface has an id and options only."}
				}
				if !known[src.ID] {
					return &codedError{code: codeAPINotFound, msg: fmt.Sprintf("There is no interface %q; choose one of the interfaces that are set up.", src.ID)}
				}
				if err := site.checkServes(src.ID, l); err != nil {
					return err
				}
				// Options are checked below, with the interfaces in use.
			case "list":
				if src.ID != "" || src.Options != nil || src.Space != "" || src.Source != nil {
					return &space.InvalidError{Msg: "A source that is the lines has nothing else."}
				}
			case "space":
				if src.ID != "" || src.Options != nil || !page.ValidID(src.Space) || src.Source == nil {
					return &space.InvalidError{Msg: "A source that is a space needs the space and a source (folder, tag or type)."}
				}
				cfg, err := s.checkSpaceSource(ctx, src.Space, *src.Source)
				if err != nil {
					return err
				}
				src.Source = &cfg
			default:
				return &space.InvalidError{Msg: "Kind of a source is api, list or space."}
			}
			checked = append(checked, src)
		}
		out[l] = checked
	}
	// The options of an interface, canonical; the same for every language it is in.
	for l, list := range out {
		for i, src := range list {
			if src.Kind != "api" {
				continue
			}
			p, isBuiltin := dailyfetch.ProviderByID(src.ID)
			if len(src.Options) > 0 && (!isBuiltin || !slices.ContainsFunc(site.all(), func(a storedAPI) bool { return a.ID == src.ID && a.Builtin })) {
				return &space.InvalidError{Msg: fmt.Sprintf("Options are for built-in interfaces; %q is not one.", src.ID)}
			}
			if len(src.Options) > 0 {
				canon, err := p.CheckOptions(src.Options)
				if err != nil {
					return &space.InvalidError{Msg: err.Error()}
				}
				out[l][i].Options = canon
			}
		}
	}
	d.Sources = out
	return nil
}

// checkServes refuses a built-in interface under a language it has no text in
// (一言 under English): it would never be used there. Under "" (all the other
// languages) anything may be listed; the readers it does not serve are skipped.
func (d *siteDaily) checkServes(id, lang string) error {
	if lang == "" {
		return nil
	}
	for _, a := range d.all() {
		if a.ID == id && a.Builtin && !a.Serves(lang) {
			return &space.InvalidError{Msg: fmt.Sprintf("%s has no text in %s; list it under the languages it has.", a.Name, lang)}
		}
	}
	return nil
}
