package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/dailyfetch"
	"github.com/KKazuhaK/PetalBook/internal/langtag"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// getDailySettings tells where the daily text of the space comes from.
func (s *server) getDailySettings(c *call) {
	s.answerDailySettings(c, c.sp.Space.Daily())
}

// answerDailySettings gives the source of the space, and whether it is in
// effect: not when the administrators do not let spaces use their own (what
// was saved is kept, and works again when they do).
func (s *server) answerDailySettings(c *call, d *space.DailyConfig) {
	inEffect := true
	if !s.single {
		allow, err := s.dailyAllowFor(c.r.Context(), c.user().TenantID)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		inEffect = allow.Own
	}
	writeJSON(c.w, http.StatusOK, struct {
		Daily    *space.DailyConfig `json:"daily"`
		InEffect bool               `json:"inEffect"`
	}{d, inEffect})
}

// putDailySettings sets, or with null removes, where the daily text comes from.
func (s *server) putDailySettings(c *call) {
	var req map[string]json.RawMessage
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	raw, ok := req["daily"]
	if !ok || len(req) != 1 {
		writeError(c.w, http.StatusBadRequest, "invalid", "give daily: the source of the daily text, or null to remove it")
		return
	}
	var cfg *space.DailyConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		var inv *space.InvalidError
		if errors.As(err, &inv) && inv.Code != "" {
			writeCoded(c.w, &codedError{code: inv.Code, msg: "daily: " + inv.Msg})
			return
		}
		writeError(c.w, http.StatusBadRequest, "invalid", "daily: "+err.Error())
		return
	}
	d, err := c.sp.Space.SetDaily(s.author(c), cfg)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.answerDailySettings(c, d)
}

type dailyPageResp struct {
	summaryResp
	// Body is the page's Markdown, cut to space.DailyBodyLimit characters.
	Body          string `json:"body"`
	BodyTruncated bool   `json:"bodyTruncated"`
}

// getDaily answers with the daily text of a day, from the first of: the
// space's own source, the external interface the space chose, the server's
// default (see dailysite.go).
func (s *server) getDaily(c *call) {
	v := c.r.URL.Query()
	date := v.Get("date")
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	if !space.ValidDailyDate(date) {
		writeError(c.w, http.StatusBadRequest, "invalid", "date must be a day like 2026-11-23")
		return
	}
	skip := 0
	if raw := v.Get("skip"); raw != "" {
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil || n < 0:
			writeError(c.w, http.StatusBadRequest, "invalid", "skip must be a number from 0 up")
			return
		case !space.ValidDailySkip(n):
			writeCoded(c.w, &codedError{code: codeSkipTooLarge, msg: "skip is too large"})
			return
		}
		skip = n
	}
	lang := ""
	if raw := v.Get("lang"); raw != "" {
		var ok bool
		if lang, ok = langtag.Canonical(raw); !ok {
			writeCoded(c.w, &codedError{code: space.CodeBadLanguage, msg: "lang is a language tag like zh-Hans, en or pt-BR"})
			return
		}
	}
	tz, ok := parseTZ(v)
	if !ok {
		writeError(c.w, http.StatusBadRequest, "invalid", "tz is minutes east of UTC, from -840 to 840")
		return
	}
	now := s.opt.DailyFetch.Now()
	day := maxAgeFor(date, tz, now)
	type none struct {
		Page   *dailyPageResp `json:"page"`
		Reason string         `json:"reason"`
	}
	var site siteDaily
	var err error
	if !s.single {
		if site, err = s.loadSite(c.r.Context()); err != nil {
			s.fail(c.w, err)
			return
		}
	}
	// 1. The space's own, unless the administrators do not let spaces have one.
	var pick *space.DailyPick
	var why space.DailyNone
	allowOwn := true
	if !s.single {
		allow, err := s.dailyAllowFor(c.r.Context(), c.user().TenantID)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		allowOwn = allow.Own
	}
	if allowOwn {
		pick, why, err = c.sp.Space.DailyText(c.sp.Info.ID, date, skip, lang)
		if err != nil {
			s.fail(c.w, err)
			return
		}
	}
	if pick != nil {
		s.writeDaily(c, struct {
			Page   *dailyPageResp `json:"page"`
			Count  int            `json:"count"`
			Origin string         `json:"origin"`
		}{&dailyPageResp{toSummary(pick.Page, c.sp.Space.TypeResolver()), pick.Body, pick.Truncated}, pick.Count, "space"}, day)
		return
	}
	if why == space.DailyNoMatch {
		s.writeDaily(c, none{nil, "no_match"}, 60)
		return
	}
	// 2 and 3. What the administrators gave (accounts mode only).
	if !s.single {
		q, reason, err := s.siteDaily(c, site, date, skip, lang, tz)
		switch {
		case errors.Is(err, dailyfetch.ErrRateLimited):
			c.w.Header().Set("Retry-After", "10")
			writeError(c.w, http.StatusTooManyRequests, "rate_limited", "another text was asked for too often; try again in a moment")
			return
		case err != nil:
			s.fail(c.w, err)
			return
		case q != nil:
			age := day
			if !q.expires.IsZero() {
				age = clampAge(int(q.expires.Sub(now) / time.Second))
			}
			s.writeDaily(c, struct {
				Page   *dailyPageResp `json:"page"`
				Quote  *dailyQuote    `json:"quote"`
				Origin string         `json:"origin"`
			}{nil, q, q.origin}, age)
			return
		case reason != "":
			s.writeDaily(c, none{nil, reason}, 60)
			return
		}
	}
	s.writeDaily(c, none{nil, "no_source"}, 60)
}

