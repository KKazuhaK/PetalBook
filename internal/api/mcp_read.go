package api

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"petalbook.app/server/internal/index"
	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/space"
)

const spaceDesc = "id of the space, from spaces_list (may be left out in single-user mode, where the only space is default)"

// mcpItem is a page as the tools list it: the summary of a listing with its
// space, rev and properties, and never a body.
type mcpItem struct {
	Space         string          `json:"space"`
	ID            string          `json:"id"`
	Path          string          `json:"path"`
	Folder        string          `json:"folder"`
	Title         string          `json:"title,omitempty"`
	Subtitle      string          `json:"subtitle,omitempty"`
	Source        []string        `json:"source"`
	Label         string          `json:"label"`
	Untitled      bool            `json:"untitled"`
	Tags          []string        `json:"tags"`
	Type          string          `json:"type"`
	EffectiveType string          `json:"effectiveType"`
	Created       string          `json:"created,omitempty"`
	Updated       string          `json:"updated"`
	Excerpt       string          `json:"excerpt"`
	Props         json.RawMessage `json:"props,omitempty"`
	PartialProps  []string        `json:"partialProps,omitempty"`
	Rev           string          `json:"rev"`
}

func toMCPItem(spaceID string, s index.Summary, types space.TypeResolver) mcpItem {
	tags := s.Tags
	if tags == nil {
		tags = []string{}
	}
	return mcpItem{Space: spaceID, ID: s.ID, Path: s.Path, Folder: s.Folder, Title: s.Title, Subtitle: s.Subtitle, Source: nonNil(s.Sources),
		Label: s.Label, Untitled: s.Untitled, Tags: tags, Type: s.Type, EffectiveType: types.For(s.Folder, s.Type), Created: s.Created,
		Updated: rfc3339(s.Updated), Excerpt: cutRunes(s.Excerpt, page.ExcerptRunes), Props: s.Props, PartialProps: s.PartialProps, Rev: s.Rev}
}

