package index

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"petalbook.app/server/internal/page"
)

var bg = context.Background()

// The check page (docs/api.md, "Checks") lists what is wrong in a space. Every
// problem is recorded when its file is indexed and read back by an index
// query, so opening the page never scans anything:
//
//   - no_id, duplicate_id and bad_header are the files that are not pages: the
//     problems table (a file that is not served has no row in pages);
//   - the other kinds belong to pages that are served, and live in the checks
//     table, keyed by (kind, path, seq). seq numbers the rows of one page and
//     kind (a page can have several missing attachments);
//   - duplicate_title is not read from a file but from the pages of a folder,
//     so it is kept up to date from the index itself: whenever a page is
//     added, changed, moved or removed, the groups it left and joined are
//     looked at again (a full rebuild does it once, with one query).

// The kinds of problem, in the order the check page lists them.
const (
	KindNoID           = "no_id"
	KindDuplicateID    = "duplicate_id"
	KindBadHeader      = "bad_header"
	KindTypeMismatch   = "type_mismatch"
	KindNotInType      = "not_in_type"
	KindBrokenLink     = "broken_link"
	KindMissingAsset   = "missing_asset"
	KindDuplicateTitle = "duplicate_title"
)

// CheckKinds lists the kinds in listing order.
var CheckKinds = []string{KindNoID, KindDuplicateID, KindBadHeader, KindTypeMismatch, KindNotInType,
	KindBrokenLink, KindMissingAsset, KindDuplicateTitle}

// fileKinds are the kinds kept in the problems table.
var fileKinds = map[string]bool{KindNoID: true, KindDuplicateID: true, KindBadHeader: true}

// MaxCheckPaths is the most paths a duplicate_id or duplicate_title problem
// names; maxRowsPerKind is the most rows one page gets of one kind.
const (
	MaxCheckPaths  = 20
	maxRowsPerKind = 20
	// CheckPage is the number of problems in a page of the listing.
	CheckPage = 50
)

// kindOfCode is the kind of problem a file that is not a page is, by the code
// page.Doc.Problems gave it. A file that could not be read is not one.
func kindOfCode(code string) string {
	switch code {
	case "no_id", "bad_id":
		return KindNoID
	case "no_header", "unterminated_header", "bad_yaml", "header_not_mapping":
		return KindBadHeader
	case "duplicate_id":
		return KindDuplicateID
	}
	return ""
}

var reYAMLLine = regexp.MustCompile(`line (\d+)`)

// headerLine is the line of the file a bad header is wrong at, 0 when it is
// not known. The YAML reader counts from the first line after the opening
// fence.
func headerLine(p *page.Problem) int {
	switch p.Code {
	case "bad_yaml":
		if m := reYAMLLine.FindStringSubmatch(p.Message); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
				return n + 1
			}
		}
	case "unterminated_header":
		return 1
	}
	return 0
}

// --- the types ---------------------------------------------------------------

// The kinds of field a type has (the names of docs/api.md "Types").
const (
	FieldText        = "text"
	FieldLongText    = "longtext"
	FieldSelect      = "select"
	FieldMultiSelect = "multiselect"
	FieldDate        = "date"
	FieldNumber      = "number"
	FieldCheckbox    = "checkbox"
	FieldURL         = "url"
	FieldOther       = "other"
)

// FieldRule is what a type says of one field.
type FieldRule struct {
	Kind    string
	Options []string // select and multiselect; none means any text
}

// TypeRule is a type as far as the checks need it.
type TypeRule struct{ Fields map[string]FieldRule }

// TypeRules tells the index what the types of the space say. For returns the
// rule of the type a page of folder with the header type headerType has, nil
// when it has none (or the type's file cannot be used). Fingerprint changes
// whenever a type or a folder binding does.
type TypeRules struct {
	For         func(folder, headerType string) *TypeRule
	Fingerprint string
}