// writeDaily answers a request for the daily text: a browser may keep it for
// maxAge seconds (it is the person's own to keep: private), and a request that
// carries the ETag of what would be sent gets 304.
func (s *server) writeDaily(c *call, v any, maxAge int) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		s.fail(c.w, err)
		return
	}
	sum := sha256.Sum256(buf.Bytes())
	etag := `"` + hex.EncodeToString(sum[:12]) + `"`
	h := c.w.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", "private, max-age="+strconv.Itoa(maxAge))
	if match := c.r.Header.Get("If-None-Match"); match != "" {
		for _, m := range strings.Split(match, ",") {
			if strings.TrimPrefix(strings.TrimSpace(m), "W/") == etag {
				c.w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}
	h.Set("Content-Type", "application/json; charset=utf-8")
	c.w.WriteHeader(http.StatusOK)
	c.w.Write(buf.Bytes())
}

// --- favourites ---------------------------------------------------------------------

type favoriteResp struct {
	Kind    string `json:"kind"`
	Target  string `json:"target"`
	Name    string `json:"name"`
	Missing bool   `json:"missing"`
	// Path is a page's path or a folder's path.
	Path string `json:"path,omitempty"`
	// Of is the folder or tag a view belongs to, OfKind says which.
	Of      string `json:"of,omitempty"`
	OfKind  string `json:"ofKind,omitempty"`
	Created string `json:"created"`
}

type favoritesResp struct {
	Items []favoriteResp `json:"items"`
	Limit int            `json:"limit"`
}

// favoritesOf is the caller's favourites in the space, with what each points
// at now. An administrator who is not a member of the space has none: what a
// person kept in a space they left is kept, and shown again if they come back,
// and not shown to whoever else opens the space as them.
func (s *server) favoritesOf(c *call) (favoritesResp, error) {
	out := favoritesResp{Items: []favoriteResp{}, Limit: store.MaxFavorites}
	if c.sp.Info.ViaAdmin {
		return out, nil
	}
	s.settleFolderMoves(c)
	favs, err := store.ListFavorites(c.r.Context(), s.opt.Store.Q(), c.user().ID, c.sp.Info.ID)
	if err != nil {
		return out, err
	}
	refs := make([]space.FavRef, len(favs))
	for i, f := range favs {
		refs[i] = space.FavRef{Kind: f.Kind, Target: f.Target}
	}
	labels, err := c.sp.Space.FavoriteLabels(refs)
	if err != nil {
		return out, err
	}
	for i, f := range favs {
		l := labels[i]
		out.Items = append(out.Items, favoriteResp{Kind: f.Kind, Target: f.Target, Name: l.Name, Missing: l.Missing,
			Path: l.Path, Of: l.Of, OfKind: l.OfKind, Created: rfc3339(f.Created)})
	}
	return out, nil
}

func (s *server) getFavorites(c *call) {
	out, err := s.favoritesOf(c)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, out)
}