// cutRunes keeps at most n characters of s.
func cutRunes(s string, n int) string {
	if len(s) <= n { // n characters are at least n bytes
		return s
	}
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

// itemOf is the item of a page the server just wrote, from the index, which
// the write has brought up to date.
func (c *mcpCall) itemOf(spaceID string, sp *space.Space, p *space.Page) (mcpItem, error) {
	s, ok, err := sp.PageSummary(p.ID)
	if err != nil {
		return mcpItem{}, err
	}
	if !ok {
		return mcpItem{}, space.ErrNotFound
	}
	return toMCPItem(spaceID, s, sp.TypeResolver()), nil
}

// pageLimit checks a limit: 0 is the default.
func pageLimit(n, def, most int) (int, error) {
	switch {
	case n == 0:
		return def, nil
	case n < 1 || n > most:
		return 0, mcpInvalid("limit must be a number from 1 to %d", most)
	}
	return n, nil
}

// withPiece puts into out, the answer of page_get or page_version, the part of
// body from offset that fits in a result beside the rest of out: at most
// mcpMaxBodyChunk bytes, cut at a character, and where the rest starts (nil at
// the end). A header so large that no part of the body fits beside it is
// too_large.
func withPiece(out map[string]any, body string, offset int) (map[string]any, error) {
	if offset < 0 || offset > len(body) || (offset < len(body) && !utf8.RuneStart(body[offset])) {
		return nil, mcpInvalid("offset must be a byte offset within the body, at the start of a character (the next of the last answer, or 0)")
	}
	// The rest of the answer, with the largest next there can be.
	out["body"], out["offset"], out["next"] = "", offset, len(body)
	b, err := mcpEncode(out)
	if err != nil {
		return nil, err
	}
	room := mcpMaxResult - len(b)
	end, size := offset, 0
	for end < len(body) {
		r, n := utf8.DecodeRuneInString(body[end:])
		if end+n-offset > mcpMaxBodyChunk || size+jsonSize(r, n) > room {
			break
		}
		end, size = end+n, size+jsonSize(r, n)
	}
	if room < 0 || (end == offset && offset < len(body)) {
		return nil, &mcpError{Code: "too_large", Message: "The header of this page leaves no room for its body in the 1 MiB a result may be; the page cannot be read through MCP.",
			Details: map[string]any{"limitBytes": mcpMaxResult, "sizeBytes": len(b)}}
	}
	out["body"] = body[offset:end]
	if end < len(body) {
		out["next"] = end
	} else {
		out["next"] = nil
	}
	return out, nil
}

// jsonSize is at least the size of the rune r, of n bytes, in a JSON string.
func jsonSize(r rune, n int) int {
	switch {
	case r == '"' || r == '\\' || r == '\n' || r == '\r' || r == '\t':
		return 2
	case r < 0x20 || r == '\u2028' || r == '\u2029' || r == utf8.RuneError && n == 1:
		return 6
	}
	return n
}

func (t *mcpTools) addReadTools() {
	addTool(t, "spaces_list", "What this credential reaches: who you act for, whether the credential reads or also writes, the spaces it names that you are a member of with what you may do in each (read, write), the folder quick notes go to, and the limits of the other tools. Start here.", false, nil, t.spacesList)
	addTool(t, "folders_list", "List folders of a space, in path order, each with the number of pages directly in it. By default the folders directly in parent (the top level when parent is left out); with recursive, every folder below it. Paged.", false, nil, t.foldersList)
	addTool(t, "tags_list", "List the tags of a space with the number of pages using each, the most used first. Paged.", false, nil, t.tagsList)
	addTool(t, "types_list", "List the page types of a space: the typed header fields of each (key, title, kind and, for a choice, its options) and the folders bound to it. Use the field keys in pages_query filters and sorts (sort \"prop:{key}\").", false, nil, t.typesList)
	addTool(t, "pages_search", "Full-text search of the pages of a space, by relevance, with the matching as in the app's search box. Returns page items (no bodies) with an excerpt, tags and properties. Paged: pass next back as cursor.", false, nil, t.pagesSearch)
	addTool(t, "pages_query", "List pages of a space by folder, tags, dates and conditions on typed fields, sorted. filter is a JSON array of {\"key\": field, \"op\": \"in\"|\"notIn\"|\"range\"|\"empty\"|\"notEmpty\"|\"contains\", \"values\": [...]} (range takes [from, to], either may be null). sort is updated, created, title or prop:{key}, with a leading - to reverse; the default is the most recently changed first. Paged: pass next back as cursor.", false, map[string]string{"filter": "array"}, t.pagesQuery)
	addTool(t, "page_get", "Read a page by id or by path (as links name it, from the space root, e.g. Tang/暮雪.md): its header fields (meta), rev and body. A body larger than 128 KiB comes in pieces: repeat with offset = next until next is null. Keep the rev for page_update.", false, nil, t.pageGet)
	addTool(t, "page_history", "The versions of a page, newest first: commit, time, author, change (add, edit, move, delete) and, for a change made through MCP or with an API token, via. Paged.", false, nil, t.pageHistory)
	addTool(t, "page_version", "A page as it was in one version (a commit from page_history): header fields and body, the body in pieces as page_get.", false, nil, t.pageVersion)
}

// --- spaces_list ---------------------------------------------------------------

type noArgs struct{}

func (t *mcpTools) spacesList(c *mcpCall, _ noArgs) (any, error) {
	l, err := c.s.spaces.List(c.ctx, c.p.User.ID)
	if err != nil {
		return nil, err
	}
	g := c.p.Grant
	type spaceOut struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
		Inbox       string   `json:"inbox"`
	}
	out := []spaceOut{}
	for _, in := range l {
		if g.Names(in.ID) && !in.Disabled {
			out = append(out, spaceOut{in.ID, in.Name, (in.Perms & g.Perms()).Names(), in.Inbox})
		}
	}
	kind := "token"
	if g.Kind == "oauth" {
		kind = "oauth"
	}
	return map[string]any{
		"user":       map[string]any{"id": c.p.User.ID, "name": c.p.User.Name},
		"credential": map[string]any{"kind": kind, "access": g.Access()},
		"spaces":     out,
		"limits":     map[string]any{"maxResultBytes": mcpMaxResult, "maxBodyChunk": mcpMaxBodyChunk, "maxWriteBody": mcpMaxWriteBody},
	}, nil
}