// UseTypes sets the rules new and changed files are checked against. It does
// not look at the pages already indexed (see ApplyTypes).
func (x *Index) UseTypes(r *TypeRules) { x.rules.Store(r) }

// SetAssetExists sets how the index finds out whether a file of the space (a
// path from its root) exists; without it attachments are not checked.
func (x *Index) SetAssetExists(f func(rel string) bool) { x.assetExists.Store(&f) }

// ApplyTypes makes the type problems of every page those of the rules r, when
// they are not already: the rules the problems on record were made with are
// compared by their fingerprint, so a start-up with the types unchanged costs
// one query. Otherwise every page that has properties is checked again from
// the index (nothing is read from the files).
func (x *Index) ApplyTypes(r *TypeRules) error {
	x.rules.Store(r)
	x.wmu.Lock()
	defer x.unlockWrite()
	ctx := bg
	var have string
	err := x.wdb.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'types_fp'`).Scan(&have)
	if err == nil && r != nil && have == r.Fingerprint {
		return nil
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	tx, err := x.wdb.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	w := x.newWriter(ctx, tx)
	defer w.close()
	if err := w.recheckTypes(r); err != nil {
		return err
	}
	return x.commit(tx)
}

// recheckTypes replaces the type problems of all pages.
func (w *writer) recheckTypes(r *TypeRules) error {
	if _, err := w.exec(`DELETE FROM checks WHERE kind IN ('type_mismatch', 'not_in_type')`); err != nil {
		return err
	}
	if r != nil {
		rows, err := w.tx.QueryContext(w.ctx, `SELECT path, folder, type, props, partial FROM pages WHERE props <> ''`)
		if err != nil {
			return err
		}
		type todo struct {
			path string
			rows []checkRow
		}
		var out []todo
		for rows.Next() {
			var p, folder, typ, props, partial string
			if err := rows.Scan(&p, &folder, &typ, &props, &partial); err != nil {
				rows.Close()
				return err
			}
			if rule := r.For(folder, typ); rule != nil {
				if cr := typeRows(props, partial, rule); len(cr) > 0 {
					out = append(out, todo{p, cr})
				}
			}
		}
		rows.Close()
		for _, t := range out {
			for i, c := range t.rows {
				if err := w.insertCheck(t.path, i, "", c); err != nil {
					return err
				}
			}
		}
	}
	fp := ""
	if r != nil {
		fp = r.Fingerprint
	}
	_, err := w.exec(`INSERT OR REPLACE INTO meta(key, value) VALUES ('types_fp', ?)`, fp)
	return err
}

// checkRow is a problem of a page that is served, before it is stored.
type checkRow struct {
	kind, message, detail, target string
}

func (w *writer) insertCheck(path string, seq int, id string, c checkRow) error {
	_, err := w.exec(`INSERT OR REPLACE INTO checks(kind, path, seq, id, message, detail, target) VALUES (?,?,?,?,?,?,?)`,
		c.kind, path, seq, id, c.message, c.detail, c.target)
	return err
}

func jsonObj(key string, v any) string {
	b, _ := json.Marshal(map[string]any{key: v})
	return string(b)
}

type propVal struct {
	key string
	val any
}

// decodeProps reads the pages.props column in the order it was written.
func decodeProps(s string) []propVal {
	dec := json.NewDecoder(strings.NewReader(s))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var out []propVal
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return out
		}
		key, _ := kt.(string)
		var v any
		if err := dec.Decode(&v); err != nil {
			return out
		}
		out = append(out, propVal{key, v})
	}
	return out
}

// strayKeys are the keys of a header that the type does not have: those with a
// value, except the ones the application keeps for itself. propsJSON and
// partialJSON are the pages.props and pages.partial columns.
func strayKeys(vals []propVal, partial []string, rule *TypeRule) []string {
	var out []string
	seen := map[string]bool{}
	add := func(k string) {
		if seen[k] || page.IsReservedKey(k) {
			return
		}
		seen[k] = true
		if _, ok := rule.Fields[k]; !ok {
			out = append(out, k)
		}
	}
	for _, v := range vals {
		add(v.key)
	}
	for _, k := range partial {
		add(k)
	}
	return out
}

func parsePartial(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	json.Unmarshal([]byte(s), &out)
	return out
}

// StrayFields lists the header fields of a page that its type does not have,
// in the order of the header: what the not_in_type fix takes out.
func StrayFields(doc *page.Doc, rule *TypeRule) []string {
	if rule == nil {
		return nil
	}
	_, props, partial := propsOf(doc.Meta(), doc.InexactKeys())
	return strayKeys(decodeProps(props), partial, rule)
}

// typeRows are the type problems of a page: fields whose value does not fit
// the type, and fields the type does not have.
func typeRows(propsJSON, partialJSON string, rule *TypeRule) []checkRow {
	vals := decodeProps(propsJSON)
	partial := parsePartial(partialJSON)
	partialSet := make(map[string]bool, len(partial))
	for _, k := range partial {
		partialSet[k] = true
	}
	var bad []string
	for _, v := range vals {
		f, ok := rule.Fields[v.key]
		// A copy that is not the whole value cannot be judged.
		if ok && !partialSet[v.key] && !fieldFits(f, v.val) {
			bad = append(bad, v.key)
		}
	}
	var out []checkRow
	if len(bad) > 0 {
		out = append(out, checkRow{kind: KindTypeMismatch, message: "These fields do not fit their type: " + strings.Join(bad, ", "), detail: jsonObj("fields", bad)})
	}
	if stray := strayKeys(vals, partial, rule); len(stray) > 0 {
		out = append(out, checkRow{kind: KindNotInType, message: "These fields are not in the page's type: " + strings.Join(stray, ", "), detail: jsonObj("fields", stray)})
	}
	return out
}

// fieldFits reports whether a value (as the props column holds it) is one the
// field takes.
func fieldFits(f FieldRule, v any) bool {
	inOptions := func(s string) bool {
		if len(f.Options) == 0 {
			return true
		}
		for _, o := range f.Options {
			if o == s {
				return true
			}
		}
		return false
	}
	switch f.Kind {
	case FieldText, FieldLongText:
		_, ok := v.(string)
		return ok
	case FieldSelect:
		s, ok := v.(string)
		return ok && inOptions(s)
	case FieldMultiSelect:
		l, ok := v.([]any)
		if !ok {
			return false
		}
		for _, e := range l {
			if s, ok := e.(string); !ok || !inOptions(s) {
				return false
			}
		}
		return true
	case FieldDate:
		s, ok := v.(string)
		if !ok {
			return false
		}
		_, err := time.Parse("2006-01-02", s)
		return err == nil
	case FieldNumber:
		_, ok := v.(float64)
		return ok
	case FieldCheckbox:
		_, ok := v.(bool)
		return ok
	case FieldURL:
		s, ok := v.(string)
		if !ok {
			return false
		}
		u, err := url.Parse(s)
		return err == nil && u.Scheme != ""
	}
	return true // other: whatever the header holds
}

// --- attachments ---------------------------------------------------------------

// assetRef is an address in a page that points into the attachments folder.
type assetRef struct{ written, rel string }

var reScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// resolveAsset is the path from the root of the space an address written in a
// page of folder points at, when that is a file under assets/ (the rules of
// the client: a scheme, "//" or "#" is not a file of the space, a leading "/"
// starts at the root, anything else at the folder of the page; "." and ".."
// are resolved and never leave the root).
func resolveAsset(address, folder string) (string, bool) {
	if address == "" || reScheme.MatchString(address) || strings.HasPrefix(address, "//") || strings.HasPrefix(address, "#") {
		return "", false
	}
	if i := strings.IndexAny(address, "?#"); i >= 0 {
		address = address[:i]
	}
	var segs []string
	if !strings.HasPrefix(address, "/") && folder != "" {
		segs = strings.Split(folder, "/")
	}
	for _, raw := range strings.Split(address, "/") {
		dec, err := url.PathUnescape(raw)
		if err != nil {
			dec = raw
		}
		for _, s := range strings.FieldsFunc(dec, func(r rune) bool { return r == '/' || r == '\\' }) {
			switch s {
			case ".":
			case "..":
				if len(segs) > 0 {
					segs = segs[:len(segs)-1]
				}
			default:
				segs = append(segs, s)
			}
		}
	}
	if len(segs) < 2 || segs[0] != "assets" {
		return "", false
	}
	return strings.Join(segs, "/"), true
}

// assetRefs finds the images and links of a page body that point into assets/.
// Code blocks are left out. It is not a Markdown parser: it reads "](address)"
// the way an inline link or image is written, with an address in angle
// brackets or with balanced parentheses, and ignores a title after it.
func assetRefs(body, folder string) []assetRef {
	if !strings.Contains(body, "](") {
		return nil
	}
	var out []assetRef
	seen := map[string]bool{}
	inFence := false
	var fence byte
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if len(t) >= 3 && (t[0] == '`' || t[0] == '~') && strings.Count(t[:3], string(t[0])) == 3 {
			if !inFence {
				inFence, fence = true, t[0]
			} else if t[0] == fence {
				inFence = false
			}
			continue
		}
		if inFence {
			continue
		}
		for rest := line; ; {
			i := strings.Index(rest, "](")
			if i < 0 {
				break
			}
			rest = rest[i+2:]
			addr, n := linkAddress(rest)
			rest = rest[n:]
			if rel, ok := resolveAsset(addr, folder); ok && !seen[rel] && len(out) < maxRowsPerKind {
				seen[rel] = true
				out = append(out, assetRef{addr, rel})
			}
		}
	}
	return out
}

