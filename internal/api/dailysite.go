package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KKazuhaK/PetalBook/internal/dailyfetch"
	"github.com/KKazuhaK/PetalBook/internal/langtag"
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
	// Kind is "space" (the source is a folder, tag or type of that space),
	// "list" (the lines an administrator pasted or uploaded) or "api" (an
	// interface for each language).
	Kind   string             `json:"kind"`
	Space  string             `json:"space,omitempty"`
	Source *space.DailyConfig `json:"source,omitempty"`
	// APIs are the ids of the interfaces by language, "" for the languages not
	// listed.
	APIs map[string]string `json:"apis,omitempty"`
}

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
	// expires is when what was answered stops being the same, zero when it is
	// the end of the day asked for.
	expires time.Time
}

// linesByLang groups what the table counts by canonical language tag (lines
// saved before the rule are under `zh`, read as `zh-Hans`): the counts, and
// for each tag the languages as saved, in order.
func linesByLang(counts map[string]int) (map[string]int, map[string][]string) {
	canon := map[string]int{}
	raw := map[string][]string{}
	for l, n := range counts {
		if n <= 0 {
			continue
		}
		c := langtag.Read(l)
		if l == "" {
			c = ""
		}
		canon[c] += n
		raw[c] = append(raw[c], l)
	}
	for _, r := range raw {
		sort.Strings(r)
	}
	return canon, raw
}

// dailyFromLines picks one of the lines the administrators gave: those in the
// language the reader's is matched to, else those for any language, else the
// first language that has some.
func (s *server) dailyFromLines(ctx context.Context, date string, skip int, lang string) (*dailyQuote, string, error) {
	q := s.opt.Store.Q()
	rawCounts, err := store.DailyLineCounts(ctx, q)
	if err != nil {
		return nil, "", err
	}
	counts, raw := linesByLang(rawCounts)
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
		if len(langs) == 0 {
			return nil, "", nil
		}
		use = langs[0]
	}
	n := int((space.DailyNumber("site:"+use, date) + uint64(skip)) % uint64(counts[use]))
	var line store.DailyLine
	found := false
	for _, r := range raw[use] {
		if n < rawCounts[r] {
			if line, err = store.NthDailyLine(ctx, q, r, n); err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, "", err
			}
			found = err == nil
			break
		}
		n -= rawCounts[r]
	}
	if !found {
		return nil, "no_match", nil // the lines changed while this was read
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
}

type adminDailyResp struct {
	Default        *siteDefault       `json:"default"`
	Languages      []string           `json:"languages"`
	KnownLanguages []langtag.Language `json:"knownLanguages"`
	Allow          dailyAllow         `json:"allow"`
	APIs           []apiView          `json:"apis"`
	Providers      []providerView     `json:"providers"`
	Lines          dailyLinesInfo     `json:"lines"`
	// Notices are what the server changed in the settings by itself.
	Notices []siteNotice `json:"notices"`
}

func (s *server) adminDailyView(ctx context.Context) (adminDailyResp, error) {
	site, err := s.loadSite(ctx)
	if err != nil {
		return adminDailyResp{}, err
	}
	rawCounts, err := store.DailyLineCounts(ctx, s.opt.Store.Q())
	if err != nil {
		return adminDailyResp{}, err
	}
	counts, _ := linesByLang(rawCounts)
	total := 0
	for _, n := range counts {
		total += n
	}
	all := site.all()
	apis := make([]apiView, len(all))
	for i, a := range all {
		apis[i] = viewOfAPI(a)
	}
	providers := make([]providerView, 0, 2)
	for _, p := range dailyfetch.Providers() {
		st := site.Providers[p.ID]
		b := builtinAPI(p, st)
		providers = append(providers, providerView{p.ID, p.Name, p.Languages, p.Site, p.Note, st.Enabled, b.Refresh, b.RefreshMinutes, b.RPS, p.RPS, b.SkipNew})
	}
	notices := site.Notices
	if notices == nil {
		notices = []siteNotice{}
	}
	return adminDailyResp{site.Default, site.Languages, langtag.Known(), *site.Allow, apis, providers, dailyLinesInfo{total, counts, store.MaxDailyLines}, notices}, nil
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
		return &space.InvalidError{Msg: "a default that is an interface needs apis, the id of an interface for each language (\"\" for all the others), and no space or source"}
	}
	if len(d.APIs) > 31 {
		return &space.InvalidError{Msg: "at most 30 languages"}
	}
	known := map[string]bool{}
	for _, a := range site.all() {
		known[a.ID] = true
	}
	out := make(map[string]string, len(d.APIs))
	for l, id := range d.APIs {
		if l != "" {
			cl, ok := langtag.Canonical(l)
			if !ok {
				return badLangError(l)
			}
			if _, dup := out[cl]; dup {
				return &space.InvalidError{Msg: fmt.Sprintf("%s is given twice", cl)}
			}
			l = cl
		}
		if !known[id] {
			return &codedError{code: codeAPINotFound, msg: fmt.Sprintf("there is no interface %q; choose one of the interfaces that are set up", id)}
		}
		out[l] = id
	}
	d.APIs = out
	return nil
}