// --- folders_list --------------------------------------------------------------

type foldersIn struct {
	Space     string `json:"space,omitempty" jsonschema:"space id from spaces_list; may be left out in single-user mode"`
	Parent    string `json:"parent,omitempty" jsonschema:"folder whose folders are listed; the top level when left out"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"list every folder below parent, not only those directly in it"`
	Cursor    string `json:"cursor,omitempty" jsonschema:"the next of the previous answer"`
	Limit     int    `json:"limit,omitempty" jsonschema:"1 to 1000, default 200"`
}

// pathCursor is a cursor that says where in an ordered list the next page
// starts: after the item it names.
func pathCursor(after string) string { return base64.RawURLEncoding.EncodeToString([]byte(after)) }

func readPathCursor(cur string) (string, error) {
	if cur == "" {
		return "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(cur)
	if err != nil {
		return "", mcpInvalid("invalid cursor")
	}
	return string(b), nil
}

func (t *mcpTools) foldersList(c *mcpCall, in foldersIn) (any, error) {
	limit, err := pageLimit(in.Limit, 200, 1000)
	if err != nil {
		return nil, err
	}
	after, err := readPathCursor(in.Cursor)
	if err != nil {
		return nil, err
	}
	if err := space.ValidFolder(in.Parent); err != nil {
		return nil, err
	}
	h, err := c.open(in.Space, false)
	if err != nil {
		return nil, err
	}
	defer h.Release()
	parent := ""
	if in.Parent != "" {
		canon, ok, err := h.Space.CanonicalFolder(in.Parent)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, mcpErr("not_found", "no such folder")
		}
		parent = canon
	}
	tree, err := h.Space.Tree()
	if err != nil {
		return nil, err
	}
	type item struct {
		Path  string `json:"path"`
		Pages int    `json:"pages"`
	}
	items := []item{}
	next := ""
	for _, f := range tree {
		if f.Path == "" || f.Path == parent || f.Path <= after {
			continue
		}
		if parent != "" && !strings.HasPrefix(f.Path, parent+"/") {
			continue
		}
		if !in.Recursive {
			rest := f.Path
			if parent != "" {
				rest = strings.TrimPrefix(f.Path, parent+"/")
			}
			if strings.Contains(rest, "/") {
				continue
			}
		}
		if len(items) == limit {
			next = pathCursor(items[len(items)-1].Path)
			break
		}
		items = append(items, item{f.Path, f.Pages})
	}
	return map[string]any{"items": items, "next": next}, nil
}

// --- tags_list -----------------------------------------------------------------

type tagsIn struct {
	Space  string `json:"space,omitempty" jsonschema:"space id from spaces_list; may be left out in single-user mode"`
	Cursor string `json:"cursor,omitempty" jsonschema:"the next of the previous answer"`
	Limit  int    `json:"limit,omitempty" jsonschema:"1 to 500, default 100"`
}