// linkAddress reads the address after "](" and says how much it used.
func linkAddress(s string) (string, int) {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	if i < len(s) && s[i] == '<' {
		if j := strings.IndexByte(s[i:], '>'); j > 0 {
			return s[i+1 : i+j], i + j + 1
		}
		return "", len(s)
	}
	start, depth := i, 0
	for ; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			i++
			continue
		}
		if c == '(' {
			depth++
		} else if c == ')' {
			if depth == 0 {
				break
			}
			depth--
		} else if c == ' ' || c == '\t' {
			break
		}
	}
	return s[start:i], i
}

// annotate computes the problems of a page that is served, from what is
// already known of it, with the rules r.
func (x *Index) annotate(e *Entry, r *TypeRules) {
	var rows []checkRow
	if r != nil && r.For != nil && e.PropsJSON != "" {
		if rule := r.For(e.Folder, e.Type); rule != nil {
			rows = append(rows, typeRows(e.PropsJSON, e.PartialJSON, rule)...)
		}
	}
	if fn := x.assetExists.Load(); fn != nil {
		for _, ref := range e.refs {
			if !(*fn)(ref.rel) {
				rows = append(rows, checkRow{kind: KindMissingAsset, message: "The file " + ref.written + " does not exist", detail: jsonObj("target", ref.written), target: ref.rel})
			}
		}
	}
	e.checks = rows
}

