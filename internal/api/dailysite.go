package api

import (
	"context"
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
	Default   *siteDefault        `json:"default"`
	Hosts     []string            `json:"hosts"`
	Languages []string            `json:"languages"`
	Allow     dailyAllow          `json:"allow"`
	APIs      []apiView           `json:"apis"`
	Presets   []dailyfetch.Preset `json:"presets"`
	Lines     dailyLinesInfo      `json:"lines"`
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
	apis := make([]apiView, len(site.APIs))
	for i, a := range site.APIs {
		apis[i] = viewOfAPI(a)
	}
	return adminDailyResp{site.Default, site.Hosts, site.Languages, *site.Allow, apis, dailyfetch.Presets(), dailyLinesInfo{total, counts, store.MaxDailyLines}}, nil
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

// --- what the owner of a space chooses ---------------------------------------------