func (t *mcpTools) tagsList(c *mcpCall, in tagsIn) (any, error) {
	limit, err := pageLimit(in.Limit, 100, 500)
	if err != nil {
		return nil, err
	}
	// The list is by use, then by name: a cursor is the last tag's count and name.
	after := -1
	afterTag := ""
	if in.Cursor != "" {
		raw, err := readPathCursor(in.Cursor)
		n, tag, ok := strings.Cut(raw, "\n")
		if v, cerr := strconv.Atoi(n); err != nil || !ok || cerr != nil || v < 0 {
			return nil, mcpInvalid("invalid cursor")
		} else {
			after, afterTag = v, tag
		}
	}
	h, err := c.open(in.Space, false)
	if err != nil {
		return nil, err
	}
	defer h.Release()
	tags, err := h.Space.Tags()
	if err != nil {
		return nil, err
	}
	// Tags are most used first (see index.Tags), so the page starts after the
	// first one that comes after the cursor.
	start := 0
	if after >= 0 {
		start = sort.Search(len(tags), func(i int) bool {
			return tags[i].Pages < after || (tags[i].Pages == after && tags[i].Tag > afterTag)
		})
	}
	end := min(start+limit, len(tags))
	next := ""
	if end < len(tags) {
		next = pathCursor(strconv.Itoa(tags[end-1].Pages) + "\n" + tags[end-1].Tag)
	}
	return map[string]any{"items": tags[start:end], "next": next}, nil
}

// --- types_list ----------------------------------------------------------------

type spaceOnly struct {
	Space string `json:"space,omitempty" jsonschema:"space id from spaces_list; may be left out in single-user mode"`
}

func (t *mcpTools) typesList(c *mcpCall, in spaceOnly) (any, error) {
	h, err := c.open(in.Space, false)
	if err != nil {
		return nil, err
	}
	defer h.Release()
	types, bindings, err := h.Space.Types()
	if err != nil {
		return nil, err
	}
	type field struct {
		Key     string   `json:"key"`
		Title   string   `json:"title"`
		Kind    string   `json:"kind"`
		Options []string `json:"options,omitempty"`
	}
	type typeOut struct {
		Name    string   `json:"name"`
		Title   string   `json:"title"`
		Fields  []field  `json:"fields"`
		Folders []string `json:"folders"`
		Error   string   `json:"error,omitempty"`
	}
	out := make([]typeOut, 0, len(types))
	at := map[string]int{}
	for _, ty := range types {
		o := typeOut{Name: ty.Name, Title: ty.Title, Fields: make([]field, len(ty.Fields)), Folders: []string{}, Error: ty.Error}
		for i, f := range ty.Fields {
			o.Fields[i] = field{f.Key, f.Title, f.Kind, f.Options[:min(len(f.Options), mcpMaxTypeOptions)]}
		}
		at[ty.Name] = len(out)
		out = append(out, o)
	}
	for _, b := range bindings {
		if i, ok := at[b.Type]; ok {
			out[i].Folders = append(out[i].Folders, b.Folder)
		}
	}
	return map[string]any{"types": out}, nil
}

// --- pages_search and pages_query ------------------------------------------------

type searchIn struct {
	Space     string   `json:"space,omitempty" jsonschema:"space id from spaces_list; may be left out in single-user mode"`
	Q         string   `json:"q" jsonschema:"the words to find, at most 500 characters"`
	In        string   `json:"in,omitempty" jsonschema:"title: match titles only"`
	Folder    *string  `json:"folder,omitempty" jsonschema:"only pages in this folder (\"\" is the top level)"`
	Recursive bool     `json:"recursive,omitempty" jsonschema:"with folder: also the folders below it"`
	Tag       []string `json:"tag,omitempty" jsonschema:"only pages with all of these tags (at most 10)"`
	Cursor    string   `json:"cursor,omitempty" jsonschema:"the next of the previous answer"`
	Limit     int      `json:"limit,omitempty" jsonschema:"1 to 50, default 20"`
}

