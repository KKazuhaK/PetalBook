package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// getDailySettings tells where the daily text of the space comes from.
func (s *server) getDailySettings(c *call) {
	writeJSON(c.w, http.StatusOK, struct {
		Daily *space.DailySource `json:"daily"`
	}{c.sp.Space.Daily()})
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
	var src *space.DailySource
	if err := json.Unmarshal(raw, &src); err != nil {
		writeError(c.w, http.StatusBadRequest, "invalid", "daily: "+err.Error())
		return
	}
	d, err := c.sp.Space.SetDaily(s.author(c), src)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Daily *space.DailySource `json:"daily"`
	}{d})
}

type dailyPageResp struct {
	summaryResp
	// Body is the page's Markdown, cut to space.DailyBodyLimit characters.
	Body          string `json:"body"`
	BodyTruncated bool   `json:"bodyTruncated"`
}

// getDaily answers with the daily text of a day.
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
		if err != nil || !space.ValidDailySkip(n) {
			writeError(c.w, http.StatusBadRequest, "invalid", "skip must be a number from 0 up")
			return
		}
		skip = n
	}
	pick, none, err := c.sp.Space.DailyText(c.sp.Info.ID, date, skip)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if pick == nil {
		reason := "no_source"
		if none == space.DailyNoMatch {
			reason = "no_match"
		}
		writeJSON(c.w, http.StatusOK, struct {
			Page   *dailyPageResp `json:"page"`
			Reason string         `json:"reason"`
		}{nil, reason})
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Page  *dailyPageResp `json:"page"`
		Count int            `json:"count"`
	}{&dailyPageResp{toSummary(pick.Page, c.sp.Space.TypeResolver()), pick.Body, pick.Truncated}, pick.Count})
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

// followFolder makes everybody's favourites of a folder that was moved follow
// it. The move is done by then; what cannot be updated is logged, and the
// favourites show as missing until someone takes them off.
func (s *server) followFolder(c *call, from, to string) {
	err := s.opt.Store.Tx(c.r.Context(), func(q store.Q) error {
		return store.MoveFolderFavorites(c.r.Context(), q, c.sp.Info.ID, from, to)
	})
	if err != nil {
		s.log.Error("favourites of a moved folder were not updated", "space", c.sp.Info.ID, "from", from, "to", to, "error", err)
	}
}