// membersOnly refuses a change of favourites to an administrator who is not a
// member: they have none.
func (s *server) membersOnly(c *call) bool {
	if c.sp.Info.ViaAdmin {
		writeError(c.w, http.StatusForbidden, "forbidden", "favourites belong to the members of a space; you are not one")
		return false
	}
	return true
}

type favRefBody struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

func (s *server) addFavorite(c *call) {
	var req favRefBody
	if !s.readJSON(c.w, c.r, &req, false) || !s.membersOnly(c) {
		return
	}
	ref, err := c.sp.Space.CheckFavorite(space.FavRef{Kind: req.Kind, Target: req.Target})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	err = s.opt.Store.Tx(c.r.Context(), func(q store.Q) error {
		if err := store.LockFavorites(c.r.Context(), q, c.user().ID); err != nil {
			return err
		}
		_, err := store.AddFavorite(c.r.Context(), q, c.user().ID, c.sp.Info.ID, store.Favorite{Kind: ref.Kind, Target: ref.Target}, time.Now())
		return err
	})
	if errors.Is(err, store.ErrFavoritesFull) {
		writeError(c.w, http.StatusBadRequest, "invalid", "a space can have at most "+strconv.Itoa(store.MaxFavorites)+" favourites of yours; remove one first")
		return
	}
	s.answerFavorites(c, err)
}

func (s *server) answerFavorites(c *call, err error) {
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out, err := s.favoritesOf(c)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, out)
}

// deleteFavorite takes one favourite off the list; one that is not there is
// not an error. What is asked for is named in the query: kind and target. The
// target is taken as it is stored, so one whose page or folder is gone can be
// removed too.
func (s *server) deleteFavorite(c *call) {
	v := c.r.URL.Query()
	kind, target := v.Get("kind"), v.Get("target")
	if (kind != store.FavPage && kind != store.FavFolder && kind != store.FavView) || target == "" {
		writeError(c.w, http.StatusBadRequest, "invalid", "kind (page, folder or view) and target say which favourite")
		return
	}
	if !s.membersOnly(c) {
		return
	}
	err := s.opt.Store.Tx(c.r.Context(), func(q store.Q) error {
		_, err := store.RemoveFavorite(c.r.Context(), q, c.user().ID, c.sp.Info.ID, kind, target)
		return err
	})
	s.answerFavorites(c, err)
}