type queryIn struct {
	Space       string          `json:"space,omitempty" jsonschema:"space id from spaces_list; may be left out in single-user mode"`
	Folder      *string         `json:"folder,omitempty" jsonschema:"only pages in this folder (\"\" is the top level)"`
	Recursive   bool            `json:"recursive,omitempty" jsonschema:"with folder: also the folders below it"`
	Tag         []string        `json:"tag,omitempty" jsonschema:"only pages with all of these tags (at most 10)"`
	Filter      json.RawMessage `json:"filter,omitempty" jsonschema:"conditions on typed fields: a JSON array of {key, op, values}, at most 11"`
	Sort        string          `json:"sort,omitempty" jsonschema:"updated, created, title or prop:{key}; a leading - reverses"`
	UpdatedFrom string          `json:"updatedFrom,omitempty" jsonschema:"changed on or after this day (2026-11-01)"`
	UpdatedTo   string          `json:"updatedTo,omitempty" jsonschema:"changed on or before this day"`
	CreatedFrom string          `json:"createdFrom,omitempty" jsonschema:"created on or after this day"`
	CreatedTo   string          `json:"createdTo,omitempty" jsonschema:"created on or before this day"`
	Cursor      string          `json:"cursor,omitempty" jsonschema:"the next of the previous answer"`
	Limit       int             `json:"limit,omitempty" jsonschema:"1 to 200, default 50"`
}

func (t *mcpTools) pagesSearch(c *mcpCall, in searchIn) (any, error) {
	if strings.TrimSpace(in.Q) == "" {
		return nil, mcpInvalid("q is required")
	}
	if utf8.RuneCountInString(in.Q) > 500 {
		return nil, mcpInvalid("q is at most 500 characters")
	}
	limit, err := pageLimit(in.Limit, 20, 50)
	if err != nil {
		return nil, err
	}
	v := url.Values{"q": {in.Q}, "limit": {strconv.Itoa(limit)}, "props": {"1"}}
	if in.In != "" {
		v.Set("in", in.In)
	}
	if in.Cursor != "" {
		v.Set("cursor", in.Cursor)
	}
	return t.list(c, in.Space, v, in.Folder, in.Recursive, in.Tag)
}

func (t *mcpTools) pagesQuery(c *mcpCall, in queryIn) (any, error) {
	limit, err := pageLimit(in.Limit, 50, 200)
	if err != nil {
		return nil, err
	}
	v := url.Values{"limit": {strconv.Itoa(limit)}, "props": {"1"}}
	for k, val := range map[string]string{"sort": in.Sort, "updatedFrom": in.UpdatedFrom, "updatedTo": in.UpdatedTo,
		"createdFrom": in.CreatedFrom, "createdTo": in.CreatedTo, "cursor": in.Cursor} {
		if val != "" {
			v.Set(k, val)
		}
	}
	if f := strings.TrimSpace(string(in.Filter)); f != "" && f != "null" {
		v.Set("filter", f)
	}
	return t.list(c, in.Space, v, in.Folder, in.Recursive, in.Tag)
}

// list is what the two listing tools share: the listing of GET …/pages, from
// the index, with the same parameters, meanings and refusals.
func (t *mcpTools) list(c *mcpCall, spaceID string, v url.Values, folder *string, recursive bool, tags []string) (any, error) {
	if folder != nil {
		v.Set("folder", *folder)
	}
	if recursive {
		v.Set("recursive", "true")
	}
	for _, tag := range tags {
		v.Add("tag", tag)
	}
	q, _, err := parseListQuery(v)
	if err != nil {
		return nil, err
	}
	h, err := c.open(spaceID, false)
	if err != nil {
		return nil, err
	}
	defer h.Release()
	res, err := h.Space.List(q)
	if err != nil {
		return nil, err
	}
	types := h.Space.TypeResolver()
	items := make([]mcpItem, len(res.Items))
	for i, it := range res.Items {
		items[i] = toMCPItem(h.Info.ID, it, types)
	}
	return map[string]any{"items": items, "next": res.Next}, nil
}

// --- page_get, page_history, page_version ---------------------------------------------

type getIn struct {
	Space  string `json:"space,omitempty" jsonschema:"space id from spaces_list; may be left out in single-user mode"`
	ID     string `json:"id,omitempty" jsonschema:"the page's id"`
	Path   string `json:"path,omitempty" jsonschema:"the page's path from the space root, as links name it; give id or path"`
	Offset int    `json:"offset,omitempty" jsonschema:"byte offset in the body to read from: 0, or the next of the previous answer"`
}