// AssetAppeared forgets the missing-attachment problems about a file that now
// exists (a file of the space, as a path from its root).
func (x *Index) AssetAppeared(rel string) error {
	x.wmu.Lock()
	defer x.unlockWrite()
	var n int
	if err := x.wdb.QueryRowContext(bg, `SELECT count(*) FROM checks WHERE kind = 'missing_asset' AND target = ?`, rel).Scan(&n); err != nil || n == 0 {
		return err
	}
	tx, err := x.wdb.BeginTx(bg, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(bg, `DELETE FROM checks WHERE kind = 'missing_asset' AND target = ?`, rel); err != nil {
		return err
	}
	return x.commit(tx)
}

// --- links ---------------------------------------------------------------------

// A link is broken when it carries an id that no page has, or, written with a
// path only, names a file that holds no page (the rule of OutLinks). Whether
// it is depends on other pages than its own, so the broken_link rows (one per
// link, seq the link's place in its page, at most maxRowsPerKind a page) are
// kept up to date from the links table: the writer notes the pages whose links
// changed and the ids and paths that appeared or went away (markLinkSrc,
// markLinkTarget), and finishLinks looks again at the pages that have links to
// those, through the indexes on links. A rebuild does them all with one query.

// markLinkSrc notes that the links of the page rowid changed.
func (w *writer) markLinkSrc(rowid int64) {
	if w.allLinks {
		return
	}
	if w.linkSrcs == nil {
		w.linkSrcs = map[int64]bool{}
	}
	w.linkSrcs[rowid] = true
}

// markLinkTarget notes that a page with this id ("" for none), at this path
// ("" for none), appeared or went away.
func (w *writer) markLinkTarget(id, path string) {
	if w.allLinks {
		return
	}
	if id != "" {
		if w.linkIDs == nil {
			w.linkIDs = map[string]bool{}
		}
		w.linkIDs[id] = true
	}
	if path != "" {
		if w.linkPaths == nil {
			w.linkPaths = map[string]bool{}
		}
		w.linkPaths[path] = true
	}
}

// brokenRows selects the broken_link rows of the links l, joined with their
// page s. The detail is {"target": the path the link names, "line": n}.
const brokenRows = `SELECT 'broken_link', s.path, l.pos, s.id,
	CASE WHEN l.dst_id <> '' THEN 'No page has the id ' || l.dst_id ELSE 'No page is at ' || l.dst_path END,
	json_object('target', l.dst_path, 'line', l.line), ''`

const linkResolves = `CASE WHEN l.dst_id <> '' THEN EXISTS (SELECT 1 FROM pages t WHERE t.id = l.dst_id)
	ELSE EXISTS (SELECT 1 FROM pages t WHERE t.path = l.dst_path) END`

// maxLinkSrcs is the most pages finishLinks looks at one by one; past it, one
// query over all links is cheaper.
const maxLinkSrcs = 2000

// finishLinks brings the broken_link rows in line with what was noted.
func (w *writer) finishLinks() error {
	all := w.allLinks
	srcs := w.linkSrcs
	if !all && (len(w.linkIDs) > 0 || len(w.linkPaths) > 0) {
		if srcs == nil {
			srcs = map[int64]bool{}
		}
		add := func(q string, keys map[string]bool) error {
			for k := range keys {
				rows, err := w.tx.QueryContext(w.ctx, q, k)
				if err != nil {
					return err
				}
				for rows.Next() {
					var src int64
					if err := rows.Scan(&src); err != nil {
						rows.Close()
						return err
					}
					srcs[src] = true
				}
				rows.Close()
				if err := rows.Err(); err != nil {
					return err
				}
				if len(srcs) > maxLinkSrcs {
					return nil
				}
			}
			return nil
		}
		if err := add(`SELECT src FROM links WHERE dst_id = ?`, w.linkIDs); err != nil {
			return err
		}
		if len(srcs) <= maxLinkSrcs {
			if err := add(`SELECT src FROM links WHERE dst_id = '' AND dst_path = ?`, w.linkPaths); err != nil {
				return err
			}
		}
	}
	w.linkSrcs, w.linkIDs, w.linkPaths, w.allLinks = nil, nil, nil, false
	if all || len(srcs) > maxLinkSrcs {
		if _, err := w.exec(`DELETE FROM checks WHERE kind = 'broken_link'`); err != nil {
			return err
		}
		_, err := w.exec(`INSERT INTO checks(kind, path, seq, id, message, detail, target) ` + brokenRows + `
			FROM (SELECT l.*, row_number() OVER (PARTITION BY l.src ORDER BY l.pos) AS n FROM links l WHERE NOT (` + linkResolves + `)) l
			JOIN pages s ON s.rowid = l.src WHERE l.n <= ` + strconv.Itoa(maxRowsPerKind))
		return err
	}
	for src := range srcs {
		var p string
		switch err := w.queryRow(`SELECT path FROM pages WHERE rowid = ?`, src).Scan(&p); {
		case errors.Is(err, sql.ErrNoRows):
			continue // gone: its rows went with its path
		case err != nil:
			return err
		}
		if _, err := w.exec(`DELETE FROM checks WHERE kind = 'broken_link' AND path = ?`, p); err != nil {
			return err
		}
		if _, err := w.exec(`INSERT INTO checks(kind, path, seq, id, message, detail, target) `+brokenRows+`
			FROM links l JOIN pages s ON s.rowid = l.src WHERE l.src = ? AND NOT (`+linkResolves+`) ORDER BY l.pos LIMIT `+strconv.Itoa(maxRowsPerKind), src); err != nil {
			return err
		}
	}
	return nil
}

// finish brings the problems that depend on other pages than their own in
// line with the writes of the transaction, before it commits.
func (w *writer) finish() error {
	if err := w.finishTitles(); err != nil {
		return err
	}
	if err := w.finishLinks(); err != nil {
		return err
	}
	return w.flushFeed()
}

// --- titles ------------------------------------------------------------------

type titleGroup struct{ folder, key string }

// markTitle notes that the pages of a folder with this title key may have
// changed in number. An untitled page is in no group.
func (w *writer) markTitle(folder, key string, untitled bool) {
	if untitled {
		return
	}
	if w.dirty == nil {
		w.dirty = map[titleGroup]bool{}
	}
	w.dirty[titleGroup{folder, key}] = true
}

const dupTitleMessage = "Another page in this folder has the same title"

// finishTitles brings the duplicate_title rows in line with the groups that
// were touched. Many groups (a rebuild) are done by one query over the pages.
func (w *writer) finishTitles() error {
	if len(w.dirty) == 0 && !w.allTitles {
		return nil
	}
	if w.allTitles || len(w.dirty) > 200 {
		w.dirty, w.allTitles = nil, false
		if _, err := w.exec(`DELETE FROM checks WHERE kind = 'duplicate_title'`); err != nil {
			return err
		}
		_, err := w.exec(`INSERT INTO checks(kind, path, seq, id, message, detail, target)
			SELECT 'duplicate_title', p.path, 0, p.id, ?, '', '' FROM pages p
			JOIN (SELECT folder, title_key FROM pages WHERE untitled = 0 GROUP BY folder, title_key HAVING count(*) > 1) d
			ON p.folder = d.folder AND p.title_key = d.title_key WHERE p.untitled = 0`, dupTitleMessage)
		return err
	}
	groups := w.dirty
	w.dirty = nil
	for g := range groups {
		rows, err := w.tx.QueryContext(w.ctx, `SELECT path, id FROM pages WHERE folder = ? AND title_key = ? AND untitled = 0`, g.folder, g.key)
		if err != nil {
			return err
		}
		var paths, ids []string
		for rows.Next() {
			var p, id string
			if err := rows.Scan(&p, &id); err != nil {
				rows.Close()
				return err
			}
			paths, ids = append(paths, p), append(ids, id)
		}
		rows.Close()
		for i, p := range paths {
			if len(paths) > 1 {
				err = w.insertCheck(p, 0, ids[i], checkRow{kind: KindDuplicateTitle, message: dupTitleMessage})
			} else {
				_, err = w.exec(`DELETE FROM checks WHERE kind = 'duplicate_title' AND path = ?`, p)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// --- reading -------------------------------------------------------------------

// Check is a problem as the check page lists it.
type Check struct {
	Kind, Path, ID, Message string
	Detail                  json.RawMessage // nil for none
	// Rev is the rev of the file the problem is in, as indexed: the baseRev of
	// a fix ("" when it is not known).
	Rev string
	// Fix is "add_id", "remove_fields" or "" (not fixable).
	Fix string
}

// FixOf is the fix a kind of problem has: what the API calls the fix.
func FixOf(kind string) string {
	switch kind {
	case KindNoID:
		return "add_id"
	case KindNotInType:
		return "remove_fields"
	}
	return ""
}

// Checks lists the problems of one kind (all kinds when kind is "") after the
// cursor, CheckPage at a time, ordered by kind (the order of CheckKinds) and
// path, with the number of problems of each kind. next is the cursor of the
// following page, "" after the last. It is a few index queries: nothing grows
// with the number of pages.
func (x *Index) Checks(kind, cursor string) (counts map[string]int, items []Check, next string, err error) {
	kinds := CheckKinds
	if kind != "" {
		if !isCheckKind(kind) {
			return nil, nil, "", fmt.Errorf("%w: unknown kind %q", ErrInvalidQuery, kind)
		}
		kinds = []string{kind}
	}
	var cur struct {
		kind string
		seq  int
		path string
		set  bool
	}
	if cursor != "" {
		b, derr := base64.RawURLEncoding.DecodeString(cursor)
		parts := strings.SplitN(string(b), "|", 3)
		if derr != nil || len(parts) != 3 || !isCheckKind(parts[0]) {
			return nil, nil, "", fmt.Errorf("%w: cursor is not one this listing gave", ErrInvalidQuery)
		}
		n, serr := strconv.Atoi(parts[1])
		if serr != nil {
			return nil, nil, "", fmt.Errorf("%w: cursor is not one this listing gave", ErrInvalidQuery)
		}
		cur.kind, cur.seq, cur.path, cur.set = parts[0], n, parts[2], true
	}
	r, err := x.begin()
	if err != nil {
		return nil, nil, "", err
	}
	defer r.end()
	counts, err = r.checkCounts()
	if err != nil {
		return nil, nil, "", err
	}
	rank := func(k string) int {
		for i, c := range CheckKinds {
			if c == k {
				return i
			}
		}
		return len(CheckKinds)
	}
	var seqs []int
	for _, k := range kinds {
		if cur.set && rank(k) < rank(cur.kind) {
			continue
		}
		need := CheckPage + 1 - len(items)
		if need <= 0 {
			break
		}
		afterPath, afterSeq, after := "", 0, cur.set && k == cur.kind
		if after {
			afterPath, afterSeq = cur.path, cur.seq
		}
		got, gotSeq, err := r.readKind(k, afterPath, afterSeq, after, need)
		if err != nil {
			return nil, nil, "", err
		}
		items, seqs = append(items, got...), append(seqs, gotSeq...)
	}
	if len(items) > CheckPage {
		last := CheckPage - 1
		next = base64.RawURLEncoding.EncodeToString([]byte(items[last].Kind + "|" + strconv.Itoa(seqs[last]) + "|" + items[last].Path))
		items = items[:CheckPage]
	}
	for i := range items {
		if err := r.enrich(&items[i]); err != nil {
			return nil, nil, "", err
		}
		items[i].Fix = FixOf(items[i].Kind)
	}
	if items == nil {
		items = []Check{}
	}
	return counts, items, next, nil
}

// checkCounts is how many problems of each kind there are.
func (r *read) checkCounts() (map[string]int, error) {
	counts := map[string]int{}
	for _, k := range CheckKinds {
		counts[k] = 0
	}
	for _, q := range []string{`SELECT kind, count(*) FROM problems WHERE kind <> '' GROUP BY kind`, `SELECT kind, count(*) FROM checks GROUP BY kind`} {
		rows, err := r.db.Query(q)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k string
			var n int
			if err := rows.Scan(&k, &n); err != nil {
				rows.Close()
				return nil, err
			}
			counts[k] = n
		}
		rows.Close()
	}
	return counts, nil
}

// CheckCounts is how many problems of each kind the index holds: the counts of
// Checks without a page of them (the notification centre reads it often).
func (x *Index) CheckCounts() (map[string]int, error) {
	r, err := x.begin()
	if err != nil {
		return nil, err
	}
	defer r.end()
	return r.checkCounts()
}

func isCheckKind(k string) bool {
	for _, c := range CheckKinds {
		if c == k {
			return true
		}
	}
	return false
}

// readKind reads up to limit problems of one kind after (path, seq).
func (r *read) readKind(kind, path string, seq int, after bool, limit int) ([]Check, []int, error) {
	var rows *sql.Rows
	var err error
	if fileKinds[kind] {
		q := `SELECT path, id, message, line, rev FROM problems WHERE kind = ?`
		args := []any{kind}
		if after {
			q += ` AND path > ?`
			args = append(args, path)
		}
		rows, err = r.db.Query(q+` ORDER BY path LIMIT ?`, append(args, limit)...)
	} else {
		q := `SELECT c.path, c.id, c.message, c.detail, c.seq, coalesce(p.rev, '') FROM checks c LEFT JOIN pages p ON p.path = c.path WHERE c.kind = ?`
		args := []any{kind}
		if after {
			q += ` AND (c.path, c.seq) > (?, ?)`
			args = append(args, path, seq)
		}
		rows, err = r.db.Query(q+` ORDER BY c.path, c.seq LIMIT ?`, append(args, limit)...)
	}
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []Check
	var seqs []int
	for rows.Next() {
		c := Check{Kind: kind}
		var n int
		if fileKinds[kind] {
			var line int
			if err := rows.Scan(&c.Path, &c.ID, &c.Message, &line, &c.Rev); err != nil {
				return nil, nil, err
			}
			if kind == KindBadHeader && line > 0 {
				c.Detail = json.RawMessage(`{"line":` + strconv.Itoa(line) + `}`)
			}
		} else {
			var detail string
			if err := rows.Scan(&c.Path, &c.ID, &c.Message, &detail, &n, &c.Rev); err != nil {
				return nil, nil, err
			}
			if detail != "" {
				c.Detail = json.RawMessage(detail)
			}
		}
		out, seqs = append(out, c), append(seqs, n)
	}
	return out, seqs, rows.Err()
}

// enrich adds what is read for the page of the listing only: the paths that
// share an id or a title.
func (r *read) enrich(c *Check) error {
	var paths []string
	switch c.Kind {
	case KindDuplicateID:
		if c.ID == "" {
			return nil
		}
		var served string
		if err := r.db.QueryRow(`SELECT path FROM pages WHERE id = ?`, c.ID).Scan(&served); err == nil {
			paths = append(paths, served)
		} else if err != sql.ErrNoRows {
			return err
		}
		rows, err := r.db.Query(`SELECT path FROM problems WHERE kind = 'duplicate_id' AND id = ? ORDER BY path LIMIT ?`, c.ID, MaxCheckPaths)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				return err
			}
			paths = append(paths, p)
		}
	case KindDuplicateTitle:
		var folder, key string
		if err := r.db.QueryRow(`SELECT folder, title_key FROM pages WHERE path = ?`, c.Path).Scan(&folder, &key); err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			return err
		}
		rows, err := r.db.Query(`SELECT path FROM pages WHERE folder = ? AND title_key = ? AND untitled = 0 ORDER BY path LIMIT ?`, folder, key, MaxCheckPaths)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				return err
			}
			paths = append(paths, p)
		}
	default:
		return nil
	}
	if len(paths) > 0 {
		b, _ := json.Marshal(map[string]any{"paths": paths})
		c.Detail = bytes.Clone(b)
	}
	return nil
}

// IDByPath returns the id of the page whose file is at rel.
func (x *Index) IDByPath(rel string) (string, bool) {
	var id string
	err := x.one(func(db querier) error {
		return db.QueryRow(`SELECT id FROM pages WHERE path = ?`, rel).Scan(&id)
	})
	return id, err == nil
}
