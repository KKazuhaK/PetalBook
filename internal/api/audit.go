package api

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"petalbook.app/server/internal/auditlog"
	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/store"
)

// auditDownloadMax is the most rows one download holds; the answer says so
// (X-Audit-Truncated) when there were more.
var auditDownloadMax = 100_000

// auditFilter reads the filters of the log from the query: category (repeated
// or separated by commas), kind, user (the person a row is about), actor,
// capacity, from and to (an RFC 3339 time, or a day; a day in to includes it),
// cursor. The scope is the caller's, narrowed by ?tenant=. It has answered the
// request itself when it returns false.
func (s *server) auditFilter(c *call) (store.Scope, store.AuditFilter, bool) {
	var f store.AuditFilter
	sc, ok := s.adminScope(c)
	if !ok {
		return sc, f, false
	}
	q := c.r.URL.Query()
	bad := func(msg string) (store.Scope, store.AuditFilter, bool) {
		writeError(c.w, http.StatusBadRequest, "invalid", msg)
		return sc, f, false
	}
	for _, v := range q["category"] {
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name == "" {
				continue
			}
			if _, ok := auditlog.CategoryInfo(auditlog.Category(name)); !ok {
				return bad("Unknown category " + name + ".")
			}
			f.Categories = append(f.Categories, name)
		}
	}
	f.Kind = strings.TrimSpace(q.Get("kind"))
	f.User = strings.TrimSpace(q.Get("user"))
	f.Actor = strings.TrimSpace(q.Get("actor"))
	if f.Capacity = strings.TrimSpace(q.Get("capacity")); f.Capacity != "" && !auditlog.ValidCapacity(f.Capacity) {
		return bad("Capacity is site_admin or tenant_admin.")
	}
	var err error
	if f.From, _, err = auditTime(q.Get("from")); err != nil {
		return bad("From is a time (RFC 3339) or a day (YYYY-MM-DD).")
	}
	var day bool
	if f.To, day, err = auditTime(q.Get("to")); err != nil {
		return bad("To is a time (RFC 3339) or a day (YYYY-MM-DD).")
	}
	if day {
		f.To = f.To.AddDate(0, 0, 1)
	}
	f.Cursor = q.Get("cursor")
	f.HideSite = !c.siteWide(authz.TenantAuditRead)
	return sc, f, true
}

// auditTime reads a time of a filter: "" is no bound; a day says so (day), the
// caller takes its end.
func auditTime(v string) (t time.Time, day bool, err error) {
	if v = strings.TrimSpace(v); v == "" {
		return t, false, nil
	}
	if t, err = time.Parse("2006-01-02", v); err == nil {
		return t, true, nil
	}
	t, err = time.Parse(time.RFC3339, v)
	return t.UTC(), false, err
}

// pathKinds are the kinds whose detail names a file by its path, a name the
// person who uploaded it chose.
var pathKinds = map[string]bool{"file_read": true, "file_downloaded": true, "page_viewed": true}

// pathRedactor returns the function that makes a row's detail safe for this
// reader: a row that names a file by its path says "a file in <space>" instead
// unless the reader could open that space themselves: a member (directly or
// through a group), someone with a view of it open, or a server administrator
// who holds the data-view key across the site. A tenant's administrator who is
// none of these, and has no view, learns that a file was read, not what it is
// called. The row keeps the path; only what is shown changes. The answer for a
// space is asked once for the whole list or download.
func (s *server) pathRedactor(c *call) func(kind string, detail json.RawMessage) json.RawMessage {
	open := map[string]bool{}
	canOpen := func(space string) bool {
		if v, ok := open[space]; ok {
			return v
		}
		v := c.siteWide(authz.TenantDataView) || s.viewing(c, space)
		if !v {
			_, err := s.spaces.OpenMembership(c.r.Context(), c.user(), space)
			v = err == nil
		}
		open[space] = v
		return v
	}
	return func(kind string, detail json.RawMessage) json.RawMessage {
		if !pathKinds[kind] || len(detail) == 0 {
			return detail
		}
		var d map[string]any
		if json.Unmarshal(detail, &d) != nil {
			return detail
		}
		if _, ok := d["path"].(string); !ok {
			return detail
		}
		space, _ := d["space"].(string)
		if canOpen(space) {
			return detail
		}
		d["path"] = "a file in " + space
		out, err := json.Marshal(d)
		if err != nil {
			return json.RawMessage(`{}`)
		}
		return out
	}
}