// putFavoritesOrder sets the order of the favourites. A favourite the list
// leaves out keeps its place after those it names; a name that is not a
// favourite is ignored.
func (s *server) putFavoritesOrder(c *call) {
	var req struct {
		Items []favRefBody `json:"items"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	if req.Items == nil {
		writeError(c.w, http.StatusBadRequest, "invalid", "items is the list of favourites in their new order")
		return
	}
	if len(req.Items) > store.MaxFavorites {
		writeError(c.w, http.StatusBadRequest, "invalid", "at most "+strconv.Itoa(store.MaxFavorites)+" favourites")
		return
	}
	if !s.membersOnly(c) {
		return
	}
	items := make([]store.Favorite, len(req.Items))
	for i, it := range req.Items {
		if it.Kind != store.FavPage && it.Kind != store.FavFolder && it.Kind != store.FavView {
			writeError(c.w, http.StatusBadRequest, "invalid", "kind must be page, folder or view")
			return
		}
		items[i] = store.Favorite{Kind: it.Kind, Target: it.Target}
	}
	err := s.opt.Store.Tx(c.r.Context(), func(q store.Q) error {
		if err := store.LockFavorites(c.r.Context(), q, c.user().ID); err != nil {
			return err
		}
		return store.OrderFavorites(c.r.Context(), q, c.user().ID, c.sp.Info.ID, items)
	})
	s.answerFavorites(c, err)
}

// The favourites of a folder follow it when it is moved. The move is recorded
// in the state database before it is made (store.RecordFolderMove) and the
// record goes in the same transaction as the favourites are changed; a record
// left by a crash, a failure or a lost connection is settled the next time
// the favourites of the space are read (settleFolderMoves).

// pendingMoveAge is how long a recorded move whose folder is still where it
// was is taken to be under way, before it is taken as not done.
const pendingMoveAge = 10 * time.Minute

// recordFolderMove notes a move about to be made; ok is false (and the move is
// not to be made) when it cannot be noted.
func (s *server) recordFolderMove(c *call, from, to string) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.r.Context()), 5*time.Second)
	defer cancel()
	if err := store.RecordFolderMove(ctx, s.opt.Store.Q(), c.sp.Info.ID, from, to, time.Now()); err != nil {
		s.fail(c.w, err)
		return false
	}
	return true
}

// followFolder makes everybody's favourites of a folder that was moved follow
// it, and forgets the record of the move (to is the address the request gave,
// as recorded; at is where the folder is now, "" when it did not move). The
// other recorded moves of the space are settled with it, in the order they
// were recorded, so that a move of a folder another unsettled move led to carries
// those favourites on. It is not the request's context: the folder is moved
// whether or not the client is still there. What cannot be updated is logged,
// and the records stay to be settled later.
func (s *server) followFolder(c *call, from, to, at string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.r.Context()), 10*time.Second)
	defer cancel()
	var err error
	if at == "" {
		err = store.DoneFolderMove(ctx, s.opt.Store.Q(), c.sp.Info.ID, from, to)
	} else {
		err = s.settleMoves(ctx, c, &store.FolderMove{From: from, To: to}, at)
	}
	if err != nil {
		s.log.Error("favourites of a moved folder were not updated", "space", c.sp.Info.ID, "from", from, "to", at, "error", err)
	}
}

// settleFolderMoves settles the recorded moves of a space (see settleMoves)
// when there are any. Cost: one query by the index of the table, which is
// empty but for a crash or a failure.
func (s *server) settleFolderMoves(c *call) {
	ctx := context.WithoutCancel(c.r.Context())
	moves, err := store.PendingFolderMoves(ctx, s.opt.Store.Q(), c.sp.Info.ID)
	if err != nil || len(moves) == 0 {
		return
	}
	if err := s.settleMoves(ctx, c, nil, ""); err != nil {
		s.log.Error("folder moves' favourites were not settled", "space", c.sp.Info.ID, "error", err)
	}
}

// settleMoves deals with the recorded moves of a space, oldest first, in one
// transaction: all of them or none. done, when it is not nil, is a move known
// to be made, to at. Of the others, a move is taken as made when its folder
// is gone from the old place (whatever its age), when a later move put another
// folder there, when only the case of the name changed and the disk has the
// new one, or when a folder is at both places: the one at the old place is
// then a new one. Its favourites go
// to where the folder is now, or, when it is gone from there too, to its new
// place, from which a later move takes them on. A move old enough whose folder
// is at the old place and not at the new did not happen and is forgotten. So
// a chain of moves (A to B, then B to C) takes A's favourites to C, and a
// folder made where one was moved from keeps none of the old one's.
func (s *server) settleMoves(ctx context.Context, c *call, done *store.FolderMove, at string) error {
	spaceID := c.sp.Info.ID
	return s.opt.Store.Tx(ctx, func(q store.Q) error {
		moves, err := store.PendingFolderMoves(ctx, q, spaceID)
		if err != nil {
			return err
		}
		for i, m := range moves {
			later := moves[i+1:]
			var to string
			if done != nil && m.From == done.From && m.To == done.To {
				to = at
			} else {
				cf, fromThere, _ := c.sp.Space.CanonicalFolder(m.From)
				ct, toThere, _ := c.sp.Space.CanonicalFolder(m.To)
				refilled := false
				for _, l := range later {
					if strings.EqualFold(l.To, m.From) {
						refilled = true
					}
				}
				switch {
				case !fromThere || refilled:
					to = m.To
					if toThere {
						to = ct
					} else {
						// Spelled as a later move from there spells it.
						for _, l := range later {
							if strings.EqualFold(l.From, m.To) {
								to = l.From
								break
							}
						}
					}
				case toThere && cf == ct && cf != m.From: // only the case changed
					to = ct
				case toThere && cf != ct:
					// A folder at both places: a move under way has one at one
					// of them (it is a rename), and one onto a folder that is
					// there is not recorded (moveFolder), so this one was made
					// and the folder at the old place is a new one.
					to = ct
				case time.Since(m.Created) < pendingMoveAge:
					continue // maybe under way
				}
			}
			if to != "" && to != m.From {
				if err := store.MoveFolderFavorites(ctx, q, spaceID, m.From, to); err != nil {
					return err
				}
			}
			if err := store.DoneFolderMove(ctx, q, spaceID, m.From, m.To); err != nil {
				return err
			}
		}
		return nil
	})
}