// checkSiteDefault checks a default against the interfaces that are set up
// (site) and the spaces there are; apis are written as canonical tags.
func (s *server) checkSiteDefault(ctx context.Context, d *siteDefault, site *siteDaily) error {
	if d.Kind != "api" && d.APIs != nil {
		return &space.InvalidError{Msg: "only a default that is an interface has apis"}
	}
	switch d.Kind {
	case "list":
		if d.Space != "" || d.Source != nil {
			return &space.InvalidError{Msg: "a default that is a list has no space or source"}
		}
	case "api":
		return checkDefaultAPIs(d, site)
	case "space":
		if !page.ValidID(d.Space) || d.Source == nil {
			return &space.InvalidError{Msg: "a default that is a space needs the space and a source (folder, tag or type)"}
		}
		if _, err := store.GetSpace(ctx, s.opt.Store.Q(), store.AllTenants(), d.Space); errors.Is(err, store.ErrNotFound) {
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
		return &space.InvalidError{Msg: "kind is space, list or api"}
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
	ctx := c.r.Context()
	lang, ok, err := storedLineLang(ctx, s.opt.Store.Q(), req.Lang)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if !ok {
		s.fail(c.w, badLangError(req.Lang))
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
	err = s.opt.Store.Tx(ctx, func(q store.Q) error {
		// The lines of this language saved before the rule (as `zh`) are replaced too.
		if err := deleteLangLines(ctx, q, lang); err != nil {
			return err
		}
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

// lineLang is the language of lines: a canonical tag, "" for any.
func lineLang(s string) (string, bool) {
	if s == "" {
		return "", true
	}
	return langtag.Canonical(s)
}

// storedLineLang is the language of lines a request names: a canonical tag,
// "" for any, or a code that is no tag exactly as lines are stored under it
// (`cn`, saved by 2.3), so that those lines can still be replaced and deleted.
func storedLineLang(ctx context.Context, q store.Q, given string) (string, bool, error) {
	if l, ok := lineLang(given); ok {
		return l, true, nil
	}
	counts, err := store.DailyLineCounts(ctx, q)
	if err != nil {
		return "", false, err
	}
	for l := range counts {
		if l == given {
			return l, true, nil
		}
	}
	return "", false, nil
}

// deleteLangLines removes the lines of a language, as saved in any form of its tag.
func deleteLangLines(ctx context.Context, q store.Q, lang string) error {
	counts, err := store.DailyLineCounts(ctx, q)
	if err != nil {
		return err
	}
	for l := range counts {
		if l == lang || lang != "" && l != "" && langtag.Read(l) == lang {
			if err := store.DeleteDailyLines(ctx, q, l); err != nil {
				return err
			}
		}
	}
	return nil
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
	ctx := c.r.Context()
	lang, ok, err := storedLineLang(ctx, s.opt.Store.Q(), c.r.URL.Query().Get("lang"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if !ok {
		s.fail(c.w, badLangError(c.r.URL.Query().Get("lang")))
		return
	}
	if err := s.opt.Store.Tx(ctx, func(q store.Q) error { return deleteLangLines(ctx, q, lang) }); err != nil {
		s.fail(c.w, err)
		return
	}
	s.adminAnswerDaily(c)
}

// --- what the owner of a space chooses ---------------------------------------------