// addressKinds are the kinds whose detail names an email address of the
// person the row is about, under these keys.
var addressKinds = map[string][]string{
	"email_added": {"email"}, "email_verified": {"email"}, "email_removed": {"email"},
	"email_claim_withdrawn": {"email"}, "email_primary_changed": {"email", "old"},
}

// hiddenAddress is what a row says in place of an address its reader may not see.
const hiddenAddress = "(hidden)"

// addressRedactor returns the function that makes a row's detail safe for this
// reader: a site administrator across the whole site sees every address; any
// other reader (a tenant's administrator, a site administrator in a tenant's
// scope) sees the person's primary address and the addresses in the tenant's
// verified domains, and the rest read "(hidden)". The person's primary address
// is asked once for each person in the list.
func (s *server) addressRedactor(c *call) func(kind, user, tenant string, detail json.RawMessage) json.RawMessage {
	if c.siteWide(authz.TenantAuditRead) {
		return func(_, _, _ string, detail json.RawMessage) json.RawMessage { return detail }
	}
	primary := map[string]string{}
	return func(kind, user, tenant string, detail json.RawMessage) json.RawMessage {
		keys := addressKinds[kind]
		if len(keys) == 0 || len(detail) == 0 {
			return detail
		}
		var d map[string]any
		if json.Unmarshal(detail, &d) != nil {
			return detail
		}
		p, seen := primary[user]
		if !seen {
			if u, err := store.GetUser(c.r.Context(), s.opt.Store.Q(), store.AllTenants(), user); err == nil {
				p = u.Email
			}
			primary[user] = p
		}
		changed := false
		for _, k := range keys {
			if a, ok := d[k].(string); ok && a != "" && !s.auth.AddressShownToTenantAdmin(c.r.Context(), tenant, p, a) {
				d[k], changed = hiddenAddress, true
			}
		}
		if !changed {
			return detail
		}
		out, err := json.Marshal(d)
		if err != nil {
			return json.RawMessage(`{}`)
		}
		return out
	}
}

func (s *server) adminAudit(c *call) {
	q := c.r.URL.Query()
	limit := 50
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > maxAdminLimit {
			writeError(c.w, http.StatusBadRequest, "invalid", "Limit is a number from 1 to "+strconv.Itoa(maxAdminLimit)+".")
			return
		}
		limit = n
	}
	sc, f, ok := s.auditFilter(c)
	if !ok {
		return
	}
	rows, next, err := s.auth.ListAudit(c.r.Context(), sc, f, limit)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	type item struct {
		ID       string `json:"id"`
		At       string `json:"at"`
		Kind     string `json:"kind"`
		Category string `json:"category"`
		Capacity any    `json:"capacity"`
		Actor    any    `json:"actor"`
		User     any    `json:"user"`
		Username string `json:"username"`
		IP       string `json:"ip"`
		Detail   any    `json:"detail"`
	}
	nullable := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	redact, redactAddr := s.pathRedactor(c), s.addressRedactor(c)
	items := make([]item, len(rows))
	for i, r := range rows {
		items[i] = item{r.ID, rfc3339(r.At), r.Kind, r.Category, nullable(r.Capacity), nullable(r.Actor), nullable(r.User), r.Username, r.IP, redactAddr(r.Kind, r.User, r.Tenant, redact(r.Kind, r.Detail))}
	}
	var nx any
	if next != "" {
		nx = next
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []item `json:"items"`
		Next  any    `json:"next"`
	}{items, nx})
}