func (t *mcpTools) pageGet(c *mcpCall, in getIn) (any, error) {
	if (in.ID == "") == (in.Path == "") {
		return nil, mcpInvalid("give the page's id or its path, one of the two")
	}
	h, err := c.open(in.Space, false)
	if err != nil {
		return nil, err
	}
	defer h.Release()
	id := in.ID
	if in.Path != "" {
		var ok bool
		if id, ok = h.Space.PageIDByPath(strings.TrimPrefix(in.Path, "/")); !ok {
			return nil, mcpErr("not_found", "no such page")
		}
	}
	if !page.ValidID(id) {
		return nil, mcpErr("not_found", "no such page")
	}
	p, err := h.Space.Get(id)
	if err != nil {
		return nil, err
	}
	meta := p.Meta
	if meta == nil {
		meta = page.Meta{}
	}
	out := map[string]any{"space": h.Info.ID, "id": p.ID, "path": p.Path, "folder": p.Folder, "rev": p.Rev, "updated": rfc3339(p.Updated), "meta": meta,
		"effectiveType": p.EffectiveType, "bodyBytes": len(p.Body)}
	if len(p.PartialProps) > 0 {
		out["partialProps"] = p.PartialProps
	}
	return withPiece(out, p.Body, in.Offset)
}

type historyIn struct {
	Space  string `json:"space,omitempty" jsonschema:"space id from spaces_list; may be left out in single-user mode"`
	ID     string `json:"id" jsonschema:"the page's id"`
	Cursor string `json:"cursor,omitempty" jsonschema:"the next of the previous answer"`
	Limit  int    `json:"limit,omitempty" jsonschema:"1 to 100, default 20"`
}

func (t *mcpTools) pageHistory(c *mcpCall, in historyIn) (any, error) {
	limit, err := pageLimit(in.Limit, 20, 100)
	if err != nil {
		return nil, err
	}
	if !page.ValidID(in.ID) {
		return nil, mcpErr("not_found", "no such page")
	}
	h, err := c.open(in.Space, false)
	if err != nil {
		return nil, err
	}
	defer h.Release()
	items, next, err := h.Space.History(c.ctx, in.ID, limit, in.Cursor)
	if err != nil {
		return nil, err
	}
	out := make([]historyItemResp, len(items))
	for i, it := range items {
		out[i] = historyItemResp{Commit: it.Commit, Time: rfc3339(it.Time), Author: it.Author, Path: it.Path, Rev: it.Rev, Change: it.Change, Via: it.Via}
	}
	return map[string]any{"items": out, "next": next}, nil
}

type versionIn struct {
	Space  string `json:"space,omitempty" jsonschema:"space id from spaces_list; may be left out in single-user mode"`
	ID     string `json:"id" jsonschema:"the page's id"`
	Commit string `json:"commit" jsonschema:"a commit from page_history"`
	Offset int    `json:"offset,omitempty" jsonschema:"byte offset in the body to read from: 0, or the next of the previous answer"`
}

func (t *mcpTools) pageVersion(c *mcpCall, in versionIn) (any, error) {
	if !page.ValidID(in.ID) {
		return nil, mcpErr("not_found", "no such page")
	}
	h, err := c.open(in.Space, false)
	if err != nil {
		return nil, err
	}
	defer h.Release()
	v, err := h.Space.Version(c.ctx, in.ID, in.Commit)
	if err != nil {
		return nil, err
	}
	meta := v.Meta
	if meta == nil {
		meta = page.Meta{}
	}
	return withPiece(map[string]any{"space": h.Info.ID, "id": in.ID, "commit": v.Commit, "time": rfc3339(v.Time), "author": v.Author, "path": v.Path, "rev": v.Rev,
		"meta": meta, "bodyBytes": len(v.Body)}, v.Body, in.Offset)
}
