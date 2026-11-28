package api

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/store"
)

// The usage export (docs/api.md, "Usage export"): a tenant's rows of
// tenant_usage_daily for a range of days, one row per day and key, as CSV or
// JSON Lines. The counters are written to the table every minute; the export
// writes what is held in memory first, so that today's row is as current as
// the request.

const (
	// usageExportMaxDays is the most days one export covers, both ends included.
	usageExportMaxDays = 366
	// usageExportDefaultDays is the range when from is left out: this many days
	// ending at to.
	usageExportDefaultDays = 30
)

// usageExportRow is one line of the JSON Lines format.
type usageExportRow struct {
	Tenant string `json:"tenant"`
	Day    string `json:"day"`
	Key    string `json:"key"`
	Value  int64  `json:"value"`
}

func (s *server) adminUsageExport(c *call) {
	id := c.r.PathValue("tenant")
	if !c.p.Access.Can(authz.TenantUsageRead, id) {
		tenantNotFound(c)
		return
	}
	ctx := c.r.Context()
	if _, err := store.GetTenant(ctx, s.opt.Store.Q(), id); errors.Is(err, store.ErrNotFound) {
		tenantNotFound(c)
		return
	} else if err != nil {
		s.fail(c.w, err)
		return
	}
	query := c.r.URL.Query()
	format := query.Get("format")
	switch format {
	case "":
		format = "csv"
	case "csv", "jsonl":
	default:
		writeError(c.w, http.StatusBadRequest, "invalid", "The format is csv or jsonl.")
		return
	}
	day := func(name string, def time.Time) (time.Time, bool) {
		v := query.Get(name)
		if v == "" {
			return def, true
		}
		t, err := time.Parse(time.DateOnly, v)
		if err != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", "The "+name+" day is written as 2026-11-23.")
			return t, false
		}
		return t, true
	}
	today := s.now().UTC().Truncate(24 * time.Hour)
	to, ok := day("to", today)
	if !ok {
		return
	}
	from, ok := day("from", to.AddDate(0, 0, 1-usageExportDefaultDays))
	if !ok {
		return
	}
	if from.After(to) {
		writeError(c.w, http.StatusBadRequest, "invalid", "The first day is after the last.")
		return
	}
	if int(to.Sub(from).Hours()/24)+1 > usageExportMaxDays {
		writeError(c.w, http.StatusBadRequest, "invalid", "An export covers at most "+strconv.Itoa(usageExportMaxDays)+" days.")
		return
	}
	if s.limits != nil {
		if err := s.limits.meter.FlushTenant(ctx, s.opt.Store, id); err != nil {
			s.fail(c.w, err)
			return
		}
	}
	rows, err := store.ListUsageDaily(ctx, s.opt.Store.Q(), id, store.UsageDay(from), store.UsageDay(to))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	h := c.w.Header()
	name := "usage-" + id + "-" + store.UsageDay(from) + "-" + store.UsageDay(to)
	h.Set("Cache-Control", "no-store")
	if format == "jsonl" {
		h.Set("Content-Type", "application/x-ndjson; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="`+name+`.jsonl"`)
		enc := json.NewEncoder(c.w)
		for _, r := range rows {
			if err := enc.Encode(usageExportRow{r.TenantID, r.Day, r.Key, r.Value}); err != nil {
				break
			}
		}
	} else {
		h.Set("Content-Type", "text/csv; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
		w := csv.NewWriter(c.w)
		_ = w.Write([]string{"tenant", "day", "key", "value"})
		for _, r := range rows {
			if err := w.Write([]string{r.TenantID, r.Day, r.Key, strconv.FormatInt(r.Value, 10)}); err != nil {
				break
			}
		}
		w.Flush()
	}
	s.auditRow(c, "usage_exported", id, map[string]any{"from": store.UsageDay(from), "to": store.UsageDay(to), "format": format, "rows": len(rows)})
}