// adminAuditKinds is the registry of kinds for the filters: the categories in
// order, each with its kinds, whether it is core, and whether an optional one is
// on in the caller's scope. A tenant's administrator is not told of the kinds
// only the server's administrators see.
func (s *server) adminAuditKinds(c *call) {
	sc, ok := s.adminScope(c)
	if !ok {
		return
	}
	hide := !c.siteWide(authz.TenantAuditRead)
	tenant := ""
	if !sc.All() {
		tenant = sc.Tenant()
	}
	type kindOut struct {
		Name string `json:"name"`
		Site bool   `json:"site"`
	}
	type catOut struct {
		ID      string    `json:"id"`
		Core    bool      `json:"core"`
		Setting any       `json:"setting"`
		On      bool      `json:"on"`
		Kinds   []kindOut `json:"kinds"`
	}
	kinds := auditlog.Kinds()
	var out []catOut
	for _, ci := range auditlog.Categories() {
		co := catOut{ID: string(ci.ID), Core: ci.Core, On: true, Kinds: []kindOut{}}
		if !ci.Core {
			co.Setting = ci.Setting
			co.On = s.auth.AuditCategoryOn(tenant, ci.ID)
		}
		for _, k := range kinds {
			if k.Category == ci.ID && !(hide && k.Visibility == auditlog.Site) {
				co.Kinds = append(co.Kinds, kindOut{k.Name, k.Visibility == auditlog.Site})
			}
		}
		out = append(out, co)
	}
	writeJSON(c.w, http.StatusOK, struct {
		Categories []catOut `json:"categories"`
	}{out})
}

// auditDownloads are the callers with a download running: one at a time each.
var auditDownloads sync.Map

// adminAuditDownload streams the rows of the filter as CSV, a page of rows at a
// time, at most auditDownloadMax. A tenant's administrator gets their tenant's
// rows, never the kinds only the server's administrators see (the same filter
// as the list). The download is audited with the filter and the number of rows.
func (s *server) adminAuditDownload(c *call) {
	sc, f, ok := s.auditFilter(c)
	if !ok {
		return
	}
	f.Cursor = ""
	who := c.user().ID
	if _, busy := auditDownloads.LoadOrStore(who, struct{}{}); busy {
		writeError(c.w, http.StatusTooManyRequests, "rate_limited", "Another download of the audit log is running.")
		return
	}
	defer auditDownloads.Delete(who)

	w := csv.NewWriter(c.w)
	started := false
	start := func() {
		if started {
			return
		}
		started = true
		h := c.w.Header()
		h.Set("Content-Type", "text/csv; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="audit-`+s.now().UTC().Format("20060102-150405")+`.csv"`)
		h.Set("Cache-Control", "no-store")
		_ = w.Write([]string{"id", "time", "category", "kind", "capacity", "tenant", "actor", "user", "username", "ip", "detail"})
	}
	flusher, _ := c.w.(http.Flusher)
	redact, redactAddr := s.pathRedactor(c), s.addressRedactor(c)
	rows := 0
	n, more, err := s.auth.StreamAudit(c.r.Context(), sc, f, auditDownloadMax, func(e auth.AuditEntry) error {
		start()
		if err := w.Write([]string{csvCell(e.ID), rfc3339(e.At), csvCell(e.Category), csvCell(e.Kind), csvCell(e.Capacity), csvCell(e.Tenant),
			csvCell(e.Actor), csvCell(e.User), csvCell(e.Username), csvCell(e.IP), csvCell(string(redactAddr(e.Kind, e.User, e.Tenant, redact(e.Kind, e.Detail))))}); err != nil {
			return err
		}
		if rows++; rows%store.AuditStreamPage == 0 {
			w.Flush()
			if flusher != nil {
				flusher.Flush()
			}
		}
		return nil
	})
	if err != nil && !started {
		s.fail(c.w, err)
		return
	}
	start()
	if more {
		// Said in the body too, for a person who opens the file.
		note := make([]string, 11)
		note[0] = "Truncated: the first 100000 rows; narrow the filter to see the rest."
		_ = w.Write(note)
		// Said after the body, which is when it is known.
		c.w.Header().Set(http.TrailerPrefix+"X-Audit-Truncated", "true")
	}
	w.Flush()
	if err != nil {
		s.log.Warn("the audit download stopped", "error", err, "rows", n)
	}
	tenant := "-"
	if !sc.All() {
		tenant = sc.Tenant()
	}
	detail := map[string]any{"rows": n, "truncated": more}
	for k, v := range map[string]string{"kind": f.Kind, "user": f.User, "actor": f.Actor, "capacity": f.Capacity,
		"category": strings.Join(f.Categories, ","), "from": timeText(f.From), "to": timeText(f.To)} {
		if v != "" {
			detail[k] = v
		}
	}
	s.auditRow(c, "audit_downloaded", tenant, detail)
}

// adminAuditPurge deletes rows of the audit log by hand, whatever the retention
// settings say: POST {category?: a category or "all", before?: RFC 3339 time or
// a day} with the query dry_run=true to count what it would delete and delete
// nothing. Without category every category is covered; without before every
// row up to now. Site administrators only (the key site.audit.purge: a tenant's
// administrator, and a site administrator in a tenant's scope, are refused).
// The rows of kind audit_purged are never deleted. A purge that deletes writes
// one audit_purged row, after the delete, saying who, when and what.
func (s *server) adminAuditPurge(c *call) {
	var req struct {
		Category string `json:"category"`
		Before   string `json:"before"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	dry := false
	if v := c.r.URL.Query().Get("dry_run"); v != "" {
		var err error
		if dry, err = strconv.ParseBool(v); err != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", "Dry_run is true or false.")
			return
		}
	}
	p := store.AuditPurge{}
	if cat := strings.TrimSpace(req.Category); cat != "" && cat != "all" {
		if _, ok := auditlog.CategoryInfo(auditlog.Category(cat)); !ok {
			writeError(c.w, http.StatusBadRequest, "invalid", "Unknown category "+cat+".")
			return
		}
		p.Category = cat
	}
	// A purge covers what was written up to now, the rows written while it runs
	// excluded.
	end := s.now().Add(time.Millisecond)
	p.Before = end
	if v := strings.TrimSpace(req.Before); v != "" {
		t, _, err := auditTime(v)
		if err != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", "Before is a time (RFC 3339) or a day (YYYY-MM-DD).")
			return
		}
		if t.Before(end) {
			p.Before = t
		}
	}
	n, err := s.auth.PurgeAudit(c.r.Context(), p, dry)
	if err != nil {
		if !dry && n > 0 {
			// Some of it went: say so, once, before the error.
			s.auditRow(c, "audit_purged", "-", purgeDetail(p, n, true))
		}
		s.fail(c.w, err)
		return
	}
	if !dry {
		s.auditRow(c, "audit_purged", "-", purgeDetail(p, n, false))
	}
	writeJSON(c.w, http.StatusOK, struct {
		DryRun   bool   `json:"dryRun"`
		Count    int64  `json:"count"`
		Category string `json:"category"`
		Before   string `json:"before"`
	}{dry, n, purgeCategory(p.Category), rfc3339(p.Before)})
}

func purgeCategory(c string) string {
	if c == "" {
		return "all"
	}
	return c
}

// purgeDetail is the detail of an audit_purged row.
func purgeDetail(p store.AuditPurge, n int64, partial bool) map[string]any {
	d := map[string]any{"category": purgeCategory(p.Category), "before": rfc3339(p.Before), "deleted": n}
	if partial {
		d["partial"] = true
	}
	return d
}

func timeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return rfc3339(t)
}

// csvCell makes a cell safe to open in a spreadsheet: one that starts with a
// character a spreadsheet takes for a formula (= + - @, or a tab or return) gets
// a quote in front, which the spreadsheet shows as text. Leading spaces, returns
// and | are looked past, as a spreadsheet does.
func csvCell(v string) string {
	if v == "" {
		return v
	}
	if v[0] == '\t' || v[0] == '\r' {
		return "'" + v
	}
	// Spreadsheets skip leading blanks and | before they read a formula.
	if i := strings.IndexFunc(v, func(r rune) bool { return !strings.ContainsRune(" \t\r\n|", r) }); i >= 0 && strings.ContainsRune("=+-@", rune(v[i])) {
		return "'" + v
	}
	return v
}
